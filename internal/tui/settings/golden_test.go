package settings_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/settings"
)

// The goldens render the Settings view from held state alone, at 100 columns, with no
// terminal and no network (settings R18). lipgloss v2 renders truecolour regardless of the
// environment, so these bytes are stable on any machine (ADR-0013). One of them is AC12's
// absence assertion, made byte-exact here and by name in TestRejectedSettingsNeverAppear.
// Regenerate with: go test ./internal/tui/settings/ -run Golden -update.

// TestGoldenDefaultView fixes the view a fresh install shows: every setting at its default,
// the Budget row focused, the discovery refresh row present (AC12), and no row for any of
// R13's rejected settings or a notification option (R18, ADR-0013).
func TestGoldenDefaultView(t *testing.T) {
	m := settings.New(keys.Standard, defaultConfig(), nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	goldie.New(t).Assert(t, "default_view", []byte(m.View()))
}

// TestGoldenEditingNumber fixes the numeric editor mid-entry: the discovery refresh row is
// being typed, showing the buffer and caret, with the two scopes flipped to this-repo and
// the profile on Vim, so the frame also proves the non-default values render (R12, R19, R20).
func TestGoldenEditingNumber(t *testing.T) {
	cfg := defaultConfig()
	cfg.WorkflowsScope = config.ScopeThisRepo
	cfg.StorageScope = config.ScopeThisRepo
	cfg.KeybindingProfile = config.KeybindingVim
	m := settings.New(keys.Vim, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focusWith(t, m, "j", "discovery_refresh_minutes")
	m = send(m, "enter")
	m = send(m, "1")
	m = send(m, "5")
	goldie.New(t).Assert(t, "editing_number", []byte(m.View()))
}
