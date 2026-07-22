package approval_test

import (
	"net/http"
	"net/url"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
)

// pendingMatcher matches a live request against a taped one on method, URL path and the empty
// If-None-Match header, the header-matched shape the tree pins go-vcr v4 for (CLAUDE.md).
func pendingMatcher(r *http.Request, i cassette.Request) bool {
	iu, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	if r.Method != i.Method || r.URL.Path != iu.Path {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

func newFetchClient(t *testing.T, cassetteName string) *ghclient.Client {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(pendingMatcher),
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

// TestClientFetchReadsPendingDeployments pins R12 against a cassette: the pending_deployments
// read returns the environments the Run awaits, each with its id, its name, its
// current_user_can_approve flag and its reviewers, which is exactly the subset R12 targets. It
// is exercised against what the API actually said with no live network.
func TestClientFetchReadsPendingDeployments(t *testing.T) {
	fetch := approval.NewClientFetch(newFetchClient(t, "pending_deployments"))

	ds, err := fetch.PendingDeployments(rid("o", "r"), 456)
	if err != nil {
		t.Fatalf("PendingDeployments returned an error: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("read %d deployments, want 1 (R12)", len(ds))
	}
	d := ds[0]
	if d.EnvironmentID != 3734916060 {
		t.Errorf("environment id = %d, want 3734916060, the id the review targets (R12)", d.EnvironmentID)
	}
	if d.EnvironmentName != "scribe-protected" {
		t.Errorf("environment name = %q, want scribe-protected (R12)", d.EnvironmentName)
	}
	if !d.CurrentUserCanApprove {
		t.Errorf("current_user_can_approve = false, want true (R10, R12)")
	}
	if len(d.Reviewers) != 2 || d.Reviewers[0] != "octocat" || d.Reviewers[1] != "deploy-team" {
		t.Errorf("reviewers = %v, want [octocat deploy-team] (R12)", d.Reviewers)
	}
}

// TestClientFetchCompletedRunReturnsNone pins R12's clean discrimination: a completed Run,
// which awaits nothing, serves an empty array, and the fetch returns no environments rather
// than something ambiguous.
func TestClientFetchCompletedRunReturnsNone(t *testing.T) {
	fetch := approval.NewClientFetch(newFetchClient(t, "pending_deployments_empty"))

	ds, err := fetch.PendingDeployments(rid("o", "r"), 999)
	if err != nil {
		t.Fatalf("PendingDeployments returned an error: %v", err)
	}
	if len(ds) != 0 {
		t.Errorf("a completed Run returned %d deployments, want 0 (R12)", len(ds))
	}
}
