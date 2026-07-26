package scheduler

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestWorkflowFilterPollsTheWorkflowEndpoint is live-run-feed R22 at the wire for the
// Workflow axis, which has no query parameter: filtering by Workflow server-side is a
// different endpoint carrying the same parameters (ADR-0016), so a filter naming a Workflow
// must poll /actions/workflows/{id}/runs and not the repository listing. Asserted at the
// counting transport, the seam cli-surface AC4 uses, because holding the filter proves
// nothing: the whole point is which resource was fetched.
//
// Without this the Workflows tab's navigation to a deleted Workflow's Orphaned Runs lands on
// the newest page of the repository's Runs, which by definition contains none of them, so the
// operator arrives at an empty Feed (workflow-management R13, AC4).
func TestWorkflowFilterPollsTheWorkflowEndpoint(t *testing.T) {
	a := gh("acme", "legacy")
	h := newHarness(t, harnessConfig{base: openCassette(t, "workflow_endpoint"), pollSet: []domain.RepoID{a}})
	h.s.SetFilter(filter.Filter{Workflow: "9004"})
	h.start(t)

	h.waitUpdates(t, 1)

	if n := h.counting.countPath("/actions/workflows/9004/runs"); n != 1 {
		t.Fatalf("polls of the Workflow's Run listing = %d, want 1 (R22, ADR-0016: the Workflow axis is an endpoint)", n)
	}
	if n := h.counting.countPath("/actions/runs"); n != 0 {
		t.Errorf("polls of the repository listing = %d, want 0 while a Workflow filter is active", n)
	}
	u := h.updates()[0]
	if len(u.Runs) != 2 {
		t.Fatalf("Update carried %d Runs, want the 2 the Workflow's listing served", len(u.Runs))
	}
	for _, r := range u.Runs {
		if r.WorkflowID != 9004 {
			t.Errorf("Update carried a Run of Workflow %d, want 9004 alone", r.WorkflowID)
		}
	}
	if !u.Filtered {
		t.Error("Update.Filtered = false, want true: a Workflow listing is a filtered listing, whose total_count R24 reads as a claimed count")
	}
	if u.ClaimedTotal != 4106 {
		t.Errorf("Update.ClaimedTotal = %d, want 4106 (R24: the response's total_count)", u.ClaimedTotal)
	}
}

// TestWorkflowSelectorResolvesByName pins ADR-0016's resolution rule: the selector is raw, a
// name or a filename or a numeric id, and the engine resolves it live from the Workflow lists
// it already keeps for the name join. A typed w:CI must reach the same endpoint a numeric
// selector does, or the axis would be server-side for one spelling and client-side for the
// other.
func TestWorkflowSelectorResolvesByName(t *testing.T) {
	a := gh("acme", "legacy")
	h := newHarness(t, harnessConfig{
		base:          openCassette(t, "workflow_endpoint_by_name"),
		pollSet:       []domain.RepoID{a},
		wireWorkflows: true,
	})
	h.s.SetFilter(filter.Filter{Workflow: "Old Pipeline"})
	h.start(t)

	h.waitUpdates(t, 1)

	if n := h.counting.countPath("/actions/workflows/9004/runs"); n != 1 {
		t.Fatalf("polls of the named Workflow's listing = %d, want 1 (ADR-0016: the engine resolves the selector)", n)
	}
	if n := h.counting.countPath("/actions/workflows?"); n != 1 {
		t.Errorf("Workflow-list reads = %d, want exactly 1: the resolution rides the one read per repository the join already pays for", n)
	}
}

// TestUnresolvableWorkflowSelectorPollsTheRepositoryListing pins the fallback: a selector
// naming no Workflow in this repository leaves the poll on the ordinary listing, where the
// client-side Match evicts every Run. A merged Feed filtered by a name spans repositories
// that do not all have that Workflow, and a repository without it must not stop polling.
func TestUnresolvableWorkflowSelectorPollsTheRepositoryListing(t *testing.T) {
	a := gh("acme", "legacy")
	h := newHarness(t, harnessConfig{
		base:          openCassette(t, "workflow_endpoint_by_name"),
		pollSet:       []domain.RepoID{a},
		wireWorkflows: true,
	})
	h.s.SetFilter(filter.Filter{Workflow: "Nothing Named This"})
	h.start(t)

	h.waitUpdates(t, 1)

	if n := h.counting.countPath("/actions/runs"); n != 1 {
		t.Fatalf("polls of the repository listing = %d, want 1 (an unresolvable selector falls back)", n)
	}
}
