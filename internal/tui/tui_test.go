package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// recordingTab is a fake tab that records what the root routed to it, so the routing
// rules are asserted in isolation from any real tab (ADR-0011).
type recordingTab struct {
	title  string
	keys   []string
	data   int // count of non-key, non-size messages
	sizes  int
	active bool
}

func (t *recordingTab) Update(msg tea.Msg) (tab, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyPressMsg:
		t.keys = append(t.keys, v.String())
	case tea.WindowSizeMsg:
		t.sizes++
	default:
		t.data++
	}
	return t, nil
}
func (t *recordingTab) View() string         { return t.title }
func (t *recordingTab) SetActive(a bool) tab { t.active = a; return t }
func (t *recordingTab) Title() string        { return t.title }

func press(s string) tea.KeyPressMsg {
	switch s {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// rootWithTabs builds a root over recording tabs, bypassing New so the routing is tested
// without any real tab.
func rootWithTabs(t0, t1, t2 *recordingTab) Model {
	return Model{
		tabs:    []tab{t0, t1, t2},
		active:  0,
		profile: keys.Standard,
	}
}

func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// TestKeyReachesFocusedTabOnly pins ADR-0011: a key press reaches exactly the focused
// tab, never a second one.
func TestKeyReachesFocusedTabOnly(t *testing.T) {
	t0, t1, t2 := &recordingTab{title: "Runs"}, &recordingTab{title: "Workflows"}, &recordingTab{title: "Storage"}
	m := rootWithTabs(t0, t1, t2)

	m = step(t, m, press("down"))
	if len(t0.keys) != 1 || len(t1.keys) != 0 || len(t2.keys) != 0 {
		t.Fatalf("down reached %d/%d/%d tabs, want only the focused one (ADR-0011)", len(t0.keys), len(t1.keys), len(t2.keys))
	}

	// Switch focus to Workflows and press again: only Workflows receives it.
	m = step(t, m, press("tab"))
	if m.active != 1 {
		t.Fatalf("tab did not move focus: active=%d", m.active)
	}
	m = step(t, m, press("down"))
	if len(t1.keys) != 1 || len(t0.keys) != 1 {
		t.Fatalf("after switch: Runs got %d keys, Workflows %d; want 1 and 1 (each only while focused)", len(t0.keys), len(t1.keys))
	}
}

// TestSizeAndDataReachEveryTab pins ADR-0011 and the reason: an unfocused Feed must keep
// receiving so R33's background reveal and R27's liveness hold.
func TestSizeAndDataReachEveryTab(t *testing.T) {
	t0, t1, t2 := &recordingTab{}, &recordingTab{}, &recordingTab{}
	m := rootWithTabs(t0, t1, t2)

	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	step(t, m, scheduler.Update{Repo: domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}})

	for i, tb := range []*recordingTab{t0, t1, t2} {
		if tb.sizes != 1 {
			t.Errorf("tab %d got %d size messages, want 1 (every tab must be laid out)", i, tb.sizes)
		}
		if tb.data != 1 {
			t.Errorf("tab %d got %d data messages, want 1 (R33, R27)", i, tb.data)
		}
	}
}

// TestSizeReservesTabBar pins that a tab receives the height below the one-line tab bar,
// so it lays out within the space it gets (R4a).
func TestSizeReservesTabBar(t *testing.T) {
	got := make(chan int, 1)
	probe := &sizeProbe{got: got}
	m := Model{tabs: []tab{probe, &recordingTab{}, &recordingTab{}}, profile: keys.Standard}
	step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if h := <-got; h != 40-tabBarHeight {
		t.Fatalf("tab received height %d, want %d (the tab bar's line reserved)", h, 40-tabBarHeight)
	}
}

type sizeProbe struct {
	got chan int
}

func (p *sizeProbe) Update(msg tea.Msg) (tab, tea.Cmd) {
	if s, ok := msg.(tea.WindowSizeMsg); ok {
		p.got <- s.Height
	}
	return p, nil
}
func (p *sizeProbe) View() string       { return "" }
func (p *sizeProbe) SetActive(bool) tab { return p }
func (p *sizeProbe) Title() string      { return "probe" }

// TestQuitOnClosedChannel pins ADR-0015: the root treats a closed engine channel as
// quit.
func TestQuitOnClosedChannel(t *testing.T) {
	m := rootWithTabs(&recordingTab{}, &recordingTab{}, &recordingTab{})
	_, cmd := m.Update(schedulerClosedMsg{})
	if cmd == nil {
		t.Fatal("closed channel produced no command; want tea.Quit (ADR-0015)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("closed channel did not quit; got %T", cmd())
	}
}

// TestQuitKey pins R7: ctrl+c and q quit.
func TestQuitKey(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := rootWithTabs(&recordingTab{}, &recordingTab{}, &recordingTab{})
		_, cmd := m.Update(press(k))
		if cmd == nil {
			t.Fatalf("%q produced no command; want tea.Quit (R7)", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%q did not quit; got %T", k, cmd())
		}
		// A quit key is global and must not reach a tab as a keystroke.
	}
}

// TestTabNavigationWrapsAndSelects pins R2: next and previous wrap the three tabs, and
// 1/2/3 jump by position, and none of these reach a tab as a keystroke.
func TestTabNavigationWrapsAndSelects(t *testing.T) {
	t0, t1, t2 := &recordingTab{}, &recordingTab{}, &recordingTab{}
	m := rootWithTabs(t0, t1, t2)

	m = step(t, m, press("shift+tab")) // previous from 0 wraps to 2
	if m.active != 2 {
		t.Fatalf("shift+tab from 0 gave active=%d, want 2 (wrap)", m.active)
	}
	m = step(t, m, press("2")) // jump to position 2 (index 1)
	if m.active != 1 {
		t.Fatalf("'2' gave active=%d, want 1", m.active)
	}
	if len(t0.keys)+len(t1.keys)+len(t2.keys) != 0 {
		t.Fatalf("a navigation key reached a tab as a keystroke (ADR-0011)")
	}
}

// TestSwitchTabTogglesActive pins that a focus change tells the losing tab to apply what
// it deferred and the gaining tab to freeze again (R10, the root's half of it).
func TestSwitchTabTogglesActive(t *testing.T) {
	t0, t1, t2 := &recordingTab{}, &recordingTab{}, &recordingTab{}
	m := rootWithTabs(t0, t1, t2)
	step(t, m, press("tab")) // 0 -> 1
	if t0.active {
		t.Fatal("the tab losing focus was not deactivated (R10)")
	}
	if !t1.active {
		t.Fatal("the tab gaining focus was not activated")
	}
}

// TestTickBroadcastsReadoutOnChange pins ADR-0015: the root pulls the Budget Readout on
// the coarse tick and broadcasts it, so exhaustion reaches every tab (R30).
func TestTickBroadcastsReadoutOnChange(t *testing.T) {
	reads := 0
	m := Model{
		tabs:    []tab{&recordingTab{}, &recordingTab{}, &recordingTab{}},
		profile: keys.Standard,
		readout: func() governor.Readout {
			reads++
			return governor.Readout{Exhausted: true, Reset: time.Unix(1000, 0)}
		},
	}
	m = step(t, m, tickMsg{})
	for i, tb := range m.tabs {
		rt := tb.(*recordingTab)
		if rt.data != 1 {
			t.Errorf("tab %d received %d broadcasts on the tick, want 1 Readout (ADR-0015)", i, rt.data)
		}
	}
	// A second tick with the same Readout does not re-broadcast it (on change only).
	m = step(t, m, tickMsg{})
	for i, tb := range m.tabs {
		rt := tb.(*recordingTab)
		if rt.data != 1 {
			t.Errorf("tab %d re-received an unchanged Readout, want broadcast on change only (ADR-0015)", i)
		}
	}
}

// TestFeedAccumulatesWhileInactive is the end-to-end of the routing reason: with the
// real Feed unfocused, a poll still reaches it, so switching back shows the revealed Run
// (R33, R27).
func TestFeedAccumulatesWhileInactive(t *testing.T) {
	setViewportCalls := 0
	m := New(Options{
		Profile:     keys.Standard,
		SetViewport: func([]domain.RepoID) { setViewportCalls++ },
	})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, press("tab")) // focus Workflows; the Feed is now unfocused
	if m.active != 1 {
		t.Fatalf("focus did not move to Workflows: active=%d", m.active)
	}
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	m = step(t, m, scheduler.Update{Repo: id, Runs: []domain.Run{
		{ID: 42, Name: "CI", WorkflowName: "CI", Status: domain.StatusInProgress, Repo: id, RunStartedAt: time.Unix(1000, 0)},
	}})

	ft, ok := m.tabs[0].(feedTab)
	if !ok {
		t.Fatal("tab 0 is not the Feed")
	}
	if !strings.Contains(ft.m.View(), "acme/api") {
		t.Fatalf("unfocused Feed did not accumulate the revealed Run (R33, R27):\n%s", ft.m.View())
	}
}
