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
	real        *ops.Ops
	confirmed   int
	started     int
	confirmedOp ops.Operation
	startedOp   ops.Operation
	startedSet  int
	startErr    error
}

func newLaunchSpy() *launchSpy {
	return &launchSpy{real: ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})}
}

func (s *launchSpy) Plan(op ops.Operation, sel []ops.Item, repos map[domain.RepoID]domain.Repo, unmatched ...ops.Unmatched) (ops.Plan, error) {
	return s.real.Plan(op, sel, repos, unmatched...)
}

func (s *launchSpy) Confirm(p ops.Plan, in ops.Input) (ops.Confirmed, error) {
	s.confirmed++
	// The verb the Feed froze and priced. Recorded here rather than asserted off the
	// Started the spy fabricates, which would only prove the spy returns its own field.
	s.confirmedOp = p.Operation()
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

// TestSingleRerunLaunchesWithoutAModal pins R18's asymmetry all the way through: a single
// re-run prices at FrictionNone, so it opens no modal, and it must still act. Before #61 it
// returned at the friction check and issued nothing, so pressing R on one Run was a silent
// no-op: the operator got neither a prompt nor a re-run. Both verbs behave this way, and
// the confirmation they skip is the modal, never ops.Confirm, which still validates the
// Plan and mints the single-use Confirmed (ADR-0019).
func TestSingleRerunLaunchesWithoutAModal(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want ops.Operation
	}{
		{"R", ops.OpRerun},
		{"F", ops.OpRerunFailed},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			m, spy := feedWithSpy(t)
			m = m.Update2(press("down")) // engage; no selection, so the cursor Run is the set
			m, cmd := m.Update(press(tc.key))

			if m.confirmOpen {
				t.Fatalf("a single %s opened a confirmation modal; R18 forbids it", tc.want)
			}
			if cmd == nil {
				t.Fatalf("a single %s issued no command, so pressing %q is a silent no-op (the #61 defect)", tc.want, tc.key)
			}
			msg := cmd()
			if spy.confirmed != 1 || spy.started != 1 {
				t.Fatalf("confirmed %d times and started %d, want exactly one of each", spy.confirmed, spy.started)
			}
			if spy.confirmedOp != tc.want {
				t.Errorf("the launched Plan's operation = %q, want %q", spy.confirmedOp, tc.want)
			}
			if _, ok := msg.(ops.Started); !ok {
				t.Fatalf("the launch command returned %T, want an ops.Started (ADR-0015)", msg)
			}
		})
	}
}

// TestSingleCancelStillTakesAModal is R18's other half, and the reason the FrictionNone
// launch above cannot simply be applied to every single-Run operation: cancel and
// force-cancel take a y/N even over one Run, because cancelled work cannot be recovered.
func TestSingleCancelStillTakesAModal(t *testing.T) {
	for _, k := range []string{"c", "C"} {
		m, spy := feedWithSpy(t)
		m = m.Update2(press("down"))
		m, cmd := m.Update(press(k))
		if !m.confirmOpen {
			t.Errorf("a single %q opened no modal; R18 requires y/N for cancel", k)
		}
		if cmd != nil {
			if msg := cmd(); msg != nil {
				t.Errorf("a single %q launched %T before any confirmation", k, msg)
			}
		}
		if spy.started != 0 {
			t.Errorf("a single %q started an operation before the operator answered", k)
		}
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

// TestConfirmedLifecycleLaunchesTheOperation is the second half of the #61 defect: the
// Feed's launch was wired for the Purge alone, so a confirmed cancel closed the modal and
// started nothing. All four lifecycle verbs now travel the same launch, because the
// running-op surface and the progress stream are generic over ops.Operation and a bulk
// lifecycle mutation is the same walk over a frozen set (run-lifecycle R16, R17, AC17).
func TestConfirmedLifecycleLaunchesTheOperation(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want ops.Operation
	}{
		{"c", ops.OpCancel},
		{"C", ops.OpForceCancel},
		{"R", ops.OpRerun},
		{"F", ops.OpRerunFailed},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			m, spy := feedWithSpy(t)
			// Two Runs selected, so every verb prices above FrictionNone and opens the modal:
			// R18 exempts only the single re-run, which has its own test.
			m = m.Update2(press("space"))
			m = m.Update2(press("down"))
			m = m.Update2(press("space"))
			m = m.Update2(press(tc.key))
			if !m.confirmOpen {
				t.Fatalf("the %q key did not open a confirmation", tc.key)
			}
			m, cmd := m.Update(press("y"))
			if m.confirmOpen {
				t.Errorf("the modal stayed open after a confirmation")
			}
			if cmd == nil {
				t.Fatalf("a confirmed %s issued no command, so nothing would be requested (the #61 defect)", tc.want)
			}
			msg := cmd()
			if spy.confirmed != 1 || spy.started != 1 {
				t.Fatalf("confirmed %d times and started %d, want exactly one of each (ADR-0019)", spy.confirmed, spy.started)
			}
			if spy.confirmedOp != tc.want {
				t.Errorf("the launched Plan's operation = %q, want %q", spy.confirmedOp, tc.want)
			}
			if _, ok := msg.(ops.Started); !ok {
				t.Fatalf("the launch command returned %T, want an ops.Started carrying the progress stream (ADR-0015)", msg)
			}
		})
	}
}
