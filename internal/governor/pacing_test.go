package governor_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// del issues one DELETE write through the governor and closes the body. It is
// safe to call from a goroutine: it reports failures with Errorf, never Fatal.
func del(t *testing.T, g *governor.Governor) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, "https://api.github.com/repos/o/r/actions/runs/1", nil)
	if err != nil {
		t.Errorf("build delete request: %v", err)
		return
	}
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Errorf("delete round trip: %v", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close delete body: %v", err)
	}
}

// TestColdStartPacing pins AC1: from cold the first write goes at once and the
// second follows one second later, because the documented-safe rate is 1.0/sec
// (R10). The interval is measured on the injected clock and the test never
// sleeps: it advances virtual time by exactly one second and observes the release.
func TestColdStartPacing(t *testing.T) {
	clk := baseClock()
	g := governor.New(respondWith(http.StatusNoContent, 4811, baseUnix+3600, ""), clk)

	t0 := clk.Now()
	del(t, g) // the first write is not spaced from any predecessor: it goes at once.
	if now := clk.Now(); !now.Equal(t0) {
		t.Fatalf("the first write advanced virtual time to %v; a cold write must not wait (AC1)", now)
	}

	issued := make(chan time.Time, 1)
	go func() {
		del(t, g)
		issued <- clk.Now()
	}()

	// Wait until the second write is parked on the clock.
	if err := clk.BlockUntilContext(context.Background(), 1); err != nil {
		t.Fatalf("block until the write parks: %v", err)
	}

	// Advance half the cold interval. At 2/sec the write would release here, so
	// confirming it is STILL parked pins AC1's 1s interval rather than merely
	// proving the write waits for some unspecified duration. A single Advance(1s)
	// would pass even at 2/sec, because a released writer reports the advanced now
	// whatever its real deadline was. When the writer is still parked (one waiter)
	// this returns at once and consumes no real time; a faster rate that had
	// already released leaves zero waiters, so the bounded context fails fast
	// instead of hanging.
	clk.Advance(500 * time.Millisecond)
	stillParked, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := clk.BlockUntilContext(stillParked, 1); err != nil {
		t.Fatalf("second write left its park before the 1s cold interval; a faster rate would release at 500ms (AC1): %v", err)
	}
	select {
	case at := <-issued:
		t.Fatalf("second write issued at %v, before the 1s cold interval; a 2/sec rate would release here (AC1)", at.Sub(t0))
	default:
	}

	// Advance the remaining half to the full second: the write releases at 1s.
	clk.Advance(500 * time.Millisecond)
	at := <-issued
	if d := at.Sub(t0); d != time.Second {
		t.Errorf("second write issued %v after the first, want exactly 1s at the cold-start 1.0/sec rate (AC1)", d)
	}
}
