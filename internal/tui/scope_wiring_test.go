package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// rootWithWorkflowScope builds a root whose Workflows tab runs under one scope, recording
// which repositories its fan-out fetched, then focuses that tab and refreshes it.
func rootWithWorkflowScope(t *testing.T, scope workflows.Scope, current func() (domain.RepoID, bool)) *[]domain.RepoID {
	t.Helper()
	hit := &[]domain.RepoID{}
	repos := []domain.Repo{
		{ID: repoID("cli", "cli"), Permissions: domain.Permissions{Push: true}},
		{ID: repoID("acme", "infra"), Permissions: domain.Permissions{Push: true}},
	}
	m := New(Options{
		Profile: keys.Standard,
		Repos:   func() []domain.Repo { return repos },
		WorkflowFetch: func(id domain.RepoID) workflows.RepoWorkflows {
			*hit = append(*hit, id)
			return workflows.RepoWorkflows{Repo: id, Complete: true}
		},
		WorkflowScope:       scope,
		WorkflowCurrentRepo: current,
	})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = step(t, m, press("tab")) // focus the Workflows tab
	drainInto(t, m, press("r"))  // refresh, which is what fans out
	return hit
}

// TestWorkflowScopeReachesTheTab pins that R0's scope is wired end to end from the root's
// seams: constructed under this-repo, the Workflows tab's fan-out covers the working
// directory's repository alone. The setting that selects the scope is [settings] R19's and is
// not built, so main.go states no scope and the tab runs the all-repos default; this asserts
// the path that setting will drive is real rather than notional.
func TestWorkflowScopeReachesTheTab(t *testing.T) {
	hit := rootWithWorkflowScope(t, workflows.ScopeThisRepo, func() (domain.RepoID, bool) {
		return repoID("acme", "infra"), true
	})

	if len(*hit) != 1 || (*hit)[0] != repoID("acme", "infra") {
		t.Fatalf("this-repo fanned out over %v, want acme/infra alone (R0)", *hit)
	}
}

// TestWorkflowScopeDefaultsToAllRepos pins the default the root builds today: with no scope
// stated, the fan-out covers every discovered repository, exactly as before the scope existed
// (R0's default).
func TestWorkflowScopeDefaultsToAllRepos(t *testing.T) {
	hit := rootWithWorkflowScope(t, "", nil)

	if len(*hit) != 2 {
		t.Fatalf("the default scope fanned out over %v, want every discovered repository (R0)", *hit)
	}
}
