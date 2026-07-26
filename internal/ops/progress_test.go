package ops_test

import (
	"context"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// drainProgress drives the fake clock while draining a started operation's progress
// stream, and returns every Progress it received in order. It is runPurge's shape for
// the async entry: the operation runs on its own goroutine, so virtual time must keep
// moving underneath it or a paced write never releases (purge R27).
func drainProgress(t *testing.T, h *harness, st ops.Started) []ops.Progress {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	var got []ops.Progress
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range st.Progress {
			got = append(got, p)
		}
		cancel() // the stream closed: stop driving virtual time
	}()
	const quantum = 200 * time.Millisecond
	for {
		if err := h.clk.BlockUntilContext(runCtx, 1); err != nil {
			break
		}
		h.clk.Advance(quantum)
	}
	<-done
	return got
}

// TestStartStreamsProgressAndFinishesWithTheSummary pins ADR-0015's write half: Start
// returns promptly with a channel of ops-typed progress events, the stream's terminal
// event carries the pass's Summary, and the channel then closes. The tally is the one
// Execute produces over the same cassette, so the async entry is the same walk with a
// stream bolted on rather than a second implementation (R15).
func TestStartStreamsProgressAndFinishesWithTheSummary(t *testing.T) {
	h := newHarness(t, "delete_mixed", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))

	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st.Op != ops.OpDelete {
		t.Errorf("Started.Op = %q, want %q; the surface renders the verb from it (#61, #64)", st.Op, ops.OpDelete)
	}
	if st.Total != 4 {
		t.Errorf("Started.Total = %d, want 4; the frozen total is R15's denominator", st.Total)
	}
	if st.Cancel == nil {
		t.Fatalf("Started carries no cancel; R16 has nothing to stop the Purge with")
	}

	got := drainProgress(t, h, st)
	if len(got) == 0 {
		t.Fatalf("the progress stream carried nothing (R15)")
	}
	final := got[len(got)-1]
	if !final.Done {
		t.Fatalf("the last event is not the terminal one; the summary would never land (R22)")
	}
	if final.Sum.Deleted != 1 || final.Sum.Gone != 1 || final.Sum.Skipped != 1 || final.Sum.FailedCount() != 1 {
		t.Errorf("terminal summary = deleted %d, gone %d, skipped %d, failed %d; want 1/1/1/1, the tally Execute reports over this cassette",
			final.Sum.Deleted, final.Sum.Gone, final.Sum.Skipped, final.Sum.FailedCount())
	}
	if final.Sum.Total != 4 {
		t.Errorf("terminal Total = %d, want the frozen 4", final.Sum.Total)
	}
	if final.Outstanding != 0 {
		t.Errorf("terminal Outstanding = %d, want 0 once the whole set is attempted", final.Outstanding)
	}
	if h.counting.deletes() != 4 {
		t.Errorf("issued %d DELETEs, want 4; the streamed walk must attempt exactly what Execute does", h.counting.deletes())
	}
	// R27: elapsed comes from the injected clock. The driver advances virtual time in
	// 200ms quanta while the governor paces, so a finished pass cannot read zero.
	if final.Elapsed <= 0 {
		t.Errorf("terminal Elapsed = %v, want time from the injected clock (R27)", final.Elapsed)
	}
}

// TestProgressTallyIsMonotonicAndCarriesTheFrozenTotal pins that every frame is a
// complete snapshot rather than a delta: the total never moves, and the concluded
// count only grows. R15's line is rendered from one frame, so a frame that is not
// self-contained would paint a partial truth.
func TestProgressTallyIsMonotonicAndCarriesTheFrozenTotal(t *testing.T) {
	h := newHarness(t, "delete_mixed", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	prev := -1
	for _, p := range drainProgress(t, h, st) {
		if p.Sum.Total != 4 {
			t.Fatalf("a frame reported Total %d, want the frozen 4 on every frame", p.Sum.Total)
		}
		concluded := p.Sum.Deleted + p.Sum.Gone + p.Sum.Acted + p.Sum.Skipped + p.Sum.FailedCount()
		if concluded < prev {
			t.Fatalf("concluded went backwards, %d after %d; a frame is a snapshot, never a delta", concluded, prev)
		}
		prev = concluded
	}
	if prev != 4 {
		t.Errorf("the last frame concluded %d of 4; the stream must end on the whole set", prev)
	}
}

// TestStartedCancelStopsTheWalk pins R16 on the handle the surface actually holds:
// calling Started.Cancel stops the operation, no further DELETE is issued past the one
// that may already be in flight, the terminal event reports the cancellation, and the
// deletions already made stay made. The cancel reaches the request context, so a Purge
// parked on the governor's pacing timer stops waiting rather than running to its
// deadline; nothing here repaints a stopped Purge that is still deleting.
func TestStartedCancelStopsTheWalk(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4, 5, 6, 7, 8), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.counting.onDelete = func(n int) {
		if n == 3 {
			st.Cancel() // the surface's cancel key, pressed after the third DELETE lands
		}
	}

	got := drainProgress(t, h, st)
	final := got[len(got)-1]
	if !final.Done {
		t.Fatalf("the cancelled stream did not end on a terminal event; the surface would hang")
	}
	if !final.Sum.Cancelled {
		t.Errorf("the terminal summary does not report the cancellation (R16, AC11)")
	}
	if n := h.counting.deletes(); n > 4 {
		t.Errorf("issued %d DELETEs after cancellation; at most one further in-flight may complete (R16)", n)
	}
	if final.Sum.Deleted == 0 || final.Sum.Deleted == 8 {
		t.Errorf("cancelled after %d deletions of 8; cancelling stops a Purge, it does not reverse one (R16)", final.Sum.Deleted)
	}
}

// TestRetryReAttemptsOnlyTheRecordedFailures pins R22 and AC18: the retry re-attempts
// exactly the Items the pass recorded as failures, and nothing else in the frozen set.
// It takes no fresh confirmation, because its set is a subset of an already-confirmed
// one and can only shrink.
func TestRetryReAttemptsOnlyTheRecordedFailures(t *testing.T) {
	h := newHarness(t, "delete_retry", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := drainProgress(t, h, st)
	sum := first[len(first)-1].Sum
	if sum.FailedCount() != 2 {
		t.Fatalf("first pass failed %d, want 2 (Runs 2 and 3 answer 403 and 500)", sum.FailedCount())
	}
	if len(sum.Failures) != 2 {
		t.Errorf("failures grouped into %d reasons, want 2 distinct groups (R22, AC18)", len(sum.Failures))
	}
	before := h.counting.deletes()

	rst, err := h.ops.Retry(context.Background(), sum)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	retried := drainProgress(t, h, rst)
	rfinal := retried[len(retried)-1]
	if !rfinal.Done {
		t.Fatalf("the retry stream did not end on a terminal event")
	}
	if got := h.counting.deletes() - before; got != 2 {
		t.Errorf("the retry issued %d DELETEs, want exactly one per recorded failure (AC18)", got)
	}
	if rfinal.Sum.Total != 2 {
		t.Errorf("the retry's frozen total is %d, want the 2 recorded failures (R22)", rfinal.Sum.Total)
	}
	if rfinal.Sum.Deleted != 2 {
		t.Errorf("the retry deleted %d of 2; the second pass's tape answers 204 for both", rfinal.Sum.Deleted)
	}
	// The retry reuses the same failure contract and so writes the same log (R29): every
	// attempt in both passes is one line.
	if n := len(h.readLog(t)); n != 5 {
		t.Errorf("the deletion log holds %d lines, want 3 from the pass and 2 from the retry (R29, AC19)", n)
	}
}

// pacerSpy stands in for the governor, reporting fixed write bounds.
type pacerSpy struct{ ceiling, floor float64 }

func (p pacerSpy) WriteCeiling() (float64, float64) { return p.ceiling, p.floor }

// TestProgressCarriesTheGovernorsWriteBounds pins AC23 at its source: every frame carries
// the governor's current dynamic write ceiling and its floor, so the surface computes the
// remaining-time range between bounds the governor published rather than between figures
// it invented. Reading them here rather than in the surface is also what keeps the range
// checkable from a fabricated frame.
func TestProgressCarriesTheGovernorsWriteBounds(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	h.ops = ops.New(ops.Options{
		Client: h.client, Clock: h.clk, LogPath: h.logPath,
		ConfirmThreshold: h.confirmThreshold, BreakerFailures: h.breakerFailures,
		Pacing: pacerSpy{ceiling: 1.96, floor: 0.5},
	})
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, p := range drainProgress(t, h, st) {
		if p.Ceiling != 1.96 || p.Floor != 0.5 {
			t.Fatalf("frame carries ceiling %v and floor %v, want the pacer's 1.96 and 0.5 (R15, AC23)", p.Ceiling, p.Floor)
		}
	}
}

// TestOutstandingExcludesTheItemsThatIssueNoRequest pins the denominator R15's estimate
// divides by: an Item stamped ineligible at Plan time concludes instantly and costs no
// wall clock, so counting it would make every remaining-time figure too long by the size
// of the skip set (AC15).
func TestOutstandingExcludesTheItemsThatIssueNoRequest(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	sel := items("o", "r", 1, 2)
	sel = append(sel, ops.RunItem(completedRun(3, "o", "readonly"))) // ineligible: read-only
	repos := snapshot(
		writableRepo("o", "r"),
		domain.Repo{ID: repoID("o", "readonly"), Permissions: domain.Permissions{Push: false}},
	)
	c := h.confirmed(t, ops.OpDelete, sel, repos)
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := drainProgress(t, h, st)
	if got[0].Outstanding != 2 {
		t.Errorf("the opening frame reports %d Items to request, want the 2 eligible of 3 (AC15)", got[0].Outstanding)
	}
	if got[0].Sum.Total != 3 {
		t.Errorf("the opening frame reports a frozen total of %d, want 3; the skipped Item is inside the total (AC15)", got[0].Sum.Total)
	}
}

// TestFailedCountAgreesWithTheRetrySet pins the equality FailedCount rests on: R22's
// groups and the retry set are recorded together, so their counts never disagree after a
// real pass. FailedCount reads the exported groups so a surface's golden can fabricate a
// frame, and this is what keeps that reading honest.
func TestFailedCountAgreesWithTheRetrySet(t *testing.T) {
	h := newHarness(t, "delete_retry", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)
	if got, want := sum.FailedCount(), len(sum.Failed()); got != want {
		t.Errorf("FailedCount() = %d but the retry set holds %d Items; the grouped record and the retry set must not disagree (R22)", got, want)
	}
	if sum.FailedCount() != 2 {
		t.Errorf("failed %d, want 2 over this cassette", sum.FailedCount())
	}
}

// TestRetryRefusesASummaryNoPassProduced pins that R22's no-fresh-confirmation exemption
// is structural. The retry set is read from a Summary only Execute can populate, so a
// Summary a caller assembled is refused: there is still no path from a selection to a
// DELETE that skips confirmation (R9).
func TestRetryRefusesASummaryNoPassProduced(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	if _, err := h.ops.Retry(context.Background(), ops.Summary{Total: 9, Failures: []ops.FailureGroup{{Reason: "invented", Count: 9}}}); err == nil {
		t.Fatalf("Retry accepted a Summary no pass produced; the retry's exemption must be structural (R9, R22)")
	}
}

// TestRetryRefusesAPassWithNoFailures pins that the retry offers nothing to re-attempt
// when the pass recorded nothing, so the surface's key stays inert rather than starting
// an empty operation.
func TestRetryRefusesAPassWithNoFailures(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := drainProgress(t, h, st)
	sum := got[len(got)-1].Sum
	if sum.FailedCount() != 0 {
		t.Fatalf("this cassette answers 204 throughout; failed %d", sum.FailedCount())
	}
	if _, err := h.ops.Retry(context.Background(), sum); err == nil {
		t.Errorf("Retry accepted a clean pass; there is nothing to re-attempt (R22)")
	}
}

// TestStartRefusesASpentConfirmation pins that the async entry carries ADR-0019's
// single-use rule exactly as Execute does: a Confirmed authorises one execution,
// whichever entry claims it.
func TestStartRefusesASpentConfirmation(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	// A read-only repository stamps the one Item ineligible, so the first pass concludes
	// without reaching the wire and the offline transport stays untouched (AC15).
	readOnly := domain.Repo{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: false}}
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1), snapshot(readOnly))
	if _, err := h.ops.Execute(context.Background(), c); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := h.ops.Start(context.Background(), c); err == nil {
		t.Fatalf("Start accepted a spent Confirmed; one confirmation must authorise one execution (ADR-0019)")
	}
}
