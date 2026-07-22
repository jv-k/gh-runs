package settings_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/settings"
)

// defaultConfig is the settings a fresh install resolves: every default Load applies
// (settings R3). A test tweaks one field and drives the view over it.
func defaultConfig() config.Config {
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

// recorder captures what the pane persisted, standing in for the config file so a pane
// test asserts the write without touching disk. err lets a test drive the failure path.
type recorder struct {
	saved []config.Config
	err   error
}

func (r *recorder) save(_, next config.Config) error {
	r.saved = append(r.saved, next)
	return r.err
}

func (r *recorder) last() config.Config {
	return r.saved[len(r.saved)-1]
}

// press builds a KeyPressMsg from a key name, mirroring the confirm pane's test helper.
func press(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

func send(m settings.Model, key string) settings.Model {
	m, _ = m.Update(press(key))
	return m
}

// sent drives a key and returns the model and the command it produced.
func sent(m settings.Model, key string) (settings.Model, tea.Cmd) {
	return m.Update(press(key))
}

// open builds an open pane over the default config and the recorder's save, sized to
// 100 columns so the frame matches the golden width (R18).
func open(r *recorder) settings.Model {
	m := settings.New(keys.Standard, defaultConfig(), r.save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// focus moves the cursor to the row whose config key is key using the Standard down
// arrow, so a test names a setting rather than an index.
func focus(t *testing.T, m settings.Model, key string) settings.Model {
	t.Helper()
	return focusWith(t, m, "down", key)
}

// focusWith is focus over an explicit down-motion key, so a Vim-profile pane navigates by
// j where a Standard one navigates by the arrow.
func focusWith(t *testing.T, m settings.Model, downKey, key string) settings.Model {
	t.Helper()
	for i := 0; i < 32; i++ {
		if m.CursorKey() == key {
			return m
		}
		m = send(m, downKey)
	}
	t.Fatalf("never reached setting %q; cursor stuck at %q", key, m.CursorKey())
	return m
}

// TestClosedRendersNothing pins that a closed pane paints an empty frame, so the root
// never shows a stale settings view over a tab.
func TestClosedRendersNothing(t *testing.T) {
	m := settings.New(keys.Standard, defaultConfig(), (&recorder{}).save)
	if m.View() != "" {
		t.Errorf("a closed settings pane rendered %q, want empty", m.View())
	}
}

// TestEscClosesThePane pins that esc closes the pane when nothing is being edited, the
// root's cue to return focus to the tab underneath (ADR-0011).
func TestEscClosesThePane(t *testing.T) {
	m := open(&recorder{})
	if !m.IsOpen() {
		t.Fatal("Open did not open the pane")
	}
	m = send(m, "esc")
	if m.IsOpen() {
		t.Error("esc did not close the pane")
	}
}

// TestCyclesKeybindingProfileLiveAndPersists pins settings R5 and R17: cycling the
// keybinding profile flips it between the two valid values, persists the change, and
// re-binds the pane's own motion at once, so Vim's j moves the cursor immediately.
func TestCyclesKeybindingProfileLiveAndPersists(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "keybinding_profile")

	m = send(m, "space")
	if m.Config().KeybindingProfile != config.KeybindingVim {
		t.Fatalf("KeybindingProfile = %q, want %q after cycling", m.Config().KeybindingProfile, config.KeybindingVim)
	}
	if len(r.saved) == 0 || r.last().KeybindingProfile != config.KeybindingVim {
		t.Fatalf("cycling the profile did not persist vim (R17)")
	}
	// The pane's motion is now Vim: j moves the cursor down to the next setting.
	before := m.CursorKey()
	m = send(m, "j")
	if m.CursorKey() == before {
		t.Errorf("Vim motion did not take effect live; j did not move the cursor (R17)")
	}
}

// TestScopesToggleIndependently pins settings R19: the two tab scopes are settable
// separately, so scoping Workflows leaves Storage alone.
func TestScopesToggleIndependently(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "workflows_scope")

	m = send(m, "space")
	if m.Config().WorkflowsScope != config.ScopeThisRepo {
		t.Errorf("WorkflowsScope = %q, want %q", m.Config().WorkflowsScope, config.ScopeThisRepo)
	}
	if m.Config().StorageScope != config.ScopeAllRepos {
		t.Errorf("StorageScope = %q, want %q; scoping Workflows must leave Storage alone (R19)", m.Config().StorageScope, config.ScopeAllRepos)
	}

	m = focus(t, m, "storage_scope")
	m = send(m, "space")
	if m.Config().StorageScope != config.ScopeThisRepo {
		t.Errorf("StorageScope = %q, want %q", m.Config().StorageScope, config.ScopeThisRepo)
	}
}

// TestCyclesBudgetTier pins that the Budget selector cycles through its three named
// tiers and wraps, the intent-level knob settings R8 admits.
func TestCyclesBudgetTier(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "budget")

	want := []config.Tier{config.TierGreedy, config.TierBackground, config.TierNormal}
	for _, w := range want {
		m = send(m, "space")
		if m.Config().Budget != w {
			t.Fatalf("Budget = %q, want %q after a cycle", m.Config().Budget, w)
		}
	}
}

// TestNumberEditClampsFloor pins settings R20 and AC13's shape: editing the discovery
// refresh to 0 is clamped to the floor of 1, not honoured, and the clamped value
// persists.
func TestNumberEditClampsFloor(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "discovery_refresh_minutes")

	m = send(m, "enter") // begin editing
	m = send(m, "0")
	m = send(m, "enter") // commit

	if got := m.Config().DiscoveryRefreshMinutes; got != 1 {
		t.Errorf("discovery refresh = %d, want the floor of 1 (R20)", got)
	}
	if len(r.saved) == 0 || r.last().DiscoveryRefreshMinutes != 1 {
		t.Errorf("the clamped discovery refresh was not persisted")
	}
}

// TestNumberEditClampsCeiling pins settings R12 and R21: a confirm threshold above 500
// is clamped to 500, and a breaker threshold of 0 is clamped to 1, the two-sided clamp.
func TestNumberEditClampsCeiling(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "confirm_threshold")
	m = send(m, "enter")
	for _, d := range "5000" {
		m = send(m, string(d))
	}
	m = send(m, "enter")
	if got := m.Config().ConfirmThreshold; got != 500 {
		t.Errorf("confirm threshold = %d, want the maximum of 500 (R12)", got)
	}

	m = focus(t, m, "purge_breaker_failures")
	m = send(m, "enter")
	m = send(m, "0")
	m = send(m, "enter")
	if got := m.Config().BreakerFailures; got != 1 {
		t.Errorf("breaker threshold = %d, want the floor of 1 (R21)", got)
	}
}

// TestNumberEditCancelLeavesValue pins that esc while editing abandons the entry and
// leaves the setting unchanged, so a mistyped number costs nothing.
func TestNumberEditCancelLeavesValue(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "discovery_refresh_minutes")
	m = send(m, "enter")
	m = send(m, "9")
	m = send(m, "esc") // cancel the edit, not close the pane
	if !m.IsOpen() {
		t.Error("esc while editing closed the pane; it must cancel the edit")
	}
	if m.Config().DiscoveryRefreshMinutes != 5 {
		t.Errorf("cancelled edit changed the value to %d, want the original 5", m.Config().DiscoveryRefreshMinutes)
	}
	if len(r.saved) != 0 {
		t.Errorf("a cancelled edit persisted %d change(s), want none", len(r.saved))
	}
}

// TestRejectedSettingsNeverAppear pins settings R18 and AC12: none of R13's five
// rejected settings appears anywhere in the rendered view, by key or by prose label.
// This is the guard R18 asks for by name: adding any of the five to the view fails it.
func TestRejectedSettingsNeverAppear(t *testing.T) {
	view := strings.ToLower(open(&recorder{}).View())
	rejected := []string{
		"poll_interval", "poll interval",
		"deletes_per_second", "deletes per second", "delete rate",
		"cache_ttl", "cache ttl",
		"concurrency",
		"skip_confirmation", "skip confirmation",
	}
	for _, bad := range rejected {
		if strings.Contains(view, bad) {
			t.Errorf("the Settings view rendered rejected setting %q; R13's settings are absent, not hidden (R18, AC12)", bad)
		}
	}
	// The discovery refresh row is a different setting and MUST appear (AC12).
	if !strings.Contains(view, "discovery") {
		t.Errorf("the Settings view is missing the discovery refresh row, which AC12 requires it to show")
	}
}

// TestNoNotificationOptions pins the 2.1 deferral (settings R11, ADR-0013): the 2.0.0
// Settings menu carries no notification options, because the subsystem behind them does
// not exist yet and R11's own golden asserts the view never renders them.
func TestNoNotificationOptions(t *testing.T) {
	view := strings.ToLower(open(&recorder{}).View())
	if strings.Contains(view, "notif") {
		t.Errorf("the Settings view rendered a notification option; R11 defers to 2.1 and ships no inert toggle (ADR-0013)")
	}
}

// TestSaveErrorIsSurfaced pins that a failed write is shown rather than swallowed: the
// view states the config could not be saved, so the operator is not misled into
// believing a change persisted (R17's spirit).
func TestSaveErrorIsSurfaced(t *testing.T) {
	r := &recorder{err: errWrite}
	m := focus(t, open(r), "budget")
	m = send(m, "space")
	if !strings.Contains(strings.ToLower(m.View()), "could not") {
		t.Errorf("a failed save was not surfaced in the view:\n%s", m.View())
	}
}

// errWrite is a fixed error the recorder returns to drive the save-failure path.
var errWrite = writeError("disk full")

type writeError string

func (e writeError) Error() string { return string(e) }

// TestEditKeysComeFromRegistry is a smoke check that the pane consumes only messages it
// should: a key it does not bind leaves the config untouched and issues no save, so a
// stray keystroke cannot mutate a setting (R7a's spirit, no literal actions).
func TestEditKeysComeFromRegistry(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "budget")
	before := m.Config()
	m, cmd := sent(m, "z") // unbound
	if m.Config() != before {
		t.Errorf("an unbound key changed a setting")
	}
	if cmd != nil {
		t.Errorf("an unbound key produced a command")
	}
	if len(r.saved) != 0 {
		t.Errorf("an unbound key persisted a change")
	}
}
