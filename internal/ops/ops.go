package ops

import (
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is exactly ghclient.Client's surface
// (ADR-0012: Request, never Get or Do, so the response's Link and rate-limit
// headers survive), narrowed to what ops uses. A cassette-backed ghclient fills it
// in tests, so a Purge is exercised against what the API actually said with no live
// DELETE (purge R26, R28). The DELETE it issues is paced by the governor and bounded
// by the limiter because both sit inside this transport, not because ops arranges it.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Options carries ops's seams and its two configured numbers. main.go fills them:
// the client is the store-then-governor-then-limiter chain, the clock is the
// injected one (purge R27), LogPath is $XDG_STATE_HOME/gh-runs/deletions.log which
// main.go resolves so ops owns no directory policy (ADR-0011, R29), and the two
// thresholds are the values config already resolved and clamped (settings R12, R21).
type Options struct {
	Client           Requester
	Clock            clock.Clock
	LogPath          string
	ConfirmThreshold int
	BreakerFailures  int
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
	}
}
