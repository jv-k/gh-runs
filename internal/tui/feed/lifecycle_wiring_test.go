package feed

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// TestCancelKeyOpensConfirmationOverSelection pins the Feed wiring for cancel: with a Run
// selected, the cancel key freezes the selection into an OpCancel Plan and opens the same
// graduated confirmation the Purge uses, its verb tracking the operation (run-lifecycle
// R16, R17, R18). A single cancel prices at y/N, not None (R18).
func TestCancelKeyOpensConfirmationOverSelection(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))  // engage the list
	m = m.Update2(press("space")) // select the Run under the cursor
	m = m.Update2(press("c"))     // open the cancel confirmation

	if !m.confirmOpen {
		t.Fatalf("the cancel key did not open the confirmation modal (run-lifecycle R16, R17)")
	}
	if m.confirm.Plan().Operation() != ops.OpCancel {
		t.Errorf("the modal's Plan operation = %q, want cancel", m.confirm.Plan().Operation())
	}
	if got := m.View(); !strings.Contains(got, "Cancel") {
		t.Errorf("the View does not name the cancel operation while the modal is open:\n%s", got)
	}
	if !m.CapturesInput() {
		t.Errorf("the Feed does not capture input while the cancel modal is up (R18's y/N)")
	}
}

// TestForceCancelKeyOpensConfirmation pins that the force-cancel key raises its own
// confirmation for a distinct operation (run-lifecycle R6).
func TestForceCancelKeyOpensConfirmation(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("C")) // force-cancel
	if !m.confirmOpen || m.confirm.Plan().Operation() != ops.OpForceCancel {
		t.Fatalf("the force-cancel key did not open a force-cancel confirmation (R6)")
	}
	if got := m.View(); !strings.Contains(got, "Force-cancel") {
		t.Errorf("the View does not name force-cancel:\n%s", got)
	}
}

// TestSingleRerunTakesNoModal pins R18's asymmetry at the Feed: a single re-run over the
// cursor Run prices at FrictionNone and opens no modal, because correcting a failed Run is
// the Feed's most common action and neither destroys a Run. Launching it is the running
// surface this stage defers, exactly as the Purge stage defers launching a confirmed Purge.
func TestSingleRerunTakesNoModal(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down")) // engage; no selection, so the cursor Run is the set
	m = m.Update2(press("R"))    // re-run
	if m.confirmOpen {
		t.Errorf("a single re-run opened a confirmation modal; R18 forbids it")
	}
	if m.CapturesInput() {
		t.Errorf("the Feed captures input after a single re-run, though no modal is up (R18)")
	}
}

// TestBulkRerunStillConfirms pins AC10: a bulk re-run over a small single-repository frozen
// set still opens the confirmation, unlike a single one. Only the single case is exempt.
func TestBulkRerunStillConfirms(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("space")) // select the Run at the top of the list
	m = m.Update2(press("down"))  // move to the next Run
	m = m.Update2(press("space")) // select it too: two distinct Runs
	m = m.Update2(press("R"))     // bulk re-run over two Runs
	if !m.confirmOpen {
		t.Fatalf("a bulk re-run did not open a confirmation; AC10 requires it")
	}
	if m.confirm.Plan().Operation() != ops.OpRerun || m.confirm.Plan().Total() != 2 {
		t.Errorf("the modal's Plan = %q of %d, want rerun of 2 (AC10)", m.confirm.Plan().Operation(), m.confirm.Plan().Total())
	}
}

// TestRerunInertForOrphanedRun pins run-detail R18 and AC15: a Run whose Workflow is
// deleted is Orphaned and can produce no further Run, so the detail pane offers no re-run.
// While the pane is open over such a Run the re-run key is inert; clearing the Orphaned
// state offers the re-run again, proving the gate is the Workflow State and nothing else.
func TestRerunInertForOrphanedRun(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("space")) // select the top Run
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // select the next Run, so a re-run would otherwise be a bulk modal
	m.detailOpen = true
	m.detail = m.detail.SetWorkflowState(domain.StateDeleted) // an Orphaned Run is on screen

	m = m.Update2(press("R"))
	if m.confirmOpen {
		t.Errorf("re-run was offered for an Orphaned Run; run-detail R18 and AC15 forbid it")
	}

	// Clear the Orphaned state: the same key now raises the bulk confirmation.
	m.detail = m.detail.SetWorkflowState(domain.StateActive)
	m = m.Update2(press("R"))
	if !m.confirmOpen {
		t.Errorf("re-run stayed inert for a live Workflow; only an Orphaned Run gates it (R18)")
	}
}

// TestLifecycleKeysInertWithoutPlanner pins that with no planner wired (a golden Feed, or
// before discovery has recorded capability) the lifecycle keys are inert, keeping the
// destructive-ish actions disabled (repo-discovery R8), exactly as the delete key is.
func TestLifecycleKeysInertWithoutPlanner(t *testing.T) {
	for _, k := range []string{"c", "C", "R", "F"} {
		m := newFeed(100, 24) // no Ops in Options
		m = feedRuns(m, repoID("o", "r"), mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0))
		m = m.Update2(press("down"))
		m = m.Update2(press(k))
		if m.confirmOpen {
			t.Errorf("key %q opened a modal with no planner wired; it must be inert (repo-discovery R8)", k)
		}
	}
}
