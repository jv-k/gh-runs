package ghclient_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
)

// fakeTransport is the base of the chain a test injects in place of the network.
// It records the request URL it was handed and returns a canned response built
// from its own fields, so a test can assert both that Request routed through the
// injected transport and that the response survives back to the caller. The
// response is a composite literal built here rather than by a helper, so bodyclose
// has no *http.Response-returning call to flag but the one it should: Request's.
type fakeTransport struct {
	gotURL  string
	status  int
	headers map[string]string
	body    string
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.gotURL = req.URL.String()
	h := http.Header{}
	for k, v := range f.headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: f.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

// TestRequestReturnsResponseThroughTransport pins ghclient's load-bearing
// contract (ADR-0012): the injected Transport is the base of the chain go-gh
// dials through, and Request returns the *http.Response rather than consuming it.
// Both halves matter. If the transport were not installed the request would never
// reach the fake, and if ghclient exposed Get or Do instead of Request the
// response headers would be gone: rate-governor R5 reads x-ratelimit-* and
// ADR-0005 reads Link from exactly this response.
func TestRequestReturnsResponseThroughTransport(t *testing.T) {
	body := `[{"id":29516338954,"status":"completed"}]`
	ft := &fakeTransport{
		status:  http.StatusOK,
		headers: map[string]string{"X-RateLimit-Remaining": "42"},
		body:    body,
	}

	client, err := ghclient.New(ghclient.Options{
		Host:      "github.com",
		AuthToken: "dummy-fixed-token", // fixed dummy: go-gh must not reach the keyring
		Transport: ft,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := client.Request(http.MethodGet, "repos/cli/cli/actions/runs", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The request routed through the injected transport to the right endpoint.
	if !strings.Contains(ft.gotURL, "api.github.com/repos/cli/cli/actions/runs") {
		t.Fatalf("transport saw URL %q, want the runs endpoint on api.github.com", ft.gotURL)
	}

	// The response header survives to the caller: the reason Request exists.
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "42" {
		t.Fatalf("response X-RateLimit-Remaining = %q, want 42; headers must survive Request", got)
	}

	// The caller owns the body and reads it (go-gh did not consume it).
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}
