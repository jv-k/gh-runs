package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui"
)

// fastPathRuns is the resource AC16 counts one of: the launch repository's Run listing.
const fastPathRuns = "/repos/acme/api/actions/runs"

// wireEvent is one thing that happened at the wire, in order: a request reaching it, and
// later the same request being answered. The pair is what makes the ordering assertable. A
// request held in the base has been issued and not answered, and AC16 counts what was
// issued, not what came back.
type wireEvent struct {
	path     string
	answered bool
}

// coldStartRT is the account this test launches against: two repositories, one of them the
// one the tool was launched inside. It holds the launch repository's opening Run listing at
// the wire until the test releases it, which is the whole instrument. Everything the cold
// start does behind that request either waits for it or does not, and the recorded order
// says which.
type coldStartRT struct {
	hold    chan struct{} // closed to answer the held Run listing
	reached chan struct{} // closed when that request reaches the wire
	once    sync.Once

	mu     sync.Mutex
	events []wireEvent
}

func (r *coldStartRT) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	r.recordIssued(path)

	if path == fastPathRuns {
		r.once.Do(func() { close(r.reached) })
		<-r.hold // released once; every later poll of the same path passes straight through
	}

	body := r.bodyFor(path)
	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + path + `"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	r.recordAnswered(path)

	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}, nil
}

// repoRuns is one repository's Run listing. The Run IDs are distinct per repository, and
// deliberately so: the Feed keys a painted row by Run ID (feed.repaintAndDefer), so two
// repositories sharing one would collapse into a single row and the reveal this test
// asserts would be invisible for reasons that have nothing to do with the scheduler.
var repoRuns = map[string]string{
	"api": `{"total_count":1,"workflow_runs":[{"id":101,"name":"api-ci","status":"completed","conclusion":"success","workflow_id":9,"run_started_at":"2026-07-25T08:55:00Z"}]}`,
	"web": `{"total_count":1,"workflow_runs":[{"id":202,"name":"web-ci","status":"completed","conclusion":"success","workflow_id":9,"run_started_at":"2026-07-25T08:50:00Z"}]}`,
}

// bodyFor answers the three resources this account serves: the enumeration, each
// repository's Run listing, and each repository's Workflow listing.
func (r *coldStartRT) bodyFor(path string) string {
	switch {
	case path == "/user/repos":
		return `[{"name":"api","full_name":"acme/api","owner":{"login":"acme"},"permissions":{"admin":true,"push":true}},
		         {"name":"web","full_name":"acme/web","owner":{"login":"acme"},"permissions":{"admin":true,"push":true}}]`
	case strings.HasSuffix(path, "/actions/runs"):
		return repoRuns[strings.Split(strings.TrimPrefix(path, "/repos/acme/"), "/")[0]]
	case strings.HasSuffix(path, "/actions/workflows"):
		return `{"total_count":1,"workflows":[{"id":9,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`
	}
	return `{}`
}

// recordIssued and recordAnswered are the two halves of the wire log. They are named rather
// than one call taking a boolean, because every call site otherwise reads as `record(path,
// true)` and the reader has to go and look up which half true is.
func (r *coldStartRT) recordIssued(path string) { r.record(wireEvent{path: path}) }

func (r *coldStartRT) recordAnswered(path string) {
	r.record(wireEvent{path: path, answered: true})
}

func (r *coldStartRT) record(e wireEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// issued is every request that has reached the wire, in order, answered or not.
func (r *coldStartRT) issued() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if !e.answered {
			out = append(out, e.path)
		}
	}
	return out
}

// issuedBeforeFastPathAnswered is every request that had reached the wire by the time the
// launch repository's opening Run listing was answered. It is the window AC16's count is
// taken over, and it is derived from the recorded order rather than from a clock, so it
// reads the same under every goroutine interleaving.
func (r *coldStartRT) issuedBeforeFastPathAnswered(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if e.answered {
			if e.path == fastPathRuns {
				return out
			}
			continue
		}
		out = append(out, e.path)
	}
	t.Fatalf("the launch repository's Run listing was never answered: %v", r.events)
	return nil
}

// TestColdStartPaintsTheCurrentRepositoryFromOneRunListing is R32 and AC16 driven through
// the composition main.go assembles: the real transport chain, a real Discovery over it, a
// real Scheduler with the fast path wired, the real gating main.go performs, the real root,
// and a real painted frame. Nothing is faked above the wire.
//
// **Which requests AC16 counts.** Run listings, `GET /repos/{owner}/{repo}/actions/runs`,
// and only those. The opening poll also reads that repository's Workflow listing, because a
// Run carries a Workflow ID and no Workflow state, so the fan-out joins the two before the
// event leaves the worker (ADR-0014, ADR-0015). That is a different resource, read at most
// once per repository per process, and AC16 does not speak to it. This test asserts both
// halves, so the distinction is pinned rather than assumed.
//
// **How the ordering is proved.** By what is on the wire and in what order, never by elapsed
// time. The launch repository's opening Run listing is held in the base, and the assertion
// is over the requests recorded before it was answered. Before this change main.go ran a
// full synchronous discovery pass before the engine started, so the account enumeration was
// the first request and ~163 probes, every one of them a Run listing, preceded the one AC16
// counts.
func TestColdStartPaintsTheCurrentRepositoryFromOneRunListing(t *testing.T) {
	current := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	other := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "web"}

	base := &coldStartRT{hold: make(chan struct{}), reached: make(chan struct{})}
	release := sync.OnceFunc(func() { close(base.hold) })

	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, t.TempDir(), clk)
	cl, err := newClients(transport, gov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}

	disc := discovery.New(discovery.Options{
		Client:  cl.shared,
		Store:   transport,
		Budget:  gov,
		Clock:   clk,
		Current: func() (domain.RepoID, error) { return current, nil },
	})

	// Seeded exactly as runTUI seeds it: a local-store read in front of the engine, so the
	// poll set the gate holds back is real rather than incidentally empty.
	seeded := disc.Reload()

	sched := scheduler.New(scheduler.Options{
		Client:     cl.shared,
		PollSet:    disc,
		First:      current,
		Classified: classifiedBy(disc),
		Budget:     gov,
		Clock:      clock.Clock(clk),
		Workflows:  cl.workflowLister(),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sched.Start(ctx)
	t.Cleanup(sched.Stop)
	// Registered after Stop so it runs before it: cleanups unwind last-registered first, and
	// Stop waits on every in-flight poll, including one still held in the base. Without this
	// order a failed assertion below would deadlock the whole run instead of reporting.
	t.Cleanup(release)

	// The gating runTUI performs, called as runTUI calls it.
	discovered := make(chan struct{})
	go func() {
		defer close(discovered)
		discoverBehind(ctx, sched, disc, current, seeded)
	}()

	<-base.reached // the launch repository's Run listing is on the wire, held there

	// Nothing else may go out while it is in flight. This is the negative AC16 asserts, and
	// it is given a window to fail in rather than asserted the instant after: the discovery
	// goroutine is running concurrently, and a window is what lets it reach the wire if it
	// is going to. It is a bounded opportunity for a background goroutine, not a wait on any
	// cadence: no interval is slept through and no clock is advanced (R21).
	select {
	case <-discovered:
		t.Fatalf("discovery ran while the fast path's opening poll was still in flight (R32): %v", base.issued())
	case <-time.After(250 * time.Millisecond):
	}
	if got := base.issued(); len(got) != 1 || got[0] != fastPathRuns {
		t.Fatalf("requests on the wire while the fast path's opening poll is in flight = %v, want only %s (AC16)", got, fastPathRuns)
	}

	// Release it. The Feed paints the launch repository's Runs, and the gate opens.
	release()

	ev := recv(t, sched.Updates())
	up, ok := ev.(scheduler.Update)
	if !ok {
		t.Fatalf("the first event was %T, want a scheduler.Update carrying the fast path's Runs", ev)
	}
	if up.Repo != current {
		t.Fatalf("the first event was for %s, want the repository the tool was launched inside (%s)", up.Repo, current)
	}

	// The Workflow listing is the join, not a second Run listing, and it is what AC16 does not
	// count. Asserting it here is deterministic rather than a sample of a moving count: the
	// join is read inside the poll, before the event is emitted, and it is memoised per
	// repository behind that repository's single-flight flag, so by the time this Update is in
	// hand the launch repository's Workflow listing has gone out exactly once and can never go
	// out again.
	if got := countSuffix(base.issued(), "/repos/acme/api/actions/workflows"); got != 1 {
		t.Errorf("Workflow listings for the launch repository by the time its Runs were emitted = %d, want 1: the join is read once per repository and is not what AC16 counts", got)
	}

	root := tui.New(cl.tuiOptions(tuiDeps{
		Config:    config.Config{},
		Profile:   keys.Standard,
		Clock:     clock.Clock(clk),
		Scheduler: sched,
		Governor:  gov,
		Store:     transport,
		Discovery: disc,
		Ops:       ops.New(ops.Options{Client: cl.shared, Clock: clk, LogPath: t.TempDir() + "/deletions.log"}),
		Downloads: t.TempDir(),
	}))
	var m tea.Model = root
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ev)

	frame := m.View().Content
	if !strings.Contains(frame, "acme/api") {
		t.Fatalf("the fast path's Runs never reached the painted frame:\n%s", frame)
	}

	// AC16, over the recorded order: exactly one Run listing had been issued by the time the
	// launch repository's own came back, and it was that one.
	before := base.issuedBeforeFastPathAnswered(t)
	if len(before) != 1 || before[0] != fastPathRuns {
		t.Fatalf("requests issued before the fast path's Run listing was answered = %v, want exactly [%s] (AC16)", before, fastPathRuns)
	}

	// R33: the rest of the account reveals progressively behind the fast path, with no user
	// interaction and without waiting out a poll interval. Discovery classifies the account,
	// each repository joins the poll set as its probe returns, and the engine polls it at the
	// next decision. No virtual time is advanced here at all: if the reveal needed a tier
	// interval to elapse this would hang rather than pass.
	waitClosed(t, discovered, "the discovery pass to return") // its classification is then complete and persisted
	if got := len(disc.PollSet()); got != 2 {
		t.Fatalf("poll set after discovery = %d repositories, want both", got)
	}

	ev = recvUpdateFor(t, sched.Updates(), other)
	m, _ = m.Update(ev)
	frame = m.View().Content
	if !strings.Contains(frame, "acme/web") {
		t.Fatalf("the repository discovery revealed never reached the painted frame (R33):\n%s", frame)
	}
}

// countSuffix tallies paths ending in suffix. The wire log holds exact paths, so a suffix
// match is what distinguishes a resource from a repository here, where the scheduler
// harness's counters match a substring of a full URL.
func countSuffix(paths []string, suffix string) int {
	n := 0
	for _, p := range paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

// TestWarmLaunchHoldsTheSeededPollSetBehindTheFastPath is AC16 on the launch that actually
// happens most: a second run, with the local-store already holding the classified account.
//
// It is a separate test because the cold-start one cannot prove this. There the poll set is
// empty at engine start, so the engine's gate has nothing to hold back and the ordering rests
// on the discovery goroutine's wait alone. Here the poll set is seeded from disk before the
// engine is constructed, exactly as runTUI seeds it, so both mechanisms are live and the
// engine's gate is the one under test: two repositories are due and one Run listing goes out.
//
// The first phase is a real prior session. It runs a real pass over a real store directory
// and persists what it classified (repo-discovery R19, local-store R2), so the second phase
// reloads a document this test did not hand-write.
func TestWarmLaunchHoldsTheSeededPollSetBehindTheFastPath(t *testing.T) {
	current := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	dir := t.TempDir()

	// Phase one: a prior session discovers and persists the account. Its hold is closed, so
	// nothing is held and the pass simply runs.
	priorBase := &coldStartRT{hold: make(chan struct{}), reached: make(chan struct{})}
	close(priorBase.hold)
	priorClk := clockwork.NewFakeClockAt(time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC))
	priorGov := governor.New(limiter.New(priorBase, limiter.Bound), priorClk)
	priorTransport := store.NewTransport(priorGov, dir, priorClk)
	priorClients, err := newClients(priorTransport, priorGov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}
	prior := discovery.New(discovery.Options{
		Client: priorClients.shared, Store: priorTransport, Budget: priorGov, Clock: priorClk,
	})
	if err := prior.Pass(t.Context(), nil); err != nil {
		t.Fatalf("prior session's discovery pass: %v", err)
	}

	// Phase two: this session. Fresh chain, same store directory, launch-repository Run
	// listing held at the wire.
	base := &coldStartRT{hold: make(chan struct{}), reached: make(chan struct{})}
	release := sync.OnceFunc(func() { close(base.hold) })

	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, dir, clk)
	cl, err := newClients(transport, gov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}
	disc := discovery.New(discovery.Options{
		Client: cl.shared, Store: transport, Budget: gov, Clock: clk,
		Current: func() (domain.RepoID, error) { return current, nil },
	})

	seeded := disc.Reload()
	if seeded != 2 {
		t.Fatalf("the local-store seeded %d repositories, want 2: the warm launch this test is about did not happen", seeded)
	}
	if got := len(disc.PollSet()); got != 2 {
		t.Fatalf("poll set at engine construction = %d, want 2: with an empty one the gate has nothing to hold back and this test proves nothing", got)
	}

	sched := scheduler.New(scheduler.Options{
		Client:     cl.shared,
		PollSet:    disc,
		First:      current,
		Classified: classifiedBy(disc),
		Budget:     gov,
		Clock:      clock.Clock(clk),
		Workflows:  cl.workflowLister(),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sched.Start(ctx)
	t.Cleanup(sched.Stop)
	t.Cleanup(release) // runs before Stop, which waits on every in-flight poll

	discovered := make(chan struct{})
	go func() {
		defer close(discovered)
		discoverBehind(ctx, sched, disc, current, seeded)
	}()

	<-base.reached

	// Two repositories are due, and both were in the poll set before the engine was
	// constructed (asserted above, which is what makes the reload demonstrably ungated).
	// Exactly one Run listing is on the wire, and it is the launch repository's. The window is
	// a bounded opportunity for the discovery goroutine to misbehave, not a wait on a cadence.
	time.Sleep(250 * time.Millisecond)
	if got := base.issued(); len(got) != 1 || got[0] != fastPathRuns {
		t.Fatalf("requests on the wire at a warm launch while the fast path's poll is in flight = %v, want only %s (AC16)", got, fastPathRuns)
	}

	// Released, the fast path emits first. Draining it before anything else waits on the
	// engine is not incidental: the Updates channel is unbuffered, so a poll blocked on an
	// undrained emit never reaches the deferred call that opens the gate, and anything waiting
	// on the gate would wait for ever.
	release()
	ev := recv(t, sched.Updates())
	if u, ok := ev.(scheduler.Update); !ok || u.Repo != current {
		t.Fatalf("the first event at a warm launch was %#v, want an Update for the launch repository %s", ev, current)
	}

	// A warm launch spends no pass at all, so the discovery goroutine returns as soon as the
	// gate lets it look.
	waitClosed(t, discovered, "the discovery goroutine to return on a warm launch")

	if ev = recvUpdateFor(t, sched.Updates(), domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "web"}); ev == nil {
		t.Fatal("the seeded set never revealed behind the fast path (R33)")
	}
	before := base.issuedBeforeFastPathAnswered(t)
	if len(before) != 1 || before[0] != fastPathRuns {
		t.Fatalf("requests issued before the fast path's Run listing was answered = %v, want exactly [%s] (AC16)", before, fastPathRuns)
	}
}

// waitClosed blocks until ch is closed, and fails rather than hanging if it never is. The
// bare receive is the tempting form and the wrong one: the thing being waited on here is a
// goroutine gated on the engine, and the engine's emit is a blocking send, so a mistake in
// the drain order deadlocks the whole run instead of reporting a line number.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// recvUpdateFor takes events off the engine's channel until one is an Update for want,
// discarding any other. It advances no clock: the reveal it waits for must arrive on the
// engine's own initiative, which is the R33 claim.
func recvUpdateFor(t *testing.T, ch <-chan scheduler.Event, want domain.RepoID) scheduler.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("the engine closed its channel before emitting")
			}
			if u, isUpdate := e.(scheduler.Update); isUpdate && u.Repo == want {
				return e
			}
		case <-deadline:
			t.Fatalf("the engine never emitted an Update for %s without a tier interval elapsing (R33)", want)
		}
	}
}
