package storage_test

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// launchSpy is a planner that records what it was asked to confirm and start, standing in
// for the ops engine. Plan is delegated to a real Ops, because a Plan cannot be forged and
// the friction it prices is what the modal enforces (ADR-0019).
type launchSpy struct {
	real      *ops.Ops
	confirmed int
	started   int
	startErr  error
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
	return ops.Started{Op: ops.OpDelete, Kind: ops.KindCache, Total: 1, Cancel: func() {}}, nil
}

// storageWithSpy builds a Storage tab over the spy planner holding one reclaimable Cache in
// a writable repository, refreshed so the eligibility gate has its capability data.
func storageWithSpy(t *testing.T) (storage.Model, *launchSpy) {
	t.Helper()
	spy := newLaunchSpy()
	repos := []domain.Repo{writable("cli", "cli")}
	m := storage.New(storage.Options{
		Profile: keys.Standard,
		Ops:     spy,
		Repos:   func() []domain.Repo { return repos },
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = fetched(m, oneCache("cli", "cli", 987654321, "setup-go", 302460229))
	m = send(m, "r") // pull the discovered repositories into the eligibility gate
	return m, spy
}

// TestConfirmedReclamationLaunchesTheOperation is the defect this issue exists to fix: a
// confirmed reclamation in the TUI closed the modal and issued no DELETE. It now returns a
// command that confirms the Plan and starts the walk, and the command's message is the
// Started handle the root adapts onto the shared running surface (R24, ADR-0015).
func TestConfirmedReclamationLaunchesTheOperation(t *testing.T) {
	m, spy := storageWithSpy(t)
	m = send(m, "space") // select the Cache under the cursor
	m = send(m, "d")     // open the confirmation, priced y/N over one object
	m, cmd := m.Update(press("y"))

	if m.CapturesInput() {
		t.Errorf("the modal stayed open after a confirmation")
	}
	if cmd == nil {
		t.Fatalf("a confirmed reclamation issued no command, so no DELETE would be issued (the #64 defect)")
	}
	msg := cmd()
	if spy.confirmed != 1 || spy.started != 1 {
		t.Fatalf("confirmed %d times and started %d, want exactly one of each (ADR-0019)", spy.confirmed, spy.started)
	}
	if _, ok := msg.(ops.Started); !ok {
		t.Fatalf("the launch command returned %T, want an ops.Started carrying the progress stream (ADR-0015)", msg)
	}
}

// TestAbortedReclamationLaunchesNothing pins purge AC6 on this tab: aborting the modal
// issues zero requests, and the launch path is not reached at all.
func TestAbortedReclamationLaunchesNothing(t *testing.T) {
	m, spy := storageWithSpy(t)
	m = send(m, "space")
	m = send(m, "d")
	_, cmd := m.Update(press("n")) // abort
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("aborting produced %T; an abort issues nothing (purge AC6)", msg)
		}
	}
	if spy.confirmed != 0 || spy.started != 0 {
		t.Errorf("an abort reached Confirm %d times and Start %d times, want zero of each (purge AC6)", spy.confirmed, spy.started)
	}
}

// TestReclamationLaunchFailureIsReportedRatherThanDropped pins that a refused launch
// surfaces. A keystroke that silently does nothing is the whole shape of the defect being
// fixed, and the refusal a running operation makes likely is ErrBusy, which arrives exactly
// when one is already walking.
func TestReclamationLaunchFailureIsReportedRatherThanDropped(t *testing.T) {
	m, spy := storageWithSpy(t)
	spy.startErr = ops.ErrBusy
	m = send(m, "space")
	m = send(m, "d")
	_, cmd := m.Update(press("y"))
	if cmd == nil {
		t.Fatalf("a confirmed reclamation issued no command")
	}
	if _, ok := cmd().(ops.LaunchFailed); !ok {
		t.Errorf("a refused launch produced no LaunchFailed message; the operator would see nothing happen")
	}
}
