package ops

import (
	"context"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is ghclient.Client's RequestWithContext
// (ADR-0012: the response survives, so Link and the rate-limit headers do), narrowed
// to what ops uses. The context is load-bearing here where the read half's seam was
// context-free: a Purge's DELETE must be cancellable (purge R16), and the context
// reaches the governor's pacing and the limiter's wire through req.Context(). A
// cassette-backed ghclient fills it in tests, so a Purge is exercised against what
// the API actually said with no live DELETE (purge R26, R28). The DELETE is paced by
// the governor and bounded by the limiter because both sit inside this transport,
// not because ops arranges it.
type Requester interface {
	RequestWithContext(ctx context.Context, method, path string, body io.Reader) (*http.Response, error)
}

// Pacer reports the governor's current dynamic write ceiling and its floor, both in
// writes per second (rate-governor R11). ops reads it to stamp the two bounds purge R15
// computes its remaining-time range between, so the range is arithmetic on a frame the
// surface already holds rather than a second seam every surface has to be handed
// (AC23). *governor.Governor satisfies it.
//
// It is deliberately not the Budget Readout. The Readout is an observation about the
// primary limit, which a Purge's writes do not spend (ADR-0007), and it is broadcast on
// change: a float that moves with every read would turn that broadcast into a storm.
type Pacer interface {
	WriteCeiling() (ceiling, floor float64)
}

// Options carries ops's seams and its two configured numbers. main.go fills them:
// the client is the store-then-governor-then-limiter chain, the clock is the
// injected one (purge R27), LogPath is $XDG_STATE_HOME/gh-runs/deletions.log which
// main.go resolves so ops owns no directory policy (ADR-0011, R29), and the two
// thresholds are the values config already resolved and clamped (settings R12, R21).
// Pacing is the governor itself, read for R15's remaining-time range; a nil one leaves
// both bounds zero, which a surface reads as having no estimate to offer yet.
type Options struct {
	Client           Requester
	Clock            clock.Clock
	LogPath          string
	ConfirmThreshold int
	BreakerFailures  int
	Pacing           Pacer
}

// logSink is the append-only deletion record Execute writes, narrowed to the two
// operations Execute performs. *deletionLog satisfies it in production; a test
// injects a sink that fails on the Nth write to prove R29's "a log write that fails
// mid-operation stops it" without needing to fill a disk (AC20). It has no read
// method, which is R29's write-only rule made structural: no code path can read the
// log back, so R24 stays the only resume.
type logSink interface {
	write(logRecord) error
	close() error
}

// Ops is the write engine. It holds the transport seam, the clock, the deletion
// log's path, and the two thresholds Plan and Execute read. It is safe to share:
// Plan and Confirm are pure over their arguments, and Execute is sequential per
// call and single-use per Confirmed.
type Ops struct {
	client           Requester
	clk              clock.Clock
	logPath          string
	confirmThreshold int
	breakerFailures  int
	pacer            Pacer

	// openLog opens the deletion log. It is a field so a test can inject a failing
	// sink; production wires the real append-only file. It is never a seam a caller
	// outside ops can set, so the log stays ops's alone (ADR-0011, R29).
	openLog func(path string, clk clock.Clock) (logSink, error)
}

// New returns an Ops over opts. A nil Clock defaults to the wall clock, which a
// test overrides with a fake so pacing and the log's timestamps are deterministic
// (purge R27, R29).
func New(opts Options) *Ops {
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	return &Ops{
		client:           opts.Client,
		clk:              clk,
		logPath:          opts.LogPath,
		confirmThreshold: opts.ConfirmThreshold,
		breakerFailures:  opts.BreakerFailures,
		pacer:            opts.Pacing,
		openLog: func(path string, c clock.Clock) (logSink, error) {
			return openDeletionLog(path, c)
		},
	}
}
