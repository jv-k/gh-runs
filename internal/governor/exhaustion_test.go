package governor_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestExhaustionWithTime pins AC9: given x-ratelimit-remaining: 0 and a reset
// instant, the governor publishes exhaustion with that resumption time and issues
// no write until virtual time passes it. Exhaustion here arrives on an ordinary
// 200 whose header simply reports nothing left, not on a rate-limit response.
func TestExhaustionWithTime(t *testing.T) {
	clk := baseClock()
	const resetAt = baseUnix + 1800 // the reset is 30 minutes out
	g := governor.New(respondWith(http.StatusOK, 0, resetAt, "{}"), clk)
	t0 := clk.Now()

	getN(t, g, 1) // a read observes remaining: 0

	ro := g.Readout()
	if !ro.Exhausted {
		t.Errorf("remaining:0 did not publish exhaustion (R9)")
	}
	if ro.Remaining != 0 {
		t.Errorf("Readout.Remaining = %d, want 0 (R5, R6)", ro.Remaining)
	}
	if want := time.Unix(resetAt, 0); !ro.Reset.Equal(want) {
		t.Errorf("Readout.Reset = %v, want %v from x-ratelimit-reset (R9, AC9)", ro.Reset, want)
	}

	issued := make(chan time.Time, 1)
	go func() {
		del(t, g)
		issued <- clk.Now()
	}()
	if err := clk.BlockUntilContext(context.Background(), 1); err != nil {
		t.Fatalf("block until the write parks: %v", err)
	}
	clk.Advance((resetAt - baseUnix - 1) * time.Second) // to one second before the reset
	select {
	case at := <-issued:
		t.Fatalf("write issued at %v, before the reset instant; an exhausted governor issues nothing (AC9)", at.Sub(t0))
	default:
	}
	clk.Advance(1 * time.Second) // to the reset instant
	at := <-issued
	if want := time.Unix(resetAt, 0); !at.Equal(want) {
		t.Errorf("write issued at %v, want at the reset instant %v (AC9)", at, want)
	}
}

// TestExhaustionWithoutTime pins AC10: a rate-limit response carrying neither a
// reset header nor a Retry-After publishes exhaustion with no time. The governor
// must not synthesise one.
func TestExhaustionWithoutTime(t *testing.T) {
	g := governor.New(respondNoReset(http.StatusForbidden, 4976, ""), baseClock())

	getN(t, g, 1) // a 403 with no authorization shape, no reset, no Retry-After

	ro := g.Readout()
	if !ro.Exhausted {
		t.Errorf("a rate-limit classification did not publish exhaustion (R9, ADR-0018)")
	}
	if !ro.Reset.IsZero() {
		t.Errorf("Readout.Reset = %v, want the zero time; the governor must not invent a resume (R9, AC10)", ro.Reset)
	}
}
