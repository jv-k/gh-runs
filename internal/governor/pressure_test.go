package governor_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestPressureIsAProjection pins AC11: pressure is a projection, not a
// percentage, and routine work does not trip it. Two runs replay the same
// reference cold start (~189 requests) and differ only in the headers. Against a
// healthy remaining the burst projects exhaustion far past the reset, so no
// pressure. Against a low remaining the same burst projects past the reset, so
// pressure. No percentage of the limit appears anywhere.
func TestPressureIsAProjection(t *testing.T) {
	const feedCount = 189            // ~163 discovery probes plus ~26 Feed payloads
	const resetOut = baseUnix + 2700 // the reset is 45 minutes out

	clk := baseClock()
	healthy := governor.New(respondWith(http.StatusOK, 4811, resetOut, "{}"), clk)
	getN(t, healthy, feedCount)
	if healthy.Readout().Pressure {
		t.Errorf("the reference cold start reported pressure; routine work must not trip it (R8a, AC11)")
	}

	clk2 := baseClock()
	strained := governor.New(respondWith(http.StatusOK, 100, resetOut, "{}"), clk2)
	getN(t, strained, feedCount)
	if !strained.Readout().Pressure {
		t.Errorf("the same burst against a low remaining did not project past the reset (R8a, AC11)")
	}
}

// TestZeroBurnIsNotPressure pins AC11b's two halves. A five-minute window of
// nothing but 304s carries zero burn, so no pressure and no division, whatever
// remaining says. A real burn ending at remaining: 0 reports pressure and
// exhaustion together.
func TestZeroBurnIsNotPressure(t *testing.T) {
	clk := baseClock()
	allNotModified := governor.New(respondWith(http.StatusNotModified, 5, baseUnix+300, ""), clk)
	for i := 0; i < 50; i++ {
		getN(t, allNotModified, 1)
		clk.Advance(6 * time.Second) // spread across five minutes of virtual time
	}
	if allNotModified.Readout().Pressure {
		t.Errorf("a window of 304s reported pressure; zero burn is never pressure, however low remaining (R8a, AC11b)")
	}

	clk2 := baseClock()
	spent := governor.New(respondWith(http.StatusOK, 0, baseUnix+300, "{}"), clk2)
	getN(t, spent, 30) // real burn, and the headers report nothing left
	ro := spent.Readout()
	if !ro.Pressure {
		t.Errorf("remaining:0 with real burn did not report pressure (R8a, AC11b)")
	}
	if !ro.Exhausted {
		t.Errorf("remaining:0 did not report exhaustion alongside pressure (R9, AC11b)")
	}
}
