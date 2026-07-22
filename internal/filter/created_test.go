package filter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestParseCreatedValidForms pins gh's date syntax and the bounds Match reads
// from it (ADR-0016). The Created bounds compare against created_at in UTC,
// everywhere. A bare date is the whole UTC day; the comparison operators and the
// A..B range pick the day's edges; a datetime is compared at its stated instant;
// and * opens a range on one side. lo is inclusive, hi exclusive, which is how
// "after the whole day" and "on or before the whole day" fall out uniformly.
func TestParseCreatedValidForms(t *testing.T) {
	cases := []struct {
		input  string
		checks []struct {
			created string
			want    bool
		}
	}{
		{
			input: "2024-01-15", // the whole UTC day
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-15T00:00:00Z", true},
				{"2024-01-15T23:59:59Z", true},
				{"2024-01-16T00:00:00Z", false},
				{"2024-01-14T23:59:59Z", false},
			},
		},
		{
			input: ">=2024-01-01",
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-01T00:00:00Z", true}, // inclusive lower bound
				{"2024-06-01T12:00:00Z", true},
				{"2023-12-31T23:59:59Z", false},
			},
		},
		{
			input: ">2024-01-15", // strictly after the whole day
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-16T00:00:00Z", true},
				{"2024-01-17T09:00:00Z", true},
				{"2024-01-15T12:00:00Z", false}, // same day is not "after"
				{"2024-01-15T23:59:59Z", false},
			},
		},
		{
			input: "<2024-01-15", // strictly before the day begins
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-14T23:59:59Z", true},
				{"2024-01-15T00:00:00Z", false},
				{"2024-01-15T12:00:00Z", false},
			},
		},
		{
			input: "<=2024-01-15", // on or before the whole day
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-15T23:59:59Z", true},
				{"2024-01-16T00:00:00Z", false},
			},
		},
		{
			input: "2024-01-01..2024-01-31", // inclusive of both days
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-01T00:00:00Z", true},
				{"2024-01-15T12:00:00Z", true},
				{"2024-01-31T23:59:59Z", true},
				{"2024-02-01T00:00:00Z", false},
				{"2023-12-31T23:59:59Z", false},
			},
		},
		{
			input: "2024-01-01..*", // open upper
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-01T00:00:00Z", true},
				{"2030-01-01T00:00:00Z", true},
				{"2023-12-31T23:59:59Z", false},
			},
		},
		{
			input: "*..2024-01-31", // open lower
			checks: []struct {
				created string
				want    bool
			}{
				{"2000-01-01T00:00:00Z", true},
				{"2024-01-31T23:59:59Z", true},
				{"2024-02-01T00:00:00Z", false},
			},
		},
		{
			input: ">=2024-01-15T12:00:00Z", // datetime precision
			checks: []struct {
				created string
				want    bool
			}{
				{"2024-01-15T12:00:00Z", true},
				{"2024-01-15T12:00:01Z", true},
				{"2024-01-15T11:59:59Z", false},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			dr, err := filter.ParseCreated(c.input)
			if err != nil {
				t.Fatalf("ParseCreated(%q) returned error %v, want nil", c.input, err)
			}
			f := filter.Filter{Created: dr}
			for _, chk := range c.checks {
				if got := f.Match(domain.Run{CreatedAt: at(chk.created)}); got != chk.want {
					t.Errorf("%q: Run created %s matched %v, want %v", c.input, chk.created, got, chk.want)
				}
			}
		})
	}
}

// TestParseCreatedComparesInUTC pins ADR-0016's "in UTC, everywhere": a
// created_at served in a non-UTC zone is compared at its UTC instant, so the
// boundary does not shift with the Run's zone. 2024-01-15T01:00:00+03:00 is
// 2024-01-14T22:00:00Z, which the 2024-01-15 day clause must exclude.
func TestParseCreatedComparesInUTC(t *testing.T) {
	dr, err := filter.ParseCreated("2024-01-15")
	if err != nil {
		t.Fatalf("ParseCreated returned error %v", err)
	}
	f := filter.Filter{Created: dr}
	// 01:00 at +03:00 is 22:00 the previous day in UTC: outside 2024-01-15.
	if f.Match(domain.Run{CreatedAt: at("2024-01-15T01:00:00+03:00")}) {
		t.Fatalf("a +03:00 instant that is 2024-01-14 in UTC matched the 2024-01-15 clause")
	}
	// 12:00 at +03:00 is 09:00Z, inside 2024-01-15.
	if !f.Match(domain.Run{CreatedAt: at("2024-01-15T12:00:00+03:00")}) {
		t.Fatalf("a +03:00 instant that is 2024-01-15 in UTC did not match the 2024-01-15 clause")
	}
}

// TestParseCreatedRejectsByName pins cli-surface R6 for the date axis: a value
// that is not gh's date syntax is rejected at construction, before any request,
// and the error names it (ADR-0016: DateRange parses at construction, which is
// where R6's reject-by-name lives).
func TestParseCreatedRejectsByName(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-date",
		"2024-13-01",         // month out of range
		"2024-02-31",         // day out of range
		"2024-1-5",           // not zero-padded ISO8601
		">",                  // operator, empty token
		"2024-01-15x",        // trailing garbage
		"*..*",               // constrains nothing
		"2024-01-01..",       // empty side, gh spells open ranges with *
		"2024-01-01..badday", // bad token on the far side
	} {
		if _, err := filter.ParseCreated(bad); err == nil {
			t.Errorf("ParseCreated(%q) returned nil, want an error", bad)
		} else if !strings.Contains(err.Error(), bad) && bad != "" {
			t.Errorf("ParseCreated(%q) error %q does not name the offending value", bad, err.Error())
		}
	}
}

// TestCreatedZeroValueIsNoConstraint pins ADR-0016's "the zero value is no
// clause": a Filter with no Created matches a Run of any creation time, so an
// unset date axis never narrows the result.
func TestCreatedZeroValueIsNoConstraint(t *testing.T) {
	var f filter.Filter // Created is the zero DateRange
	for _, ts := range []string{"1999-01-01T00:00:00Z", "2024-06-01T12:00:00Z", "2100-01-01T00:00:00Z"} {
		if !f.Match(domain.Run{CreatedAt: at(ts)}) {
			t.Fatalf("zero Created excluded a Run created %s; it must be no constraint", ts)
		}
	}
}
