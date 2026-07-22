package governor_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestClassification pins R14 and open question 1's discrimination rule against
// recorded response shapes. The governor classifies each response once and
// publishes the verdict so a consumer applies R13 without re-deriving it. The
// authorization 403 carries the measured shape (a documentation_url and a
// message, no Retry-After); every other 403 defaults to rate limiting, which is
// the safe direction. Classification must not consume the body the consumer
// still needs.
func TestClassification(t *testing.T) {
	g := governor.New(openCassette(t, "testdata/classification"), baseClock())

	cases := []struct {
		path        string
		wantLimited bool
		why         string
	}{
		{"clean", false, "a 200 is not a rate-limit response"},
		{"forbidden_ratelimit", true, "a 403 without the authorization shape defaults to rate limiting (open question 1)"},
		{"forbidden_auth", false, "a 403 matching the measured authorization shape is an authorization outcome (open question 1)"},
		{"too_many", true, "a 429 is always a rate-limit response (R12, R14)"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, "https://api.github.com/"+tc.path, nil)
		if err != nil {
			t.Fatalf("%s: build request: %v", tc.path, err)
		}
		resp, err := g.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: round trip: %v", tc.path, err)
		}
		got := governor.RateLimited(resp)
		body, readErr := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("%s: close body: %v", tc.path, closeErr)
		}
		if readErr != nil {
			t.Fatalf("%s: read body: %v", tc.path, readErr)
		}
		if got != tc.wantLimited {
			t.Errorf("%s: RateLimited = %v, want %v (%s)", tc.path, got, tc.wantLimited, tc.why)
		}
		if len(body) == 0 {
			t.Errorf("%s: classification consumed the body; the consumer sees nothing", tc.path)
		}
	}
}
