package feed

import (
	"strings"
	"testing"
	"time"

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
// the Feed's most common action and neither destroys a Run. It launches straight away, which
// TestSingleRerunLaunchesWithNoModal pins; this test owns the no-modal half alone.
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
// deleted is Orphaned and can produce no further Run, so no re-run is offered over a set of
// them. The gate reads the Workflow State the fan-out stamped on the Run (ADR-0014), so the
// next poll restoring a live State offers the re-run again, which proves the gate is that
// State and nothing else.
func TestRerunInertForOrphanedRun(t *testing.T) {
	id := repoID("o", "r")
	orphan := func(runID int64, started time.Time) domain.Run {
		r := mkRun(runID, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, started)
		r.WorkflowState = domain.StateDeleted
		return r
	}
	m := newFeedWithOps(t)
	m = feedRuns(m, id, orphan(1, t0), orphan(2, t0.Add(-time.Minute)))
	m = m.Update2(press("space")) // select the top Run
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // select the next, so a re-run would otherwise be a bulk modal

	m = m.Update2(press("R"))
	if m.confirmOpen {
		t.Errorf("re-run was offered for Orphaned Runs; run-detail R18 and AC15 forbid it")
	}

	// The Workflow is live again in the next poll's Runs: the same key raises the bulk
	// confirmation.
	m = feedRuns(m, id,
		mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
		mkRun(2, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Minute)),
	)
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

// TestCancelModalEscalatesToForceCancel is run-lifecycle R5, R6 and AC6 end to end at the
// Feed: the force-cancel key inside a cancel modal re-prices the same frozen set for the
// harder verb and puts the graduated confirmation back up in front of it. The escalation
// is a choice the operator makes, never a substitution the tool makes for them (R6), so
// nothing is issued until the new modal is answered.
func TestCancelModalEscalatesToForceCancel(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("c"))
	if m.confirm.Plan().Operation() != ops.OpCancel {
		t.Fatalf("the cancel key did not open a cancel confirmation")
	}

	m = m.Update2(press("C")) // escalate

	if !m.confirmOpen {
		t.Fatalf("escalating closed the modal; the harder verb still takes a confirmation (R6, R17)")
	}
	if got := m.confirm.Plan().Operation(); got != ops.OpForceCancel {
		t.Errorf("the escalated modal's Plan operation = %q, want force-cancel (R6, AC6)", got)
	}
	if got := m.View(); !strings.Contains(got, "Force-cancel") {
		t.Errorf("the escalated modal does not name force-cancel:\n%s", got)
	}
}

// TestEscalationKeepsTheFrozenSet pins R16 across the escalation: the set froze when the
// cancel modal opened, and Feed activity after that moment must not change it. So the
// force-cancel is re-priced over the Items the cancel Plan already holds, never over a
// fresh read of the selection, and a Run that arrived in between is not swept in.
func TestEscalationKeepsTheFrozenSet(t *testing.T) {
	m := newFeedWithOps(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("c"))
	frozen := m.confirm.Plan().Items()

	// A poll lands while the modal is up, adding a Run and selecting nothing.
	m = feedRuns(m, repoID("o", "r"),
		mkRun(3, "o", "r", "CI", domain.StatusInProgress, "", t0.Add(time.Minute)),
		mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Minute)),
	)
	m = m.Update2(press("C"))

	got := m.confirm.Plan().Items()
	if len(got) != len(frozen) {
		t.Fatalf("the escalated set holds %d Items, want the %d the cancel froze (R16)", len(got), len(frozen))
	}
	for i := range got {
		if got[i].ID != frozen[i].ID {
			t.Errorf("the escalated set's Item %d is Run %d, want %d (R16)", i, got[i].ID, frozen[i].ID)
		}
	}
}
