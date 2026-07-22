package logview_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
)

// logMatcher matches a live request against a taped one on method, full URL and the empty
// If-None-Match header, the header-matched shape the tree pins go-vcr v4 for (CLAUDE.md). The
// full URL matters here because the fetch spans two hosts, the API and the signed blob, and
// the path alone would not tell the endpoint from the redirect target it points to.
func logMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method || r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// newClient builds a ghclient over a replay-only cassette, so the fetch is exercised against
// what the API actually said with no live network.
func newClient(t *testing.T, cassetteName string) *ghclient.Client {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(logMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", cassetteName, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", cassetteName, err)
		}
	})
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: rec})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// TestClientFetchFollowsRedirectToPlainText pins R1, R13 and AC1: fetching a Job's log hits
// the per-Job logs endpoint, follows the 302 to the signed blob, and returns the plain-text
// body. It reaches the endpoint, not the run-level archive: the cassette tapes no archive
// interaction, so a fetch that requested one would fail under ModeReplayOnly (R2, AC5).
func TestClientFetchFollowsRedirectToPlainText(t *testing.T) {
	client := newClient(t, "job_log")
	data, err := logview.ClientFetch(client)(rid("o", "r"), 123456789)
	if err != nil {
		t.Fatalf("ClientFetch returned an error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ClientFetch returned no bytes; the redirect to the signed blob was not followed (R13)")
	}
	body := string(data)
	if !strings.Contains(body, "hello from the job") {
		t.Errorf("fetched body does not carry the taped log content: %q (R1, R13)", body)
	}
	if !strings.HasPrefix(body, "2026-") {
		t.Errorf("fetched body did not begin with the taped log's first line: %q", body[:min(40, len(body))])
	}
}

// TestClientFetchNon200IsEmpty pins R18 and AC7: a non-200, or an error the RESTClient raises
// for one, yields an empty result and no surfaced failure, so the pane renders the empty
// state whatever the cause (a deleted or in-progress Job's log). No cassette is needed,
// because a fake Requester returning a 404 exercises the branch directly.
func TestClientFetchNon200IsEmpty(t *testing.T) {
	fake := requesterFunc(func(string, string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}}, nil
	})
	data, err := logview.ClientFetch(fake)(rid("o", "r"), 1)
	if err != nil {
		t.Fatalf("a non-200 must yield no error (R18): %v", err)
	}
	if len(data) != 0 {
		t.Errorf("a non-200 must yield no bytes so the pane shows the empty state (R18, AC7): %q", data)
	}
}

// requesterFunc adapts a function to the logview.Requester interface, ignoring the body a GET
// never carries.
type requesterFunc func(method, path string) (*http.Response, error)

func (f requesterFunc) Request(method, path string, _ io.Reader) (*http.Response, error) {
	return f(method, path)
}
