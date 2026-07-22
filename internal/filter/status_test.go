package filter_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestParseStatusClassifies pins ADR-0016's parse: one -s value is a membership
// lookup, found in the Status list it appends to Statuses, found in the
// Conclusion list it appends to Conclusions, and no type ever holds a value that
// might be either. startup_failure is the boundary case cli-surface R4 resolved
// onto the Conclusion side, so it is pinned explicitly.
func TestParseStatusClassifies(t *testing.T) {
	cases := []struct {
		value           string
		wantStatuses    []domain.Status
		wantConclusions []domain.Conclusion
	}{
		{"completed", []domain.Status{domain.StatusCompleted}, nil},
		{"in_progress", []domain.Status{domain.StatusInProgress}, nil},
		{"waiting", []domain.Status{domain.StatusWaiting}, nil},
		{"failure", nil, []domain.Conclusion{domain.ConclusionFailure}},
		{"action_required", nil, []domain.Conclusion{domain.ConclusionActionRequired}},
		{"startup_failure", nil, []domain.Conclusion{domain.ConclusionStartupFailure}},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			var f filter.Filter
			if err := f.ParseStatus(c.value); err != nil {
				t.Fatalf("ParseStatus(%q) returned error %v, want nil", c.value, err)
			}
			if !equalStatuses(f.Statuses, c.wantStatuses) {
				t.Fatalf("after ParseStatus(%q), Statuses = %v, want %v", c.value, f.Statuses, c.wantStatuses)
			}
			if !equalConclusions(f.Conclusions, c.wantConclusions) {
				t.Fatalf("after ParseStatus(%q), Conclusions = %v, want %v", c.value, f.Conclusions, c.wantConclusions)
			}
		})
	}
}

// TestParseStatusAccumulates pins that ParseStatus appends rather than replaces,
// so a multi-select filter input (or the approvals saved filter) builds the pair
// value by value across calls.
func TestParseStatusAccumulates(t *testing.T) {
	var f filter.Filter
	for _, v := range []string{"queued", "failure", "waiting"} {
		if err := f.ParseStatus(v); err != nil {
			t.Fatalf("ParseStatus(%q) returned error %v", v, err)
		}
	}
	wantS := []domain.Status{domain.StatusQueued, domain.StatusWaiting}
	wantC := []domain.Conclusion{domain.ConclusionFailure}
	if !equalStatuses(f.Statuses, wantS) {
		t.Fatalf("Statuses = %v, want %v", f.Statuses, wantS)
	}
	if !equalConclusions(f.Conclusions, wantC) {
		t.Fatalf("Conclusions = %v, want %v", f.Conclusions, wantC)
	}
}

// TestParseStatusDeduplicates pins that a repeated -s value does not grow its
// set. ParseStatus builds two sets, so passing the same value twice leaves one
// entry, which keeps Match linear over a bounded pair rather than over a set an
// operator can inflate by repeating a flag. Distinct values still accumulate.
func TestParseStatusDeduplicates(t *testing.T) {
	var f filter.Filter
	for _, v := range []string{"failure", "failure", "queued", "queued", "failure"} {
		if err := f.ParseStatus(v); err != nil {
			t.Fatalf("ParseStatus(%q) returned error %v", v, err)
		}
	}
	wantS := []domain.Status{domain.StatusQueued}
	wantC := []domain.Conclusion{domain.ConclusionFailure}
	if !equalStatuses(f.Statuses, wantS) {
		t.Fatalf("Statuses = %v, want %v: repeated values must not duplicate", f.Statuses, wantS)
	}
	if !equalConclusions(f.Conclusions, wantC) {
		t.Fatalf("Conclusions = %v, want %v: repeated values must not duplicate", f.Conclusions, wantC)
	}
}

// TestParseStatusRejectsTypoByName pins cli-surface R6 on its measured failure
// mode: -s faliure must be rejected by name, client-side, before any request. An
// unchecked typo reaches the wire and comes back with every Run in the
// repository, which reads as "no failures" rather than "you typed it wrong". The
// error must name the offending value, and the sets must stay untouched.
func TestParseStatusRejectsTypoByName(t *testing.T) {
	for _, bad := range []string{"faliure", "", "succes", "in-progress", "SUCCESS"} {
		var f filter.Filter
		err := f.ParseStatus(bad)
		if err == nil {
			t.Fatalf("ParseStatus(%q) returned nil, want an error", bad)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Fatalf("ParseStatus(%q) error %q does not name the offending value", bad, err.Error())
		}
		if len(f.Statuses) != 0 || len(f.Conclusions) != 0 {
			t.Fatalf("ParseStatus(%q) mutated the sets on rejection: %v / %v", bad, f.Statuses, f.Conclusions)
		}
	}
}

// TestParseStatusAcceptsEveryEnumValue pins ADR-0016's single-validation-point
// claim: filter accepts exactly what domain declares, so the accepted set and the
// domain vocabulary cannot drift. Every one of gh's 15 values classifies without
// error and lands in exactly one set, which is R4's arithmetic exercised through
// the parse rather than asserted over the lists alone.
func TestParseStatusAcceptsEveryEnumValue(t *testing.T) {
	for _, s := range domain.StatusValues() {
		var f filter.Filter
		if err := f.ParseStatus(string(s)); err != nil {
			t.Fatalf("ParseStatus(%q) returned error %v, want nil", s, err)
		}
		if len(f.Statuses) != 1 || f.Statuses[0] != s || len(f.Conclusions) != 0 {
			t.Fatalf("ParseStatus(%q) did not classify as the sole Status: %v / %v", s, f.Statuses, f.Conclusions)
		}
	}
	for _, c := range domain.ConclusionValues() {
		var f filter.Filter
		if err := f.ParseStatus(string(c)); err != nil {
			t.Fatalf("ParseStatus(%q) returned error %v, want nil", c, err)
		}
		if len(f.Conclusions) != 1 || f.Conclusions[0] != c || len(f.Statuses) != 0 {
			t.Fatalf("ParseStatus(%q) did not classify as the sole Conclusion: %v / %v", c, f.Statuses, f.Conclusions)
		}
	}
}

func equalStatuses(got, want []domain.Status) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalConclusions(got, want []domain.Conclusion) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
