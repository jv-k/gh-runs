package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// These pin settings R10 and R17 at the root, in the shape of TestThemeChangeAppliesImmediately
// next door: cycling the Timestamps row repaints the Feed's STARTED column from the next
// frame rather than at the next launch. The mechanism is the root pulling the pane's held
// config after every key it routed there, so no message class carries the change and
// live-run-feed R33's routing is untouched.

// tsNow is the instant the Feed's clock reports, and tsStarted is three hours before it, so
// the relative form is the fixed string "3h" and the absolute form is a fixed instant.
var (
	tsNow      = time.Date(2026, 7, 15, 19, 39, 0, 0, time.UTC)
	tsStarted  = tsNow.Add(-3 * time.Hour)
	tsAbsolute = "2026-07-15T16:39:00Z"
)

// timestampRoot returns a root over cfg with one Run in the Feed, painted at a size that
// leaves room for the row, and a fake clock so an age is a fixed string.
func timestampRoot(t *testing.T, cfg config.Config) Model {
	t.Helper()
	m := New(Options{Profile: keys.Standard, Config: cfg, Clock: clockwork.NewFakeClockAt(tsNow)})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	m = step(t, m, scheduler.Update{Repo: id, Runs: []domain.Run{
		{ID: 42, Name: "CI", WorkflowName: "CI", Status: domain.StatusInProgress, Repo: id, RunStartedAt: tsStarted},
	}})
	return m
}

// feedFrame is what the Feed paints, read off the root's held tab rather than off the root's
// own frame, so it can be asserted while the Settings pane is over it and while another tab
// is focused.
func feedFrame(t *testing.T, m Model) string {
	t.Helper()
	ft, ok := m.tabs[feedTabIndex].(feedTab)
	if !ok {
		t.Fatal("tab 0 is not the Feed")
	}
	return ft.m.View()
}

// cursorTo walks the Settings pane's cursor to the named row, failing rather than looping if
// the row is not reachable.
func cursorTo(t *testing.T, m Model, key string) Model {
	t.Helper()
	for i := 0; i < 16 && m.settings.CursorKey() != key; i++ {
		m = step(t, m, press("down"))
	}
	if m.settings.CursorKey() != key {
		t.Fatalf("never reached the %q row; cursor at %q", key, m.settings.CursorKey())
	}
	return m
}

// TestTimestampFormatAtLaunchIsTheLoadedConfig pins R10's default and the launch read: the
// first frame carries the loaded config's form, and absolute is what a config that states no
// format resolves to.
func TestTimestampFormatAtLaunchIsTheLoadedConfig(t *testing.T) {
	cfg := wiringConfig()
	cfg.Timestamp = config.TimestampRelative
	if got := feedFrame(t, timestampRoot(t, cfg)); !strings.Contains(got, "3h") {
		t.Errorf("a config resolved to relative did not open in the relative form (R10):\n%s", got)
	}

	cfg.Timestamp = config.TimestampAbsolute
	if got := feedFrame(t, timestampRoot(t, cfg)); !strings.Contains(got, tsAbsolute) {
		t.Errorf("a config resolved to absolute did not open in the absolute form (R10):\n%s", got)
	}
}

// TestTimestampChangeAppliesImmediately is the live half of R17: with the Settings pane open
// over the focused Feed, cycling the Timestamps row repaints the STARTED column on the next
// frame, with no relaunch.
func TestTimestampChangeAppliesImmediately(t *testing.T) {
	m := timestampRoot(t, wiringConfig())
	if got := feedFrame(t, m); !strings.Contains(got, tsAbsolute) {
		t.Fatalf("the Feed did not open in the absolute default (R10):\n%s", got)
	}

	m = step(t, m, press(",")) // open Settings over the Feed
	m = cursorTo(t, m, "timestamp")
	m = step(t, m, press("space")) // absolute to relative

	got := feedFrame(t, m)
	if !strings.Contains(got, "3h") {
		t.Errorf("cycling the Timestamps row did not repaint the STARTED column (R17):\n%s", got)
	}
	if strings.Contains(got, tsAbsolute) {
		t.Errorf("the absolute instant survived the change to relative (R10):\n%s", got)
	}

	m = step(t, m, press("space")) // relative back to absolute
	if got := feedFrame(t, m); !strings.Contains(got, tsAbsolute) {
		t.Errorf("cycling back did not restore the absolute instant (R17):\n%s", got)
	}
}

// TestTimestampChangeReachesAnUnfocusedFeed is the half a message class would have made
// hard: the change is cycled from over another tab, and the Feed carries it before it is
// focused again, without waiting for a poll or a refresh. The root pushes into the tab it
// holds, so focus never enters into it.
func TestTimestampChangeReachesAnUnfocusedFeed(t *testing.T) {
	m := timestampRoot(t, wiringConfig())
	m = step(t, m, press("tab")) // focus Workflows; the Feed is now unfocused
	if m.active == feedTabIndex {
		t.Fatal("focus did not leave the Feed")
	}

	m = step(t, m, press(",")) // open Settings over Workflows
	m = cursorTo(t, m, "timestamp")
	m = step(t, m, press("space")) // absolute to relative
	m = step(t, m, press("esc"))   // close the pane, still over Workflows

	if got := feedFrame(t, m); !strings.Contains(got, "3h") {
		t.Errorf("the unfocused Feed kept the old form (R17, R33):\n%s", got)
	}
	m = step(t, m, press("shift+tab")) // back to Runs
	if m.active != feedTabIndex {
		t.Fatalf("focus did not return to the Feed: active=%d", m.active)
	}
	if got := m.View().Content; !strings.Contains(got, "3h") {
		t.Errorf("the refocused Feed painted the old form (R17):\n%s", got)
	}
}
