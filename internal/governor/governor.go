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

// AIMD limits from rate-governor R10 and R11. The ramp starts at the
// documented-safe rate, adds a step per clean streak, halves on a rate-limit
// response, and is held between the floor and the dynamic ceiling's cap.
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

	// R8a's burn window. Burn is the primary allowance consumed over the last five
	// minutes, divided by the full five minutes and never by the elapsed part, so
	// a cold-start burst amortises and stays silent where a one-minute window
	// would fire on it. The width is a constant, chosen not tuned.
	burnWindow = 5 * time.Minute
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

// Governor is the single authority for Budget accounting and write throughput
// (R1, R2). It observes every response's rate-limit headers, paces writes on the
// injected clock against an AIMD ramp under a dynamic ceiling, classifies rate
// limits, and publishes the Budget Readout with R8a's pressure projection.
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

	// burnEvents holds the instant of each primary point consumed in the trailing
	// five minutes (R8a): one per non-304 response, zero per 304 (R7). Pressure is
	// projected from its count. Pruned to burnWindow on use.
	burnEvents []time.Time

	// Exhaustion (R9). A rate-limit classification and a primary remaining:0 are
	// two different lifecycles that once shared one bool, which let an interleaved
	// clean read clear a Retry-After hold (blocker 2). They are separated here.
	//
	// inRateLimit is set on a rate-limit classification and stays set until a clean
	// response confirms recovery, but only once limitResume has passed: a clean
	// primary header arriving mid-hold must not release it. limitResume is the
	// hold's own deadline (Retry-After, else x-ratelimit-reset), zero when the
	// limit gave no time (AC10) or no hold is active. Primary exhaustion is derived
	// from lastRemaining rather than stored, so a recovering header clears it free.
	inRateLimit bool
	limitResume time.Time
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
		g.paceWrite(req)
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
	limited := classify(req, resp)
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
		// R7: a conditional request returning 304 costs zero primary allowance and
		// feeds no burn.
		g.notModified++
		g.recordClean(now)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// R7/R8a: a 200 costs one primary point, and burn tracks the same 2xx basis
		// as primaryUsed. Counting a 403, 429 or 5xx toward burn would over-report
		// it and bias the pressure projection, past R8a's cited R7 arithmetic.
		g.primaryUsed++
		g.burnEvents = append(g.burnEvents, now)
		g.recordClean(now)
	default:
		// An authorization 403, a 404 or a 5xx is neither clean nor a rate limit.
		// It breaks the clean streak's consecutiveness without backing off, so a
		// stream of authorization 403s never ramps the write rate (open question 1),
		// and it feeds no burn. Exhaustion is derived from lastRemaining, so a
		// remaining:0 here still reads as exhausted without a stored flag.
		g.cleanStreak = 0
	}

	// Prune both windows on append so their growth tracks window occupancy however
	// often a getter is polled: a headless Purge that never reads the Readout must
	// not leak one time.Time per request across a multi-hour run.
	g.readEvents = pruneBefore(g.readEvents, now.Add(-readWindow))
	g.burnEvents = pruneBefore(g.burnEvents, now.Add(-burnWindow))
}

// recordClean advances the ramp on a clean response (R10) and clears a rate-limit
// hold once recovery is genuinely confirmed. Recovery is confirmed only after the
// hold's own deadline has passed (blocker 2): a clean primary header arriving
// mid-hold must not release a Retry-After the limit itself set, so the clear is
// gated on limitResume. Clearing it here stops the Readout reporting a stale past
// resume instant once recovery is real.
func (g *Governor) recordClean(now time.Time) {
	g.cleanStreak++
	if g.cleanStreak >= cleanBeforeRamp {
		g.ramp()
		g.cleanStreak = 0
	}
	if g.inRateLimit && (g.limitResume.IsZero() || !now.Before(g.limitResume)) {
		g.inRateLimit = false
		g.limitResume = time.Time{}
	}
}

// enterRateLimit records a rate-limit classification. Exhaustion is published
// whatever the primary headers say (R9, ADR-0018) through the hold this sets, and
// the next write is pushed past the resume instant so the governor issues nothing
// until virtual time passes it (R12, AC4). The resume instant is the Retry-After
// interval where one is supplied, else x-ratelimit-reset, else none (R9). paceWrite
// gates the write hold on limitResume alone, independent of the primary header, so
// an interleaved clean read cannot release it (blocker 2). The request itself is
// never a failure (R13): the consumer re-attempts it, which is the classification
// stamp's whole purpose (R14).
func (g *Governor) enterRateLimit(resp *http.Response, now time.Time) {
	g.inRateLimit = true
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			g.limitResume = now.Add(time.Duration(secs) * time.Second)
			return
		}
	}
	if g.lastReset != 0 {
		g.limitResume = time.Unix(g.lastReset, 0)
		return
	}
	g.limitResume = time.Time{} // AC10: exhausted, with no time to resume from.
}

// resumeInstantLocked is the instant the account may issue again (R9): the active
// rate-limit hold's deadline where one is still in the future, otherwise the
// primary x-ratelimit-reset, otherwise the zero time, which is exhaustion without
// a time rather than an invented one (AC10). A hold deadline already in the past
// is stale and falls through to the current primary reset, so the Readout stops
// reporting it once recovery is due. The lastReset guard keeps 1970 out:
// time.Unix(0,0) is not the zero time, so a never-observed reset must not become
// the epoch.
func (g *Governor) resumeInstantLocked(now time.Time) time.Time {
	if !g.limitResume.IsZero() && now.Before(g.limitResume) {
		return g.limitResume
	}
	if g.lastReset != 0 {
		return time.Unix(g.lastReset, 0)
	}
	return time.Time{}
}

// exhaustedLocked reports whether the Readout is exhausted (R9): a primary
// remaining of zero, or an active rate-limit classification. The rate-limit case
// stays exhausted while its hold is active, or where the limit gave no time to
// wait for (AC10). It is derived rather than stored so a recovering primary header
// clears the primary case for free, and so an interleaved clean read cannot clear
// the rate-limit case before its deadline (blocker 2).
func (g *Governor) exhaustedLocked(now time.Time) bool {
	if g.lastRemaining == 0 {
		return true
	}
	if g.inRateLimit {
		return g.limitResume.IsZero() || now.Before(g.limitResume)
	}
	return false
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
	g.readEvents = pruneBefore(g.readEvents, now.Add(-readWindow))
	return float64(len(g.readEvents)) * readPoints
}

// pruneBefore drops the leading elements of events at or before cutoff, compacting
// in place, and returns the retained slice. The read window and the burn window
// prune identically, so one helper serves both, and observe calls it on append to
// bound growth regardless of getter cadence.
func pruneBefore(events []time.Time, cutoff time.Time) []time.Time {
	drop := 0
	for drop < len(events) && !events[drop].After(cutoff) {
		drop++
	}
	if drop > 0 {
		n := copy(events, events[drop:])
		events = events[:n]
	}
	return events
}

// paceWrite blocks until this write's slot on the injected clock (R2, R21). It
// claims the slot under the lock, so concurrent writes take successive slots one
// interval apart (R3: one global throttle, whatever the repository count), then
// waits outside the lock. No write precedes a hold instant, so a backed-off
// governor issues nothing until virtual time passes it (R9, AC4, AC9). Two holds
// apply and are honoured independently:
//
//   - a rate-limit hold (blocker 2), gated purely on limitResume's own deadline,
//     so an interleaved clean read that observes a healthy primary remaining
//     cannot shorten it;
//   - a primary-exhaustion hold at remaining:0, gated on the current header, so a
//     recovering primary remaining releases it. The two are different policies.
//
// It honours req.Context() cancellation so a cancelled Purge stops waiting out its
// pacing interval rather than parking to the deadline.
func (g *Governor) paceWrite(req *http.Request) {
	g.mu.Lock()
	now := g.clk.Now()
	start := now
	if g.nextWrite.After(start) {
		start = g.nextWrite
	}
	// The rate-limit hold survives interleaved clean reads: it is gated on the
	// deadline the limit itself supplied, not on the primary-remaining flag a clean
	// header would clear (blocker 2, R12, AC4).
	if !g.limitResume.IsZero() && start.Before(g.limitResume) {
		start = g.limitResume
	}
	// Primary exhaustion holds until the primary reset, and a recovering header
	// releases it (R9, AC9).
	if g.lastRemaining == 0 && g.lastReset != 0 {
		if reset := time.Unix(g.lastReset, 0); start.Before(reset) {
			start = reset
		}
	}
	g.nextWrite = start.Add(g.intervalLocked())
	wait := start.Sub(now)
	g.mu.Unlock()

	if wait > 0 {
		timer := g.clk.NewTimer(wait)
		select {
		case <-timer.Chan():
		case <-req.Context().Done():
			timer.Stop()
		}
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
// current burn would exhaust the remaining allowance before it resets, and by no
// other test. remaining / burn is the seconds until exhaustion at the current
// rate; pressure is that being shorter than the time to the reset. Where burn is
// zero the governor reports no pressure and evaluates no quotient, because nothing
// is projected to run out at a rate of nothing. A compiled percentage of the
// limit appears nowhere: the same burst is fine on a healthy remaining and doomed
// on a low one, which is exactly what a percentage cannot see (ADR-0007).
func (g *Governor) pressureLocked(now time.Time) bool {
	if g.lastRemaining < 0 {
		// No x-ratelimit-remaining has been observed, so there is nothing to
		// project. A missing header must not force a false pressure reading.
		return false
	}
	burn := g.burnPerSecondLocked(now)
	if burn == 0 {
		return false
	}
	if g.lastReset == 0 {
		return false
	}
	timeToReset := time.Unix(g.lastReset, 0).Sub(now).Seconds()
	if timeToReset <= 0 {
		return false
	}
	return float64(g.lastRemaining)/burn < timeToReset
}

// burnPerSecondLocked is the primary allowance consumed over the trailing five
// minutes divided by the full five minutes (R8a), never by the elapsed part: the
// under-report in the first five minutes of a session is the safe direction, when
// the failure R29 names is a readout nobody believes.
func (g *Governor) burnPerSecondLocked(now time.Time) float64 {
	g.burnEvents = pruneBefore(g.burnEvents, now.Add(-burnWindow))
	return float64(len(g.burnEvents)) / burnWindow.Seconds()
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
// zero time rather than an invented one (AC10). Pressure is R8a's projection over
// the burn window, false while burn is zero, and Exhausted covers both a primary
// remaining:0 and an active rate-limit hold (R9, ADR-0018), the latter surviving
// interleaved clean reads until its own deadline (blocker 2).
func (g *Governor) Readout() Readout {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clk.Now()

	return Readout{
		Remaining: int(g.lastRemaining),
		Reset:     g.resumeInstantLocked(now),
		Pressure:  g.pressureLocked(now),
		Exhausted: g.exhaustedLocked(now),
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
