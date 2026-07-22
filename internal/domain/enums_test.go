package domain_test

import (
	"reflect"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestStatusValues pins the six Statuses the filter engine validates against
// (ADR-0016: "the membership lists move to domain"). The list is gh's -s enum
// Status half (cli-surface R4), and the order is the const block's so a reader
// can diff the two by eye. StatusValues must not carry a Conclusion.
func TestStatusValues(t *testing.T) {
	want := []domain.Status{
		domain.StatusQueued,
		domain.StatusInProgress,
		domain.StatusCompleted,
		domain.StatusWaiting,
		domain.StatusRequested,
		domain.StatusPending,
	}
	if got := domain.StatusValues(); !reflect.DeepEqual(got, want) {
		t.Fatalf("StatusValues() = %v, want %v", got, want)
	}
}

// TestConclusionValues pins the nine Conclusions the filter engine validates
// against (ADR-0016). It is gh's -s enum Conclusion half (cli-surface R4), and
// it must exclude ConclusionNone: the "" null is Conclusion's zero value, never
// a value a person types at -s, and including it would let ParseStatus classify
// the empty string as a valid conclusion.
func TestConclusionValues(t *testing.T) {
	want := []domain.Conclusion{
		domain.ConclusionSuccess,
		domain.ConclusionFailure,
		domain.ConclusionCancelled,
		domain.ConclusionSkipped,
		domain.ConclusionTimedOut,
		domain.ConclusionNeutral,
		domain.ConclusionActionRequired,
		domain.ConclusionStale,
		domain.ConclusionStartupFailure,
	}
	if got := domain.ConclusionValues(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConclusionValues() = %v, want %v", got, want)
	}
	for _, c := range domain.ConclusionValues() {
		if c == domain.ConclusionNone {
			t.Fatalf("ConclusionValues() includes ConclusionNone (the null); it must not")
		}
	}
}

// TestValueListsSpanGhStatusEnum pins cli-surface R4's arithmetic: gh's 15-value
// -s enum is exactly the six Statuses plus the nine Conclusions, with nothing
// left over on either side. The count is the load-bearing fact the permissive
// parse rests on (ADR-0016), so it is asserted rather than assumed.
func TestValueListsSpanGhStatusEnum(t *testing.T) {
	if n := len(domain.StatusValues()) + len(domain.ConclusionValues()); n != 15 {
		t.Fatalf("Statuses + Conclusions = %d, want gh's 15-value -s enum", n)
	}
}
