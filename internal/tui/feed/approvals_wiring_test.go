package feed

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
)

// stubApprover satisfies approval.Approver so the Approve key can open the decision pane. It
// records nothing and issues no write, because these tests exercise the Feed-to-pane wiring;
// the pane's own submit path and the writes themselves are tested in approval and ops against
// fakes and cassettes (the no-live-write rule).
type stubApprover struct{}

func (stubApprover) ApproveRun(context.Context, domain.RepoID, int64) error    { return nil }
func (stubApprover) ReviewDeployment(context.Context, ops.ReviewRequest) error { return nil }

// stubFetcher satisfies approval.Fetcher so opening a pending-deployment review does not reach
// the wire; the returned fetch Cmd is dropped by these tests, so the pane stays in its loading
// state, which is enough to prove the Feed opened the review.
type stubFetcher struct{}

func (stubFetcher) PendingDeployments(domain.RepoID, int64) ([]approval.PendingDeployment, error) {
	return nil, nil
}

// forkPRRun is a fork-PR Run awaiting approval: Status completed, Conclusion action_required.
func forkPRRun(id int64, started time.Time) domain.Run {
	return mkRun(id, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionActionRequired, started)
}

// waitingRun is a Run awaiting a pending deployment: Status waiting, null Conclusion.
func waitingRun(id int64, started time.Time) domain.Run {
	return mkRun(id, "o", "r", "Deploy", domain.StatusWaiting, "", started)
}

// feedWithApprovals builds a Feed wired to a stub approver and fetcher, so the Approve key can
// open the decision pane over an awaiting Run.
func feedWithApprovals(width, height int) Model {
	m := New(Options{Profile: keys.Standard, Approver: stubApprover{}, ApprovalFetch: stubFetcher{}})
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// TestApprovalBadgeCountsBothKinds pins AC1: the badge counts one fork-PR Run and one pending
// deployment as two, so it is a two-field count, not a Status-only one.
func TestApprovalBadgeCountsBothKinds(t *testing.T) {
	m := newFeed(100, 10)
	id := repoID("o", "r")
	m = feedRuns(m, id,
		forkPRRun(1, t0),
		waitingRun(2, t0.Add(-time.Minute)),
		mkRun(3, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	if got := m.approvalCount(); got != 2 {
		t.Fatalf("approvalCount = %d, want 2 (AC1)", got)
	}
	if !strings.Contains(m.View(), "2 runs awaiting a decision") {
		t.Fatalf("badge did not read the two-kind count (AC1):\n%s", m.View())
	}
}

// TestApprovalBadgeHiddenAtZero pins AC7 and R8: with no awaiting Run the badge is not
// rendered, so it never becomes permanent chrome.
func TestApprovalBadgeHiddenAtZero(t *testing.T) {
	m := newFeed(100, 10)
	m = feedRuns(m, repoID("o", "r"), mkRun(3, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	if _, ok := m.approvalBadgeLine(); ok {
		t.Fatalf("the badge rendered with no awaiting Run (AC7, R8)")
	}
	if strings.Contains(m.View(), "awaiting a decision") {
		t.Fatalf("badge text present at zero count (AC7):\n%s", m.View())
	}
}

// TestApprovalBadgeClearsThroughPoll pins AC11 and R15: when an approved Run's fields change on
// the next poll it drops out of the count, with no request beyond the Feed's ordinary poll.
func TestApprovalBadgeClearsThroughPoll(t *testing.T) {
	m := newFeed(100, 10)
	id := repoID("o", "r")
	m = feedRuns(m, id, waitingRun(2, t0))
	if m.approvalCount() != 1 {
		t.Fatalf("a waiting Run must count 1 before the poll (AC11)")
	}
	// The next poll carries the same Run no longer waiting: the deployment proceeded.
	m = feedRuns(m, id, mkRun(2, "o", "r", "Deploy", domain.StatusInProgress, "", t0))
	if m.approvalCount() != 0 {
		t.Fatalf("the badge did not clear through the ordinary poll (AC11, R15)")
	}
	if _, ok := m.approvalBadgeLine(); ok {
		t.Fatalf("the badge is still rendered after the count returned to zero (AC7, AC11)")
	}
}

// TestApprovalBadgeCountsHeldNotTotalCount pins AC6 and R7: the badge counts the Runs held,
// never a filtered listing's total_count, which reports matches rather than reachable matches.
func TestApprovalBadgeCountsHeldNotTotalCount(t *testing.T) {
	m := newFeed(100, 10)
	id := repoID("o", "r")
	m = feedRuns(m, id, forkPRRun(1, t0), waitingRun(2, t0.Add(-time.Minute)))
	// A filtered listing reported total_count 40 for this repository, but only 2 matching Runs
	// are held. The badge must read the held count, not the 40.
	m.totals[id.String()] = capTotal{reachable: 2, claimed: 40}
	if got := m.approvalCount(); got != 2 {
		t.Fatalf("approvalCount = %d, want 2: the badge counts held Runs, never total_count (AC6, R7)", got)
	}
}

// TestApprovalsFilterActivationAppliesInPlace pins R9 and AC8: activating the badge narrows the
// Feed to the awaiting Runs, leaves the Feed the focused surface, opens no view, and toggling
// off restores the full list.
func TestApprovalsFilterActivationAppliesInPlace(t *testing.T) {
	m := newFeed(100, 12)
	id := repoID("o", "r")
	m = feedRuns(m, id,
		forkPRRun(1, t0),
		waitingRun(2, t0.Add(-time.Minute)),
		mkRun(3, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	if len(m.displayedIDs) != 3 {
		t.Fatalf("want 3 rows before the filter, got %d", len(m.displayedIDs))
	}

	m = m.Update2(press("b")) // ApprovalsFilter, the badge's activation
	if !m.approvalsFilter {
		t.Fatalf("pressing b did not activate the approvals saved filter (R9)")
	}
	if len(m.displayedIDs) != 2 {
		t.Fatalf("the saved filter did not narrow to the 2 awaiting Runs: %v (R9)", m.displayedIDs)
	}
	if m.approvalOpen {
		t.Fatalf("activating the badge opened a pane; AC8 opens no new destination")
	}
	if m.CapturesInput() {
		t.Fatalf("activating the badge captured input; the Feed stays the focused surface (AC8)")
	}

	m = m.Update2(press("b")) // toggle off
	if m.approvalsFilter || len(m.displayedIDs) != 3 {
		t.Fatalf("toggling the filter off did not restore the full view: filter=%v rows=%d", m.approvalsFilter, len(m.displayedIDs))
	}
}

// TestBadgeAndFilterCostNothing pins AC4 and R5: the badge count and the saved filter are pure
// over held Runs, so they work with no I/O seam wired at all, which is what makes them cost no
// request and spend no Budget.
func TestBadgeAndFilterCostNothing(t *testing.T) {
	m := newFeed(100, 10) // Options{Profile} only: no Approver, no ApprovalFetch, no SetViewport
	id := repoID("o", "r")
	m = feedRuns(m, id, forkPRRun(1, t0), waitingRun(2, t0.Add(-time.Minute)))
	if m.approvalCount() != 2 {
		t.Fatalf("the badge count needs no I/O seam (AC4, R5)")
	}
	m = m.Update2(press("b"))
	if !m.approvalsFilter || len(m.displayedIDs) != 2 {
		t.Fatalf("the saved filter needs no I/O seam (AC4, R5)")
	}
}

// TestApproveKeyRoutesForkPRToApprove pins R11, R3 and AC2: the Approve key over a fork-PR Run
// opens the approve action, captures input as a modal, and closes on esc.
func TestApproveKeyRoutesForkPRToApprove(t *testing.T) {
	m := feedWithApprovals(100, 24)
	m = feedRuns(m, repoID("o", "r"), forkPRRun(1, t0))

	m = m.Update2(press("A"))
	if !m.approvalOpen {
		t.Fatalf("the Approve key did not open the decision over the fork-PR Run (R11)")
	}
	if !strings.Contains(m.View(), "Approve fork-PR run") {
		t.Fatalf("the fork-PR Run did not open the approve action (R3, AC2):\n%s", m.View())
	}
	if strings.Contains(m.View(), "Review pending deployments") {
		t.Fatalf("the fork-PR Run offered the review action too (AC2: neither offers the other's)")
	}
	if !m.CapturesInput() {
		t.Fatalf("the decision modal does not capture input (R7)")
	}
	m = m.Update2(press("esc"))
	if m.approvalOpen {
		t.Fatalf("esc did not close the decision pane")
	}
	if m.CapturesInput() {
		t.Fatalf("the Feed still captures input after the pane closed")
	}
}

// TestApproveKeyRoutesPendingDeploymentToReview pins R12, R3 and AC2: the Approve key over a
// pending deployment opens the review action, not the approve action.
func TestApproveKeyRoutesPendingDeploymentToReview(t *testing.T) {
	m := feedWithApprovals(100, 24)
	m = feedRuns(m, repoID("o", "r"), waitingRun(2, t0))

	m = m.Update2(press("A"))
	if !m.approvalOpen {
		t.Fatalf("the Approve key did not open the review over the pending deployment (R12)")
	}
	if !strings.Contains(m.View(), "Review pending deployments") {
		t.Fatalf("the pending deployment did not open the review action (R3, AC2):\n%s", m.View())
	}
	if strings.Contains(m.View(), "Approve fork-PR run") {
		t.Fatalf("the pending deployment offered the fork-PR approve too (AC2: neither offers the other's)")
	}
}

// TestApproveKeyInertOnNonAwaitingRun pins AC2's third leg: a Run awaiting nothing offers no
// action, so the Approve key is inert over it.
func TestApproveKeyInertOnNonAwaitingRun(t *testing.T) {
	m := feedWithApprovals(100, 24)
	m = feedRuns(m, repoID("o", "r"), mkRun(3, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	m = m.Update2(press("A"))
	if m.approvalOpen {
		t.Fatalf("the Approve key opened a decision over a Run awaiting nothing (AC2)")
	}
}

// TestApproveKeyInertWithoutApprover pins that with no approver wired (a golden test, or before
// the write engine is wired) the Approve key is inert, keeping the mutation disabled.
func TestApproveKeyInertWithoutApprover(t *testing.T) {
	m := newFeed(100, 24) // no Approver in Options
	m = feedRuns(m, repoID("o", "r"), forkPRRun(1, t0))
	m = m.Update2(press("A"))
	if m.approvalOpen {
		t.Fatalf("the Approve key opened a decision with no approver wired; it must be inert")
	}
}
