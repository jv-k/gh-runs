package cli_test

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/cli"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// testToken is a FIXED dummy token, never a real one. The store hashes the
// Authorization header into its cache key (local-store R20), so it must be
// stable across a test, and go-gh must not reach the keyring for a real one.
const testToken = "dummy-fixed-token"

// cliMatcher matches a live request against a taped interaction on method, full
// URL (query string included, because the list command pushes the filter's
// server-side half as query parameters) and the If-None-Match header. The header
// is the load-bearing comparison the canon pins go-vcr v4 for: a conditional
// re-list carries If-None-Match and a first list does not, so a matcher blind to
// it would let a revalidation pass vacuously (CLAUDE.md, ADR-0013). It is
// narrower than v4's default, which also compares every other header go-gh
// injects per machine, matching the exact outbound shape rather than the one
// property under test.
func cliMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method {
		return false
	}
	if r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

// countingRT counts the requests that reach the wire and records their URLs. It
// sits directly above the cassette, below the governor and the store, so it
// counts real network exchanges and only those. It is how the zero-request
// acceptance criteria (a typo rejected client-side, an unsupported host rejected
// offline) are checked against what actually left the process, rather than
// against a message a test watched for (cli-surface R19, AC5, AC7).
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

// countMatching counts wire requests whose URL contains substr, so a fan-out
// test can prove one listing request went to each repository (cli-surface AC15).
func (c *countingRT) countMatching(substr string) int {
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

// errRT is a base transport that fails any request. A zero-request test wraps it
// so that if the command ever reaches the wire the test fails loudly rather than
// silently succeeding against a cassette (cli-surface R19).
type errRT struct{ t *testing.T }

func (e *errRT) RoundTrip(req *http.Request) (*http.Response, error) {
	e.t.Helper()
	e.t.Fatalf("a request reached the wire that should have been rejected offline: %s", req.URL)
	return nil, nil
}

// harness is the whole floor a cli test runs on: a base (a cassette, or the
// failing transport for offline-rejection tests), wrapped by the counter, nested
// under the limiter, the governor and the store exactly as main.go nests them
// (ADR-0012, ADR-0018), with a ghclient over the top. Every request the command
// issues travels the real transport chain, so the seam the command is tested at
// is the same one it runs on in production.
type harness struct {
	deps     cli.Deps
	counting *countingRT
	clk      *clockwork.FakeClock
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	env      map[string]string
}

// newHarness assembles the chain over the named cassette in testdata. A cold
// temp dir gives the store a fresh cache, so the first list of each URL is a wire
// miss matched by the empty If-None-Match branch of cliMatcher.
func newHarness(t *testing.T, cassetteName string) *harness {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(cliMatcher),
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
	return newHarnessOverBase(t, rec)
}

// newHarnessOffline assembles the chain over a transport that fails on any wire
// request, for the client-side-rejection paths that must issue zero requests.
func newHarnessOffline(t *testing.T) *harness {
	t.Helper()
	return newHarnessOverBase(t, &errRT{t: t})
}

func newHarnessOverBase(t *testing.T, base http.RoundTripper) *harness {
	t.Helper()
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	counting := &countingRT{base: base}
	// The limiter is innermost, directly above the counted wire, exactly as main.go
	// nests it: store over governor over limiter over the base (ADR-0018).
	gov := governor.New(limiter.New(counting, limiter.Bound), clk)
	transport := store.NewTransport(gov, t.TempDir(), clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: testToken, Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := map[string]string{}
	h := &harness{
		counting: counting,
		clk:      clk,
		stdout:   stdout,
		stderr:   stderr,
		env:      env,
	}
	h.deps = cli.Deps{
		Client:  client,
		Getenv:  func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Stdout:  stdout,
		Stderr:  stderr,
		Clock:   clk,
		Current: func() (domain.RepoID, error) { return domain.RepoID{}, errNoCurrentRepo },
		Discovered: func() ([]domain.RepoID, error) {
			return nil, errNoDiscoverySet
		},
	}
	return h
}

// withCurrent sets the working-directory repository resolver (cli-surface R8: no
// -R inside a repository means that repository).
func (h *harness) withCurrent(id domain.RepoID) *harness {
	h.deps.Current = func() (domain.RepoID, error) { return id, nil }
	return h
}

// withDiscovered sets the fan-out repository set (cli-surface R22: outside a
// repository, list fans out across the discovered set).
func (h *harness) withDiscovered(ids ...domain.RepoID) *harness {
	h.deps.Discovered = func() ([]domain.RepoID, error) { return ids, nil }
	return h
}

// run executes the command with args and returns its process exit code
// (cli-surface R17). Output is captured in the harness buffers.
func (h *harness) run(args ...string) int {
	return cli.Execute(h.deps, args)
}

// gh builds a github.com-qualified repository identity.
func gh(owner, name string) domain.RepoID {
	return domain.RepoID{Host: "github.com", Owner: owner, Name: name}
}

// errNoCurrentRepo and errNoDiscoverySet are the default seam errors: a test that
// does not wire a resolver behaves as if it is outside a repository with an empty
// discovered set, so a path that reaches either without being configured fails
// visibly rather than silently.
var (
	errNoCurrentRepo  = &harnessError{"no current repository configured in this test"}
	errNoDiscoverySet = &harnessError{"no discovered set configured in this test"}
)

type harnessError struct{ msg string }

func (e *harnessError) Error() string { return e.msg }
