package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// mustParse builds the Filter a query line states, for a test that would rather read the
// line than assemble the struct. It goes through the Feed's own grammar, so a filter a test
// asserts is a filter a person could have typed.
func mustParse(t *testing.T, q string) filter.Filter {
	t.Helper()
	f, err := filter.ParseQuery(q)
	if err != nil {
		t.Fatalf("filter.ParseQuery(%q): %v", q, err)
	}
	return f
}

// TestLaunchFilterDefaultsToEmpty pins settings R3 and AC1 for R9's key: with no config
// file the launch filter is the empty Filter, which matches every Run, and an empty file
// changes nothing. Neither produces a diagnostic.
func TestLaunchFilterDefaultsToEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write bool
	}{
		{name: "no config file"},
		{name: "empty config file", write: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.write {
				writeConfig(t, dir, "")
			}
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			if len(diags) != 0 {
				t.Fatalf("Load returned diagnostics: %v", diags)
			}
			if !reflect.DeepEqual(cfg.LaunchFilter, filter.Filter{}) {
				t.Errorf("LaunchFilter = %+v, want the empty default", cfg.LaunchFilter)
			}
		})
	}
}

// TestLaunchFilterPrecedence is settings AC3 in full, at the seam that resolves it: a
// config file setting the launch filter, overridden by the equivalent flag, yields the
// flag's value; with the flag absent it yields the config's; with both absent, the default.
// All four combinations, because three of them pass on an implementation that ignores one
// layer entirely.
func TestLaunchFilterPrecedence(t *testing.T) {
	fileFilter := "launch_filter:\n  branch: main\n  conclusion: [failure]\n"

	for _, tc := range []struct {
		name string
		file string
		flag filter.Filter
		want filter.Filter
	}{
		{
			name: "neither: the default holds",
			want: filter.Filter{},
		},
		{
			name: "file only: the file is used",
			file: fileFilter,
			want: filter.Filter{Branch: "main", Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
		},
		{
			name: "flag only: the flag is used",
			flag: filter.Filter{Actor: "octocat"},
			want: filter.Filter{Actor: "octocat"},
		},
		{
			name: "both: the flag wins",
			file: fileFilter,
			flag: filter.Filter{Actor: "octocat"},
			want: filter.Filter{Actor: "octocat"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				writeConfig(t, dir, tc.file)
			}
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{LaunchFilter: tc.flag})

			if len(diags) != 0 {
				t.Fatalf("Load returned diagnostics: %v", diags)
			}
			if !reflect.DeepEqual(cfg.LaunchFilter, tc.want) {
				t.Errorf("LaunchFilter = %+v, want %+v", cfg.LaunchFilter, tc.want)
			}
		})
	}
}

// TestLaunchFilterFlagReplacesRatherThanMerges pins the whole-value override AC3 implies:
// the flag's filter is the filter, not the file's with the flag's axes laid over it. A
// merge would produce a narrowing neither source asked for, and nothing could state the
// broader one.
func TestLaunchFilterFlagReplacesRatherThanMerges(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n  status: [queued]\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, _ := config.Load(env, config.Flags{LaunchFilter: filter.Filter{Actor: "octocat"}})

	if cfg.LaunchFilter.Branch != "" || len(cfg.LaunchFilter.Statuses) != 0 {
		t.Errorf("LaunchFilter = %+v, want the file's axes replaced rather than merged", cfg.LaunchFilter)
	}
}

// TestAllocatedEmptyFlagDoesNotWipeTheFile pins that "no filter flag was passed" is a
// question about what the flags constrain, not about how they were allocated. A caller that
// builds its sets with make() and appends nothing holds a Filter that matches every Run, so
// it states no filter and must not override the file's (R4, AC3).
//
// Nothing fills Flags.LaunchFilter today, and filter.ParseQuery returns nil sets, so this
// is luck rather than a contract until it is pinned here. The day a CLI builds its Filter
// with make() the file's launch filter would be destroyed by a flag naming no axis, with no
// diagnostic, which is an AC3 violation with no test between it and a release.
func TestAllocatedEmptyFlagDoesNotWipeTheFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	allocatedEmpty := filter.Filter{
		Statuses:    []domain.Status{},
		Conclusions: []domain.Conclusion{},
		Repos:       []domain.RepoID{},
	}
	cfg, diags := config.Load(env, config.Flags{LaunchFilter: allocatedEmpty})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if cfg.LaunchFilter.Branch != "main" {
		t.Errorf("LaunchFilter = %+v, want the file's filter to stand: a flag that constrains "+
			"nothing states no filter", cfg.LaunchFilter)
	}
}

// TestLaunchFilterReadsEveryAxis pins the shape the file carries: each axis of ADR-0016's
// Filter that the key spells decodes into its own typed field, and the permissive pair
// arrives as two distinct keys rather than as the CLI's conflated -s string (R9).
func TestLaunchFilterReadsEveryAxis(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, strings.Join([]string{
		"launch_filter:",
		"  branch: release/2.0",
		"  commit: 0a1b2c3",
		"  actor: octocat",
		"  event: push",
		"  workflow: 9004",
		`  created: ">=2026-01-01"`,
		"  status: [queued, in_progress]",
		"  conclusion: [failure]",
		"",
	}, "\n"))
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	want := filter.Filter{
		Branch:      "release/2.0",
		Commit:      "0a1b2c3",
		Actor:       "octocat",
		Event:       "push",
		Workflow:    "9004",
		Created:     mustParse(t, "created:>=2026-01-01").Created,
		Statuses:    []domain.Status{domain.StatusQueued, domain.StatusInProgress},
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
	}
	if !reflect.DeepEqual(cfg.LaunchFilter, want) {
		t.Errorf("LaunchFilter = %+v, want %+v", cfg.LaunchFilter, want)
	}
}

// TestLaunchFilterAxisCountIsDeliberate fails the day ADR-0016's Filter grows a field, so
// adding one is a decision about the config file rather than a silent omission from it.
// Every field is spelled in the file: the repository axis was the one omission, and issue
// #102 closed it by giving the grammar the Settings view edits a repo: token.
//
// A field is not the same as a sub-key, and since #117 the two counts differ. ThisRepo is a
// tenth field that rides inside the existing repos key as a bare this-repo entry, because it
// is a member of the repository axis rather than an axis of its own (ADR-0016). So the file
// carries ten fields across nine sub-keys, and a new field has three homes to choose between
// rather than two: its own sub-key, a spelling inside an existing one, or a documented
// omission recorded here.
func TestLaunchFilterAxisCountIsDeliberate(t *testing.T) {
	const subKeys, ridingInsideOne, deliberatelyAbsent = 9, 1, 0
	carried := subKeys + ridingInsideOne
	if got := reflect.TypeOf(filter.Filter{}).NumField(); got != carried+deliberatelyAbsent {
		t.Fatalf("filter.Filter has %d fields, and launch_filter carries %d of them across %d sub-keys "+
			"(%d riding inside another key), with %d left out on purpose. Decide where the new one belongs: "+
			"its own sub-key in launchfilter.go, a spelling inside an existing one, or a documented omission here",
			got, carried, subKeys, ridingInsideOne, deliberatelyAbsent)
	}
}

// TestLaunchFilterReadsTheRepositoryAxis pins R9's ninth axis (#102). The key is a sequence
// of refs, decoded through the same door R7's exclude list uses, and both spellings of a ref
// name the same repository (ADR-0009).
func TestLaunchFilterReadsTheRepositoryAxis(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - cli/cli\n    - github.com/jv-k/gh-runs\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	want := []domain.RepoID{
		{Host: domain.HostGitHub, Owner: "cli", Name: "cli"},
		{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"},
	}
	if !reflect.DeepEqual(cfg.LaunchFilter.Repos, want) {
		t.Errorf("LaunchFilter.Repos = %v, want %v", cfg.LaunchFilter.Repos, want)
	}
	if len(diags) != 0 {
		t.Errorf("a well-formed repos list produced diagnostics: %v", diags)
	}
}

// TestRepositoryOnlyLaunchFilterIsNotEmpty pins the trap the axis used to be. The Feed
// decides a filter is active by asking whether QueryString is non-empty, and Load tells "no
// filter was set" from one that was the same way. While the axis had no token, a filter
// carrying only repositories narrowed the rows while every predicate above called it empty.
func TestRepositoryOnlyLaunchFilterIsNotEmpty(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - cli/cli\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, _ := config.Load(env, config.Flags{})

	if got := cfg.LaunchFilter.QueryString(); got == "" {
		t.Errorf("QueryString = %q for a filter carrying a repository: it would report no active filter", got)
	}
	if reflect.DeepEqual(cfg.LaunchFilter, filter.Filter{}) {
		t.Error("a repository-only launch filter read back as the zero Filter")
	}
}

// TestLaunchFilterRepositoryEntryDropsOneBadEntry pins R14 for the new key, the rule R7's
// exclude list already follows: the malformed entry is named as written with its line, the
// entries that parsed stand, and the run does not fail.
func TestLaunchFilterRepositoryEntryDropsOneBadEntry(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - cli/cli\n    - not-a-ref\n    - jv-k/gh-runs\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	want := []domain.RepoID{
		{Host: domain.HostGitHub, Owner: "cli", Name: "cli"},
		{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"},
	}
	if !reflect.DeepEqual(cfg.LaunchFilter.Repos, want) {
		t.Errorf("LaunchFilter.Repos = %v, want the two that parsed: %v", cfg.LaunchFilter.Repos, want)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags)
	}
	if !strings.Contains(diags[0].Message, "not-a-ref") {
		t.Errorf("diagnostic %q does not name the entry as written", diags[0].Message)
	}
	if !strings.Contains(diags[0].Message, "launch_filter.repos") {
		t.Errorf("diagnostic %q does not name the key", diags[0].Message)
	}
}

// TestSaveLaunchFilterRepositoryRoundTrips pins R17 for the new axis end to end: a filter
// the view produced from a repo: token in its line persists to the file and returns on the
// next load, in the bare OWNER/REPO spelling the exclude list already writes.
func TestSaveLaunchFilterRepositoryRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "# mine\nlaunch_filter:\n  branch: main # keep me\n  someday: soon\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev, _ := config.Load(env, config.Flags{})
	next := prev
	edited, err := filter.ParseQuery("branch:main repo:cli/cli")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	next.LaunchFilter = edited

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved := readSaved(t, dir)
	if !strings.Contains(saved, "- cli/cli") {
		t.Errorf("the repository axis was not written in the bare spelling:\n%s", saved)
	}
	for _, want := range []string{"# mine", "branch: main # keep me", "someday: soon"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lost %q:\n%s", want, saved)
		}
	}
	// R17 asks for key order too, not only comments and unknown keys: adding an axis must
	// append rather than reshuffle what the operator already had.
	if i, j := strings.Index(saved, "branch:"), strings.Index(saved, "someday:"); i > j {
		t.Errorf("writing the new axis reordered the existing sub-keys:\n%s", saved)
	}
	cfg, _ := config.Load(env, config.Flags{})
	if !reflect.DeepEqual(cfg.LaunchFilter, next.LaunchFilter) {
		t.Errorf("reloaded LaunchFilter = %+v, want %+v:\n%s", cfg.LaunchFilter, next.LaunchFilter, saved)
	}
}

// TestLaunchFilterWrongShapeKeepsTheDefault pins settings R14 for R9's key: a value of the
// wrong shape leaves the empty default in place, says so, and does not fail the run. The
// scalar case is the one a person reaches for, having written the CLI's -s string here.
func TestLaunchFilterWrongShapeKeepsTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{name: "a scalar", file: "launch_filter: status:failure\n"},
		{name: "a list", file: "launch_filter:\n  - failure\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.file)
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			if !reflect.DeepEqual(cfg.LaunchFilter, filter.Filter{}) {
				t.Errorf("LaunchFilter = %+v, want the empty default to stand", cfg.LaunchFilter)
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %v, want exactly one", diags)
			}
			if !strings.Contains(diags[0].Message, "launch_filter") {
				t.Errorf("diagnostic %q does not name the key", diags[0].Message)
			}
		})
	}
}

// TestLaunchFilterBadAxisDropsThatAxisAlone pins that one bad clause costs one clause: the
// rest of the filter stands, which is the rule resolveFile already applies to the file's
// top level. Dropping the whole filter over a typo would widen the Feed where the operator
// asked to narrow it.
func TestLaunchFilterBadAxisDropsThatAxisAlone(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n  status: [nonsense]\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.LaunchFilter.Branch != "main" {
		t.Errorf("Branch = %q, want the good axis kept", cfg.LaunchFilter.Branch)
	}
	if len(cfg.LaunchFilter.Statuses) != 0 {
		t.Errorf("Statuses = %v, want the rejected value dropped", cfg.LaunchFilter.Statuses)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags)
	}
	for _, want := range []string{"launch_filter.status", "nonsense"} {
		if !strings.Contains(diags[0].Message, want) {
			t.Errorf("diagnostic %q does not name %q", diags[0].Message, want)
		}
	}
}

// TestLaunchFilterKeepsStatusAndConclusionDistinct is R9's whole point: the stored form
// holds the two as distinct typed fields, so a Conclusion written under status is named as
// a Conclusion and dropped rather than quietly classified into the other set. Quiet
// classification is the CLI string's conflation arriving by another door.
func TestLaunchFilterKeepsStatusAndConclusionDistinct(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       string
		wantInMsg  []string
		wantFilter filter.Filter
	}{
		{
			name:      "a Conclusion under status",
			file:      "launch_filter:\n  status: [failure]\n",
			wantInMsg: []string{"launch_filter.status", "failure", "Conclusion", "Status", "conclusion"},
		},
		{
			name:      "a Status under conclusion",
			file:      "launch_filter:\n  conclusion: [queued]\n",
			wantInMsg: []string{"launch_filter.conclusion", "queued", "Status", "Conclusion", "status"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.file)
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			if !reflect.DeepEqual(cfg.LaunchFilter, tc.wantFilter) {
				t.Errorf("LaunchFilter = %+v, want the misfiled value dropped", cfg.LaunchFilter)
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %v, want exactly one", diags)
			}
			for _, want := range tc.wantInMsg {
				if !strings.Contains(diags[0].Message, want) {
					t.Errorf("diagnostic %q does not name %q", diags[0].Message, want)
				}
			}
		})
	}
}

// TestLaunchFilterAcceptsALoneValue pins the short spelling a hand-written config takes:
// one value need not be a list. The marshaller always writes the sequence form, so the two
// spellings never fight over what a Save produces.
func TestLaunchFilterAcceptsALoneValue(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  status: queued\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("Load returned diagnostics: %v", diags)
	}
	if want := []domain.Status{domain.StatusQueued}; !reflect.DeepEqual(cfg.LaunchFilter.Statuses, want) {
		t.Errorf("Statuses = %v, want %v", cfg.LaunchFilter.Statuses, want)
	}
}

// TestLaunchFilterUnknownAxisIsIgnoredWithADiagnostic pins R14 one level down: a sub-key
// this version does not apply warns and does not fail the run, exactly as an unrecognised
// top-level key does.
func TestLaunchFilterUnknownAxisIsIgnoredWithADiagnostic(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n  someday: yes\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.LaunchFilter.Branch != "main" {
		t.Errorf("Branch = %q, want the known axis applied", cfg.LaunchFilter.Branch)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags)
	}
	if !strings.Contains(diags[0].Message, "launch_filter.someday") {
		t.Errorf("diagnostic %q does not name the unrecognised axis", diags[0].Message)
	}
}

// TestLaunchFilterBadCreatedIsRejectedByName pins that the created clause is validated by
// filter.ParseCreated, the same door every other consumer uses, so a bad range is refused
// before anything reads it (ADR-0016, cli-surface R6).
func TestLaunchFilterBadCreatedIsRejectedByName(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  created: yesterday\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if cfg.LaunchFilter.Created.String() != "" {
		t.Errorf("Created = %q, want the clause dropped", cfg.LaunchFilter.Created.String())
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags)
	}
	if !strings.Contains(diags[0].Message, "yesterday") {
		t.Errorf("diagnostic %q does not name the value", diags[0].Message)
	}
}

// TestSaveLaunchFilterChangesOnlyThatKey is settings R17 and AC11's marshaller half for
// R9's key: setting the launch filter writes launch_filter and nothing else, with unrelated
// comments, key order and unrecognised keys intact.
func TestSaveLaunchFilterChangesOnlyThatKey(t *testing.T) {
	dir := t.TempDir()
	original := "# My gh-runs config\n" +
		"budget: greedy # spend a little more\n" +
		"\n" +
		"# something a newer version might know\n" +
		"future_thing: 42\n" +
		"keybinding_profile: vim\n"
	writeConfig(t, dir, original)
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	prev.Budget = config.TierGreedy
	prev.KeybindingProfile = config.KeybindingVim
	next := prev
	next.LaunchFilter = filter.Filter{
		Branch:      "main",
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
	}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved := readSaved(t, dir)
	for _, want := range []string{
		"# My gh-runs config",
		"budget: greedy # spend a little more",
		"# something a newer version might know",
		"future_thing: 42",
		"keybinding_profile: vim",
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lost %q:\n%s", want, saved)
		}
	}
	wantKeys := []string{"budget", "future_thing", "keybinding_profile", "launch_filter"}
	if got := topLevelKeys(saved); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("top-level keys = %v, want %v:\n%s", got, wantKeys, saved)
	}

	// The written key reads back as the value that was saved, which is the property that
	// makes the view and the file the same settings (R17). The reload carries one
	// diagnostic, for future_thing, which is the point of keeping that key: R14 warns
	// about it and the run continues.
	cfg, _ := config.Load(env, config.Flags{})
	if !reflect.DeepEqual(cfg.LaunchFilter, next.LaunchFilter) {
		t.Errorf("reloaded LaunchFilter = %+v, want %+v:\n%s", cfg.LaunchFilter, next.LaunchFilter, saved)
	}
}

// TestSaveLaunchFilterRoundTripsEveryAxis pins that every axis the file spells survives a
// write and a read. It is what stops an axis being decoded and silently never written back,
// which the axis-count guard alone would not catch.
func TestSaveLaunchFilterRoundTripsEveryAxis(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	next := baseConfig()
	next.LaunchFilter = filter.Filter{
		Branch:      "release/2.0",
		Commit:      "0a1b2c3",
		Actor:       "octocat",
		Event:       "push",
		Workflow:    "9004",
		Created:     mustParse(t, "created:>=2026-01-01").Created,
		Statuses:    []domain.Status{domain.StatusQueued, domain.StatusInProgress},
		Conclusions: []domain.Conclusion{domain.ConclusionFailure, domain.ConclusionTimedOut},
		// The marker is a field of the filter and rides inside the repos key, so "every
		// axis" has to include it or the round trip that names itself exhaustive is not.
		ThisRepo: true,
	}

	if err := config.Save(env, baseConfig(), next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, diags := config.Load(env, config.Flags{})
	if len(diags) != 0 {
		t.Fatalf("Load of the saved file returned diagnostics: %v\n%s", diags, readSaved(t, dir))
	}
	if !reflect.DeepEqual(cfg.LaunchFilter, next.LaunchFilter) {
		t.Errorf("round-tripped LaunchFilter = %+v, want %+v:\n%s",
			cfg.LaunchFilter, next.LaunchFilter, readSaved(t, dir))
	}
}

// TestSaveLaunchFilterRemovesAClearedAxis pins that clearing a clause in the view clears it
// in the file. A stale clause left behind would narrow the next launch by something the
// operator had already removed, which is the failure R17's "the view and the file are the
// same settings" exists to prevent.
func TestSaveLaunchFilterRemovesAClearedAxis(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n  status:\n    - queued\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev, _ := config.Load(env, config.Flags{})
	next := prev
	next.LaunchFilter = filter.Filter{Branch: "main"}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved := readSaved(t, dir)
	if strings.Contains(saved, "queued") {
		t.Errorf("the cleared Status clause survived the write:\n%s", saved)
	}
	cfg, _ := config.Load(env, config.Flags{})
	if !reflect.DeepEqual(cfg.LaunchFilter, next.LaunchFilter) {
		t.Errorf("reloaded LaunchFilter = %+v, want %+v:\n%s", cfg.LaunchFilter, next.LaunchFilter, saved)
	}
}

// TestSaveLaunchFilterKeepsSubKeyCommentsAndUnknowns pins R17 one level down: editing one
// axis leaves the other axes where they are, with their comments, and leaves a sub-key this
// version does not recognise entirely alone.
func TestSaveLaunchFilterKeepsSubKeyCommentsAndUnknowns(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "# my launch view\n"+
		"launch_filter:\n"+
		"  # only what I broke\n"+
		"  conclusion:\n"+
		"    - failure # and nothing else\n"+
		"  branch: main # the one that matters\n"+
		"  someday: soon\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev, _ := config.Load(env, config.Flags{})
	next := prev
	next.LaunchFilter.Branch = "release/2.0"

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved := readSaved(t, dir)
	for _, want := range []string{
		"# my launch view",
		"# only what I broke",
		"failure # and nothing else", // a comment on a list item, one level further down
		"branch: release/2.0 # the one that matters",
		"someday: soon",
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lost %q:\n%s", want, saved)
		}
	}
	if i, j := strings.Index(saved, "conclusion:"), strings.Index(saved, "branch:"); i > j {
		t.Errorf("the sub-key order changed:\n%s", saved)
	}
}

// TestSaveWithNoLaunchFilterChangeWritesNothing pins that the key is written only when it
// changes: opening the view over a config with no launch filter and saving another setting
// leaves the file without one.
func TestSaveWithNoLaunchFilterChangeWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "budget: normal\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev := baseConfig()
	next := prev
	next.Budget = config.TierGreedy

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved := readSaved(t, dir); strings.Contains(saved, "launch_filter") {
		t.Errorf("an unchanged launch filter was written anyway:\n%s", saved)
	}
}

// TestSaveClearedLaunchFilterReadsBackEmpty pins the last state a Save can leave: every
// axis removed. The emptied mapping must read back as the empty filter rather than as a
// null the loader would have to guess at.
func TestSaveClearedLaunchFilterReadsBackEmpty(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  branch: main\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev, _ := config.Load(env, config.Flags{})
	next := prev
	next.LaunchFilter = filter.Filter{}

	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, diags := config.Load(env, config.Flags{})
	if len(diags) != 0 {
		t.Fatalf("Load of the cleared file returned diagnostics: %v\n%s", diags, readSaved(t, dir))
	}
	if !reflect.DeepEqual(cfg.LaunchFilter, filter.Filter{}) {
		t.Errorf("LaunchFilter = %+v, want empty:\n%s", cfg.LaunchFilter, readSaved(t, dir))
	}
}

// TestLaunchFilterReadsTheThisRepoMarker pins the file half of ADR-0016's marker: a bare
// this-repo entry sits beside named entries under launch_filter.repos, where the key already
// says which axis it is, and it lands on the marker field rather than in Repos.
func TestLaunchFilterReadsTheThisRepoMarker(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - this-repo\n    - cli/cli\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, diags := config.Load(env, config.Flags{})

	if len(diags) != 0 {
		t.Fatalf("a well-formed repos list produced diagnostics: %v", diags)
	}
	if !cfg.LaunchFilter.ThisRepo {
		t.Error("the this-repo entry did not set the marker")
	}
	want := []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}
	if !reflect.DeepEqual(cfg.LaunchFilter.Repos, want) {
		t.Errorf("LaunchFilter.Repos = %v, want %v: the marker must be lifted out rather than "+
			"decoded as an identity", cfg.LaunchFilter.Repos, want)
	}
}

// TestSaveLaunchFilterRoundTripsTheThisRepoMarker is the round trip the issue asks for
// through load, edit and Save. The marker MUST come back as the word it went in as: resolving
// it into a name on the way out would write the directory the operator happened to be in over
// the setting they stated, which is the R17 defect that kept the repository axis out of the
// file to begin with (ADR-0016).
func TestSaveLaunchFilterRoundTripsTheThisRepoMarker(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - this-repo\n    - cli/cli\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	prev, _ := config.Load(env, config.Flags{})
	if !prev.LaunchFilter.ThisRepo {
		t.Fatal("the fixture did not load the marker, so this test would prove nothing")
	}

	// Edit an unrelated axis, as the Settings view would, and save the whole filter back.
	next := prev
	edited := prev.LaunchFilter
	edited.Branch = "main"
	next.LaunchFilter = edited
	if err := config.Save(env, prev, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved := readSaved(t, dir)
	if !strings.Contains(saved, "- this-repo") {
		t.Errorf("the marker was not written back as this-repo:\n%s", saved)
	}
	cfg, diags := config.Load(env, config.Flags{})
	if len(diags) != 0 {
		t.Fatalf("Load of the saved file returned diagnostics: %v\n%s", diags, saved)
	}
	if !reflect.DeepEqual(cfg.LaunchFilter, next.LaunchFilter) {
		t.Errorf("round-tripped LaunchFilter = %+v, want %+v:\n%s",
			cfg.LaunchFilter, next.LaunchFilter, saved)
	}
}

// TestExcludeAndPinRejectTheThisRepoMarker pins the boundary the lift exists to keep. exclude
// and pin share resolveRepoList with launch_filter.repos, and only the launch filter's own
// axis handler lifts the marker out. Those two name repositories to leave alone and to keep at
// the top, and neither means anything for a directory the tool is not in, so each must report
// the entry by name and keep the rest of its list.
func TestExcludeAndPinRejectTheThisRepoMarker(t *testing.T) {
	for _, key := range []string{"exclude", "pin"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, key+":\n  - this-repo\n  - cli/cli\n")
			env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

			cfg, diags := config.Load(env, config.Flags{})

			got := cfg.Exclude
			if key == "pin" {
				got = cfg.Pin
			}
			want := []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s = %v, want %v: the marker must not be admitted here, and the "+
					"entries that parsed must stand (R14)", key, got, want)
			}
			if len(diags) == 0 {
				t.Errorf("%s accepted this-repo silently; a rejected entry must be named (R14)", key)
			}
		})
	}
}

// TestThisRepoTokenCannotBeARepositoryReference asserts the collision the canon reasons about
// rather than assuming it. ADR-0016 argues the marker is safe because ParseRepoRef requires
// two segments and this has one. If that ever stopped being true, the token would parse as a
// repository somewhere and the lift above would be shadowing a real identity.
func TestThisRepoTokenCannotBeARepositoryReference(t *testing.T) {
	if _, err := domain.ParseRepoRef(filter.ThisRepoToken); err == nil {
		t.Fatalf("ParseRepoRef(%q) succeeded: the marker now collides with a repository "+
			"reference, and the whole spelling has to be reconsidered (ADR-0016)", filter.ThisRepoToken)
	}
}

// TestMarkerOnlyLaunchFilterIsNotEmpty is TestRepositoryOnlyLaunchFilterIsNotEmpty for the
// marker, and the same trap. Load tells "no filter was set" from one that was by asking
// whether QueryString is non-empty, so a launch filter carrying only this-repo would narrow
// the Feed to the working directory while every predicate above called it absent.
func TestMarkerOnlyLaunchFilterIsNotEmpty(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "launch_filter:\n  repos:\n    - this-repo\n")
	env := envMap(map[string]string{"XDG_CONFIG_HOME": dir})

	cfg, _ := config.Load(env, config.Flags{})

	if !cfg.LaunchFilter.ThisRepo {
		t.Fatal("the marker did not load, so this test would prove nothing")
	}
	if got := cfg.LaunchFilter.QueryString(); got == "" {
		t.Error("a launch filter carrying only the this-repo marker rendered as empty: it narrows " +
			"the Feed, so every surface that asks whether a filter is active must see one")
	}
}
