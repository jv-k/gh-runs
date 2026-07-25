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
		{"repos/cli/cli/actions/permissions", false, "a 403 whose documentation_url points at the called endpoint's own reference page is the measured authorization shape (open question 1)"},
		{"repos/o/r/actions/runs", true, "a secondary-limit 403 whose documentation_url points at the rate-limits page, not the called endpoint, defaults to rate limiting (open question 1)"},
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

// TestSubActionEndpointClassification pins open question 1's discriminator on the
// Actions sub-action endpoints, whose path ends in the action rather than in the
// resource it acts on (cancel, rerun, enable, disable). GitHub documents each of
// them on the parent resource's reference page, so the measured authorization
// documentation_url carries workflow-runs or workflows and never the segment the
// request path ends in. A fine-grained PAT's 403 on these is the expected case
// (purge R13, run-lifecycle R3, workflow-management R7), and reading it as a rate
// limit spends three bounded backoffs before purge R19a reclassifies it. The safe
// direction is untouched: the rate-limits page, or a Retry-After, is still rate
// limiting (R12).
func TestSubActionEndpointClassification(t *testing.T) {
	rec := openCassette(t, "testdata/verb_endpoints")

	cases := []struct {
		method      string
		path        string
		wantLimited bool
		why         string
	}{
		{http.MethodPost, "repos/o/r/actions/runs/1/cancel", false, "cancel is documented on the workflow-runs page, so the doc URL names the Run, not the verb"},
		{http.MethodPost, "repos/o/r/actions/runs/2/force-cancel", false, "force-cancel is documented on the workflow-runs page (run-lifecycle R6)"},
		{http.MethodPost, "repos/o/r/actions/runs/3/rerun", false, "re-run is documented on the workflow-runs page (run-lifecycle R8)"},
		{http.MethodPost, "repos/o/r/actions/runs/4/rerun-failed-jobs", false, "re-run-failed-jobs is documented on the workflow-runs page (run-lifecycle R13)"},
		{http.MethodPost, "repos/o/r/actions/runs/5/approve", false, "approve is documented on the workflow-runs page"},
		{http.MethodPut, "repos/o/r/actions/workflows/9001/enable", false, "enable is documented on the workflows page (workflow-management R5, R7)"},
		{http.MethodPut, "repos/o/r/actions/workflows/9002/disable", false, "disable is documented on the workflows page (workflow-management R5, R7)"},
		{http.MethodPost, "repos/o/r/actions/workflows/9003/dispatches", false, "a Dispatch is documented on the workflows page (workflow-dispatch R14)"},
		{http.MethodPost, "repos/o/r/actions/runs/6/cancel", true, "a verb endpoint answering with the rate-limits page is still rate limiting (open question 1)"},
		{http.MethodPost, "repos/o/r/actions/runs/7/cancel", true, "a Retry-After means rate limiting outright, whatever the body says (R12)"},
	}
	for _, tc := range cases {
		// One Governor per case, so a case's pacing slot and any backoff hold it set
		// never make the next case's write park on the fake clock (R2, R12).
		g := governor.New(rec, baseClock())
		// http.NoBody rather than nil: the cassette's default matcher parses the form of
		// a POST or a PUT, which a nil body fails outright (ADR-0013's go-vcr v4 pin).
		req, err := http.NewRequest(tc.method, "https://api.github.com/"+tc.path, http.NoBody)
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
			t.Errorf("%s %s: RateLimited = %v, want %v (%s)", tc.method, tc.path, got, tc.wantLimited, tc.why)
		}
		if len(body) == 0 {
			t.Errorf("%s: classification consumed the body; the consumer sees nothing", tc.path)
		}
	}
}
