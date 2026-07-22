package scheduler

import (
	"math"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// dwell is R15's escalation dwell: R8a's own five-minute burn window, reused rather
// than invented. Burn is measured over that window, so five minutes is how long the
// measurement takes to fully reflect the previous stage's effect. Escalating faster
// than the measurement can respond is chasing noise (ADR-0021).
const dwell = 5 * time.Minute

// secondaryCeiling is the ~900 points/min secondary-limit budget the scheduler
// keeps its projected read consumption under (R11, the Constraints table). It is
// the binding constraint on cadence: conditional requests are free against the
// primary limit (ADR-0004), so the secondary pool is what the tiers and the
// auto-scale below are sized against. A GET costs one point, pessimistically
// including a 304 (open question 1).
const secondaryCeiling = 900.0

// pointsPerMinute is the seconds in a minute, the numerator of the projection: a
// poll set of n at an interval of i seconds issues n * (60/i) requests per minute,
// each one point.
const pointsPerMinute = 60.0

// projectedPointsPerMin is R11's projection: n repositories polled every interval
// issue n * (60 / interval-seconds) points per minute (AC8). It is the arithmetic
// the whole feature is shaped around, so it lives as one function the auto-scale and
// the tests both read.
func projectedPointsPerMin(n int, interval time.Duration) float64 {
	if interval <= 0 {
		return 0
	}
	return float64(n) * (pointsPerMinute / interval.Seconds())
}

// wholeSetInterval is the ambient (slow-tier) interval for a poll set of n: the R5
// slow target, stretched only if n is large enough that even the target would carry
// projected consumption past the secondary ceiling (R11, AC8). At reference scale
// (26) and well beyond it the target holds unstretched, because the slow tier at 30s
// is far under budget; the stretch is a guard for scale the reference account never
// reaches, and it is why no schedule ever polls the whole set at the medium tier's
// 5s (AC8). It is never faster than the slow target, so the whole set never polls
// faster than the ambient tier.
func wholeSetInterval(n int) time.Duration {
	if n <= 0 {
		return slowTarget
	}
	// The smallest interval that keeps n * (60/interval) at or under the ceiling is
	// 60n/ceiling seconds. Below the target this is a non-binding guard; above it,
	// it is the auto-scale R11 requires. Round up: truncating to the nanosecond would
	// leave the interval a hair short and the projection a hair over the ceiling.
	minForBudget := time.Duration(math.Ceil(pointsPerMinute * float64(n) / secondaryCeiling * float64(time.Second)))
	if minForBudget > slowTarget {
		return minForBudget
	}
	return slowTarget
}

// demotion is R15's staged, compounding, held-until-reset demotion state
// (ADR-0021). An episode begins at pressure onset, escalates one stage per dwell
// while pressure persists, freezes where escalation halts if pressure clears, and
// holds until the reset instant with no promotion inside the window.
type demotion struct {
	active      bool      // an episode is in progress
	onset       time.Time // when this episode's pressure onset occurred
	reset       time.Time // the rate-limit reset instant this episode holds until
	frozen      bool      // pressure has cleared; the stage no longer escalates
	frozenStage int       // the stage escalation halted at when pressure cleared
}

// observePressure folds a Budget Readout into the demotion state machine at now
// (R15, ADR-0021). It is the scheduler's whole reaction to pressure: it parses no
// rate-limit header and keeps no accounting of its own (R14), it reads the Readout's
// pressure flag and its reset instant and nothing else.
func (s *Scheduler) observePressure(r governor.Readout, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observePressureLocked(r, now)
}

// observePressureLocked is observePressure under the caller's lock.
func (s *Scheduler) observePressureLocked(r governor.Readout, now time.Time) {
	// A demotion holds until the reset, then the schedule recomputes from scratch
	// (R15, AC20). Ending the episode at or after its held reset is what lets the
	// undemoted intervals return, and what lets a fresh onset restart at stage one.
	if s.dem.active && !now.Before(s.dem.reset) {
		s.dem.active = false
	}

	if r.Pressure {
		if !s.dem.active {
			// Onset: a fresh episode, holding until this Readout's reset and
			// escalating from stage one (ADR-0021). R8a guarantees a non-zero reset
			// whenever pressure is true, so the hold always has an end.
			s.dem = demotion{active: true, onset: now, reset: r.Reset}
		}
		// Pressure persisting needs no further action: stageAtLocked escalates the
		// stage from the elapsed time while the episode is unfrozen.
		return
	}

	// Pressure has cleared. Freeze the stage where escalation halted and hold it,
	// unpromoted, until the reset (R15, AC20). A re-fire inside the same window
	// leaves it frozen, because escalation needs pressure to have persisted and a
	// clear broke that; holding the more-demoted posture is R8a's safe direction.
	if s.dem.active && !s.dem.frozen {
		// Read the stage before setting frozen: stageAtLocked returns frozenStage
		// once frozen is set, so the order matters.
		s.dem.frozenStage = s.stageAtLocked(now)
		s.dem.frozen = true
	}
}

// stageAtLocked is the demotion stage at now: 0 when no episode is active, the
// frozen stage once pressure has cleared, otherwise one plus the number of whole
// dwells since onset (R15, ADR-0021). Stage one begins at onset, stage two a dwell
// later, and so on with no cap.
func (s *Scheduler) stageAtLocked(now time.Time) int {
	if !s.dem.active {
		return 0
	}
	if s.dem.frozen {
		return s.dem.frozenStage
	}
	if now.Before(s.dem.onset) {
		return 0
	}
	return int(now.Sub(s.dem.onset)/dwell) + 1
}

// intervalFor is the poll interval for a tier at now: the tier's base cadence
// (the slow tier's auto-scaled to the current poll-set size, R11) shifted by the
// active demotion stage (R15). It is the one place the tiers, the budget maths and
// the demotion arithmetic meet.
func (s *Scheduler) intervalFor(t Tier, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intervalForLocked(t, now, s.slowBaseLocked())
}

// intervalForLocked is intervalFor under the caller's lock, taking the precomputed
// slow-tier base so the scheduling loop resolves the poll-set size once per tick
// rather than once per repository.
func (s *Scheduler) intervalForLocked(t Tier, now time.Time, slowBase time.Duration) time.Duration {
	var base time.Duration
	switch t {
	case tierFast:
		base = fastTarget
	case tierMedium:
		base = mediumTarget
	default:
		base = slowBase
	}
	// Compounding doubles: every already-demoted tier's interval doubles again at
	// each step, and a tier joins the demotion at twice its target (ADR-0021). The
	// slow tier demotes from stage one, the medium from stage two, the fast from
	// stage three, which is R15's "least visible first" as arithmetic.
	return base << demoteSteps(t, s.stageAtLocked(now))
}

// demoteSteps is how many times a tier's interval has doubled at a demotion stage
// (R15, ADR-0021). The slow tier doubles every step from stage one, the medium tier
// from stage two, the fast tier from stage three. At stage three the slow tier has
// stretched eightfold while the fast tier has conceded 3s to 6s.
func demoteSteps(t Tier, stage int) int {
	switch t {
	case tierSlow:
		return stage
	case tierMedium:
		return max(0, stage-1)
	case tierFast:
		return max(0, stage-2)
	default:
		return 0
	}
}

// slowBaseLocked is the slow tier's base interval: the ambient interval for the
// current poll set (R11), auto-scaled only at a scale the reference account never
// reaches. A nil poll set (the pure-arithmetic tests) reads as size zero and yields
// the unstretched slow target.
func (s *Scheduler) slowBaseLocked() time.Duration {
	n := 0
	if s.opts.PollSet != nil {
		n = len(s.opts.PollSet.PollSet())
	}
	return wholeSetInterval(n)
}
