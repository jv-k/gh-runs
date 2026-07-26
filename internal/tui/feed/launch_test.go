package feed

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// launchSpy is a planner that records what it was asked to confirm and start, standing
// in for the ops engine. Plan is delegated to a real Ops, because a Plan cannot be
// forged and the friction it prices is what the modal enforces (ADR-0019).
type launchSpy struct {
	real       *ops.Ops
	confirmed  int
	started    int
	startedOp  ops.Operation
	startedSet int
	startErr   error
}

func newLaunchSpy() *launchSpy {
	return &launchSpy{real: ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})}
}

func (s *launchSpy) Plan(op ops.Operation, sel []ops.Item, repos map[domain.RepoID]domain.Repo) (ops.Plan, error) {
	return s.real.Plan(op, sel, repos)
}

func (s *launchSpy) Confirm(p ops.Plan, in ops.Input) (ops.Confirmed, error) {
	s.confirmed++
	return s.real.Confirm(p, in)
}

func (s *launchSpy) Start(_ context.Context, c ops.Confirmed) (ops.Started, error) {
	s.started++
	if s.startErr != nil {
		return ops.Started{}, s.startErr
	}
	// The Confirmed is opaque, which is the point: the tab cannot inspect or rebuild it.
	// What it can prove is that exactly one reached here per confirmation.
	_ = c
	return ops.Started{Op: s.startedOp, Total: s.startedSet, Cancel: func() {}}, nil
}

// feedWithSpy builds a Feed over the spy planner with one writable repository and two
// completed Runs, so the delete key can freeze a selection and confirm it.
func feedWithSpy(t *testing.T) (Model, *launchSpy) {
	t.Helper()
	spy := newLaunchSpy()
	spy.startedOp, spy.startedSet = ops.OpDelete, 1
	m := New(Options{Profile: keys.Standard, Ops: spy})
	m = m.Update2(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("o", "r"),
		mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Minute)),
	)
	return m, spy
}

// TestConfirmedDeleteLaunchesTheOperation is the defect this issue exists to fix: a
// confirmed delete in the TUI used to close the modal and issue no DELETE. It now
// returns a command that confirms the Plan and starts the walk, and the command's message
// is the Started handle the root adapts (ADR-0015).
func TestConfirmedDeleteLaunchesTheOperation(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("d")) // opens the modal over the cursor Run, priced y/N
	m, cmd := m.Update(press("y"))

	if m.confirmOpen {
		t.Errorf("the modal stayed open after a confirmation")
	}
	if cmd == nil {
		t.Fatalf("a confirmed delete issued no command, so no DELETE would be issued (the #59 defect)")
	}
	msg := cmd()
	if spy.confirmed != 1 || spy.started != 1 {
		t.Fatalf("confirmed %d times and started %d, want exactly one of each (ADR-0019)", spy.confirmed, spy.started)
	}
	if _, ok := msg.(ops.Started); !ok {
		t.Fatalf("the launch command returned %T, want an ops.Started carrying the progress stream (ADR-0015)", msg)
	}
}

// TestAbortedDeleteLaunchesNothing pins AC6: aborting the modal issues zero requests, and
// the launch path is not reached at all.
func TestAbortedDeleteLaunchesNothing(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("d"))
	_, cmd := m.Update(press("n")) // abort
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("aborting produced %T; an abort issues nothing (AC6)", msg)
		}
	}
	if spy.confirmed != 0 || spy.started != 0 {
		t.Errorf("an abort reached Confirm %d times and Start %d times, want zero of each (AC6)", spy.confirmed, spy.started)
	}
}

// TestLaunchFailureIsReportedRatherThanDropped pins that a refused launch surfaces. A
// keystroke that silently does nothing is the whole shape of the defect being fixed, so a
// declined or spent confirmation becomes a message rather than a swallowed error.
func TestLaunchFailureIsReportedRatherThanDropped(t *testing.T) {
	m, spy := feedWithSpy(t)
	spy.startErr = ops.ErrSpent
	m = m.Update2(press("down"))
	m = m.Update2(press("d"))
	_, cmd := m.Update(press("y"))
	if cmd == nil {
		t.Fatalf("a confirmed delete issued no command")
	}
	if _, ok := cmd().(ops.LaunchFailed); !ok {
		t.Errorf("a refused launch produced no LaunchFailed message; the operator would see nothing happen")
	}
}

// TestLifecycleConfirmationDoesNotLaunchYet pins the seam left for #61: the running-op
// surface is generic over ops.Operation and the Feed's launch is wired for the Purge
// alone at this stage, so a confirmed cancel still closes the modal and starts nothing.
// Removing this gate is the whole of what run-lifecycle's execution issue has to do here.
func TestLifecycleConfirmationDoesNotLaunchYet(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("space")) // deselect, so the cursor Run is the set
	m = m.Update2(press("c"))     // a bulk cancel confirmation
	if !m.confirmOpen {
		t.Fatalf("the cancel key did not open a confirmation")
	}
	m, cmd := m.Update(press("y"))
	if m.confirmOpen {
		t.Errorf("the modal stayed open after a confirmation")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("a confirmed cancel launched %T; #61 owns wiring that launch", msg)
		}
	}
	if spy.started != 0 {
		t.Errorf("a confirmed cancel started an operation; #61 owns that wiring")
	}
}
