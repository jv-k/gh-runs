package ghlink_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/ghlink"
)

// TestNextFollowsRelNext pins the crawl's continue signal: a Link header carrying
// rel="next" yields that URL (ADR-0005, purge R1).
func TestNextFollowsRelNext(t *testing.T) {
	header := `<https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2>; rel="next", ` +
		`<https://api.github.com/repos/o/r/actions/runs?per_page=100&page=287>; rel="last"`
	got := ghlink.Next(header)
	want := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2"
	if got != want {
		t.Fatalf("Next() = %q, want %q", got, want)
	}
}

// TestNextStopsWhenAbsent pins the crawl's stop signal: past the end the header
// carries prev, last and first but no next, and Next returns "" so the loop ends
// (ADR-0005, purge R2, AC5).
func TestNextStopsWhenAbsent(t *testing.T) {
	header := `<https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1>; rel="prev", ` +
		`<https://api.github.com/repos/o/r/actions/runs?per_page=100&page=287>; rel="last", ` +
		`<https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1>; rel="first"`
	if got := ghlink.Next(header); got != "" {
		t.Fatalf("Next() = %q, want \"\" past the end", got)
	}
}

// TestNextToleratesQueryCommas pins the bracket-aware parse: a listing URL carrying
// commas of its own must not be torn apart by a comma split (purge R1). This is a
// robustness case rather than an observation. It was written as /user/repos, which
// measurement has since contradicted (see TestNextReadsTheMeasuredUserReposHeader
// and the package note), so it keeps the shape and drops the claim about which
// endpoint produces it.
func TestNextToleratesQueryCommas(t *testing.T) {
	header := `<https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=2>; rel="next"`
	want := "https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=2"
	if got := ghlink.Next(header); got != want {
		t.Fatalf("Next() = %q, want %q", got, want)
	}
}

// TestNextReadsTheMeasuredUserReposHeader holds the header /user/repos actually
// emits, copied from the wire on 2026-07-27 rather than composed here. It differs
// from the one above in the way that matters: the commas the request sent come back
// percent-encoded, and rel="last" is present alongside rel="next". Next must return
// the encoded URL unaltered, because discovery re-requests exactly that string and
// discovery's cassette matcher compares it byte for byte (#154).
func TestNextReadsTheMeasuredUserReposHeader(t *testing.T) {
	header := `<https://api.github.com/user/repos?per_page=100&affiliation=owner%2Ccollaborator%2Corganization_member&page=2>; rel="next", ` +
		`<https://api.github.com/user/repos?per_page=100&affiliation=owner%2Ccollaborator%2Corganization_member&page=2>; rel="last"`
	want := "https://api.github.com/user/repos?per_page=100&affiliation=owner%2Ccollaborator%2Corganization_member&page=2"
	if got := ghlink.Next(header); got != want {
		t.Fatalf("Next() = %q, want %q", got, want)
	}
}

// TestNextEmptyHeader is the first page's case: no Link header at all yields "".
func TestNextEmptyHeader(t *testing.T) {
	if got := ghlink.Next(""); got != "" {
		t.Fatalf("Next(\"\") = %q, want \"\"", got)
	}
}
