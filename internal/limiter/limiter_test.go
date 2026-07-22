package limiter_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/limiter"
)

// gatedBase is an instrumented base RoundTripper. It records the peak number of
// RoundTrip calls concurrently in flight and blocks each one on release, so a test
// saturates the limiter and reads the peak deterministically, sleeping through
// nothing. It is the wire the limiter sits directly above.
type gatedBase struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int

	entered chan struct{}
	release chan struct{}
}

func (g *gatedBase) RoundTrip(_ *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.maxSeen {
		g.maxSeen = g.inFlight
	}
	g.mu.Unlock()

	g.entered <- struct{}{}
	<-g.release

	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func (g *gatedBase) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
}

// TestNoMoreThanBoundInFlight is AC15: at no instant are more than Bound RoundTrips
// concurrently in flight through the limiter, under any pressure. It launches many
// more calls than the bound, saturates the pool, reads the peak, then drains,
// releasing one at a time so the pool stays full. The peak is exactly the bound:
// the limiter reached it (genuinely concurrent) and never exceeded it (bounded). It
// is deterministic and uses no real sleep.
func TestNoMoreThanBoundInFlight(t *testing.T) {
	const calls = 30
	base := &gatedBase{
		entered: make(chan struct{}, calls),
		release: make(chan struct{}),
	}
	lim := limiter.New(base, limiter.Bound)

	var wg sync.WaitGroup
	wg.Add(calls)
	for range calls {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/x", nil)
			resp, err := lim.RoundTrip(req)
			if err == nil && resp != nil {
				_ = resp.Body.Close()
			}
		}()
	}

	// Saturate: wait for exactly Bound calls to reach the base and block there. With
	// more calls than the bound, the rest wait on the limiter's semaphore and never
	// reach the base, so this read blocks at the bound and never past it.
	for range limiter.Bound {
		<-base.entered
	}
	saturated := base.peak()

	// Drain: release calls one at a time and let every goroutine finish. As each base
	// call returns, the limiter frees a slot and one waiting goroutine takes it, so
	// the pool holds at the bound and the peak never rises above it.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case base.release <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	wg.Wait()
	close(done)

	if saturated != limiter.Bound {
		t.Errorf("peak in flight at saturation = %d, want exactly %d (bounded and genuinely concurrent)", saturated, limiter.Bound)
	}
	if final := base.peak(); final > limiter.Bound {
		t.Errorf("peak over the whole run = %d, exceeded the bound of %d", final, limiter.Bound)
	}
}

// blockingBase blocks every RoundTrip until its context is cancelled, so a test can
// fill the limiter's slots and then prove a waiter honours cancellation.
type blockingBase struct {
	entered chan struct{}
}

func (b *blockingBase) RoundTrip(req *http.Request) (*http.Response, error) {
	b.entered <- struct{}{}
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// TestWaiterHonoursContextCancellation proves the limiter releases a blocked waiter
// when its request's context is cancelled, rather than parking it behind a
// saturated pool (ADR-0018's quit context). It fills every slot, then issues one
// more request whose context it cancels, and that request returns the context error
// without ever reaching the base.
func TestWaiterHonoursContextCancellation(t *testing.T) {
	base := &blockingBase{entered: make(chan struct{}, limiter.Bound)}
	lim := limiter.New(base, limiter.Bound)

	// Fill every slot with requests that block in the base on a context we hold open.
	fillCtx, cancelFill := context.WithCancel(context.Background())
	defer cancelFill()
	var wg sync.WaitGroup
	wg.Add(limiter.Bound)
	for range limiter.Bound {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/x", nil)
			resp, err := lim.RoundTrip(req.WithContext(fillCtx))
			if err == nil && resp != nil {
				_ = resp.Body.Close()
			}
		}()
	}
	for range limiter.Bound {
		<-base.entered
	}

	// One more request cannot get a slot. Cancelling its context must free it.
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/blocked", nil)
		resp, err := lim.RoundTrip(req.WithContext(waiterCtx))
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
		result <- err
	}()

	cancelWaiter()
	if err := <-result; err == nil {
		t.Fatal("a waiter blocked on a saturated pool returned nil after its context was cancelled, want the context error")
	}

	// Release the slot-holders and let them unwind.
	cancelFill()
	wg.Wait()
}
