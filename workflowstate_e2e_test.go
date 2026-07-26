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

// orphanRT answers the two requests the join needs: a Run listing whose Runs belong to
// Workflow 9001, and a Workflow listing that reports 9001 as deleted. It is the shape the
// API serves, not a seam: the whole chain below the engine is real.
type orphanRT struct{}

func (orphanRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"total_count":1,"workflow_runs":[{"id":42,"workflow_id":9001,"run_number":18,"status":"completed","conclusion":"failure","name":"Old Pipeline"}]}`
	if strings.Contains(req.URL.Path, "/actions/workflows") {
		body = `{"total_count":1,"workflows":[{"id":9001,"name":"Old Pipeline","path":".github/workflows/old.yml","state":"deleted"}]}`
	}
	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + req.URL.Path + `-v1"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}, nil
}

// TestOrphanedRunReachesThePaintedFrame drives the whole chain issue #57 names, with no
// seam faked: a real Scheduler over the real transport chain polls a repository whose
// Workflow has been deleted, resolves the Workflow list through the real lister main.go
// wires, stamps the join onto the Run, and the event crosses the real Updates channel to
// the real root, whose real Feed opens the real detail pane and paints R8's marker.
//
// The per-seam tests each prove their own half. This is the one that would catch the
// halves being wired to each other wrongly, which is the defect this issue is: every
// half already worked, and nothing joined them, so the marker had never once lit up.
func TestOrphanedRunReachesThePaintedFrame(t *testing.T) {
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "legacy"}
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 15, 16, 39, 0, 0, time.UTC))

	gov := governor.New(limiter.New(orphanRT{}, limiter.Bound), clk)
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
		// The same seam main.go wires, built the same way, over the same client surface.
		Workflows: clients{shared: client}.workflowLister(),
	})
	sched.Start(t.Context())
	t.Cleanup(sched.Stop)

	root := tui.New(tui.Options{
		Updates: sched.Updates(),
		Readout: gov.Readout,
		Profile: keys.Standard,
	})
	var m tea.Model = root
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	ev := recv(t, sched.Updates())
	up, ok := ev.(scheduler.Update)
	if !ok {
		t.Fatalf("the engine emitted %T for a 200, want scheduler.Update", ev)
	}
	if len(up.Runs) != 1 {
		t.Fatalf("the Update carried %d Runs, want 1", len(up.Runs))
	}
	if got := up.Runs[0].WorkflowState; got != domain.StateDeleted {
		t.Fatalf("the Run left the engine stamped %q, want %q (ADR-0014's join)", got, domain.StateDeleted)
	}
	m, _ = m.Update(ev)

	// Open the detail pane over the Run the Feed just revealed, the one call site R8's
	// marker has.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	frame := m.View().Content
	if !strings.Contains(frame, "Workflow deleted") {
		t.Fatalf("the Orphaned Run's marker never reached the painted frame (R8, AC11):\n%s", frame)
	}
	t.Logf("orphaned frame:\n%s", frame)
}
