package store

import (
	"net/http"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestLastRevalidatedExposesEntryTime pins local-store R7: the store exposes each
// entry's last-revalidated time to its consumers, so a paused Feed can say what it is
// showing and as of when (live-run-feed R30). A repository with no entry reports that no
// time is known rather than the zero instant read as real.
func TestLastRevalidatedExposesEntryTime(t *testing.T) {
	start := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	clk := clockwork.NewFakeClockAt(start)
	dir := t.TempDir()
	base := &roundTripFunc{respond: func(req *http.Request) *http.Response {
		return ok200(`"etag-1"`, `{"ok":true}`)
	}}
	tr := NewTransport(base, dir, clk)

	cli := domain.RepoID{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}

	if _, ok := tr.LastRevalidated(cli); ok {
		t.Fatal("LastRevalidated reported a time before any entry existed (R7)")
	}

	// A 200 persists an entry stamped at the clock's now.
	getThrough(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")
	got, ok := tr.LastRevalidated(cli)
	if !ok {
		t.Fatal("LastRevalidated found no entry after a 200 persisted one (R7)")
	}
	if !got.Equal(start) {
		t.Errorf("LastRevalidated = %s, want the clock's %s (R3, R7)", got, start)
	}

	// A repository with no persisted entry is unknown, not the zero time.
	other := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "web"}
	if _, ok := tr.LastRevalidated(other); ok {
		t.Errorf("LastRevalidated reported a time for a repository with no entry (R7)")
	}
}
