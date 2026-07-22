package ops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// TestCancelMixedOutcomes pins cancel's failure contract across its four branches in one
// bulk pass (run-lifecycle R4, R20, R22, AC5, AC12, AC13). A 202 is 'acted', not a
// cancelled Conclusion, because cancel is asynchronous and the 202 is the request being
// accepted (R4, AC5). A 409 (not cancelable) and a 404 (gone, so not running) are both
// skips that do not advance the breaker, and the operation proceeds past them to a second
// clean cancel (R20, AC12). None of the four writes a deletion-log line (R24).
func TestCancelMixedOutcomes(t *testing.T) {
	h := newHarness(t, "cancel_mixed", 50, 50)
	c := h.confirmed(t, ops.OpCancel, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Acted != 2 || sum.Skipped != 2 || sum.FailedCount() != 0 {
		t.Errorf("summary = acted %d, skipped %d, failed %d; want 2/2/0 (R4, R20, R22, AC5, AC12, AC13)",
			sum.Acted, sum.Skipped, sum.FailedCount())
	}
	if sum.Deleted != 0 || sum.Gone != 0 {
		t.Errorf("a cancel is not a deletion: deleted %d, gone %d, want 0/0", sum.Deleted, sum.Gone)
	}
	if sum.CircuitBroke {
		t.Errorf("a 409 or 404 skip tripped the breaker; neither is a failure (R20, AC12)")
	}
	if got := h.counting.countMethod("POST"); got != 4 {
		t.Errorf("issued %d cancel POSTs, want 4 (each Run attempted once)", got)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("a cancel issued a DELETE; it must not (run-lifecycle R1: cancel changes Status, it does not delete)")
	}
	// R24: none of the four lifecycle operations is a deletion, so purge R29's log MUST
	// NOT record them, and Execute opens no log for them at all.
	if h.logExists() {
		t.Errorf("a bulk cancel wrote a deletion log; run-lifecycle R24 forbids it (purge R29 scope)")
	}
}

// TestForceCancelIsADistinctEndpoint pins R6: force-cancel is a distinct operation against
// a distinct endpoint, never substituted for cancel. Its 202 is 'acted', like cancel's.
func TestForceCancelIsADistinctEndpoint(t *testing.T) {
	h := newHarness(t, "forcecancel", 50, 50)
	c := h.confirmed(t, ops.OpForceCancel, items("o", "r", 1), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Acted != 1 || sum.FailedCount() != 0 {
		t.Errorf("summary = acted %d, failed %d; want 1/0 (R6)", sum.Acted, sum.FailedCount())
	}
	if _, ok := h.counting.postBody("/actions/runs/1/force-cancel"); !ok {
		t.Errorf("force-cancel did not POST its own /force-cancel endpoint (R6)")
	}
	if _, ok := h.counting.postBody("/actions/runs/1/cancel"); ok {
		t.Errorf("force-cancel POSTed the plain /cancel endpoint; it is a distinct endpoint (R6)")
	}
	if h.logExists() {
		t.Errorf("force-cancel wrote a deletion log; R24 forbids it")
	}
}

// TestRerunActsAndDebugIsOptIn pins R8 and R14/AC14: a re-run is accepted with a 201 (it
// adds an Attempt, it does not create a Run), it writes no deletion log (R24), and the
// enable_debug_logging flag rides the request body only when the opt-in is set. The default
// path sends no body at all, so it carries no flag.
func TestRerunActsAndDebugIsOptIn(t *testing.T) {
	h := newHarness(t, "rerun_ok", 50, 50)

	// Run 1: the default path. Acted, and the request carries no enable_debug_logging (AC14).
	def := h.confirmed(t, ops.OpRerun, items("o", "r", 1), snapshot(writableRepo("o", "r")))
	sumDef := runPurge(t, h, def)
	if sumDef.Acted != 1 || sumDef.FailedCount() != 0 {
		t.Errorf("default re-run = acted %d, failed %d; want 1/0 (R8)", sumDef.Acted, sumDef.FailedCount())
	}
	if body, ok := h.counting.postBody("/actions/runs/1/rerun"); !ok || strings.Contains(body, "enable_debug_logging") {
		t.Errorf("default re-run body = %q, want no enable_debug_logging (AC14)", body)
	}

	// Run 2: the debug opt-in. WithDebugLogging sets the flag the request body carries (AC14).
	p, err := h.ops.Plan(ops.OpRerun, items("o", "r", 2), snapshot(writableRepo("o", "r")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	dbg, err := h.ops.Confirm(p.WithDebugLogging(), ops.NonInteractiveYes())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if sumDbg := runPurge(t, h, dbg); sumDbg.Acted != 1 {
		t.Errorf("debug re-run = acted %d, want 1 (R8)", sumDbg.Acted)
	}
	if body, ok := h.counting.postBody("/actions/runs/2/rerun"); !ok || !strings.Contains(body, `"enable_debug_logging":true`) {
		t.Errorf("debug re-run body = %q, want enable_debug_logging true (R14, AC14)", body)
	}
	if h.logExists() {
		t.Errorf("a re-run wrote a deletion log; R24 forbids it (a re-run adds an Attempt, it deletes nothing)")
	}
}

// TestRerunFailedIsADistinctEndpointAndTakesDebug pins R13 and R14: re-run failed Jobs is
// its own endpoint, and the debug opt-in applies to it exactly as to plain re-run (AC14).
func TestRerunFailedIsADistinctEndpointAndTakesDebug(t *testing.T) {
	h := newHarness(t, "rerun_failed", 50, 50)
	p, err := h.ops.Plan(ops.OpRerunFailed, items("o", "r", 1), snapshot(writableRepo("o", "r")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c, err := h.ops.Confirm(p.WithDebugLogging(), ops.NonInteractiveYes())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	sum := runPurge(t, h, c)
	if sum.Acted != 1 {
		t.Errorf("re-run failed = acted %d, want 1 (R13)", sum.Acted)
	}
	body, ok := h.counting.postBody("/actions/runs/1/rerun-failed-jobs")
	if !ok {
		t.Fatalf("re-run failed did not POST its own /rerun-failed-jobs endpoint (R13)")
	}
	if !strings.Contains(body, `"enable_debug_logging":true`) {
		t.Errorf("re-run failed debug body = %q, want enable_debug_logging true (R14, AC14)", body)
	}
}

// TestFourOhFourReadsByRequestedEndState pins AC13 and R22: a 404 is not read uniformly.
// For a cancel it means the Run is gone and so not running, the requested end state, a skip.
// For a re-run it means the Run cannot gain an Attempt, a failure. The identical status,
// two opposite readings, driven from one Run across two operations.
func TestFourOhFourReadsByRequestedEndState(t *testing.T) {
	h := newHarness(t, "lifecycle_404", 50, 50)
	repos := snapshot(writableRepo("o", "r"))

	// cancel of a deleted Run: the 404 is a skip, not a failure (R22, AC13).
	cancelC := h.confirmed(t, ops.OpCancel, items("o", "r", 1), repos)
	cancelSum := runPurge(t, h, cancelC)
	if cancelSum.Skipped != 1 || cancelSum.FailedCount() != 0 {
		t.Errorf("cancel 404 = skipped %d, failed %d; want 1/0 (R22, AC13)", cancelSum.Skipped, cancelSum.FailedCount())
	}

	// re-run of the same deleted Run: the 404 is a failure (R22, AC13).
	rerunC := h.confirmed(t, ops.OpRerun, items("o", "r", 1), repos)
	rerunSum := runPurge(t, h, rerunC)
	if rerunSum.FailedCount() != 1 || rerunSum.Skipped != 0 {
		t.Errorf("re-run 404 = failed %d, skipped %d; want 1/0 (R22, AC13)", rerunSum.FailedCount(), rerunSum.Skipped)
	}
}

// TestRerunBreakerReusesThePurgeContract pins R21: a bulk lifecycle operation reuses the
// Purge's breaker unchanged. Three re-run 404s (each a failure) at a breaker threshold of
// three stop the operation, and the fourth Run is never attempted (its POST has no tape).
func TestRerunBreakerReusesThePurgeContract(t *testing.T) {
	h := newHarness(t, "rerun_breaker", 50, 3) // breaker threshold 3
	c := h.confirmed(t, ops.OpRerun, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if !sum.CircuitBroke {
		t.Errorf("three consecutive re-run failures did not trip the breaker (R21)")
	}
	if got := h.counting.countMethod("POST"); got != 3 {
		t.Errorf("issued %d re-run POSTs, want 3; the fourth Run must not be attempted after the break (R21)", got)
	}
	if sum.FailedCount() != 3 {
		t.Errorf("recorded %d failures, want 3", sum.FailedCount())
	}
}

// TestCancelRateLimitBoundThenSkip pins R19, R19a and R3 reuse for a bulk cancel: a Run
// whose cancel keeps answering a rate-limit-classified 403 is re-attempted at most three
// times, then skipped, and the operation proceeds. The rate limit is never a failure (R21),
// so the operation does not circuit-break. This is the Purge's contract, unchanged.
func TestCancelRateLimitBoundThenSkip(t *testing.T) {
	h := newHarness(t, "cancel_ratelimit", 50, 50)
	c := h.confirmed(t, ops.OpCancel, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.FailedCount() != 0 {
		t.Errorf("a rate limit was counted as a failure; R21 forbids it. Failures: %+v", sum.Failures)
	}
	if sum.CircuitBroke {
		t.Errorf("a rate limit tripped the breaker; a backoff must not advance it (R21)")
	}
	if sum.Skipped != 1 || sum.Acted != 1 {
		t.Errorf("summary = skipped %d, acted %d; want 1/1 (Run 1 bounded-and-skipped, Run 2 cancelled) (R19a)", sum.Skipped, sum.Acted)
	}
	// Run 1's cancel was issued exactly three times before R19a reclassified it, Run 2 once.
	if got := h.counting.countMethod("POST"); got != 4 {
		t.Errorf("issued %d cancel POSTs, want 4 (Run 1 x3 bounded, Run 2 x1) (R19a)", got)
	}
}

// TestLifecycleSkipsIneligibleWithoutTouchingTheWire pins that an Item stamped ineligible
// at Plan time is skipped with no request, over a transport that fails any wire call, so a
// single leaked mutation would fail the test loudly (R2, AC15, and the no-live-mutation
// rule R28 extends to cancel and re-run). A read-only repository's Run is never cancelled.
func TestLifecycleSkipsIneligibleWithoutTouchingTheWire(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	sel := []ops.Item{
		ops.RunItem(completedRun(1, "o", "readonly")),
		ops.RunItem(completedRun(2, "o", "readonly")),
	}
	repos := snapshot(domain.Repo{ID: repoID("o", "readonly"), Permissions: domain.Permissions{Push: false}})
	c := h.confirmed(t, ops.OpCancel, sel, repos)
	sum := runPurge(t, h, c)

	if sum.Skipped != 2 || sum.Acted != 0 || sum.FailedCount() != 0 {
		t.Errorf("summary = skipped %d, acted %d, failed %d; want 2/0/0 (R2, AC15)", sum.Skipped, sum.Acted, sum.FailedCount())
	}
	if len(h.counting.calls) != 0 {
		t.Errorf("an ineligible cancel reached the wire (%d calls); it must issue nothing (R2, AC15, R28)", len(h.counting.calls))
	}
	if h.logExists() {
		t.Errorf("a lifecycle operation wrote a deletion log; R24 forbids it")
	}
}

// TestLifecycleFrictionAsymmetry pins R17 and R18's asymmetry, the reuse of the Purge's
// graduated confirmation this stage lands. A single cancel and a single force-cancel each
// take a y/N (R18); a single re-run and a single re-run-failed take none, because neither
// destroys a Run and correcting one is the Feed's most common action (R18). A bulk re-run
// over a small single-repository set still confirms (AC10), and a cross-repository bulk
// operation types the count (R17, AC9).
func TestLifecycleFrictionAsymmetry(t *testing.T) {
	oneRepo := snapshot(writableRepo("o", "a"))
	twoRepos := snapshot(writableRepo("o", "a"), writableRepo("o", "b"))
	cross := append(runItems("o", "a", 1, 1), runItems("o", "b", 1, 100)...)

	cases := []struct {
		name string
		op   ops.Operation
		sel  []ops.Item
		reps map[domain.RepoID]domain.Repo
		want ops.FrictionLevel
	}{
		{"single cancel is y/N (R18)", ops.OpCancel, runItems("o", "a", 1, 1), oneRepo, ops.FrictionYN},
		{"single force-cancel is y/N (R18)", ops.OpForceCancel, runItems("o", "a", 1, 1), oneRepo, ops.FrictionYN},
		{"single re-run takes none (R18)", ops.OpRerun, runItems("o", "a", 1, 1), oneRepo, ops.FrictionNone},
		{"single re-run-failed takes none (R18)", ops.OpRerunFailed, runItems("o", "a", 1, 1), oneRepo, ops.FrictionNone},
		{"bulk re-run small single-repo still confirms (AC10)", ops.OpRerun, runItems("o", "a", 3, 1), oneRepo, ops.FrictionYN},
		{"bulk cancel cross-repo types the count (AC9)", ops.OpCancel, cross, twoRepos, ops.FrictionTypedCount},
		{"bulk re-run cross-repo types the count (AC10)", ops.OpRerun, cross, twoRepos, ops.FrictionTypedCount},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := newPlanOps(50).Plan(c.op, c.sel, c.reps)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if p.Friction() != c.want {
				t.Errorf("Friction() = %d, want %d", p.Friction(), c.want)
			}
		})
	}
}

// TestLifecycleConfirmedIsSingleUse pins that ADR-0019's spent cell guards a lifecycle
// operation exactly as it guards a Purge: one Confirmed authorises one Execute, and a
// second returns ErrSpent and issues nothing.
func TestLifecycleConfirmedIsSingleUse(t *testing.T) {
	h := newHarness(t, "rerun_ok", 50, 50)
	c := h.confirmed(t, ops.OpRerun, items("o", "r", 1), snapshot(writableRepo("o", "r")))
	if first := runPurge(t, h, c); first.Acted != 1 {
		t.Fatalf("first Execute acted %d, want 1", first.Acted)
	}
	before := h.counting.countMethod("POST")
	if _, err := h.ops.Execute(context.Background(), c); err == nil {
		t.Errorf("second Execute succeeded; a spent Confirmed must return ErrSpent (ADR-0019)")
	}
	if after := h.counting.countMethod("POST"); after != before {
		t.Errorf("the second Execute issued %d further POSTs; a spent Confirmed issues nothing", after-before)
	}
}
