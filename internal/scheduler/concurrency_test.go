package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
)

// TestConcurrencyIsBoundedByLimiter is AC15 and R17/R18. Given many more
// repositories than the bound, at no instant are more than limiter.Bound polls in
// flight on the wire, and the bound is the transport limiter's, not a
// scheduler-private semaphore (ADR-0018): the scheduler spawns a goroutine per due
// poll and the limiter, innermost in the chain, caps the wire. The test holds the
// pool saturated and the peak it observes at the base is exactly the bound.
func TestConcurrencyIsBoundedByLimiter(t *testing.T) {
	const repos = 30
	base := &gatedBaseRT{
		entered: make(chan struct{}, repos),
		release: make(chan struct{}),
	}
	ids := make([]domain.RepoID, repos)
	for i := range ids {
		ids[i] = gh("acme", fmt.Sprintf("r%d", i))
	}
	h := newHarness(t, harnessConfig{base: base, pollSet: ids})
	h.start(t)

	// The cold start makes every repository due at once, so the loop spawns a poll
	// per repository. Wait for the bound's worth to reach the wire, releasing none:
	// the rest wait on the limiter's semaphore, so this read blocks at the bound.
	for range limiter.Bound {
		<-base.entered
	}
	saturated := base.peak()

	// Release everything and let the cold-start polls finish.
	go func() {
		for range repos {
			base.release <- struct{}{}
		}
	}()
	h.waitPolls(t, repos)

	if saturated != limiter.Bound {
		t.Errorf("peak polls in flight = %d, want exactly %d (bounded by the limiter, genuinely concurrent)", saturated, limiter.Bound)
	}
	if final := base.peak(); final > limiter.Bound {
		t.Errorf("peak over the whole run = %d, exceeded the limiter's bound of %d", final, limiter.Bound)
	}
}

// TestStopIsIdempotent is the double-Stop guard. A second Stop must be a safe no-op:
// without the guard it closes the already-closed Updates channel and panics. The Feed
// owns quit (ADR-0015), so a defensive second Stop from a shutdown path must not crash
// the process.
func TestStopIsIdempotent(t *testing.T) {
	clk := clockwork.NewFakeClockAt(t0)
	s := New(Options{Clock: clk})
	s.Start(t.Context())
	s.Stop()
	s.Stop() // must not panic on the second close
}

// TestStopUnwindsInFlightPoll is ADR-0018's quit contract. Stop cancels the root
// context, an in-flight poll completes rather than being abandoned, the workers
// unwind through the WaitGroup, and the engine closes its Updates channel. The test
// holds a poll in flight, calls Stop, and proves it returns only once the poll is
// released and that Updates is then closed.
func TestStopUnwindsInFlightPoll(t *testing.T) {
	base := &gatedBaseRT{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	h := newHarness(t, harnessConfig{base: base, pollSet: []domain.RepoID{gh("acme", "a")}})
	h.start(t)

	<-base.entered // the cold-start poll is on the wire, blocked in the base

	done := make(chan struct{})
	go func() {
		h.stop()
		close(done)
	}()

	// Stop is waiting on the in-flight poll: it must not have returned yet.
	select {
	case <-done:
		t.Fatal("Stop returned before the in-flight poll completed")
	case <-time.After(50 * time.Millisecond):
	}

	// Release the poll; Stop then unwinds.
	base.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung after the in-flight poll was released")
	}

	if _, ok := <-h.s.Updates(); ok {
		t.Error("Updates channel was not closed after Stop")
	}
}
