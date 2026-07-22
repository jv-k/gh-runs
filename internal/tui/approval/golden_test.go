package approval_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
)

// The goldens render the decision pane from held state alone, at 100 columns, with no
// terminal and no network. lipgloss v2 renders truecolour regardless of the environment, so
// these bytes are stable on any machine (ADR-0013). Regenerate with:
// go test ./internal/tui/approval/ -run Golden -update.

// laid returns the pane laid out at 100 columns.
func laid() approval.Model {
	m := approval.New(approval.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// TestGoldenForkPRApproval fixes the fork-PR approval prompt: the Run it approves and a single
// approve action, with no comment and no environments (R11, AC2).
func TestGoldenForkPRApproval(t *testing.T) {
	m := laid()
	m, _ = m.Open(approval.Target{Repo: rid("cli", "cli"), RunID: 29516338954, Kind: approvals.KindForkPR, Title: "CI"})
	goldie.New(t).Assert(t, "fork_pr_approval", []byte(m.View()))
}

// TestGoldenDeploymentReview fixes the pending-deployment review: the environments the Run
// awaits with their reviewers and the current_user_can_approve indicator, the approve-or-reject
// decision defaulting to approve, and the required comment prompt (R10, R12, R13).
func TestGoldenDeploymentReview(t *testing.T) {
	m := laid()
	m, _ = m.Open(approval.Target{Repo: rid("pytorch", "pytorch"), RunID: 29350572503, Kind: approvals.KindPendingDeployment, Title: "Docker build"})
	m, _ = m.Update(approval.DeploymentsLoaded{Deployments: []approval.PendingDeployment{
		{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected", CurrentUserCanApprove: true, Reviewers: []string{"octocat", "deploy-team"}},
	}})
	goldie.New(t).Assert(t, "deployment_review", []byte(m.View()))
}

// TestGoldenDeploymentReviewNotReviewer fixes R10 and R14 at the row: an environment the
// current user cannot approve is shown as not-your-review, and the review is still presented
// because reviewer standing is discovered by acting, not pre-gated.
func TestGoldenDeploymentReviewNotReviewer(t *testing.T) {
	m := laid()
	m, _ = m.Open(approval.Target{Repo: rid("acme", "api"), RunID: 42, Kind: approvals.KindPendingDeployment, Title: "Deploy"})
	m, _ = m.Update(approval.DeploymentsLoaded{Deployments: []approval.PendingDeployment{
		{EnvironmentID: 100, EnvironmentName: "production", CurrentUserCanApprove: false, Reviewers: []string{"release-team"}},
	}})
	goldie.New(t).Assert(t, "deployment_review_not_reviewer", []byte(m.View()))
}
