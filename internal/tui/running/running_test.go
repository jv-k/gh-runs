package running_test

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/running"
)

// started builds a Started handle over a cancel spy, the shape a launching Cmd hands the
// root (ADR-0015). No ops engine is involved: the pane consumes frames and knows nothing
// about who produced them, which is what makes every frame below fabricable.
func started(op ops.Operation, total int, cancel func()) ops.Started {
	return ops.Started{Op: op, Kind: ops.KindRun, Total: total, Cancel: func() { cancel() }}
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
		Kind:        ops.KindRun,
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

// retrier records the Summary handed to Retry, standing in for the ops engine. err makes
// it refuse, which is what the engine does for a spent re-attempt or a busy gate.
type retrier struct {
	got   ops.Summary
	calls int
	err   error
}

func (r *retrier) Retry(_ context.Context, sum ops.Summary) (ops.Started, error) {
	r.calls++
	r.got = sum
	if r.err != nil {
		return ops.Started{}, r.err
	}
	return ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: sum.FailedCount(), Cancel: func() {}}, nil
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

// longReason is the R19a reclassification's recorded reason carrying a fine-grained PAT's
// verbatim 403, which R13 and AC14a make the expected case rather than a corner. It is 177
// columns, so it is the line that proves the frame is laid out rather than assumed.
const longReason = "still rejected after 3 attempts, so the backoff was abandoned and the Run skipped (R19a). " +
	"The API said: HTTP 403: Resource not accessible by personal access token"

// ansiRE matches the ANSI styling lipgloss emits.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain strips the styling so an assertion reads the words the operator sees rather than
// the escape sequences between them. lipgloss puts one around every styled segment, so a
// count and its label are not adjacent in the raw string even though they are on screen.
func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

// widths returns each rendered line's visible width, ignoring the ANSI styling.
func widths(view string) []int {
	var out []int
	for _, line := range strings.Split(view, "\n") {
		out = append(out, lipgloss.Width(line))
	}
	return out
}

// TestNoLineOverrunsTheFrame pins that the surface lays out to the width it was given.
// A line wider than the frame wraps in the terminal and draws more rows than Height
// reported, so the root reserves too few and the strip overruns the tab beneath it. The
// R19a skip reason is the case that makes it real: it is the expected fine-grained-PAT
// outcome and it renders at 177 columns.
func TestNoLineOverrunsTheFrame(t *testing.T) {
	// The narrow widths are not terminals anyone runs, but they are where a special case
	// used to skip the clamp, and a skipped clamp is a Height that under-reports.
	for _, w := range []int{1, 5, 19, 20, 40, 60, 80, 100, 130} {
		m := sized(running.New(keys.Standard).WithRetrier(&retrier{}), w).Start(started(ops.OpDelete, 18258, func() {}))
		m, _ = m.Update(ops.Progress{
			Op: ops.OpDelete, Done: true,
			Sum: ops.Summary{
				Total: 18258, Deleted: 18000, Skipped: 200,
				Failures: []ops.FailureGroup{{Reason: longReason, Count: 5}},
				Skips:    []ops.FailureGroup{{Reason: longReason, Count: 200}},
				Reason:   "append to " + strings.Repeat("very-long-path/", 12) + "deletions.log: no space left on device",
			},
		})
		view := m.View()
		for i, got := range widths(view) {
			if got > w {
				t.Errorf("at width %d, line %d renders %d columns; it wraps and the root's row reservation is wrong", w, i, got)
			}
		}
		if got, want := m.Height(), len(strings.Split(view, "\n")); got != want {
			t.Errorf("at width %d, Height() = %d but the view holds %d lines", w, got, want)
		}
	}
}

// TestProgressLineFitsTheFrame pins the same property for the live line, which carries a
// count, a rate, an elapsed and a range and is the one on screen for hours.
func TestProgressLineFitsTheFrame(t *testing.T) {
	for _, w := range []int{40, 60, 80} {
		m := sized(running.New(keys.Standard), w).Start(started(ops.OpDelete, 18258, func() {}))
		p := frame(18258, 12204, 6054, 100*time.Minute, 1.93, 1.96, 0.5)
		p.Sum.Gone = 148
		p.Sum.Skipped = 1204
		p.Sum.Failures = []ops.FailureGroup{{Reason: longReason, Count: 37}}
		m, _ = m.Update(p)
		for i, got := range widths(m.View()) {
			if got > w {
				t.Errorf("at width %d, progress line %d renders %d columns", w, i, got)
			}
		}
	}
}

// TestRangeCollapsesAcrossAUnitBoundary pins R15's collapse rule where it matters most:
// at the end of a Purge, when the two ends are seconds apart but fall either side of a
// unit boundary. Comparing rendered strings leaves "60s to 1m" on screen through a
// Purge's last minute, which is the shape the requirement's own reasoning rules out.
func TestRangeCollapsesAcrossAUnitBoundary(t *testing.T) {
	cases := []struct {
		name        string
		outstanding int
		rate        float64
		ceiling     float64
	}{
		// 60 outstanding: 60s at the ceiling, 59s at the observed rate. One second apart,
		// across the minute boundary.
		{"minute boundary", 60, 60.0 / 59.0, 1.0},
		// 3,600 outstanding: 60m at the ceiling, 60m15s at the observed rate. Across the
		// hour boundary.
		{"hour boundary", 3600, 3600.0 / 3615.0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 10000, func() {}))
			m, _ = m.Update(frame(10000, 0, tc.outstanding, time.Minute, tc.rate, tc.ceiling, 0.5))
			got := m.View()
			if strings.Contains(got, " to ") {
				t.Errorf("the range did not collapse across the %s; the ends differ by less than one displayed step (R15, AC23):\n%s", tc.name, got)
			}
			if !strings.Contains(got, "estimate") {
				t.Errorf("the collapsed figure is not labelled an estimate (R15):\n%s", got)
			}
		})
	}
}

// sizedTo lays the pane out in a whole terminal, so the row budget the strip works to has
// a height to derive from.
func sizedTo(m running.Model, w, h int) running.Model {
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// manyGroups builds n distinct failure reasons, the shape a flaky link produces: a
// transport failure's reason embeds the request URL, so it carries the Run id and every
// one is its own group.
func manyGroups(n int) []ops.FailureGroup {
	out := make([]ops.FailureGroup, n)
	for i := range out {
		out[i] = ops.FailureGroup{
			Reason: "delete request failed: Delete \"https://api.github.com/repos/o/r/actions/runs/" +
				strconv.Itoa(4675883901+i) + "\": dial tcp: lookup api.github.com: no such host",
			Count: 1,
		}
	}
	return out
}

// TestSummaryStatesEveryFailureGroup pins R22's "a count for each" where the frame can
// hold them: the summary states every reason rather than the first few, so it does not
// disagree with the CLI's printout of the same Summary.
func TestSummaryStatesEveryFailureGroup(t *testing.T) {
	groups := make([]ops.FailureGroup, 7)
	for i := range groups {
		groups[i] = ops.FailureGroup{Reason: "HTTP 5" + strconv.Itoa(i) + "0", Count: i + 1}
	}
	m := sizedTo(running.New(keys.Standard).WithRetrier(&retrier{}), 100, 40).Start(started(ops.OpDelete, 100, func() {}))
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
		Sum: ops.Summary{Total: 100, Deleted: 72, Failures: groups},
	})
	got := plain(m.View())
	for _, g := range groups {
		if !strings.Contains(got, g.Reason) {
			t.Errorf("the summary omits the group %q; R22 requires a count for each reason", g.Reason)
		}
	}
	if strings.Contains(got, "more reasons") {
		t.Errorf("the summary truncated its failure groups where the frame could hold them (R22):\n%s", got)
	}
}

// TestStripNeverTakesMoreThanItsShareOfTheTerminal pins R14 in the other direction. A
// Purge is not modal, and a strip covering the screen is a modal wearing a different name.
// The group count is bounded by nothing the tool controls: a transport failure's reason
// embeds the request URL, so it carries the Run id and every one is its own group, and the
// breaker counts consecutive failures so an interleaved success resets it. One flaky link
// mid-Purge is hundreds of groups.
func TestStripNeverTakesMoreThanItsShareOfTheTerminal(t *testing.T) {
	// 8 is the height where a share of the terminal falls below what the summary's own
	// fixed lines need, so it is where the budget's floor takes over from its share. An
	// 8-row split pane is a state people have.
	for _, h := range []int{8, 10, 24, 40, 60} {
		for _, n := range []int{5, 40, 200} {
			m := sizedTo(running.New(keys.Standard).WithRetrier(&retrier{}), 100, h).
				Start(started(ops.OpDelete, 5000, func() {}))
			m, _ = m.Update(ops.Progress{
				Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
				Sum: ops.Summary{
					Total: 5000, Deleted: 4000,
					Failures: manyGroups(n),
					Skips:    manyGroups(n),
					Reason:   "circuit breaker tripped after 50 consecutive failures",
				},
			})
			got := m.Height()
			if got > h/2 {
				t.Errorf("at %d rows with %d groups the strip takes %d rows, more than half the terminal; that is a modal (R14, AC10)", h, n, got)
			}
			if got < 2 {
				t.Errorf("at %d rows with %d groups the strip collapsed to %d rows and says nothing", h, n, got)
			}
			// Whatever is dropped, the account and the keystrokes on offer survive.
			view := plain(m.View())
			if !strings.Contains(view, "4,000 deleted") {
				t.Errorf("at %d rows with %d groups the strip lost the tally:\n%s", h, n, view)
			}
			if !strings.Contains(view, "dismiss") {
				t.Errorf("at %d rows with %d groups the strip lost its keystroke hints:\n%s", h, n, view)
			}
		}
	}
}

// TestAnOverflowingSummarySaysSo pins that a capped list does not read as a complete one.
// The reasons that did not fit are stated as a count rather than dropped in silence.
//
// The short heights are where a stop reason takes a row of its own, squeezing the group
// block to nothing: at those heights the count is the only thing left saying that R22's
// per-reason breakdown exists at all, so it is the last line to go rather than the first.
func TestAnOverflowingSummarySaysSo(t *testing.T) {
	for _, h := range []int{8, 10, 16, 24, 40} {
		for _, reason := range []string{"", "stopped by the operator"} {
			m := sizedTo(running.New(keys.Standard).WithRetrier(&retrier{}), 100, h).
				Start(started(ops.OpDelete, 5000, func() {}))
			m, _ = m.Update(ops.Progress{
				Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
				Sum: ops.Summary{
					Total: 5000, Deleted: 4000,
					Failures:  manyGroups(60),
					Cancelled: reason != "",
					Reason:    reason,
				},
			})
			got := plain(m.View())
			if !strings.Contains(got, "more reasons") {
				t.Errorf("at %d rows with reason %q, a capped list did not say how many reasons it dropped:\n%s", h, reason, got)
			}
			if reason != "" && !strings.Contains(got, reason) {
				t.Errorf("at %d rows the summary dropped the reason it stopped (AC20):\n%s", h, got)
			}
		}
	}
}

// TestTheStripKeepsItsAffordancesAtEveryHeight pins D1's harm. The strip persists until it
// is dismissed, so a summary without its keystroke hints leaves the operator with a banner
// and no visible way to clear it or retry. And a launch refused while one runs is reported
// on the last line of the running phase: dropping that line is the "a keystroke that
// silently does nothing" defect this whole surface exists to fix, reintroduced at small
// heights. A blind cut of the row budget takes the tail, which is exactly these lines.
func TestTheStripKeepsItsAffordancesAtEveryHeight(t *testing.T) {
	// 2 is below anything the row budget's floor allows, so it is the height that forces
	// the backstop cut and proves it keeps the tail rather than the head. 3 is where the
	// stop reason still fits but the failure groups no longer do.
	for _, h := range []int{2, 3, 4, 6, 8, 10, 24, 40} {
		// A finished summary that stopped early keeps its hints.
		m := sizedTo(running.New(keys.Standard).WithRetrier(&retrier{}), 100, h).
			Start(started(ops.OpDelete, 5000, func() {}))
		m, _ = m.Update(ops.Progress{
			Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
			Sum: ops.Summary{
				Total: 5000, Deleted: 4000,
				Failures:  manyGroups(12),
				LogFailed: true,
				Reason:    "append to deletions.log: no space left on device",
			},
		})
		got := plain(m.View())
		if !strings.Contains(got, "dismiss") {
			t.Errorf("at %d rows the summary lost its keystroke hints; the strip cannot be cleared:\n%s", h, got)
		}
		// The reason it stopped survives wherever three rows are available, which is every
		// height the row budget's floor covers. Below that the head's tally and the way out
		// are all that fit, and a two-row terminal is not a state to design for.
		if h >= 3 && !strings.Contains(got, "no space left on device") {
			t.Errorf("at %d rows the summary lost the reason it stopped (AC20):\n%s", h, got)
		}

		// A running operation that refused a second launch keeps the refusal note.
		r := sizedTo(running.New(keys.Standard), 100, h).Start(started(ops.OpDelete, 5000, func() {}))
		r, _ = r.Update(frame(5000, 400, 4600, time.Minute, 1.0, 2.0, 0.5))
		r = r.Fail(ops.LaunchFailed{Op: ops.OpDelete, Kind: ops.KindRun, Err: ops.ErrBusy})
		if got := plain(r.View()); !strings.Contains(got, "already running") {
			t.Errorf("at %d rows the refusal note was dropped; the keystroke looks like it did nothing:\n%s", h, got)
		}
	}
}

// TestARefusedRetryKeepsTheOperationsName pins F3's glossary violation. The retry key is
// the one launch path whose refusal carries no Kind of its own, and an empty Kind is a
// real value (Reclamation's mixed Cache-and-Artifact list), so it cannot double as
// unknown. Pressing the retry key twice on a finished Purge, which the single-use cell
// refuses the second time, must not relabel that Purge a Reclamation.
func TestARefusedRetryKeepsTheOperationsName(t *testing.T) {
	r := &retrier{err: ops.ErrNothingToRetry}
	m := sizedTo(running.New(keys.Standard).WithRetrier(r), 100, 40).Start(started(ops.OpDelete, 10, func() {}))
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
		Sum: ops.Summary{Total: 10, Deleted: 8, Failures: []ops.FailureGroup{{Reason: "HTTP 500", Count: 2}}},
	})
	_, cmd := m.Update(press("ctrl+r"))
	if cmd == nil {
		t.Fatalf("the retry key issued no command")
	}
	msg, ok := cmd().(ops.LaunchFailed)
	if !ok {
		t.Fatalf("a refused retry returned %T, want an ops.LaunchFailed", msg)
	}
	if msg.Kind != ops.KindRun {
		t.Errorf("the refusal carries Kind %q, want the Purge's %q; without it the pane falls through to the Reclamation label", msg.Kind, ops.KindRun)
	}
	m = m.Fail(msg)
	got := plain(m.View())
	if !strings.Contains(got, "Purge did not start") {
		t.Errorf("a refused Purge retry is not named a Purge (CONTEXT.md):\n%s", got)
	}
	if strings.Contains(got, "Reclaim") {
		t.Errorf("a refused Purge retry is labelled a Reclamation; the glossary holds those apart (CONTEXT.md):\n%s", got)
	}
}

// TestARefusalNamesItsOwnKindNotTheStripsPreviousOne pins the inverse of the same rule. A
// Reclamation's frozen set mixes Caches and Artifacts, so its Kind is legitimately empty:
// the empty Kind is a real value and cannot double as unknown. A refusal must therefore be
// named from what it carries and never from what the strip happened to be showing, or a
// Reclamation refused on a finished Purge's strip is labelled a Purge. That is the case
// storage-reclamation reaches the moment it wires its launcher and a busy gate refuses it
// (#64), and it is why carrying the previous Kind over would be wrong rather than merely
// redundant.
func TestARefusalNamesItsOwnKindNotTheStripsPreviousOne(t *testing.T) {
	m := sizedTo(running.New(keys.Standard).WithRetrier(&retrier{}), 100, 40).Start(started(ops.OpDelete, 10, func() {}))
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
		Sum: ops.Summary{Total: 10, Deleted: 10},
	})
	// A Reclamation over a mixed Cache-and-Artifact set, refused because one is running.
	m = m.Fail(ops.LaunchFailed{Op: ops.OpDelete, Kind: "", Err: ops.ErrBusy})
	got := plain(m.View())
	if !strings.Contains(got, "Reclaim did not start") {
		t.Errorf("a refused Reclamation is not named one; it took its label from the Purge on screen (CONTEXT.md):\n%s", got)
	}
	if strings.Contains(got, "Purge did not start") {
		t.Errorf("a refused Reclamation is labelled a Purge:\n%s", got)
	}
}

// TestLiveLineKeepsDeletedAndGoneApart pins that the live line does not fold a 404 into
// the deletions. R18 counts a 404 a success and R29's log records it as gone precisely
// because "I deleted it" and "it was already gone" are different facts about the world.
// A line reading "1,228 deleted" over 1,220 deletions and 8 that were already gone is the
// dishonest count this whole tool exists to correct.
func TestLiveLineKeepsDeletedAndGoneApart(t *testing.T) {
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 18258, func() {}))
	p := frame(18258, 1220, 17030, 10*time.Minute, 1.9, 1.96, 0.5)
	p.Sum.Gone = 8
	m, _ = m.Update(p)
	got := plain(m.View())
	if !strings.Contains(got, "1,220 deleted") {
		t.Errorf("the live line does not report the deletions on their own (R18, R29):\n%s", got)
	}
	if strings.Contains(got, "1,228") {
		t.Errorf("the live line folds the 404s into the deletions; they are different facts (R18, R29):\n%s", got)
	}
	if !strings.Contains(got, "8 gone") {
		t.Errorf("the live line does not report the 404s at all (R18):\n%s", got)
	}
}

// TestVerbNamesTheObjectAndNotOnlyTheVerb pins CONTEXT.md's vocabulary against the
// Operation alone. A Purge is a filtered bulk deletion of Runs; the same Operation over
// Caches and Artifacts is a Reclamation, and over a Run's logs it is neither. A surface
// reading only the verb would label a Reclamation a Purge, which is the label #64 would
// inherit.
func TestVerbNamesTheObjectAndNotOnlyTheVerb(t *testing.T) {
	cases := []struct {
		kind ops.Kind
		want string
	}{
		{ops.KindRun, "Purge"},
		{ops.KindCache, "Reclaim"},
		{ops.KindArtifact, "Reclaim"},
		{"", "Reclaim"}, // a mixed Cache-and-Artifact set, which is Reclamation's ordinary list
		{ops.KindLog, "Delete logs"},
	}
	for _, tc := range cases {
		m := sized(running.New(keys.Standard), 100).Start(ops.Started{Op: ops.OpDelete, Kind: tc.kind, Total: 9, Cancel: func() {}})
		if got := plain(m.View()); !strings.Contains(got, tc.want) {
			t.Errorf("a delete over %q renders %q, want it to name %q (CONTEXT.md)", tc.kind, strings.Split(got, "\n")[0], tc.want)
		}
	}
}

// TestARefusedLaunchDoesNotDisturbARunningOne pins the other half of R16's orphaning
// guard. The engine refuses a second launch while one runs, and that refusal arrives here
// as a message: showing it must not replace the running operation's state, because the
// handle this pane holds carries the only cancel the running operation has.
func TestARefusedLaunchDoesNotDisturbARunningOne(t *testing.T) {
	stopped := false
	m := sized(running.New(keys.Standard), 100).Start(started(ops.OpDelete, 4000, func() { stopped = true }))
	m, _ = m.Update(frame(4000, 400, 3600, time.Minute, 1.0, 2.0, 0.5))
	m = m.Fail(ops.LaunchFailed{Op: ops.OpDelete, Kind: ops.KindRun, Err: ops.ErrBusy})

	if !m.Running() {
		t.Fatalf("a refused second launch replaced the running operation's state")
	}
	if got := plain(m.View()); !strings.Contains(got, "400 deleted") {
		t.Errorf("the running operation's progress stopped being painted:\n%s", got)
	}
	if got := plain(m.View()); !strings.Contains(got, "already running") {
		t.Errorf("the refusal is not reported at all; the keystroke would look like it did nothing:\n%s", got)
	}
	_, cmd := m.Update(press("ctrl+x"))
	if cmd == nil {
		t.Fatalf("the cancel key stopped working after a refused launch")
	}
	cmd()
	if !stopped {
		t.Errorf("the cancel no longer reaches the running operation; it was orphaned (R16)")
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
