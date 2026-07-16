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
)

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
	writeRate     float64 // R10: current AIMD write rate, per second.
	cleanStreak   int     // R10: consecutive clean responses toward the next increase.
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

// RoundTrip forwards the request to the network, then observes the rate-limit
// headers (R5) and accounts the exchange (R7) on the way back.
func (g *Governor) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := g.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	g.observe(resp)
	return resp, nil
}

// observe reads R5's headers and applies R7's accounting. It reads the status
// straight off the wire, below the store's reconstitution, which is the only
// place a 304 still looks like a 304 (ADR-0012).
func (g *Governor) observe(resp *http.Response) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// R5: the headers arrive free on every response and describe the primary
	// limit. R6 treats them as authoritative over the governor's own tally.
	if n, ok := parseHeaderInt(resp.Header.Get("X-RateLimit-Remaining")); ok {
		g.lastRemaining = n
	}
	if n, ok := parseHeaderInt(resp.Header.Get("X-RateLimit-Reset")); ok {
		g.lastReset = n
	}

	switch {
	case resp.StatusCode == http.StatusNotModified:
		// R7: a conditional request returning 304 costs zero primary allowance.
		g.notModified++
		g.cleanStreak++
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// R12: back off hard on a rate-limit response, and never count it as a
		// clean one. Discrimination by body shape (open question 1) is a stage-2
		// concern; the floor drives only clean reads.
		g.backoff()
		g.cleanStreak = 0
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// R7: a 200 costs one primary point.
		g.primaryUsed++
		g.cleanStreak++
		if g.cleanStreak >= cleanBeforeRamp {
			g.ramp()
			g.cleanStreak = 0
		}
	}
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

// WriteRate reports the current AIMD write rate in writes per second (R10).
func (g *Governor) WriteRate() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writeRate
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
