package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// DispatchRequest is one workflow_dispatch trigger: the Workflow to run, the ref to run it at,
// and the operator's input values (workflow-dispatch R2, R5, R16). Inputs are the string map the
// API accepts, which the typed form builds from its controls, so ops stays schema-agnostic: the
// YAML parsing, the type-per-control mapping and the choice-bounded selection are the pane's
// (R6, R10), and by the time a value reaches here it is a plain string the endpoint takes.
type DispatchRequest struct {
	Repo       domain.RepoID
	WorkflowID int64
	Ref        string
	Inputs     map[string]string
}

// DispatchResult is the Run a Dispatch created, read from the 200 response's workflow_run_id
// (workflow-dispatch R16). return_run_details:true makes the Run ID arrive in the Dispatch
// response with no correlation poll (measurement #27), so a Dispatch resolves to a known Run and
// the retired R17 to R19 correlation is never built.
type DispatchResult struct {
	RunID   int64
	RunURL  string
	HTMLURL string
}

// DispatchError is a Dispatch the API rejected, carrying the status code and the API's own
// message so the form can surface a 422 (a disabled Workflow, R22), a 404 (a deleted Workflow,
// R15) or a 403 (the API as the final authority despite the push gate, R14) rather than an
// opaque failure. It is a distinct type so a caller can read the code with errors.As.
type DispatchError struct {
	Code    int
	Message string
}

func (e *DispatchError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("dispatch rejected: HTTP %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("dispatch rejected: HTTP %d", e.Code)
}

// dispatchBody is the workflow_dispatch POST body. return_run_details is sent true on every
// Dispatch (R16), so the Run ID comes back on the 200. inputs is omitted when empty, matching the
// measured input-less shape {"ref":...,"return_run_details":true} (#27), and the API receives
// every input value as a string, which is what github.event.inputs.* is at the Workflow anyway.
type dispatchBody struct {
	Ref              string            `json:"ref"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	ReturnRunDetails bool              `json:"return_run_details"`
}

// Dispatch triggers a workflow_dispatch and returns the Run it created (workflow-dispatch R16).
//
// It is a write ops owns, because ADR-0011 gives ops every write in the product and a tab renders
// and calls but never issues a write of its own. It travels the same store-then-governor-then-
// limiter transport every other write does, so rate-governor R2 paces it at the transport layer
// by method (a POST is a write whether or not a list names it) exactly as it paces the ten Execute
// writes, and the content-creation dimension a dispatched Run counts against is the governor's to
// react to on the response (write-point-costs.md).
//
// It is deliberately NOT Execute, and that keeps Execute's invariant literally intact. Execute
// walks a frozen set and returns counts, and it is the only call that issues a DELETE and the only
// call that writes the deletion log. A Dispatch is a single POST that must return a Run ID (R16),
// which no Summary carries, and the workflow-dispatch canon prices no graduated confirmation over
// it: the form's refusal to submit until every required input has a value is the confirming act
// (R9), not a Plan/Confirm friction. ADR-0019 left dispatch's confirmation shape open ("that fog's
// to decide") and never committed it to an Operation, so Dispatch sits beside Execute as ops's
// second write entry rather than distorting the frozen-set machinery to carry a ref, an inputs map
// and a returned Run ID. The DELETE safety is untouched: this issues a POST and opens no log.
//
// The gate R14 names (permissions.push && !archived) costs no request and is the caller's, applied
// before Dispatch is ever reached (AC6), so the tool never dispatches to a repository it can see is
// archived or read-only. Here the API is the final authority: a 403 that arrives despite the push
// gate, a 422 for a disabled Workflow (R22) or a 404 for a deleted one (R15) is returned as a
// DispatchError, never a success.
func (o *Ops) Dispatch(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	raw, err := json.Marshal(dispatchBody{Ref: req.Ref, Inputs: req.Inputs, ReturnRunDetails: true})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("dispatch: encoding the request failed: %w", err)
	}
	resp, err := o.client.RequestWithContext(ctx, http.MethodPost, dispatchPath(req.Repo, req.WorkflowID), bytes.NewReader(raw))
	if err != nil {
		if ctx.Err() != nil {
			return DispatchResult{}, ctx.Err()
		}
		return DispatchResult{}, fmt.Errorf("dispatch: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DispatchResult{}, &DispatchError{Code: resp.StatusCode, Message: dispatchMessage(resp)}
	}
	var out struct {
		WorkflowRunID int64  `json:"workflow_run_id"`
		RunURL        string `json:"run_url"`
		HTMLURL       string `json:"html_url"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DispatchResult{}, fmt.Errorf("dispatch: reading the response failed: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return DispatchResult{}, fmt.Errorf("dispatch: decoding the response failed: %w", err)
	}
	return DispatchResult{RunID: out.WorkflowRunID, RunURL: out.RunURL, HTMLURL: out.HTMLURL}, nil
}

// dispatchPath is the create-a-workflow-dispatch-event endpoint, addressing the Workflow by id
// exactly as enable and disable do (R16). A deleted Workflow's id 404s here whatever ref it names
// (R15, measurement #34), which is why the barrier is the missing Workflow rather than missing
// YAML.
func dispatchPath(repo domain.RepoID, workflowID int64) string {
	return fmt.Sprintf("repos/%s/%s/actions/workflows/%d/dispatches", repo.Owner, repo.Name, workflowID)
}

// dispatchMessage reads the API's own reason from a rejection body so a DispatchError names it
// (R22's 422 says "disabled workflow" verbatim), falling back to the bare status where the body
// carries none. It reads the full body the response owns, which the caller closes.
func dispatchMessage(resp *http.Response) string {
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
