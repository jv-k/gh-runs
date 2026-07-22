package store

// White-box tests for the on-disk store's mechanics: the schema version (R12),
// corruption tolerance (R13), the host-qualified repository tag (R14), write
// invalidation's in-memory half (R23), eviction (R15) and the advisory lock
// (R21, R22). These observe behaviour at two seams the acceptance criteria
// themselves use: the http.RoundTripper, and the files the store leaves on disk.
// They live in package store, rather than store_test, only to reach the two
// knobs the spec forbids exposing publicly: the eviction bound (R15, "not a
// knob") and lock release (R21, held for a process's lifetime, released here to
// stand in for a second process). The network is a roundTripFunc, because none
// of these properties is about what the API said: the conditional-request
// correctness that needs a real cassette is proved end to end in
// transport_test.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

// roundTripFunc is an in-process base RoundTripper. It records the If-None-Match
// header of the last request it saw, so a test can assert whether the store sent
// a conditional request, and returns whatever the test configures.
type roundTripFunc struct {
	lastIfNoneMatch string
	seen            int
	respond         func(req *http.Request) *http.Response
}

func (f *roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	f.lastIfNoneMatch = req.Header.Get("If-None-Match")
	f.seen++
	return f.respond(req), nil
}

// stubRoundTrip is a stateless base RoundTripper. It records nothing, so it is
// safe for concurrent use without synchronisation, which roundTripFunc is not.
// The concurrency test uses it so that the only shared state under -race is the
// store's own.
type stubRoundTrip func(*http.Request) *http.Response

func (f stubRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

// ok200 builds a 200 carrying an ETag and body, the shape the store persists.
func ok200(etag, body string) *http.Response {
	h := make(http.Header)
	h.Set("ETag", etag)
	h.Set("Content-Type", "application/json; charset=utf-8")
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// notModified builds a bare 304, the shape a conditional request receives.
func notModified(etag string) *http.Response {
	h := make(http.Header)
	h.Set("ETag", etag)
	return &http.Response{
		StatusCode: http.StatusNotModified,
		Status:     "304 Not Modified",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// noContent204 builds a 204, the shape a successful write receives.
func noContent204() *http.Response {
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// writeThrough issues one non-GET through the transport and drains the response.
func writeThrough(t *testing.T, tr *Transport, method, url string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build %s request: %v", method, err)
	}
	req.Header.Set("Authorization", "token dummy-fixed-token")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("%s round trip: %v", method, err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("%s read body: %v", method, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("%s close body: %v", method, err)
	}
}

// getThrough drives one GET through the transport and returns the body served to
// the caller. It closes the response body, so bodyclose stays satisfied.
func getThrough(t *testing.T, tr *Transport, url string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "token dummy-fixed-token")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	return string(body)
}

// entryFiles lists the persisted entry files under dir, excluding the lock.
func entryFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob entries: %v", err)
	}
	return matches
}

// readEntry decodes the one persisted entry file, failing if there is not
// exactly one.
func readEntry(t *testing.T, dir string) entry {
	t.Helper()
	files := entryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("want exactly one persisted entry, found %d: %v", len(files), files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	return e
}

// TestUnknownSchemaIsDiscarded pins R12/AC12: a persisted entry whose schema
// version the binary does not recognise is discarded and rebuilt, never trusted.
// Observable at the transport seam: a trusted entry would make the next request
// conditional (If-None-Match), and a discarded one makes it unconditional.
func TestUnknownSchemaIsDiscarded(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	base := &roundTripFunc{respond: func(*http.Request) *http.Response {
		return ok200(`"etag-1"`, `{"ok":true}`)
	}}
	tr := NewTransport(base, dir, clk)

	const url = "https://api.github.com/repos/cli/cli/actions/runs"

	// Seed a good entry.
	getThrough(t, tr, url)
	if base.lastIfNoneMatch != "" {
		t.Fatalf("first GET was conditional, but nothing was cached yet")
	}

	// Rewrite the persisted entry with a schema version from the future.
	files := entryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("seed persisted %d entries, want 1", len(files))
	}
	var raw map[string]any
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read seeded entry: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode seeded entry: %v", err)
	}
	raw["schema"] = 999
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-encode entry: %v", err)
	}
	if err := os.WriteFile(files[0], out, 0o600); err != nil {
		t.Fatalf("write future-schema entry: %v", err)
	}

	// The next GET must ignore the unrecognised entry and go out unconditional.
	getThrough(t, tr, url)
	if base.lastIfNoneMatch != "" {
		t.Errorf("GET carried If-None-Match %q against an unknown-schema entry; R12 requires it be discarded, not revalidated", base.lastIfNoneMatch)
	}

	// It was rebuilt at the current schema, so a later GET revalidates again.
	if got := readEntry(t, dir).Schema; got != schemaVersion {
		t.Errorf("rebuilt entry schema = %d, want %d", got, schemaVersion)
	}
}

// TestCorruptEntryIsDiscarded pins R13/AC13: an unreadable, truncated or
// malformed entry never fails a request. It is treated as a miss and rebuilt, so
// the request goes out unconditional and the caller still gets a body. Observed
// at the transport seam, one corruption per case.
func TestCorruptEntryIsDiscarded(t *testing.T) {
	corruptions := map[string]string{
		"empty file":                  "",
		"truncated json":              `{"schema":1,"etag":"`,
		"not json at all":             "\x00\x01\x02 not json",
		"valid json, no schema field": `{"etag":"\"etag-1\"","body":"eyJvayI6dHJ1ZX0="}`,
	}
	for name, contents := range corruptions {
		t.Run(name, func(t *testing.T) {
			clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
			dir := t.TempDir()
			base := &roundTripFunc{respond: func(*http.Request) *http.Response {
				return ok200(`"etag-1"`, `{"ok":true}`)
			}}
			tr := NewTransport(base, dir, clk)

			const url = "https://api.github.com/repos/cli/cli/actions/runs"

			// Seed a good entry, then overwrite it with the corruption under test.
			getThrough(t, tr, url)
			files := entryFiles(t, dir)
			if len(files) != 1 {
				t.Fatalf("seed persisted %d entries, want 1", len(files))
			}
			if err := os.WriteFile(files[0], []byte(contents), 0o600); err != nil {
				t.Fatalf("write corrupt entry: %v", err)
			}

			// The launch must not fail: the request succeeds, unconditional.
			if body := getThrough(t, tr, url); body != `{"ok":true}` {
				t.Errorf("caller got %q, want the rebuilt body", body)
			}
			if base.lastIfNoneMatch != "" {
				t.Errorf("GET carried If-None-Match %q against a corrupt entry; R13 requires it be discarded", base.lastIfNoneMatch)
			}
		})
	}
}

// TestPersistedRepoIsHostQualified pins R14/AC14: a persisted entry records its
// owning repository host-qualified as host/owner/name, never the bare
// owner/name, so a later write can invalidate it (R10) and no key can be
// constructed without a host (ADR-0009). The API host api.github.com maps to the
// repository host github.com.
func TestPersistedRepoIsHostQualified(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	base := &roundTripFunc{respond: func(*http.Request) *http.Response {
		return ok200(`"etag-1"`, `{"ok":true}`)
	}}
	tr := NewTransport(base, dir, clk)

	getThrough(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")

	got := readEntry(t, dir).Repo
	const want = "github.com/cli/cli"
	if got != want {
		t.Errorf("persisted repo = %q, want %q (host-qualified, R14)", got, want)
	}
	if got == "cli/cli" {
		t.Errorf("persisted repo is bare owner/name; AC14 fails if a host is ever missing")
	}
}

// TestEvictsOldestByLastRevalidated pins R15/AC15: at write time, when the store
// would exceed its total bound, the oldest entries by last-revalidated time are
// evicted first, and no newer entry is ever evicted while an older one remains.
// The bound is a compiled constant with no user knob (R15); the test lowers it
// through the package var, which is why it is white-box.
func TestEvictsOldestByLastRevalidated(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	base := &roundTripFunc{respond: func(*http.Request) *http.Response {
		return ok200(`"etag-x"`, `{"payload":"fixed-size-body-so-entries-weigh-the-same"}`)
	}}
	tr := NewTransport(base, dir, clk)

	// Three repositories, each polled once, one virtual minute apart. r1 is the
	// oldest last-revalidated, r3 the newest of the three.
	for _, r := range []string{"r1", "r2", "r3"} {
		getThrough(t, tr, "https://api.github.com/repos/o/"+r+"/actions/runs")
		clk.Advance(time.Minute)
	}
	seeded := entryFiles(t, dir)
	if len(seeded) != 3 {
		t.Fatalf("seed persisted %d entries, want 3", len(seeded))
	}

	// Pin the bound to two entries' worth. The entries are equal weight, so a
	// fourth write must evict down to two.
	one, err := os.Stat(seeded[0])
	if err != nil {
		t.Fatalf("stat seeded entry: %v", err)
	}
	orig := maxStoreBytes
	maxStoreBytes = 2 * one.Size()
	defer func() { maxStoreBytes = orig }()

	// r4 is the newest write. It must survive; the two oldest (r1, r2) must go.
	getThrough(t, tr, "https://api.github.com/repos/o/r4/actions/runs")

	got := map[string]bool{}
	for _, f := range entryFiles(t, dir) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read surviving entry: %v", err)
		}
		var e entry
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("decode surviving entry: %v", err)
		}
		got[e.Repo] = true
	}

	want := map[string]bool{"github.com/o/r3": true, "github.com/o/r4": true}
	if len(got) != len(want) {
		t.Fatalf("store holds %d entries after eviction, want %d: %v", len(got), len(want), got)
	}
	for repo := range want {
		if !got[repo] {
			t.Errorf("evicted %s but it is newer than a survivor; AC15 forbids evicting a newer entry while an older remains", repo)
		}
	}
	for _, evicted := range []string{"github.com/o/r1", "github.com/o/r2"} {
		if got[evicted] {
			t.Errorf("%s survived; the oldest last-revalidated entries must be evicted first", evicted)
		}
	}
}

// TestAdvisoryLockAndDegradedReader pins R21, R22, R23 and AC18 over one store
// directory. flock(2) denies a second open even within this process, so two
// transports over one directory stand in for two processes: the first is the
// writer, the second degrades to a reader that reads and revalidates but never
// writes. It also pins the empty lock file (R22), the in-memory invalidation a
// reader applies without reaching the persisted entry (R23), re-acquisition after
// the holder releases (R22's kernel release), and the derived lock file being
// safe to delete mid-run (R11).
func TestAdvisoryLockAndDegradedReader(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()

	const url = "https://api.github.com/repos/cli/cli/actions/runs"
	const deleteURL = "https://api.github.com/repos/cli/cli/actions/runs/999"

	// A GET revalidates for free when conditional (304) and fetches otherwise
	// (200); a write answers 204. This lets the test tell a free revalidation from
	// a full fetch, so a reader's still-conditional read is visible (R23).
	respond := func(req *http.Request) *http.Response {
		switch {
		case req.Method != http.MethodGet:
			return noContent204()
		case req.Header.Get("If-None-Match") != "":
			return notModified(`"etag-1"`)
		default:
			return ok200(`"etag-1"`, `{"ok":true}`)
		}
	}

	// First process: acquires the lock and persists one entry.
	base1 := &roundTripFunc{respond: respond}
	w := NewTransport(base1, dir, clk)
	t.Cleanup(func() { _ = w.releaseLock() })
	if !w.writer {
		t.Fatal("the first transport did not acquire the lock; exactly one writer is required (R21)")
	}
	getThrough(t, w, url)
	if n := len(entryFiles(t, dir)); n != 1 {
		t.Fatalf("writer persisted %d entries, want 1", n)
	}

	// Second process over the same directory: no lock, degrades to a reader.
	base2 := &roundTripFunc{respond: respond}
	r := NewTransport(base2, dir, clk)
	t.Cleanup(func() { _ = r.releaseLock() })
	if r.writer {
		t.Fatal("the second transport acquired the lock; a second process must degrade to a reader (R21)")
	}

	// The reader reads the writer's entry and revalidates for free: its request
	// carries If-None-Match and it serves the stored body from the 304 (R23).
	if body := getThrough(t, r, url); body != `{"ok":true}` {
		t.Errorf("reader served %q, want the stored body", body)
	}
	if base2.lastIfNoneMatch == "" {
		t.Error("the reader's revalidation was unconditional; its 304s must stay free (R23)")
	}
	if n := len(entryFiles(t, dir)); n != 1 {
		t.Errorf("the reader wrote to the store (now %d entries); a degraded reader must never write (R21)", n)
	}

	// The lock file is empty and stays empty (R22).
	assertLockEmpty(t, dir)

	// R23: the reader's own successful write invalidates its in-memory view, so its
	// next read of that repository goes out unconditional, yet it never reaches the
	// persisted entry, which the writer still owns.
	writeThrough(t, r, http.MethodDelete, deleteURL)
	base2.lastIfNoneMatch = ""
	getThrough(t, r, url)
	if base2.lastIfNoneMatch != "" {
		t.Error("after its own write the reader still revalidated the repository; R23 requires in-memory invalidation")
	}
	if n := len(entryFiles(t, dir)); n != 1 {
		t.Errorf("the reader removed the persisted entry (now %d); R23 forbids it reaching the persisted entry", n)
	}

	// R22: the kernel releases the lock when the holder ends. Releasing the
	// writer's lock lets a third process acquire it and write.
	if err := w.releaseLock(); err != nil {
		t.Fatalf("release the writer's lock: %v", err)
	}
	base3 := &roundTripFunc{respond: respond}
	third := NewTransport(base3, dir, clk)
	t.Cleanup(func() { _ = third.releaseLock() })
	if !third.writer {
		t.Error("the third transport did not acquire the freed lock; a released lock must be re-acquirable (R22)")
	}

	// R11: the lock file is derived. Deleting it mid-run costs nothing.
	if err := os.Remove(filepath.Join(dir, lockName)); err != nil {
		t.Fatalf("remove the lock file: %v", err)
	}
	if body := getThrough(t, third, url); body == "" {
		t.Error("a read failed after the lock file was deleted; the lock is derived and deleting it must cost nothing (R11)")
	}
}

// TestConcurrentUseIsSafe exercises the http.RoundTripper concurrency contract:
// go-gh calls RoundTrip from many poll goroutines at once, so a writer's persists,
// eviction sweep and invalidations must not corrupt each other (AC18, "no corrupt
// entry and no partially-written one"). Run under -race, it fails on any data
// race; after the goroutines join, every persisted entry must still decode.
func TestConcurrentUseIsSafe(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	base := stubRoundTrip(func(req *http.Request) *http.Response {
		if req.Method != http.MethodGet {
			return noContent204()
		}
		return ok200(`"etag-c"`, `{"payload":"a body of some length to weigh entries"}`)
	})
	tr := NewTransport(base, dir, clk)
	t.Cleanup(func() { _ = tr.releaseLock() })

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			repo := "https://api.github.com/repos/o/r" + strconv.Itoa(n%6)
			// Poll the repository, then delete within it: persist races invalidate.
			resp, err := tr.RoundTrip(mustGet(repo + "/actions/runs"))
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			resp, err = tr.RoundTrip(mustDelete(repo + "/actions/runs/1"))
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()

	// Every file the store left behind must be a whole, decodable entry: an atomic
	// rename means no half-written JSON is ever visible.
	for _, f := range entryFiles(t, dir) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read entry %s: %v", f, err)
		}
		var e entry
		if err := json.Unmarshal(data, &e); err != nil {
			t.Errorf("entry %s did not decode, so a partial write was observed: %v", f, err)
		}
	}
}

// mustGet and mustDelete build authenticated requests for the concurrency test.
func mustGet(url string) *http.Request    { return mustReq(http.MethodGet, url) }
func mustDelete(url string) *http.Request { return mustReq(http.MethodDelete, url) }

func mustReq(method, url string) *http.Request {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "token dummy-fixed-token")
	req.Header.Set("Accept", "application/vnd.github+json")
	return req
}

// assertLockEmpty fails if the lock file is absent or carries any bytes: nothing
// reads it, so it holds no PID and no schema (R22).
func assertLockEmpty(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("lock file holds %d bytes; it must be empty and stay empty (R22)", info.Size())
	}
}
