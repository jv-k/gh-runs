package rundetail

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// recordingRequester is a gated fake that records the path ClientFetch built and returns a
// canned response, so a test asserts the path and the decode without a live transport
// (ADR-0015). The property under test is the path (AC3, AC4) and the JSON decode, which is a
// fake's job here exactly as it is the scheduler's; a recorded cassette would add nothing to
// a path assertion and a header-matched one is owed only where the wire itself is under test.
type recordingRequester struct {
	path   string
	status int
	body   string
}

func (r *recordingRequester) Request(_, path string, _ io.Reader) (*http.Response, error) {
	r.path = path
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

// TestClientFetchPathHasNoAttemptHistory pins R6, AC3 and AC4 at the path: the Jobs request
// names the latest Attempt's listing, carries no filter=all, and names no attempts/N
// segment, because prior Attempts' Jobs are not served and never requested.
func TestClientFetchPathHasNoAttemptHistory(t *testing.T) {
	rr := &recordingRequester{status: http.StatusOK, body: `{"total_count":0,"jobs":[]}`}
	if _, err := ClientFetch(rr)(repoID("cli", "cli"), 4821); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := "repos/cli/cli/actions/runs/4821/jobs"; rr.path != want {
		t.Fatalf("jobs path = %q, want %q (R6)", rr.path, want)
	}
	if strings.Contains(rr.path, "filter=all") {
		t.Fatalf("the jobs path requested filter=all as history (AC4): %q", rr.path)
	}
	if strings.Contains(rr.path, "attempts/") {
		t.Fatalf("the jobs path requested a prior Attempt's Jobs (AC3): %q", rr.path)
	}
}

// TestClientFetchDecodesJobsAndSteps pins that ClientFetch decodes the Jobs with their Steps
// inline and reads an in_progress Job's null Conclusion as the empty Conclusion (resolved
// open questions 1 and 2), so the pane paints the measured payload rather than a belief.
func TestClientFetchDecodesJobsAndSteps(t *testing.T) {
	body := `{"total_count":2,"jobs":[
	  {"id":1,"name":"build","status":"completed","conclusion":"success","steps":[
	    {"number":1,"name":"Set up job","status":"completed","conclusion":"success"}
	  ]},
	  {"id":2,"name":"lint","status":"in_progress","conclusion":null,"steps":[]}
	]}`
	jobs, err := ClientFetch(&recordingRequester{status: http.StatusOK, body: body})(repoID("cli", "cli"), 1)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("decoded %d Jobs, want 2", len(jobs))
	}
	if jobs[0].Name != "build" || len(jobs[0].Steps) != 1 || jobs[0].Steps[0].Name != "Set up job" {
		t.Fatalf("Job or Step decode wrong: %+v", jobs[0])
	}
	if jobs[1].Status != domain.StatusInProgress || jobs[1].Conclusion != domain.ConclusionNone {
		t.Fatalf("an in_progress Job did not decode a null Conclusion as empty: %+v", jobs[1])
	}
}

// TestClientFetchNon200IsEmpty pins that a non-200 yields an empty result and no error, so
// the pane renders R12a's "no Jobs yet" and the fast tier retries (AC14) rather than
// surfacing a distinct failure.
func TestClientFetchNon200IsEmpty(t *testing.T) {
	jobs, err := ClientFetch(&recordingRequester{status: http.StatusNotFound})(repoID("cli", "cli"), 1)
	if err != nil || jobs != nil {
		t.Fatalf("a non-200 should yield empty and no error (R12a), got jobs=%v err=%v", jobs, err)
	}
}
