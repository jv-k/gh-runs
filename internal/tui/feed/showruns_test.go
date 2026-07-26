package feed

import (
	"reflect"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// wfRun stamps a Run with the Workflow id it came from, the field a numeric selector
// matches on (ADR-0016) and the one that survives the Workflow's deletion.
func wfRun(id int64, workflow string, workflowID int64) domain.Run {
	r := mkRun(id, "cli", "cli", workflow, domain.StatusCompleted, domain.ConclusionSuccess, t0)
	r.WorkflowID = workflowID
	return r
}

// TestShowRunsNarrowsTheFeedToOneWorkflow pins the Feed's half of workflow-management R13
// and AC4: handed a filter query, the Feed narrows to it exactly as if it had been typed,
// so a deleted Workflow's Orphaned Runs are reached as a Feed filtered to that Workflow.
func TestShowRunsNarrowsTheFeedToOneWorkflow(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001), wfRun(2, "Old Deploy", 9004))

	m = m.Update2(ShowRuns("workflow:9004"))

	if got, want := m.displayedOrder(), []int64{2}; !reflect.DeepEqual(got, want) {
		t.Errorf("displayed Runs = %v, want %v: the Feed must show the named Workflow's Runs alone (R13)", got, want)
	}
	if m.filter.Workflow != "9004" {
		t.Errorf("held filter Workflow = %q, want %q", m.filter.Workflow, "9004")
	}
}

// TestShowRunsFillsTheFilterInput pins that the arriving filter and the filter input agree:
// the query lands in the input the operator edits, so the Feed states what it is narrowed to
// and the next keystroke into that input starts from the applied filter rather than wiping
// it (live-run-feed R23).
func TestShowRunsFillsTheFilterInput(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))

	m = m.Update2(ShowRuns("workflow:9004"))

	if got := m.filterInput.Value(); got != "workflow:9004" {
		t.Errorf("filter input = %q, want %q", got, "workflow:9004")
	}
}

// TestShowRunsPublishesToTheScheduler pins R22 for the arriving filter: it is published to
// the scheduler exactly as an accepted one is, so the poll narrows server-side where it can
// rather than leaving the Feed to search the ~30-Run held window alone.
func TestShowRunsPublishesToTheScheduler(t *testing.T) {
	var got []filter.Filter
	m := feedWithFilterSink(100, 12, func(f filter.Filter) { got = append(got, f) })
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))

	_, cmd := m.Update(ShowRuns("workflow:9004"))
	runLeafCmds(cmd)

	if len(got) == 0 {
		t.Fatal("an arriving filter published nothing to the scheduler (R22)")
	}
	if last := got[len(got)-1]; last.Workflow != "9004" {
		t.Fatalf("published filter = %+v, want Workflow=9004 (R22)", last)
	}
}

// TestShowRunsIgnoresAnUnparseableQuery pins that a query the Feed's own parser rejects
// leaves the last good filter standing, matching what a half-typed filter does, rather than
// blanking the list.
func TestShowRunsIgnoresAnUnparseableQuery(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001), wfRun(2, "Old Deploy", 9004))
	m = m.Update2(ShowRuns("workflow:9004"))

	m = m.Update2(ShowRuns("status:not-a-status"))

	if m.filter.Workflow != "9004" {
		t.Errorf("held filter Workflow = %q after an unparseable query, want the last good %q", m.filter.Workflow, "9004")
	}
}
