package scheduler

import (
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// t0 is a fixed base instant for the demotion arithmetic. The clock is virtual, so
// the absolute value is irrelevant; only the offsets matter.
var t0 = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

// pressure builds a Readout reporting Budget pressure with a reset one hour out, far
// enough that the escalation stages under test all fall inside the window (R8a's
// projection guarantees a non-zero reset whenever pressure is true).
func pressure(reset time.Time) governor.Readout {
	return governor.Readout{Pressure: true, Reset: reset}
}

// TestDemotionStagesOnTheClock is AC19 and R15's staging and arithmetic. With
// pressure reported continuously from t=0, each five-minute burn window escalates
// one stage: the slow tier alone at onset (60s), the medium tier joining at t=5min
// (10s, slow 120s), the fast tier at t=10min (6s, medium 20s, slow 240s). At
// t=4min59s the medium tier is still unstretched.
func TestDemotionStagesOnTheClock(t *testing.T) {
	s := New(Options{})
	s.observePressure(pressure(t0.Add(time.Hour)), t0)

	cases := []struct {
		when               time.Duration
		slow, medium, fast time.Duration
		note               string
	}{
		{0, 60 * time.Second, 5 * time.Second, 3 * time.Second, "stage 1: slow alone"},
		{4*time.Minute + 59*time.Second, 60 * time.Second, 5 * time.Second, 3 * time.Second, "still stage 1 at 4m59s"},
		{5 * time.Minute, 120 * time.Second, 10 * time.Second, 3 * time.Second, "stage 2: medium joins"},
		{10 * time.Minute, 240 * time.Second, 20 * time.Second, 6 * time.Second, "stage 3: fast joins"},
	}
	for _, c := range cases {
		now := t0.Add(c.when)
		if got := s.intervalFor(tierSlow, now); got != c.slow {
			t.Errorf("%s: slow = %s, want %s", c.note, got, c.slow)
		}
		if got := s.intervalFor(tierMedium, now); got != c.medium {
			t.Errorf("%s: medium = %s, want %s", c.note, got, c.medium)
		}
		if got := s.intervalFor(tierFast, now); got != c.fast {
			t.Errorf("%s: fast = %s, want %s", c.note, got, c.fast)
		}
	}
}

// TestSlowTierDemotesBeforeFast is AC10 and R15's priority. At the first escalation
// step the slow tier's interval has grown (its rate has fallen) while the medium and
// fast tiers are untouched: demotion sacrifices the least visible work first.
// Requests per minute strictly decrease as the stages advance.
func TestSlowTierDemotesBeforeFast(t *testing.T) {
	s := New(Options{})
	s.observePressure(pressure(t0.Add(time.Hour)), t0)

	// Stage 1: slow has fallen, fast and medium have not.
	if got := s.intervalFor(tierSlow, t0); got <= slowTarget {
		t.Errorf("stage 1: slow = %s, want longer than the %s target (its rate must fall first)", got, slowTarget)
	}
	if got := s.intervalFor(tierFast, t0); got != fastTarget {
		t.Errorf("stage 1: fast = %s, want the %s target unchanged (the live tier is spared first)", got, fastTarget)
	}
	if got := s.intervalFor(tierMedium, t0); got != mediumTarget {
		t.Errorf("stage 1: medium = %s, want the %s target unchanged", got, mediumTarget)
	}

	// Requests per minute strictly decrease: the slow interval strictly grows across
	// the escalation steps.
	prev := s.intervalFor(tierSlow, t0)
	for _, when := range []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute} {
		got := s.intervalFor(tierSlow, t0.Add(when))
		if got <= prev {
			t.Errorf("slow interval at %s = %s, want strictly longer than the previous %s", when, got, prev)
		}
		prev = got
	}
}

// TestDemotionHoldsUntilReset is AC20 and R15's release rule. Pressure fires at t=0
// with a reset at t=40min and clears at t=6min. No tier is promoted before the
// reset: the slow tier stays at its stage-2 interval throughout the window. At the
// reset instant the schedule recomputes at undemoted intervals, and a fresh onset
// restarts escalation at the slow tier alone.
func TestDemotionHoldsUntilReset(t *testing.T) {
	s := New(Options{})
	reset := t0.Add(40 * time.Minute)
	s.observePressure(pressure(reset), t0)

	// Pressure clears at t=6min, by which point escalation has reached stage 2
	// (slow 120s). It freezes there.
	s.observePressure(governor.Readout{Pressure: false, Reset: reset}, t0.Add(6*time.Minute))

	// Held, not promoted, for the rest of the window. Undemoted slow would be 30s.
	for _, when := range []time.Duration{6 * time.Minute, 20 * time.Minute, 39*time.Minute + 59*time.Second} {
		if got := s.intervalFor(tierSlow, t0.Add(when)); got != 120*time.Second {
			t.Errorf("at %s (inside the window): slow = %s, want it held at 120s, not promoted", when, got)
		}
	}

	// At the reset instant the schedule recomputes at undemoted intervals.
	s.observePressure(governor.Readout{Pressure: false, Reset: reset}, reset)
	if got := s.intervalFor(tierSlow, reset); got != slowTarget {
		t.Errorf("at the reset: slow = %s, want the undemoted target %s", got, slowTarget)
	}

	// A fresh onset restarts escalation at the slow tier alone (stage 1).
	s.observePressure(pressure(reset.Add(time.Hour)), reset)
	if got := s.intervalFor(tierSlow, reset); got != 60*time.Second {
		t.Errorf("fresh onset: slow = %s, want 60s (stage one)", got)
	}
	if got := s.intervalFor(tierMedium, reset); got != mediumTarget {
		t.Errorf("fresh onset: medium = %s, want %s unchanged (slow tier alone)", got, mediumTarget)
	}
	if got := s.intervalFor(tierFast, reset); got != fastTarget {
		t.Errorf("fresh onset: fast = %s, want %s unchanged (slow tier alone)", got, fastTarget)
	}
}

// TestDemotionShiftIsClamped is the overflow guard on the demotion shift. The stage
// is uncapped by design (ADR-0021), but the shift that applies it is clamped so a
// very large stage never overflows a tier's interval into a negative (always-due)
// value, which would invert demotion into hammering (PRD R4's direction). A stage
// reachable only with a reset far longer than ADR-0021's 60-minute ceiling still
// yields a strictly positive, bounded interval for every tier.
func TestDemotionShiftIsClamped(t *testing.T) {
	s := New(Options{})
	// Onset at t0 with a reset far enough out that the episode never ends, so a query
	// 100 days later drives stageAtLocked to roughly 28,800, a stage no real
	// rate-limit window can reach.
	s.observePressure(pressure(t0.Add(10000*24*time.Hour)), t0)
	now := t0.Add(100 * 24 * time.Hour)

	ceiling := slowTarget << maxDemoteSteps
	for _, tier := range []Tier{tierSlow, tierMedium, tierFast} {
		got := s.intervalFor(tier, now)
		if got <= 0 {
			t.Errorf("%s interval at a huge stage = %s, want strictly positive (the shift overflowed)", tier, got)
		}
		if got > ceiling {
			t.Errorf("%s interval at a huge stage = %s, want at most the clamped ceiling %s", tier, got, ceiling)
		}
	}
}

// TestNoPressureNoDemotion is the baseline: with no pressure ever reported, every
// tier polls at its undemoted target.
func TestNoPressureNoDemotion(t *testing.T) {
	s := New(Options{})
	s.observePressure(governor.Readout{}, t0)
	if got := s.intervalFor(tierSlow, t0); got != slowTarget {
		t.Errorf("no pressure: slow = %s, want %s", got, slowTarget)
	}
	if got := s.intervalFor(tierMedium, t0); got != mediumTarget {
		t.Errorf("no pressure: medium = %s, want %s", got, mediumTarget)
	}
	if got := s.intervalFor(tierFast, t0); got != fastTarget {
		t.Errorf("no pressure: fast = %s, want %s", got, fastTarget)
	}
}
