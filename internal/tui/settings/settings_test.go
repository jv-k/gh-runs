package settings_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
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
		Theme:                   config.ThemeAuto,
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

// TestCyclesTheme pins settings R6 and R17: the theme selector cycles through exactly the
// three members config validates, wraps, and persists each change. The view and the file
// are the same setting, so a member the loader would reject can never be reached here.
func TestCyclesTheme(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "theme")

	if m.Config().Theme != config.ThemeAuto {
		t.Fatalf("Theme = %q, want the default %q", m.Config().Theme, config.ThemeAuto)
	}
	for _, want := range []config.Theme{config.ThemeDark, config.ThemeLight, config.ThemeAuto} {
		m = send(m, "space")
		if m.Config().Theme != want {
			t.Fatalf("Theme = %q, want %q after a cycle", m.Config().Theme, want)
		}
		if len(r.saved) == 0 || r.last().Theme != want {
			t.Fatalf("cycling the theme to %q did not persist it (R17)", want)
		}
	}
	if m.Config().KeybindingProfile != config.KeybindingStandard {
		t.Errorf("cycling the theme disturbed another setting: %+v", m.Config())
	}
}

// TestThemeEditWritesOnlyThatKey pins settings AC11 through the real persister rather than
// the recorder: cycling the theme in the view and leaving writes theme and nothing else,
// with unrelated comments, key order and unknown keys intact in the file.
func TestThemeEditWritesOnlyThatKey(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "gh-runs")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# My gh-runs config\n" +
		"budget: normal # a share of the allowance\n" +
		"theme: auto\n" +
		"future_thing: 42\n"
	path := filepath.Join(appDir, "config.yml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(key string) (string, bool) {
		if key == "XDG_CONFIG_HOME" {
			return dir, true
		}
		return "", false
	}
	save := func(prev, next config.Config) error { return config.Save(env, prev, next) }

	m := settings.New(keys.Standard, defaultConfig(), save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = send(focus(t, m, "theme"), "space")
	m = send(m, "esc") // leave the pane, as quitting does

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(written)
	if !strings.Contains(got, "theme: dark") {
		t.Errorf("the view did not write the theme it holds (%q):\n%s", m.Config().Theme, got)
	}
	for _, want := range []string{
		"# My gh-runs config",
		"budget: normal",
		"a share of the allowance",
		"future_thing: 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the write discarded %q (R17, AC11):\n%s", want, got)
		}
	}
	if strings.Index(got, "budget") > strings.Index(got, "theme") {
		t.Errorf("the write reordered the keys; budget must stay first:\n%s", got)
	}
}

// TestThemeRowIsRendered pins settings R6, R17 and AC12: the view carries a theme row
// showing the current member and, when focused, the set it cycles through, so the Settings
// view and the config file are the same settings.
func TestThemeRowIsRendered(t *testing.T) {
	view := strings.ToLower(focus(t, open(&recorder{}), "theme").View())
	for _, want := range []string{"theme", "auto", "dark", "light"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Settings view does not render %q on the theme row (R6, AC12):\n%s", want, view)
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
	// reflect.DeepEqual rather than !=: Config carries R7's two repository slices, so it
	// is no longer a comparable struct. The property is unchanged.
	if !reflect.DeepEqual(m.Config(), before) {
		t.Errorf("an unbound key changed a setting")
	}
	if cmd != nil {
		t.Errorf("an unbound key produced a command")
	}
	if len(r.saved) != 0 {
		t.Errorf("an unbound key persisted a change")
	}
}

// TestExcludeRowShowsTheConfiguredList pins settings R17's first sentence for R7's
// exclude key: the view and the config file are the same settings, so a config
// carrying an exclude list shows it, named and reachable by the cursor.
func TestExcludeRowShowsTheConfiguredList(t *testing.T) {
	cfg := defaultConfig()
	cfg.Exclude = []domain.RepoID{repo("jv-k", "noisy"), repo("acme", "vendor")}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	view := m.View()
	for _, want := range []string{"Excluded repositories", "jv-k/noisy", "acme/vendor"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Settings view does not show %q:\n%s", want, view)
		}
	}
	if got := focus(t, m, "exclude").CursorKey(); got != "exclude" {
		t.Errorf("CursorKey after focusing the exclude row = %q", got)
	}
}

// TestEmptyExcludeRowReadsAsNone pins the fresh-install frame: with no config file the
// list is empty (R3, AC1), and the view says so in text rather than painting a blank
// cell, because a blank reads as broken where "none" reads as a setting nobody has used.
func TestEmptyExcludeRowReadsAsNone(t *testing.T) {
	m := settings.New(keys.Standard, defaultConfig(), nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	if view := m.View(); !strings.Contains(view, "none") {
		t.Errorf("the empty exclude list does not read as none:\n%s", view)
	}
}

// TestExcludeRowEditsAndPersists is settings R17 and AC11 reached through the surface
// they name: a change made in the view persists to the file. enter opens the row's
// editor pre-filled with the list as written, typing rewrites it, and enter commits.
// Editing here rather than only in the file is what makes AC11 reachable at all, since
// the pane is the only thing AC11's "in the view" can mean.
func TestExcludeRowEditsAndPersists(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "exclude")

	m = send(m, "enter") // open the editor, empty list so an empty buffer
	for _, k := range strings.Split("jv-k/noisy", "") {
		m = send(m, k)
	}
	m = send(m, "enter") // commit

	want := []domain.RepoID{repo("jv-k", "noisy")}
	if got := m.Config().Exclude; !reflect.DeepEqual(got, want) {
		t.Fatalf("Exclude after the edit = %v, want %v", got, want)
	}
	if len(r.saved) != 1 {
		t.Fatalf("saves = %d, want exactly one", len(r.saved))
	}
	if got := r.last().Exclude; !reflect.DeepEqual(got, want) {
		t.Errorf("persisted Exclude = %v, want %v", got, want)
	}
}

// TestExcludeRowEditPrefillsAndRemoves pins that the gesture removes as well as adds.
// The editor opens on the list as written, so backspacing an entry out of the buffer
// and committing removes it. A gesture that could only append would be a one-way
// ratchet, which is worse than no gesture.
func TestExcludeRowEditPrefillsAndRemoves(t *testing.T) {
	r := &recorder{}
	cfg := defaultConfig()
	cfg.Exclude = []domain.RepoID{repo("jv-k", "noisy"), repo("acme", "vendor")}
	m := settings.New(keys.Standard, cfg, r.save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "exclude")

	m = send(m, "enter")
	// The buffer opens on "jv-k/noisy, acme/vendor". Trim the second entry away.
	for i := 0; i < len("acme/vendor")+2; i++ {
		m = send(m, "backspace")
	}
	m = send(m, "enter")

	want := []domain.RepoID{repo("jv-k", "noisy")}
	if got := m.Config().Exclude; !reflect.DeepEqual(got, want) {
		t.Errorf("Exclude after removing an entry = %v, want %v", got, want)
	}
}

// TestExcludeRowEditRejectsAnUnparseableEntry pins that the editor never admits an
// identity the loader would refuse. A malformed or foreign-host entry is dropped and the
// row says so, rather than being stored as a repository that can never match anything.
func TestExcludeRowEditRejectsAnUnparseableEntry(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "exclude")

	m = send(m, "enter")
	for _, k := range strings.Split("nonsense", "") {
		m = send(m, k)
	}
	m = send(m, "enter")

	if got := m.Config().Exclude; len(got) != 0 {
		t.Errorf("Exclude after an unparseable entry = %v, want empty", got)
	}
	if !strings.Contains(m.View(), "nonsense") {
		t.Errorf("the view does not report the rejected entry:\n%s", m.View())
	}
}

// TestExcludeRowEditCancels pins esc's contract on the row, matching the numeric editor:
// it abandons the edit and leaves the setting as it was, and it does not close the pane.
func TestExcludeRowEditCancels(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "exclude")
	before := m.Config()

	m = send(m, "enter")
	for _, k := range strings.Split("jv-k/noisy", "") {
		m = send(m, k)
	}
	m = send(m, "esc")

	if !reflect.DeepEqual(m.Config(), before) {
		t.Error("esc committed the abandoned edit")
	}
	if !m.IsOpen() {
		t.Error("esc during an edit closed the pane")
	}
	if len(r.saved) != 0 {
		t.Error("an abandoned edit persisted a change")
	}
}

// TestSpaceDoesNotChangeTheExcludeRow pins that the exclude row takes the editor gesture
// and not the selector one: space is the key that cycles a fixed set, and a repository
// list is not one, so it must leave the list alone rather than cycle something.
func TestSpaceDoesNotChangeTheExcludeRow(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "exclude")
	before := m.Config()
	m = send(m, "space")
	if !reflect.DeepEqual(m.Config(), before) {
		t.Error("space changed the exclude row")
	}
	if len(r.saved) != 0 {
		t.Error("space on the exclude row persisted a change")
	}
}

// TestLaunchFilterRowShowsTheConfiguredFilter pins settings R17's first sentence for R9's
// key: the view carries a launch-filter row, and it shows the filter the config holds, in
// the same grammar the Feed's own / input takes.
func TestLaunchFilterRowShowsTheConfiguredFilter(t *testing.T) {
	cfg := defaultConfig()
	cfg.LaunchFilter = filter.Filter{
		Branch:      "main",
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
	}
	m := settings.New(keys.Standard, cfg, nil).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view := focus(t, m, "launch_filter").View()

	for _, want := range []string{"Launch filter", "branch:main", "status:failure"} {
		if !strings.Contains(view, want) {
			t.Errorf("the launch filter row does not show %q:\n%s", want, view)
		}
	}
}

// TestEmptyLaunchFilterRowReadsAsNone pins the fresh-install frame: with no config file the
// launch filter is empty (R3, AC1), and the row says so rather than painting a blank cell.
func TestEmptyLaunchFilterRowReadsAsNone(t *testing.T) {
	view := focus(t, open(&recorder{}), "launch_filter").View()
	if !strings.Contains(view, "Launch filter") || !strings.Contains(view, "none") {
		t.Errorf("an empty launch filter did not read as none:\n%s", view)
	}
}

// TestLaunchFilterRowEditsAndPersists is settings R17 and AC11 reached through the surface
// AC11 names: the gesture is in the view. enter opens the editor, the line is typed, enter
// commits, and the pane persists the parsed Filter rather than the text.
func TestLaunchFilterRowEditsAndPersists(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "launch_filter")

	m = send(m, "enter") // open the editor, empty filter so an empty buffer
	m = typeLine(m, "branch:main")
	m = send(m, "enter") // commit

	want := filter.Filter{Branch: "main"}
	if got := m.Config().LaunchFilter; !reflect.DeepEqual(got, want) {
		t.Fatalf("LaunchFilter after the edit = %+v, want %+v", got, want)
	}
	if len(r.saved) != 1 {
		t.Fatalf("saves = %d, want exactly one", len(r.saved))
	}
	if got := r.last().LaunchFilter; !reflect.DeepEqual(got, want) {
		t.Errorf("persisted LaunchFilter = %+v, want %+v", got, want)
	}
}

// TestLaunchFilterEditTypesTheSeparator pins the character the grammar is written with.
// KeyPressMsg.String() answers "space" for the space bar, so a predicate over it would
// silently refuse the one key that separates two clauses, and a two-clause filter would be
// untypeable. The buffer reads Text, so the space arrives.
func TestLaunchFilterEditTypesTheSeparator(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "launch_filter")

	m = send(m, "enter")
	m = typeLine(m, "branch:main")
	m = send(m, "space") // the key whose String() is a name, not a character
	m = typeLine(m, "actor:octocat")
	m = send(m, "enter")

	want := filter.Filter{Branch: "main", Actor: "octocat"}
	if got := m.Config().LaunchFilter; !reflect.DeepEqual(got, want) {
		t.Errorf("LaunchFilter = %+v, want %+v (the separator must reach the buffer)", got, want)
	}
}

// TestLaunchFilterRowEditPrefillsAndClears pins that the gesture removes as well as sets.
// The editor opens on the filter as it stands, so backspacing it away and committing an
// empty line is how a launch filter is taken off, and one gesture does both.
func TestLaunchFilterRowEditPrefillsAndClears(t *testing.T) {
	r := &recorder{}
	cfg := defaultConfig()
	cfg.LaunchFilter = filter.Filter{Branch: "main"}
	m := settings.New(keys.Standard, cfg, r.save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "launch_filter")

	m = send(m, "enter") // opens pre-filled with "branch:main"
	for range "branch:main" {
		m = send(m, "backspace")
	}
	m = send(m, "enter")

	if got := m.Config().LaunchFilter; !reflect.DeepEqual(got, filter.Filter{}) {
		t.Errorf("LaunchFilter after clearing the line = %+v, want empty", got)
	}
	if len(r.saved) != 1 {
		t.Fatalf("saves = %d, want exactly one", len(r.saved))
	}
}

// TestLaunchFilterRowRejectsAnUnparseableLine pins that the view never holds a filter the
// Feed could not state: a line filter.ParseQuery refuses leaves the setting exactly as it
// was, persists nothing, and is named in the frame rather than swallowed (R14's rule
// applied to input arriving by keystroke).
func TestLaunchFilterRowRejectsAnUnparseableLine(t *testing.T) {
	r := &recorder{}
	cfg := defaultConfig()
	cfg.LaunchFilter = filter.Filter{Branch: "main"}
	m := settings.New(keys.Standard, cfg, r.save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = focus(t, m, "launch_filter")

	m = send(m, "enter") // opens pre-filled with "branch:main"
	m = send(m, "space")
	m = typeLine(m, "nonsense")
	m = send(m, "enter")

	// The good clause is not adopted either: ParseQuery rejects the line, and half a filter
	// is a narrowing nobody stated.
	if got := m.Config().LaunchFilter; !reflect.DeepEqual(got, cfg.LaunchFilter) {
		t.Errorf("LaunchFilter = %+v, want the filter left as it was", got)
	}
	if len(r.saved) != 0 {
		t.Errorf("a rejected line persisted %d writes, want none", len(r.saved))
	}
	if !strings.Contains(m.View(), "nonsense") {
		t.Errorf("the frame does not name the value it refused:\n%s", m.View())
	}
}

// TestLaunchFilterRowEditCancels pins esc's contract on the row, matching the numeric and
// list editors: it abandons the entry and leaves the setting alone, and does not close the
// pane out from under the operator.
func TestLaunchFilterRowEditCancels(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "launch_filter")

	m = send(m, "enter")
	m = typeLine(m, "branch:main")
	m = send(m, "esc")

	if got := m.Config().LaunchFilter; !reflect.DeepEqual(got, filter.Filter{}) {
		t.Errorf("LaunchFilter after esc = %+v, want it unchanged", got)
	}
	if !m.IsOpen() {
		t.Error("esc cancelling an edit closed the pane")
	}
	if len(r.saved) != 0 {
		t.Errorf("a cancelled edit persisted %d writes, want none", len(r.saved))
	}
}

// TestSpaceDoesNotChangeTheLaunchFilterRow pins that the row takes the editor gesture and
// not the selector one: space cycles a fixed set, and a filter is not one, so outside an
// edit it must leave the setting alone.
func TestSpaceDoesNotChangeTheLaunchFilterRow(t *testing.T) {
	r := &recorder{}
	m := focus(t, open(r), "launch_filter")
	before := m.Config()
	m = send(m, "space")
	if !reflect.DeepEqual(m.Config(), before) {
		t.Error("space changed the launch filter row")
	}
	if len(r.saved) != 0 {
		t.Error("space on the launch filter row persisted a change")
	}
}

// TestLaunchFilterEditWritesOnlyThatKey is AC11 end to end for R9: the filter is edited in
// the view, the pane is left as quitting leaves it, and config.yml carries the new key with
// every unrelated comment, key and ordering intact.
func TestLaunchFilterEditWritesOnlyThatKey(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "gh-runs")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# My gh-runs config\n" +
		"budget: normal # a share of the allowance\n" +
		"theme: auto\n" +
		"future_thing: 42\n"
	path := filepath.Join(appDir, "config.yml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(key string) (string, bool) {
		if key == "XDG_CONFIG_HOME" {
			return dir, true
		}
		return "", false
	}
	save := func(prev, next config.Config) error { return config.Save(env, prev, next) }

	m := settings.New(keys.Standard, defaultConfig(), save).Open()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = send(focus(t, m, "launch_filter"), "enter")
	m = typeLine(m, "branch:main")
	m = send(m, "space")
	m = typeLine(m, "failure")
	m = send(m, "enter")
	m = send(m, "esc") // leave the pane, as quitting does
	if m.IsOpen() {
		t.Fatal("esc after the commit did not leave the pane")
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(written)
	for _, want := range []string{"launch_filter:", "branch: main", "conclusion:", "- failure"} {
		if !strings.Contains(got, want) {
			t.Errorf("the view did not write %q:\n%s", want, got)
		}
	}
	for _, want := range []string{
		"# My gh-runs config",
		"budget: normal",
		"a share of the allowance",
		"theme: auto",
		"future_thing: 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the write discarded %q (R17, AC11):\n%s", want, got)
		}
	}
	if strings.Index(got, "budget") > strings.Index(got, "theme") {
		t.Errorf("the write reordered the keys; budget must stay first:\n%s", got)
	}
	// The Conclusion is stored under its own key, never under status, which is the whole of
	// what R9 asks the stored form to keep apart.
	if strings.Contains(got, "status:") {
		t.Errorf("a Conclusion was stored under status, conflating the pair (R9):\n%s", got)
	}
}

// typeLine sends each character of a line as its own key press, which is how a person types
// one. It is the exclude tests' strings.Split spelled once.
func typeLine(m settings.Model, line string) settings.Model {
	for _, c := range strings.Split(line, "") {
		m = send(m, c)
	}
	return m
}

// repo builds a github.com-qualified identity for the tests above (ADR-0009).
func repo(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}
