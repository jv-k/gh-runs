package governor_test

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// delTo issues one DELETE to a specific URL, closing the body. Safe from a
// goroutine: it reports with Errorf, never Fatal.
func delTo(t *testing.T, g *governor.Governor, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
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

// TestOneGlobalThrottle pins R3 and AC12: the throttle is account-scoped, not per
// repository. A write to a second repository waits behind the first write's slot,
// so the aggregate rate does not scale with repository count.
func TestOneGlobalThrottle(t *testing.T) {
	clk := baseClock()
	g := governor.New(respondWith(http.StatusNoContent, 4811, baseUnix+3600, ""), clk)
	t0 := clk.Now()

	delTo(t, g, "https://api.github.com/repos/o/a/actions/runs/1") // repo A, issued at once

	issued := make(chan time.Time, 1)
	go func() {
		delTo(t, g, "https://api.github.com/repos/o/b/actions/runs/2") // repo B
		issued <- clk.Now()
	}()
	if err := clk.BlockUntilContext(context.Background(), 1); err != nil {
		t.Fatalf("block until the second write parks: %v", err)
	}
	select {
	case at := <-issued:
		t.Fatalf("a write to a second repo issued at %v without waiting; the throttle must be global (R3, AC12)", at.Sub(t0))
	default:
	}
	clk.Advance(1 * time.Second)
	at := <-issued
	if d := at.Sub(t0); d != time.Second {
		t.Errorf("cross-repo write spacing = %v, want 1s; the rate does not scale with repo count (R3, AC12)", d)
	}
}

// TestPurgeAtReferenceScale pins AC3: a cassette of 18,258 successful DELETEs
// completes in milliseconds of real time while virtual time advances across the
// run, and no 60s window of virtual time spends more than ~900 points. With no
// reads in flight the writes cap at 2.5/sec (150/min, 750 points/min), so the
// robust invariant is at most 180 writes in any 60s window. The advancer steps
// virtual time in small quanta whenever the writer parks, so nothing sleeps.
func TestPurgeAtReferenceScale(t *testing.T) {
	const runs = 18258
	clk := baseClock()
	g := governor.New(openCassette(t, "testdata/bulk_delete"), clk)

	issued := make([]time.Time, 0, runs)
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer cancel()
		for i := 0; i < runs; i++ {
			req, err := http.NewRequest(http.MethodDelete, "https://api.github.com/repos/o/r/actions/runs/1", nil)
			if err != nil {
				t.Errorf("delete %d: build request: %v", i, err)
				return
			}
			resp, err := g.RoundTrip(req)
			if err != nil {
				t.Errorf("delete %d: %v", i, err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			if err := resp.Body.Close(); err != nil {
				t.Errorf("delete %d: close body: %v", i, err)
				return
			}
			mu.Lock()
			issued = append(issued, clk.Now())
			mu.Unlock()
		}
	}()

	const quantum = 50 * time.Millisecond
	for {
		if err := clk.BlockUntilContext(ctx, 1); err != nil {
			break // the writer finished and cancelled the context
		}
		clk.Advance(quantum)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(issued) != runs {
		t.Fatalf("only %d of %d writes completed (R20, AC3)", len(issued), runs)
	}
	if span := issued[len(issued)-1].Sub(issued[0]); span < time.Hour {
		t.Errorf("the Purge spanned %v of virtual time; at reference scale it runs for hours (R20, AC3)", span)
	}
	assertNoWindowExceeds(t, issued, time.Minute, 180)
}

// assertNoWindowExceeds fails if any window-wide span of the monotonic issue
// times holds more than maxCount writes. A DELETE is 5 points, so 180 writes is
// the ~900-point budget (AC3, AC2).
func assertNoWindowExceeds(t *testing.T, times []time.Time, window time.Duration, maxCount int) {
	t.Helper()
	worst := 0
	j := 0
	for i := range times {
		if j < i {
			j = i
		}
		for j < len(times) && times[j].Sub(times[i]) < window {
			j++
		}
		if count := j - i; count > worst {
			worst = count
		}
	}
	if worst > maxCount {
		t.Fatalf("busiest %v window held %d writes (%d points), over the ~900-point budget (AC3, AC2)", window, worst, worst*5)
	}
	t.Logf("busiest %v window held %d writes (%d points), within the ~900-point budget", window, worst, worst*5)
}
