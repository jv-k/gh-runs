package workflows_test

import (
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// fanOut records which repositories the refresh fetched, the observable that says which
// scope the tab ran under (R0). It answers an empty list, so the tab holds nothing and the
// test reads the fan-out alone.
type fanOut struct{ hit []domain.RepoID }

func (f *fanOut) fetch(id domain.RepoID) workflows.RepoWorkflows {
	f.hit = append(f.hit, id)
	return workflows.RepoWorkflows{Repo: id, Complete: true}
}

func (f *fanOut) names() []string {
	out := make([]string, 0, len(f.hit))
	for _, id := range f.hit {
		out = append(out, id.Owner+"/"+id.Name)
	}
	sort.Strings(out)
	return out
}

// scopedTab builds a Workflows tab under one scope, over the given working-directory
// repository resolution and discovered repositories, and refreshes it so the fan-out runs.
func scopedTab(t *testing.T, scope workflows.Scope, current func() (domain.RepoID, bool), repos ...domain.Repo) (workflows.Model, *fanOut) {
	t.Helper()
	f := &fanOut{}
	rr := append([]domain.Repo(nil), repos...)
	m := workflows.New(workflows.Options{
		Profile:     keys.Standard,
		Fetch:       f.fetch,
		Repos:       func() []domain.Repo { return rr },
		Scope:       scope,
		CurrentRepo: current,
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = act(t, m, "r")
	return m, f
}

func here(id domain.RepoID) func() (domain.RepoID, bool) {
	return func() (domain.RepoID, bool) { return id, true }
}

func nowhere() (domain.RepoID, bool) { return domain.RepoID{}, false }

// TestDefaultScopeIsAllRepos pins R0's default: the zero Scope is all-repos, and the fan-out
// covers the whole discovered set, one request per repository over ADR-0003's machinery. A
// tool whose thesis is cross-repo opens with the rollup.
func TestDefaultScopeIsAllRepos(t *testing.T) {
	_, f := scopedTab(t, "", here(rid("cli", "cli")),
		writable("cli", "cli"), writable("acme", "infra"), writable("old", "legacy"))

	want := []string{"acme/infra", "cli/cli", "old/legacy"}
	if got := f.names(); !equalStrings(got, want) {
		t.Errorf("the default scope fetched %v, want every discovered repository %v (R0)", got, want)
	}
}

// TestThisRepoScopeCoversTheWorkingDirectoryRepositoryAlone pins R0's second code path and
// settings R19's definition: this-repo resolves to the repository of the working directory,
// and the fan-out covers it alone. It answers "which Workflows are disabled in the repo I am
// in", which a rollup answers badly.
func TestThisRepoScopeCoversTheWorkingDirectoryRepositoryAlone(t *testing.T) {
	m, f := scopedTab(t, workflows.ScopeThisRepo, here(rid("acme", "infra")),
		writable("cli", "cli"), writable("acme", "infra"), writable("old", "legacy"))

	if got, want := f.names(), []string{"acme/infra"}; !equalStrings(got, want) {
		t.Fatalf("this-repo fetched %v, want %v alone (R0, settings R19)", got, want)
	}
	if view := m.View(); !strings.Contains(view, "this-repo") {
		t.Errorf("the view does not state the scope it ran under:\n%s", view)
	}
}

// TestThisRepoWithoutARepositoryFallsBackAndSaysSo pins settings R19's fallback: where there
// is no working-directory repository, the tab falls back to all-repos and says so, rather
// than painting an empty view or interrupting with a picker.
func TestThisRepoWithoutARepositoryFallsBackAndSaysSo(t *testing.T) {
	m, f := scopedTab(t, workflows.ScopeThisRepo, nowhere,
		writable("cli", "cli"), writable("acme", "infra"))

	want := []string{"acme/infra", "cli/cli"}
	if got := f.names(); !equalStrings(got, want) {
		t.Fatalf("the fallback fetched %v, want every discovered repository %v (settings R19)", got, want)
	}
	view := m.View()
	if !strings.Contains(view, "this-repo") || !strings.Contains(view, "all-repos") {
		t.Errorf("the fallback is not stated in the view; R19 requires it be said, not silent:\n%s", view)
	}
}

// TestThisRepoScopeWithNoResolverFallsBack pins the same fallback when no resolver is wired
// at all, the headless case a golden test and a test harness construct. It falls back rather
// than fanning out over nothing.
func TestThisRepoScopeWithNoResolverFallsBack(t *testing.T) {
	_, f := scopedTab(t, workflows.ScopeThisRepo, nil, writable("cli", "cli"))

	if got, want := f.names(), []string{"cli/cli"}; !equalStrings(got, want) {
		t.Errorf("with no resolver the fan-out covered %v, want the all-repos fallback %v", got, want)
	}
}

// TestThisRepoScopeReachesAnUndiscoveredRepository pins that the working directory's
// repository is fetched whether or not discovery has reported it: this-repo is the repo the
// operator is in, not the intersection of that with the enumeration. Its capability stays
// not-yet-known, so the gate keeps failing closed (R6).
func TestThisRepoScopeReachesAnUndiscoveredRepository(t *testing.T) {
	_, f := scopedTab(t, workflows.ScopeThisRepo, here(rid("solo", "sandbox")), writable("cli", "cli"))

	if got, want := f.names(), []string{"solo/sandbox"}; !equalStrings(got, want) {
		t.Errorf("this-repo fetched %v, want the working directory's repository %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
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
