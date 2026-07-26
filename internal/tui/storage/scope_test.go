package storage_test

import (
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// fanOut records which repositories the refresh fetched, the observable that says which
// scope the tab ran under (R0). It answers empty storage, so the tab holds nothing beyond
// the repositories themselves and the test reads the fan-out alone.
type fanOut struct{ hit []domain.RepoID }

func (f *fanOut) fetch(id domain.RepoID) storage.RepoStorage {
	f.hit = append(f.hit, id)
	return storage.RepoStorage{Repo: id, ArtifactsComplete: true}
}

func (f *fanOut) names() []string {
	out := make([]string, 0, len(f.hit))
	for _, id := range f.hit {
		out = append(out, id.Owner+"/"+id.Name)
	}
	sort.Strings(out)
	return out
}

// scopedTab builds a Storage tab under one scope, over the given working-directory
// repository resolution and discovered repositories, and refreshes it so the fan-out runs.
func scopedTab(t *testing.T, scope storage.Scope, current func() (domain.RepoID, bool), repos ...domain.Repo) (storage.Model, *fanOut) {
	t.Helper()
	f := &fanOut{}
	rr := append([]domain.Repo(nil), repos...)
	m := storage.New(storage.Options{
		Profile:     keys.Standard,
		Fetch:       f.fetch,
		Repos:       func() []domain.Repo { return rr },
		Scope:       scope,
		CurrentRepo: current,
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	_, cmd := m.Update(press("r"))
	m = drainStorageCmd(t, m, cmd)
	return m, f
}

func here(id domain.RepoID) func() (domain.RepoID, bool) {
	return func() (domain.RepoID, bool) { return id, true }
}

func nowhere() (domain.RepoID, bool) { return domain.RepoID{}, false }

// TestDefaultScopeIsAllRepos pins R0's default: the zero Scope is all-repos, and the fan-out
// covers the whole discovered set, one cache-usage request per repository over ADR-0003's
// machinery. "Which of my 163 repositories is hoarding Caches?" is the question this view
// exists to answer, and a single-repository view cannot answer it at all.
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
// and the fan-out covers it alone. R0 requires both paths to exist and both to be correct.
func TestThisRepoScopeCoversTheWorkingDirectoryRepositoryAlone(t *testing.T) {
	m, f := scopedTab(t, storage.ScopeThisRepo, here(rid("acme", "infra")),
		writable("cli", "cli"), writable("acme", "infra"), writable("old", "legacy"))

	if got, want := f.names(), []string{"acme/infra"}; !equalStrings(got, want) {
		t.Fatalf("this-repo fetched %v, want %v alone (R0, settings R19)", got, want)
	}
	if view := m.View(); !strings.Contains(view, "this-repo") {
		t.Errorf("the view does not state the scope it ran under:\n%s", view)
	}
}

// TestThisRepoScopePresentsOneRepositoryAndNoRollup pins R0's "under this-repo it presents
// one repository": the per-repository rollup leads the all-repos view because it answers the
// cross-repository question, and one repository is its own rollup.
func TestThisRepoScopePresentsOneRepositoryAndNoRollup(t *testing.T) {
	m, _ := scopedTab(t, storage.ScopeThisRepo, here(rid("acme", "infra")),
		writable("cli", "cli"), writable("acme", "infra"))
	m = fetched(m, oneCache("acme", "infra", 1, "setup-go", 302460229))

	view := m.View()
	if strings.Contains(view, "REPOSITORY           CACHES") {
		t.Errorf("this-repo painted the cross-repository rollup over one repository:\n%s", view)
	}
	if strings.Contains(view, "cli/cli") {
		t.Errorf("this-repo painted a repository outside the scope:\n%s", view)
	}
}

// TestThisRepoWithoutARepositoryFallsBackAndSaysSo pins settings R19's fallback: where there
// is no working-directory repository, the tab falls back to all-repos and says so, rather
// than painting an empty view or interrupting with a picker.
func TestThisRepoWithoutARepositoryFallsBackAndSaysSo(t *testing.T) {
	m, f := scopedTab(t, storage.ScopeThisRepo, nowhere,
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
// at all, the headless case a golden test and a test harness construct.
func TestThisRepoScopeWithNoResolverFallsBack(t *testing.T) {
	_, f := scopedTab(t, storage.ScopeThisRepo, nil, writable("cli", "cli"))

	if got, want := f.names(), []string{"cli/cli"}; !equalStrings(got, want) {
		t.Errorf("with no resolver the fan-out covered %v, want the all-repos fallback %v", got, want)
	}
}

// TestThisRepoScopeReachesAnUndiscoveredRepository pins that the working directory's
// repository is fetched whether or not discovery has reported it: this-repo is the repository
// the operator is in, not the intersection of that with the enumeration. Its capability stays
// not-yet-known, so the reclamation gate keeps failing closed (R20, repo-discovery R8).
func TestThisRepoScopeReachesAnUndiscoveredRepository(t *testing.T) {
	m, f := scopedTab(t, storage.ScopeThisRepo, here(rid("solo", "sandbox")), writable("cli", "cli"))

	if got, want := f.names(), []string{"solo/sandbox"}; !equalStrings(got, want) {
		t.Fatalf("this-repo fetched %v, want the working directory's repository %v", got, want)
	}
	m = fetched(m, oneCache("solo", "sandbox", 7, "setup-go", 302460229))
	m = send(m, "space")
	m = send(m, "d")
	if m.CapturesInput() {
		t.Errorf("a repository discovery has not reported must offer no reclamation (R20, repo-discovery R8)")
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
