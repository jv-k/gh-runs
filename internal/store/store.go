// Package store is the local store's http.RoundTripper (local-store R19).
//
// It is the innermost RoundTripper in go-gh's stack. opts.Transport replaces
// http.DefaultTransport rather than wrapping it, so ours has nothing beneath it
// and must dial through an injected base (R19a): http.DefaultTransport in
// production, a cassette in a test. It sends If-None-Match when it holds an ETag
// and reconstitutes a 200 from the resulting 304 (R19b), because a bare 304
// travelling upward becomes an *api.HTTPError inside go-gh, where every non-2xx
// is treated as a failure. The 304-versus-200 distinction is drawn below this
// reconstitution, for this component and the governor, and never above it.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// rateLimitPrefix marks the headers that describe an exchange rather than an
// entity. On a reconstituted 200 they come from the fresh 304 and never from the
// stored copy, because a persisted counter would be a stale reading of a live one
// (ADR-0012). Go canonicalises x-ratelimit-* to this casing.
const rateLimitPrefix = "X-Ratelimit-"

// schemaVersion is the on-disk format the running binary understands. An entry
// carrying any other version is discarded and rebuilt rather than migrated in
// place or read optimistically (local-store R12). R11 is what makes discarding
// always available: the cost of a rebuild is one slow launch, never data loss.
const schemaVersion = 1

// maxStoreBytes is the store's total on-disk bound (local-store R15, ADR-0017).
// It is a compiled constant and not a user knob, by R9's argument transferred:
// no value a user could pick improves on LRU eviction. It is a backstop, not a
// target, and the reference workload sits an order of magnitude below it. It is a
// var rather than a const only so the eviction test can lower it; nothing in the
// product writes to it. The per-repository bound is R16's and applies upstream,
// before eviction is ever involved.
var maxStoreBytes int64 = 50 << 20

// readFile indirects os.ReadFile so a test can count how many entries the store
// reads. It guards local-store R15's efficiency property: eviction sums sizes by
// stat and reads an entry only when the store is over its bound, so a free 304 (the
// common case) rescans nothing. It is a var only for that observation and is never
// reassigned in the product.
var readFile = os.ReadFile

// Transport is the store's RoundTripper.
type Transport struct {
	base http.RoundTripper
	dir  string
	clk  clock.Clock

	// writer is true when this process holds the advisory lock and may write. A
	// process that does not still reads and revalidates, but writes nothing
	// (local-store R21). lock is the held file; nil for a reader.
	writer bool
	lock   *fileLock

	// invalidated is a degraded reader's in-memory view of repositories its own
	// writes have changed (local-store R23). It suppresses the persisted entry
	// without reaching it, because the writing process owns that file. A writer
	// never touches this map: it deletes the files instead. Guarded by mu.
	mu          sync.Mutex
	invalidated map[string]bool

	// writeMu serialises the writer's disk mutations. RoundTrip is called
	// concurrently by go-gh (the Feed polls many repositories at once), so an
	// eviction sweep must not interleave with another write, and an invalidation
	// must not race a persist. Writes themselves land atomically by rename, so a
	// reader, in this process or the other, never observes a partial file (AC18).
	writeMu sync.Mutex
}

// NewTransport returns a Transport dialing through base and persisting ETags and
// payloads under dir. main.go supplies dir (the XDG cache directory in
// production, per ADR-0017); a test supplies a temp dir. The clock records each
// entry's last-revalidated time (local-store R3, R17). It takes the advisory
// write lock non-blocking at startup (R21): a process that gets it writes for its
// lifetime, one that does not degrades to a reader on the spot.
func NewTransport(base http.RoundTripper, dir string, clk clock.Clock) *Transport {
	t := &Transport{
		base:        base,
		dir:         dir,
		clk:         clk,
		invalidated: make(map[string]bool),
	}
	t.acquire()
	return t
}

// acquire takes the advisory write lock without blocking (local-store R21). A
// store with no directory, an unwritable one, or a contended lock all leave the
// transport a reader: it does not wait, does not poll the lock, and does not
// delay a launch (R22). The lock file is created empty and never written to. The
// directory is the store's own, so it is created 0700: a per-user cache no other
// account has reason to read (ADR-0017).
func (t *Transport) acquire() {
	if t.dir == "" {
		return
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return
	}
	lock, ok := acquireLock(filepath.Join(t.dir, lockName))
	if !ok {
		return
	}
	t.lock = lock
	t.writer = true
	// The writer owns the store, so it is the one to reclaim atomic-write
	// temporaries a crashed writer left behind (local-store R11).
	t.sweepTempFiles()
}

// sweepTempFiles reclaims orphaned atomic-write temporaries left by a writer that
// died between os.CreateTemp and os.Rename (local-store R11). Both the entry writes
// at the store root and the document writes under docs/ (doc.go) place temporaries
// with os.CreateTemp, so both directories are swept: a crash mid-document-write
// otherwise leaks an orphan under docs/ that no sweep reaches. enforceBound globs
// only *.json, so an orphan store.tmp-* would never count toward the bound nor be
// reclaimed on its own. It runs once at startup for the writer, best-effort: a
// failure costs a little disk and nothing else.
func (t *Transport) sweepTempFiles() {
	patterns := []string{
		filepath.Join(t.dir, "store.tmp-*"),
		filepath.Join(t.dir, docsSubdir, "store.tmp-*"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, f := range matches {
			_ = os.Remove(f)
		}
	}
}

// releaseLock drops the advisory lock. Production never calls it: the process
// holds the lock for its lifetime and the kernel releases it at exit (local-store
// R21, R22). It exists for tests that stand in for a holding process ending.
func (t *Transport) releaseLock() error {
	if t.lock == nil {
		return nil
	}
	err := t.lock.release()
	t.lock = nil
	t.writer = false
	return err
}

// isInvalidated reports whether a degraded reader has invalidated repo in its own
// view this session (local-store R23). A writer never populates the map, so this
// is always false for a writer, which reaches disk instead.
func (t *Transport) isInvalidated(repo string) bool {
	if repo == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.invalidated[repo]
}

// entry is one persisted resource. It carries only the ETag, the entity headers
// and the payload, never a request header, so the token never reaches disk
// (local-store R20, AC14a). Schema versions it (R12), and Repo host-qualifies its
// owning repository so a successful write can invalidate it (R10, R14).
type entry struct {
	Schema          int                 `json:"schema"`
	ETag            string              `json:"etag"`
	Repo            string              `json:"repo,omitempty"`
	Header          map[string][]string `json:"header"`
	Body            []byte              `json:"body"`
	LastRevalidated time.Time           `json:"last_revalidated"`
}

// RoundTrip sends a conditional request when it holds an ETag for this key, and
// reconstitutes a 200 from the resulting 304. A non-GET is a write: never
// conditional, and on success it invalidates the repository's cached entries.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		// A write is never revalidated: its key would never match a persisted GET,
		// and its body must survive to reach the wire. On a 2xx the tool has just
		// changed the repository, so its cached reads are invalidated immediately
		// rather than left to be discovered at the next revalidation (local-store
		// R10). The caller owns the returned body.
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			t.invalidate(repoOf(req))
		}
		return resp, nil
	}

	key := t.key(req)
	var cached entry
	var hasCached bool
	// A degraded reader that invalidated this repository skips the persisted
	// entry rather than reaching it (local-store R23): the writing process owns
	// that file, and the read simply goes out unconditional.
	if !t.isInvalidated(repoOf(req)) {
		cached, hasCached = t.load(key)
	}

	sent := req
	if hasCached && cached.ETag != "" {
		// local-store R8/AC5: revalidate rather than serve on age. Clone so the
		// caller's request is never mutated (the http.RoundTripper contract).
		sent = req.Clone(req.Context())
		sent.Header.Set("If-None-Match", cached.ETag)
	}

	resp, err := t.base.RoundTrip(sent)
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusNotModified && hasCached:
		return t.reconstitute(req, resp, cached, key)
	case resp.StatusCode == http.StatusOK:
		return t.persist(req, resp, key)
	default:
		return resp, nil
	}
}

// key is a SHA256 over the method, the URL, the Accept header, the Authorization
// header and the body, rendered as hex (local-store R20). It never contains the
// raw Authorization header, so writing it as a filename persists no token. It
// mirrors go-gh's own cacheKey, folding in Method and the body so a GET and a
// DELETE of one URL, and two POSTs with different bodies, are different keys.
func (t *Transport) key(req *http.Request) string {
	var body []byte
	if req.Body != nil {
		body = drainBody(req)
	}
	preimage := req.Method + "\n" +
		req.URL.String() + "\n" +
		req.Header.Get("Accept") + "\n" +
		req.Header.Get("Authorization") + "\n"
	sum := sha256.Sum256(append([]byte(preimage), body...))
	return hex.EncodeToString(sum[:])
}

// repoOf returns the host-qualified repository a request addresses, as
// host/owner/name (local-store R14, ADR-0009), or "" for a request that names no
// repository. The GitHub REST host api.github.com maps to the repository host
// github.com, the enterprise-agnostic derivation go-gh itself uses. It reuses
// domain.RepoID so the qualification format has one source, and a bare owner/name
// can never be produced (AC14).
func repoOf(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" || parts[1] == "" || parts[2] == "" {
		return ""
	}
	id := domain.RepoID{
		Host:  strings.TrimPrefix(req.URL.Host, "api."),
		Owner: parts[1],
		Name:  parts[2],
	}
	return id.String()
}

// drainBody reads a request body for hashing and restores it (and GetBody) so the
// round trip can still send it.
func drainBody(req *http.Request) []byte {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return body
}

// persist saves a 200's ETag, entity headers and payload, then hands the caller a
// fresh body because the original is consumed.
func (t *Transport) persist(req *http.Request, resp *http.Response, key string) (*http.Response, error) {
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		t.save(key, entry{
			ETag:            etag,
			Repo:            repoOf(req),
			Header:          entityHeaders(resp.Header),
			Body:            body,
			LastRevalidated: t.clk.Now(),
		})
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// reconstitute builds the synthetic 200 that never leaves as a 304. Entity
// headers (body, ETag, Content-Type, Link, and every other) come from the store.
// The x-ratelimit-* family comes from the fresh 304, because it describes the
// exchange just made. A GitHub 304 carries no Link (measured), so the stored copy
// is the only source of it (ADR-0012).
func (t *Transport) reconstitute(orig *http.Request, notModified *http.Response, cached entry, key string) (*http.Response, error) {
	if err := notModified.Body.Close(); err != nil {
		return nil, err
	}

	header := make(http.Header, len(cached.Header))
	for k, v := range cached.Header {
		header[k] = append([]string(nil), v...)
	}
	for k, v := range notModified.Header {
		if strings.HasPrefix(k, rateLimitPrefix) {
			header[k] = append([]string(nil), v...)
		}
	}

	// Record the free revalidation's time (local-store R3).
	cached.LastRevalidated = t.clk.Now()
	t.save(key, cached)

	body := cached.Body
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         notModified.Proto,
		ProtoMajor:    notModified.ProtoMajor,
		ProtoMinor:    notModified.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       orig,
	}, nil
}

// entityHeaders copies the response headers that describe the entity, dropping
// the per-exchange rate-limit family so a later reconstitution takes those from
// the fresh 304 (ADR-0012).
func entityHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		if strings.HasPrefix(k, rateLimitPrefix) {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

func (t *Transport) path(key string) string {
	return filepath.Join(t.dir, key+".json")
}

// LastRevalidated reports the most recent last-revalidated time recorded for repo, and
// whether any entry for it exists (local-store R7). It exposes R3's per-entry timestamp
// to the store's consumers, so a Feed whose revalidation has paused can say what it is
// showing and as of when rather than presenting cached rows as live (live-run-feed R30).
// It reads the persisted entries the repository owns, the same repo tag invalidation
// keys on (R14), and takes the newest across them, so the answer is the freshest instant
// anything the Feed shows for that repository was seen live. A degraded reader answers
// too, from the writer's files: reads are always allowed (R21, R23). It scans the store,
// so a consumer reads it at a coarse cadence, never on every frame.
func (t *Transport) LastRevalidated(repo domain.RepoID) (time.Time, bool) {
	if t.dir == "" {
		return time.Time{}, false
	}
	files, err := filepath.Glob(filepath.Join(t.dir, "*.json"))
	if err != nil {
		return time.Time{}, false
	}
	want := repo.String()
	var newest time.Time
	found := false
	for _, f := range files {
		data, err := readFile(f)
		if err != nil {
			continue
		}
		var e entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		if e.Schema != schemaVersion || e.Repo != want {
			continue
		}
		if !found || e.LastRevalidated.After(newest) {
			newest = e.LastRevalidated
			found = true
		}
	}
	return newest, found
}

// load reads a persisted entry. A missing, unreadable or corrupt entry is not an
// error and never fails a request: the store is derived state and always safe to
// rebuild (local-store R11, R13).
func (t *Transport) load(key string) (entry, bool) {
	if t.dir == "" {
		return entry{}, false
	}
	data, err := readFile(t.path(key))
	if err != nil {
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, false
	}
	// A version this binary does not recognise is discarded, not migrated in
	// place or read optimistically (local-store R12). A file written by an older
	// or newer format, or one whose schema field is absent, reads as a miss and
	// is rebuilt on the next 200.
	if e.Schema != schemaVersion {
		return entry{}, false
	}
	return e, true
}

// save writes a persisted entry best-effort. A write failure costs a future cold
// start its speed and nothing else (local-store R11), so it is swallowed rather
// than surfaced. The file is 0600 and its name is a hash, so no token is written.
// A degraded reader never writes to the store (R21, R23), so save is a no-op for
// one: its newly observed ETags die with the process, and the next cold start is
// slightly colder for it.
func (t *Transport) save(key string, e entry) {
	if t.dir == "" || !t.writer {
		return
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return
	}
	e.Schema = schemaVersion
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if !writeFileAtomic(t.dir, t.path(key), data) {
		return
	}
	t.enforceBound()
}

// writeFileAtomic writes data to a fresh temp file in dir and renames it over
// path, so a reader never observes a half-written entry (AC18). Rename is atomic
// within a filesystem, and the temp name is unique and does not end in .json, so
// the entry glob never sees it. It reports whether the entry landed. A failure
// costs a future cold start its speed and nothing else (local-store R11).
func writeFileAtomic(dir, path string, data []byte) bool {
	tmp, err := os.CreateTemp(dir, "store.tmp-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return false
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return false
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return false
	}
	return true
}

// enforceBound evicts the oldest entries by last-revalidated time until the
// store's total size is within maxStoreBytes (local-store R15, ADR-0017). It runs
// at write time, after the new entry lands, so a write that carries the store
// past the bound pays for itself. R3's timestamp supplies the ordering, so
// eviction keeps no bookkeeping of its own. The just-written entry sorts newest
// and is evicted last, so a write never evicts itself while an older entry
// remains (AC15).
//
// The total is summed by os.Stat, not by reading every entry, so the common case
// (a store within the bound, which every free 304 and most 200s leave it) opens no
// file: it stats, sees it fits, and returns. Only a store actually over the bound
// reads each entry, for its last-revalidated time. The earlier sweep read and
// unmarshalled every entry on every write, which was quadratic across a poll of
// many repositories and made a supposedly free 304 expensive.
func (t *Transport) enforceBound() {
	files, err := filepath.Glob(filepath.Join(t.dir, "*.json"))
	if err != nil {
		return
	}
	type sized struct {
		path string
		size int64
	}
	all := make([]sized, 0, len(files))
	var total int64
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		all = append(all, sized{path: f, size: info.Size()})
		total += info.Size()
	}
	if total <= maxStoreBytes {
		return
	}
	// Over the bound: read each entry now, and only now, for its last-revalidated
	// time, the LRU key. An entry that no longer decodes takes the zero time, so it
	// sorts oldest and its bytes are reclaimed before any live entry's.
	type aged struct {
		path string
		size int64
		age  time.Time
	}
	metas := make([]aged, 0, len(all))
	for _, s := range all {
		var age time.Time
		if data, err := readFile(s.path); err == nil {
			var e entry
			if json.Unmarshal(data, &e) == nil {
				age = e.LastRevalidated
			}
		}
		metas = append(metas, aged{path: s.path, size: s.size, age: age})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].age.Before(metas[j].age) })
	for _, m := range metas {
		if total <= maxStoreBytes {
			return
		}
		if err := os.Remove(m.path); err == nil {
			total -= m.size
		}
	}
}

// invalidate drops repo's cached entries after a successful write, so the write
// is not shadowed by the stale reads it just changed (local-store R10). A writer
// removes the persisted files; a degraded reader marks repo in its own in-memory
// view instead and never reaches the persisted entry, because the writing process
// owns that file (R23). It is a no-op for a request that named no repository.
func (t *Transport) invalidate(repo string) {
	if repo == "" {
		return
	}
	if !t.writer {
		t.mu.Lock()
		t.invalidated[repo] = true
		t.mu.Unlock()
		return
	}
	if t.dir == "" {
		return
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	files, err := filepath.Glob(filepath.Join(t.dir, "*.json"))
	if err != nil {
		return
	}
	for _, f := range files {
		data, err := readFile(f)
		if err != nil {
			continue
		}
		var e entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		if e.Repo == repo {
			_ = os.Remove(f)
		}
	}
}
