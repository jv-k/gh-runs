package ops_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// items builds eligible RunItems for the given ids in one writable repository.
func items(owner, name string, ids ...int64) []ops.Item {
	out := make([]ops.Item, len(ids))
	for i, id := range ids {
		out[i] = ops.RunItem(completedRun(id, owner, name))
	}
	return out
}

// TestExecuteMixedOutcomes pins the failure contract's four terminal dispositions
// and their log lines in one pass (R18, R20, AC12, AC16a, AC19): a 204 is deleted, a
// 404 is gone-and-counted-success, a 409 is an in-progress skip, and an authorization
// 403 is a failure. Every attempt is one log line, in order, host-qualified, and the
// first line's timestamp is the injected clock's exact start, not the wall clock.
func TestExecuteMixedOutcomes(t *testing.T) {
	h := newHarness(t, "delete_mixed", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Deleted != 1 || sum.Gone != 1 || sum.Skipped != 1 || sum.FailedCount() != 1 {
		t.Errorf("summary = deleted %d, gone %d, skipped %d, failed %d; want 1/1/1/1 (R18, R20, AC12, AC16a)",
			sum.Deleted, sum.Gone, sum.Skipped, sum.FailedCount())
	}
	if h.counting.deletes() != 4 {
		t.Errorf("issued %d DELETEs, want 4 (each Run attempted once)", h.counting.deletes())
	}

	log := h.readLog(t)
	if len(log) != 4 {
		t.Fatalf("log has %d lines, want one per attempt (R29, AC19): %+v", len(log), log)
	}
	// The first line is written before the driver advances the clock, so its timestamp
	// is the injected clock's exact start. The wall clock can never be this value, which
	// is what proves the timestamp is R27's clock and not time.Now (AC19).
	if log[0].timestamp != "2026-07-22T12:00:00Z" {
		t.Errorf("first log timestamp = %q, want the injected clock's start 2026-07-22T12:00:00Z (AC19)", log[0].timestamp)
	}
	if log[0].repo != "github.com/o/r" {
		t.Errorf("log repo = %q, want host-qualified github.com/o/r (R29, AC19)", log[0].repo)
	}
	wantOutcome := []string{"deleted", "gone", "skipped", "failed"}
	for i, w := range wantOutcome {
		if log[i].outcome != w {
			t.Errorf("log line %d outcome = %q, want %q", i, log[i].outcome, w)
		}
		if log[i].kind != "run" {
			t.Errorf("log line %d kind = %q, want run", i, log[i].kind)
		}
	}
	if log[0].reason != "" {
		t.Errorf("a deletion's reason must be empty (R29), got %q", log[0].reason)
	}
	if !strings.Contains(log[3].reason, "403") {
		t.Errorf("a failure's reason should carry the status, got %q (R20)", log[3].reason)
	}
}

// TestExecuteSkipsIneligibleWithoutAttempting pins AC15: an Item stamped ineligible at
// Plan time is recorded as skipped and never has a DELETE issued for it. Two eligible
// Runs delete; the read-only Run is skipped with no wire request.
func TestExecuteSkipsIneligibleWithoutAttempting(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	sel := items("o", "r", 1, 2)                                    // eligible
	sel = append(sel, ops.RunItem(completedRun(3, "o", "readonly"))) // ineligible: read-only repo
	repos := snapshot(
		writableRepo("o", "r"),
		domain.Repo{ID: repoID("o", "readonly"), Permissions: domain.Permissions{Push: false}},
	)
	c := h.confirmed(t, ops.OpDelete, sel, repos)
	sum := runPurge(t, h, c)

	if sum.Deleted != 2 || sum.Skipped != 1 || sum.FailedCount() != 0 {
		t.Errorf("summary = deleted %d, skipped %d, failed %d; want 2/1/0 (AC15)", sum.Deleted, sum.Skipped, sum.FailedCount())
	}
	if h.counting.deletes() != 2 {
		t.Errorf("issued %d DELETEs, want 2; the ineligible Run must not be attempted (AC15)", h.counting.deletes())
	}
	// The skip is still recorded, one line per attempt (ADR-0019: skip lines included).
	if got := h.outcomes(t); got["deleted"] != 2 || got["skipped"] != 1 {
		t.Errorf("log outcomes = %v, want 2 deleted and 1 skipped (R29)", got)
	}
	for _, f := range h.readLog(t) {
		if f.id == "3" && !strings.Contains(f.reason, "read-only") {
			t.Errorf("read-only skip reason = %q, want it to name read-only (R11)", f.reason)
		}
	}
}

// TestRateLimitBoundThenSkip pins R19, R19a and AC14a: a Run whose DELETE keeps
// answering a rate-limit-classified 403 is re-attempted at most three times, then
// recorded as a skip, and the Purge proceeds to the next Run. The rate limit is never
// counted as a failure (R13), so the summary carries no failure and the Purge does
// not circuit-break. The three attempts are bounded by the tape itself.
func TestRateLimitBoundThenSkip(t *testing.T) {
	h := newHarness(t, "delete_ratelimit", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.FailedCount() != 0 {
		t.Errorf("a rate limit was counted as a failure; R13 forbids it. Failures: %+v", sum.Failures)
	}
	if sum.CircuitBroke {
		t.Errorf("a rate limit tripped the breaker; a backoff must not advance it (R21, AC14)")
	}
	if sum.Skipped != 1 || sum.Deleted != 1 {
		t.Errorf("summary = skipped %d, deleted %d; want 1/1 (Run 1 bounded-and-skipped, Run 2 deleted) (R19a, AC14a)", sum.Skipped, sum.Deleted)
	}
	// Run 1's DELETE was issued exactly three times before R19a reclassified it, and
	// Run 2 once: four DELETEs in total, the bound proven by the count.
	if h.counting.deletes() != 4 {
		t.Errorf("issued %d DELETEs, want 4 (Run 1 x3 bounded, Run 2 x1) (R19a, AC14a)", h.counting.deletes())
	}
}

// TestCircuitBreakerStops pins R21 and AC13: with the breaker threshold at 3, three
// consecutive failures stop the Purge, no fourth DELETE is issued, and the summary
// names the circuit-break. The failures group by their distinct reasons (R22, AC18).
func TestCircuitBreakerStops(t *testing.T) {
	h := newHarness(t, "delete_breaker", 50, 3) // breaker threshold 3
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if !sum.CircuitBroke {
		t.Errorf("three consecutive failures did not trip the breaker (R21, AC13)")
	}
	if h.counting.deletes() != 3 {
		t.Errorf("issued %d DELETEs, want 3; the fourth Run must not be attempted after the break (R21, AC13)", h.counting.deletes())
	}
	if sum.FailedCount() != 3 {
		t.Errorf("recorded %d failures, want 3", sum.FailedCount())
	}
	if len(sum.Failures) != 2 {
		t.Errorf("failure groups = %d, want 2 distinct reasons (R22, AC18): %+v", len(sum.Failures), sum.Failures)
	}
	if sum.Reason == "" || !strings.Contains(sum.Reason, "breaker") {
		t.Errorf("summary reason = %q, want it to name the circuit-break (R21)", sum.Reason)
	}
}

// TestCircuitBreakerResetsOnSuccess pins AC13's "one success resets": two failures, a
// deletion, then two more failures, at a threshold of 3, never reaches a consecutive
// three, so the Purge does not break and every Run is attempted.
func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	h := newHarness(t, "delete_breaker_reset", 50, 3)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4, 5), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.CircuitBroke {
		t.Errorf("the breaker fired though a success reset the streak below the threshold (R21, AC13)")
	}
	if h.counting.deletes() != 5 {
		t.Errorf("issued %d DELETEs, want all 5 attempted (AC13)", h.counting.deletes())
	}
	if sum.Deleted != 1 || sum.FailedCount() != 4 {
		t.Errorf("summary = deleted %d, failed %d; want 1/4", sum.Deleted, sum.FailedCount())
	}
}

// TestBreakerThresholdOfOneStopsAtFirstFailure pins AC13's floor: a threshold of 1
// stops the Purge on the first failure, issuing no second DELETE.
func TestBreakerThresholdOfOneStopsAtFirstFailure(t *testing.T) {
	h := newHarness(t, "delete_breaker", 50, 1) // threshold 1
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if !sum.CircuitBroke || h.counting.deletes() != 1 {
		t.Errorf("threshold 1: broke=%v deletes=%d, want broke and exactly 1 DELETE (AC13)", sum.CircuitBroke, h.counting.deletes())
	}
}
