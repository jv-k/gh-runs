package governor_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// getPath issues one GET to a cassette path and closes the body, returning the
// response for inspection first.
func getPath(t *testing.T, g *governor.Governor, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/"+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s: round trip: %v", path, err)
	}
	return resp
}

// TestForbiddenRateLimitBacksOff pins AC5 against a recorded 403 that carries
// rate-limit signals rather than the authorization shape: the issue rate falls,
// the request is not a transport error, and it is classified so the consumer
// re-attempts rather than failing (R12, R13, R14).
func TestForbiddenRateLimitBacksOff(t *testing.T) {
	g := governor.New(openCassette(t, "testdata/classification"), baseClock())

	for i := 0; i < 40; i++ {
		resp := getPath(t, g, "clean")
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}
	before := g.WriteRate()

	resp := getPath(t, g, "forbidden_ratelimit")
	if !governor.RateLimited(resp) {
		t.Errorf("the rate-limit 403 was not classified as a rate-limit response (R14, AC5)")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	if after := g.WriteRate(); after >= before {
		t.Errorf("issue rate did not fall after a rate-limit 403: before=%v after=%v (R12, AC5)", before, after)
	}
}

// TestSecondaryLimitWithDocURLBacksOff pins blocker 1 and AC5 against the shape
// a loose matcher misreads: a secondary-limit 403 carrying a documentation_url
// and no Retry-After. The doc URL points at the rate-limits page, not the called
// endpoint, so it is not the measured authorization shape and must classify as
// rate limiting. If it were read as authorization the governor would not back off
// and would keep hammering a live secondary limit, which is the account block
// this component exists to prevent (open question 1, R12, R14, AC5).
func TestSecondaryLimitWithDocURLBacksOff(t *testing.T) {
	g := governor.New(openCassette(t, "testdata/classification"), baseClock())

	for i := 0; i < 40; i++ {
		resp := getPath(t, g, "clean")
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}
	before := g.WriteRate()

	resp := getPath(t, g, "repos/o/r/actions/runs")
	if !governor.RateLimited(resp) {
		t.Errorf("a secondary-limit 403 carrying a documentation_url was not classified as a rate-limit response; the governor would hammer a live limit (open question 1, R14, AC5)")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	if after := g.WriteRate(); after >= before {
		t.Errorf("issue rate did not fall after a secondary-limit 403 with a documentation_url: before=%v after=%v (R12, AC5)", before, after)
	}
}

// TestInterleavedReadDoesNotDefeatRetryAfter pins blocker 2 and AC4 in the
// normal case. A write trips the secondary limit with Retry-After 60, then a
// single clean Feed poll interleaves. purge R14 keeps the Feed polling during a
// Purge, so this interleaved clean read is the normal case, not an edge. The
// clean read carries a healthy primary remaining, but it must not release the
// Retry-After hold: exhaustion survives and the re-attempt still waits the full
// 60s. A hold gated behind a bool that a clean primary header clears would issue
// at the ramp interval instead, which is the account block R12 exists to prevent.
func TestInterleavedReadDoesNotDefeatRetryAfter(t *testing.T) {
	clk := baseClock()
	g := governor.New(openCassette(t, "testdata/retry_after_interleaved"), clk)
	t0 := clk.Now()

	// The write trips the secondary limit.
	req, err := http.NewRequest(http.MethodDelete, "https://api.github.com/repos/o/r/actions/runs/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatalf("a rate limit surfaced as a transport error; it is never a failure (R13): %v", err)
	}
	if !governor.RateLimited(resp) {
		t.Errorf("the 429 was not classified as a rate-limit response (R14)")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if want := t0.Add(60 * time.Second); !g.Readout().Reset.Equal(want) {
		t.Fatalf("Readout.Reset = %v, want %v derived from Retry-After: 60 (R9)", g.Readout().Reset, want)
	}

	// The interleaved clean Feed poll: the normal case during a Purge (purge R14).
	clean := getPath(t, g, "repos/o/r/actions/runs")
	if err := clean.Body.Close(); err != nil {
		t.Fatalf("close clean body: %v", err)
	}

	// The clean read must not release the rate-limit hold. This is the crux of
	// blocker 2: exhaustion is derived from the hold's own deadline, not from a
	// primary-remaining flag a healthy header clears.
	if !g.Readout().Exhausted {
		t.Errorf("an interleaved clean read cleared exhaustion during the Retry-After hold (blocker 2, R9, ADR-0018)")
	}

	// The re-attempt must not issue until the full 60s Retry-After has elapsed,
	// even though a healthy primary remaining was just observed on the clean read.
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
		t.Fatalf("re-attempt issued %v after the limit, before the 60s Retry-After elapsed despite an interleaved clean read (blocker 2, AC4)", at.Sub(t0))
	default:
	}
	clk.Advance(1 * time.Second)
	at := <-issued
	if d := at.Sub(t0); d != 60*time.Second {
		t.Errorf("re-attempt issued %v after the limit, want exactly 60s; the clean read must not shorten the hold (blocker 2, AC4)", d)
	}
}

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
