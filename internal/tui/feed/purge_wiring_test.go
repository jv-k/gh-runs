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

// newFeedWithOps builds a Feed wired to a real planner (ops.Plan needs no transport)
// and a discovered writable repository, so the delete key can freeze the selection.
func newFeedWithOps(t *testing.T) Model {
	t.Helper()
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	m := New(Options{Profile: keys.Standard, Ops: planner})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("o", "r"),
		mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Minute)),
	)
	return m
}

// TestDeleteKeyOpensConfirmationOverTheSelection pins the Feed wiring: with a Run
// selected, the delete key freezes the selection into a Plan and opens the confirmation
// modal, which the View then paints in place of the list (purge R4, R5, R7).
func TestDeleteKeyOpensConfirmationOverTheSelection(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))  // engage the list
	m = m.Update2(press("space")) // select the Run under the cursor
	m = m.Update2(press("d"))     // open the confirmation

	if !m.confirmOpen {
		t.Fatalf("the delete key did not open the confirmation modal (purge R4 to R9)")
	}
	if !m.CapturesInput() {
		t.Errorf("the Feed does not capture input while the modal is up; a typed count or y would leak to global keys (R7)")
	}
	if got := m.View(); !strings.Contains(got, "Delete") {
		t.Errorf("the View does not paint the confirmation modal while it is open:\n%s", got)
	}
}

// TestDeleteKeyWithoutSelectionUsesTheCursorRun pins that the delete key falls back to
// the Run under the cursor when nothing is selected, so a single-Run delete needs no
// prior selection.
func TestDeleteKeyWithoutSelectionUsesTheCursorRun(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down")) // engage; no selection
	m = m.Update2(press("d"))
	if !m.confirmOpen {
		t.Fatalf("the delete key did not open a confirmation over the cursor Run")
	}
	if m.confirm.Plan().Total() != 1 {
		t.Errorf("the cursor-only frozen set has %d Runs, want 1", m.confirm.Plan().Total())
	}
}

// TestConfirmationAbortReturnsToTheList pins that aborting the modal dismisses it and
// returns the Feed to the list, having issued nothing.
func TestConfirmationAbortReturnsToTheList(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("d"))
	m = m.Update2(press("n")) // abort
	if m.confirmOpen {
		t.Errorf("aborting the modal did not return the Feed to the list (AC6)")
	}
	if m.CapturesInput() {
		t.Errorf("the Feed still captures input after the modal closed")
	}
}

// TestDeleteKeyInertWithoutPlanner pins that with no planner wired (a golden test, or
// before discovery has recorded capability), the delete key is inert, keeping the
// destructive action disabled (repo-discovery R8).
func TestDeleteKeyInertWithoutPlanner(t *testing.T) {
	m := newFeed(100, 24) // no Ops in Options
	m = feedRuns(m, repoID("o", "r"), mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	m = m.Update2(press("down"))
	m = m.Update2(press("d"))
	if m.confirmOpen {
		t.Errorf("the delete key opened a modal with no planner wired; it must be inert (repo-discovery R8)")
	}
}
