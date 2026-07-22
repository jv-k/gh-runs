package approval_test

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// press builds a KeyPressMsg from a key name, mirroring the confirm, Feed and dispatch test
// helpers so a test drives the pane through the same key.Matches path the runtime does (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

func send(m approval.Model, key string) approval.Model {
	m, _ = m.Update(press(key))
	return m
}

func typeText(m approval.Model, s string) approval.Model {
	for _, r := range s {
		m = send(m, string(r))
	}
	return m
}

// runCmd runs a Cmd and feeds its message back into the model, so a test drives the async
// submit and fetch paths deterministically.
func runCmd(m approval.Model, cmd tea.Cmd) approval.Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	m, _ = m.Update(msg)
	return m
}

// approveCall records one ApproveRun request.
type approveCall struct {
	repo  domain.RepoID
	runID int64
}

// fakeApprover records the requests it is handed and returns a canned outcome, so a test
// drives the pane's submit path with no live approve or review (the no-live-write rule; every
// real write is a cassette in ops).
type fakeApprover struct {
	approveCalls []approveCall
	reviewCalls  []ops.ReviewRequest
	approveErr   error
	reviewErr    error
}

func (f *fakeApprover) ApproveRun(_ context.Context, repo domain.RepoID, runID int64) error {
	f.approveCalls = append(f.approveCalls, approveCall{repo, runID})
	return f.approveErr
}

func (f *fakeApprover) ReviewDeployment(_ context.Context, req ops.ReviewRequest) error {
	f.reviewCalls = append(f.reviewCalls, req)
	return f.reviewErr
}

// fakeFetcher returns held deployments, so a test drives the fetch path with no network.
type fakeFetcher struct {
	deployments []approval.PendingDeployment
	err         error
	calls       int
}

func (f *fakeFetcher) PendingDeployments(_ domain.RepoID, _ int64) ([]approval.PendingDeployment, error) {
	f.calls++
	return f.deployments, f.err
}

// openForkPR opens the pane over a fork-PR Run, the Kind the Feed classified from a completed
// Run with an action_required Conclusion.
func openForkPR(t *testing.T, opts approval.Options) approval.Model {
	t.Helper()
	m := approval.New(opts)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Open(approval.Target{Repo: rid("o", "r"), RunID: 123, Kind: approvals.KindForkPR, Title: "CI"})
	return m
}

// openDeployment opens the pane over a pending-deployment Run and injects its fetched
// environments, returning a laid-out, loaded model with no network (the golden path).
func openDeployment(t *testing.T, opts approval.Options, ds ...approval.PendingDeployment) approval.Model {
	t.Helper()
	m := approval.New(opts)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Open(approval.Target{Repo: rid("o", "r"), RunID: 456, Kind: approvals.KindPendingDeployment, Title: "Deploy"})
	m, _ = m.Update(approval.DeploymentsLoaded{Deployments: ds})
	return m
}

// TestForkPROpensToApproveOnly pins R11 and AC2: a fork-PR Run offers only the approve
// action, no comment and no environments, because /approve takes none.
func TestForkPROpensToApproveOnly(t *testing.T) {
	m := openForkPR(t, approval.Options{Profile: keys.Standard, Approver: &fakeApprover{}})
	view := m.View()
	if !strings.Contains(view, "Approve fork-PR run") {
		t.Errorf("the fork-PR pane did not render the approve prompt (R11): %q", view)
	}
	if strings.Contains(strings.ToLower(view), "comment") || strings.Contains(strings.ToLower(view), "environment") {
		t.Errorf("the fork-PR pane offered a comment or an environment; /approve takes neither (AC2): %q", view)
	}
}

// TestForkPRSubmitApproves pins R11: submitting a fork-PR approval calls ApproveRun for the
// Run and reports success.
func TestForkPRSubmitApproves(t *testing.T) {
	ap := &fakeApprover{}
	m := openForkPR(t, approval.Options{Profile: keys.Standard, Approver: ap})

	m, cmd := m.Update(press("A")) // Approve submits
	if cmd == nil {
		t.Fatal("submit issued no Cmd; a fork-PR approval must call ApproveRun (R11)")
	}
	m = runCmd(m, cmd)
	if len(ap.approveCalls) != 1 || ap.approveCalls[0].runID != 123 {
		t.Fatalf("ApproveRun calls = %v, want one for run 123 (R11)", ap.approveCalls)
	}
	if len(ap.reviewCalls) != 0 {
		t.Errorf("a fork-PR approval called ReviewDeployment; it routes to /approve alone (R3, AC2)")
	}
	if !strings.Contains(strings.ToLower(m.View()), "approved") {
		t.Errorf("the pane did not report the approval succeeded (R11): %q", m.View())
	}
}

// TestForkPRForbiddenIsOutcomeNotError pins R14: a 403 approving a fork-PR Run is the
// not-a-designated-reviewer outcome, not an error, and it is never retried.
func TestForkPRForbiddenIsOutcomeNotError(t *testing.T) {
	ap := &fakeApprover{approveErr: &ops.ApprovalError{Code: 403, Message: "no permission"}}
	m := openForkPR(t, approval.Options{Profile: keys.Standard, Approver: ap})

	m, cmd := m.Update(press("A"))
	m = runCmd(m, cmd)
	if !strings.Contains(strings.ToLower(m.View()), "not a designated reviewer") {
		t.Errorf("a 403 must read as the not-a-designated-reviewer outcome (R14): %q", m.View())
	}
	// R14: no retry. A second submit after the outcome issues nothing.
	_, cmd2 := m.Update(press("A"))
	if cmd2 != nil {
		t.Errorf("a second submit after a 403 issued a Cmd; R14 forbids a retry")
	}
	if len(ap.approveCalls) != 1 {
		t.Errorf("ApproveRun called %d times, want exactly 1 (R14 forbids a retry)", len(ap.approveCalls))
	}
}

// TestDeploymentOpenFetchesEnvironments pins R12: opening over a pending deployment fetches
// the environments the Run awaits before the review is submittable.
func TestDeploymentOpenFetchesEnvironments(t *testing.T) {
	fetch := &fakeFetcher{deployments: []approval.PendingDeployment{{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected", CurrentUserCanApprove: true}}}
	m := approval.New(approval.Options{Profile: keys.Standard, Approver: &fakeApprover{}, Fetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.Open(approval.Target{Repo: rid("o", "r"), RunID: 456, Kind: approvals.KindPendingDeployment})
	if cmd == nil {
		t.Fatal("opening a pending-deployment review must fetch its environments (R12)")
	}
	m = runCmd(m, cmd)
	if fetch.calls != 1 {
		t.Errorf("PendingDeployments called %d times, want 1 (R12)", fetch.calls)
	}
	if !strings.Contains(m.View(), "scribe-protected") {
		t.Errorf("the review did not present the fetched environment (R12): %q", m.View())
	}
}

// TestReviewSubmitsEnvironmentIDsStateAndComment pins R12: an approve review submits the
// environment ids, the state approved and the collected comment.
func TestReviewSubmitsEnvironmentIDsStateAndComment(t *testing.T) {
	ap := &fakeApprover{}
	m := openDeployment(t, approval.Options{Profile: keys.Standard, Approver: ap},
		approval.PendingDeployment{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected", CurrentUserCanApprove: true})

	m = send(m, "enter") // edit the comment
	m = typeText(m, "ship it")
	m = send(m, "enter") // commit

	m, cmd := m.Update(press("A")) // submit
	if cmd == nil {
		t.Fatal("a review with a comment must submit (R12)")
	}
	m = runCmd(m, cmd)
	if len(ap.reviewCalls) != 1 {
		t.Fatalf("ReviewDeployment calls = %d, want 1 (R12)", len(ap.reviewCalls))
	}
	req := ap.reviewCalls[0]
	if len(req.EnvironmentIDs) != 1 || req.EnvironmentIDs[0] != 3734916060 {
		t.Errorf("review targeted %v, want [3734916060] (R12)", req.EnvironmentIDs)
	}
	if req.State != ops.ReviewApproved {
		t.Errorf("review state = %q, want approved (R12)", req.State)
	}
	if req.Comment != "ship it" {
		t.Errorf("review comment = %q, want the edited comment (R12)", req.Comment)
	}
	if len(ap.approveCalls) != 0 {
		t.Errorf("a deployment review called ApproveRun; it routes to /pending_deployments alone (R3, AC2)")
	}
	if !strings.Contains(strings.ToLower(m.View()), "deployment approved") {
		t.Errorf("the pane did not report the review was approved (R12): %q", m.View())
	}
}

// TestReviewToggleSubmitsReject pins R12's reject branch: flipping the decision submits state
// rejected.
func TestReviewToggleSubmitsReject(t *testing.T) {
	ap := &fakeApprover{}
	m := openDeployment(t, approval.Options{Profile: keys.Standard, Approver: ap},
		approval.PendingDeployment{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected"})

	m = send(m, "space") // flip approve -> reject
	m = send(m, "enter") // edit comment
	m = typeText(m, "not now")
	m = send(m, "enter")

	m, cmd := m.Update(press("A"))
	m = runCmd(m, cmd)
	if len(ap.reviewCalls) != 1 || ap.reviewCalls[0].State != ops.ReviewRejected {
		t.Fatalf("review state = %v, want rejected after the toggle (R12)", ap.reviewCalls)
	}
	if !strings.Contains(strings.ToLower(m.View()), "rejected") {
		t.Errorf("the pane did not report the review was rejected (R12): %q", m.View())
	}
}

// TestReviewRefusesEmptyComment pins R13 and AC9: submitting a review with an empty comment
// is refused and issues no request.
func TestReviewRefusesEmptyComment(t *testing.T) {
	ap := &fakeApprover{}
	m := openDeployment(t, approval.Options{Profile: keys.Standard, Approver: ap},
		approval.PendingDeployment{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected"})

	m, cmd := m.Update(press("A")) // submit with no comment
	if cmd != nil {
		t.Errorf("an empty-comment review issued a Cmd; R13 forbids it (AC9)")
	}
	if len(ap.reviewCalls) != 0 {
		t.Errorf("an empty-comment review called ReviewDeployment; R13 issues nothing (AC9)")
	}
	if !strings.Contains(strings.ToLower(m.View()), "comment") {
		t.Errorf("the refusal did not name the required comment (R13): %q", m.View())
	}
}

// TestReviewForbiddenIsOutcomeNotError pins R14 and AC10: a 403 review reads as the
// not-a-designated-reviewer outcome, issues no retry, and the pane surfaces the outcome
// without a failure style.
func TestReviewForbiddenIsOutcomeNotError(t *testing.T) {
	ap := &fakeApprover{reviewErr: &ops.ApprovalError{Code: 403, Message: "no permission"}}
	m := openDeployment(t, approval.Options{Profile: keys.Standard, Approver: ap},
		approval.PendingDeployment{EnvironmentID: 3734916060, EnvironmentName: "scribe-protected", CurrentUserCanApprove: false})

	m = send(m, "enter")
	m = typeText(m, "please")
	m = send(m, "enter")

	m, cmd := m.Update(press("A"))
	m = runCmd(m, cmd)
	if !strings.Contains(strings.ToLower(m.View()), "not a designated reviewer") {
		t.Errorf("a 403 review must read as the not-a-designated-reviewer outcome (R14, AC10): %q", m.View())
	}
	_, cmd2 := m.Update(press("A"))
	if cmd2 != nil {
		t.Errorf("a second submit after a 403 issued a Cmd; AC10 forbids a retry")
	}
	if len(ap.reviewCalls) != 1 {
		t.Errorf("ReviewDeployment called %d times, want exactly 1 (AC10 forbids a retry)", len(ap.reviewCalls))
	}
}

// TestDoubleSubmitIssuesOneWrite pins the mutation safety: a second submit while one is in
// flight issues no second request, so a double keypress cannot approve twice.
func TestDoubleSubmitIssuesOneWrite(t *testing.T) {
	ap := &fakeApprover{}
	m := openForkPR(t, approval.Options{Profile: keys.Standard, Approver: ap})

	m, cmd := m.Update(press("A"))
	if cmd == nil {
		t.Fatal("first submit issued no Cmd")
	}
	_, cmd2 := m.Update(press("A")) // in flight: inert
	if cmd2 != nil {
		t.Errorf("a second submit while a write is in flight issued a Cmd; it must not write twice")
	}
	_ = runCmd(m, cmd) // run the first submit's Cmd for its recorded call
	if len(ap.approveCalls) != 1 {
		t.Errorf("issued %d approvals, want exactly 1 (mutation safety)", len(ap.approveCalls))
	}
}

// TestCloseFromEscAndNilApproverInert pins the pane's boundaries: esc closes, and with no
// Approver wired the submit key issues nothing (the golden path, mutation disabled).
func TestCloseFromEscAndNilApproverInert(t *testing.T) {
	m := openForkPR(t, approval.Options{Profile: keys.Standard}) // no Approver
	m, cmd := m.Update(press("A"))
	if cmd != nil {
		t.Errorf("submit with no Approver wired issued a Cmd; it must be inert")
	}
	m = send(m, "esc")
	if m.IsOpen() {
		t.Errorf("esc did not close the pane")
	}
	if m.View() != "" {
		t.Errorf("a closed pane still painted a view: %q", m.View())
	}
}
