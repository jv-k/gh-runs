package workflows_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// deletedList is a repository holding one active and one deleted Workflow, the pair R13's
// navigation is stated over. The rows sort by name within a repository, so CI is row 0 and
// Old Deploy is row 1.
func deletedList() workflows.RepoWorkflows {
	return workflows.RepoWorkflows{Repo: rid("cli", "cli"), Complete: true, Workflows: []domain.Workflow{
		workflow(9001, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("cli", "cli")),
		workflow(9004, "Old Deploy", ".github/workflows/deploy.yml", domain.StateDeleted, rid("cli", "cli")),
	}}
}

// navRequest presses the navigation key and returns the request the Cmd carries, failing
// when it carried none.
func navRequest(t *testing.T, m workflows.Model) workflows.NavigateToRuns {
	t.Helper()
	_, cmd := m.Update(press("enter"))
	if cmd == nil {
		t.Fatal("the navigation key returned no Cmd; R13 requires a deleted Workflow to lead to its Runs")
	}
	out := cmd()
	msg, ok := out.(workflows.NavigateToRuns)
	if !ok {
		t.Fatalf("the navigation Cmd yielded %T, want workflows.NavigateToRuns", out)
	}
	return msg
}

// TestDeletedWorkflowNavigatesToItsRuns pins R13 and AC4's navigation half: the row of a
// Workflow in state deleted leads to its Runs, and the request names the Workflow by its
// numeric id. The id is what survives deletion: it is stamped on every Run the Workflow
// produced and stays unique once the YAML is gone, while a name can be taken by a later
// Workflow. The tab only asks; the cross-tab move is the root's, because a tab may never
// import another tab (ADR-0011).
func TestDeletedWorkflowNavigatesToItsRuns(t *testing.T) {
	m := newWorkflows(t, 100, 20, nil, nil, writable("cli", "cli"))
	m = fetched(m, deletedList())
	m = send(m, "r")    // populate the gate from the discovered repositories
	m = send(m, "down") // move to the deleted Workflow

	got := navRequest(t, m)
	if got.Filter.Workflow != "9004" {
		t.Errorf("navigation Filter.Workflow = %q, want %q: the Feed selects the Workflow by the id its Runs carry", got.Filter.Workflow, "9004")
	}
	if q := got.Filter.QueryString(); q != "workflow:9004" {
		t.Errorf("navigation filter renders as %q, want %q", q, "workflow:9004")
	}
}

// TestNavigationIsOfferedRegardlessOfState pins R14: a Workflow's Runs stay listable
// whatever its state, so the navigation is not confined to the deleted rows R13 names. A
// disabled or deleted Workflow's Runs are ordinary Runs.
func TestNavigationIsOfferedRegardlessOfState(t *testing.T) {
	m := newWorkflows(t, 100, 20, nil, nil, writable("cli", "cli"))
	m = fetched(m, deletedList())
	m = send(m, "r")

	if got := navRequest(t, m); got.Filter.Workflow != "9001" {
		t.Errorf("navigation from the active Workflow selects %q, want %q (R14)", got.Filter.Workflow, "9001")
	}
}

// TestNavigationInertWithoutARow pins that the key issues nothing over an empty list, so a
// tab that has fetched nothing yet cannot ask the root to navigate to a Workflow it does
// not hold.
func TestNavigationInertWithoutARow(t *testing.T) {
	m := newWorkflows(t, 100, 20, nil, nil, writable("cli", "cli"))
	if _, cmd := m.Update(press("enter")); cmd != nil {
		t.Error("the navigation key returned a Cmd over an empty list, want none")
	}
}
