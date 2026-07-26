package running_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/running"
)

// started builds a Started handle over a cancel spy, the shape a launching Cmd hands the
// root (ADR-0015). No ops engine is involved: the pane consumes frames and knows nothing
// about who produced them, which is what makes every frame below fabricable.
func started(op ops.Operation, total int, cancel func()) ops.Started {
	return ops.Started{Op: op, Total: total, Cancel: func() { cancel() }}
}

func sized(m running.Model, w int) running.Model {
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
	return m
}

// press builds a key press. A ctrl chord is the modifier plus the bare rune, which is
// what key.Matches resolves through KeyPressMsg.String().
func press(s string) tea.KeyPressMsg {
	if after, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(after)[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// frame builds one running snapshot: a Purge partway through a frozen set.
func frame(total, deleted, outstanding int, elapsed time.Duration, rate, ceiling, floor float64) ops.Progress {
	return ops.Progress{
		Op:          ops.OpDelete,
		Sum:         ops.Summary{Total: total, Deleted: deleted},
		Outstanding: outstanding,
		Elapsed:     elapsed,
		Rate:        rate,
		Ceiling:     ceiling,
		Floor:       floor,
	}
}

// TestIdlePaneRendersNothing pins that the surface costs no rows until an operation is
// launched, which is what lets the root reserve its height from the pane rather than
// from a constant.
func TestIdlePaneRendersNothing(t *testing.T) {
	m := sized(running.New(keys.Standard), 100)
	if m.Active() {
		t.Errorf("a fresh pane reports itself active with no operation running")
	}
	if got := m.View(); got != "" {
		t.Errorf("an idle pane painted %q, want nothing", got)
	}
	if got := m.Height(); got != 0 {
		t.Errorf("an idle pane occupies %d rows, want 0", got)
	}
}

// TestStartPaintsBeforeTheFirstFrame pins that launching an operation puts the surface
// on screen at once, from the Started handle alone. A Purge's first DELETE is a second
// away at the governor's opening rate, and a surface that appears only on the first
// frame would leave the operator with no evidence the keystroke did anything.
func TestStartPaintsBeforeTheFirstFrame(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 18258, func() {}))
	if !m.Active() {
		t.Fatalf("a started operation left the pane inactive")
	}
	got := m.View()
	if !strings.Contains(got, "18,258") {
		t.Errorf("the opening frame does not carry the frozen total (R15):\n%s", got)
	}
	if m.Height() == 0 {
		t.Errorf("an active pane occupies no rows, so the root would reserve none for it")
	}
}

// TestProgressLineCarriesR15sSixFigures pins R15: deleted against the frozen total,
// skips and failures so far, the current delete rate, elapsed time, and remaining time.
func TestProgressLineCarriesR15sSixFigures(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 18258, func() {}))
	p := frame(18258, 1220, 17026, 10*time.Minute, 1.9, 1.96, 0.5)
	p.Sum.Skipped = 12
	p.Sum.Failures = []ops.FailureGroup{{Reason: "HTTP 500", Count: 3}}
	m, _ = m.Update(p)

	got := m.View()
	for _, want := range []string{"1,220", "18,258", "12", "3", "1.9", "10m"} {
		if !strings.Contains(got, want) {
			t.Errorf("the progress line is missing %q, one of R15's figures:\n%s", want, got)
		}
	}
	if !strings.Contains(got, " to ") {
		t.Errorf("the remaining time is not a range while the rate and the ceiling disagree (R15, AC23):\n%s", got)
	}
}

// TestRemainingIsARangeBetweenTheGovernorsBounds pins AC23's arithmetic: the optimistic
// end derives from the current dynamic ceiling and the pessimistic end never exceeds the
// remaining count divided by the governor's floor. Both ends are read off the rendered
// line, because the line is what the requirement is about.
func TestRemainingIsARangeBetweenTheGovernorsBounds(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 4000, func() {}))
	// 3,600 outstanding at the 2.0/sec ceiling is 30m; at the observed 0.4/sec it would be
	// 2h30m, but the floor of 0.5/sec clamps the pessimistic end at 2h.
	m, _ = m.Update(frame(4000, 400, 3600, time.Minute, 0.4, 2.0, 0.5))
	got := m.View()
	if !strings.Contains(got, "30m to 2h") {
		t.Errorf("remaining range = %q, want the ceiling's 30m to the floor's 2h (R15, AC23)", got)
	}
}

// TestRangeCollapsesToASingleEstimate pins R15's collapse rule and AC23's second half:
// when both ends round to the same displayed figure the line collapses to that figure,
// explicitly presented as an estimate. Nothing but display granularity participates: the
// frame carries no timer and no setting, only the two rates converging.
func TestRangeCollapsesToASingleEstimate(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 4000, func() {}))
	// The observed rate has caught up with the ceiling, so both ends land on 30m.
	m, _ = m.Update(frame(4000, 400, 3600, time.Minute, 2.0, 2.0, 0.5))
	got := m.View()
	if strings.Contains(got, " to ") {
		t.Errorf("the range did not collapse once both ends round alike (R15, AC23):\n%s", got)
	}
	if !strings.Contains(got, "estimate") {
		t.Errorf("the collapsed figure is not labelled an estimate (R15):\n%s", got)
	}
}

// TestNoEstimateWithoutAPacer pins the honest gap: with no governor bounds on the frame
// the surface offers no remaining time at all rather than inventing one. A point figure
// with nothing behind it is the shape R15 exists to forbid.
func TestNoEstimateWithoutAPacer(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 4000, func() {}))
	m, _ = m.Update(frame(4000, 400, 3600, time.Minute, 0, 0, 0))
	if got := m.View(); strings.Contains(got, "remaining") {
		t.Errorf("a remaining time was offered with no governor bounds to derive it from:\n%s", got)
	}
}

// TestCancelKeyStopsTheOperation pins R16: the cancel key reaches the Started handle's
// cancel, which is the context every request is issued under. The pane repaints as
// stopping and does not claim the operation is over until its terminal frame lands.
func TestCancelKeyStopsTheOperation(t *testing.T) {
	stopped := false
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 4000, func() { stopped = true }))
	m, _ = m.Update(frame(4000, 400, 3600, time.Minute, 1.0, 2.0, 0.5))
	m, cmd := m.Update(press("ctrl+x"))
	if cmd == nil {
		t.Fatalf("the cancel key issued no command; nothing would stop the operation (R16)")
	}
	cmd()
	if !stopped {
		t.Errorf("the cancel key did not reach the operation's cancel; a repaint is not a stop (R16)")
	}
	if !m.Active() {
		t.Errorf("the pane went inactive on the keystroke; a cancelled Purge is over when its terminal frame lands, not before (R16)")
	}
}

// TestSummaryGroupsFailuresAndNamesTheRetryKey pins R22 and AC18: the end-of-operation
// summary groups failures by reason with a count for each, and names the single keystroke
// that re-attempts only them.
func TestSummaryGroupsFailuresAndNamesTheRetryKey(t *testing.T) {
	m := sized(running.New(keys.Standard).WithRetrier(&retrier{}), 100).Start(started(ops.OpDelete, 10, func() {}))
	m, _ = m.Update(ops.Progress{
		Op:   ops.OpDelete,
		Done: true,
		Sum: ops.Summary{
			Total: 10, Deleted: 6, Gone: 1, Skipped: 1,
			Failures: []ops.FailureGroup{
				{Reason: "HTTP 403: Must have admin rights to Repository.", Count: 1},
				{Reason: "HTTP 500", Count: 1},
			},
		},
	})
	got := m.View()
	if !strings.Contains(got, "1 x failed: HTTP 500") || !strings.Contains(got, "1 x failed: HTTP 403") {
		t.Errorf("the summary does not group failures by reason with a count each (R22, AC18):\n%s", got)
	}
	if !strings.Contains(got, "ctrl+r") {
		t.Errorf("the summary does not name the retry keystroke (R22):\n%s", got)
	}
	if m.Running() {
		t.Errorf("the pane still reports the operation running after its terminal frame")
	}
	if !m.Active() {
		t.Errorf("the summary is not on screen; R22's retry has nothing to be offered from")
	}
}

// TestSummaryWithoutFailuresOffersNoRetry pins that the retry key is offered only where
// there is something to re-attempt, so a clean pass's summary does not advertise a
// keystroke that would do nothing.
func TestSummaryWithoutFailuresOffersNoRetry(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 3, func() {}))
	m, _ = m.Update(ops.Progress{Op: ops.OpDelete, Done: true, Sum: ops.Summary{Total: 3, Deleted: 3}})
	if got := m.View(); strings.Contains(got, "ctrl+r") {
		t.Errorf("a clean pass's summary advertises the retry key:\n%s", got)
	}
}

// TestDismissClearsTheSummary pins that the same key that stops a running operation
// dismisses its summary once it has finished, so the strip does not sit on the operator's
// screen for the rest of the session.
func TestDismissClearsTheSummary(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 3, func() {}))
	m, _ = m.Update(ops.Progress{Op: ops.OpDelete, Done: true, Sum: ops.Summary{Total: 3, Deleted: 3}})
	m, _ = m.Update(press("ctrl+x"))
	if m.Active() {
		t.Errorf("the dismiss key left the summary on screen")
	}
	if got := m.View(); got != "" {
		t.Errorf("a dismissed pane painted %q, want nothing", got)
	}
}

// retrier records the Summary handed to Retry, standing in for the ops engine.
type retrier struct {
	got   ops.Summary
	calls int
}

func (r *retrier) Retry(_ context.Context, sum ops.Summary) (ops.Started, error) {
	r.calls++
	r.got = sum
	return ops.Started{Op: ops.OpDelete, Total: sum.FailedCount(), Cancel: func() {}}, nil
}

// TestRetryKeyReAttemptsTheRecordedFailures pins R22's keystroke end to end at the pane's
// seam: it hands the finished pass's Summary to the engine, which is what carries the
// recorded failures, and the resulting Started comes back as a message the root routes
// like any other launch.
func TestRetryKeyReAttemptsTheRecordedFailures(t *testing.T) {
	r := &retrier{}
	m := sized(running.New(keys.Standard).WithRetrier(r), 100).Start(started(ops.OpDelete, 10, func() {}))
	final := ops.Progress{
		Op: ops.OpDelete, Done: true,
		Sum: ops.Summary{Total: 10, Deleted: 8, Failures: []ops.FailureGroup{{Reason: "HTTP 500", Count: 2}}},
	}
	m, _ = m.Update(final)
	_, cmd := m.Update(press("ctrl+r"))
	if cmd == nil {
		t.Fatalf("the retry key issued no command (R22)")
	}
	msg := cmd()
	if r.calls != 1 {
		t.Fatalf("Retry was called %d times, want once", r.calls)
	}
	if r.got.FailedCount() != 2 {
		t.Errorf("Retry was handed a Summary recording %d failures, want the pass's 2 (R22)", r.got.FailedCount())
	}
	st, ok := msg.(ops.Started)
	if !ok {
		t.Fatalf("the retry command returned %T, want an ops.Started the root routes like any launch", msg)
	}
	if st.Total != 2 {
		t.Errorf("the retry's frozen total is %d, want the 2 recorded failures", st.Total)
	}
}

// TestRetryKeyInertWhileRunning pins that the retry key does nothing while the operation
// is still going: R22's set is the recorded failures of a finished pass, and a running
// pass has not finished recording them.
func TestRetryKeyInertWhileRunning(t *testing.T) {
	r := &retrier{}
	m := sized(running.New(keys.Standard).WithRetrier(r), 100).Start(started(ops.OpDelete, 10, func() {}))
	m, _ = m.Update(frame(10, 2, 8, time.Minute, 1.0, 2.0, 0.5))
	if _, cmd := m.Update(press("ctrl+r")); cmd != nil {
		t.Errorf("the retry key acted mid-operation (R22)")
	}
	if r.calls != 0 {
		t.Errorf("Retry was called mid-operation")
	}
}

// TestStoppedEarlyStatesWhyItStopped pins that a pass that halted before the whole set
// says so on its face: a circuit break, a log failure and a cancellation each carry a
// reason, and R29's log failure in particular must never be a silent stop (R21, R29, AC20).
func TestStoppedEarlyStatesWhyItStopped(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 100, func() {}))
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Done: true,
		Sum: ops.Summary{Total: 100, Deleted: 40, LogFailed: true, Reason: "write deletions.log: no space left on device"},
	})
	got := m.View()
	if !strings.Contains(got, "no space left on device") {
		t.Errorf("the summary does not name the log as the reason it stopped (R29, AC20):\n%s", got)
	}
}
