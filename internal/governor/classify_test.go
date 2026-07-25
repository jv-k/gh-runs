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

// TestParentDocumentedEndpointClassification pins open question 1's discriminator on
// the endpoints whose reference page's path does not carry their last segment. Two
// shapes produce that. A segment naming an action (cancel, enable) or a sub-resource
// (logs, usage, pending_deployments) is documented on the parent resource's page, so
// the doc URL carries workflow-runs, workflows or cache instead. And a Cache is
// documented at /rest/actions/cache while its endpoint says caches.
//
// A fine-grained PAT's 403 on these is the expected case (purge R13, run-lifecycle R3,
// workflow-management R7), and reading it as a rate limit spends three bounded backoffs,
// and their waits, before purge R19a reclassifies it, halving the write ramp on each one.
// The safe direction is untouched: the rate-limits page, a Retry-After, and a trailing
// segment the classifier does not know are all still rate limiting (R12).
func TestParentDocumentedEndpointClassification(t *testing.T) {
	rec := openCassette(t, "testdata/parent_documented")

	cases := []struct {
		method      string
		path        string
		wantLimited bool
		why         string
	}{
		{http.MethodPost, "repos/o/r/actions/runs/1/cancel", false, "cancel is documented on the workflow-runs page, so the doc URL names the Run, not the action"},
		{http.MethodPost, "repos/o/r/actions/runs/2/force-cancel", false, "force-cancel is documented on the workflow-runs page (run-lifecycle R6)"},
		{http.MethodPost, "repos/o/r/actions/runs/3/rerun", false, "re-run is documented on the workflow-runs page (run-lifecycle R8)"},
		{http.MethodPost, "repos/o/r/actions/runs/4/rerun-failed-jobs", false, "re-run-failed-jobs is documented on the workflow-runs page (run-lifecycle R13)"},
		{http.MethodPost, "repos/o/r/actions/runs/5/approve", false, "approve is documented on the workflow-runs page (approvals R11)"},
		{http.MethodPut, "repos/o/r/actions/workflows/9001/enable", false, "enable is documented on the workflows page (workflow-management R5, R7)"},
		{http.MethodPut, "repos/o/r/actions/workflows/9002/disable", false, "disable is documented on the workflows page (workflow-management R5, R7)"},
		{http.MethodPost, "repos/o/r/actions/workflows/9003/dispatches", false, "a Dispatch is documented on the workflows page (workflow-dispatch R14)"},
		{http.MethodDelete, "repos/o/r/actions/caches/123", false, "a Cache is documented at the singular /rest/actions/cache (storage-reclamation, R2's Cache deletion)"},
		{http.MethodDelete, "repos/o/r/actions/artifacts/77", false, "an Artifact's page is the plural it already matched (R2's Artifact deletion)"},
		{http.MethodDelete, "repos/o/r/actions/runs/8/logs", false, "log deletion is documented on the workflow-runs page (R2, log-viewer R17)"},
		{http.MethodGet, "repos/o/r/actions/jobs/55/logs", false, "a Job's logs are documented on the workflow-jobs page, the same segment under a different parent"},
		{http.MethodGet, "repos/o/r/actions/cache/usage", false, "cache usage is a sub-resource of the singular cache page"},
		{http.MethodPost, "repos/o/r/actions/runs/9/pending_deployments", false, "a pending-deployment review is documented on the workflow-runs page (approvals R12)"},
		{http.MethodGet, "repos/o/r/actions/runs/10/timing", true, "a trailing segment the classifier does not know stays the terminal resource, matches nothing, and defaults to rate limiting"},
		{http.MethodPost, "repos/o/r/actions/runs/6/cancel", true, "a parent-documented endpoint answering with the rate-limits page is still rate limiting (open question 1)"},
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
