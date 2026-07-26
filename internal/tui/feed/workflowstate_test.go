package feed

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// deletedMarker is the line run-detail R8 asks the pane to paint for an Orphaned Run.
// The Feed's part is handing the pane the State the fan-out stamped; the wording and
// its placement are the pane's own, fixed by its goldens.
const deletedMarker = "Workflow deleted"

// orphanRun is a Run whose Workflow was deleted, as the fan-out stamps it (ADR-0014).
func orphanRun(id int64, owner, name, workflow string, started time.Time) domain.Run {
	r := mkRun(id, owner, name, workflow, domain.StatusCompleted, domain.ConclusionFailure, started)
	r.WorkflowState = domain.StateDeleted
	return r
}

// TestOpenDetailMarksAnOrphanedRun pins the wiring run-detail R8 and AC11 need. The
// pane has always been able to mark a deleted Workflow; until the Feed hands it the
// State the fan-out stamped, the marker never triggers and every Run reads as one whose
// Workflow still exists.
func TestOpenDetailMarksAnOrphanedRun(t *testing.T) {
	m := newFeed(100, 30)
	m = feedRuns(m, repoID("acme", "legacy"), orphanRun(1, "acme", "legacy", "Old Pipeline", t0))

	if got := m.View(); strings.Contains(got, deletedMarker) {
		t.Fatalf("precondition: the marker is painted before the pane is even open:\n%s", got)
	}

	m = m.Update2(press("enter")) // open the detail pane over the Run under the cursor

	if got := m.View(); !strings.Contains(got, deletedMarker) {
		t.Errorf("the pane does not mark the Orphaned Run's Workflow deleted (R8, AC11):\n%s", got)
	}
}

// TestLiveWorkflowIsNotMarked is the other half. A Run whose Workflow still exists must
// not be marked, or the marker says nothing and R8's distinction is lost.
func TestLiveWorkflowIsNotMarked(t *testing.T) {
	m := newFeed(100, 30)
	r := mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0)
	r.WorkflowState = domain.StateActive
	m = feedRuns(m, repoID("acme", "api"), r)

	m = m.Update2(press("enter"))

	if got := m.View(); strings.Contains(got, deletedMarker) {
		t.Errorf("a Run on a live Workflow was marked deleted:\n%s", got)
	}
}

// TestTheMarkerFollowsTheCursor pins that the State is re-stamped on every retarget. The
// pane clears it on a new selection deliberately, because the marker is per-Run, so a
// Feed that stamped it only at open would paint the first Run's answer over every Run
// the cursor later reaches.
func TestTheMarkerFollowsTheCursor(t *testing.T) {
	m := newFeed(100, 30)
	m = feedRuns(m, repoID("acme", "legacy"),
		mkRun(1, "acme", "legacy", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		orphanRun(2, "acme", "legacy", "Old Pipeline", t0.Add(-time.Minute)),
	)

	m = m.Update2(press("enter")) // open over Run 1, whose Workflow is live
	if got := m.View(); strings.Contains(got, deletedMarker) {
		t.Fatalf("the live Run was marked deleted at open:\n%s", got)
	}

	m = m.Update2(press("down")) // the cursor moves onto the Orphaned Run
	if got := m.View(); !strings.Contains(got, deletedMarker) {
		t.Errorf("moving onto an Orphaned Run did not raise the marker (R8):\n%s", got)
	}

	m = m.Update2(press("up")) // and back onto the live one
	if got := m.View(); strings.Contains(got, deletedMarker) {
		t.Errorf("the marker survived the cursor leaving the Orphaned Run:\n%s", got)
	}
}

// newOrphanFeed builds a Feed over one writable repository holding the given Runs, wired to
// a real planner so the re-run keys can freeze a selection.
func newOrphanFeed(t *testing.T, runs ...domain.Run) Model {
	t.Helper()
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	m := New(Options{Profile: keys.Standard, Ops: planner})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("acme", "legacy"), Permissions: domain.Permissions{Push: true}}})
	return feedRuns(m, repoID("acme", "legacy"), runs...)
}

// TestOrphanedRunOffersNoRerun pins run-detail R18 and AC15, the requirement the unwired
// State left unreachable. A deleted Workflow can produce no further Run, so re-run is not
// offered over a set that is nothing but Orphaned Runs.
func TestOrphanedRunOffersNoRerun(t *testing.T) {
	m := newOrphanFeed(t,
		orphanRun(1, "acme", "legacy", "Old Pipeline", t0),
		orphanRun(2, "acme", "legacy", "Old Pipeline", t0.Add(-time.Minute)),
	)

	m = m.Update2(press("space")) // select the Run under the cursor
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // and the next, so the re-run would otherwise price at a modal
	m = m.Update2(press("enter")) // open the pane over the Orphaned Run under the cursor
	m = m.Update2(press("R"))     // re-run

	if m.confirmOpen {
		t.Errorf("a re-run was offered over a set of Orphaned Runs; R18 and AC15 forbid it")
	}
}

// TestOrphanedRunOffersNoRerunWithThePaneClosed pins that the exclusion is a property of
// the Run and not of whether the operator happens to have the detail pane open. The Run
// carries its Workflow's State, so the gate reads the Run.
func TestOrphanedRunOffersNoRerunWithThePaneClosed(t *testing.T) {
	m := newOrphanFeed(t,
		orphanRun(1, "acme", "legacy", "Old Pipeline", t0),
		orphanRun(2, "acme", "legacy", "Old Pipeline", t0.Add(-time.Minute)),
	)

	m = m.Update2(press("space"))
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // two Orphaned Runs selected, and no pane open
	m = m.Update2(press("R"))

	if m.detailOpen {
		t.Fatal("precondition: the detail pane is open, so this asserts nothing new")
	}
	if m.confirmOpen {
		t.Errorf("a re-run was offered over Orphaned Runs while the pane was closed (R18, AC15)")
	}
}

// TestMixedSelectionRerunsTheHealthyRuns pins the limb that matters most. R18 excludes a
// Run whose Workflow is deleted, never a batch that contains one. Dropping the whole
// operation would silently discard the healthy Runs the operator selected beside it, with
// no modal and no message, which is worse than the thing the rule exists to prevent.
func TestMixedSelectionRerunsTheHealthyRuns(t *testing.T) {
	m := newOrphanFeed(t,
		mkRun(1, "acme", "legacy", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
		mkRun(2, "acme", "legacy", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Minute)),
		orphanRun(3, "acme", "legacy", "Old Pipeline", t0.Add(-2*time.Minute)),
	)

	for i := 0; i < 3; i++ {
		m = m.Update2(press("space")) // select all three
		if i < 2 {
			m = m.Update2(press("down"))
		}
	}
	m = m.Update2(press("enter")) // the cursor rests on the Orphaned Run, pane open over it
	m = m.Update2(press("R"))

	if !m.confirmOpen {
		t.Fatalf("the re-run was dropped because one Run in the set was Orphaned; R18 excludes the Run, not the batch")
	}
	plan := m.confirm.Plan()
	if got := plan.Total(); got != 3 {
		t.Errorf("the Plan totals %d, want 3 (the skipped are counted inside the total)", got)
	}
	if got := plan.Skipped(); got != 1 {
		t.Errorf("the Plan skips %d, want 1 (the Orphaned Run alone, stated in the modal's skip lines)", got)
	}
}
