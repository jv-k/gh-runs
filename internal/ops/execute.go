package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// maxRateLimitRetries bounds R19's re-attempt at three (R19a). Three halvings carry
// the throttle from its cap to its floor, so a fourth backoff buys nothing, and
// without the bound a Run answering an authorization 403 that classifies as a rate
// limit would be retried until the process is killed (R13, rate-governor question 1).
const maxRateLimitRetries = 3

// FailureGroup is one reason among R22's grouped failures, with a count (AC18).
type FailureGroup struct {
	Reason string
	Count  int
}

// Summary is what one Purge pass did, and only this pass (R25): it claims no
// cumulative progress across sessions, because none is recorded. It is the shape the
// end-of-Purge summary and the CLI's exit code both read (R22, cli-surface R17). The
// live progress line and its remaining-time range (R15, AC23) are the running-Purge
// surface's, which broadcasts as it goes; this is the terminal account.
type Summary struct {
	Total   int // the frozen set size
	Deleted int // 204s, R29 'deleted'
	Gone    int // 404s counted as success (R18), R29 'gone'
	Acted   int // a lifecycle mutation the API accepted: cancel's 202, a re-run's 201 (run-lifecycle R4, R8). Never a deletion, so it writes no R29 log line (R24)
	Skipped int // eligibility, in-progress and the R19a reclassification (R11, R12, R19a)

	Failures     []FailureGroup // R22, grouped by reason
	CircuitBroke bool           // R21: consecutive failures reached the breaker
	Cancelled    bool           // R16: the user interrupted the Purge
	LogFailed    bool           // R29: the deletion log could not be written
	Reason       string         // why the Purge stopped early, else empty

	failed []Item // the failed Items, for R22's retry-only-the-failures keystroke
}

// FailedCount is the number of Runs recorded as failures (R20). A partially failed
// Purge exits 1 on this being non-zero (cli-surface R17, AC20).
func (s Summary) FailedCount() int { return len(s.failed) }

// Failed is the failed Items in attempt order, the subset R22's retry re-attempts
// without fresh confirmation, because it can only shrink from an already-confirmed
// set (ADR-0019).
func (s Summary) Failed() []Item {
	out := make([]Item, len(s.failed))
	copy(out, s.failed)
	return out
}

// StoppedEarly reports whether the Purge halted before the whole set: a circuit
// break, a log failure or a cancellation. The CLI states how to resume (R24).
func (s Summary) StoppedEarly() bool { return s.CircuitBroke || s.LogFailed || s.Cancelled }

// addFailure records a failure under its reason, keeping first-seen group order.
func (s *Summary) addFailure(item Item, reason string) {
	s.failed = append(s.failed, item)
	for i := range s.Failures {
		if s.Failures[i].Reason == reason {
			s.Failures[i].Count++
			return
		}
	}
	s.Failures = append(s.Failures, FailureGroup{Reason: reason, Count: 1})
}

// verdict is Execute's classification of one DELETE response.
type verdict int

const (
	verdictDeleted     verdict = iota // a 204 or other 2xx: the Run is gone because we deleted it
	verdictGone                       // a 404: the Run is gone already, which is success (R18)
	verdictRateLimited                // the governor classified a rate limit (R19)
	verdictInProgress                 // a 409: the API rejected an in-progress Run (R12, AC16a)
	verdictFailed                     // an authorization 403, a 5xx, anything else (R20)
)

// disposition is one Item's terminal outcome after the retry loop.
type disposition int

const (
	dispDeleted disposition = iota
	dispGone
	dispActed // a lifecycle mutation the API accepted (run-lifecycle R4, R8)
	dispSkipped
	dispFailed
	dispCancelled
	dispRetry // a rate limit: re-attempt under R19, never a terminal disposition executeSet sees
)

// itemResult is one Item's terminal disposition, plus the failure reason when it
// failed so Execute can group it (R22).
type itemResult struct {
	disp   disposition
	reason string
}

// Execute is the only call that issues a DELETE and the only call that writes the
// deletion log, and they are the same call (ADR-0011, ADR-0019). It flips the
// Confirmed's spent cell, proves the log writable before the first DELETE, then
// walks the frozen set in order: an Item stamped ineligible is recorded and skipped
// with no DELETE (AC15, AC16), and an eligible Item is deleted under the failure
// contract (R18 to R21). It returns the pass's Summary. A stop condition (a circuit
// break, a cancellation, or a log failure) returns a Summary rather than a Go error,
// because each is a reported outcome the surface renders, not a programming fault;
// the error return is for a caller misuse (a spent Confirmed, an unsupported op).
func (o *Ops) Execute(ctx context.Context, c Confirmed) (Summary, error) {
	if c.spent == nil || !c.spent.CompareAndSwap(false, true) {
		return Summary{}, ErrSpent // single use; a nil cell is the zero Confirmed (ADR-0019)
	}
	plan := c.plan
	switch plan.op {
	case OpDelete:
		return o.executeDelete(ctx, plan)
	case OpCancel, OpForceCancel, OpRerun, OpRerunFailed, OpEnable, OpDisable:
		return o.executeLifecycle(ctx, plan)
	default:
		return Summary{}, fmt.Errorf("ops: Execute does not support operation %q", plan.op)
	}
}

// executeDelete runs a Purge: it proves the deletion log writable before the first
// DELETE and walks the frozen set writing one line per attempt (purge R29). Only a
// deletion is recorded, so the log is Execute's alone and this is the one path that
// opens it (ADR-0011, ADR-0019).
func (o *Ops) executeDelete(ctx context.Context, plan Plan) (Summary, error) {
	// R29: the log is opened and proved writable before the first DELETE. An
	// operation that cannot open it refuses to start, issues zero DELETEs, and names
	// the log as the reason (AC20). This is the precondition that makes "no record,
	// no deletion" a property of Execute rather than a promise four call sites make.
	log, err := o.openLog(o.logPath, o.clk)
	if err != nil {
		return Summary{Total: plan.Total(), LogFailed: true, Reason: err.Error()}, nil
	}
	defer func() { _ = log.close() }()
	return o.executeSet(ctx, plan, log, func(ctx context.Context, log logSink, item Item) (itemResult, error) {
		return o.deleteItem(ctx, log, item)
	})
}

// executeLifecycle runs a non-deletion mutation set: a cancel, force-cancel, re-run,
// re-run-failed, or a Workflow enable or disable. It opens no deletion log and requires
// none, because none of these is a deletion: each leaves an object standing on GitHub
// that carries its own record, so purge R29's log MUST NOT record them (run-lifecycle
// R24, workflow-management R5, purge R29 scope). It reuses the Purge's failure contract
// unchanged (R21): a rate limit backs off and is not a failure, a permission or
// unexpected error advances the breaker, and the same consecutive count circuit-breaks.
// Only the per-response classification differs, and mutateItem owns it.
func (o *Ops) executeLifecycle(ctx context.Context, plan Plan) (Summary, error) {
	return o.executeSet(ctx, plan, nil, func(ctx context.Context, _ logSink, item Item) (itemResult, error) {
		return o.mutateItem(ctx, plan.op, plan.debug, item)
	})
}

// executeSet is the frozen-set walk both a Purge and a bulk lifecycle operation share:
// the failure contract R21 reuses unchanged. An ineligible Item is recorded and skipped
// with no request (AC15, AC16); an eligible one is attempted; a success resets the
// breaker and a failure advances it, circuit-breaking at the threshold (R21). log is
// the Purge's deletion record and nil for a lifecycle operation, which writes none
// (R24); attempt issues the one request and classifies it under the operation's own
// contract. A stop condition returns a Summary rather than a Go error, because each is
// a reported outcome the surface renders, not a programming fault (ADR-0019).
func (o *Ops) executeSet(ctx context.Context, plan Plan, log logSink,
	attempt func(context.Context, logSink, Item) (itemResult, error)) (Summary, error) {
	sum := Summary{Total: plan.Total()}
	failureStreak := 0
	for i := range plan.items {
		item := plan.items[i]
		if ctx.Err() != nil {
			sum.Cancelled = true // R16: no further request is issued
			return sum, nil
		}
		// An Item stamped ineligible at Plan time is recorded as skipped, with no
		// request (R10, R11, R12, R19, AC15, AC16). A Purge writes the skip line from
		// the Item's fields verbatim (ADR-0019); a lifecycle operation writes no log
		// (R24). A log write that fails stops the Purge.
		if item.Skip != SkipNone {
			if log != nil {
				if lerr := log.write(skipLine(item, string(item.Skip))); lerr != nil {
					return stopOnLogFailure(sum, lerr)
				}
			}
			sum.Skipped++
			continue
		}
		res, lerr := attempt(ctx, log, item)
		if lerr != nil {
			return stopOnLogFailure(sum, lerr) // R29: a mid-op log failure stops the Purge
		}
		switch res.disp {
		case dispDeleted:
			sum.Deleted++
			failureStreak = 0 // a success resets the breaker (R21)
		case dispGone:
			sum.Gone++
			failureStreak = 0
		case dispActed:
			sum.Acted++ // a 202/201 the API accepted (run-lifecycle R4, R8)
			failureStreak = 0
		case dispSkipped:
			sum.Skipped++ // transparent to the breaker: a skip is neither success nor failure
		case dispFailed:
			sum.addFailure(item, res.reason)
			failureStreak++
			if failureStreak >= o.breakerFailures {
				sum.CircuitBroke = true
				sum.Reason = fmt.Sprintf("circuit breaker tripped after %d consecutive failures", failureStreak)
				return sum, nil // R21: stop, no further request
			}
		case dispCancelled:
			sum.Cancelled = true
			return sum, nil // R16
		}
	}
	return sum, nil
}

// stopOnLogFailure records that the deletion log write failed and stops the Purge
// exactly as R21's breaker does: no further DELETE, objects already deleted stay
// deleted, and the summary names the log as the reason (R29, AC20).
func stopOnLogFailure(sum Summary, err error) (Summary, error) {
	sum.LogFailed = true
	sum.Reason = err.Error()
	return sum, nil
}

// deleteItem issues the DELETE for one Item under R19's bounded retry, classifies the
// response, and writes the one log line for the attempt's terminal outcome (R29). It
// returns the disposition and any log-write error, which is fatal to the Purge. A
// rate limit is re-attempted (R19), bounded at three then reclassified to a skip
// (R19a); a 404 is success (R18); a 409 is an in-progress skip (R12, AC16a); an
// authorization 403 or an unexpected status is a failure (R20). The governor has
// already paced this write and, on a rate limit, backed off, so the next attempt
// waits on the injected clock without ops arranging it (ADR-0007, R27).
func (o *Ops) deleteItem(ctx context.Context, log logSink, item Item) (itemResult, error) {
	rateLimitStreak := 0
	for {
		if ctx.Err() != nil {
			return itemResult{disp: dispCancelled}, nil
		}
		resp, err := o.client.RequestWithContext(ctx, http.MethodDelete, deletePath(item), nil)
		if err != nil {
			if ctx.Err() != nil {
				return itemResult{disp: dispCancelled}, nil // a cancelled request is not a failure
			}
			reason := "delete request failed: " + err.Error()
			return itemResult{disp: dispFailed, reason: reason}, log.write(failLine(item, reason))
		}
		v := classifyDelete(resp)
		reason := ""
		if v == verdictFailed {
			reason = failureReason(resp)
		}
		_ = resp.Body.Close()

		switch v {
		case verdictRateLimited:
			// R19: never a failure, re-attempt. R13 forbids counting it as one, so the
			// breaker never advances here. The governor's RoundTrip already halved the
			// rate and set the resume hold; the next iteration's write waits it out.
			rateLimitStreak++
			if rateLimitStreak >= maxRateLimitRetries {
				// R19a: after three consecutive rate-limit classifications on the same
				// Run, reclassify as an authorization failure and skip under R20. It is a
				// skip, not a failure, so it does not advance the breaker (AC14a).
				reason := "rate limit persisted after retries; skipped as an authorization failure"
				return itemResult{disp: dispSkipped}, log.write(skipLine(item, reason))
			}
			continue
		case verdictGone:
			return itemResult{disp: dispGone}, log.write(logLine(item, outcomeGone, "")) // R18
		case verdictDeleted:
			return itemResult{disp: dispDeleted}, log.write(logLine(item, outcomeDeleted, ""))
		case verdictInProgress:
			// R12, AC16a: a Run recorded completed at crawl but now in progress. The
			// API's write-time rejection is the guard, synchronous with the write, so it
			// is recorded as a skip and no cancel is issued for it.
			return itemResult{disp: dispSkipped}, log.write(skipLine(item, string(SkipNotCompleted)))
		default: // verdictFailed
			return itemResult{disp: dispFailed, reason: reason}, log.write(failLine(item, reason))
		}
	}
}

// classifyDelete reads the governor's verdict and the status code. The rate-limit
// verdict is read from the governor's stamped header first, because a secondary-limit
// 403 can arrive with a healthy x-ratelimit-remaining and only the governor's
// body-shape classification tells it from an authorization 403 (R19, rate-governor
// question 1). A 404 is gone (R18), a 2xx is deleted, a 409 is an in-progress
// rejection (R12), and everything else is a failure (R20).
func classifyDelete(resp *http.Response) verdict {
	if governor.RateLimited(resp) {
		return verdictRateLimited
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return verdictGone
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return verdictDeleted
	case resp.StatusCode == http.StatusConflict:
		return verdictInProgress
	default:
		return verdictFailed
	}
}

// failureReason derives R20's recorded reason from the response: the API's message
// where the body carries one, else the bare status. Distinct messages group into
// distinct failure groups (AC18). The governor already read and restored the body
// during classification, so this reads the full body the consumer is owed.
func failureReason(resp *http.Response) string {
	if resp.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &payload) == nil && payload.Message != "" {
			return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, payload.Message)
		}
	}
	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}

// notCancelableReason is the recorded reason for a 409 from cancel. It names force-cancel
// as the escalation so the surface offering it reads the reason, never the raw status
// (run-lifecycle R5, R6, AC6). In a bulk cancel a 409 is a raced completion and a skip
// (R20, AC12); the single-Run surface reads the same reason to offer force-cancel (R5).
const notCancelableReason = "run is not cancelable; escalate with force-cancel"

// mutateItem issues one lifecycle request (cancel, force-cancel, re-run or re-run-failed)
// under R19's bounded rate-limit retry, classifies it under the operation's contract, and
// returns the disposition. It writes no deletion log: none of the four is a deletion, so
// purge R29's log MUST NOT record them (run-lifecycle R24), and its (itemResult, error)
// shape carries a nil error to match deleteItem so executeSet drives both. A rate limit is
// re-attempted (R19), bounded at three then reclassified to a skip (R19a); the rest is
// classifyLifecycle's, which is the one place the four operations' 404 and 409 readings
// differ (R22). The governor paces this write and, on a rate limit, has already backed
// off, so the next attempt waits on the injected clock without ops arranging it (R23, R26).
func (o *Ops) mutateItem(ctx context.Context, op Operation, debug bool, item Item) (itemResult, error) {
	rateLimitStreak := 0
	for {
		if ctx.Err() != nil {
			return itemResult{disp: dispCancelled}, nil
		}
		method, path, body := lifecycleRequest(op, item, debug)
		resp, err := o.client.RequestWithContext(ctx, method, path, body)
		if err != nil {
			if ctx.Err() != nil {
				return itemResult{disp: dispCancelled}, nil // a cancelled request is not a failure
			}
			return itemResult{disp: dispFailed, reason: string(op) + " request failed: " + err.Error()}, nil
		}
		disp, reason := classifyLifecycle(op, resp)
		_ = resp.Body.Close()
		if disp == dispRetry {
			// R19: a rate limit is never a failure, so the breaker never advances here. The
			// governor's RoundTrip already halved the rate and set the resume hold; the next
			// iteration's write waits it out. R19a bounds the re-attempt at three, then the
			// Run is reclassified as an authorization skip and the operation proceeds (AC14a).
			rateLimitStreak++
			if rateLimitStreak >= maxRateLimitRetries {
				return itemResult{disp: dispSkipped, reason: "rate limit persisted after retries; skipped as an authorization failure"}, nil
			}
			continue
		}
		return itemResult{disp: disp, reason: reason}, nil
	}
}

// classifyLifecycle maps one response to a disposition under the operation's own failure
// contract, the one place the four operations diverge (R22). A rate limit is dispRetry for
// mutateItem's bounded loop (R19). For cancel and force-cancel: a 2xx is the accepted
// request, not the cancelled outcome (R4, AC5), so it is dispActed and no Conclusion is
// inferred; a 404 means the Run no longer exists and so is not running, which is the
// requested end state, recorded as a skip (R22, AC13); a 409 means the Run is not
// cancelable, a raced completion recorded as a skip that does not advance the breaker (R5,
// R20, AC12); anything else is a failure (R20, R3's expected 403 despite push). For re-run
// and re-run-failed: a 2xx added the Attempt (R8); a 404 means the Run cannot gain one and
// is a failure, the opposite reading of the same status (R22, AC13); the API's own reason
// is surfaced rather than pre-judged (R15, AC15).
func classifyLifecycle(op Operation, resp *http.Response) (disposition, string) {
	if governor.RateLimited(resp) {
		return dispRetry, ""
	}
	code := resp.StatusCode
	switch op {
	case OpEnable, OpDisable:
		// A 204 is the toggle accepted (workflow-management R5). R8 re-reads the list to
		// reflect the API's reported state rather than inferring it here, so a 2xx is
		// dispActed and nothing about the new State is assumed. Anything else is a failure,
		// and R7 makes a 403 despite push a permission failure surfaced with the API's own
		// reason, never treated as a bug. A rate limit was already returned as dispRetry above.
		if code >= 200 && code < 300 {
			return dispActed, ""
		}
		return dispFailed, failureReason(resp) // R7: report the API's rejection verbatim
	case OpCancel, OpForceCancel:
		switch {
		case code >= 200 && code < 300:
			return dispActed, "" // 202 accepted: a request, not an outcome (R4, AC5)
		case code == http.StatusNotFound:
			return dispSkipped, "run no longer exists, so it is not running" // R22, AC13
		case code == http.StatusConflict:
			return dispSkipped, notCancelableReason // R5, R20, AC12
		default:
			return dispFailed, failureReason(resp) // R20, R3
		}
	default: // OpRerun, OpRerunFailed
		if code >= 200 && code < 300 {
			return dispActed, "" // 201 created: an Attempt added (R8)
		}
		return dispFailed, failureReason(resp) // R22/AC13: a 404 is a failure here; R15: surface the API's reason
	}
}

// lifecycleRequest builds the endpoint, method and body for one non-deletion mutation.
// The four Run operations POST the Run's own endpoint (run-lifecycle R6's distinct
// force-cancel endpoint, R13's distinct re-run-failed endpoint), and only the two re-runs
// carry a body, and only when debug logging is on. Enable and disable PUT the Workflow's
// endpoint instead, addressing the Workflow by id, because they act on a Workflow rather
// than a Run (workflow-management R5). The item's ID is the Workflow id for a toggle and
// the Run id for the rest, stamped by the constructor, so the path is right for each.
func lifecycleRequest(op Operation, item Item, debug bool) (method, path string, body io.Reader) {
	switch op {
	case OpEnable:
		return http.MethodPut, workflowPath(item) + "/enable", nil
	case OpDisable:
		return http.MethodPut, workflowPath(item) + "/disable", nil
	}
	base := fmt.Sprintf("repos/%s/%s/actions/runs/%d", item.Repo.Owner, item.Repo.Name, item.ID)
	switch op {
	case OpCancel:
		return http.MethodPost, base + "/cancel", nil
	case OpForceCancel:
		return http.MethodPost, base + "/force-cancel", nil
	case OpRerun:
		return http.MethodPost, base + "/rerun", rerunBody(debug)
	case OpRerunFailed:
		return http.MethodPost, base + "/rerun-failed-jobs", rerunBody(debug)
	default:
		return http.MethodPost, base, nil // unreachable: Execute validated the operation
	}
}

// workflowPath is the Workflow's Actions endpoint, addressed by id, that the enable and
// disable PUTs extend with their verb (workflow-management R5). The id is the Workflow's,
// carried on the Item the WorkflowItem constructor froze.
func workflowPath(item Item) string {
	return fmt.Sprintf("repos/%s/%s/actions/workflows/%d", item.Repo.Owner, item.Repo.Name, item.ID)
}

// rerunBody is R14's enable_debug_logging opt-in. On the default path it sends no body,
// so the request carries no enable_debug_logging at all, which is exactly what AC14
// asserts of the default. With the opt-in it sends the one flag both re-run endpoints
// accept, matching gh run rerun --debug (R14, AC14).
func rerunBody(debug bool) io.Reader {
	if !debug {
		return nil
	}
	return strings.NewReader(`{"enable_debug_logging":true}`)
}

// deletePath builds the DELETE endpoint per Item Kind (ADR-0019: Delete resolves its
// endpoint per Kind). Only KindRun is exercised at stage 9; the Cache, Artifact and
// log endpoints are the shapes later stages reuse Execute for.
func deletePath(item Item) string {
	base := fmt.Sprintf("repos/%s/%s/actions", item.Repo.Owner, item.Repo.Name)
	switch item.Kind {
	case KindLog:
		return fmt.Sprintf("%s/runs/%d/logs", base, item.ID)
	case KindCache:
		return fmt.Sprintf("%s/caches/%d", base, item.ID)
	case KindArtifact:
		return fmt.Sprintf("%s/artifacts/%d", base, item.ID)
	default: // KindRun
		return fmt.Sprintf("%s/runs/%d", base, item.ID)
	}
}

// logLine, skipLine and failLine build R29's record from an Item's fields verbatim,
// so the kind column and the id are the same ones R4's tuple carries (ADR-0019).
func logLine(item Item, outcome logOutcome, reason string) logRecord {
	return logRecord{repo: item.Repo, kind: item.Kind, id: item.ID, outcome: outcome, reason: reason}
}

func skipLine(item Item, reason string) logRecord { return logLine(item, outcomeSkipped, reason) }
func failLine(item Item, reason string) logRecord { return logLine(item, outcomeFailed, reason) }
