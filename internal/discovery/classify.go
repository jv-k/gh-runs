package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// apiRunsPage is the fragment of an actions/runs listing discovery reads. It
// decodes workflow_runs and deliberately ignores total_count for classification:
// a filtered listing inflates total_count past the silent 1,000 cap (R4, AC5,
// ADR-0005), so a classification drawn from it would inherit that lie. The probe
// is unfiltered so total_count would in fact be honest here, but reading the array
// rather than the count is what makes the rule hold whatever the URL.
type apiRunsPage struct {
	TotalCount   int          `json:"total_count"`
	WorkflowRuns []domain.Run `json:"workflow_runs"`
}

// probeResult is one classification: the identity, whether it has Runs, whether
// its response carried an ETag (which the two-tier refresh reads, R12), and the
// Runs themselves so the fast path can paint them (R14).
type probeResult struct {
	id      domain.RepoID
	hasRuns bool
	hasETag bool
	runs    []domain.Run
	err     error
}

// probe issues one unfiltered Run-listing request for a repository and classifies
// it as having Runs if and only if the response body carries at least one Run
// (R3). The request carries no query parameter at all, so it is unfiltered by
// construction (R4, AC5) and it can never be a code-search request (R6, AC4):
// discovery reads Runs, never Workflow files, so a repository whose Workflow was
// deleted but whose Run history survives still classifies as having Runs. It goes
// through ghclient.Request, so the governor accounts it (R17) and the store
// revalidates it (R12): a re-probe that has not changed answers 304, reconstituted
// to a 200 below this call, and costs no primary allowance.
func (d *Discovery) probe(ctx context.Context, id domain.RepoID) probeResult {
	if err := ctx.Err(); err != nil {
		return probeResult{id: id, err: err}
	}
	path := fmt.Sprintf("repos/%s/%s/actions/runs", id.Owner, id.Name)
	resp, err := d.opts.Client.Request(http.MethodGet, path, nil)
	if err != nil {
		return probeResult{id: id, err: fmt.Errorf("probe %s: %w", id, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return probeResult{id: id, err: fmt.Errorf("probe %s: status %d", id, resp.StatusCode)}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return probeResult{id: id, err: fmt.Errorf("probe %s: read body: %w", id, err)}
	}
	var page apiRunsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return probeResult{id: id, err: fmt.Errorf("probe %s: decode: %w", id, err)}
	}
	return probeResult{
		id:      id,
		hasRuns: len(page.WorkflowRuns) > 0,
		hasETag: resp.Header.Get("ETag") != "",
		runs:    page.WorkflowRuns,
	}
}

// Pass runs a full discovery pass: enumerate the account (R1), probe every
// repository with bounded concurrency (R16), classify each as it returns and emit
// it (R15), then persist the whole set (R19). It is the initial discovery and the
// on-demand full refresh (R11): a manual refresh runs the full pass. emit may be
// nil; when set it is called once per classified repository as the probe returns,
// so a consumer fills in progressively rather than waiting on the last probe
// (AC12).
//
// The pass exercises exactly the reference cost model: two enumeration requests
// plus one probe per repository, and no request to learn capability (AC1, AC3,
// AC7). Capability rides along with enumeration, so a repository is classified
// and gated from a single probe.
func (d *Discovery) Pass(ctx context.Context, emit func(Record)) error {
	repos, err := d.enumerate(ctx)
	if err != nil {
		return err
	}
	d.classifyAll(ctx, repos, emit)
	d.persist()
	return nil
}

// classifyAll probes every enumerated repository, bounded at probeConcurrency
// requests in flight (R16, AC13), and folds each result into the set as it
// returns (R15). A worker pool of fixed size is the bound: at no instant are more
// than probeConcurrency probes outstanding, and no user-facing setting changes
// that (AC13). The pool stops launching new probes once the governor reports
// exhaustion, so a burst that meets a rate limit does not keep firing into it
// (R17, ADR-0018); probes already in flight complete and emit.
func (d *Discovery) classifyAll(ctx context.Context, repos []enumerated, emit func(Record)) {
	byID := make(map[string]apiRepo, len(repos))
	for _, r := range repos {
		byID[r.id.String()] = r.repo
	}

	jobs := make(chan domain.RepoID)
	var wg sync.WaitGroup
	var emitMu sync.Mutex

	worker := func() {
		defer wg.Done()
		for id := range jobs {
			res := d.probe(ctx, id)
			if res.err != nil {
				// A probe failure classifies nothing: the repository keeps whatever a
				// prior pass or a reload recorded, and the next re-probe retries it.
				continue
			}
			rec := recordFrom(res.id, byID[res.id.String()], res.hasRuns)
			d.putProbed(rec, d.opts.Clock.Now(), res.hasETag)
			if emit != nil {
				emitMu.Lock()
				emit(rec)
				emitMu.Unlock()
			}
		}
	}

	n := probeConcurrency
	if len(repos) < n {
		n = len(repos)
	}
	wg.Add(n)
	for range n {
		go worker()
	}

	for _, r := range repos {
		if ctx.Err() != nil || d.exhausted() {
			break
		}
		jobs <- r.id
	}
	close(jobs)
	wg.Wait()
}

// exhausted reports whether the governor has published exhaustion, so the burst
// stops launching new probes (R17, ADR-0018). A nil Budget (the orchestration
// fakes) never reports exhaustion, so the check is inert there.
func (d *Discovery) exhausted() bool {
	if d.opts.Budget == nil {
		return false
	}
	return d.opts.Budget.Readout().Exhausted
}

// persist writes the whole classified set to the store as one document (R19,
// local-store R2). It is best-effort by the store's own contract: a degraded
// reader writes nothing and a write failure costs a future cold start its speed
// and nothing else (local-store R11, R21). The document is host-qualified in every
// record (AC14).
func (d *Discovery) persist() {
	if d.opts.Store == nil {
		return
	}
	d.mu.Lock()
	records := make([]Record, 0, len(d.records))
	for _, r := range d.records {
		// A record whose capability is not yet Known is a fast-path placeholder, not
		// a result: it carries no recorded capability to persist (local-store R2), and
		// leaving it out keeps a half-finished launch from persisting a repository the
		// next session would admit before adoption confirmed it (R22). Its ETags
		// persist regardless, through the store's entry cache.
		if !r.Known {
			continue
		}
		records = append(records, r)
	}
	d.mu.Unlock()
	d.opts.Store.SaveDoc(docName, records)
}

// Reload loads the persisted classification and capability from the store, so a
// cold start paints the poll set before any probe and then revalidates for free
// (R19, local-store R2, AC7). It issues no request. A missing, corrupt or
// wrong-schema document reads as absent and leaves the set empty, which a
// subsequent pass rebuilds (local-store R11, R13). It reports how many records it
// admitted, so a caller can tell a warm start from a cold one.
func (d *Discovery) Reload() int {
	if d.opts.Store == nil {
		return 0
	}
	var records []Record
	if !d.opts.Store.LoadDoc(docName, &records) {
		return 0
	}
	n := 0
	for _, r := range records {
		id, err := newRepoID(r.Host, r.Owner, r.Name)
		if err != nil {
			// A persisted key without a github.com host is rejected rather than
			// trusted (AC14). It contributes no entry.
			continue
		}
		if r.Adopted {
			// R22: an adopted repository's record persists so revalidation stays
			// free, but its membership does not. Only a launch inside it re-admits
			// it, via the fast path, so a session launched elsewhere never sees it.
			continue
		}
		r.Host, r.Owner, r.Name = id.Host, id.Owner, id.Name
		d.put(r)
		n++
	}
	return n
}
