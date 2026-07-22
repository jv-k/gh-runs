package filter_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestQueryEmitsPushableAxes pins ADR-0016's server-side half: Query emits
// branch, actor, event, created and head_sha, with the gh/API parameter names
// (branch, actor, event, created, head_sha) rather than the flag spellings. An
// empty axis emits nothing, and the created parameter is the verbatim string the
// user typed, so no re-serialisation of ours can shift a boundary.
func TestQueryEmitsPushableAxes(t *testing.T) {
	created, err := filter.ParseCreated(">=2024-01-15")
	if err != nil {
		t.Fatalf("ParseCreated returned error %v", err)
	}
	f := filter.Filter{
		Branch:  "main",
		Commit:  "abc123",
		Actor:   "octocat",
		Event:   "push",
		Created: created,
	}
	q := f.Query()
	for _, want := range []struct{ key, val string }{
		{"branch", "main"},
		{"head_sha", "abc123"},
		{"actor", "octocat"},
		{"event", "push"},
		{"created", ">=2024-01-15"},
	} {
		if got := q.Get(want.key); got != want.val {
			t.Errorf("Query()[%q] = %q, want %q", want.key, got, want.val)
		}
	}
}

// TestQueryZeroFilterIsEmpty pins that the zero Filter pushes nothing: a
// match-all Purge crawls unfiltered (cli-surface R15), and an empty Query is
// what a request built from it carries.
func TestQueryZeroFilterIsEmpty(t *testing.T) {
	var f filter.Filter
	if q := f.Query(); len(q) != 0 {
		t.Fatalf("Query() of the zero Filter = %v, want empty", q)
	}
}

// TestQueryStatusSingleton pins ADR-0016's singleton rule: Query emits status=<v>
// exactly when the two sets hold one value between them, and the value is pushed
// as status whether it is a Status or a Conclusion, because status is the one
// parameter the API matches on both fields. gh runs list -s failure thus produces
// status=failure, the request gh produces. More than one value between the sets
// pushes nothing and the clause rides in Match.
func TestQueryStatusSingleton(t *testing.T) {
	cases := []struct {
		name       string
		f          filter.Filter
		wantStatus string // "" means the status parameter must be absent
	}{
		{"single status pushes", filter.Filter{Statuses: []domain.Status{domain.StatusInProgress}}, "in_progress"},
		{"single conclusion pushes as status", filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure}}, "failure"},
		{"two statuses push nothing", filter.Filter{Statuses: []domain.Status{domain.StatusQueued, domain.StatusInProgress}}, ""},
		{"two conclusions push nothing", filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure, domain.ConclusionSuccess}}, ""},
		{
			name:       "the approvals pair pushes nothing and rides in Match",
			f:          filter.Filter{Statuses: []domain.Status{domain.StatusWaiting}, Conclusions: []domain.Conclusion{domain.ConclusionActionRequired}},
			wantStatus: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := c.f.Query()
			if got := q.Get("status"); got != c.wantStatus {
				t.Fatalf("Query()[status] = %q, want %q", got, c.wantStatus)
			}
		})
	}
}

// TestQueryNeverEmitsClientOnlyAxes pins ADR-0016's guarantees at the projection:
// Query never emits a conclusion parameter (none exists, and the API ignores one
// silently, cli-surface R5 and AC4), never emits the repository axis (no
// parameter form at all), and never emits workflow (that is an endpoint choice,
// not a query parameter). Even a single Conclusion rides out as status, never as
// conclusion.
func TestQueryNeverEmitsClientOnlyAxes(t *testing.T) {
	f := filter.Filter{
		Workflow:    "CI",
		Conclusions: []domain.Conclusion{domain.ConclusionFailure}, // singleton: rides out as status
		Repos:       []domain.RepoID{{Host: "github.com", Owner: "cli", Name: "cli"}},
	}
	q := f.Query()
	for _, forbidden := range []string{"conclusion", "conclusions", "repo", "repos", "repository", "workflow"} {
		if _, present := q[forbidden]; present {
			t.Errorf("Query() emitted a %q parameter; it must never", forbidden)
		}
	}
	// The single conclusion must have gone out as status, not vanished.
	if got := q.Get("status"); got != "failure" {
		t.Errorf("Query()[status] = %q, want the single conclusion pushed as status=failure", got)
	}
}
