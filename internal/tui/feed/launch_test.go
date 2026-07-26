package feed

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
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

// TestSingleRerunLaunchesWithNoModal is the silent no-op #61 names. A single re-run
// prices at FrictionNone (run-lifecycle R18, AC11), so no modal opens, and until now the
// key returned with no modal AND no execution: pressing R on one Run did nothing at all.
// It now launches over ops.NoInput(), the empty act a FrictionNone write carries
// (ADR-0019), so the one keystroke the Feed's most common corrective action rests on
// actually adds the Attempt (R1, R8).
func TestSingleRerunLaunchesWithNoModal(t *testing.T) {
	m, spy := feedWithSpy(t)
	spy.startedOp = ops.OpRerun
	m = m.Update2(press("down")) // engage; no selection, so the cursor Run is the set

	m, cmd := m.Update(press("R"))
	if m.confirmOpen {
		t.Fatalf("a single re-run opened a confirmation modal; R18 forbids it")
	}
	if cmd == nil {
		t.Fatalf("a single re-run issued no command: pressing R on one Run is a silent no-op")
	}
	msg := cmd()
	if spy.confirmed != 1 || spy.started != 1 {
		t.Fatalf("confirmed %d times and started %d, want exactly one of each (ADR-0019)", spy.confirmed, spy.started)
	}
	st, ok := msg.(ops.Started)
	if !ok {
		t.Fatalf("the re-run command returned %T, want an ops.Started (ADR-0015)", msg)
	}
	if st.Op != ops.OpRerun {
		t.Errorf("the launched operation is %q, want rerun", st.Op)
	}
}

// TestSingleCancelStillTakesTheModal is the other half of R18's asymmetry, pinned beside
// the re-run so the two cannot drift: a single cancel opens the y/N modal and issues
// nothing until it is answered (AC11).
func TestSingleCancelStillTakesTheModal(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m, cmd := m.Update(press("c"))
	if !m.confirmOpen {
		t.Fatalf("a single cancel opened no modal; R18 requires the y/N prompt")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("opening the cancel modal produced %T; no request is issued before the answer (AC11)", msg)
		}
	}
	if spy.confirmed != 0 || spy.started != 0 {
		t.Errorf("opening the modal reached Confirm %d times and Start %d, want zero of each (AC11)", spy.confirmed, spy.started)
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

// TestConfirmedLifecycleLaunchesTheOperation is the gate #61 removed. The running-op
// surface is generic over ops.Operation and so is the stream, so a confirmed cancel now
// runs through the same Confirm-then-Start path a Purge does and hands back the same
// Started handle the root adapts (run-lifecycle R16, ADR-0015). Before this, a confirmed
// cancel closed the modal and started nothing.
func TestConfirmedLifecycleLaunchesTheOperation(t *testing.T) {
	m, spy := feedWithSpy(t)
	spy.startedOp = ops.OpCancel
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("space")) // deselect, so the cursor Run is the set
	m = m.Update2(press("c"))     // a single-Run cancel confirmation, priced y/N (R18)
	if !m.confirmOpen {
		t.Fatalf("the cancel key did not open a confirmation")
	}
	m, cmd := m.Update(press("y"))
	if m.confirmOpen {
		t.Errorf("the modal stayed open after a confirmation")
	}
	if cmd == nil {
		t.Fatalf("a confirmed cancel issued no command, so no cancel would be requested")
	}
	msg := cmd()
	if spy.confirmed != 1 || spy.started != 1 {
		t.Fatalf("confirmed %d times and started %d, want exactly one of each (ADR-0019)", spy.confirmed, spy.started)
	}
	st, ok := msg.(ops.Started)
	if !ok {
		t.Fatalf("the launch command returned %T, want an ops.Started (ADR-0015)", msg)
	}
	if st.Op != ops.OpCancel {
		t.Errorf("the launched operation is %q, want cancel", st.Op)
	}
}

// recordingClient is an ops.Requester that answers every write 201 Created and keeps what
// it was asked. It is a wiring stub and not a stand-in for the API: what the four
// operations' responses mean is settled against cassettes in ops's own tests, and what is
// under test here is that one keystroke reaches the wire at all, at the right method and
// the right endpoint.
type recordingClient struct {
	mu    sync.Mutex
	calls []string
}

func (c *recordingClient) RequestWithContext(_ context.Context, method, path string, _ io.Reader) (*http.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, method+" "+path)
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusCreated,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("{}")),
	}, nil
}

func (c *recordingClient) issued() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// feedOnEngine stands a real ops engine on a recording transport and gives it one Run, so
// a test can drive a keystroke the whole way to the request it produces.
func feedOnEngine(t *testing.T, r domain.Run) (Model, *recordingClient) {
	t.Helper()
	client := &recordingClient{}
	m := New(Options{Profile: keys.Standard, Ops: ops.New(ops.Options{
		Client: client, ConfirmThreshold: 50, BreakerFailures: 50,
	})})
	m = m.Update2(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("o", "r"), r)
	return m, client
}

// TestSingleRerunReachesTheWire is the silent no-op proved the whole way down. It presses
// R over one Run, runs the command the keystroke produced, and drains the operation's
// stream to its terminal frame. What lands is the re-run endpoint for that Run and nothing
// else, so every link between the key and the request is exercised: the frozen selection,
// ops.Plan's FrictionNone pricing (run-lifecycle R18), Confirm over NoInput, Start's walk,
// and the endpoint lifecycleRequest builds.
//
// It issues no live re-run. The transport is in-process and no test in this repository has
// a live endpoint to reach (run-lifecycle R28).
func TestSingleRerunReachesTheWire(t *testing.T) {
	m, client := feedOnEngine(t, mkRun(4242, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0))

	m = m.Update2(press("down"))
	_, cmd := m.Update(press("R"))
	if cmd == nil {
		t.Fatalf("pressing R on one Run produced no command: the keystroke is a silent no-op")
	}
	st, ok := cmd().(ops.Started)
	if !ok {
		t.Fatalf("pressing R produced no ops.Started (ADR-0015)")
	}
	drain(t, st)

	want := "POST repos/o/r/actions/runs/4242/rerun"
	if got := client.issued(); len(got) != 1 || got[0] != want {
		t.Fatalf("the wire saw %v, want exactly [%q] (run-lifecycle R8)", got, want)
	}
}

// TestConfirmedCancelReachesTheWire is the same proof for the confirmed path: the modal's
// y is the explicit act, and what follows it is a cancel against the Run's own cancel
// endpoint (run-lifecycle R16, R18).
func TestConfirmedCancelReachesTheWire(t *testing.T) {
	m, client := feedOnEngine(t, mkRun(4243, "o", "r", "CI", domain.StatusInProgress, "", t0))

	m = m.Update2(press("down"))
	m = m.Update2(press("c"))
	_, cmd := m.Update(press("y"))
	if cmd == nil {
		t.Fatalf("a confirmed cancel produced no command")
	}
	st, ok := cmd().(ops.Started)
	if !ok {
		t.Fatalf("a confirmed cancel produced no ops.Started")
	}
	drain(t, st)

	want := "POST repos/o/r/actions/runs/4243/cancel"
	if got := client.issued(); len(got) != 1 || got[0] != want {
		t.Fatalf("the wire saw %v, want exactly [%q] (run-lifecycle R16)", got, want)
	}
}

// drain reads the operation's stream to its terminal frame, so the walk has finished
// before the test reads what reached the wire. The stream is bounded by the frozen set, so
// a deadline here can only be a defect and never a slow machine.
func drain(t *testing.T, st ops.Started) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case p, ok := <-st.Progress:
			if !ok || p.Done {
				return
			}
		case <-deadline:
			t.Fatalf("the operation produced no terminal frame")
		}
	}
}
