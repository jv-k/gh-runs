package scheduler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// errRT fails every request at the wire, the transport error a poll cannot
// distinguish from an unanswered repository without RepoPollFailed.
type errRT struct{ err error }

func (e errRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

// statusRT answers every request with a fixed status and body, so a test can pin how
// one non-200 class is classified without a cassette per class.
type statusRT struct {
	code int
	body string
	hdr  http.Header
}

func (s statusRT) RoundTrip(req *http.Request) (*http.Response, error) {
	h := s.hdr
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	body := s.body
	return &http.Response{
		StatusCode:    s.code,
		Status:        http.StatusText(s.code),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		// go-gh reads resp.Request.URL when it converts a non-2xx into an HTTPError, and
		// http.Client fills this field on every real response, so a fake that omits it
		// panics on exactly the path these tests exercise.
		Request: req,
	}, nil
}

// TestTransportErrorEmitsRepoPollFailed pins ADR-0015's fourth event. A poll that
// never reaches a status code is a per-repository failure, and until it is emitted a
// failed repository is indistinguishable from one that has simply not answered yet.
func TestTransportErrorEmitsRepoPollFailed(t *testing.T) {
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	h := newHarness(t, harnessConfig{
		base:    errRT{err: errors.New("dial tcp 140.82.121.6:443: connect: connection refused")},
		pollSet: []domain.RepoID{id},
	})
	h.start(t)
	h.waitPolls(t, 1)
	h.waitEvents(t, 1)

	fails := h.failures()
	if len(fails) != 1 {
		t.Fatalf("got %d RepoPollFailed events, want 1 (ADR-0015 catalog)", len(fails))
	}
	if fails[0].Repo != id {
		t.Errorf("RepoPollFailed carried repo %v, want %v", fails[0].Repo, id)
	}
	if fails[0].Err == nil {
		t.Fatal("RepoPollFailed carried a nil error; the Feed has nothing to distinguish")
	}
	if got := len(h.updates()); got != 0 {
		t.Errorf("a failed poll emitted %d Updates, want 0", got)
	}
}

// TestNonOKStatusEmitsRepoPollFailed pins which non-200s are this repository's own
// failure. A 404 or a 5xx is: the repository has gone, lost visibility, or the API is
// unwell, and each is invisible until reported. The status travels in the error,
// because a 404 and a 502 ask different things of the operator.
func TestNonOKStatusEmitsRepoPollFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want string
	}{
		{"gone or invisible", http.StatusNotFound, "404"},
		{"unauthenticated", http.StatusUnauthorized, "401"},
		{"api unwell", http.StatusBadGateway, "502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
			h := newHarness(t, harnessConfig{
				base:    statusRT{code: tc.code, body: `{"message":"nope"}`},
				pollSet: []domain.RepoID{id},
			})
			h.start(t)
			h.waitPolls(t, 1)
			h.waitEvents(t, 1)

			fails := h.failures()
			if len(fails) != 1 {
				t.Fatalf("HTTP %d produced %d RepoPollFailed events, want 1", tc.code, len(fails))
			}
			if fails[0].Repo != id {
				t.Errorf("RepoPollFailed carried repo %v, want %v", fails[0].Repo, id)
			}
			if fails[0].Err == nil || !strings.Contains(fails[0].Err.Error(), tc.want) {
				t.Errorf("RepoPollFailed error %v does not name the status %s", fails[0].Err, tc.want)
			}
		})
	}
}

// TestRateLimitStatusEmitsNothing pins the exclusion ADR-0018 requires. A 403 or 429
// is an account-wide condition the governor has already folded into the Readout the
// loop reads, so reporting it per repository would state one condition once per
// repository and would put a failure marker on repositories that are perfectly well.
func TestRateLimitStatusEmitsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		hdr  http.Header
	}{
		{"secondary limit", http.StatusForbidden, http.Header{"X-Ratelimit-Remaining": {"0"}}},
		{"too many requests", http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
			h := newHarness(t, harnessConfig{
				base:    statusRT{code: tc.code, body: `{"message":"rate limited"}`, hdr: tc.hdr},
				pollSet: []domain.RepoID{id},
			})
			h.start(t)
			h.waitPolls(t, 1)

			if got := h.eventCount(); got != 0 {
				t.Fatalf("HTTP %d emitted %d events, want 0 (the governor owns this condition)", tc.code, got)
			}
		})
	}
}
