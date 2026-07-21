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
	"strings"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// rateLimitPrefix marks the headers that describe an exchange rather than an
// entity. On a reconstituted 200 they come from the fresh 304 and never from the
// stored copy, because a persisted counter would be a stale reading of a live one
// (ADR-0012). Go canonicalises x-ratelimit-* to this casing.
const rateLimitPrefix = "X-Ratelimit-"

// Transport is the store's RoundTripper.
type Transport struct {
	base http.RoundTripper
	dir  string
	clk  clock.Clock
}

// NewTransport returns a Transport dialing through base and persisting ETags and
// payloads under dir. main.go supplies dir (the XDG cache directory in
// production, per ADR-0017); a test supplies a temp dir. The clock records each
// entry's last-revalidated time (local-store R3, R17).
func NewTransport(base http.RoundTripper, dir string, clk clock.Clock) *Transport {
	return &Transport{base: base, dir: dir, clk: clk}
}

// entry is one persisted resource. It carries only the ETag, the entity headers
// and the payload, never a request header, so the token never reaches disk
// (local-store R20, AC14a).
type entry struct {
	ETag            string              `json:"etag"`
	Header          map[string][]string `json:"header"`
	Body            []byte              `json:"body"`
	LastRevalidated time.Time           `json:"last_revalidated"`
}

// RoundTrip sends a conditional request when it holds an ETag for this key, and
// reconstitutes a 200 from the resulting 304.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := t.key(req)
	cached, hasCached := t.load(key)

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
		return t.persist(resp, key)
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
func (t *Transport) persist(resp *http.Response, key string) (*http.Response, error) {
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

// load reads a persisted entry. A missing, unreadable or corrupt entry is not an
// error and never fails a request: the store is derived state and always safe to
// rebuild (local-store R11, R13).
func (t *Transport) load(key string) (entry, bool) {
	if t.dir == "" {
		return entry{}, false
	}
	data, err := os.ReadFile(t.path(key))
	if err != nil {
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, false
	}
	return e, true
}

// save writes a persisted entry best-effort. A write failure costs a future cold
// start its speed and nothing else (local-store R11), so it is swallowed rather
// than surfaced. The file is 0600 and its name is a hash, so no token is written.
func (t *Transport) save(key string, e entry) {
	if t.dir == "" {
		return
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.WriteFile(t.path(key), data, 0o600)
}
