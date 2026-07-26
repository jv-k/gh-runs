package ops_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// blockedProgress holds a started operation's stream unread so the walk is still in
// flight while the test does something else, and returns the release that drains it to
// completion while driving virtual time. Nothing advances the fake clock until then, so
// the walk issues its first request and parks on the governor's pacing timer.
func blockedProgress(t *testing.T, h *harness, st ops.Started) (release func() []ops.Progress) {
	t.Helper()
	return func() []ops.Progress { return drainProgress(t, h, st) }
}

// firstRequest returns a channel closed once the operation's first wire request lands, so
// a test can wait for the walk to be genuinely under way rather than racing the goroutine
// Start spawned. It must be armed before Start.
func firstRequest(h *harness) <-chan struct{} {
	landed := make(chan struct{})
	var once sync.Once
	h.counting.onDelete = func(int) { once.Do(func() { close(landed) }) }
	return landed
}

// TestStartRefusesASecondOperationWhileOneRuns pins R16 against the orphaning bug: the
// handle carrying a running operation's cancel is the only way to stop it, so a second
// launch that overwrote it would leave the first invisible and uncancellable for the rest
// of the session, running to completion on a set of tens of thousands. The engine refuses
// instead, which is what makes the guarantee hold for every surface that launches rather
// than for the one that remembered to check.
func TestStartRefusesASecondOperationWhileOneRuns(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	first := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4, 5, 6, 7, 8), snapshot(writableRepo("o", "r")))
	second := h.confirmed(t, ops.OpDelete, items("o", "r", 9), snapshot(writableRepo("o", "r")))

	st, err := h.ops.Start(context.Background(), first)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := blockedProgress(t, h, st)

	if _, err := h.ops.Start(context.Background(), second); !errors.Is(err, ops.ErrBusy) {
		t.Fatalf("a second Start while one runs returned %v, want ErrBusy; accepting it orphans the first operation's cancel (R16)", err)
	}
	// The first operation is still the one the handle stops, which is the property the
	// refusal exists to preserve.
	st.Cancel()
	got := release()
	final := got[len(got)-1]
	if !final.Sum.Cancelled {
		t.Errorf("the first operation did not stop on its own handle's cancel; it was orphaned (R16)")
	}
	if h.counting.deletes() >= 8 {
		t.Errorf("the first operation ran to completion after being cancelled (%d DELETEs of 8)", h.counting.deletes())
	}
	// Once it is over, the gate is free and the second set may be launched.
	if _, err := h.ops.Start(context.Background(), second); err != nil {
		t.Errorf("the gate stayed held after the operation ended: %v", err)
	}
}

// TestConcurrentDeletionsCannotBothHoldTheLog pins R29's record against splitting. Two
// deletion walks running at once hold two append handles to one file, each tracking size
// from its own Stat, so one handle's rotation renames the file the other is still
// appending to and a generation can vanish. That is the one file recoverable from nowhere
// else, so the second deletion refuses to start and names the log, exactly as an
// unwritable log does (R29, AC20).
//
// No surface reaches this today: the Feed's Purge is the only in-process launcher, and
// logview's and storage's planners stop at Plan. The test drives the two entries directly,
// which is the shape the queued callers will take (log-viewer R17, storage-reclamation
// R17). It is written now because the harm is silent and permanent: a split log is
// discovered by a person asking what a Purge destroyed, after the answer stopped being
// recoverable.
func TestConcurrentDeletionsCannotBothHoldTheLog(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	purge := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4, 5, 6, 7, 8), snapshot(writableRepo("o", "r")))
	logs := h.confirmed(t, ops.OpDelete, []ops.Item{ops.LogItem(completedRun(9, "o", "r"))}, snapshot(writableRepo("o", "r")))

	landed := firstRequest(h)
	st, err := h.ops.Start(context.Background(), purge)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := blockedProgress(t, h, st)
	<-landed // the Purge is under way and holds the log

	before := h.counting.deletes()
	// The refusal is a decision, not a wait, so it returns without touching the wire and
	// without needing virtual time to move. Bounding the call is what turns a regression
	// into a named failure: a second deletion that queued behind the running one instead
	// would park on the governor's pacing timer, which nothing is advancing here, and the
	// test would hang rather than say what broke.
	type result struct {
		sum ops.Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, e := h.ops.Execute(context.Background(), logs)
		done <- result{s, e}
	}()
	var sum ops.Summary
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Execute: %v", r.err)
		}
		sum = r.sum
	case <-time.After(5 * time.Second):
		t.Fatalf("a second deletion did not return while one was running; it queued behind it rather than refusing (R29)")
	}
	if !sum.LogFailed {
		t.Errorf("a second deletion started beside a running one; two handles to one log split R29's record")
	}
	if !strings.Contains(sum.Reason, "log") {
		t.Errorf("the refusal reason is %q, want it to name the log (AC20)", sum.Reason)
	}
	if h.counting.deletes() != before {
		t.Errorf("the refused deletion issued %d DELETEs, want zero: no record, no deletion (R29)", h.counting.deletes()-before)
	}
	release()
}

// stepPacer is a Pacer the test drives one frame at a time. Every frame the walk builds
// reads the write bounds through it, so it is a synchronisation point inside stream that
// needs no wire request and no clock: the test can park a goroutine at an exact line and
// act while it is held there.
type stepPacer struct {
	hit    chan struct{}
	resume chan struct{}
}

func newStepPacer() *stepPacer {
	return &stepPacer{hit: make(chan struct{}), resume: make(chan struct{})}
}

func (p *stepPacer) WriteCeiling() (float64, float64) {
	p.hit <- struct{}{}
	<-p.resume
	return 2.5, 0.5
}

// step lets exactly one frame through.
func (p *stepPacer) step() {
	<-p.hit
	p.resume <- struct{}{}
}

// hold blocks until a frame reaches the pacer and leaves it parked there.
func (p *stepPacer) hold() { <-p.hit }

// letGo releases the frame hold is holding.
func (p *stepPacer) letGo() { p.resume <- struct{}{} }

// TestOneGoroutineCannotReleaseAnothersGate pins the gate against a double release. The
// walk frees the gate explicitly before its terminal frame, so a second operation can win
// it during that frame. A second, unconditional release on the way out would then clear
// the gate the second operation is holding, and a third launch would be admitted while it
// was still deleting. That is H1's harm exactly: the second operation's handle, its only
// cancel, is dropped by the root.
//
// The probe needs no wire request and no clock. Every frame reads the write bounds through
// the injected Pacer, and the terminal frame reads them after the explicit release, so
// parking a goroutine there is parking it inside the window under test.
func TestOneGoroutineCannotReleaseAnothersGate(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	pacer := newStepPacer()
	o := ops.New(ops.Options{
		Client: h.client, Clock: h.clk, LogPath: h.logPath,
		ConfirmThreshold: 50, BreakerFailures: 50, Pacing: pacer,
	})
	// A read-only repository stamps every Item ineligible, so each walk concludes without
	// reaching the wire (AC15) and the offline transport stays untouched.
	readOnly := domain.Repo{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: false}}
	confirmed := func(id int64) ops.Confirmed {
		t.Helper()
		p, err := o.Plan(ops.OpDelete, items("o", "r", id), snapshot(readOnly))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		c, err := o.Confirm(p, ops.NonInteractiveYes())
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		return c
	}

	first, err := o.Start(context.Background(), confirmed(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pacer.step() // the opening frame
	pacer.step() // the skipped Item's frame
	pacer.hold() // parked building the terminal frame, with the gate already released

	second, err := o.Start(context.Background(), confirmed(2))
	if err != nil {
		t.Fatalf("the gate was not free once the first walk released it: %v", err)
	}

	pacer.letGo()
	for range first.Progress { // drains to close, which happens after the first walk's release
	}

	if _, err := o.Start(context.Background(), confirmed(3)); !errors.Is(err, ops.ErrBusy) {
		t.Fatalf("a third operation started while the second still held the gate (%v); one goroutine's release cleared another's, which orphans the second operation's cancel (R16)", err)
	}

	// Let the parked second walk finish, so no goroutine outlives the test. The barrier is
	// the stream's own close rather than a timer: the walk cannot reach it while parked in
	// the pacer, so once the drain ends there are no further frames to service. A timer
	// here would sleep through a real interval, which this package's seams exist to avoid,
	// and would park the walk for the process lifetime on a loaded machine.
	second.Cancel()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range second.Progress {
		}
	}()
	for {
		select {
		case <-pacer.hit:
			pacer.resume <- struct{}{}
		case <-drained:
			return
		}
	}
}

// TestRetryIsSingleUse pins R22's set against unbounded re-attempts of one recorded pass.
// A Summary authorises one retry, exactly as a Confirmed authorises one execution
// (ADR-0019), so two keystrokes landing before the first handle arrives cannot dispatch
// two walks over the same Items.
func TestRetryIsSingleUse(t *testing.T) {
	h := newHarness(t, "delete_retry", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3), snapshot(writableRepo("o", "r")))
	st, err := h.ops.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := drainProgress(t, h, st)
	sum := got[len(got)-1].Sum

	rst, err := h.ops.Retry(context.Background(), sum)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if _, err := h.ops.Retry(context.Background(), sum); err == nil {
		t.Errorf("the same Summary retried twice; one recorded pass authorises one re-attempt (R22)")
	}
	// The retry holds the same gate a launch does, so nothing else starts beside it. Two
	// keystrokes landing before the first handle arrives cannot dispatch two walks.
	other := h.confirmed(t, ops.OpDelete, items("o", "r", 7), snapshot(writableRepo("o", "r")))
	if _, err := h.ops.Start(context.Background(), other); !errors.Is(err, ops.ErrBusy) {
		t.Errorf("a launch beside a running retry returned %v, want ErrBusy", err)
	}
	drainProgress(t, h, rst)
}

// TestPlanKindNamesTheObjectTheSetHolds pins what a surface needs to say what it is doing.
// A delete over Runs is a Purge, a delete over Caches and Artifacts is a Reclamation, and
// the Operation alone cannot tell them apart (CONTEXT.md).
func TestPlanKindNamesTheObjectTheSetHolds(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	repos := snapshot(writableRepo("o", "r"))

	runs, err := h.ops.Plan(ops.OpDelete, items("o", "r", 1, 2), repos)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if runs.Kind() != ops.KindRun {
		t.Errorf("a Run set reports Kind %q, want %q", runs.Kind(), ops.KindRun)
	}

	mixed, err := h.ops.Plan(ops.OpDelete, []ops.Item{
		ops.CacheItem(domain.Cache{ID: 1, Repo: repoID("o", "r")}),
		ops.ArtifactItem(domain.Artifact{ID: 2, Repo: repoID("o", "r")}),
	}, repos)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if mixed.Kind() != "" {
		t.Errorf("a set mixing Kinds reports %q, want the empty Kind; no single noun describes it", mixed.Kind())
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
