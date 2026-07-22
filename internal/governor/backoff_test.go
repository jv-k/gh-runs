package governor_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestBackoffWaitsRetryAfter pins AC4 against a recorded 429. A write trips the
// secondary limit; the governor honours the Retry-After (R12) by issuing no
// further write until virtual time has advanced the full 60 seconds, publishes
// exhaustion with that resume instant (R9), never returns the limit as a
// transport error, and classifies it as a rate-limit response so the consumer
// re-attempts rather than failing (R13, R14).
func TestBackoffWaitsRetryAfter(t *testing.T) {
	clk := baseClock()
	g := governor.New(openCassette(t, "testdata/retry_after"), clk)
	t0 := clk.Now()

	req1, err := http.NewRequest(http.MethodDelete, "https://api.github.com/repos/o/r/actions/runs/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp1, err := g.RoundTrip(req1)
	if err != nil {
		t.Fatalf("a rate limit surfaced as a transport error; it is never a failure (R13): %v", err)
	}
	if !governor.RateLimited(resp1) {
		t.Errorf("the 429 was not classified as a rate-limit response (R14, AC6)")
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	if err := resp1.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	ro := g.Readout()
	if !ro.Exhausted {
		t.Errorf("Readout not exhausted after a rate-limit response (R9, ADR-0018)")
	}
	if want := t0.Add(60 * time.Second); !ro.Reset.Equal(want) {
		t.Errorf("Readout.Reset = %v, want %v derived from Retry-After: 60 (R9, R12)", ro.Reset, want)
	}

	// The re-attempt must not issue until the 60s interval has fully elapsed.
	issued := make(chan time.Time, 1)
	go func() {
		del(t, g)
		issued <- clk.Now()
	}()
	if err := clk.BlockUntilContext(context.Background(), 1); err != nil {
		t.Fatalf("block until the re-attempt parks: %v", err)
	}
	clk.Advance(59 * time.Second)
	select {
	case at := <-issued:
		t.Fatalf("re-attempt issued %v after the limit, before the 60s Retry-After elapsed (AC4)", at.Sub(t0))
	default:
	}
	clk.Advance(1 * time.Second)
	at := <-issued
	if d := at.Sub(t0); d != 60*time.Second {
		t.Errorf("re-attempt issued %v after the limit, want exactly 60s (AC4)", d)
	}
}
