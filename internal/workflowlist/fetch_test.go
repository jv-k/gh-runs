package workflowlist_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/workflowlist"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// newClient builds a ghclient over a transport (a cassette), for the fetch tests.
func newClient(t *testing.T, transport http.RoundTripper) workflowlist.Requester {
	t.Helper()
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// workflowsMatcher matches a live request against a taped one on method and URL path. The
// path alone disambiguates the fixture, so the match is robust to how go-gh encodes the query.
func workflowsMatcher(r *http.Request, i cassette.Request) bool {
	iu, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	return r.Method == i.Method && r.URL.Path == iu.Path
}

// TestClientFetchDecodesTheWorkflowList pins R1, R2 and R3 against a cassette, with no live
// network: every Workflow's id, name, path and state decode, the three disabled states stay
// distinct from one another and from a bare "disabled" (R2), the deleted Workflow is present
// (R11), each is stamped with its repository (R0), and the enumeration is complete because
// the Link header carried no next page (R1). This list is the name-to-id map the reader builds
// for every consumer: the Workflows tab, the scheduler's join, and the CLI.
func TestClientFetchDecodesTheWorkflowList(t *testing.T) {
	rec, err := recorder.New("testdata/repo_workflows",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(workflowsMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder: %v", err)
		}
	})
	client := newClient(t, rec)

	repo := rid("o", "r")
	rw := workflowlist.ClientFetch(client)(repo)
	if rw.Err != nil {
		t.Fatalf("ClientFetch returned an error: %v", rw.Err)
	}
	if len(rw.Workflows) != 4 {
		t.Fatalf("decoded %d Workflows, want 4 (R1)", len(rw.Workflows))
	}
	if !rw.Complete {
		t.Errorf("Complete = false, want true (no Link rel=next means a complete list) (R1)")
	}

	byID := map[int64]domain.Workflow{}
	for _, w := range rw.Workflows {
		if w.Repo != repo {
			t.Errorf("Workflow %d repo = %v, want it stamped with %v (R0)", w.ID, w.Repo, repo)
		}
		byID[w.ID] = w
	}

	// R1: id, name, path all decode.
	if w := byID[9001]; w.Name != "CI" || w.Path != ".github/workflows/ci.yml" {
		t.Errorf("Workflow 9001 = name %q path %q, want CI / .github/workflows/ci.yml (R1)", w.Name, w.Path)
	}
	// R2: the three disabled states stay distinct, never collapsed to a bare "disabled".
	states := map[int64]domain.State{
		9001: domain.StateActive,
		9002: domain.StateDisabledManually,
		9003: domain.StateDisabledInactivity,
		9004: domain.StateDeleted,
	}
	for id, want := range states {
		if got := byID[id].State; got != want {
			t.Errorf("Workflow %d state = %q, want %q (R2, R3): the state is the API's own value, kept distinct", id, got, want)
		}
	}
}

// TestClientFetchRecordsAForbiddenResponse pins R7: a 403 on a fine-grained PAT is recorded
// as an outcome on the RepoWorkflows rather than treated as a fault, so the fan-out continues.
func TestClientFetchRecordsAForbiddenResponse(t *testing.T) {
	rec, err := recorder.New("testdata/repo_forbidden",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(workflowsMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() { _ = rec.Stop() })
	client := newClient(t, rec)

	rw := workflowlist.ClientFetch(client)(rid("o", "r"))
	if rw.Err == nil {
		t.Errorf("a 403 must be recorded on the RepoWorkflows, not swallowed (R7)")
	}
}

// bareRequester answers every request with one canned response and no Go error, the shape a
// Requester that is not go-gh's REST client has. ghclient.Client raises an *api.HTTPError of
// its own on a non-2xx, so the cassette above never reaches the reader's own status check.
type bareRequester struct{ resp *http.Response }

func (b bareRequester) Request(string, string, io.Reader) (*http.Response, error) {
	return b.resp, nil
}

// TestClientFetchRecordsANonSuccessStatusFromABareRequester pins R7 at the seam rather than
// at go-gh: a Requester that hands back a non-2xx response without a Go error must still have
// the status recorded on the RepoWorkflows, never decoded as if it were a list. The body is
// the API's error envelope, which unmarshals into the listing envelope without complaint and
// would otherwise read as a repository with no Workflows at all.
func TestClientFetchRecordsANonSuccessStatusFromABareRequester(t *testing.T) {
	client := bareRequester{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Resource not accessible by personal access token"}`)),
	}}

	rw := workflowlist.ClientFetch(client)(rid("o", "r"))
	if rw.Err == nil {
		t.Fatalf("a 403 must be recorded on the RepoWorkflows, not decoded as an empty list (R7)")
	}
	if !strings.Contains(rw.Err.Error(), "Forbidden") {
		t.Errorf("recorded error = %q, want it to name the status (R7)", rw.Err)
	}
}
