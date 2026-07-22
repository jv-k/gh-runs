// Package governor is the rate governor: an http.RoundTripper nested under the
// store's and above the network (ADR-0012). It is the only layer where the
// rate-limit headers rate-governor R5 reads still exist, because go-gh's client
// returns an error and discards the response above it. It observes every real
// network exchange and only those: a store hit that never reaches the network
// costs no allowance and the governor never sees it, and a 304 reaches the
// governor as a 304, before the store rewrites it to a 200.
package governor

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// AIMD limits from rate-governor R10 and R11. The floor build carries the state
// but the ramp is exercised by the governor's own cassette suite at stage 2; the
// read path this file proves does not pace writes.
const (
	documentedSafeRate = 1.0 // R10: writes start at the documented-safe 1.0/sec.
	rampCeiling        = 2.5 // R11: cap on the dynamic ceiling.
	rampFloor          = 0.5 // R11: a run of backoffs slows rather than stalls.
	rampStep           = 0.25
	cleanBeforeRamp    = 20 // R10: additive increase after 20 consecutive clean responses.

	// R11's dynamic ceiling: deletes/min = (secondaryPool - observed read
	// points/min) / writePoints. Reads and writes spend the same ~900 points/min
	// pool (the Constraints table), a read costs 1 point and a write costs 5
	// (R16), and the read figure is the count in the trailing minute (R1 already
	// makes the governor account every request).
	secondaryPool = 900.0
	writePoints   = 5.0
	readPoints    = 1.0
	readWindow    = time.Minute
)

// Readout is the Budget Readout (CONTEXT.md): what the governor observed
// about the account's rate limiting at a moment. An observation, never a
// policy, and never the Budget, which is the input it is most easily confused with.
type Readout struct {
	Remaining int       // primary allowance left, from the last response's headers (R5)
	Reset     time.Time // the reset or resume instant. Zero when none is derivable (R9)
	Pressure  bool      // R8a's projection. Never true while burn is zero
	Exhausted bool      // authoritative for R9, live-run-feed R30, polling-scheduler R16.
	// Also true through a secondary-limit backoff (ADR-0018)
}

// Governor observes the primary Budget and paces writes. This build implements
// the observation and accounting the floor test exercises; the ramp state is
// present and minimal.
type Governor struct {
	base http.RoundTripper
	clk  clock.Clock

	mu            sync.Mutex
	primaryUsed   int64   // R7: a 200 costs one primary point.
	notModified   int64   // R7: a 304 costs zero, counted here for observability.
	lastRemaining int64   // R5: last observed x-ratelimit-remaining, -1 before any response.
	lastReset     int64   // R5: last observed x-ratelimit-reset, unix seconds.
	writeRate     float64 // R10: current AIMD ramp target, per second, before the ceiling clamp.
	cleanStreak   int     // R10: consecutive clean responses toward the next increase.

	// Pacing (R2, R21). nextWrite is the earliest instant the next write may go
	// out. Every write advances it by one interval so concurrent writes take
	// successive slots rather than colliding (R3: one global throttle).
	nextWrite time.Time

	// readEvents holds the instant of each read point in the trailing minute
	// (R11). Reads share the secondary pool with writes, so the more the tool is
	// polling, the lower the write ceiling. It is pruned to readWindow on use.
	readEvents []time.Time

	// Exhaustion (R9). exhausted is authoritative for the scheduler and the Feed;
	// exhaustReset is the resume instant, zero when none is derivable.
	exhausted    bool
	exhaustReset time.Time
}

// New returns a Governor wrapping base and injecting clk for all timing (R21).
// ADR-0012's sketch writes governor.New(base); the clock is the injection R21
// and ADR-0011's import table both require, so it is a constructor parameter here.
func New(base http.RoundTripper, clk clock.Clock) *Governor {
	return &Governor{
		base:          base,
		clk:           clk,
		writeRate:     documentedSafeRate,
		lastRemaining: -1,
	}
}

// RoundTrip paces writes (R2) and forwards the request to the network, then
// observes the rate-limit headers (R5) and accounts the exchange (R7) on the way
// back. A read is never paced (R4).
func (g *Governor) RoundTrip(req *http.Request) (*http.Response, error) {
	if isWrite(req.Method) {
		g.paceWrite()
	}
	resp, err := g.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	g.observe(req, resp)
	return resp, nil
}

// observe classifies the response (R14), reads R5's headers and applies R7's
// accounting. It reads the status straight off the wire, below the store's
// reconstitution, which is the only place a 304 still looks like a 304
// (ADR-0012). Classification runs before the lock because it may read the 403
// body, and always restores it for the consumer.
func (g *Governor) observe(req *http.Request, resp *http.Response) {
	limited := classify(resp)
	stampRateLimited(resp, limited)

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clk.Now()

	// R5: the headers arrive free on every response and describe the primary
	// limit. R6 treats them as authoritative over the governor's own tally.
	if n, ok := parseHeaderInt(resp.Header.Get("X-RateLimit-Remaining")); ok {
		g.lastRemaining = n
	}
	if n, ok := parseHeaderInt(resp.Header.Get("X-RateLimit-Reset")); ok {
		g.lastReset = n
	}

	// R11/R16: a read (GET, HEAD, OPTIONS) costs one point against the secondary
	// pool the write ceiling is computed from, whatever its status. Record it so
	// the ceiling subtracts the reads currently in flight.
	if isRead(req.Method) {
		g.readEvents = append(g.readEvents, now)
	}

	switch {
	case limited:
		// R12: back off hard on a rate-limit response, and never count it as a
		// clean one. R9/ADR-0018: a rate-limit classification publishes exhaustion.
		g.backoff()
		g.cleanStreak = 0
		g.enterRateLimit(resp, now)
	case resp.StatusCode == http.StatusNotModified:
		// R7: a conditional request returning 304 costs zero primary allowance.
		g.notModified++
		g.recordClean(req, now, false)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// R7: a 200 costs one primary point.
		g.primaryUsed++
		g.recordClean(req, now, true)
	default:
		// An authorization 403, a 404 or a 5xx is neither clean nor a rate limit.
		// It breaks the clean streak's consecutiveness without backing off, so a
		// stream of authorization 403s never ramps the write rate (open question 1).
		g.cleanStreak = 0
		g.exhausted = g.lastRemaining == 0
	}
}

// recordClean advances the ramp on a clean response and clears exhaustion when
// the headers say the allowance is not zero. primary reports whether the response
// consumed one primary point (R7): a 200 did, a 304 did not.
func (g *Governor) recordClean(req *http.Request, now time.Time, primary bool) {
	g.cleanStreak++
	if g.cleanStreak >= cleanBeforeRamp {
		g.ramp()
		g.cleanStreak = 0
	}
	g.exhausted = g.lastRemaining == 0
}

// enterRateLimit records a rate-limit classification. Exhaustion is published
// whatever the primary headers say (R9, ADR-0018), and the next write is pushed
// past the resume instant so the governor issues nothing until virtual time
// passes it (R12, AC4). The request itself is never a failure (R13): the consumer
// re-attempts it, which is the classification stamp's whole purpose (R14).
func (g *Governor) enterRateLimit(resp *http.Response, now time.Time) {
	g.exhausted = true
	// A Retry-After interval is the explicit backoff signal and is recorded as an
	// absolute instant (R12). Without one, resumeInstantLocked falls back to
	// x-ratelimit-reset, so exhaustReset is only stamped when Retry-After supplies
	// a resume the header cannot.
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			g.exhaustReset = now.Add(time.Duration(secs) * time.Second)
		}
	}
}

// resumeInstantLocked is the instant the account may issue again (R9): a
// Retry-After derived resume where one was recorded, otherwise x-ratelimit-reset,
// otherwise the zero time, which is exhaustion without a time rather than an
// invented one (AC10). The lastReset guard keeps 1970 out: time.Unix(0,0) is not
// the zero time, so a never-observed reset must not become the epoch.
func (g *Governor) resumeInstantLocked() time.Time {
	if !g.exhaustReset.IsZero() {
		return g.exhaustReset
	}
	if g.lastReset != 0 {
		return time.Unix(g.lastReset, 0)
	}
	return time.Time{}
}

// ramp is R10's additive increase, capped at R11's ceiling.
func (g *Governor) ramp() {
	g.writeRate += rampStep
	if g.writeRate > rampCeiling {
		g.writeRate = rampCeiling
	}
}

// backoff is R10's multiplicative decrease, floored at R11's floor.
func (g *Governor) backoff() {
	g.writeRate /= 2
	if g.writeRate < rampFloor {
		g.writeRate = rampFloor
	}
}

// isWrite reports whether a method is one of R2's paced writes. R16 prices these
// at the published 5-point method default. GET, HEAD and OPTIONS are reads: never
// paced (R4), priced at 1 point.
func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// isRead reports whether a method is priced as a 1-point read (R16). Anything
// that is not a read is a write for pricing; the method sets are disjoint over
// what this tool issues.
func isRead(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// dynamicCeilingLocked is R11's write ceiling in writes per second: the secondary
// pool less the observed read points, priced at 5 points a write, held within
// R11's floor and cap. With no reads it yields 3.0/sec and the 2.5 cap binds;
// with the Feed at 312 points/min it yields ~1.96/sec, which is where the wall
// sits when a Purge and the Feed share the pool.
func (g *Governor) dynamicCeilingLocked(now time.Time) float64 {
	reads := g.readPointsPerMinLocked(now)
	ceiling := (secondaryPool - reads) / (writePoints * readWindow.Seconds())
	if ceiling > rampCeiling {
		ceiling = rampCeiling
	}
	if ceiling < rampFloor {
		ceiling = rampFloor
	}
	return ceiling
}

// readPointsPerMinLocked is the read points observed in the trailing minute
// (R11). It prunes the window first, so an old burst stops depressing the ceiling
// once it ages out.
func (g *Governor) readPointsPerMinLocked(now time.Time) float64 {
	cutoff := now.Add(-readWindow)
	drop := 0
	for drop < len(g.readEvents) && !g.readEvents[drop].After(cutoff) {
		drop++
	}
	if drop > 0 {
		n := copy(g.readEvents, g.readEvents[drop:])
		g.readEvents = g.readEvents[:n]
	}
	return float64(len(g.readEvents)) * readPoints
}

// paceWrite blocks until this write's slot on the injected clock (R2, R21). It
// claims the slot under the lock, so concurrent writes take successive slots one
// interval apart (R3: one global throttle, whatever the repository count), then
// sleeps outside the lock. The slot never precedes an exhaustion resume, so a
// backed-off governor issues nothing until virtual time passes it (R9, AC9).
func (g *Governor) paceWrite() {
	g.mu.Lock()
	now := g.clk.Now()
	start := now
	if g.nextWrite.After(start) {
		start = g.nextWrite
	}
	// While exhausted, no write precedes the resume instant, so the governor
	// issues nothing until virtual time passes it (R9, AC9). This covers both a
	// rate-limit classification (a Retry-After resume) and a plain remaining:0
	// (the header reset), because resumeInstantLocked reads both.
	if g.exhausted {
		if resume := g.resumeInstantLocked(); resume.After(start) {
			start = resume
		}
	}
	g.nextWrite = start.Add(g.intervalLocked())
	wait := start.Sub(now)
	g.mu.Unlock()

	if wait > 0 {
		g.clk.Sleep(wait)
	}
}

// intervalLocked is the inter-write gap at the current effective rate.
func (g *Governor) intervalLocked() time.Duration {
	return time.Duration(float64(time.Second) / g.effectiveRateLocked())
}

// effectiveRateLocked is the rate writes are actually paced at: the ramp target
// held within R11's dynamic ceiling, which subtracts the observed read points, so
// a Purge slows for a busy Feed automatically (R11, ADR-0007). The ceiling
// already carries R11's floor and cap.
func (g *Governor) effectiveRateLocked() float64 {
	rate := g.writeRate
	if ceiling := g.dynamicCeilingLocked(g.clk.Now()); rate > ceiling {
		rate = ceiling
	}
	if rate < rampFloor {
		rate = rampFloor
	}
	return rate
}

// pressureLocked is R8a's projection: consumption is under pressure when the
// current burn would exhaust the remaining allowance before it resets. The burn
// window it reads is built in a later slice, so this reports no pressure until
// then.
func (g *Governor) pressureLocked(now time.Time) bool {
	_ = now
	return false
}

// PrimaryUsed reports the primary allowance the governor has accounted: one per
// 200 and zero per 304 (R7).
func (g *Governor) PrimaryUsed() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.primaryUsed
}

// Revalidations reports how many free 304s the governor has seen (R7). It exists
// so a caller can prove a conditional round trip stayed free.
func (g *Governor) Revalidations() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.notModified
}

// Remaining reports the last observed x-ratelimit-remaining, or -1 before any
// response. The header is authoritative over any local projection (R5, R6).
func (g *Governor) Remaining() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastRemaining
}

// Readout publishes the Budget Readout (ADR-0014), computed under the governor's
// lock and safe for any goroutine to call: rate-governor R8's "publish to any
// component that asks" is a getter, asked. Remaining and Reset are wired from the
// x-ratelimit-* headers observe already reads (R5). A zero Reset is R9 kept, not
// a bug: with no reset instant observed, lastReset stays 0 and Reset stays the
// zero time rather than an invented one. Pressure and Exhausted await burn
// tracking (stage 2) and stay false while burn is zero.
func (g *Governor) Readout() Readout {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clk.Now()

	return Readout{
		Remaining: int(g.lastRemaining),
		Reset:     g.resumeInstantLocked(),
		Pressure:  g.pressureLocked(now),
		Exhausted: g.exhausted,
	}
}

// WriteRate reports the rate writes are actually paced at, in writes per second:
// the AIMD ramp target (R10) held within R11's floor and cap. It is the observed
// ceiling AC2 and AC2a check, so it returns the effective value rather than the
// raw ramp target.
func (g *Governor) WriteRate() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.effectiveRateLocked()
}

func parseHeaderInt(v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
