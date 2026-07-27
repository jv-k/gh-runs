package filter_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestParseQueryReadsEveryAxis pins the grammar the filter input accepts (live-run-feed
// R22, R23): an axis:value token per axis, gh's short spellings beside the long ones, and a
// bare token classified into the permissive pair.
func TestParseQueryReadsEveryAxis(t *testing.T) {
	got, err := filter.ParseQuery("branch:main c:abc123 u:octocat e:push w:9004 status:failure")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if got.Branch != "main" || got.Commit != "abc123" || got.Actor != "octocat" || got.Event != "push" || got.Workflow != "9004" {
		t.Errorf("parsed = %+v, want every axis set from its token", got)
	}
	if len(got.Conclusions) != 1 || got.Conclusions[0] != domain.ConclusionFailure {
		t.Errorf("parsed Conclusions = %v, want the permissive value classified as a Conclusion", got.Conclusions)
	}
}

// TestParseQueryRejectsAnUnknownValueByName pins cli-surface R6 at this input: a bare token
// that is neither a Status nor a Conclusion is rejected, by the one validation every
// consumer shares, rather than silently ignored.
func TestParseQueryRejectsAnUnknownValueByName(t *testing.T) {
	if _, err := filter.ParseQuery("not-a-status"); err == nil {
		t.Fatal("ParseQuery accepted an unrecognised permissive value, want an error naming it")
	}
}

// TestQueryStringRoundTrips pins the pair: every axis the grammar has renders back into a
// line that parses to the same Filter. This is what lets one surface hand another a filter
// as a value and have it appear in the operator's input as an editable line.
func TestQueryStringRoundTrips(t *testing.T) {
	created, err := filter.ParseCreated(">=2026-01-01")
	if err != nil {
		t.Fatalf("ParseCreated: %v", err)
	}
	cases := []struct {
		name string
		in   filter.Filter
	}{
		{"empty", filter.Filter{}},
		{"workflow alone", filter.Filter{Workflow: "9004"}},
		{"every scalar axis", filter.Filter{Branch: "main", Commit: "abc123", Actor: "octocat", Event: "push", Workflow: "42"}},
		{"created", filter.Filter{Created: created}},
		{"a status", filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}}},
		{"a conclusion", filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure}}},
		{"a repository", filter.Filter{Repos: []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}}},
		{"repositories beside other axes", filter.Filter{
			Branch: "main",
			Repos: []domain.RepoID{
				{Host: domain.HostGitHub, Owner: "cli", Name: "cli"},
				{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"},
			},
			Conclusions: []domain.Conclusion{domain.ConclusionFailure},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := c.in.QueryString()
			back, err := filter.ParseQuery(line)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", line, err)
			}
			if back.QueryString() != line {
				t.Errorf("round trip of %q produced %q", line, back.QueryString())
			}
			// Compare the Filter itself and not only the rendered line. A line comparison
			// alone cannot see a dropped axis: an axis QueryString omits is absent from
			// both sides and agrees with itself, which is how the repository axis sat
			// unrendered under a green round-trip test.
			if !reflect.DeepEqual(back, c.in) {
				t.Errorf("round trip of %q produced %+v, want %+v", line, back, c.in)
			}
			if back.Query().Encode() != c.in.Query().Encode() {
				t.Errorf("round trip changed the server-side half: %q, want %q", back.Query().Encode(), c.in.Query().Encode())
			}
		})
	}
}

// TestGrammarSpellsEveryAxis is the guard on the hole this file's repository token closed.
// The Feed reports a filter as active, and config reports a launch filter as empty, by
// asking whether QueryString is non-empty. An axis the grammar drops therefore narrows the
// rows while every surface above calls the filter absent, which is what the repository axis
// did for a release.
//
// It is reflective on purpose. The axis-count guard in config compares Filter's field count
// against that package's sub-key list, so it says nothing about whether this grammar spells
// what it counts: a tenth axis with a config key and no token would leave that test green
// and put the trap straight back. This is the test that fails instead.
//
// A new axis fails it twice over: the fixture below no longer sets every field, and the
// round trip no longer returns an equal Filter. Both messages name the field.
func TestGrammarSpellsEveryAxis(t *testing.T) {
	created, err := filter.ParseCreated(">=2026-01-01")
	if err != nil {
		t.Fatalf("ParseCreated: %v", err)
	}
	full := filter.Filter{
		Branch:      "main",
		Commit:      "abc123",
		Actor:       "octocat",
		Event:       "push",
		Workflow:    "9004",
		Created:     created,
		Statuses:    []domain.Status{domain.StatusCompleted},
		Conclusions: []domain.Conclusion{domain.ConclusionFailure},
		Repos:       []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}},
		// The marker rides inside the repository axis rather than beside it, so the
		// fixture carries both forms at once. That is also the round trip worth pinning:
		// repo:this-repo and repo:cli/cli are OR members of one axis, and the grammar
		// must return both rather than collapsing one into the other (ADR-0016).
		ThisRepo: true,
	}

	v := reflect.ValueOf(full)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("the fixture leaves %s at its zero value: an axis this test cannot see "+
				"is an axis QueryString can drop unnoticed", v.Type().Field(i).Name)
		}
	}

	back, err := filter.ParseQuery(full.QueryString())
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", full.QueryString(), err)
	}
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		if got, want := reflect.ValueOf(back).Field(i), v.Field(i); !reflect.DeepEqual(got.Interface(), want.Interface()) {
			t.Errorf("the grammar does not carry %s: round trip gave %v, want %v. "+
				"An axis with no token narrows the Feed while it reports no active filter",
				name, got, want)
		}
	}
}

// TestParseQueryReadsTheRepositoryAxis pins the token ADR-0016 expects the Feed's filter
// input to carry. Both spellings of a ref name the same repository, because the axis holds
// host-qualified identities and ParseRepoRef defaults the host (ADR-0009, cli-surface R8).
func TestParseQueryReadsTheRepositoryAxis(t *testing.T) {
	ghRuns := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"}
	for _, line := range []string{"repo:jv-k/gh-runs", "repo:github.com/jv-k/gh-runs"} {
		t.Run(line, func(t *testing.T) {
			got, err := filter.ParseQuery(line)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", line, err)
			}
			if len(got.Repos) != 1 || got.Repos[0] != ghRuns {
				t.Errorf("Repos = %v, want the host-qualified %v", got.Repos, ghRuns)
			}
			if len(got.Statuses) != 0 || len(got.Conclusions) != 0 {
				t.Errorf("the token leaked into the permissive pair: %+v", got)
			}
		})
	}
}

// TestParseQueryRejectsABadRepositoryByName pins that the token routes through the one
// validation door and fails the whole line, as an unknown Status value does. ParseQuery does
// not adopt a partial filter, so the zero value comes back rather than the axes that parsed.
func TestParseQueryRejectsABadRepositoryByName(t *testing.T) {
	for _, tc := range []struct{ name, line, wants string }{
		{"a malformed ref", "repo:not-a-ref", "not-a-ref"},
		{"an unsupported host", "repo:gitlab.com/jv-k/gh-runs", "gitlab.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filter.ParseQuery("branch:main " + tc.line)
			if err == nil {
				t.Fatalf("ParseQuery accepted %q, want an error naming the value", tc.line)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not name %q", err, tc.wants)
			}
			if !reflect.DeepEqual(got, filter.Filter{}) {
				t.Errorf("a rejected line returned the partial filter %+v, want the zero value", got)
			}
		})
	}
}

// TestParseQueryAccumulatesRepositories pins the axis's OR: repeated tokens grow the set and
// a repeated value does not, the rule ParseStatus already follows for the permissive pair.
func TestParseQueryAccumulatesRepositories(t *testing.T) {
	got, err := filter.ParseQuery("repo:cli/cli repo:jv-k/gh-runs repo:cli/cli")
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	want := []domain.RepoID{
		{Host: domain.HostGitHub, Owner: "cli", Name: "cli"},
		{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"},
	}
	if !reflect.DeepEqual(got.Repos, want) {
		t.Errorf("Repos = %v, want %v: distinct tokens accumulate and a repeat does not", got.Repos, want)
	}
}

// TestQueryStringRendersTheRepositoryAxis pins the bare OWNER/REPO spelling. It round-trips
// exactly because NewRepoID rejects every host but github.com, so no RepoID in the tree
// carries a host the two-segment parse could lose. It is the spelling the config writer
// already uses for settings R7's exclude list, so the file and the input line agree.
func TestQueryStringRendersTheRepositoryAxis(t *testing.T) {
	f := filter.Filter{Workflow: "9004", Repos: []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}}
	if got, want := f.QueryString(), "workflow:9004 repo:cli/cli"; got != want {
		t.Errorf("QueryString = %q, want %q", got, want)
	}
}

// TestQueryEmitsNoRepositoryParameter pins the half that does not change. Repository is an
// endpoint choice rather than a query parameter, which ADR-0005 and ADR-0016 both fix, so
// the axis gaining a token must not grow it a server-side form.
func TestQueryEmitsNoRepositoryParameter(t *testing.T) {
	f := filter.Filter{Repos: []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}}
	if got := f.Query().Encode(); got != "" {
		t.Errorf("Query() = %q, want no parameter: the repository axis has no server-side form", got)
	}
}
