package ops_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// runWrite runs a single-shot write in a goroutine and drives the fake clock so the paced
// POST releases without a real sleep, exactly as runDispatch does for a Dispatch and
// runPurge for a Purge (an approve and a review are writes, so the governor paces them,
// rate-governor R2). It returns the write's error. A write that returns before touching
// the transport (an empty-comment refusal) still unwinds cleanly, because the goroutine
// cancels runCtx the instant it returns and the driver breaks.
func runWrite(t *testing.T, h *harness, fn func(context.Context) error) error {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := fn(runCtx)
		cancel()
		done <- err
	}()
	const quantum = 200 * time.Millisecond
	for {
		if err := h.clk.BlockUntilContext(runCtx, 1); err != nil {
			break
		}
		h.clk.Advance(quantum)
	}
	return <-done
}

// TestApproveRunPostsApprove pins approvals R11 against a cassette, with no live approve: a
// fork-PR Run's approval is a single POST to the run's /approve endpoint that returns a
// 2xx. It issues no DELETE and writes no deletion log, because approving a Run creates
// nothing to delete and Execute stays the sole DELETE and sole deletion-log writer
// (ADR-0011, ADR-0019). Every write here is a taped POST replayed in ModeReplayOnly.
func TestApproveRunPostsApprove(t *testing.T) {
	h := newHarness(t, "approve_ok", 50, 50)

	err := runWrite(t, h, func(ctx context.Context) error {
		return h.ops.ApproveRun(ctx, repoID("o", "r"), 123)
	})
	if err != nil {
		t.Fatalf("ApproveRun returned an error on a 2xx: %v (R11)", err)
	}

	posts := h.counting.urls("POST")
	if len(posts) != 1 || !strings.HasSuffix(posts[0], "/actions/runs/123/approve") {
		t.Fatalf("want exactly one POST to the run's /approve endpoint, got %v (R11)", posts)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("ApproveRun issued a DELETE; an approval deletes nothing (DELETE safety)")
	}
	if h.logExists() {
		t.Errorf("ApproveRun wrote a deletion log; only Execute opens it (ADR-0019)")
	}
}

// TestApproveRunForbiddenIsNotAReviewer pins R14 and R11's resolved open question: a 403
// from an approve is an expected outcome, not a fault, discovered by acting. It surfaces as
// an *ops.ApprovalError carrying the code, and NotAReviewer reads it as the
// not-a-designated-reviewer outcome, so the surface treats it as an outcome rather than an
// error, a retry or a fault. It holds despite permissions.push, because fine-grained PATs
// expose no scopes and the API is the final authority.
func TestApproveRunForbiddenIsNotAReviewer(t *testing.T) {
	h := newHarness(t, "approve_forbidden", 50, 50)

	err := runWrite(t, h, func(ctx context.Context) error {
		return h.ops.ApproveRun(ctx, repoID("o", "r"), 123)
	})
	if err == nil {
		t.Fatal("ApproveRun accepted a 403; it must surface it (R14)")
	}
	var ae *ops.ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("error is %T, want *ops.ApprovalError so the surface can read the code (R14): %v", err, err)
	}
	if ae.Code != 403 {
		t.Errorf("ApprovalError.Code = %d, want 403 (R14)", ae.Code)
	}
	if !ops.NotAReviewer(err) {
		t.Errorf("NotAReviewer(err) = false, want true: a 403 is the not-a-designated-reviewer outcome (R14)")
	}
	// R14: no retry. A single POST left, never a second attempt at the same approval.
	if n := h.counting.countMethod("POST"); n != 1 {
		t.Errorf("a 403 approve issued %d POSTs, want exactly 1 (R14 forbids a retry)", n)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("ApproveRun issued a DELETE (DELETE safety)")
	}
}

// TestReviewDeploymentApproves pins R12 against a cassette: a deployment review is a single
// POST to the run's /pending_deployments endpoint carrying the environment ids, the state
// approved and the comment. It issues no DELETE and writes no deletion log.
func TestReviewDeploymentApproves(t *testing.T) {
	h := newHarness(t, "review_approved", 50, 50)

	err := runWrite(t, h, func(ctx context.Context) error {
		return h.ops.ReviewDeployment(ctx, ops.ReviewRequest{
			Repo:           repoID("o", "r"),
			RunID:          456,
			EnvironmentIDs: []int64{3734916060},
			State:          ops.ReviewApproved,
			Comment:        "ship it",
		})
	})
	if err != nil {
		t.Fatalf("ReviewDeployment returned an error on a 2xx: %v (R12)", err)
	}

	posts := h.counting.urls("POST")
	if len(posts) != 1 || !strings.HasSuffix(posts[0], "/actions/runs/456/pending_deployments") {
		t.Fatalf("want one POST to the /pending_deployments endpoint, got %v (R12)", posts)
	}
	body, ok := h.counting.postBody("/pending_deployments")
	if !ok {
		t.Fatal("no POST body captured for the review")
	}
	if !strings.Contains(body, `"environment_ids":[3734916060]`) {
		t.Errorf("review body is missing the environment ids (R12): %q", body)
	}
	if !strings.Contains(body, `"state":"approved"`) {
		t.Errorf("review body is missing state approved (R12): %q", body)
	}
	if !strings.Contains(body, `"comment":"ship it"`) {
		t.Errorf("review body is missing the comment (R12): %q", body)
	}
	if h.counting.deletes() != 0 || h.logExists() {
		t.Errorf("a review issued a DELETE or wrote a deletion log; it does neither (DELETE safety)")
	}
}

// TestReviewDeploymentRejects pins R12's reject branch: the same endpoint carries state
// rejected. Status and Conclusion never enter here: the review targets environment ids and
// carries a decision, which is what distinguishes /pending_deployments from /approve.
func TestReviewDeploymentRejects(t *testing.T) {
	h := newHarness(t, "review_rejected", 50, 50)

	err := runWrite(t, h, func(ctx context.Context) error {
		return h.ops.ReviewDeployment(ctx, ops.ReviewRequest{
			Repo:           repoID("o", "r"),
			RunID:          457,
			EnvironmentIDs: []int64{3734916060},
			State:          ops.ReviewRejected,
			Comment:        "not now",
		})
	})
	if err != nil {
		t.Fatalf("ReviewDeployment returned an error on a 2xx: %v (R12)", err)
	}
	body, ok := h.counting.postBody("/pending_deployments")
	if !ok {
		t.Fatal("no POST body captured for the review")
	}
	if !strings.Contains(body, `"state":"rejected"`) {
		t.Errorf("reject body is missing state rejected (R12): %q", body)
	}
}

// TestReviewDeploymentForbiddenIsNotAReviewer pins R14 and AC10: a 403 from a review is the
// not-a-designated-reviewer outcome, never an error or a retry. It surfaces as an
// ApprovalError NotAReviewer reads, and exactly one POST left the wire.
func TestReviewDeploymentForbiddenIsNotAReviewer(t *testing.T) {
	h := newHarness(t, "review_forbidden", 50, 50)

	err := runWrite(t, h, func(ctx context.Context) error {
		return h.ops.ReviewDeployment(ctx, ops.ReviewRequest{
			Repo:           repoID("o", "r"),
			RunID:          458,
			EnvironmentIDs: []int64{3734916060},
			State:          ops.ReviewApproved,
			Comment:        "please",
		})
	})
	if !ops.NotAReviewer(err) {
		t.Fatalf("NotAReviewer(err) = false, want true for a 403 review (R14, AC10): %v", err)
	}
	if n := h.counting.countMethod("POST"); n != 1 {
		t.Errorf("a 403 review issued %d POSTs, want exactly 1 (AC10 forbids a retry)", n)
	}
}

// TestReviewDeploymentRefusesEmptyComment pins R13 and AC9: a review with an empty comment
// is refused and issues no request. It runs over the offline transport, which Fatals on any
// wire request, so a leak fails loudly rather than passing against a cassette.
func TestReviewDeploymentRefusesEmptyComment(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)

	for _, comment := range []string{"", "   ", "\t\n"} {
		err := h.ops.ReviewDeployment(context.Background(), ops.ReviewRequest{
			Repo:           repoID("o", "r"),
			RunID:          459,
			EnvironmentIDs: []int64{3734916060},
			State:          ops.ReviewApproved,
			Comment:        comment,
		})
		if !errors.Is(err, ops.ErrEmptyComment) {
			t.Fatalf("ReviewDeployment(comment=%q) = %v, want ErrEmptyComment (R13, AC9)", comment, err)
		}
	}
	if h.counting.countMethod("POST") != 0 {
		t.Errorf("an empty-comment review issued a request; R13 forbids it (AC9)")
	}
}
