package scheduler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// completedRunsBody is a valid one-Run listing whose Run is completed, so a
// repository the stub answers stays slow-tier. The orchestration tests that use it
// count wire requests, not tier transitions.
const completedRunsBody = `{"total_count":1,"workflow_runs":[{"id":1,"status":"completed","name":"CI"}]}`

// stubRT answers every GET with a 200 carrying completedRunsBody and a per-path
// ETag, so the store persists a distinct entry per repository. It is the base for the
// orchestration tests (live set changes, exhaustion, demotion), which are about how
// the scheduler drives itself rather than about the wire, so a fake stands in for a
// cassette.
type stubRT struct{}

func (stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + req.URL.Path + `-v1"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(completedRunsBody)),
		ContentLength: int64(len(completedRunsBody)),
	}, nil
}

// gatedBaseRT blocks each request until it is released and records the peak number
// concurrently in flight, so a test can prove the transport limiter, not the
// scheduler, bounds the wire (AC15) and that Stop unwinds an in-flight poll. It sits
// at the very bottom of the chain, so the concurrency it measures is what the limiter
// admits.
type gatedBaseRT struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int

	entered chan struct{}
	release chan struct{}
}

func (g *gatedBaseRT) RoundTrip(req *http.Request) (*http.Response, error) {
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

	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + req.URL.Path + `-v1"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(completedRunsBody)),
		ContentLength: int64(len(completedRunsBody)),
	}, nil
}

func (g *gatedBaseRT) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
}

// testToken is a FIXED dummy token, never a real one. The store hashes the
// Authorization header into its key (local-store R20), so the token must be stable
// across a test, and go-gh must not reach the keyring for a real one.
const testToken = "dummy-fixed-token"

// schedulerMatcher matches a live request against a taped interaction on method,
// full URL, and the If-None-Match header. The header is the load-bearing part: a
// steady-state poll carries If-None-Match and the first poll does not, so comparing
// it is what keeps the 304 tests (AC16, R22) from passing vacuously, which is the
// whole reason the canon pins go-vcr v4 over v3 (CLAUDE.md, ADR-0013).
func schedulerMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method {
		return false
	}
	if r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

// openCassette opens a recorded fixture in replay-only mode with the header-matching
// matcher above. WithReplayableInteractions lets one taped 304 answer many identical
// steady-state polls.
func openCassette(t *testing.T, name string) *recorder.Recorder {
	t.Helper()
	rec, err := recorder.New("testdata/"+name,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(schedulerMatcher),
		recorder.WithReplayableInteractions(true),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", name, err)
		}
	})
	return rec
}

// countingRT counts the requests that reach the wire, records their URLs, and counts
// the conditional ones. It sits directly above the cassette, below the limiter, so it
// counts real network exchanges and only those (mirroring discovery's harness). Its
// reqs channel lets a test wait for a known number of polls to leave.
type countingRT struct {
	base http.RoundTripper

	mu          sync.Mutex
	n           int
	conditional int
	urls        []string
}

func (c *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	cond := req.Header.Get("If-None-Match") != ""
	resp, err := c.base.RoundTrip(req)
	c.mu.Lock()
	c.n++
	if cond {
		c.conditional++
	}
	c.urls = append(c.urls, req.URL.String())
	c.mu.Unlock()
	return resp, err
}

func (c *countingRT) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *countingRT) conditionalCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conditional
}

// countPath counts how many wire requests targeted a URL containing substr, so a
// test can prove a repository was or was not polled over an interval (AC7).
func (c *countingRT) countPath(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, u := range c.urls {
		if strings.Contains(u, substr) {
			n++
		}
	}
	return n
}

// sawOnly reports whether every wire request's URL contained one of the allowed
// substrings, so a test can prove no repository outside the poll set was ever polled
// (AC6).
func (c *countingRT) sawOnly(allowed ...string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.urls {
		ok := false
		for _, a := range allowed {
			if strings.Contains(u, a) {
				ok = true
				break
			}
		}
		if !ok {
			return u, false
		}
	}
	return "", true
}

// fakePollSet is a mutable poll set for the orchestration tests (AC7). PollSet
// returns a copy, so a live mutation never races a reader.
type fakePollSet struct {
	mu  sync.Mutex
	ids []domain.RepoID
}

func (f *fakePollSet) PollSet() []domain.RepoID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RepoID(nil), f.ids...)
}

func (f *fakePollSet) set(ids ...domain.RepoID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append([]domain.RepoID(nil), ids...)
}

// fakeBudget is a controllable Budget Readout for the demotion and exhaustion tests
// (AC10, AC11, AC12), which are about how the scheduler reacts to the Readout rather
// than about the wire. fn, when set, computes the Readout from the current time, so a
// test can make exhaustion clear at a chosen instant.
type fakeBudget struct {
	mu sync.Mutex
	r  governor.Readout
	fn func() governor.Readout
}

func (f *fakeBudget) Readout() governor.Readout {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fn != nil {
		return f.fn()
	}
	return f.r
}

func (f *fakeBudget) set(r governor.Readout) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.r = r
}

// harness is the whole floor a scheduler test runs on. For a cassette test the base
// is the recorder; for an orchestration test it is a fake RoundTripper or a nil
// Budget. Either way the chain is nested exactly as main.go nests it (ADR-0012,
// ADR-0018): the counted wire under the limiter under the governor under the store,
// with a ghclient over the top and a Scheduler over that.
type harness struct {
	s        *Scheduler
	counting *countingRT
	gov      *governor.Governor
	clk      *clockwork.FakeClock
	ps       *fakePollSet

	mu      sync.Mutex
	updates []Update
	updated chan struct{}
	drainWG sync.WaitGroup
}

// harnessConfig customises a harness before its Scheduler is built.
type harnessConfig struct {
	base    http.RoundTripper // the injected wire base; a cassette or a fake
	pollSet []domain.RepoID
	budget  Budget // nil means wire the governor
}

// newHarness assembles the chain and the Scheduler, enables the settle probe, and
// starts a background drainer of Updates. It does not Start the engine; a test does,
// so it controls the cold-start moment.
func newHarness(t *testing.T, cfg harnessConfig) *harness {
	t.Helper()
	clk := clockwork.NewFakeClockAt(t0)
	counting := &countingRT{base: cfg.base}
	gov := governor.New(limiter.New(counting, limiter.Bound), clk)
	transport := store.NewTransport(gov, t.TempDir(), clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: testToken, Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ps := &fakePollSet{}
	ps.set(cfg.pollSet...)

	budget := cfg.budget
	if budget == nil {
		budget = gov
	}

	s := New(Options{Client: client, PollSet: ps, Budget: budget, Clock: clk})
	s.settled = make(chan time.Duration, 1)
	s.polled = make(chan struct{}, 4096)

	h := &harness{s: s, counting: counting, gov: gov, clk: clk, ps: ps, updated: make(chan struct{}, 4096)}
	h.drainWG.Add(1)
	go func() {
		defer h.drainWG.Done()
		for u := range s.Updates() {
			h.mu.Lock()
			h.updates = append(h.updates, u)
			h.mu.Unlock()
			h.updated <- struct{}{}
		}
	}()
	return h
}

// waitPolls blocks until n more poll goroutines have fully finished, a barrier
// strong enough to assert a negative: after it returns, a 304 poll has decided to
// emit nothing and the governor has observed the exchange.
func (h *harness) waitPolls(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-h.s.polled:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for poll %d of %d to finish", i+1, n)
		}
	}
}

// start starts the engine and registers a cleanup that stops it.
func (h *harness) start(t *testing.T) {
	t.Helper()
	h.s.Start(t.Context())
	t.Cleanup(func() { h.stop() })
}

// stop stops the engine and waits for the Updates drainer to finish. Scheduler.Stop
// is idempotent, so a test may call this explicitly to assert quit behaviour and the
// registered cleanup runs it again as a safe no-op.
func (h *harness) stop() {
	h.s.Stop()
	h.drainWG.Wait()
}

// blockUntil waits for the loop to have n timers or tickers pending on the fake
// clock, so a test advances virtual time only once the loop is asleep on its timer.
func (h *harness) blockUntil(n int) {
	_ = h.clk.BlockUntilContext(context.Background(), n)
}

// waitUpdates blocks until n more Updates have been delivered to the Feed drainer.
func (h *harness) waitUpdates(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-h.updated:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for update %d of %d (received %d)", i+1, n, h.updateCount())
		}
	}
}

func (h *harness) updateCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.updates)
}

// waitSettle blocks until the loop reports it has chosen the wanted wait, draining
// any intermediate evaluations, so a test synchronises on the loop having converged
// after asynchronous poll completions before it advances virtual time.
func (h *harness) waitSettle(t *testing.T, want time.Duration) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case w := <-h.s.settled:
			if w == want {
				return
			}
		case <-deadline:
			t.Fatalf("loop did not settle on %s", want)
		}
	}
}
