package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// adoptRT is an account whose enumeration returns one repository, launched from inside a
// different one: a clone the account does not own, which /user/repos will never return.
// That is the case repo-discovery R22 exists for, and ADR-0020 added adoption for.
type adoptRT struct {
	mu    sync.Mutex
	paths []string
}

func (r *adoptRT) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()

	body := r.bodyFor(path)
	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + path + `"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}, nil
}

// bodyFor answers the four resources this account serves. The clone is absent from the
// enumeration and present at its own metadata path, which is the whole shape of R22: the
// only way to learn its capability is to ask for it directly.
func (r *adoptRT) bodyFor(path string) string {
	switch {
	case path == "/user/repos":
		return `[{"name":"api","full_name":"acme/api","owner":{"login":"acme"},"permissions":{"admin":true,"push":true}}]`
	case path == "/repos/someone-else/clone":
		return `{"name":"clone","full_name":"someone-else/clone","owner":{"login":"someone-else"},
		         "archived":false,"disabled":false,"permissions":{"admin":false,"push":true}}`
	case strings.HasSuffix(path, "/actions/runs"):
		return `{"total_count":1,"workflow_runs":[{"id":101,"name":"ci","status":"completed",
		         "conclusion":"success","workflow_id":9,"run_started_at":"2026-07-25T08:55:00Z"}]}`
	case strings.HasSuffix(path, "/actions/workflows"):
		return `{"total_count":1,"workflows":[{"id":9,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`
	}
	return `{}`
}

func (r *adoptRT) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path {
			n++
		}
	}
	return n
}

// TestALaunchRepositoryEnumerationNeverReturnsIsAdoptedForTheSession is repo-discovery
// R22 driven through the composition main.go assembles, which is where it was unreached
// (issue #100).
//
// A clone the account does not own never appears in /user/repos, so no pass will ever
// record it. Without adoption it ends the session with no record at all, which reads as
// not-yet-known capability: the tri-state fails closed, so nothing unsafe is offered, but
// nothing is ever withdrawn either. A Purge stays permanently unavailable in the one
// repository the operator is sitting in and can plainly write to.
//
// The assertion is the acceptance criterion in the form the issue states it: the session
// ends with a Known capability, and the repository is in the poll set so the Feed keeps
// painting it.
func TestALaunchRepositoryEnumerationNeverReturnsIsAdoptedForTheSession(t *testing.T) {
	clone := domain.RepoID{Host: domain.HostGitHub, Owner: "someone-else", Name: "clone"}

	base := &adoptRT{}
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, t.TempDir(), clk)
	cl, err := newClients(transport, gov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}

	disc := discovery.New(discovery.Options{
		Client: cl.shared, Store: transport, Budget: gov, Clock: clk,
	})
	seeded := disc.Reload()

	sched := scheduler.New(scheduler.Options{
		Client:     cl.shared,
		PollSet:    disc,
		First:      clone,
		Classified: classifiedBy(disc),
		Budget:     gov,
		Clock:      clock.Clock(clk),
		Workflows:  cl.workflowLister(),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sched.Start(ctx)
	t.Cleanup(sched.Stop)
	// The engine's event channel is unbuffered, and a poll emits before the deferred call
	// that opens the fast path's gate. An undrained channel therefore holds the gate shut
	// and nothing behind it ever runs, so the drainer is a precondition of the test rather
	// than tidiness. The root plays this part in a real session.
	go func() {
		for range sched.Updates() {
		}
	}()

	// The gating runTUI performs, called as runTUI calls it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		discoverBehind(ctx, sched, disc, clone, seeded)
	}()
	waitClosed(t, done, "discovery and adoption to finish")

	if got := disc.Capability(clone); got != domain.CapabilityPermitted {
		t.Errorf("capability of the launch repository = %v, want permitted: R22's adoption never ran", got)
	}
	// R22 spends exactly one GET /repos/{owner}/{repo} for it.
	if got := base.count("/repos/someone-else/clone"); got != 1 {
		t.Errorf("adoption requests = %d, want exactly 1 (R22)", got)
	}
	// Admitted to the poll set, because it has Runs. Without this the scheduler drops it
	// the moment Classified starts reporting true, and the Feed goes quiet for it.
	inPollSet := false
	for _, id := range disc.PollSet() {
		if id == clone {
			inPollSet = true
		}
	}
	if !inPollSet {
		t.Errorf("poll set = %v, want it to carry the adopted launch repository", disc.PollSet())
	}

	// The enumerated member is unaffected: adoption is additive, not a replacement for the
	// pass.
	if got := disc.Capability(domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}); got != domain.CapabilityPermitted {
		t.Errorf("enumerated member capability = %v, want permitted", got)
	}
}

// TestAWarmLaunchStillAdoptsTheLaunchRepository pins the case a naive wiring misses. A
// warm local-store seeds the poll set and spends no discovery pass at all, and adoption
// hangs off the end of that pass in the obvious reading. R22 is not conditional on a pass
// having run: it is conditional on enumeration not holding the repository, which is just
// as true when the classification came from disk.
func TestAWarmLaunchStillAdoptsTheLaunchRepository(t *testing.T) {
	clone := domain.RepoID{Host: domain.HostGitHub, Owner: "someone-else", Name: "clone"}
	dir := t.TempDir()

	// First launch: a full pass, which persists the enumerated member's classification.
	base := &adoptRT{}
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC))
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, dir, clk)
	cl, err := newClients(transport, gov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}
	first := discovery.New(discovery.Options{Client: cl.shared, Store: transport, Budget: gov, Clock: clk})
	if err := first.Pass(context.Background(), nil); err != nil {
		t.Fatalf("seeding pass: %v", err)
	}

	// Second launch over the same store and the same transport, which is warm: Reload
	// returns the persisted set, so runTUI spends no pass.
	warm := discovery.New(discovery.Options{Client: cl.shared, Store: transport, Budget: gov, Clock: clk})
	seeded := warm.Reload()
	if seeded == 0 {
		t.Fatalf("precondition: the second launch reloaded %d records, want a warm store", seeded)
	}

	sched := scheduler.New(scheduler.Options{
		Client: cl.shared, PollSet: warm, First: clone, Classified: classifiedBy(warm),
		Budget: gov, Clock: clock.Clock(clk), Workflows: cl.workflowLister(),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sched.Start(ctx)
	t.Cleanup(sched.Stop)
	// The engine's event channel is unbuffered, and a poll emits before the deferred call
	// that opens the fast path's gate. An undrained channel therefore holds the gate shut
	// and nothing behind it ever runs, so the drainer is a precondition of the test rather
	// than tidiness. The root plays this part in a real session.
	go func() {
		for range sched.Updates() {
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		discoverBehind(ctx, sched, warm, clone, seeded)
	}()
	waitClosed(t, done, "the warm launch's adoption to finish")

	if got := warm.Capability(clone); got != domain.CapabilityPermitted {
		t.Errorf("capability on a warm launch = %v, want permitted: adoption was skipped with the pass", got)
	}
}
