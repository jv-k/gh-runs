package store_test

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

// testToken is a FIXED dummy token, never a real one. local-store R20 hashes the
// Authorization header into the store key, so the token must be stable between
// record and replay or the key would shift underneath the test. The matcher is
// given nothing that would let it vary.
const testToken = "dummy-fixed-token"

// wantBody is the payload the tape returns on the 200 and the store re-serves on
// every reconstituted 304.
const wantBody = `[{"id":1,"status":"completed"}]`

// TestTransportChainRevalidates proves the whole floor end to end against a
// cassette: three identical GETs through the full chain
// (ghclient -> go-gh -> store -> governor -> tape) produce 1x200 + 2x304 on the
// wire, the governor accounts primaryUsed == 1 rather than 3, and the caller
// receives the response body on all three because the store reconstitutes the
// 304s, so it never sees a 304 or an *api.HTTPError. This is local-store
// AC5/AC6 and rate-governor R7, proved rather than assumed.
func TestTransportChainRevalidates(t *testing.T) {
	rec, err := recorder.New("testdata/store_revalidation",
		recorder.WithMode(recorder.ModeReplayOnly),
		// The v4 default matcher compares the full header map, which is what makes
		// the conditional request's If-None-Match part of the match and stops AC5
		// passing vacuously (ADR-0013). WithIgnoreHeaders is case-sensitive and
		// does a raw map delete, so the two go-gh injects that break replay are
		// spelled in canonical form exactly as go-gh sends them: Time-Zone
		// (machine-local) and User-Agent (version-bound). Authorization is stable
		// here because the token is fixed, so it need not be ignored.
		recorder.WithMatcher(cassette.NewDefaultMatcher(
			cassette.WithIgnoreHeaders("Time-Zone", "User-Agent"),
		)),
		// One taped conditional interaction answers both re-requests.
		recorder.WithReplayableInteractions(true),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder: %v", err)
		}
	})

	// A fake clock: every test in this feature advances virtual time rather than
	// sleeping (local-store AC17, rate-governor R21).
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))

	// The chain main.go assembles, with the cassette as the injected base
	// (local-store R19a) and the store's own directory in a per-test temp dir.
	storeDir := t.TempDir()
	gov := governor.New(rec, clk)
	transport := store.NewTransport(gov, storeDir, clk)

	client, err := ghclient.New(ghclient.Options{
		AuthToken: testToken,
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	const path = "repos/cli/cli/actions/runs/12345"
	for i := 1; i <= 3; i++ {
		resp, err := client.Request(http.MethodGet, path, nil)
		if err != nil {
			t.Fatalf("request %d returned an error, so a 304 leaked instead of being reconstituted: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("request %d: read body: %v", i, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("request %d: close body: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: caller saw status %d, want 200 (the store must reconstitute the 304)", i, resp.StatusCode)
		}
		if string(body) != wantBody {
			t.Errorf("request %d: body = %q, want %q", i, body, wantBody)
		}
		t.Logf("request %d: caller saw status=%d, body=%q", i, resp.StatusCode, body)
	}

	t.Logf("wire totals: 200s(primaryUsed)=%d, 304s(revalidations)=%d, observed x-ratelimit-remaining=%d",
		gov.PrimaryUsed(), gov.Revalidations(), gov.Remaining())

	// rate-governor R7: one 200 costs one primary point, and each of the two 304s
	// costs zero. The governor's tally is 1, not 3.
	if got := gov.PrimaryUsed(); got != 1 {
		t.Errorf("governor primaryUsed = %d, want 1 (one 200 plus two free 304s)", got)
	}
	// The two conditional round trips reached the governor as 304s, below the
	// store's reconstitution (ADR-0012). This is the 2x304 half of the wire count.
	if got := gov.Revalidations(); got != 2 {
		t.Errorf("governor saw %d revalidations (304s), want 2", got)
	}
	// rate-governor R5: the governor read x-ratelimit-remaining at the transport
	// layer, from the headers that arrive free on every response.
	if got := gov.Remaining(); got != 4811 {
		t.Errorf("governor observed remaining = %d, want 4811", got)
	}

	// local-store R20/AC14a: the token appears in no filename and no file the
	// store wrote. The key is a SHA256 hash that folds Authorization in, so it
	// leaks no recoverable form of the token to disk. Grep the whole store
	// directory for the token's characters.
	assertNoTokenOnDisk(t, storeDir)
}

// assertNoTokenOnDisk walks dir and fails if the fixed token appears in any file
// name or file content (local-store AC14a).
func assertNoTokenOnDisk(t *testing.T, dir string) {
	t.Helper()
	var files int
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files++
		if strings.Contains(filepath.Base(path), testToken) {
			t.Errorf("token leaked into a filename: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), testToken) {
			t.Errorf("token leaked into file contents: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store dir: %v", err)
	}
	if files == 0 {
		t.Fatal("store persisted no files, so the persistence path was never exercised")
	}
	t.Logf("scanned %d persisted file(s) under the store dir; the token appears in none", files)
}
