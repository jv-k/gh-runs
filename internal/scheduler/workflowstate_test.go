package scheduler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// legacy is the repository the workflow_state cassette answers for: two Runs, one on a
// deleted Workflow and one on an active one.
var legacy = domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "legacy"}

// fakeLister is a WorkflowLister that counts its calls per repository, so a test can
// prove the list is read once and the join is served from memory after that. The
// property under test is the join and its bound, which is orchestration rather than
// the wire, so a fake stands in for a cassette here exactly as it does for the poll
// set and the Budget. The wire shape of the Workflow list is pinned where it is
// built, and the whole chain is pinned by the end-to-end test at the repository root.
type fakeLister struct {
	mu   sync.Mutex
	ws   []domain.Workflow
	err  error
	errN int // fail the first errN calls, then serve ws
	n    map[string]int
}

func (f *fakeLister) list(id domain.RepoID) ([]domain.Workflow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = make(map[string]int)
	}
	f.n[id.String()]++
	if f.err != nil && f.n[id.String()] <= f.errN {
		return nil, f.err
	}
	return f.ws, nil
}

func (f *fakeLister) calls(id domain.RepoID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[id.String()]
}

// legacyWorkflows is the repository's Workflow list: 9001 deleted, 9002 active.
func legacyWorkflows() []domain.Workflow {
	return []domain.Workflow{
		{ID: 9001, Name: "Old Pipeline", Path: ".github/workflows/old.yml", State: domain.StateDeleted},
		{ID: 9002, Name: "CI", Path: ".github/workflows/ci.yml", State: domain.StateActive},
	}
}

// byRunID indexes an Update's Runs so an assertion names the Run it means.
func byRunID(u Update) map[int64]domain.Run {
	out := make(map[int64]domain.Run, len(u.Runs))
	for _, r := range u.Runs {
		out[r.ID] = r
	}
	return out
}

// TestPolledRunsCarryTheirWorkflowState pins the join run-detail R8's marker reads. The
// run object carries no Workflow state key, so a Run whose Workflow was deleted is
// indistinguishable from one whose Workflow still exists until the fan-out joins
// WorkflowID against the repository's Workflow list, where it already holds both sides
// (ADR-0014). Without this the marker can never light up, whatever the pane renders.
func TestPolledRunsCarryTheirWorkflowState(t *testing.T) {
	lister := &fakeLister{ws: legacyWorkflows()}
	h := newHarness(t, harnessConfig{
		base:      openCassette(t, "workflow_state"),
		pollSet:   []domain.RepoID{legacy},
		workflows: lister.list,
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)

	runs := byRunID(h.updates()[0])
	if len(runs) != 2 {
		t.Fatalf("the cold-start Update carried %d Runs, want 2", len(runs))
	}
	if got := runs[1].WorkflowState; got != domain.StateDeleted {
		t.Errorf("the Run on the deleted Workflow carried State %q, want %q (R8: it is an Orphaned Run)", got, domain.StateDeleted)
	}
	if got := runs[2].WorkflowState; got != domain.StateActive {
		t.Errorf("the Run on the live Workflow carried State %q, want %q", got, domain.StateActive)
	}
}

// TestWorkflowListIsReadOncePerRepository pins the cost. A Workflow's State changes only
// when a person edits, disables or deletes it, so reading the list once per repository
// and joining from memory keeps the marker's price at one request per repository for the
// process, rather than one per poll of every repository in the set.
func TestWorkflowListIsReadOncePerRepository(t *testing.T) {
	lister := &fakeLister{ws: legacyWorkflows()}
	h := newHarness(t, harnessConfig{
		base:      openCassette(t, "workflow_state"),
		pollSet:   []domain.RepoID{legacy},
		workflows: lister.list,
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)
	h.waitSettle(t, slowTarget)

	// The second poll answers a changed 200, so its Runs are stamped again, from the
	// list already held.
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)

	if got := lister.calls(legacy); got != 1 {
		t.Errorf("the Workflow list was read %d times across two polls, want 1", got)
	}
	second := byRunID(h.updates()[1])
	if len(second) != 3 {
		t.Fatalf("the second Update carried %d Runs, want 3", len(second))
	}
	if got := second[3].WorkflowState; got != domain.StateDeleted {
		t.Errorf("a Run that arrived after the list was read carried State %q, want %q", got, domain.StateDeleted)
	}
}

// TestUnresolvedWorkflowStateIsEmpty pins the honest failure. A Workflow the list does
// not name, and a repository with no list wired at all, both leave the State empty,
// which reads as not-deleted. Guessing the other way would mark every Run of every
// repository as Orphaned and offer no re-run anywhere (run-detail R18).
func TestUnresolvedWorkflowStateIsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lister WorkflowLister
	}{
		{"no lister wired", nil},
		{"the Workflow is not in the list", (&fakeLister{ws: []domain.Workflow{{ID: 4242, State: domain.StateActive}}}).list},
		{"the list could not be read", (&fakeLister{err: errors.New("HTTP 403: Forbidden"), errN: 99}).list},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessConfig{
				base:      openCassette(t, "workflow_state"),
				pollSet:   []domain.RepoID{legacy},
				workflows: tc.lister,
			})
			h.start(t)
			h.waitPolls(t, 1)
			h.waitUpdates(t, 1)

			for _, r := range h.updates()[0].Runs {
				if r.WorkflowState != "" {
					t.Errorf("Run %d carried State %q, want the empty unresolved State", r.ID, r.WorkflowState)
				}
			}
		})
	}
}

// TestWorkflowListIsReadOnceWhateverTheOutcome pins the bound on the failure path, which is
// the one that has no natural end. A repository whose Workflow list answers 403 for a
// fine-grained PAT would otherwise be re-read on every changed poll for the whole session,
// silently, with no backoff and no cap, and at the fast tier that is a request every three
// seconds for a listing that will not answer.
//
// The read is therefore attempted once per repository per process whatever it returns. The
// cost is that a repository whose list failed keeps its marker dark until a restart, which
// is the state the marker was in before it was wired at all, and never a false one.
func TestWorkflowListIsReadOnceWhateverTheOutcome(t *testing.T) {
	lister := &fakeLister{ws: legacyWorkflows(), err: errors.New("HTTP 403: Forbidden"), errN: 1}
	h := newHarness(t, harnessConfig{
		base:      openCassette(t, "workflow_state"),
		pollSet:   []domain.RepoID{legacy},
		workflows: lister.list,
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)
	h.waitSettle(t, slowTarget)

	if got := byRunID(h.updates()[0])[1].WorkflowState; got != "" {
		t.Fatalf("a Run stamped %q from a list that failed to read, want the empty State", got)
	}

	// A second changed poll, which the lister would now answer. It is not asked.
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)

	if got := lister.calls(legacy); got != 1 {
		t.Errorf("the Workflow list was read %d times after a failure, want 1 (an unbounded retry)", got)
	}
}

// TestNoRunsReadsNoWorkflowList pins the other half of the cost bound. A repository whose
// listing came back empty has nothing to stamp, so the join is not worth a request.
func TestNoRunsReadsNoWorkflowList(t *testing.T) {
	lister := &fakeLister{ws: legacyWorkflows()}
	empty := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "empty"}
	h := newHarness(t, harnessConfig{
		base:      emptyRunsRT{},
		pollSet:   []domain.RepoID{empty},
		workflows: lister.list,
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)

	if got := lister.calls(empty); got != 0 {
		t.Errorf("a repository with no Runs read its Workflow list %d times, want 0", got)
	}
}

// wireWorkflowLister reads a repository's Workflow listing over the harness's client, the
// same request main.go's lister makes through the same chain. A test using it counts the
// join's real requests at countingRT rather than a fake's calls, which is the only way the
// engine's shipped request profile is measured at all.
func wireWorkflowLister(c *ghclient.Client) WorkflowLister {
	return func(id domain.RepoID) ([]domain.Workflow, error) {
		resp, err := c.Request(http.MethodGet, "repos/"+id.Owner+"/"+id.Name+"/actions/workflows?per_page=100", nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, statusError(resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var page struct {
			Workflows []domain.Workflow `json:"workflows"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		return page.Workflows, nil
	}
}

// TestTheJoinCostsOneRequestPerRepository is the cost claim measured at the wire, with the
// lister wired as it ships rather than faked. polling-scheduler R11 projects one request per
// repository per interval, so a second request class in the poll path is exactly the kind of
// thing that has to be counted rather than asserted.
func TestTheJoinCostsOneRequestPerRepository(t *testing.T) {
	h := newHarness(t, harnessConfig{
		base:          openCassette(t, "workflow_state"),
		pollSet:       []domain.RepoID{legacy},
		wireWorkflows: true,
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)
	h.waitSettle(t, slowTarget)

	if got := h.counting.countPath("/actions/runs"); got != 1 {
		t.Errorf("cold-start Run listings = %d, want 1", got)
	}
	if got := h.counting.countPath("/actions/workflows"); got != 1 {
		t.Errorf("cold-start Workflow listings = %d, want 1", got)
	}
	if got := byRunID(h.updates()[0])[1].WorkflowState; got != domain.StateDeleted {
		t.Fatalf("the wired lister did not resolve the join: State %q, want %q", got, domain.StateDeleted)
	}

	// A second changed poll costs one Run listing and no second Workflow listing. The
	// cassette would answer another, so this counts the memo and not the fixture.
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)

	if got := h.counting.countPath("/actions/runs"); got != 2 {
		t.Errorf("Run listings after two polls = %d, want 2 (one per repository per interval)", got)
	}
	if got := h.counting.countPath("/actions/workflows"); got != 1 {
		t.Errorf("Workflow listings after two polls = %d, want 1 (once per repository, not per poll)", got)
	}
}

// TestExhaustionStopsTheJoinToo re-checks the assertion the wired feature could have voided.
// orchestration_test's exhaustion case counts every wire request while paused and wants
// zero, and it runs with no lister. The join reads from inside the poll goroutine, and a
// paused loop spawns none, so the count stays zero with the feature on. Asserting it here
// is what keeps that true if the read ever moves.
func TestExhaustionStopsTheJoinToo(t *testing.T) {
	resume := t0.Add(15 * time.Minute)
	budget := &fakeBudget{}
	budget.set(governor.Readout{Exhausted: true, Reset: resume})
	h := newHarness(t, harnessConfig{
		base:          openCassette(t, "workflow_state"),
		pollSet:       []domain.RepoID{legacy},
		budget:        budget,
		wireWorkflows: true,
	})
	h.start(t)
	h.waitSettle(t, resume.Sub(t0))

	if got := h.counting.count(); got != 0 {
		t.Errorf("wire requests while exhausted = %d, want 0 (the join must not poll past a pause)", got)
	}
}
