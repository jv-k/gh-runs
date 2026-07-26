package settings_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/tui/settings"
)

// The goldens render the Settings view from held state alone, at 100 columns, with no
// terminal and no network (settings R18). lipgloss v2 renders truecolour regardless of the
// environment, so these bytes are stable on any machine (ADR-0013). One of them is AC12's
// absence assertion, made byte-exact here and by name in TestRejectedSettingsNeverAppear.
// Regenerate with: go test ./internal/tui/settings/ -run Golden -update.
//
// **No test in this package may take t.Parallel.** The palette's appearance is ambient
// (ADR-0011), so every golden but the light one is taken under the default dark set, and a
// parallel test holding Light would paint another test's frame in the wrong palette. The
// failure is a golden diff of colour bytes with nothing in it to say why.

// TestGoldenDefaultView fixes the view a fresh install shows: every setting at its default,
// the Budget row focused, the discovery refresh row present (AC12), and no row for any of
// R13's rejected settings or a notification option (R18, ADR-0013).
func TestGoldenDefaultView(t *testing.T) {
	m := settings.New(keys.Standard, defaultConfig(), nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	goldie.New(t).Assert(t, "default_view", []byte(m.View()))
}

// TestGoldenThemeFocused fixes the theme row under the cursor: the member in force, the
// three the set offers, and the intent-level description, which is what makes AC12's
// "renders a theme row" byte-exact rather than a claim (settings R6, R18).
func TestGoldenThemeFocused(t *testing.T) {
	m := settings.New(keys.Standard, defaultConfig(), nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focusWith(t, m, "down", "theme")
	goldie.New(t).Assert(t, "theme_focused", []byte(m.View()))
}

// TestGoldenLightPalette fixes the same frame under the light palette, which is the theme
// setting's whole observable effect (settings R6). Every other golden in the suite is taken
// under the dark palette, the default, so this one is where the light set is pinned: the
// text and the layout are the dark golden's, and every colour differs.
func TestGoldenLightPalette(t *testing.T) {
	// The default is left behind rather than restored, so a stray appearance stops here
	// rather than walking forward into the next test.
	defer palette.Use(palette.Dark)
	palette.Use(palette.Light)

	m := settings.New(keys.Standard, defaultConfig(), nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focusWith(t, m, "down", "theme")
	goldie.New(t).Assert(t, "theme_focused_light", []byte(m.View()))
}

// TestGoldenEditingNumber fixes the numeric editor mid-entry: the discovery refresh row is
// being typed, showing the buffer and caret, with the two scopes flipped to this-repo, the
// profile on Vim and the theme on dark, so the frame also proves the non-default values
// render (R6, R12, R19, R20).
func TestGoldenEditingNumber(t *testing.T) {
	cfg := defaultConfig()
	cfg.WorkflowsScope = config.ScopeThisRepo
	cfg.StorageScope = config.ScopeThisRepo
	cfg.KeybindingProfile = config.KeybindingVim
	cfg.Theme = config.ThemeDark
	m := settings.New(keys.Vim, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focusWith(t, m, "j", "discovery_refresh_minutes")
	m = send(m, "enter")
	m = send(m, "1")
	m = send(m, "5")
	goldie.New(t).Assert(t, "editing_number", []byte(m.View()))
}

// TestGoldenExcludeList fixes the frame R7's exclude row paints when it is set: the row
// focused with its description and its edit hint, naming as many repositories as the
// value column holds and counting the rest. It is the reference account's shape, where
// 163 repositories are discovered and roughly 10 are wanted, so a list that overflows the
// column is what a real config produces rather than a corner.
func TestGoldenExcludeList(t *testing.T) {
	cfg := defaultConfig()
	cfg.Exclude = []domain.RepoID{
		repo("jv-k", "dotfiles"),
		repo("jv-k", "scratch"),
		repo("acme", "vendored-fork"),
		repo("acme", "archive"),
		repo("acme", "legacy-pipeline"),
	}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "exclude")
	goldie.New(t).Assert(t, "exclude_list", []byte(m.View()))
}

// TestGoldenLaunchFilter fixes the frame R9's launch-filter row paints when it is set: the
// row focused with its description and its edit hint, and the filter rendered in the
// grammar the Feed's own / input takes. It carries a Status and a Conclusion at once, which
// is the case the stored form keeps apart and the displayed line deliberately does not: the
// input is permissive by R23, and it is the file that holds the two in distinct fields.
func TestGoldenLaunchFilter(t *testing.T) {
	cfg := defaultConfig()
	cfg.LaunchFilter = filter.Filter{
		Branch:      "main",
		Statuses:    []domain.Status{domain.StatusQueued},
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
	}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "launch_filter")
	goldie.New(t).Assert(t, "launch_filter", []byte(m.View()))
}

// TestGoldenEditingLaunchFilter fixes the launch-filter editor mid-entry (R9, R17): the row
// is open on the filter as written with a second clause being typed, showing the buffer and
// the caret. This is the frame AC11's gesture goes through, so it is the one worth pinning
// byte for byte.
func TestGoldenEditingLaunchFilter(t *testing.T) {
	cfg := defaultConfig()
	cfg.LaunchFilter = filter.Filter{Branch: "main"}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "launch_filter")
	m = send(m, "enter")
	for _, k := range []string{"space", "a", "c", "t", "o", "r", ":", "o", "c", "t", "o"} {
		m = send(m, k)
	}
	goldie.New(t).Assert(t, "editing_launch_filter", []byte(m.View()))
}

// TestGoldenEditingExcludeList fixes the exclude editor mid-entry (R7, R17): the row is
// open on the list as written with a new entry being typed, showing the buffer and the
// caret. This is the frame AC11's gesture goes through, so it is the one worth pinning
// byte for byte.
func TestGoldenEditingExcludeList(t *testing.T) {
	cfg := defaultConfig()
	cfg.Exclude = []domain.RepoID{repo("jv-k", "dotfiles")}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "exclude")
	m = send(m, "enter")
	for _, k := range []string{",", " ", "a", "c", "m", "e", "/", "x"} {
		m = send(m, k)
	}
	goldie.New(t).Assert(t, "editing_exclude_list", []byte(m.View()))
}
