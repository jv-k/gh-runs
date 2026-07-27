package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
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

	// definitive reports that this failure is evidence the repository is gone or
	// unreachable for good, which is the only thing R23 counts. It is meaningful only
	// alongside a non-nil err. A transport error, a 5xx, a timeout, a decode failure
	// and a rate-limited 403 all leave it false, so none of them can retire a
	// repository and none of them can postpone a retirement either.
	definitive bool
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
		// Every non-2xx arrives here, as an *api.HTTPError with a nil response, so
		// this is the branch R23's verdict is taken on and the status check below is
		// not (ADR-0012).
		return probeResult{
			id:         id,
			err:        fmt.Errorf("probe %s: %w", id, err),
			definitive: definitiveFailure(err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	// Defence only. A non-2xx never reaches here, and a 2xx that is not 200 (a 204,
	// say) is not evidence about the repository, so it is not definitive.
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

// definitiveFailure reports whether a failed probe is evidence R23 may count: a 404,
// or a 403 the governor reports as not rate limiting.
//
// It reads the error rather than a response because go-gh's RESTClient turns every
// non-2xx into an *api.HTTPError carrying a copy of the response headers and returns
// a nil response (ADR-0012). The status and the governor's stamp are both on the
// error, and nowhere else, so the errors.As read here is the same one the scheduler's
// rate-limit check and the CLI's exit-code mapping already make.
//
// An error that is not an *api.HTTPError carries no status and no headers: a
// transport error, a timeout, a decode failure. None of them is evidence about the
// repository, so none of them is definitive, and R23 requires each to leave the count
// untouched rather than reset it.
//
// The 403 is read through the governor's stamp and never off the status code. A
// secondary-limit 403 arrives with a healthy x-ratelimit-remaining, so the status
// cannot tell rate limiting from authorization, and only the governor has looked at
// the body. The check requires the stamp to be present and to say false, rather than
// merely not to say true, because RateLimitedHeaders reports false for headers that
// were never classified at all and that reading would retire a repository on a 403
// nobody looked at. Unclassified is not definitive, which is the safe direction.
//
// On a fine-grained PAT the 403 signal does not fire at all. GitHub answers that
// token class with the general fine-grained-permissions page, which names no
// resource, so the governor classifies it as rate limiting and R23 degrades to 404
// alone. That is correct rather than a gap: a deleted or private repository answers
// 404 and retires on every token class (ADR-0020).
func definitiveFailure(err error) bool {
	var he *api.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.StatusCode {
	case http.StatusNotFound:
		return true
	case http.StatusForbidden:
		return governor.ClassifiedHeaders(he.Headers) && !governor.RateLimitedHeaders(he.Headers)
	default:
		return false
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

// classifyAll probes every enumerated repository and folds each result into the
// set as it returns (R15), classifying it from the response body and pairing it
// with the capability that rode along with enumeration at no extra request (R7).
// The fan-out and its concurrency shape live in fanOut; classifyAll's only job is
// to map a probe result to a freshly classified Record.
func (d *Discovery) classifyAll(ctx context.Context, repos []enumerated, emit func(Record)) {
	byID := make(map[string]apiRepo, len(repos))
	ids := make([]domain.RepoID, 0, len(repos))
	for _, r := range repos {
		byID[r.id.String()] = r.repo
		ids = append(ids, r.id)
	}
	d.fanOut(ctx, ids, func(res probeResult) Record {
		return recordFrom(res.id, byID[res.id.String()], res.hasRuns)
	}, emit)
}

// fanOut probes every id concurrently, folds each successful result into the set as
// it returns, and emits it. It spawns one goroutine per id and relies on the
// transport-chain limiter (ADR-0018) to bound requests on the wire at the
// process-wide constant, so the fan-out holds no bound of its own: the goroutine
// count is bounded naturally by the probe-set size (~163 at a full pass, ~26 at a
// re-probe) and the wire by the limiter innermost in the chain. This is the shape
// classifyAll and Reprobe share, so it lives once: build is the only thing that
// differs between them, mapping a successful probe to the Record to store (a fresh
// classification for a pass, the recorded capability carried forward for a
// re-probe).
//
// It stops launching probes once the context is cancelled or the governor reports
// exhaustion (R17, ADR-0018): a burst that meets a rate limit does not keep firing
// into a limit that names the whole token, and probes already in flight complete
// and emit. emit is serialised within one fan-out, so a caller must not run a Pass
// and a Reprobe concurrently unless its own emit is safe for concurrent calls.
func (d *Discovery) fanOut(ctx context.Context, ids []domain.RepoID, build func(probeResult) Record, emit func(Record)) {
	var wg sync.WaitGroup
	var emitMu sync.Mutex
	for _, id := range ids {
		if ctx.Err() != nil || d.exhausted() {
			break
		}
		if d.excluded(id) {
			// settings R7: an excluded repository is removed from discovery and all
			// polling, so it is never probed. fanOut is the one door every probe but the
			// fast path's and adoption's passes through, so the rule holds for a pass and
			// a re-probe alike (AC5).
			continue
		}
		wg.Add(1)
		go func(id domain.RepoID) {
			defer wg.Done()
			if ctx.Err() != nil || d.exhausted() {
				return
			}
			res := d.probe(ctx, id)
			if res.err != nil {
				// A probe failure classifies nothing: the repository keeps whatever a
				// prior pass or a reload recorded, and the next re-probe retries it.
				// R23 is the one thing a failure does move, and only a definitive one:
				// the count lives on the record, so it is taken here on the failure
				// path rather than in putProbed, which this return never reaches.
				if res.definitive {
					d.countDefinitiveFailure(id)
				}
				return
			}
			rec := build(res)
			d.putProbed(rec, d.opts.Clock.Now(), res.hasETag)
			if emit != nil {
				emitMu.Lock()
				emit(rec)
				emitMu.Unlock()
			}
		}(id)
	}
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
	// The exclude list that shaped this document is written with it, so a later session
	// can tell whether the omissions in it are still the ones the operator asked for
	// (settings R7). Without it, removing an exclude line would never bring the
	// repository back on a warm cache.
	d.opts.Store.SaveDoc(excludeDocName, d.excludeFingerprint())
}

// Reload loads the persisted classification and capability from the store, so a
// cold start paints the poll set before any probe and then revalidates for free
// (R19, local-store R2, AC7). It issues no request. A missing, corrupt or
// wrong-schema document reads as absent and leaves the set empty, which a
// subsequent pass rebuilds (local-store R11, R13). It reports how many records it
// admitted, so a caller can tell a warm start from a cold one.
//
// A document written under a different exclude list is stale and reads as absent
// (settings R7). Every caller spends a Pass when Reload reports nothing, so this is
// what makes an exclusion reversible: a Pass under an exclusion writes a document that
// omits the repository, and without the check, deleting the config line would leave a
// warm cache answering non-zero forever and the repository gone until the cache
// directory was cleared. The check fires on a membership change and on nothing else,
// so the ordinary launch stays warm and costs no re-enumeration.
func (d *Discovery) Reload() int {
	if d.opts.Store == nil {
		return 0
	}
	if !d.excludeFingerprintMatches() {
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
		if d.excluded(id) {
			// settings R7, defence in depth. The staleness check above already sends a
			// session whose exclude list changed down the cold path, so a document
			// reaching here should hold no excluded repository. It can hold one anyway
			// when the marker write was dropped and the record write was not, which the
			// store's best-effort contract permits (local-store R11, R21), and this is
			// what keeps the exclusion holding in that case. put would refuse the record
			// regardless; refusing it here also keeps the returned count honest, and a
			// caller reads that count to tell a warm start from a cold one.
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

// excludeFingerprintMatches reports whether the persisted results were shaped by the
// exclude list this session holds. An absent marker matches an empty list, so a store
// written before this key existed keeps loading warm and nobody pays a re-enumeration
// for the upgrade. Any other disagreement makes the document stale.
func (d *Discovery) excludeFingerprintMatches() bool {
	var persisted []string
	if !d.opts.Store.LoadDoc(excludeDocName, &persisted) {
		return len(d.exclude) == 0
	}
	return slices.Equal(persisted, d.excludeFingerprint())
}
