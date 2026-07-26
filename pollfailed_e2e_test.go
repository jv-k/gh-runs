package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui"
)

// flakyRT answers the first n requests with a 502 and every request after that with a
// one-Run 200, so one transport drives a failure and its recovery in sequence.
type flakyRT struct {
	failures int
	n        int
}

func (f *flakyRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.n++
	h := http.Header{
		"Content-Type":          {"application/json"},
		"X-Ratelimit-Remaining": {"5000"},
	}
	if f.n <= f.failures {
		body := `{"message":"Server Error"}`
		return &http.Response{
			StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: h, Body: io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)), Request: req,
		}, nil
	}
	h.Set("Etag", `"v`+strings.Repeat("x", f.n)+`"`)
	body := `{"total_count":1,"workflow_runs":[{"id":7,"status":"completed","conclusion":"success","name":"CI"}]}`
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}, nil
}

type fixedPollSet []domain.RepoID

func (f fixedPollSet) PollSet() []domain.RepoID { return f }

// TestRepoPollFailedReachesThePaintedFrame drives the whole chain issue #54 names, with
// no seam faked: a real Scheduler over the real transport chain polls a repository that
// answers 502, the event crosses the real Updates channel, the real root broadcasts it,
// and the Feed's real View paints the indicator. Then the same repository answers 200
// and the indicator clears.
//
// The per-seam tests each prove their own half. This is the one that would catch the
// halves being wired to each other wrongly, which is the defect a widened channel type
// makes possible.
func TestRepoPollFailedReachesThePaintedFrame(t *testing.T) {
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 15, 16, 39, 0, 0, time.UTC))

	rt := &flakyRT{failures: 1}
	gov := governor.New(limiter.New(rt, limiter.Bound), clk)
	transport := store.NewTransport(gov, t.TempDir(), clk)
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	sched := scheduler.New(scheduler.Options{
		Client:  client,
		PollSet: fixedPollSet{id},
		Budget:  gov,
		Clock:   clock.Clock(clk),
	})
	sched.Start(t.Context())
	t.Cleanup(sched.Stop)

	root := tui.New(tui.Options{
		Updates: sched.Updates(),
		Readout: gov.Readout,
		Profile: keys.Standard,
	})
	var m tea.Model = root
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	// The failed poll, taken off the real channel exactly as the root's listen Cmd does.
	ev := recv(t, sched.Updates())
	if _, ok := ev.(scheduler.RepoPollFailed); !ok {
		t.Fatalf("the engine emitted %T for a 502, want scheduler.RepoPollFailed", ev)
	}
	m, _ = m.Update(ev)

	frame := m.View().Content
	if !strings.Contains(frame, "failed to update") || !strings.Contains(frame, "acme/api") {
		t.Fatalf("the failed poll never reached the painted frame:\n%s", frame)
	}
	t.Logf("failed frame:\n%s", frame)

	// Recovery: drive virtual time forward until the next poll lands, which now answers
	// 200.
	//
	// One advance is not enough, and a single BlockUntilContext does not make it enough.
	// The event above is emitted from inside poll, whose deferred clearInFlight has not
	// run when the test receives it, so a pollDue racing that window finds the
	// repository still in flight and skips it (ADR-0018's single-flight rule, working as
	// intended). The loop then re-arms and nothing further happens, because virtual time
	// only moves when this test moves it. Blocking on the timer does not close the
	// window either: a timer is already armed from before the failed poll, so
	// BlockUntilContext returns at once on that one.
	//
	// Advancing in a loop is the fix, because the skip is transient by construction. It
	// costs one extra iteration in the racing case and none otherwise.
	ev = advanceUntilEvent(t, clk, sched.Updates())
	if _, ok := ev.(scheduler.Update); !ok {
		t.Fatalf("the engine emitted %T for a 200, want scheduler.Update", ev)
	}
	m, _ = m.Update(ev)

	frame = m.View().Content
	if strings.Contains(frame, "failed to update") {
		t.Fatalf("the indicator survived the repository's recovery:\n%s", frame)
	}
	if !strings.Contains(frame, "acme/api") {
		t.Fatalf("the recovered repository's Run is not painted:\n%s", frame)
	}
	t.Logf("recovered frame:\n%s", frame)
}

// advanceUntilEvent moves the fake clock forward a tier interval at a time until the
// engine emits, blocking on the loop being asleep before each advance so no advance is
// issued into a loop that has not armed its timer.
//
// It exists because a scheduler test outside package scheduler cannot reach that
// package's settled and polled seams, which are unexported, so it cannot barrier on a
// poll goroutine having fully unwound. Advancing repeatedly gets the same determinism
// from the outside: the loop is asleep at every advance, and a repository skipped for
// being in flight is picked up by the next one.
func advanceUntilEvent(t *testing.T, clk *clockwork.FakeClock, ch <-chan scheduler.Event) scheduler.Event {
	t.Helper()
	for i := 0; i < 20; i++ {
		if err := clk.BlockUntilContext(t.Context(), 1); err != nil {
			t.Fatalf("waiting for the loop to arm its timer: %v", err)
		}
		clk.Advance(30 * time.Second) // slowTarget, the widest interval a poll set of one takes
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("the engine closed its channel before emitting")
			}
			return e
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("the engine emitted nothing across 20 tier intervals of virtual time")
	return nil
}

func recv(t *testing.T, ch <-chan scheduler.Event) scheduler.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the engine closed its channel before emitting")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an engine event")
	}
	return nil
}
