package config_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// ids renders a list of host-qualified identities as strings, so a test asserts
// membership and order in one comparable value.
func ids(list []domain.RepoID) []string {
	out := make([]string, len(list))
	for i, id := range list {
		out[i] = id.String()
	}
	return out
}

// equal reports whether two string slices carry the same values in the same order.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExcludeAndPinDefaultToEmpty pins settings R3 and AC1 for R7's two lists: with
// no config file both are empty and no diagnostic is emitted, so discovery is
// unchanged from the state before either key existed.
func TestExcludeAndPinDefaultToEmpty(t *testing.T) {
	dir := t.TempDir() // empty: no config.yml inside
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if len(cfg.Exclude) != 0 {
		t.Errorf("Exclude = %v, want empty", ids(cfg.Exclude))
	}
	if len(cfg.Pin) != 0 {
		t.Errorf("Pin = %v, want empty", ids(cfg.Pin))
	}
}

// TestExcludeAndPinReadFromFile pins settings R7's two keys at the loader. Both hold
// host-qualified identity (ADR-0009): the bare OWNER/REPO form defaults to
// github.com and an explicit github.com/OWNER/REPO is accepted and means the same
// repository, exactly as the CLI's -R parses its selector. The list order is the
// file's, because the pin list's order is what prioritises one repository over
// another.
func TestExcludeAndPinReadFromFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, ""+
		"exclude:\n"+
		"  - jv-k/noisy\n"+
		"  - github.com/acme/vendor\n"+
		"pin:\n"+
		"  - jv-k/main-project\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	wantExclude := []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}
	if got := ids(cfg.Exclude); !equal(got, wantExclude) {
		t.Errorf("Exclude = %v, want %v", got, wantExclude)
	}
	wantPin := []string{"github.com/jv-k/main-project"}
	if got := ids(cfg.Pin); !equal(got, wantPin) {
		t.Errorf("Pin = %v, want %v", got, wantPin)
	}
}

// TestRepoListWrongTypeFallsBackToEmpty pins settings R14 for R7's two keys, in the
// shape the selector settings already use: a value that is not a list falls that one
// setting back to its default, an empty list, with a diagnostic naming the key and
// what it wanted. The sibling key is untouched, because each key is decoded from its
// own node.
func TestRepoListWrongTypeFallsBackToEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents string
	}{
		{"scalar number", "exclude: 5\npin:\n  - jv-k/kept\n"},
		{"scalar string", "exclude: jv-k/noisy\npin:\n  - jv-k/kept\n"},
		{"mapping", "exclude:\n  jv-k: noisy\npin:\n  - jv-k/kept\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.contents)
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			if len(cfg.Exclude) != 0 {
				t.Errorf("Exclude = %v, want the empty default to stand", ids(cfg.Exclude))
			}
			if len(diags) == 0 {
				t.Fatal("a wrong-typed exclude produced no diagnostic")
			}
			if msg := diags[0].Message; !strings.Contains(msg, "exclude") {
				t.Errorf("diagnostic %q does not name the exclude key", msg)
			}
			// The sibling key is resolved from its own node, so one bad value cannot
			// discard the rest of the file.
			if got := ids(cfg.Pin); !equal(got, []string{"github.com/jv-k/kept"}) {
				t.Errorf("Pin = %v, want the valid sibling key kept", got)
			}
		})
	}
}

// TestRepoListRejectsUnsupportedHostAndBadShape pins ADR-0009's explicit rejection at
// the config surface: an entry naming a host 2.0.0 does not serve, or one outside
// GitHub's owner and name charset, contributes no identity and gets a diagnostic
// naming it. The entries that parsed are kept, because dropping a whole exclude list
// over one typo would poll repositories the person told the tool to leave alone.
func TestRepoListRejectsUnsupportedHostAndBadShape(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, ""+
		"exclude:\n"+
		"  - jv-k/good\n"+
		"  - ghe.example.com/acme/internal\n"+
		"  - just-an-owner\n"+
		"  - jv-k/../escape\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if got := ids(cfg.Exclude); !equal(got, []string{"github.com/jv-k/good"}) {
		t.Errorf("Exclude = %v, want only the entry that parsed", got)
	}
	if len(diags) != 3 {
		t.Fatalf("diagnostics = %d, want one per rejected entry: %v", len(diags), diags)
	}
	for _, want := range []string{"ghe.example.com", "just-an-owner", "escape"} {
		found := false
		for _, d := range diags {
			if strings.Contains(d.Message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no diagnostic names the rejected entry %q: %v", want, diags)
		}
	}
}

// TestSaveWritesRepoListsWithoutDamage pins settings R17 and AC11 for R7's two keys:
// editing the lists changes those keys only, and unrelated comments, key order and
// keys this version does not recognise all survive. The values are written as YAML
// sequences of the bare OWNER/REPO spelling, which is the form the loader reads back.
func TestSaveWritesRepoListsWithoutDamage(t *testing.T) {
	dir := t.TempDir()
	original := "# My gh-runs config\n" +
		"budget: greedy # spend a little more\n" +
		"exclude:\n" +
		"  - jv-k/noisy # too chatty\n" +
		"\n" +
		"# something a newer version might know\n" +
		"future_thing: 42\n" +
		"keybinding_profile: vim\n"
	writeConfig(t, dir, original)
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	prev.Budget = config.TierGreedy
	prev.KeybindingProfile = config.KeybindingVim
	prev.Exclude = []domain.RepoID{gh("jv-k", "noisy")}

	next := prev
	next.Exclude = []domain.RepoID{gh("jv-k", "noisy"), gh("acme", "vendor")}
	next.Pin = []domain.RepoID{gh("jv-k", "main-project")}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := readSaved(t, dir)
	for _, want := range []string{"- jv-k/noisy", "- acme/vendor", "pin:", "- jv-k/main-project"} {
		if !strings.Contains(got, want) {
			t.Errorf("saved config is missing %q:\n%s", want, got)
		}
	}
	// R17: everything the edit did not name survives, including the comment sitting on
	// the exclude list's own value node.
	for _, want := range []string{
		"# My gh-runs config",
		"budget: greedy",
		"spend a little more",
		"too chatty", // the comment on a list item the edit kept
		"# something a newer version might know",
		"future_thing: 42",
		"keybinding_profile: vim",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Save discarded %q (R17):\n%s", want, got)
		}
	}
	if idx := strings.Index(got, "budget"); idx > strings.Index(got, "exclude") {
		t.Errorf("Save reordered the keys; budget must stay first:\n%s", got)
	}

	// The written form is what the loader reads: a round trip through Load yields the
	// identities the view held, in the order it held them.
	cfg, diags := config.Load(env, config.Flags{})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "future_thing") {
		t.Fatalf("round-trip diagnostics = %v, want only the unknown-key warning", diags)
	}
	if got := ids(cfg.Exclude); !equal(got, []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}) {
		t.Errorf("Exclude after round-trip = %v", got)
	}
	if got := ids(cfg.Pin); !equal(got, []string{"github.com/jv-k/main-project"}) {
		t.Errorf("Pin after round-trip = %v", got)
	}
}

// TestSaveClearsARepoListToAnEmptyKey pins the other direction of R17: emptying a list
// in the view writes an empty sequence rather than leaving the old entries in the file,
// and the next Load reads an empty list back.
func TestSaveClearsARepoListToAnEmptyKey(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "exclude:\n  - jv-k/noisy\nbudget: normal\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	prev.Exclude = []domain.RepoID{gh("jv-k", "noisy")}
	next := prev
	next.Exclude = nil

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readSaved(t, dir); strings.Contains(got, "jv-k/noisy") {
		t.Errorf("Save left the cleared entry in the file:\n%s", got)
	}
	cfg, _ := config.Load(env, config.Flags{})
	if len(cfg.Exclude) != 0 {
		t.Errorf("Exclude after clearing = %v, want empty", ids(cfg.Exclude))
	}
}

// gh builds a github.com-qualified identity for the tests above.
func gh(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}
