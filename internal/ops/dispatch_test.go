package ops_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// runDispatch runs Dispatch in a goroutine and drives the fake clock so the paced POST releases
// without a real sleep, exactly as runToggle does for a toggle and runPurge for a Purge (a
// Dispatch is a write, so the governor paces it, rate-governor R2). Unlike runToggle it returns
// the error rather than failing on it, because the disabled-Workflow path is a rejection the
// test asserts (R22).
func runDispatch(t *testing.T, h *harness, req ops.DispatchRequest) (ops.DispatchResult, error) {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	type result struct {
		r   ops.DispatchResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := h.ops.Dispatch(runCtx, req)
		cancel()
		done <- result{r, err}
	}()
	const quantum = 200 * time.Millisecond
	for {
		if err := h.clk.BlockUntilContext(runCtx, 1); err != nil {
			break
		}
		h.clk.Advance(quantum)
	}
	r := <-done
	return r.r, r.err
}

// TestDispatchReturnsRunIDFromResponse pins R16 against a cassette, with no live dispatch: a
// workflow_dispatch POST carrying return_run_details:true returns 200 with a workflow_run_id,
// and Dispatch reads the Run ID from the response body rather than correlating (measurement
// #27). It is a single POST to the dispatches endpoint, issues no DELETE, and writes no deletion
// log, because a Dispatch creates a Run rather than deleting one. Every write here is a taped
// POST replayed in ModeReplayOnly.
func TestDispatchReturnsRunIDFromResponse(t *testing.T) {
	h := newHarness(t, "dispatch_ok", 50, 50)

	res, err := runDispatch(t, h, ops.DispatchRequest{
		Repo:       repoID("o", "r"),
		WorkflowID: 9001,
		Ref:        "main",
		Inputs:     map[string]string{"tag_name": "v1.2.3"},
	})
	if err != nil {
		t.Fatalf("Dispatch returned an error: %v", err)
	}
	if res.RunID != 29803635501 {
		t.Errorf("RunID = %d, want 29803635501 read from workflow_run_id (R16)", res.RunID)
	}
	if res.HTMLURL == "" {
		t.Errorf("HTMLURL is empty; the 200 response carries html_url (R16)")
	}

	posts := h.counting.urls("POST")
	if len(posts) != 1 || !strings.HasSuffix(posts[0], "/actions/workflows/9001/dispatches") {
		t.Fatalf("want exactly one POST to the dispatches endpoint, got %v (R16)", posts)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("Dispatch issued a DELETE; a dispatch creates a Run, it deletes nothing (safety)")
	}

	// R16: return_run_details:true rides every Dispatch, and the ref and inputs are on the body.
	body, ok := h.counting.postBody("/dispatches")
	if !ok {
		t.Fatal("no POST body captured for the dispatch")
	}
	if !strings.Contains(body, `"return_run_details":true`) {
		t.Errorf("dispatch body is missing return_run_details:true (R16): %q", body)
	}
	if !strings.Contains(body, `"ref":"main"`) {
		t.Errorf("dispatch body is missing the ref (R16): %q", body)
	}
	if !strings.Contains(body, `"tag_name":"v1.2.3"`) {
		t.Errorf("dispatch body is missing the operator's inputs: %q", body)
	}

	// A Dispatch is not a deletion, so Execute's deletion log is never opened for it (purge R29
	// scope): only Execute writes that log, and Dispatch is not Execute.
	if h.logExists() {
		t.Errorf("Dispatch wrote a deletion log; it creates a Run rather than deleting one")
	}
}

// TestDispatchOmitsInputsWhenEmpty pins that an input-less Workflow dispatches with a body
// carrying no inputs object at all, matching the measured shape {"ref":...,"return_run_details":
// true} (measurement #27). The Run ID still comes back from the response.
func TestDispatchOmitsInputsWhenEmpty(t *testing.T) {
	h := newHarness(t, "dispatch_no_inputs", 50, 50)

	res, err := runDispatch(t, h, ops.DispatchRequest{
		Repo:       repoID("o", "r"),
		WorkflowID: 9002,
		Ref:        "main",
	})
	if err != nil {
		t.Fatalf("Dispatch returned an error: %v", err)
	}
	if res.RunID != 29803635999 {
		t.Errorf("RunID = %d, want 29803635999 (R16)", res.RunID)
	}
	body, ok := h.counting.postBody("/dispatches")
	if !ok {
		t.Fatal("no POST body captured for the dispatch")
	}
	if strings.Contains(body, `"inputs"`) {
		t.Errorf("an input-less dispatch must omit the inputs object (measurement #27): %q", body)
	}
}

// TestDispatchSurfacesDisabledRejection pins R22 and R14's "the API is the final authority"
// against a cassette: a Dispatch the API rejects (a 422 for a disabled Workflow, or a 403 for a
// token without Actions write despite a push gate) is returned as a DispatchError carrying the
// status code and the API's own message, never swallowed and never labelled a success. No live
// dispatch is issued, so no Run is created.
func TestDispatchSurfacesDisabledRejection(t *testing.T) {
	h := newHarness(t, "dispatch_disabled", 50, 50)

	_, err := runDispatch(t, h, ops.DispatchRequest{
		Repo:       repoID("o", "r"),
		WorkflowID: 9003,
		Ref:        "main",
	})
	if err == nil {
		t.Fatal("Dispatch accepted a disabled Workflow; the 422 must be surfaced (R22)")
	}
	var de *ops.DispatchError
	if !errors.As(err, &de) {
		t.Fatalf("error is %T, want *ops.DispatchError so the form can read the code (R14, R22): %v", err, err)
	}
	if de.Code != 422 {
		t.Errorf("DispatchError.Code = %d, want 422 (R22, measurement #33)", de.Code)
	}
	if !strings.Contains(strings.ToLower(de.Message), "disabled") {
		t.Errorf("DispatchError.Message = %q, want the API's own reason naming the disabled state (R22)", de.Message)
	}
}
