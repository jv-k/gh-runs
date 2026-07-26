package ops

import (
	"context"
	"errors"
	"time"
)

// ErrNotFinished is returned when Retry is handed a pass that has not finished. The
// retry set is the recorded failures, and a running pass has not finished recording
// them (R22).
var ErrNotFinished = errors.New("ops: this operation has not finished")

// ErrNothingToRetry is returned when Retry is handed a pass with no recorded failures,
// or a Summary no pass produced. R22's retry set can only shrink from an already
// confirmed frozen set, so a set with no provenance is refused rather than executed.
var ErrNothingToRetry = errors.New("ops: this operation recorded no failures to retry")

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

// Concluded is how many Items of the frozen set have reached a terminal disposition,
// R15's numerator against Sum.Total.
func (p Progress) Concluded() int {
	return p.Sum.Deleted + p.Sum.Gone + p.Sum.Acted + p.Sum.Skipped + p.Sum.FailedCount()
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
	Total    int
	Progress <-chan Progress
	Cancel   context.CancelFunc
}

// LaunchFailed reports that an operation could not be started: a declined confirmation,
// a spent one, or a retry the engine refused. It is a message rather than a dropped
// error, because a keystroke that silently does nothing is the exact defect the in-TUI
// running surface exists to fix. It is the emitter's own type, like Started, so every
// surface that launches an operation reports a refusal the same way.
type LaunchFailed struct {
	Op  Operation
	Err error
}

// Error makes LaunchFailed usable where the refusal is handled as an error rather than
// rendered, and keeps the operation's verb in the text either way.
func (l LaunchFailed) Error() string {
	if l.Err == nil {
		return string(l.Op) + " could not be started"
	}
	return string(l.Op) + " could not be started: " + l.Err.Error()
}

// Unwrap exposes the refusal underneath, so errors.Is against ErrDeclined or ErrSpent
// still answers.
func (l LaunchFailed) Unwrap() error { return l.Err }

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
	plan, err := c.claim()
	if err != nil {
		return Started{}, err
	}
	if !supportedOperation(plan.op) {
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
	if sum.op == "" {
		return Started{}, ErrNothingToRetry // no pass produced this Summary
	}
	failed := sum.Failed()
	if len(failed) == 0 {
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
	return Started{Op: plan.op, Total: plan.Total(), Progress: ch, Cancel: cancel}
}

// stream drives the walk and publishes its snapshots. It closes the channel when the
// pass ends, which is the surface's signal that the stream is over, and it releases the
// context whatever ended it so a cancelled or completed operation leaks neither.
func (o *Ops) stream(ctx context.Context, cancel context.CancelFunc, plan Plan, ch chan Progress) {
	defer cancel()
	defer close(ch)

	begin := o.clk.Now()
	win := &rateWindow{}
	outstanding := -1 // sentinel: the first frame is not an attempt landing

	frame := func(sum Summary, left int, done bool) Progress {
		now := o.clk.Now()
		// Outstanding falls by exactly one when a request concludes, so the walk needs to
		// tell the window nothing: the count is the signal. A skip stamped at Plan time
		// leaves it unchanged and correctly contributes no sample, because it took no time.
		if outstanding >= 0 && left < outstanding {
			win.mark(now)
		}
		outstanding = left
		ceiling, floor := o.pacing()
		return Progress{
			Op:          plan.op,
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
