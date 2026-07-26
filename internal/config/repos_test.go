package config_test

import (
	"slices"
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

// TestExcludeDefaultsToEmpty pins settings R3 and AC1 for R7's exclude key: with no
// config file it is empty and no diagnostic is emitted, so discovery is unchanged from
// the state before the key existed.
func TestExcludeDefaultsToEmpty(t *testing.T) {
	dir := t.TempDir() // empty: no config.yml inside
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load with no config file returned diagnostics: %v", diags)
	}
	if len(cfg.Exclude) != 0 {
		t.Errorf("Exclude = %v, want empty", ids(cfg.Exclude))
	}
}

// TestExcludeReadFromFile pins settings R7's exclude key at the loader. It holds
// host-qualified identity (ADR-0009): the bare OWNER/REPO form defaults to github.com
// and an explicit github.com/OWNER/REPO is accepted and means the same repository,
// exactly as the CLI's -R parses its selector, because both now route through
// domain.ParseRepoRef.
func TestExcludeReadFromFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, ""+
		"exclude:\n"+
		"  - jv-k/noisy\n"+
		"  - github.com/acme/vendor\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	want := []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}
	if got := ids(cfg.Exclude); !slices.Equal(got, want) {
		t.Errorf("Exclude = %v, want %v", got, want)
	}
}

// TestPinKeyIsNotRecognised pins R7's pin half as deferred rather than half-built. The
// key warns as an unrecognised setting and changes nothing, which is settings R11's
// ruling applied here: a key with no subsystem behind it defers with the subsystem
// rather than shipping inert, and R14 means a config carrying one still starts. Issue
// #97 carries the pin, and R3 plus R14 mean adding the key later needs no migration.
func TestPinKeyIsNotRecognised(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "pin:\n  - jv-k/main-project\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	_, diags := config.Load(env, config.Flags{})

	if len(diags) != 1 || !strings.Contains(diags[0].Message, "pin") {
		t.Fatalf("diagnostics = %v, want one naming the unrecognised pin key", diags)
	}
	if !strings.Contains(diags[0].Message, "unrecognised") {
		t.Errorf("diagnostic %q is not the generic unknown-key message", diags[0].Message)
	}
}

// TestRepoListWrongTypeFallsBackToEmpty pins settings R14 for R7's exclude key, in the
// shape the selector settings already use: a value that is not a list falls that one
// setting back to its default, an empty list, with a diagnostic naming the key and
// what it wanted. The unrelated key is untouched, because each key is decoded from its
// own node.
func TestRepoListWrongTypeFallsBackToEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents string
	}{
		{"scalar number", "exclude: 5\nbudget: greedy\n"},
		{"scalar string", "exclude: jv-k/noisy\nbudget: greedy\n"},
		{"mapping", "exclude:\n  jv-k: noisy\nbudget: greedy\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.contents)
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			if len(cfg.Exclude) != 0 {
				t.Errorf("Exclude = %v, want the empty default to stand", ids(cfg.Exclude))
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %v, want exactly one for the wrong-typed exclude", diags)
			}
			if msg := diags[0].Message; !strings.Contains(msg, "exclude") {
				t.Errorf("diagnostic %q does not name the exclude key", msg)
			}
			// The unrelated key is resolved from its own node, so one bad value cannot
			// discard the rest of the file.
			if cfg.Budget != config.TierGreedy {
				t.Errorf("Budget = %q, want the valid sibling key kept", cfg.Budget)
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

	if got := ids(cfg.Exclude); !slices.Equal(got, []string{"github.com/jv-k/good"}) {
		t.Errorf("Exclude = %v, want only the entry that parsed", got)
	}
	if len(diags) != 3 {
		t.Fatalf("diagnostics = %d, want one per rejected entry: %v", len(diags), diags)
	}
	// Each diagnostic names the entry as written and the line that entry sits on, not
	// the line the sequence starts at. A twenty-entry list is exactly the case this
	// message exists for, and a message that points at line 3 for an entry on line 12
	// misleads the person it was written for.
	for _, want := range []struct{ entry, line string }{
		{"ghe.example.com/acme/internal", "line 3"},
		{"just-an-owner", "line 4"},
		{"jv-k/../escape", "line 5"},
	} {
		found := false
		for _, d := range diags {
			if strings.Contains(d.Message, want.entry) {
				found = true
				if !strings.Contains(d.Message, want.line) {
					t.Errorf("diagnostic %q does not name %q", d.Message, want.line)
				}
			}
		}
		if !found {
			t.Errorf("no diagnostic names the rejected entry %q: %v", want.entry, diags)
		}
	}
}

// TestSaveWritesRepoListsWithoutDamage pins settings R17 and AC11 for R7's exclude key
// at the marshaller: editing the list changes that key only, and unrelated comments, key
// order and keys this version does not recognise all survive. The value is written as a
// YAML sequence of the bare OWNER/REPO spelling, which is the form the loader reads back.
// The gesture that reaches this from the view is pinned in tui/settings.
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

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := readSaved(t, dir)
	for _, want := range []string{"- jv-k/noisy", "- acme/vendor"} {
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
	if got := ids(cfg.Exclude); !slices.Equal(got, []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}) {
		t.Errorf("Exclude after round-trip = %v", got)
	}
}

// TestSaveFillsAnExistingEmptyListKey pins the marshaller case a config file reaches
// after the list has been emptied once: the key is already there as an empty sequence,
// and gaining entries must turn it back into a block list rather than leave the flow
// style the empty form carries.
func TestSaveFillsAnExistingEmptyListKey(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "exclude: []\nbudget: normal\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	next := prev
	next.Exclude = []domain.RepoID{gh("jv-k", "noisy"), gh("acme", "vendor")}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := readSaved(t, dir)
	for _, want := range []string{"- jv-k/noisy", "- acme/vendor", "budget: normal"} {
		if !strings.Contains(got, want) {
			t.Errorf("saved config is missing %q:\n%s", want, got)
		}
	}
	cfg, _ := config.Load(env, config.Flags{})
	if got := ids(cfg.Exclude); !slices.Equal(got, []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}) {
		t.Errorf("Exclude after filling an empty key = %v", got)
	}
}

// TestSaveKeepsTheCommentOnAHostQualifiedEntry pins R17's "MUST NOT discard comments"
// for an entry written in the long form. The item nodes are matched by the identity
// they parse to, not by their literal text, because the file may spell an entry
// github.com/OWNER/REPO while the marshaller writes OWNER/REPO. Matching on text meant
// a long-form entry never found its own node, so it was rebuilt from scratch and its
// comment went with it. TestExcludeReadFromFile documents the long form as supported
// input, so this was a supported spelling silently losing an operator's note.
func TestSaveKeepsTheCommentOnAHostQualifiedEntry(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "exclude:\n  - github.com/jv-k/noisy # too chatty\nbudget: normal\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	prev.Exclude = []domain.RepoID{gh("jv-k", "noisy")}
	next := prev
	next.Exclude = []domain.RepoID{gh("jv-k", "noisy"), gh("acme", "vendor")}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := readSaved(t, dir)
	if !strings.Contains(got, "too chatty") {
		t.Errorf("Save discarded the comment on a host-qualified entry (R17):\n%s", got)
	}
	if !strings.Contains(got, "- acme/vendor") {
		t.Errorf("saved config is missing the added entry:\n%s", got)
	}
	cfg, _ := config.Load(env, config.Flags{})
	if got := ids(cfg.Exclude); !slices.Equal(got, []string{"github.com/jv-k/noisy", "github.com/acme/vendor"}) {
		t.Errorf("Exclude after round-trip = %v", got)
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
