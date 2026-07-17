package clock_test

import (
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// TestFakeClockSatisfiesAlias pins the seam ADR-0011 and ADR-0014 designed the
// package around: clock.Clock is an alias for clockwork.Clock, so clockwork's
// fake is assignable to it directly and every timing test in the tree advances
// virtual time rather than sleeping (local-store R17, rate-governor R21).
func TestFakeClockSatisfiesAlias(t *testing.T) {
	start := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	var c clock.Clock = clockwork.NewFakeClockAt(start)
	if !c.Now().Equal(start) {
		t.Fatalf("fake clock Now() = %v, want %v", c.Now(), start)
	}
}

// TestRealClockReads is a smoke test that the production clock returns a real
// time rather than the zero value.
func TestRealClockReads(t *testing.T) {
	if clock.Real().Now().IsZero() {
		t.Fatal("real clock returned the zero time")
	}
}
