package governor_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestCeilingTracksReads pins R11 and AC2a: the write ceiling is dynamic,
// computed as (900 - observed read points per minute) / 5 deletes per minute,
// capped at 2.5/sec and floored at 0.5/sec. With the scheduler's reads consuming
// 312 points/min the ramp stops near 1.96/sec; with no reads in flight the same
// ramp reaches the 2.5 cap. The two runs differ only in the reads the governor
// observes, never in configuration.
func TestCeilingTracksReads(t *testing.T) {
	// Run 1: 312 read points sit inside the trailing minute (the clock does not
	// advance, so every read is current). (900 - 312) / 300 = 1.96/sec.
	clk := baseClock()
	g := governor.New(respondWith(http.StatusOK, 4811, baseUnix+3600, "{}"), clk)
	getN(t, g, 312)
	if got := g.WriteRate(); got < 1.9 || got > 2.0 {
		t.Errorf("with 312 read points/min the ceiling = %v, want ~1.96 = (900-312)/300 (R11, AC2a)", got)
	}
	if got := g.WriteRate(); got == 2.5 {
		t.Errorf("the ramp reached the 2.5 cap despite 312 read points/min; the ceiling must track the reads (AC2a)")
	}

	// Run 2: the same ramp with no reads in flight reaches the 2.5 cap. The reads
	// that drove the ramp are aged out of the 60s window before the rate is read.
	clk2 := baseClock()
	g2 := governor.New(respondWith(http.StatusOK, 4811, baseUnix+3600, "{}"), clk2)
	getN(t, g2, 120)
	clk2.Advance(61 * time.Second)
	if got := g2.WriteRate(); got != 2.5 {
		t.Errorf("with no reads in flight the ceiling = %v, want the 2.5 cap (R11, AC2a)", got)
	}
}
