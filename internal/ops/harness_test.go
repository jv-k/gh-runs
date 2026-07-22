package ops_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// testToken is a FIXED dummy token, never a real one. The store hashes the
// Authorization header into its cache key (local-store R20), so it must be stable,
// and go-gh must not reach the keyring for a real one.
const testToken = "dummy-fixed-token"

// opsMatcher matches a live request against a taped interaction on method and full
// URL (query included, because the crawl carries per_page). The DELETE fixtures
// carry no If-None-Match, so it is compared too, empty on both sides, keeping the
// matcher the same shape the rest of the tree pins go-vcr v4 for (CLAUDE.md).
func opsMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method {
		return false
	}
	if r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

// countingRT counts wire requests and records their method and URL, so a test can
// assert exactly which DELETEs left and prove no DELETE leaked past a stop (purge
// R28, AC20). It sits directly above the cassette, below the governor and store, so
// it counts real network exchanges and only those.
type countingRT struct {
	base     http.RoundTripper
	mu       sync.Mutex
	calls    []call
	onDelete func(n int) // called after the nth DELETE completes on the wire
}

type call struct {
	method string
	url    string
	body   string
}

func (c *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	// Capture the request body and restore it, so a test can assert what a re-run sent
	// (AC14's enable_debug_logging) without the cassette matching on it. GET and DELETE
	// carry no body, so this only reads the POST bodies the lifecycle operations send.
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		body = string(raw)
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}
	c.mu.Lock()
	c.calls = append(c.calls, call{req.Method, req.URL.String(), body})
	c.mu.Unlock()
	resp, err := c.base.RoundTrip(req)
	if req.Method == http.MethodDelete {
		c.mu.Lock()
		n := 0
		for _, cl := range c.calls {
			if cl.method == http.MethodDelete {
				n++
			}
		}
		hook := c.onDelete
		c.mu.Unlock()
		if hook != nil {
			hook(n) // lets a cancellation test cancel the context after the nth DELETE lands
		}
	}
	return resp, err
}

func (c *countingRT) deletes() int { return c.countMethod(http.MethodDelete) }

// urls returns the request URLs of every wire call made with the given method, in
// order, so a reclamation test can assert a DELETE carried the Cache's id and not its
// key (storage-reclamation R16, AC10).
func (c *countingRT) urls(method string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, cl := range c.calls {
		if cl.method == method {
			out = append(out, cl.url)
		}
	}
	return out
}

// postBody returns the request body of the first POST to a URL ending in suffix, and
// whether one was found, so a test can read what a re-run sent (AC14).
func (c *countingRT) postBody(suffix string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.calls {
		if cl.method == http.MethodPost && strings.HasSuffix(cl.url, suffix) {
			return cl.body, true
		}
	}
	return "", false
}

func (c *countingRT) countMethod(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, cl := range c.calls {
		if cl.method == method {
			n++
		}
	}
	return n
}

// errRT fails any request, so a zero-request test fails loudly if the code ever
// reaches the wire rather than silently passing against a cassette (purge R28).
type errRT struct{ t *testing.T }

func (e *errRT) RoundTrip(req *http.Request) (*http.Response, error) {
	e.t.Helper()
	e.t.Fatalf("a request reached the wire that should not have: %s %s", req.Method, req.URL)
	return nil, nil
}

// harness is the whole floor an ops test runs on: a cassette (or the failing
// transport) as the base, wrapped by the counter, nested under the limiter, the
// governor and the store exactly as main.go nests them (ADR-0012, ADR-0018), with a
// ghclient over the top and an Ops over that. Every DELETE travels the real chain, so
// it is paced by the governor and bounded by the limiter, and the fake clock makes a
// multi-hour Purge finish in milliseconds (purge R27).
type harness struct {
	ops              *ops.Ops
	client           *ghclient.Client
	counting         *countingRT
	clk              *clockwork.FakeClock
	logPath          string
	stateDir         string
	confirmThreshold int
	breakerFailures  int
}

// withLog returns the harness pointing its Ops at a different deletion-log path over
// the same transport chain, so a test can aim the log at an unwritable location and
// prove the precondition refuses without touching the wire (AC20).
func (h *harness) withLog(t *testing.T, logPath string) *harness {
	t.Helper()
	h.logPath = logPath
	h.ops = ops.New(ops.Options{
		Client:           h.client,
		Clock:            h.clk,
		LogPath:          logPath,
		ConfirmThreshold: h.confirmThreshold,
		BreakerFailures:  h.breakerFailures,
	})
	return h
}

// newHarness assembles the chain over the named cassette, with a log path in a temp
// dir and the given confirm and breaker thresholds.
func newHarness(t *testing.T, cassetteName string, confirmThreshold, breakerFailures int) *harness {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(opsMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", cassetteName, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", cassetteName, err)
		}
	})
	return newHarnessOverBase(t, rec, confirmThreshold, breakerFailures)
}

// newOfflineHarness assembles the chain over a transport that fails on any wire
// request, for the paths that must issue zero DELETEs (AC20's precondition).
func newOfflineHarness(t *testing.T, confirmThreshold, breakerFailures int) *harness {
	t.Helper()
	return newHarnessOverBase(t, &errRT{t: t}, confirmThreshold, breakerFailures)
}

func newHarnessOverBase(t *testing.T, base http.RoundTripper, confirmThreshold, breakerFailures int) *harness {
	t.Helper()
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	counting := &countingRT{base: base}
	gov := governor.New(limiter.New(counting, limiter.Bound), clk)
	stateDir := t.TempDir()
	transport := store.NewTransport(gov, t.TempDir(), clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: testToken, Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	logPath := filepath.Join(stateDir, "gh-runs", "deletions.log")
	o := ops.New(ops.Options{
		Client:           client,
		Clock:            clk,
		LogPath:          logPath,
		ConfirmThreshold: confirmThreshold,
		BreakerFailures:  breakerFailures,
	})
	return &harness{
		ops:              o,
		client:           client,
		counting:         counting,
		clk:              clk,
		logPath:          logPath,
		stateDir:         stateDir,
		confirmThreshold: confirmThreshold,
		breakerFailures:  breakerFailures,
	}
}

// runPurge runs Execute in a goroutine and drives the fake clock so paced writes
// release without a real sleep (purge R27, R28). It advances virtual time in small
// quanta whenever the writer parks, and stops when Execute returns and cancels the
// driver's context. It returns the pass's Summary.
func runPurge(t *testing.T, h *harness, c ops.Confirmed) ops.Summary {
	t.Helper()
	return runPurgeCtx(t, h, context.Background(), c)
}

// runPurgeCtx is runPurge over a caller-supplied context, so a cancellation test can
// cancel mid-run while the driver keeps virtual time moving.
func runPurgeCtx(t *testing.T, h *harness, ctx context.Context, c ops.Confirmed) ops.Summary {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	type result struct {
		s   ops.Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := h.ops.Execute(runCtx, c)
		cancel() // unblock the driver the instant Execute returns
		done <- result{s, err}
	}()
	const quantum = 200 * time.Millisecond
	for {
		if err := h.clk.BlockUntilContext(runCtx, 1); err != nil {
			break // Execute finished (or the caller cancelled) and cancelled runCtx
		}
		h.clk.Advance(quantum)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("Execute returned an error: %v", r.err)
	}
	return r.s
}

// plan builds a Confirmed over items with a snapshot covering their repositories,
// confirmed non-interactively, for a test that is about Execute rather than the
// confirmation gate.
func (h *harness) confirmed(t *testing.T, op ops.Operation, items []ops.Item, repos map[domain.RepoID]domain.Repo) ops.Confirmed {
	t.Helper()
	p, err := h.ops.Plan(op, items, repos)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c, err := h.ops.Confirm(p, ops.NonInteractiveYes())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	return c
}

// logField is one parsed deletion-log line: R29's six fields.
type logField struct {
	timestamp, repo, kind, id, outcome, reason string
}

// readLog reads and parses the deletion log's lines. It is the test's own reader; no
// ops code path reads the log back, which is R29's write-only rule (AC19). It errors
// if a line does not carry exactly six tab-separated fields.
func (h *harness) readLog(t *testing.T) []logField {
	t.Helper()
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		t.Fatalf("read deletion log: %v", err)
	}
	var out []logField
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			t.Fatalf("log line has %d fields, want 6 (R29): %q", len(f), line)
		}
		out = append(out, logField{f[0], f[1], f[2], f[3], f[4], f[5]})
	}
	return out
}

// logExists reports whether the deletion log file is present, so a test can prove
// Plan, Confirm and Crawl write nothing and only Execute opens it (ADR-0019).
func (h *harness) logExists() bool {
	_, err := os.Stat(h.logPath)
	return err == nil
}

// outcomes tallies the log's outcome column, so a test reads what was recorded
// rather than what the code believes it recorded.
func (h *harness) outcomes(t *testing.T) map[string]int {
	t.Helper()
	m := make(map[string]int)
	for _, f := range h.readLog(t) {
		m[f.outcome]++
	}
	return m
}
