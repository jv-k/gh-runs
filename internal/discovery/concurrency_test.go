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
)

// probeBound is the process's probe concurrency bound (ADR-0018, repo-discovery
// R16). It is duplicated here as the value AC13 asserts against; the engine holds
// it as an unexported constant no setting alters.
const probeBound = 10

// gatedRequester is a fake transport for the orchestration properties: bounded
// concurrency (AC13) and incremental publication (AC12), which are about how
// discovery fans out rather than about what the API says. The fidelity properties
// are proved against cassettes; this fake exists only to make probe timing
// observable and controllable. Every probe blocks on release, so a test holds a
// known number in flight and asserts the bound and the progressive emission
// directly.
type gatedRequester struct {
	enumBody string
	runsBody string

	mu       sync.Mutex
	inFlight int
	maxSeen  int

	entered chan struct{}
	release chan struct{}
}

func (g *gatedRequester) Request(method, path string, body io.Reader) (*http.Response, error) {
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

func (g *gatedRequester) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
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

// TestProbeConcurrencyIsBounded is AC13. Given many more repositories than the
// bound, at no instant are more than probeBound probes in flight, and no
// user-facing setting alters that. The test releases probes one at a time so the
// pool stays saturated, and the peak in-flight count it observes is exactly the
// bound: bounded, and genuinely concurrent rather than serial.
func TestProbeConcurrencyIsBounded(t *testing.T) {
	const repos = 30
	g := &gatedRequester{
		enumBody: enumBody(repos),
		runsBody: hasRunsBody,
		entered:  make(chan struct{}, repos),
		release:  make(chan struct{}),
	}
	d := discovery.New(discovery.Options{Client: g, Clock: clockwork.NewFakeClock()})

	done := make(chan struct{})
	go func() {
		_ = d.Pass(context.Background(), nil)
		close(done)
	}()

	// Let the pool saturate: wait for the bound's worth of probes to be in flight,
	// releasing none. With more repositories than the bound, exactly probeBound can
	// be in flight at once and the rest wait for a worker slot, so this read blocks
	// at the bound and never past it.
	for range probeBound {
		<-g.entered
	}
	saturated := g.peak()

	// Release everything and let the pass finish.
	go func() {
		for {
			select {
			case g.release <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	<-done

	if saturated != probeBound {
		t.Errorf("peak probes in flight = %d, want exactly %d (bounded and concurrent)", saturated, probeBound)
	}
	if final := g.peak(); final > probeBound {
		t.Errorf("peak over the whole run = %d, exceeded the bound of %d", final, probeBound)
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
