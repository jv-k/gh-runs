package ops_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// activeWorkflow is an active Workflow, eligible for disable (workflow-management R5).
func activeWorkflow(id int64, owner, name string) domain.Workflow {
	return domain.Workflow{
		ID:    id,
		Name:  "CI",
		Path:  ".github/workflows/ci.yml",
		State: domain.StateActive,
		Repo:  repoID(owner, name),
	}
}

// disabledWorkflow is a manually disabled Workflow, eligible for enable (R5).
func disabledWorkflow(id int64, owner, name string) domain.Workflow {
	w := activeWorkflow(id, owner, name)
	w.State = domain.StateDisabledManually
	return w
}

// deletedWorkflow is a Workflow whose YAML is gone: neither enable nor disable is offered
// (R9), and Plan must refuse to build one for it.
func deletedWorkflow(id int64, owner, name string) domain.Workflow {
	w := activeWorkflow(id, owner, name)
	w.State = domain.StateDeleted
	return w
}

// runToggle runs ToggleWorkflow in a goroutine and drives the fake clock so a paced write
// releases without a real sleep (rate-governor R21, R22), exactly as runPurge does for a
// Purge. It cancels the driver the instant ToggleWorkflow returns.
func runToggle(t *testing.T, h *harness, op ops.Operation, wf domain.Workflow, repos map[domain.RepoID]domain.Repo) ops.Summary {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	type result struct {
		s   ops.Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := h.ops.ToggleWorkflow(runCtx, op, wf, repos)
		cancel()
		done <- result{s, err}
	}()
	const quantum = 200 * time.Millisecond
	for {
		if err := h.clk.BlockUntilContext(runCtx, 1); err != nil {
			break
		}
		h.clk.Advance(quantum)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("ToggleWorkflow returned an error: %v", r.err)
	}
	return r.s
}

// TestWorkflowToggleActsOverCassette pins R5 and R8 against a cassette, with no live
// mutation: enabling a disabled Workflow PUTs its /enable endpoint and disabling an active
// one PUTs its /disable endpoint, each accepted with a 204 recorded as Acted. The toggle is
// not a deletion, so it writes no deletion-log line and issues no DELETE (R24, purge R29
// scope). Every write here is a taped PUT replayed in ModeReplayOnly.
func TestWorkflowToggleActsOverCassette(t *testing.T) {
	h := newHarness(t, "workflow_toggle", 50, 50)
	repos := snapshot(writableRepo("o", "r"))

	enable := runToggle(t, h, ops.OpEnable, disabledWorkflow(9001, "o", "r"), repos)
	if enable.Acted != 1 || enable.FailedCount() != 0 || enable.Skipped != 0 {
		t.Errorf("enable = acted %d, failed %d, skipped %d; want 1/0/0 (R5)", enable.Acted, enable.FailedCount(), enable.Skipped)
	}

	disable := runToggle(t, h, ops.OpDisable, activeWorkflow(9002, "o", "r"), repos)
	if disable.Acted != 1 || disable.FailedCount() != 0 {
		t.Errorf("disable = acted %d, failed %d; want 1/0 (R5)", disable.Acted, disable.FailedCount())
	}

	puts := h.counting.urls("PUT")
	if len(puts) != 2 {
		t.Fatalf("issued %d PUTs, want 2 (one enable, one disable) (R5): %v", len(puts), puts)
	}
	if !hasSuffix(puts, "/actions/workflows/9001/enable") {
		t.Errorf("no PUT to the enable endpoint; got %v (R5)", puts)
	}
	if !hasSuffix(puts, "/actions/workflows/9002/disable") {
		t.Errorf("no PUT to the disable endpoint; got %v (R5)", puts)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("a Workflow toggle issued a DELETE; enable and disable flip State, they delete nothing (R5)")
	}
	// A toggle is not a deletion, so purge R29's log MUST NOT record it, and Execute opens
	// no log for it at all (run-lifecycle R24, purge R29 scope).
	if h.logExists() {
		t.Errorf("a Workflow toggle wrote a deletion log; a toggle deletes nothing (purge R29 scope)")
	}
}

// TestWorkflowToggleSkipsIneligibleWithoutTouchingTheWire pins R6 and R9 as a property of
// the write path, over a transport that fails any wire call so a single leaked PUT fails the
// test loudly (the no-live-mutation rule). An archived repository, a read-only repository,
// and a deleted Workflow each make Plan stamp a skip, so Execute issues nothing. This is the
// gate's defense in depth beneath the tab's own refusal (AC2).
func TestWorkflowToggleSkipsIneligibleWithoutTouchingTheWire(t *testing.T) {
	archived := domain.Repo{ID: repoID("o", "arch"), Permissions: domain.Permissions{Push: true}, Archived: true}
	readonly := domain.Repo{ID: repoID("o", "ro"), Permissions: domain.Permissions{Push: false}}
	writable := writableRepo("o", "w")

	cases := []struct {
		name string
		op   ops.Operation
		wf   domain.Workflow
		repo domain.Repo
	}{
		{"archived repo is never toggleable (R6)", ops.OpDisable, activeWorkflow(1, "o", "arch"), archived},
		{"read-only repo is never toggleable (R6)", ops.OpEnable, disabledWorkflow(2, "o", "ro"), readonly},
		{"a deleted Workflow is never toggleable (R9)", ops.OpEnable, deletedWorkflow(3, "o", "w"), writable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newOfflineHarness(t, 50, 50)
			sum, err := h.ops.ToggleWorkflow(context.Background(), c.op, c.wf, snapshot(c.repo))
			if err != nil {
				t.Fatalf("ToggleWorkflow returned an error: %v", err)
			}
			if sum.Skipped != 1 || sum.Acted != 0 || sum.FailedCount() != 0 {
				t.Errorf("summary = skipped %d, acted %d, failed %d; want 1/0/0 (R6, R9)", sum.Skipped, sum.Acted, sum.FailedCount())
			}
			if len(h.counting.calls) != 0 {
				t.Errorf("an ineligible toggle reached the wire (%d calls); it must issue nothing (R6, R9)", len(h.counting.calls))
			}
			if h.logExists() {
				t.Errorf("a Workflow toggle wrote a deletion log; a toggle deletes nothing")
			}
		})
	}
}

// TestWorkflowToggleFrictionIsNone pins that enable and disable price at FrictionNone, the
// no-confirmation level a reversible toggle takes (R5, R8): the workflow-management canon asks
// for no graduated confirmation, so ToggleWorkflow confirms with NoInput and shows no modal.
func TestWorkflowToggleFrictionIsNone(t *testing.T) {
	repos := snapshot(writableRepo("o", "r"))
	for _, op := range []ops.Operation{ops.OpEnable, ops.OpDisable} {
		p, err := newPlanOps(50).Plan(op, []ops.Item{ops.WorkflowItem(activeWorkflow(1, "o", "r"))}, repos)
		if err != nil {
			t.Fatalf("Plan(%s): %v", op, err)
		}
		if p.Friction() != ops.FrictionNone {
			t.Errorf("Plan(%s).Friction() = %d, want FrictionNone (%d); a reversible toggle needs no confirmation (R5)", op, p.Friction(), ops.FrictionNone)
		}
	}
}

// TestToggleWorkflowRejectsNonToggleOp pins that ToggleWorkflow is the entry for the two
// reversible toggles alone: handing it a delete or a cancel is a caller misuse it refuses
// rather than silently issuing the wrong write.
func TestToggleWorkflowRejectsNonToggleOp(t *testing.T) {
	o := newPlanOps(50)
	_, err := o.ToggleWorkflow(context.Background(), ops.OpDelete, activeWorkflow(1, "o", "r"), snapshot(writableRepo("o", "r")))
	if err == nil {
		t.Errorf("ToggleWorkflow accepted OpDelete; it must reject anything but OpEnable and OpDisable")
	}
}

// hasSuffix reports whether any string in xs ends with suffix.
func hasSuffix(xs []string, suffix string) bool {
	for _, x := range xs {
		if strings.HasSuffix(x, suffix) {
			return true
		}
	}
	return false
}
