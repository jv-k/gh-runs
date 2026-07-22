package discovery_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// testToken is a FIXED dummy token, never a real one. The store hashes the
// Authorization header into its key (local-store R20), so the token must be stable
// across the test, and go-gh must not reach the keyring for a real one.
const testToken = "dummy-fixed-token"

// discoveryMatcher matches a live request against a taped interaction on method,
// full URL, and the If-None-Match header. The header is the load-bearing part: a
// conditional re-probe carries If-None-Match and an unconditional probe does not,
// so comparing it is what keeps AC10's 304 test from passing vacuously, which is
// the whole reason the canon pins go-vcr v4 over v3 (CLAUDE.md, ADR-0013). It is
// narrower than v4's default matcher on purpose: the default also compares the
// parsed Form, the request URI and every header, which makes a hand-written
// cassette match the exact shape of go-gh's outbound request rather than the one
// property under test. Matching method, URL and If-None-Match is exactly the
// distinction discovery's fixtures need and nothing it does not.
func discoveryMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method {
		return false
	}
	if r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

// countingRT counts the requests that reach the wire and records their URLs. It
// sits directly above the cassette, below the governor and the store, so it counts
// real network exchanges and only those: a request the store answers from a 304 is
// still a wire exchange and is counted, but a request the store could short-circuit
// without dialing would not be. Its count is how the cost acceptance criteria (two
// enumeration requests, one probe per repository, zero capability requests) are
// checked against what actually left, rather than against what the code believes it
// sent.
type countingRT struct {
	base http.RoundTripper
	mu   sync.Mutex
	n    int
	urls []string
}

func (c *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.urls = append(c.urls, req.URL.String())
	c.mu.Unlock()
	return c.base.RoundTrip(req)
}

func (c *countingRT) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// sawPath reports whether any wire request's URL contained substr, so a test can
// prove a path was never issued (no code-search request, AC4) or was (adoption's
// one GET, R22).
func (c *countingRT) sawPath(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.urls {
		if strings.Contains(u, substr) {
			return true
		}
	}
	return false
}

// countExact counts how many wire requests targeted exactly url, so a test can
// distinguish adoption's GET /repos/owner/repo from the probe's GET
// /repos/owner/repo/actions/runs, which the substring check cannot (R22).
func (c *countingRT) countExact(url string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, u := range c.urls {
		if u == url {
			n++
		}
	}
	return n
}

// harness is the whole floor a discovery test runs on: the cassette as the
// injected base, wrapped by the counter, nested under the limiter, the governor and
// the store exactly as main.go nests them (ADR-0012, ADR-0018), with a ghclient over
// the top and a Discovery over that. Every request a test's discovery issues travels
// the real transport chain, so the governor's accounting, the store's revalidation
// and the limiter's wire bound are exercised rather than stubbed (R17, R12, R20).
type harness struct {
	disc     *discovery.Discovery
	client   *ghclient.Client
	store    *store.Transport
	gov      *governor.Governor
	counting *countingRT
	clk      *clockwork.FakeClock
	dir      string
}

// newDocStore returns a real store Transport over dir for the document-persistence
// seam alone (no HTTP: base is nil). The first over a directory is the writer; a
// second is a reader, which is how a restart-and-reload test reads what a prior
// session wrote (local-store R21).
func newDocStore(t *testing.T, dir string) *store.Transport {
	t.Helper()
	return store.NewTransport(nil, dir, clockwork.NewFakeClock())
}

// harnessOption customises a harness before its Discovery is built.
type harnessOption func(*discovery.Options)

// withRefresh sets the fast-tier interval.
func withRefresh(d time.Duration) harnessOption {
	return func(o *discovery.Options) { o.Refresh = d }
}

// withCurrent sets the fast-path resolver seam (R14).
func withCurrent(f func() (domain.RepoID, error)) harnessOption {
	return func(o *discovery.Options) { o.Current = f }
}

// newHarness assembles the chain over the named cassette. The store's directory is
// a fresh temp dir unless the test reuses one to simulate a restart (AC7).
func newHarness(t *testing.T, cassetteName string, dir string, opts ...harnessOption) *harness {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(discoveryMatcher),
		recorder.WithReplayableInteractions(true),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", cassetteName, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", cassetteName, err)
		}
	})

	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	counting := &countingRT{base: rec}
	// The limiter is innermost, directly above the counted wire, exactly as main.go
	// nests it (ADR-0018): store over governor over limiter over the base. Every
	// discovery test therefore exercises the real chain with the wire bound in place.
	gov := governor.New(limiter.New(counting, limiter.Bound), clk)
	if dir == "" {
		dir = t.TempDir()
	}
	transport := store.NewTransport(gov, dir, clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: testToken, Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	o := discovery.Options{
		Client:  client,
		Store:   transport,
		Budget:  gov,
		Clock:   clk,
		Refresh: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(&o)
	}

	return &harness{
		disc:     discovery.New(o),
		client:   client,
		store:    transport,
		gov:      gov,
		counting: counting,
		clk:      clk,
		dir:      dir,
	}
}
