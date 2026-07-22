package discovery_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// gatedRequester is a fake transport for the incremental-publication property
// (AC12), which is about how discovery folds a probe result back to its consumer
// rather than about the wire. The fidelity properties are proved against cassettes;
// this fake exists only to make probe timing observable and controllable. Every
// probe blocks on release, so a test holds a known number in flight and asserts the
// progressive emission directly.
type gatedRequester struct {
	enumBody string
	runsBody string

	mu       sync.Mutex
	inFlight int
	maxSeen  int

	entered chan struct{}
	release chan struct{}
}

func (g *gatedRequester) Request(_ string, path string, _ io.Reader) (*http.Response, error) {
	// Only the probe is gated. Enumeration (user/repos) returns at once so the
	// fan-out under observation is the probe burst alone.
	if !strings.Contains(path, "/actions/runs") {
		return fakeResp(g.enumBody), nil
	}
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
	return fakeResp(g.runsBody), nil
}

// fakeResp builds a 200 carrying body, the shape ghclient.Request would return.
func fakeResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// enumBody builds a one-page enumeration payload of n repositories.
func enumBody(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"repo%d","full_name":"jv-k/repo%d","owner":{"login":"jv-k"},"permissions":{"push":true},"archived":false,"disabled":false}`, i, i)
	}
	b.WriteString("]")
	return b.String()
}

const hasRunsBody = `{"total_count":1,"workflow_runs":[{"id":1,"status":"completed"}]}`

// gatedBaseRT is an instrumented base RoundTripper for AC13. It records the peak
// number of probe requests concurrently in flight at the wire and blocks each on
// release. It sits at the very bottom of the transport chain, so the concurrency it
// measures is the concurrency the limiter admits, which is the point: the bound is
// the transport limiter's, not a discovery-private semaphore. Enumeration returns at
// once; only the probe burst is gated and measured.
type gatedBaseRT struct {
	enumBody string
	runsBody string

	mu       sync.Mutex
	inFlight int
	maxSeen  int

	entered chan struct{}
	release chan struct{}
}

func (g *gatedBaseRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Path, "/actions/runs") {
		return jsonResp(g.enumBody), nil
	}
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
	return jsonResp(g.runsBody), nil
}

func (g *gatedBaseRT) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
}

// jsonResp builds a 200 with a JSON body, the shape the wire returns to the store.
func jsonResp(body string) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/2.0",
		ProtoMajor:    2,
		ProtoMinor:    0,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// TestProbeConcurrencyIsBoundedByLimiter is AC13. Given many more repositories than
// the bound, at no instant are more than limiter.Bound probes in flight on the wire,
// and the bound is enforced by the transport-chain limiter rather than by discovery
// itself (ADR-0018): discovery spawns a goroutine per probe and the limiter, nested
// innermost exactly as main.go nests it, caps the wire. The test releases probes one
// at a time so the pool stays saturated, and the peak in-flight count it observes at
// the base is exactly the bound: bounded, and genuinely concurrent rather than
// serial. No user-facing setting alters the bound: nothing in Options touches it.
func TestProbeConcurrencyIsBoundedByLimiter(t *testing.T) {
	const repos = 30
	base := &gatedBaseRT{
		enumBody: enumBody(repos),
		runsBody: hasRunsBody,
		entered:  make(chan struct{}, repos),
		release:  make(chan struct{}),
	}

	// Assemble the production chain with the limiter innermost, exactly as main.go
	// nests it (ADR-0018): store over governor over limiter over the base.
	clk := clockwork.NewFakeClock()
	chain := store.NewTransport(governor.New(limiter.New(base, limiter.Bound), clk), t.TempDir(), clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: testToken, Transport: chain})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	d := discovery.New(discovery.Options{Client: client, Clock: clk})

	done := make(chan struct{})
	go func() {
		_ = d.Pass(context.Background(), nil)
		close(done)
	}()

	// Let the pool saturate: wait for the bound's worth of probes to be in flight,
	// releasing none. With more repositories than the bound, exactly limiter.Bound
	// can be on the wire at once and the rest wait on the limiter's semaphore, so
	// this read blocks at the bound and never past it.
	for range limiter.Bound {
		<-base.entered
	}
	saturated := base.peak()

	// Release everything and let the pass finish.
	go func() {
		for {
			select {
			case base.release <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	<-done

	if saturated != limiter.Bound {
		t.Errorf("peak probes in flight = %d, want exactly %d (bounded by the limiter, and genuinely concurrent)", saturated, limiter.Bound)
	}
	if final := base.peak(); final > limiter.Bound {
		t.Errorf("peak over the whole run = %d, exceeded the limiter's bound of %d", final, limiter.Bound)
	}
}

// TestPublishesIncrementally is AC12. A repository's classification is observable
// while other probes are still in flight: the consumer is not blocked on the final
// probe. The test holds several probes in flight, releases one, and receives its
// emitted record while the others are still blocked.
func TestPublishesIncrementally(t *testing.T) {
	const repos = 6
	g := &gatedRequester{
		enumBody: enumBody(repos),
		runsBody: hasRunsBody,
		entered:  make(chan struct{}, repos),
		release:  make(chan struct{}),
	}
	d := discovery.New(discovery.Options{Client: g, Clock: clockwork.NewFakeClock()})

	emitted := make(chan discovery.Record, repos)
	done := make(chan struct{})
	go func() {
		_ = d.Pass(context.Background(), func(r discovery.Record) { emitted <- r })
		close(done)
	}()

	// Hold three probes in flight, none released yet.
	<-g.entered
	<-g.entered
	<-g.entered
	select {
	case r := <-emitted:
		t.Fatalf("a record (%v) was emitted before any probe was released; publication waited on nothing but should have had nothing to publish yet", r.ID())
	default:
	}

	// Release one. Its record must be observable while the other two are still in
	// flight, which is the whole of AC12.
	g.release <- struct{}{}
	select {
	case <-emitted:
		// A result was published while other probes were still blocked in flight.
	case <-time.After(2 * time.Second):
		t.Fatal("no record emitted after releasing one probe; publication is not incremental")
	}

	// Drain the rest.
	for {
		select {
		case g.release <- struct{}{}:
		case <-done:
			return
		}
	}
}
