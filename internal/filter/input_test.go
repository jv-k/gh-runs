package filter_test

import (
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
			if back.Query().Encode() != c.in.Query().Encode() {
				t.Errorf("round trip changed the server-side half: %q, want %q", back.Query().Encode(), c.in.Query().Encode())
			}
		})
	}
}

// TestQueryStringOmitsTheRepositoryAxis pins the one axis with no token, matching the one
// axis with no query parameter (ADR-0016). It is the Feed's own scoping, set from the view
// rather than from a line of text, so a line never carries it and never claims to.
func TestQueryStringOmitsTheRepositoryAxis(t *testing.T) {
	f := filter.Filter{Workflow: "9004", Repos: []domain.RepoID{{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}}}
	if got := f.QueryString(); got != "workflow:9004" {
		t.Errorf("QueryString = %q, want %q: the repository axis has no token", got, "workflow:9004")
	}
}
