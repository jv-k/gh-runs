package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/config"
)

// baseConfig is a Config carrying every default Load applies, so a test tweaks the
// one field under scrutiny and leaves the rest matching the loader's defaults. It is
// the prev a Save diffs next against.
func baseConfig() config.Config {
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

// readSaved returns the bytes of the config file Save wrote under xdgDir.
func readSaved(t *testing.T, xdgDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(xdgDir, "gh-runs", "config.yml"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	return string(b)
}

// TestScopesDefaultToAllRepos pins settings R19: both tab scopes default to all-repos,
// and a missing file yields them with no diagnostic (R3).
func TestScopesDefaultToAllRepos(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if cfg.WorkflowsScope != config.ScopeAllRepos {
		t.Errorf("WorkflowsScope = %q, want %q", cfg.WorkflowsScope, config.ScopeAllRepos)
	}
	if cfg.StorageScope != config.ScopeAllRepos {
		t.Errorf("StorageScope = %q, want %q", cfg.StorageScope, config.ScopeAllRepos)
	}
}

// TestScopesAreIndependent pins settings R19's "settable separately": scoping one tab
// leaves the other alone. The file sets only the Storage scope; the Workflows scope
// stays at its default.
func TestScopesAreIndependent(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "storage_scope: this-repo\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.StorageScope != config.ScopeThisRepo {
		t.Errorf("StorageScope = %q, want %q", cfg.StorageScope, config.ScopeThisRepo)
	}
	if cfg.WorkflowsScope != config.ScopeAllRepos {
		t.Errorf("WorkflowsScope = %q, want %q (scoping Storage must leave Workflows alone)", cfg.WorkflowsScope, config.ScopeAllRepos)
	}
}

// TestInvalidScopeRejected pins settings R19's two-value set: a scope that is neither
// all-repos nor this-repo is rejected with a diagnostic naming both, and the default
// stands rather than the bad value being adopted (the shape AC4 fixes for the profile).
func TestInvalidScopeRejected(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "workflows_scope: my-org\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.WorkflowsScope != config.ScopeAllRepos {
		t.Errorf("WorkflowsScope = %q, want the default %q to stand", cfg.WorkflowsScope, config.ScopeAllRepos)
	}
	if len(diags) == 0 {
		t.Fatal("an invalid scope produced no diagnostic")
	}
	msg := diags[0].Message
	for _, want := range []string{"workflows_scope", string(config.ScopeAllRepos), string(config.ScopeThisRepo)} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic %q does not name %q", msg, want)
		}
	}
}

// TestSaveChangesOnlyEditedKey pins settings AC11 and R17: editing one setting leaves
// config.yml changed in that key only, with unrelated comments, key order and other
// keys intact. prev and next differ only in Budget, so Save must touch budget and
// nothing else.
func TestSaveChangesOnlyEditedKey(t *testing.T) {
	dir := t.TempDir()
	original := "# My gh-runs config\n" +
		"budget: greedy # spend a little more\n" +
		"confirm_threshold: 100\n" +
		"\n" +
		"# something a newer version might know\n" +
		"future_thing: 42\n" +
		"keybinding_profile: vim\n"
	writeConfig(t, dir, original)
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	prev.Budget = config.TierGreedy
	prev.ConfirmThreshold = 100
	prev.KeybindingProfile = config.KeybindingVim
	next := prev
	next.Budget = config.TierBackground

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := readSaved(t, dir)
	if !strings.Contains(got, "budget: background") {
		t.Errorf("saved config did not change budget to background:\n%s", got)
	}
	if strings.Contains(got, "greedy") {
		t.Errorf("saved config still names the old budget value:\n%s", got)
	}
	// Unrelated content survives: the head comment, the inline comment on budget, the
	// unknown key and its value, the other settings, and the key order.
	for _, want := range []string{
		"# My gh-runs config",
		"spend a little more",
		"confirm_threshold: 100",
		"# something a newer version might know",
		"future_thing: 42",
		"keybinding_profile: vim",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Save discarded %q (R17: comments, order and unknown keys must survive):\n%s", want, got)
		}
	}
	if idx := strings.Index(got, "budget"); idx > strings.Index(got, "confirm_threshold") {
		t.Errorf("Save reordered the keys; budget must stay first:\n%s", got)
	}
}

// TestSaveRoundTripsThroughLoad pins R17: a change written by Save is read back by the
// next Load, and the unknown key still only warns rather than failing the run (R14).
func TestSaveRoundTripsThroughLoad(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: normal\nfuture_thing: 1\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	next := prev
	next.KeybindingProfile = config.KeybindingVim
	next.StorageScope = config.ScopeThisRepo

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfg, _ := config.Load(env, config.Flags{})
	if cfg.KeybindingProfile != config.KeybindingVim {
		t.Errorf("KeybindingProfile after round-trip = %q, want %q", cfg.KeybindingProfile, config.KeybindingVim)
	}
	if cfg.StorageScope != config.ScopeThisRepo {
		t.Errorf("StorageScope after round-trip = %q, want %q", cfg.StorageScope, config.ScopeThisRepo)
	}
}

// TestSaveCreatesFileWhenAbsent pins that Save works from no file at all: it creates
// config.yml under the resolved directory carrying just the changed key (R3, R17).
func TestSaveCreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir() // no config.yml, no gh-runs directory
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	next := prev
	next.Budget = config.TierGreedy

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := readSaved(t, dir)
	if !strings.Contains(got, "budget: greedy") {
		t.Errorf("Save did not write the changed key to a fresh file:\n%s", got)
	}
}

// TestSaveNoChangeWritesNothing pins that a Save with nothing changed writes no file,
// so opening and closing the view without an edit leaves the directory untouched (AC2's
// spirit: no file appears when nothing was set).
func TestSaveNoChangeWritesNothing(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg := baseConfig()
	if err := config.Save(env, cfg, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "gh-runs", "config.yml")); !os.IsNotExist(err) {
		t.Errorf("Save with no change created a config file; it must write nothing")
	}
}

// TestSaveLeavesNoTempFile pins that the atomic write cleans up after itself: after a
// successful Save the directory holds config.yml and no stray temporary alongside it.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	next := prev
	next.DiscoveryRefreshMinutes = 9
	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "gh-runs"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.yml" {
			t.Errorf("Save left a stray file %q; the write must be atomic and clean", e.Name())
		}
	}
}
