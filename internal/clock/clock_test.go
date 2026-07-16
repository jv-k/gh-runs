package clock_test

import (
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/clock"
)

// TestFakeClockSatisfiesInterface pins the seam ADR-0011 designed the package
// around: clockwork's fake is assignable to clock.Clock, so every timing test in
// the tree advances virtual time rather than sleeping (local-store R17,
// rate-governor R21).
func TestFakeClockSatisfiesInterface(t *testing.T) {
	start := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	var c clock.Clock = clockwork.NewFakeClockAt(start)
	if !c.Now().Equal(start) {
		t.Fatalf("fake clock Now() = %v, want %v", c.Now(), start)
	}
}

// TestSystemClockReads is a smoke test that the production clock returns a real
// time rather than the zero value.
func TestSystemClockReads(t *testing.T) {
	if clock.System().Now().IsZero() {
		t.Fatal("system clock returned the zero time")
	}
}
