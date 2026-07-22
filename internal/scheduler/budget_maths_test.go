package scheduler

import (
	"testing"
	"time"
)

// TestProjectedPoints is AC8's arithmetic and R11's table. A poll costs one
// secondary point (GET = 1 point), so a poll set of n at a given interval projects
// n * (60 / interval-seconds) points per minute. The reference figures are the
// spec's: 26 at 5s is 312 and fine, 100 at 5s is 1,200 and over the ~900 ceiling.
func TestProjectedPoints(t *testing.T) {
	cases := []struct {
		n        int
		interval time.Duration
		want     float64
	}{
		{26, 5 * time.Second, 312},   // R11: fine
		{26, 2 * time.Second, 780},   // R11: at the edge
		{26, 1 * time.Second, 1560},  // R11: over
		{100, 5 * time.Second, 1200}, // R11: over (the 100-repo cliff)
	}
	for _, c := range cases {
		if got := projectedPointsPerMin(c.n, c.interval); got != c.want {
			t.Errorf("projectedPointsPerMin(%d, %s) = %g, want %g", c.n, c.interval, got, c.want)
		}
	}
}

// TestReferenceScaleHoldsTheTarget is AC8's "26 at 5s" premise: at reference scale
// the whole poll set's ambient interval is the R5 slow target, unstretched. The
// auto-scaling in R11 is a guard for scale the reference account never reaches, not
// a tax on it.
func TestReferenceScaleHoldsTheTarget(t *testing.T) {
	if got := wholeSetInterval(26); got != slowTarget {
		t.Errorf("wholeSetInterval(26) = %s, want the slow target %s", got, slowTarget)
	}
	// A poll set of 100 is still under budget at the 30s ambient interval (200
	// points/min), so it too holds the target: the medium tier's 5s is what the
	// widest reading tried to apply to all 100, and ADR-0021's viewport cap, not a
	// stretched ambient interval, is what prevents that.
	if got := wholeSetInterval(100); got != slowTarget {
		t.Errorf("wholeSetInterval(100) = %s, want the slow target %s", got, slowTarget)
	}
}

// TestIntervalAutoScales is AC8's "the interval auto-scales instead" and R11's
// requirement to reduce frequency so projected consumption stays under the secondary
// limit. A poll set large enough that even the 30s ambient interval would exceed the
// ceiling gets a stretched interval, and the projection at that interval is within
// the ceiling.
func TestIntervalAutoScales(t *testing.T) {
	const big = 1000 // 1000 at 30s would be 2,000 points/min, well over ~900
	got := wholeSetInterval(big)
	if got <= slowTarget {
		t.Fatalf("wholeSetInterval(%d) = %s, want longer than the slow target %s (auto-scaled)", big, got, slowTarget)
	}
	if p := projectedPointsPerMin(big, got); p > secondaryCeiling {
		t.Errorf("projected %g points/min at the auto-scaled interval, want within the ~%g ceiling", p, secondaryCeiling)
	}
}

// TestWholeSetNeverPollsAtMediumInterval is AC8's "no schedule is produced that
// polls all of them at 5s". The whole poll set's ambient interval is never as fast
// as the medium tier's 5s, whatever the poll-set size, so the whole set is never
// polled at 5s. Only the viewport-capped medium tier polls that fast.
func TestWholeSetNeverPollsAtMediumInterval(t *testing.T) {
	if slowTarget <= mediumTarget {
		t.Fatalf("slow target %s is not slower than medium %s", slowTarget, mediumTarget)
	}
	for _, n := range []int{1, 26, 100, 1000, 10000} {
		if got := wholeSetInterval(n); got < slowTarget {
			t.Errorf("wholeSetInterval(%d) = %s, faster than the slow target %s", n, got, slowTarget)
		}
	}
}
