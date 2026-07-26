package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/tui/running"
)

// rootWithRunning builds a root over recording tabs with the running-operation pane
// wired, sized, and idle.
func rootWithRunning(tabs ...*recordingTab) Model {
	m := Model{
		tabs:    []tab{tabs[0], tabs[1], tabs[2]},
		active:  0,
		profile: keys.Standard,
		running: running.New(keys.Standard),
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return next.(Model)
}

// launched is a Started over a live channel, the shape a tab's launch Cmd returns.
func launched(ch chan ops.Progress, cancel func()) ops.Started {
	return ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Progress: ch, Cancel: cancel}
}

// on tags a frame with the stream it arrived from, which is what the root's adapter does
// and what lets it discard a superseded operation's tail.
func on(ch chan ops.Progress, p ops.Progress) progressFrame {
	return progressFrame{stream: ch, p: p}
}

// TestStartedArmsTheAdapterAndPaints pins ADR-0015's write half at the root: the first
// message hands over the channel, the root adapts it with the same receive-one loop as
// the engine's, and the surface is on screen from that moment.
func TestStartedArmsTheAdapterAndPaints(t *testing.T) {
	m := rootWithRunning(&recordingTab{title: "Runs"}, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, cmd := m.Update(launched(ch, func() {}))
	m = next.(Model)

	if !m.running.Active() {
		t.Fatalf("the launch left the running surface off screen")
	}
	if !strings.Contains(m.View().Content, "18,258") {
		t.Errorf("the root does not paint the running surface over the focused tab (R15):\n%s", m.View().Content)
	}
	if cmd == nil {
		t.Fatalf("the launch armed no adapter; the progress stream would never be read (ADR-0015)")
	}
	// The adapter is the receive-one-then-reschedule command: it blocks on the channel and
	// returns what it received as a message.
	want := ops.Progress{Op: ops.OpDelete, Sum: ops.Summary{Total: 18258, Deleted: 7}}
	ch <- want
	got := cmd()
	f, ok := got.(progressFrame)
	if !ok || f.p.Sum.Deleted != 7 {
		t.Fatalf("the adapter returned %T (%+v), want the Progress it received", got, got)
	}
	if f.stream != (<-chan ops.Progress)(ch) {
		t.Errorf("the frame is not tagged with the stream it came from; a superseded operation's tail could not be discarded")
	}
}

// TestProgressIsBroadcastAndRearms pins ADR-0015's "progress is broadcast": a Purge
// outlives the operator's attention, so every tab sees the frames and the adapter
// re-arms until the stream closes.
func TestProgressIsBroadcastAndRearms(t *testing.T) {
	t0, t1, t2 := &recordingTab{}, &recordingTab{}, &recordingTab{}
	m := rootWithRunning(t0, t1, t2)
	ch := make(chan ops.Progress, 2)
	next, _ := m.Update(launched(ch, func() {}))
	m = next.(Model)

	before := []int{t0.data, t1.data, t2.data}
	_, cmd := m.Update(on(ch, ops.Progress{Op: ops.OpDelete, Sum: ops.Summary{Total: 18258, Deleted: 7}}))
	for i, tb := range []*recordingTab{t0, t1, t2} {
		if tb.data != before[i]+1 {
			t.Errorf("tab %d received %d data messages, want the progress frame broadcast to it (ADR-0015)", i, tb.data-before[i])
		}
	}
	if cmd == nil {
		t.Fatalf("the root did not re-arm the adapter after a frame; the stream would stall")
	}
}

// TestTerminalFrameStopsTheAdapter pins that the root stops listening once the stream is
// over, so a finished Purge leaves no command blocked on a closed channel.
func TestTerminalFrameStopsTheAdapter(t *testing.T) {
	m := rootWithRunning(&recordingTab{}, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() {}))
	m = next.(Model)

	next, cmd := m.Update(on(ch, ops.Progress{Op: ops.OpDelete, Done: true, Sum: ops.Summary{Total: 3, Deleted: 3}}))
	m = next.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("the root re-armed the adapter past the terminal frame, returning %T", msg)
		}
	}
	if m.running.Running() {
		t.Errorf("the surface still reports the operation running after its terminal frame")
	}
	if !m.running.Active() {
		t.Errorf("the summary left the screen; R22's retry is offered from it")
	}
}

// TestPurgeIsNotModal pins AC10 and R14: with a Purge in flight the Feed still applies
// polled updates and still accepts cursor movement, and every other tab stays reachable.
func TestPurgeIsNotModal(t *testing.T) {
	t0, t1, t2 := &recordingTab{title: "Runs"}, &recordingTab{title: "Workflows"}, &recordingTab{title: "Storage"}
	m := rootWithRunning(t0, t1, t2)
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() {}))
	m = next.(Model)

	// A polled update still reaches every tab.
	dataBefore := t0.data
	m = step(t, m, scheduler.Update{Repo: domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}})
	if t0.data <= dataBefore {
		t.Errorf("the Feed stopped receiving polled updates while a Purge ran (R14, AC10)")
	}
	// Cursor movement still reaches the focused tab.
	m = step(t, m, press("down"))
	if len(t0.keys) != 1 {
		t.Errorf("cursor movement did not reach the Feed while a Purge ran (AC10)")
	}
	// A view change still works: the tab bar still switches.
	m = step(t, m, press("tab"))
	if m.active != 1 {
		t.Errorf("tab navigation was blocked while a Purge ran (R14, AC10)")
	}
	if !m.running.Active() {
		t.Errorf("switching tabs dropped the Purge's indicator; it must paint whichever tab is focused (ADR-0015)")
	}
	if !strings.Contains(m.View().Content, "18,258") {
		t.Errorf("the indicator is not painted over the second tab:\n%s", m.View().Content)
	}
}

// TestCancelKeyReachesTheSurface pins R16 at the root: the cancel chord reaches the
// running surface rather than the focused tab, and the tab does not also act on it.
func TestCancelKeyReachesTheSurface(t *testing.T) {
	stopped := false
	t0 := &recordingTab{title: "Runs"}
	m := rootWithRunning(t0, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() { stopped = true }))
	m = next.(Model)

	_, cmd := m.Update(press("ctrl+x"))
	if len(t0.keys) != 0 {
		t.Errorf("the cancel chord also reached the focused tab; two components acted on one keystroke (ADR-0011)")
	}
	if cmd == nil {
		t.Fatalf("the cancel chord issued no command (R16)")
	}
	cmd()
	if !stopped {
		t.Errorf("the cancel chord did not reach the operation's cancel (R16)")
	}
}

// TestRunningKeysReachTheTabWhenIdle pins that the two chords are the surface's only
// while it is up: with nothing running they route to the focused tab like any other key,
// so they are not stolen from a tab that later wants them.
func TestRunningKeysReachTheTabWhenIdle(t *testing.T) {
	t0 := &recordingTab{title: "Runs"}
	m := rootWithRunning(t0, &recordingTab{}, &recordingTab{})
	step(t, m, press("ctrl+x"))
	if len(t0.keys) != 1 {
		t.Errorf("with nothing running, the cancel chord did not route to the focused tab")
	}
}

// TestCancelReachesTheSurfaceWhileATabCaptures pins R16's "at any point while it runs".
// The strip says which key stops the Purge for the whole time a confirm modal or a filter
// input is up, so the key has to work then: an operator typing a count into a second
// modal, watching a Purge they want stopped, must not have the chord swallowed by the
// modal. The capture rule is about q, n and digits being filter text, and a ctrl chord
// never is, which is why ctrl+c is already let through the same way.
func TestCancelReachesTheSurfaceWhileATabCaptures(t *testing.T) {
	stopped := false
	t0 := &recordingTab{title: "Runs", captures: true}
	m := rootWithRunning(t0, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() { stopped = true }))
	m = next.(Model)

	_, cmd := m.Update(press("ctrl+x"))
	if len(t0.keys) != 0 {
		t.Errorf("the capturing tab also received the chord; two components acted on one keystroke (ADR-0011)")
	}
	if cmd == nil {
		t.Fatalf("the cancel chord was swallowed while a tab captured input; R16 says at any point while it runs")
	}
	cmd()
	if !stopped {
		t.Errorf("the cancel chord did not reach the operation's cancel while a tab captured input (R16)")
	}
}

// TestCancelReachesTheSurfaceWhileSettingsIsOpen pins the same for the root's own pane.
// Settings binds neither chord, so nothing is taken from it, and a Purge started before
// the pane was opened stays stoppable.
func TestCancelReachesTheSurfaceWhileSettingsIsOpen(t *testing.T) {
	stopped := false
	m := rootWithRunning(&recordingTab{title: "Runs"}, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() { stopped = true }))
	m = next.(Model)
	m.settings = m.settings.Open()

	_, cmd := m.Update(press("ctrl+x"))
	if cmd == nil {
		t.Fatalf("the cancel chord was swallowed by the Settings pane; R16 says at any point while it runs")
	}
	cmd()
	if !stopped {
		t.Errorf("the cancel chord did not reach the operation's cancel with Settings open (R16)")
	}
}

// TestCapturingTabKeepsItsOwnKeys pins that the rule is unchanged for everything else:
// while the focused tab holds text-input focus the root still takes no global key, so a
// typed count and a filter's text are never stolen (R7, R23).
func TestCapturingTabKeepsItsOwnKeys(t *testing.T) {
	t0 := &recordingTab{title: "Runs", captures: true}
	m := rootWithRunning(t0, &recordingTab{}, &recordingTab{})
	ch := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(ch, func() {}))
	m = next.(Model)
	for _, k := range []string{"q", "1", "5"} {
		m = step(t, m, press(k))
	}
	if len(t0.keys) != 3 {
		t.Errorf("a capturing tab received %d of its 3 keys; the root must take no global key while it captures (R7, R23)", len(t0.keys))
	}
}

// TestSurfaceReservesItsRowsFromTheTabs pins that the strip's height comes off what the
// tabs are laid out in, so a Purge's indicator never overlaps the list it sits above.
func TestSurfaceReservesItsRowsFromTheTabs(t *testing.T) {
	got := make(chan int, 4)
	probe := &sizeProbe{got: got}
	m := Model{tabs: []tab{probe, &recordingTab{}, &recordingTab{}}, profile: keys.Standard, running: running.New(keys.Standard)}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	if h := <-got; h != 40-tabBarHeight {
		t.Fatalf("idle: tab received height %d, want %d", h, 40-tabBarHeight)
	}

	ch := make(chan ops.Progress, 1)
	next, _ = m.Update(launched(ch, func() {}))
	m = next.(Model)
	next, _ = m.Update(on(ch, ops.Progress{
		Op:          ops.OpDelete,
		Sum:         ops.Summary{Total: 18258, Deleted: 100},
		Outstanding: 18158, Elapsed: time.Minute, Rate: 1.0, Ceiling: 2.0, Floor: 0.5,
	}))
	m = next.(Model)
	strip := m.running.Height()
	if strip == 0 {
		t.Fatalf("the running surface reports no height while an operation runs")
	}
	var last int
	for len(got) > 0 {
		last = <-got
	}
	if last != 40-tabBarHeight-strip {
		t.Errorf("with the surface up, the tab was laid out in %d rows, want %d (the strip's %d reserved)", last, 40-tabBarHeight-strip, strip)
	}
}

// TestTheRootNeverLaysATabOutInNegativeRows pins the other half of the strip's bound. The
// root subtracts the strip's rows from what it hands the tabs, and a summary that grew
// without limit would make that subtraction negative: the tabs would be laid out in a
// height no terminal has and the root would paint more rows than the screen holds. The
// group count is bounded by nothing the tool controls, because a transport failure's
// reason embeds the request URL and so carries the Run id.
func TestTheRootNeverLaysATabOutInNegativeRows(t *testing.T) {
	groups := make([]ops.FailureGroup, 200)
	for i := range groups {
		groups[i] = ops.FailureGroup{Reason: "delete request failed: run " + strconv.Itoa(i), Count: 1}
	}
	for _, h := range []int{4, 6, 10, 24, 40} {
		got := make(chan int, 64)
		probe := &sizeProbe{got: got}
		m := Model{tabs: []tab{probe, &recordingTab{}, &recordingTab{}}, profile: keys.Standard, running: running.New(keys.Standard)}
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		m = next.(Model)

		ch := make(chan ops.Progress, 1)
		next, _ = m.Update(launched(ch, func() {}))
		m = next.(Model)
		next, _ = m.Update(on(ch, ops.Progress{
			Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
			Sum: ops.Summary{Total: 5000, Deleted: 4000, Failures: groups, Skips: groups},
		}))
		m = next.(Model)

		for len(got) > 0 {
			if inner := <-got; inner < 0 {
				t.Errorf("at %d rows the root laid a tab out in %d rows", h, inner)
			}
		}
		if rows := strings.Count(m.View().Content, "\n") + 1; rows > h {
			t.Errorf("at %d rows the root painted %d rows; the strip overran the screen (R14, AC10)", h, rows)
		}
	}
}

// TestASupersededStreamsTailIsDiscarded pins the discard-by-tag rule. The engine frees its
// launch gate just before a finished operation's terminal frame goes out, so a second
// operation can be launched while that frame is still in flight. Applying it would mark
// the new operation finished before it had deleted anything, and its cancel would be gone
// with the summary the operator then dismissed.
func TestASupersededStreamsTailIsDiscarded(t *testing.T) {
	m := rootWithRunning(&recordingTab{}, &recordingTab{}, &recordingTab{})
	first := make(chan ops.Progress, 1)
	next, _ := m.Update(launched(first, func() {}))
	m = next.(Model)

	second := make(chan ops.Progress, 1)
	next, _ = m.Update(launched(second, func() {}))
	m = next.(Model)

	// The first stream's terminal frame arrives late.
	next, _ = m.Update(on(first, ops.Progress{Op: ops.OpDelete, Done: true, Sum: ops.Summary{Total: 3, Deleted: 3}}))
	m = next.(Model)
	if !m.running.Running() {
		t.Errorf("a superseded stream's terminal frame finished the operation that replaced it")
	}

	// The current stream's frames still apply.
	next, _ = m.Update(on(second, ops.Progress{Op: ops.OpDelete, Done: true, Sum: ops.Summary{Total: 9, Deleted: 9}}))
	m = next.(Model)
	if m.running.Running() {
		t.Errorf("the current stream's terminal frame was discarded")
	}
}
