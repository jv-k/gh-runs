package store_test

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// TestWriteInvalidatesRepo proves local-store R10/AC10 end to end against a
// cassette: a GET of a repository's Runs listing persists an entry, then a
// successful DELETE of a Run in that repository invalidates it, so no subsequent
// cold start would serve the stale listing from the store. The DELETE is taped,
// never live: this tool deletes irreversibly and deletion is exercised against
// cassettes alone.
func TestWriteInvalidatesRepo(t *testing.T) {
	rec, err := recorder.New("testdata/store_invalidation",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(cassette.NewDefaultMatcher(
			cassette.WithIgnoreHeaders("Time-Zone", "User-Agent", "Content-Type"),
		)),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder: %v", err)
		}
	})

	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	storeDir := t.TempDir()
	gov := governor.New(rec, clk)
	transport := store.NewTransport(gov, storeDir, clk)

	client, err := ghclient.New(ghclient.Options{
		AuthToken: "dummy-fixed-token",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	// The GET persists exactly one entry for github.com/cli/cli.
	drain(t, client, http.MethodGet, "repos/cli/cli/actions/runs")
	if n := len(entryJSON(t, storeDir)); n != 1 {
		t.Fatalf("after GET the store holds %d entries, want 1", n)
	}

	// A successful DELETE in the same repository invalidates its cached entries.
	drain(t, client, http.MethodDelete, "repos/cli/cli/actions/runs/999")
	if n := len(entryJSON(t, storeDir)); n != 0 {
		t.Errorf("after a successful DELETE the store still holds %d entries; R10 requires the repository's entries be invalidated", n)
	}
}

// drain issues one request through the client and discards the body, so
// bodyclose stays satisfied and the transport observes the exchange.
func drain(t *testing.T, client *ghclient.Client, method, path string) {
	t.Helper()
	resp, err := client.Request(method, path, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("%s %s: drain body: %v", method, path, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("%s %s: close body: %v", method, path, err)
	}
}

// entryJSON lists the persisted entry files under dir, excluding the lock.
func entryJSON(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob entries: %v", err)
	}
	return matches
}
