package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// wiringConfig is a resolved config with every default, for the root's Settings-pane
// wiring tests.
func wiringConfig() config.Config {
	return config.Config{
		Budget:                  config.TierNormal,
		ConfirmThreshold:        50,
		BreakerFailures:         50,
		DiscoveryRefreshMinutes: 5,
		KeybindingProfile:       config.KeybindingStandard,
		WorkflowsScope:          config.ScopeAllRepos,
		StorageScope:            config.ScopeAllRepos,
	}
}

// TestSettingsKeyOpensThePane pins ADR-0011 and R2: the Settings key opens the root's own
// pane over the focused tab, and the frame then shows the settings view rather than the
// tab. It replaces the stage-0 stub that made the key a no-op until this stage.
func TestSettingsKeyOpensThePane(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if m.settings.IsOpen() {
		t.Fatal("the Settings pane started open; it must open only on the Settings key")
	}
	m = step(t, m, press(",")) // the Settings binding
	if !m.settings.IsOpen() {
		t.Fatal("the Settings key did not open the pane (R2, ADR-0011)")
	}
	if got := m.View().Content; !strings.Contains(got, "Settings") || !strings.Contains(got, "Keybinding profile") {
		t.Fatalf("an open Settings pane did not render its view:\n%s", got)
	}
}

// TestKeysRouteToOpenSettingsPane pins that while the pane is open every key reaches it,
// not the tab underneath: a global navigation key does not switch tabs, and the pane's own
// motion moves its cursor (ADR-0011: settings is focus resolution's one exception).
func TestKeysRouteToOpenSettingsPane(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, press(","))

	firstKey := m.settings.CursorKey()
	m = step(t, m, press("down")) // moves the pane cursor, not the tab focus
	if m.active != 0 {
		t.Errorf("a key switched tabs while Settings was open: active=%d, want 0", m.active)
	}
	if m.settings.CursorKey() == firstKey {
		t.Errorf("a key did not reach the open Settings pane; cursor stayed at %q", firstKey)
	}

	// tab, which would switch tabs when the pane is closed, is inert underneath it.
	m = step(t, m, press("tab"))
	if m.active != 0 {
		t.Errorf("tab switched tabs while Settings was open: active=%d, want 0", m.active)
	}
}

// TestEscClosesSettingsPaneAtRoot pins that esc closes the pane and returns the frame to
// the focused tab, the root's cue read from the pane's IsOpen (ADR-0011).
func TestEscClosesSettingsPaneAtRoot(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, press(","))
	if !m.settings.IsOpen() {
		t.Fatal("precondition: pane did not open")
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.settings.IsOpen() {
		t.Error("esc did not close the Settings pane")
	}
	if got := m.View().Content; strings.Contains(got, "Keybinding profile") {
		t.Errorf("a closed Settings pane still rendered its view:\n%s", got)
	}
}

// TestInterruptQuitsWithSettingsOpen pins R7: ctrl+c quits even while the Settings pane is
// open, the one key that is never routed into a subview.
func TestInterruptQuitsWithSettingsOpen(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = step(t, m, press(","))
	_, cmd := m.Update(press("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c with Settings open produced no command; want tea.Quit (R7)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c with Settings open did not quit; got %T", cmd())
	}
}
