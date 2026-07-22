package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// ErrEmptyComment is returned when a deployment review is submitted with an empty comment.
// R13 refuses it and issues no request, and ops enforces the refusal here as well as in the
// pane, so the rule holds even if a caller reaches ops directly (AC9).
var ErrEmptyComment = errors.New("ops: a deployment review requires a comment")

// ReviewState is the decision a deployment review carries: approved or rejected (approvals
// R12). The value is sent verbatim as the request's state, so it is the API's own vocabulary
// and never a Status or a Conclusion, which live on the Run and not on a review.
type ReviewState string

const (
	ReviewApproved ReviewState = "approved"
	ReviewRejected ReviewState = "rejected"
)

// ReviewRequest is one pending-deployment review: the Run, the environment ids it targets,
// the decision, and the comment it carries (approvals R12). The plural environment ids are
// intentional: nothing bounds a submission to one, and the request shape supports several
// whether or not a Run can await several (R12's resolved open question).
type ReviewRequest struct {
	Repo           domain.RepoID
	RunID          int64
	EnvironmentIDs []int64
	State          ReviewState
	Comment        string
}

// ApprovalError is an approve or a review the API rejected, carrying the status code and
// the API's own message. A 403 is R14's expected outcome, which NotAReviewer reads; every
// other code is a genuine failure the surface states with the API's reason. It is a distinct
// type so a caller reads the code with errors.As.
type ApprovalError struct {
	Code    int
	Message string
}

func (e *ApprovalError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("approval rejected: HTTP %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("approval rejected: HTTP %d", e.Code)
}

// NotAReviewer reports whether err is a 403 from an approve or a review, R14's expected
// outcome: the account is not a designated reviewer, which is never an error, a retry or a
// fault. It holds even when a 403 arrives despite permissions.push, because fine-grained
// PATs expose no scopes and the API is the final authority (R14). errors.As unwraps, so a
// wrapped rejection is caught too.
func NotAReviewer(err error) bool {
	var ae *ApprovalError
	return errors.As(err, &ae) && ae.Code == http.StatusForbidden
}

// pendingDeploymentsBody is the review POST body: the environment ids, the state and the
// comment (approvals R12). Every field is sent, because the review targets specific
// environments with an explicit decision and a required comment (R13).
type pendingDeploymentsBody struct {
	EnvironmentIDs []int64 `json:"environment_ids"`
	State          string  `json:"state"`
	Comment        string  `json:"comment"`
}

// ApproveRun approves a fork-PR Run awaiting maintainer approval (approvals R11). It POSTs
// the run's /approve endpoint with no body.
//
// It is deliberately NOT Execute, which keeps Execute's invariant literally intact: Execute
// is the only call that issues a DELETE and the only call that writes the deletion log
// (ADR-0011, ADR-0019). An approval creates nothing to delete and records nothing, so it
// opens no log and issues no DELETE, exactly as Dispatch is a POST beside Execute rather
// than a distortion of the frozen-set machinery. It travels the same store-then-governor-
// then-limiter transport every other write does, so the governor paces it by method.
//
// R11 does not pre-gate: a 403 is an expected result discovered by acting, not a fault
// (R14), and pre-flight is impossible for fine-grained PATs anyway, so the API stays the
// authority. A 403 surfaces as an ApprovalError NotAReviewer reads; a single POST is issued
// and never retried.
func (o *Ops) ApproveRun(ctx context.Context, repo domain.RepoID, runID int64) error {
	resp, err := o.client.RequestWithContext(ctx, http.MethodPost, approvePath(repo, runID), nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("approve: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &ApprovalError{Code: resp.StatusCode, Message: apiMessage(resp)}
	}
	return nil
}

// ReviewDeployment approves or rejects a Run's pending deployments (approvals R12). It POSTs
// the run's /pending_deployments endpoint with the environment ids, the state and the
// comment.
//
// It refuses an empty comment before issuing a request (R13, AC9), the same refusal the
// pane makes, so the rule holds even if a caller reaches ops directly. Like ApproveRun it is
// a single POST that issues no DELETE and opens no deletion log, so Execute stays the sole
// DELETE and sole deletion-log writer. A 403 is R14's expected outcome, surfaced as an
// ApprovalError NotAReviewer reads, and never retried.
func (o *Ops) ReviewDeployment(ctx context.Context, req ReviewRequest) error {
	if strings.TrimSpace(req.Comment) == "" {
		return ErrEmptyComment // R13: refuse, and issue nothing (AC9)
	}
	raw, err := json.Marshal(pendingDeploymentsBody{
		EnvironmentIDs: req.EnvironmentIDs,
		State:          string(req.State),
		Comment:        req.Comment,
	})
	if err != nil {
		return fmt.Errorf("review: encoding the request failed: %w", err)
	}
	resp, err := o.client.RequestWithContext(ctx, http.MethodPost, pendingDeploymentsPath(req.Repo, req.RunID), bytes.NewReader(raw))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("review: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &ApprovalError{Code: resp.StatusCode, Message: apiMessage(resp)}
	}
	return nil
}

// approvePath is the approve-a-workflow-run-for-a-fork-pull-request endpoint, addressing the
// Run by id (approvals R11).
func approvePath(repo domain.RepoID, runID int64) string {
	return fmt.Sprintf("repos/%s/%s/actions/runs/%d/approve", repo.Owner, repo.Name, runID)
}

// pendingDeploymentsPath is the review-pending-deployments endpoint, addressing the Run by
// id (approvals R12). It is the same path the pane's GET reads the awaited environments
// from, so the ids the review targets are the ids that GET returned.
func pendingDeploymentsPath(repo domain.RepoID, runID int64) string {
	return fmt.Sprintf("repos/%s/%s/actions/runs/%d/pending_deployments", repo.Owner, repo.Name, runID)
}

// apiMessage reads the API's own message from a rejection body so an ApprovalError names it,
// falling back to the bare status where the body carries none. It reads the full body the
// response owns, which the caller closes.
func apiMessage(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		return payload.Message
	}
	return ""
}
