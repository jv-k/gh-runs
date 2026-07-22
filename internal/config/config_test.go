package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/config"
)

// envMap returns an Env backed by a map, so a test can inject $XDG_CONFIG_HOME
// and the other lookups Load reads without touching the real environment.
func envMap(m map[string]string) config.Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// writeConfig writes contents to $XDG_CONFIG_HOME/gh-runs/config.yml, the path
// settings R1 fixes for the XDG limb.
func writeConfig(t *testing.T, xdgDir, contents string) {
	t.Helper()
	appDir := filepath.Join(xdgDir, "gh-runs")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.yml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetTierDefaultsToNormal pins settings R3 and AC1 for the Budget tier:
// with $XDG_CONFIG_HOME pointing at a directory that holds no config.yml, Load
// reports the default tier, which the product owner fixed at normal (settings
// resolved open questions, 2026-07-16). A missing file is valid and produces the
// defaults, with no diagnostic.
func TestBudgetTierDefaultsToNormal(t *testing.T) {
	dir := t.TempDir() // empty: no config.yml inside
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.Budget != config.TierNormal {
		t.Fatalf("Budget = %q, want %q", cfg.Budget, config.TierNormal)
	}
}

// TestBudgetTierReadFromFile pins settings R4's file limb: a config.yml naming
// budget: greedy is read, and Load reports that tier in place of the default.
func TestBudgetTierReadFromFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: greedy\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.Budget != config.TierGreedy {
		t.Fatalf("Budget = %q, want %q", cfg.Budget, config.TierGreedy)
	}
}

// TestBudgetFlagBeatsFile pins settings R4's ordering at the flag limb: with a
// config.yml naming greedy and a flag naming background, the flag wins, because
// flags outrank the file.
func TestBudgetFlagBeatsFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: greedy\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{Budget: config.TierBackground})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.Budget != config.TierBackground {
		t.Fatalf("Budget = %q, want %q (flag must beat file)", cfg.Budget, config.TierBackground)
	}
}

// TestBudgetFlagBeatsDefaultWithNoFile pins R4's flag limb when the file source
// is absent: a flag must override the default even with no config.yml, so flag
// resolution cannot be short-circuited by a missing file.
func TestBudgetFlagBeatsDefaultWithNoFile(t *testing.T) {
	dir := t.TempDir() // empty: no config.yml
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{Budget: config.TierGreedy})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.Budget != config.TierGreedy {
		t.Fatalf("Budget = %q, want %q (flag must beat default with no file)", cfg.Budget, config.TierGreedy)
	}
}

// TestInvalidBudgetTierRejected pins the settings validation direction for the
// Budget: an unrecognised tier in the file is not adopted (ADR-0014: Tier is our
// vocabulary, not the API's, so nothing preserves an unknown one). It falls back
// to the default and emits one actionable diagnostic (R14, mirroring AC4), which
// names the offending value and the valid set.
func TestInvalidBudgetTierRejected(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: turbo\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierNormal {
		t.Fatalf("Budget = %q, want the default %q after rejecting an invalid value", cfg.Budget, config.TierNormal)
	}
	if len(diags) != 1 {
		t.Fatalf("want exactly one diagnostic for the invalid value, got %d: %v", len(diags), diags)
	}
	msg := diags[0].Message
	for _, want := range []string{"turbo", "background", "normal", "greedy"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q does not mention %q", msg, want)
		}
	}
}

// TestConfirmThresholdDefaults pins settings R12/AC8: with no config file the
// effective type-the-count threshold is 50.
func TestConfirmThresholdDefaults(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.ConfirmThreshold != 50 {
		t.Fatalf("ConfirmThreshold = %d, want the default 50", cfg.ConfirmThreshold)
	}
}

// TestConfirmThresholdClampedToMax pins settings AC8's clamp: a value above the
// hard maximum of 500 is held at 500, not honoured, with a diagnostic naming the
// key and the bound. The inversion is why: raising the threshold lowers
// protection, so the ceiling is a floor under protection (R12).
func TestConfirmThresholdClampedToMax(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "confirm_threshold: 5000\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.ConfirmThreshold != 500 {
		t.Fatalf("ConfirmThreshold = %d, want it clamped to 500", cfg.ConfirmThreshold)
	}
	if len(diags) != 1 {
		t.Fatalf("want one clamp diagnostic, got %d: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "confirm_threshold") || !strings.Contains(diags[0].Message, "500") {
		t.Fatalf("clamp diagnostic %q should name the key and the bound", diags[0].Message)
	}
}

// TestConfirmThresholdNoLowerBound pins settings AC8's other end: a value of 1 is
// honoured, because there is no lower bound. A person may demand the typed
// confirmation for nearly every deletion.
func TestConfirmThresholdNoLowerBound(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "confirm_threshold: 1\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("an in-range value should emit no diagnostic, got: %v", diags)
	}
	if cfg.ConfirmThreshold != 1 {
		t.Fatalf("ConfirmThreshold = %d, want 1 honoured", cfg.ConfirmThreshold)
	}
}

// TestBreakerThresholdDefaults pins settings R21/AC13: with no config file the
// effective Purge breaker threshold is 50.
func TestBreakerThresholdDefaults(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.BreakerFailures != 50 {
		t.Fatalf("BreakerFailures = %d, want the default 50", cfg.BreakerFailures)
	}
}

// TestBreakerThresholdClampedHigh pins the upper bound (AC13): 5000 clamps to
// 500, the same inversion as the confirm threshold.
func TestBreakerThresholdClampedHigh(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "purge_breaker_failures: 5000\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.BreakerFailures != 500 {
		t.Fatalf("BreakerFailures = %d, want it clamped to 500", cfg.BreakerFailures)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "purge_breaker_failures") {
		t.Fatalf("want one clamp diagnostic naming the key, got: %v", diags)
	}
}

// TestBreakerThresholdClampedLow pins the lower bound (AC13): 0 clamps to 1,
// because no configuration may produce a Purge whose breaker never fires (R21).
func TestBreakerThresholdClampedLow(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "purge_breaker_failures: 0\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.BreakerFailures != 1 {
		t.Fatalf("BreakerFailures = %d, want it clamped up to 1", cfg.BreakerFailures)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "purge_breaker_failures") {
		t.Fatalf("want one clamp diagnostic naming the key, got: %v", diags)
	}
}

// TestDiscoveryRefreshDefaults pins settings R20: with no config file the
// discovery revalidation interval is 5 minutes.
func TestDiscoveryRefreshDefaults(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.DiscoveryRefreshMinutes != 5 {
		t.Fatalf("DiscoveryRefreshMinutes = %d, want the default 5", cfg.DiscoveryRefreshMinutes)
	}
}

// TestDiscoveryRefreshClampedToFloor pins R20's floor: a value under 1 is clamped
// up to 1 with a diagnostic, so the fast re-probe tier can never be configured to
// hammer the API.
func TestDiscoveryRefreshClampedToFloor(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "discovery_refresh_minutes: 0\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.DiscoveryRefreshMinutes != 1 {
		t.Fatalf("DiscoveryRefreshMinutes = %d, want it clamped up to 1", cfg.DiscoveryRefreshMinutes)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "discovery_refresh_minutes") {
		t.Fatalf("want one clamp diagnostic naming the key, got: %v", diags)
	}
}

// TestDiscoveryRefreshHasNoCeiling pins the other side of R20: the key has a floor
// but no maximum, so a large interval is honoured unchanged.
func TestDiscoveryRefreshHasNoCeiling(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "discovery_refresh_minutes: 120\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("a large interval should emit no diagnostic, got: %v", diags)
	}
	if cfg.DiscoveryRefreshMinutes != 120 {
		t.Fatalf("DiscoveryRefreshMinutes = %d, want 120 honoured", cfg.DiscoveryRefreshMinutes)
	}
}

// TestKeybindingProfileDefaults pins settings R5: with no config file the profile
// is Standard, the terminal-native motion set (live-run-feed R7). Vim is opt-in.
func TestKeybindingProfileDefaults(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.KeybindingProfile != config.KeybindingStandard {
		t.Fatalf("KeybindingProfile = %q, want the default %q", cfg.KeybindingProfile, config.KeybindingStandard)
	}
}

// TestKeybindingProfileVim pins that the other of the two values is read.
func TestKeybindingProfileVim(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "keybinding_profile: vim\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("a valid profile should emit no diagnostic, got: %v", diags)
	}
	if cfg.KeybindingProfile != config.KeybindingVim {
		t.Fatalf("KeybindingProfile = %q, want %q", cfg.KeybindingProfile, config.KeybindingVim)
	}
}

// TestKeybindingProfileRejectsThird pins settings R5/AC4: exactly two profiles
// exist, so a third value like mac or windows is rejected with a diagnostic
// listing the two valid values, and the default stands.
func TestKeybindingProfileRejectsThird(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "keybinding_profile: mac\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.KeybindingProfile != config.KeybindingStandard {
		t.Fatalf("KeybindingProfile = %q, want the default %q after rejecting a third value", cfg.KeybindingProfile, config.KeybindingStandard)
	}
	if len(diags) != 1 {
		t.Fatalf("want exactly one diagnostic, got %d: %v", len(diags), diags)
	}
	for _, want := range []string{"mac", "vim", "standard"} {
		if !strings.Contains(diags[0].Message, want) {
			t.Fatalf("diagnostic %q does not mention %q", diags[0].Message, want)
		}
	}
}

// TestUnknownKeyWarnsAndContinues pins settings R14/AC7: an unrecognised key
// produces a generic diagnostic naming it and does not fail the run, so the
// defaults stand and a future 2.1 key can arrive without a migration.
func TestUnknownKeyWarnsAndContinues(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "some_future_key: 1\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierNormal {
		t.Fatalf("an unknown key must not disturb the defaults; Budget = %q", cfg.Budget)
	}
	if len(diags) != 1 {
		t.Fatalf("want one unknown-key diagnostic, got %d: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "some_future_key") {
		t.Fatalf("diagnostic %q should name the unknown key", diags[0].Message)
	}
}

// TestRejectedKeyPollIntervalGetsItsReason pins settings AC7's named case:
// poll_interval starts the run normally and emits a diagnostic carrying R13's
// specific reason (the token tier, the repo count and the points model), not the
// generic unknown-key message. R13's point is that these are refused for stated
// reasons, and someone who reaches for one deserves the reason.
func TestRejectedKeyPollIntervalGetsItsReason(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "poll_interval: 5\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierNormal {
		t.Fatalf("a rejected key must not disturb the defaults; Budget = %q", cfg.Budget)
	}
	if len(diags) != 1 {
		t.Fatalf("want one diagnostic, got %d: %v", len(diags), diags)
	}
	msg := diags[0].Message
	if !strings.Contains(msg, "poll_interval") {
		t.Fatalf("diagnostic %q should name the key", msg)
	}
	for _, want := range []string{"token tier", "repo count", "points model"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("poll_interval diagnostic %q should carry R13's reason (missing %q)", msg, want)
		}
	}
	if strings.Contains(msg, "unrecognised") {
		t.Fatalf("a rejected key got the generic message, not its specific reason: %q", msg)
	}
}

// TestAllRejectedKeysGetSpecificReasons pins the mechanism for all five settings
// R13 refuses. poll_interval is named in AC7; the other four spellings are chosen
// here (deletes_per_second, cache_ttl, concurrency, skip_confirmation). Each must
// start the run and get a specific reason, never the generic unknown-key message,
// because R14 requires the specific diagnostic for exactly these five.
func TestAllRejectedKeysGetSpecificReasons(t *testing.T) {
	for _, key := range []string{"poll_interval", "deletes_per_second", "cache_ttl", "concurrency", "skip_confirmation"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, key+": 1\n")
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			_, diags := config.Load(env, config.Flags{})

			if len(diags) != 1 {
				t.Fatalf("want one diagnostic for %q, got %d: %v", key, len(diags), diags)
			}
			if !strings.Contains(diags[0].Message, key) {
				t.Fatalf("diagnostic %q should name the key %q", diags[0].Message, key)
			}
			if strings.Contains(diags[0].Message, "unrecognised") {
				t.Fatalf("%q got the generic message, not a specific reason: %q", key, diags[0].Message)
			}
		})
	}
}

// TestTypeErrorPreservesValidSiblings pins the fix for the blast radius verify
// found: one field of the wrong type must not discard the other valid settings.
// A config.yml with a valid budget and keybinding_profile plus a string where
// confirm_threshold wants a number keeps the two valid values, falls the bad one
// back to its default, and emits one diagnostic naming the offending key (R14).
func TestTypeErrorPreservesValidSiblings(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: greedy\nkeybinding_profile: vim\nconfirm_threshold: \"oops\"\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierGreedy {
		t.Fatalf("Budget = %q, want greedy preserved despite a sibling's type error", cfg.Budget)
	}
	if cfg.KeybindingProfile != config.KeybindingVim {
		t.Fatalf("KeybindingProfile = %q, want vim preserved despite a sibling's type error", cfg.KeybindingProfile)
	}
	if cfg.ConfirmThreshold != 50 {
		t.Fatalf("ConfirmThreshold = %d, want the default 50 after a type error on that field", cfg.ConfirmThreshold)
	}
	if len(diags) != 1 {
		t.Fatalf("want one diagnostic for the bad field, got %d: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "confirm_threshold") {
		t.Fatalf("diagnostic %q should name the offending key", diags[0].Message)
	}
}

// TestUnparseableFileWarnsAndKeepsDefaults pins that a config.yml the parser
// cannot read (a syntax error) does not fail the run and does not vanish
// silently: the defaults stand and one diagnostic names the file (R3, R14 spirit).
func TestUnparseableFileWarnsAndKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: [greedy\n") // an unclosed flow sequence
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierNormal {
		t.Fatalf("Budget = %q, want the defaults when the file cannot be parsed", cfg.Budget)
	}
	if len(diags) != 1 {
		t.Fatalf("want one parse diagnostic, got %d: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "config.yml") {
		t.Fatalf("parse diagnostic %q should name the file", diags[0].Message)
	}
}

// TestNonMappingRootWarnsAndKeepsDefaults pins the other unreadable shape: a file
// that is valid YAML but not a mapping (a list at the root) yields the defaults
// and one diagnostic, rather than silently defaulting.
func TestNonMappingRootWarnsAndKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "- a\n- b\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.Budget != config.TierNormal {
		t.Fatalf("Budget = %q, want the defaults when the root is not a mapping", cfg.Budget)
	}
	if len(diags) != 1 {
		t.Fatalf("want one diagnostic, got %d: %v", len(diags), diags)
	}
}

// TestEmptyFileChangesNothing pins settings AC1's second clause: writing an empty
// config.yml "changes nothing", producing behaviour identical to no config file
// at all. A comment-only file carries no settings and behaves the same. Comparing
// the two resolved Configs directly, rather than against hand-written defaults,
// keeps this honest as later settings are added.
func TestEmptyFileChangesNothing(t *testing.T) {
	// The baseline AC1 fixes: no config file at all, every setting its default.
	noFile, noFileDiags := config.Load(
		envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()}), config.Flags{})
	if len(noFileDiags) != 0 {
		t.Fatalf("no-file baseline returned diagnostics: %v", noFileDiags)
	}

	for _, contents := range []string{"", "# a comment, and no settings\n"} {
		dir := t.TempDir()
		writeConfig(t, dir, contents)
		cfg, diags := config.Load(
			envMap(map[string]string{"XDG_CONFIG_HOME": dir}), config.Flags{})

		if len(diags) != 0 {
			t.Fatalf("empty/comment-only config returned diagnostics: %v", diags)
		}
		if cfg != noFile {
			t.Fatalf("empty config = %+v, want identical to no file %+v (AC1)", cfg, noFile)
		}
	}
}

// TestReadsFromHomeConfigWhenXDGUnset pins settings R1's default limb: with
// $XDG_CONFIG_HOME unset, the file is read from $HOME/.config/gh-runs/config.yml.
// This is the common case on macOS, where $XDG_CONFIG_HOME is typically unset, so
// a config living at ~/.config would otherwise be silently ignored.
func TestReadsFromHomeConfigWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	appDir := filepath.Join(home, ".config", "gh-runs")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.yml"), []byte("budget: greedy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := envMap(map[string]string{"HOME": home}) // no $XDG_CONFIG_HOME

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.Budget != config.TierGreedy {
		t.Fatalf("Budget = %q, want greedy read from ~/.config/gh-runs when XDG is unset", cfg.Budget)
	}
}
