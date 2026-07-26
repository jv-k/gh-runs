package feed

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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

	m = m.Update2(ShowRuns(filter.Filter{Workflow: "9004"}))

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

	m = m.Update2(ShowRuns(filter.Filter{Workflow: "9004"}))

	if got := m.filterInput.Value(); got != "workflow:9004" {
		t.Errorf("filter input = %q, want %q", got, "workflow:9004")
	}
}

// TestShowRunsPublishesToTheScheduler pins R22 for the arriving filter: it is published to
// the scheduler exactly as an accepted one is, so the poll itself narrows. For the Workflow
// axis that means polling the Workflow's own Run listing, which is what makes a deleted
// Workflow's Orphaned Runs reachable at all: they are older than the page of the repository
// listing the Feed holds, so a client-side narrowing would find none of them.
func TestShowRunsPublishesToTheScheduler(t *testing.T) {
	var got []filter.Filter
	m := feedWithFilterSink(100, 12, func(f filter.Filter) { got = append(got, f) })
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))

	_, cmd := m.Update(ShowRuns(filter.Filter{Workflow: "9004"}))
	runLeafCmds(cmd)

	if len(got) == 0 {
		t.Fatal("an arriving filter published nothing to the scheduler (R22)")
	}
	if last := got[len(got)-1]; last.Workflow != "9004" {
		t.Fatalf("published filter = %+v, want Workflow=9004 (R22)", last)
	}
}

// TestShowRunsStatesTheFilterOnScreen pins that a filter the operator did not type is
// visible: R13 asks for a Feed "filtered to that Workflow", and a narrowing nobody can see
// is indistinguishable from a Workflow with no Runs, or from a broken tool. The line states
// the filter and the key that clears it, and it stands whether or not the input is focused.
func TestShowRunsStatesTheFilterOnScreen(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001), wfRun(2, "Old Deploy", 9004))

	m = m.Update2(ShowRuns(filter.Filter{Workflow: "9004"}))

	view := m.View()
	if !strings.Contains(view, "workflow:9004") {
		t.Errorf("the frame does not state the filter it is showing (R13):\n%s", view)
	}
}

// TestNarrowedEmptyListSaysSo pins the empty case, which is the ordinary one when navigating
// to a deleted Workflow whose Runs are all older than the page the Feed holds: a blank area
// under a header says nothing at all. The frame must distinguish "nothing matches this
// filter" from "nothing has been fetched yet".
func TestNarrowedEmptyListSaysSo(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))

	m = m.Update2(ShowRuns(filter.Filter{Workflow: "4242"}))

	view := m.View()
	if !strings.Contains(view, "No Runs match") {
		t.Errorf("a filter matching nothing painted no explanation:\n%s", view)
	}
}

// TestFilterLineIsClampedAndSanitised pins the two properties every chrome line carrying
// text we did not author must have. It is counted as exactly one line by chromeLineCount, so
// a wrap overruns the frame by a row: a commit SHA alone is 47 columns and a full filter
// runs past 200 at a 100-column frame. And a Workflow name is API data any author on a
// polled repository controls, so an escape sequence in one would rewrite the terminal.
func TestFilterLineIsClampedAndSanitised(t *testing.T) {
	m := newFeed(100, 12)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))

	m = m.Update2(ShowRuns(filter.Filter{
		Branch:   "release/2026-07-a-very-long-branch-name-somebody-really-pushed",
		Commit:   "0123456789abcdef0123456789abcdef01234567",
		Actor:    "a-long-handle-here",
		Workflow: "a\x1b[31mred\x07",
	}))

	line, ok := m.filterLine()
	if !ok {
		t.Fatal("no filter line for an active filter")
	}
	if w := lipgloss.Width(line); w != 100 {
		t.Errorf("filter line is %d columns at a 100-column frame, want exactly 100 (chromeLineCount counts it as one line)", w)
	}
	if strings.ContainsAny(line, "\x1b\x07") && strings.Contains(line, "\x1b[31m") {
		t.Errorf("the filter line carries an unsanitised escape sequence: %q", line)
	}
}

// TestShowRunsClosesADetailPaneWithNothingUnderIt pins that a narrowing that empties the
// list takes the detail pane with it. The pane is opened over a row, so with no row it
// refers to nothing: it would paint one Run's Jobs under a list saying nothing matches, and
// a live Run would keep its ~3s refresh going for a row nobody can see (run-detail R13, R16).
func TestShowRunsClosesADetailPaneWithNothingUnderIt(t *testing.T) {
	m := feedWithDetail(120, 40)
	m = feedRuns(m, repoID("cli", "cli"), wfRun(1, "CI", 9001))
	m = m.Update2(press("enter")) // open the pane over the Run
	if !m.detailOpen {
		t.Fatal("setup: the detail pane did not open")
	}

	m = m.Update2(ShowRuns(filter.Filter{Workflow: "4242"}))

	if m.detailOpen {
		t.Error("the detail pane stayed open over a Run the filter removed from the list")
	}
}

// TestEmptyFeedSaysNothingHasArrivedYet pins the other empty case it must not be confused
// with: no filter, no Runs held, which is the cold start before the first poll answers.
func TestEmptyFeedSaysNothingHasArrivedYet(t *testing.T) {
	m := newFeed(100, 12)

	view := m.View()
	if !strings.Contains(view, "No Runs yet") {
		t.Errorf("an empty unfiltered Feed painted no explanation:\n%s", view)
	}
}
