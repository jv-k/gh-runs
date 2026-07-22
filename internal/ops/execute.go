package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
	verdictDeleted    verdict = iota // a 204 or other 2xx: the Run is gone because we deleted it
	verdictGone                      // a 404: the Run is gone already, which is success (R18)
	verdictRateLimited               // the governor classified a rate limit (R19)
	verdictInProgress                // a 409: the API rejected an in-progress Run (R12, AC16a)
	verdictFailed                    // an authorization 403, a 5xx, anything else (R20)
)

// disposition is one Item's terminal outcome after the retry loop.
type disposition int

const (
	dispDeleted disposition = iota
	dispGone
	dispSkipped
	dispFailed
	dispCancelled
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
	if plan.op != OpDelete {
		return Summary{}, fmt.Errorf("ops: Execute supports %s at this stage; %s is run-lifecycle's (stage 10)", OpDelete, plan.op)
	}
	sum := Summary{Total: plan.Total()}

	// R29: the log is opened and proved writable before the first DELETE. An
	// operation that cannot open it refuses to start, issues zero DELETEs, and names
	// the log as the reason (AC20). This is the precondition that makes "no record,
	// no deletion" a property of Execute rather than a promise four call sites make.
	log, err := o.openLog(o.logPath, o.clk)
	if err != nil {
		sum.LogFailed = true
		sum.Reason = err.Error()
		return sum, nil
	}
	defer func() { _ = log.close() }()

	failureStreak := 0
	for i := range plan.items {
		item := plan.items[i]
		if ctx.Err() != nil {
			sum.Cancelled = true // R16: no further DELETE is issued
			return sum, nil
		}
		// An Item stamped ineligible at Plan time is recorded as skipped, with no
		// DELETE (R10, R11, R12, AC15, AC16). The skip line is written from the Item's
		// fields verbatim (ADR-0019), and a log write that fails stops the Purge.
		if item.Skip != SkipNone {
			if lerr := log.write(skipLine(item, string(item.Skip))); lerr != nil {
				return stopOnLogFailure(sum, lerr)
			}
			sum.Skipped++
			continue
		}
		res, lerr := o.deleteItem(ctx, log, item)
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
		case dispSkipped:
			sum.Skipped++ // transparent to the breaker: a skip is neither success nor failure
		case dispFailed:
			sum.addFailure(item, res.reason)
			failureStreak++
			if failureStreak >= o.breakerFailures {
				sum.CircuitBroke = true
				sum.Reason = fmt.Sprintf("circuit breaker tripped after %d consecutive failures", failureStreak)
				return sum, nil // R21: stop, no further DELETE
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
