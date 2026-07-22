package workflows_test

import (
	"testing"

	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// The goldens render the Workflows tab's frame from held state alone, at 100 columns, with no
// terminal and no network. lipgloss v2 renders truecolour regardless of the environment, so
// these bytes are stable on any machine (ADR-0013). Regenerate with:
// go test ./internal/tui/workflows/ -run Golden -update.

// TestGoldenList fixes the cross-repository list (R0, R1) with every state category and both
// gate reasons: an active Workflow offers Disable and a disabled one offers Enable, the three
// disabled states render distinct and verbatim (R2, R3, AC1), a deleted Workflow offers
// neither and labels its Runs Orphaned (R9, R11), a read-only repository and an archived one
// state their distinct reasons (R6), and the STATE column never says Status (R4, AC6).
func TestGoldenList(t *testing.T) {
	readonly := domain.Repo{ID: rid("acme", "infra"), Permissions: domain.Permissions{Push: false}}
	archived := domain.Repo{ID: rid("old", "legacy"), Permissions: domain.Permissions{Push: true}, Archived: true}
	m := newWorkflows(t, 100, 20, nil, nil, writable("cli", "cli"), readonly, archived)
	m = fetched(m, workflows.RepoWorkflows{Repo: rid("cli", "cli"), Complete: true, Workflows: []domain.Workflow{
		workflow(9001, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("cli", "cli")),
		workflow(9002, "Release", ".github/workflows/release.yml", domain.StateDisabledManually, rid("cli", "cli")),
		workflow(9003, "Nightly", ".github/workflows/nightly.yml", domain.StateDisabledInactivity, rid("cli", "cli")),
		workflow(9004, "Old Deploy", ".github/workflows/deploy.yml", domain.StateDeleted, rid("cli", "cli")),
	}})
	m = fetched(m, workflows.RepoWorkflows{Repo: rid("acme", "infra"), Complete: true, Workflows: []domain.Workflow{
		workflow(5001, "Lint", ".github/workflows/lint.yml", domain.StateActive, rid("acme", "infra")),
	}})
	m = fetched(m, workflows.RepoWorkflows{Repo: rid("old", "legacy"), Complete: true, Workflows: []domain.Workflow{
		workflow(6001, "Deploy", ".github/workflows/deploy.yml", domain.StateActive, rid("old", "legacy")),
	}})
	m = send(m, "r") // populate the gate from the discovered repositories

	goldie.New(t).Assert(t, "list", []byte(m.View()))
}

// TestGoldenToggleFailure fixes R7 and AC3: a rejected toggle surfaces the API's permission
// failure on the status line, and the row's displayed state is left unchanged, because only an
// accepted toggle re-reads the list (R8).
func TestGoldenToggleFailure(t *testing.T) {
	tog := &stubToggler{result: ops.Summary{
		Failures: []ops.FailureGroup{{Reason: "HTTP 403: Resource not accessible by personal access token", Count: 1}},
	}}
	m := newWorkflows(t, 100, 14, tog, nil, writable("cli", "cli"))
	m = fetched(m, workflows.RepoWorkflows{Repo: rid("cli", "cli"), Complete: true, Workflows: []domain.Workflow{
		workflow(9001, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("cli", "cli")),
	}})
	m = send(m, "r")
	m = act(t, m, "s") // the toggle is attempted and rejected

	goldie.New(t).Assert(t, "toggle_failure", []byte(m.View()))
}
