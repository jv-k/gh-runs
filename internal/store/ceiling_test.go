package store

// White-box tests for the per-entry ceiling (R25, AC21). They live in package
// store, as disk_test.go's eviction tests do, to reach maxEntryBytes: the ceiling
// is not a knob (R25 inherits R9's argument), so a test lowers it here rather than
// building an 8 MiB payload to cross it.
//
// The property under test is what persist READ, not what it saved, and a cassette
// cannot observe that: go-vcr replays from a buffer, so every byte is already in
// memory before the store runs. The base is therefore a fake whose body counts the
// bytes handed out. transport_test.go keeps the positive control on the cassette
// seam, where a real API-shaped 200-with-ETag under the ceiling still persists and
// still revalidates free (AC5, AC6).

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

// countingBody is a response body that records how many bytes have been handed out
// and how many times it was closed. It is the whole instrument: the ceiling's
// buffering half is the claim that persist stops reading, and the only way to
// observe stopping is to count what was read at the instant persist returned.
type countingBody struct {
	src    *bytes.Reader
	read   int
	closed int
}

func newCountingBody(n int) *countingBody {
	return &countingBody{src: bytes.NewReader(bytes.Repeat([]byte("x"), n))}
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	b.read += n
	return n, err
}

func (b *countingBody) Close() error {
	b.closed++
	return nil
}

// ceilingTransport returns a Transport over a temp dir whose base answers every GET
// with a 200 carrying an ETag and the given body, and the body itself so a test can
// read its counters.
func ceilingTransport(t *testing.T, size int) (*Transport, *countingBody, string) {
	t.Helper()
	body := newCountingBody(size)
	base := stubRoundTrip(func(*http.Request) *http.Response {
		return ok200Body(`W/"ceiling"`, body)
	})
	dir := t.TempDir()
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	return NewTransport(base, dir, clk), body, dir
}

// getUndrained issues one GET through the transport and returns the response without
// reading it, so a caller can inspect what persist read before anything else does.
func getUndrained(t *testing.T, tr *Transport, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET: %v", err)
	}
	req.Header.Set("Authorization", "token dummy-fixed-token")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	return resp
}

// withCeiling lowers maxEntryBytes for one test and restores it, as the eviction
// tests lower maxStoreBytes. Neither is a knob in the product.
func withCeiling(t *testing.T, n int64) {
	t.Helper()
	orig := maxEntryBytes
	maxEntryBytes = n
	t.Cleanup(func() { maxEntryBytes = orig })
}

// TestOverTheCeilingIsNeitherBufferedNorSaved is the buffering half of R25 and the
// first half of AC21. A response past the ceiling must cost the ceiling in memory
// and not its own size, which is the cost declining to SAVE does not reclaim: the
// io.ReadAll this replaces ran before the ETag was ever looked at, so a response the
// store then declined had already been held whole.
func TestOverTheCeilingIsNeitherBufferedNorSaved(t *testing.T) {
	withCeiling(t, 1<<10)
	const size = 64 << 10 // 64x the ceiling
	tr, body, dir := ceilingTransport(t, size)

	resp := getUndrained(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")

	// The instant that matters: persist has returned and the caller has read
	// nothing. Exactly ceiling+1 bytes were pulled, the one byte past the ceiling
	// being how the store learned it was over.
	if got, want := body.read, int(maxEntryBytes)+1; got != want {
		t.Errorf("persist read %d bytes before returning, want %d (the ceiling plus the one byte that proves it was crossed): a %d-byte response must not be buffered whole", got, want, size)
	}
	if n := len(entryFiles(t, dir)); n != 0 {
		t.Errorf("an over-ceiling response left %d local-store entries, want none", n)
	}

	// R25's other half: declining must never cost the caller its body. The stream
	// is handed back with the prefix stitched in front of the untouched remainder.
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the declined body: %v", err)
	}
	if len(got) != size {
		t.Fatalf("caller received %d bytes, want %d: declining to cache truncated the response", len(got), size)
	}
	if !bytes.Equal(got, bytes.Repeat([]byte("x"), size)) {
		t.Error("caller received the wrong bytes: the prefix and the remainder were not stitched in order")
	}

	// The caller owns the declined body, so its Close must reach the original.
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close the declined body: %v", err)
	}
	if body.closed == 0 {
		t.Error("closing the declined body did not close the underlying stream, so a declined response leaks its connection")
	}
}

// TestTheCeilingBoundary pins where the line falls. Exactly at the ceiling is a
// resource and is kept; one byte past it is a payload and is not. An off-by-one
// here is invisible in production and silently stops caching the Feed's own poll.
func TestTheCeilingBoundary(t *testing.T) {
	t.Run("exactly at the ceiling is saved", func(t *testing.T) {
		withCeiling(t, 4<<10)
		tr, body, dir := ceilingTransport(t, int(maxEntryBytes))
		getThrough(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")
		if n := len(entryFiles(t, dir)); n != 1 {
			t.Errorf("a response exactly at the ceiling left %d entries, want 1", n)
		}
		if got := body.read; got != int(maxEntryBytes) {
			t.Errorf("read %d bytes, want %d (the whole body, which is what saving it requires)", got, maxEntryBytes)
		}
	})

	t.Run("one byte past the ceiling is declined", func(t *testing.T) {
		withCeiling(t, 4<<10)
		tr, _, dir := ceilingTransport(t, int(maxEntryBytes)+1)
		getThrough(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")
		if n := len(entryFiles(t, dir)); n != 0 {
			t.Errorf("a response one byte past the ceiling left %d entries, want none", n)
		}
	})
}

// TestUnderTheCeilingIsUnchanged is the regression guard. Every response the store
// exists to hold sits under the ceiling, so the common path must behave exactly as
// it did: fully buffered, saved, and handed back re-readable.
func TestUnderTheCeilingIsUnchanged(t *testing.T) {
	withCeiling(t, 4<<10)
	const size = 512
	tr, body, dir := ceilingTransport(t, size)

	resp := getUndrained(t, tr, "https://api.github.com/repos/cli/cli/actions/runs")
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if len(got) != size {
		t.Errorf("caller received %d bytes, want %d", len(got), size)
	}
	if body.read != size {
		t.Errorf("persist read %d bytes, want the whole %d-byte body", body.read, size)
	}
	if n := len(entryFiles(t, dir)); n != 1 {
		t.Fatalf("an under-ceiling response left %d entries, want 1", n)
	}

	// The entry carries the repository tag, so invalidation can still reach it
	// (R10, R14). A ceiling that quietly changed what was written would show here.
	e := readEntry(t, dir)
	if e.Repo != "github.com/cli/cli" {
		t.Errorf("entry Repo = %q, want github.com/cli/cli", e.Repo)
	}
	if len(e.Body) != size {
		t.Errorf("entry body is %d bytes, want %d", len(e.Body), size)
	}
}

// TestDecliningLeavesAPriorEntryAlone is the case a resource crossing the ceiling
// between two requests produces. The decision is that declining touches only the
// new response: the old entry keeps its ETag, its last-revalidated time stops
// advancing, so it ages to the front of the eviction queue and falls out when space
// is needed. It can never serve stale data, because reconstitute runs only on a
// 304 and a 304 is the server asserting that body is current, and if the resource
// shrinks back under the ceiling the entry is useful again.
func TestDecliningLeavesAPriorEntryAlone(t *testing.T) {
	withCeiling(t, 4<<10)
	dir := t.TempDir()
	clk := clockwork.NewFakeClockAt(time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))

	small := newCountingBody(512)
	large := newCountingBody(int(maxEntryBytes) + 1)
	bodies := []*countingBody{small, large}
	var n int
	base := stubRoundTrip(func(*http.Request) *http.Response {
		resp := ok200Body(`W/"v`+strconv.Itoa(n)+`"`, bodies[n])
		n++
		return resp
	})

	tr := NewTransport(base, dir, clk)
	const url = "https://api.github.com/repos/cli/cli/actions/runs"

	getThrough(t, tr, url)
	files := entryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("the first response left %d entries, want 1", len(files))
	}
	before, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read the prior entry: %v", err)
	}

	// The resource has now grown past the ceiling. The store must decline it and
	// leave what it already holds alone.
	getThrough(t, tr, url)

	after, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("the prior entry was removed by a declining persist: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a declining persist rewrote the prior entry, which it must leave untouched for LRU to reclaim")
	}
	if got := len(entryFiles(t, dir)); got != 1 {
		t.Errorf("store holds %d entries, want the 1 it already held", got)
	}
}
