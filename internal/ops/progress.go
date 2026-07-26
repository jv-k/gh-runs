package ops

import (
	"context"
	"errors"
	"time"
)

// ErrBusy is returned when an operation is launched while one is already running. One
// Ops runs one streamed operation at a time, and the reason is R16: the Started handle
// carries the only cancel a running operation has, so a second launch that replaced it
// would leave the first invisible and uncancellable for the rest of the session, running
// to completion on a set of tens of thousands. Refusing here rather than in a surface is
// what makes that hold for every launcher rather than for the one that remembered.
var ErrBusy = errors.New("ops: an operation is already running; stop it before starting another")

// ErrNothingToRetry is returned when Retry is handed a pass with no recorded failures, a
// Summary no pass produced, or one whose single re-attempt is already spent. R22's retry
// set can only shrink from an already confirmed frozen set, so a set with no provenance
// is refused rather than executed.
var ErrNothingToRetry = errors.New("ops: this operation has no recorded failures left to retry")

// rateSamples is the width of R15's observed trailing rate window, in attempts. A
// trailing window rather than the run's average is what the requirement asks for: the
// throttle ramps across a 3x band, so an average taken from the first minute of a
// multi-hour Purge describes a rate the operation left behind long ago. Thirty-two
// attempts is ~16 seconds at the governor's cap and ~64 at its floor, short enough to
// follow a backoff and long enough that one slow response does not swing the figure.
const rateSamples = 32

// Progress is one observation of a running operation, and the whole of what a surface
// needs to paint R15's line: the running tally, how much of the set is still to be
// requested, the elapsed time from the injected clock, the observed trailing rate, and
// the two governor bounds R15's remaining-time range is computed between.
//
// Every frame is a complete snapshot rather than a delta, which is what lets the stream
// drop a frame the surface has not taken yet (see offer) and lets a golden fabricate
// one. The tally's slices are copied on the way out, because groupByReason increments a
// group's Count in place and an aliased slice would be a live race between the
// operation's goroutine and the surface's (ADR-0015).
//
// It is generic over Operation deliberately. A Purge, a bulk lifecycle mutation and a
// Reclamation are the same walk over a frozen set under the same failure contract, so
// they are the same stream and the same surface, differing only in the verb (#61, #64).
type Progress struct {
	// Op is the verb this pass was confirmed for, so one surface renders all of them.
	Op Operation
	// Kind is the object the frozen set holds, or empty where the set mixes Kinds. The
	// Operation alone cannot name what is happening: a delete over Runs is a Purge and a
	// delete over Caches and Artifacts is a Reclamation, which CONTEXT.md holds apart.
	Kind Kind
	// Sum is the running tally, and the pass's terminal Summary once Done is true.
	Sum Summary
	// Outstanding is how many eligible Items are still to be requested: R15's remaining
	// count, and the numerator of both ends of its remaining-time range. An Item stamped
	// ineligible at Plan time issues no request and is not counted here, because it costs
	// no wall clock and would make every estimate too long.
	Outstanding int
	// Elapsed is time since the pass began, from the injected clock and never the wall
	// clock (R27).
	Elapsed time.Duration
	// Rate is the observed trailing rate in attempts per second, zero until two attempts
	// have landed. R15's pessimistic end derives from it.
	Rate float64
	// Ceiling and Floor are the governor's current dynamic write ceiling and its floor,
	// in writes per second (rate-governor R11). R15's optimistic end derives from the
	// ceiling, and its pessimistic end is clamped never worse than the floor. Both are
	// zero when no pacer is wired, which a surface reads as "no estimate yet" rather
	// than as an infinitely slow Purge.
	Ceiling float64
	Floor   float64
	// Done marks the stream's terminal event. Sum is the pass's Summary on it, and the
	// channel closes immediately afterwards.
	Done bool
}

// Started is the handle a launched operation hands its surface, and the first message
// the initiating Cmd returns (ADR-0015): the verb, the frozen total, the progress
// stream, and the cancel that stops the operation. It carries no Bubble Tea type, and
// ops imports none: the type is the emitter's own and the root adapts it, exactly as
// the engine's channel is adapted.
//
// Cancel is R16. It cancels the context every request is issued under, so a cancelled
// operation stops waiting on the governor's pacing timer and stops issuing rather than
// repainting while the walk grinds on.
type Started struct {
	Op       Operation
	Kind     Kind
	Total    int
	Progress <-chan Progress
	Cancel   context.CancelFunc
}

// LaunchFailed reports that an operation could not be started: a declined confirmation,
// a spent one, a launch refused because one is already running, or a retry with nothing
// left to re-attempt. It is a message rather than a dropped error, because a keystroke
// that silently does nothing is the exact defect the in-TUI running surface exists to
// fix. It is the emitter's own type, like Started, so every surface that launches an
// operation reports a refusal the same way.
type LaunchFailed struct {
	Op   Operation
	Kind Kind
	Err  error
}

// Start runs a Confirmed set on its own goroutine and returns promptly with the channel
// its progress travels (ADR-0015). It is Execute's async entry and not a second
// implementation: both claim the same single-use Confirmed and drive the same walk, so
// the failure contract, the deletion log's precondition and the breaker are identical
// whichever one a surface calls.
//
// It exists because a Purge runs for ~155 minutes in the normal case and as long as ~10
// hours at the governor's floor, which a blocking call cannot do inside a tea loop
// without freezing every other surface (R14, AC10). A caller with no screen keeps
// calling Execute.
func (o *Ops) Start(ctx context.Context, c Confirmed) (Started, error) {
	// The launch gate is taken before the Confirmed is claimed, so a refused launch leaves
	// the confirmation unspent and the operator can start it once the running one is over
	// rather than having to confirm the same set again.
	if !o.launching.CompareAndSwap(false, true) {
		return Started{}, ErrBusy
	}
	plan, err := c.claim()
	if err != nil {
		o.launching.Store(false)
		return Started{}, err
	}
	if !supportedOperation(plan.op) {
		o.launching.Store(false)
		return Started{}, unsupportedOperation(plan.op)
	}
	return o.start(ctx, plan), nil
}

// Retry re-attempts only the failures a finished pass recorded, reusing the same
// throttle and the same failure contract (R22, AC18). It needs no fresh confirmation
// because its set is a subset of an already-confirmed frozen set and can only shrink.
//
// That exemption is structural rather than conventional. The retry set is read from the
// Summary's unexported failed list, which only a pass through Execute can populate, and
// the operation is read from the Summary's own unexported stamp, so a Summary a caller
// assembled carries neither and is refused. There is still no path from a selection to a
// DELETE that skips confirmation (R9): this path starts at a DELETE that already went
// through one.
func (o *Ops) Retry(ctx context.Context, sum Summary) (Started, error) {
	if !o.launching.CompareAndSwap(false, true) {
		return Started{}, ErrBusy
	}
	// A Summary authorises one re-attempt, exactly as a Confirmed authorises one execution
	// (ADR-0019). The cell is minted by the walk and shared by every copy of the Summary,
	// so single use survives the value crossing the progress channel, and a Summary a
	// caller assembled carries no cell at all.
	if sum.retry == nil || !sum.retry.CompareAndSwap(false, true) {
		o.launching.Store(false)
		return Started{}, ErrNothingToRetry
	}
	failed := sum.Failed()
	if len(failed) == 0 {
		o.launching.Store(false)
		return Started{}, ErrNothingToRetry
	}
	// The retry Plan carries the failures with the eligibility stamps the first pass
	// resolved. Re-stamping them would need the repository snapshot again and could only
	// re-derive what Plan already decided, and R5 froze the set at the modal, not here.
	plan := Plan{
		op:        sum.op,
		items:     failed,
		friction:  FrictionNone,
		breakdown: breakdownOf(failed),
		debug:     sum.debug,
	}
	return o.start(ctx, plan), nil
}

// start spawns the walk and returns its handle. It derives its own cancellable context
// from ctx, so Cancel stops this operation and nothing else, and closing over it is what
// makes R16 a property of the returned handle rather than of the caller remembering to
// keep a cancel func.
func (o *Ops) start(ctx context.Context, plan Plan) Started {
	runCtx, cancel := context.WithCancel(ctx)
	ch := make(chan Progress, 1)
	go o.stream(runCtx, cancel, plan, ch)
	return Started{Op: plan.op, Kind: plan.Kind(), Total: plan.Total(), Progress: ch, Cancel: cancel}
}

// stream drives the walk and publishes its snapshots. It closes the channel when the
// pass ends, which is the surface's signal that the stream is over, releases the context
// whatever ended it so a cancelled or completed operation leaks neither, and frees the
// launch gate so the next operation may start.
func (o *Ops) stream(ctx context.Context, cancel context.CancelFunc, plan Plan, ch chan Progress) {
	// The gate is released once per walk, and the release is idempotent because it happens
	// on two paths. The explicit one below runs before the terminal frame, so a surface
	// acting on that frame is not refused by a gate this walk has already finished with;
	// this one covers any exit that does not reach it.
	//
	// It must not be a second unconditional store. Between the explicit release and the
	// walk's return, another operation can win the gate: that window is exactly the one the
	// explicit release opens, and it is reached on every walk. Clearing a gate this walk no
	// longer holds would admit a third launch while the second was still deleting, which is
	// the orphaning this gate exists to prevent.
	//
	// The release is registered after close, so it runs before it. A consumer learns the
	// stream ended only once the gate is free, which makes "the channel closed" a usable
	// signal rather than one that races the release.
	released := false
	release := func() {
		if !released {
			released = true
			o.launching.Store(false)
		}
	}
	defer close(ch)
	defer release()
	defer cancel()

	begin := o.clk.Now()
	win := &rateWindow{}
	outstanding := -1 // sentinel: the first frame is not an attempt landing

	frame := func(sum Summary, left int, done bool) Progress {
		now := o.clk.Now()
		// Outstanding falls by exactly one when a request concludes, so the walk needs to
		// tell the window nothing: the count is the signal. A skip stamped at Plan time
		// leaves it unchanged and correctly contributes no sample, because it took no time.
		// The terminal frame zeroes the count whatever is left, so it is not an attempt
		// landing and must not be sampled as one.
		if !done && outstanding >= 0 && left < outstanding {
			win.mark(now)
		}
		if !done {
			outstanding = left
		}
		ceiling, floor := o.pacing()
		return Progress{
			Op:          plan.op,
			Kind:        plan.Kind(),
			Sum:         sum.snapshot(),
			Outstanding: left,
			Elapsed:     now.Sub(begin),
			Rate:        win.rate(),
			Ceiling:     ceiling,
			Floor:       floor,
			Done:        done,
		}
	}

	sum, err := o.execute(ctx, plan, func(s Summary, left int) {
		offer(ch, frame(s, left, false))
	})
	if err != nil {
		// Only a caller misuse reaches here, and Start validated the operation before
		// spawning. Report it on the stream rather than dropping it, because a surface
		// waiting on a terminal event would otherwise wait forever.
		sum.Reason = err.Error()
	}
	// Release before the terminal frame: R22's retry is offered from that frame, and a
	// keystroke on it must not be refused by a gate this walk has already finished with.
	release()
	offer(ch, frame(sum, 0, true))
}

// pacing reads the governor's current write bounds, or zeroes when no pacer is wired.
func (o *Ops) pacing() (ceiling, floor float64) {
	if o.pacer == nil {
		return 0, 0
	}
	return o.pacer.WriteCeiling()
}

// offer hands one Progress to the surface without ever blocking the operation on it.
// A frame is a complete snapshot, so a frame the surface has not taken yet is simply
// replaced by the newer one: the channel holds one, and a full channel is drained and
// refilled rather than waited on.
//
// That is what keeps a Purge's pace the governor's rather than the repaint loop's, which
// matters because [rate-governor] R11's ceiling is the only thing that may set a delete
// rate (R17). It is also why the terminal event cannot be lost: it replaces whatever
// stale frame is sitting in the buffer, and a buffered value is still delivered after
// the channel closes. There is one producer, so the loop settles in at most two turns.
func offer(ch chan Progress, p Progress) {
	for {
		select {
		case ch <- p:
			return
		default:
		}
		select {
		case <-ch: // drop the stale snapshot the newer one supersedes
		default:
		}
	}
}

// rateWindow is R15's observed trailing rate: the instants of the last rateSamples
// attempts, from the injected clock (R27).
type rateWindow struct{ at []time.Time }

// mark records that an attempt concluded at now.
func (w *rateWindow) mark(now time.Time) {
	w.at = append(w.at, now)
	if len(w.at) > rateSamples {
		w.at = w.at[len(w.at)-rateSamples:]
	}
}

// rate is attempts per second across the window. It is zero until two attempts have
// landed, and zero where they landed in the same instant, because a rate derived from
// one sample is a number with no observation behind it and R15's pessimistic end is
// better served by the governor's floor than by a fabricated figure.
func (w *rateWindow) rate() float64 {
	if len(w.at) < 2 {
		return 0
	}
	span := w.at[len(w.at)-1].Sub(w.at[0]).Seconds()
	if span <= 0 {
		return 0
	}
	return float64(len(w.at)-1) / span
}
