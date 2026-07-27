package scheduler

import (
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestPinnedRepositoryIsPolledFasterThanAmbient is settings R7's pin half made
// observable, which is the whole reason the key exists (#97). A pinned repository that
// is off screen and has no live Run must be polled more often than an unpinned one in
// the same state. Asserting the intervals is the test the acceptance criterion asks
// for: the ordering of a slice nothing consumes would prove nothing.
func TestPinnedRepositoryIsPolledFasterThanAmbient(t *testing.T) {
	s := New(Options{})
	pinned, ambient := gh("acme", "pinned"), gh("acme", "ambient")
	s.SetPinned([]domain.RepoID{pinned})

	// Neither is on screen and neither has a live Run, so the pin is the only
	// difference between them.
	if got := s.tierOf(pinned); got != tierMedium {
		t.Errorf("pinned repository: tier = %v, want medium", got)
	}
	if got := s.tierOf(ambient); got != tierSlow {
		t.Errorf("unpinned off-screen repository: tier = %v, want slow", got)
	}

	now := time.Now()
	fast, slow := s.intervalFor(s.tierOf(pinned), now), s.intervalFor(s.tierOf(ambient), now)
	if fast >= slow {
		t.Errorf("pinned interval %v is not shorter than the ambient %v: the pin has no observable effect", fast, slow)
	}
}

// TestALiveRunStillOutranksAPin pins R7's fastest-tier-wins against the new input. A
// pin promotes to medium, and medium must never slow a repository whose Run is moving.
func TestALiveRunStillOutranksAPin(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "live")
	s.SetPinned([]domain.RepoID{id})
	s.setLastRuns(id, []domain.Run{run(domain.StatusInProgress)})

	if got := s.tierOf(id); got != tierFast {
		t.Errorf("pinned repository with a live Run: tier = %v, want fast (R7)", got)
	}
}

// TestUnpinningReturnsARepositoryToAmbient pins the release. The pin list is adopted
// live, exactly as the viewport and the poll set are (R3, AC17), so clearing it must
// return the repository to the tier it otherwise holds rather than stranding it in the
// medium tier for the session.
func TestUnpinningReturnsARepositoryToAmbient(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "pinned")
	s.SetPinned([]domain.RepoID{id})
	if got := s.tierOf(id); got != tierMedium {
		t.Fatalf("precondition: tier = %v, want medium", got)
	}

	s.SetPinned(nil)

	if got := s.tierOf(id); got != tierSlow {
		t.Errorf("after unpinning: tier = %v, want slow", got)
	}
}

// TestMediumTierIsPricedAgainstTheWholeCeiling is the acceptance criterion ADR-0021's
// constraint forces, and the reason the promotion is affordable at all.
//
// The viewport tier is self-capping at terminal height. A pin list is operator-authored
// and unbounded, so promoting it naively reintroduces the exact cliff the viewport
// reading was chosen to avoid: ADR-0021 measured the whole set at 5s as 312 points/min
// at 26 repositories and unaffordable at 100.
//
// The bound is the secondary ceiling the ambient tier is already auto-scaled against
// (R11), not a fresh constant. It is priced against the headroom that tier leaves rather
// than against the whole ceiling, because R11 bounds projected consumption and not one
// tier's share: pricing the tier alone lets a large poll set with a large pin list pass
// the ceiling while each tier looks affordable by itself.
//
// The first two rows are ADR-0021's own figures. At 26 the target holds unstretched and
// the projection is exactly the 312 points/min it measured. At 100, where it called the
// whole set at 5s unaffordable, the base stretches and holds the total at the ceiling.
func TestMediumTierIsPricedAgainstTheWholeCeiling(t *testing.T) {
	for _, tc := range []struct {
		name            string
		medium, total   int
		wantBase        time.Duration
		wantTotalPoints float64
	}{
		{"reference scale, part of the set on screen", 10, 26, mediumTarget, 152},
		{"reference scale, the whole set promoted", 26, 26, mediumTarget, 312},
		{"ADR-0021's cliff", 100, 100, 6666666667 * time.Nanosecond, secondaryCeiling},
		{"a pin list inside a much larger poll set", 100, 300, 12 * time.Second, secondaryCeiling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slowBase := wholeSetInterval(tc.total)
			got := mediumSetInterval(tc.medium, tc.total, slowBase)
			if got != tc.wantBase {
				t.Errorf("mediumSetInterval(%d, %d, %v) = %v, want %v",
					tc.medium, tc.total, slowBase, got, tc.wantBase)
			}
			// The projection R11 bounds is the whole schedule's, not one tier's.
			total := projectedPointsPerMin(tc.medium, got) +
				projectedPointsPerMin(tc.total-tc.medium, slowBase)
			if total > secondaryCeiling+0.001 {
				t.Errorf("%d medium of %d project %.1f points/min in total, over the %.0f ceiling",
					tc.medium, tc.total, total, secondaryCeiling)
			}
			if diff := total - tc.wantTotalPoints; diff > 0.001 || diff < -0.001 {
				t.Errorf("total projection = %.1f points/min, want %.1f", total, tc.wantTotalPoints)
			}
		})
	}
}

// TestPromotionIsNeverAnInversion pins the direction at every scale. A medium base
// slower than the ambient one would poll a pinned repository less often than an
// unpinned one, which is settings R7's "pinning MUST prioritise" inverted and R7's
// fastest-tier-wins broken. Past the point where the budget can afford a promotion the
// two tiers converge instead, so a pin is worth nothing rather than worth less than
// nothing.
func TestPromotionIsNeverAnInversion(t *testing.T) {
	for _, tc := range []struct{ medium, total int }{
		{1, 26}, {26, 26}, {100, 100}, {100, 300}, {451, 451}, {500, 500}, {2000, 2000},
	} {
		slowBase := wholeSetInterval(tc.total)
		got := mediumSetInterval(tc.medium, tc.total, slowBase)
		if got > slowBase {
			t.Errorf("mediumSetInterval(%d, %d, %v) = %v, slower than the ambient tier: the pin is a demotion",
				tc.medium, tc.total, slowBase, got)
		}
	}
}

// TestMediumBaseNeverOutrunsTheTarget pins the guard's other direction. The auto-scale
// may only stretch the medium tier, never shorten it: a small pin list must not be read
// as licence to poll faster than R5's 5s target.
func TestMediumBaseNeverOutrunsTheTarget(t *testing.T) {
	for _, tc := range []struct{ medium, total int }{{0, 0}, {1, 26}, {5, 26}, {26, 26}} {
		if got := mediumSetInterval(tc.medium, tc.total, wholeSetInterval(tc.total)); got < mediumTarget {
			t.Errorf("mediumSetInterval(%d, %d) = %v, faster than the %v target",
				tc.medium, tc.total, got, mediumTarget)
		}
	}
}

// TestPinningAnExcludedRepositoryPollsItNever is settings AC14's pin side, asserted at
// the wire rather than over a slice (#97). A repository named in both lists is excluded,
// and the pin has no observable effect at all.
//
// The two settings meet at different layers, which is what makes the outcome unambiguous
// rather than a precedence rule someone has to remember. Exclusion is applied at
// discovery, so an excluded repository never enters the poll set; the pin is a tier
// input, and a tier is only ever chosen for a repository the poll set already carries.
// There is no order of operations in which a pin reaches a repository exclusion removed.
//
// Excluding is modelled here as discovery does it, by the repository being absent from
// the poll set, because that is exactly the state discovery leaves behind (its own
// exclude_test.go pins that it does). The pin names it anyway, as an operator who wrote
// both lists would.
func TestPinningAnExcludedRepositoryPollsItNever(t *testing.T) {
	polled, excluded := gh("acme", "polled"), gh("acme", "excluded")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{polled}})
	h.s.SetPinned([]domain.RepoID{excluded, polled})
	h.start(t)

	h.waitPolls(t, 1)
	// The in-set repository is pinned, so the loop settles on the medium interval its
	// promotion bought it, not the ambient one.
	h.waitSettle(t, mediumTarget)
	// Advance several medium intervals, so a promotion that did reach the excluded
	// repository would have polled it by now.
	for range 3 {
		h.clk.Advance(mediumTarget)
		h.waitPolls(t, 1)
		h.waitSettle(t, mediumTarget)
	}

	if got := h.counting.countPath("/repos/acme/excluded/"); got != 0 {
		t.Errorf("the excluded repository was polled %d times despite being excluded: %v",
			got, h.counting.urls)
	}
	// The negative alone would pass on a scheduler that polled nothing, so pin the
	// positive beside it: the repository that was only pinned is polled normally.
	if got := h.counting.countPath("/repos/acme/polled/"); got < 1 {
		t.Errorf("the pinned repository in the poll set was polled %d times, want it polling", got)
	}
}

// TestPinnedRepositoryPollsMoreOftenOverVirtualTime is the acceptance criterion in the
// form it asks for: proved on the injected clock at known virtual times, by counting
// what reached the wire, rather than by comparing two durations a function returned.
//
// Both repositories are in the poll set, both are off screen, and neither holds a live
// Run, so the pin is the only difference between them. Over one slow interval of virtual
// time the pinned one is due at every medium interval and the unpinned one once, which
// is the whole observable effect settings R7's pin half buys (#97).
func TestPinnedRepositoryPollsMoreOftenOverVirtualTime(t *testing.T) {
	pinned, ambient := gh("acme", "pinned"), gh("acme", "ambient")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{pinned, ambient}})
	h.s.SetPinned([]domain.RepoID{pinned})
	h.start(t)

	// Cold start polls both, then the loop settles on the shorter of the two intervals.
	h.waitPolls(t, 2)
	h.waitSettle(t, mediumTarget)

	// Advance exactly one slow interval, one medium interval at a time. The pinned
	// repository comes due at each step; the unpinned one only at the last.
	//
	// Each step waits for every poll that step released, which is the barrier the tallies
	// below are read through. The step where the slow interval elapses releases two,
	// because both repositories come due together there. Waiting for one lets the loop
	// stamp both polls and settle while the second goroutine has not yet reached the wire,
	// so the assertion reads a tally one short, on whichever of the two lost the race
	// (#133). No barrier on the loop's timer is needed beside this one: the loop arms its
	// timer before it publishes the wait, so waitSettle already means the timer is armed.
	//
	// Which step that is comes from the same interval arithmetic the tallies below assert,
	// not from the step's position, so a change to either target moves the barrier with it
	// rather than leaving it silently one poll short.
	const steps = int(slowTarget / mediumTarget) // 6
	for step := range steps {
		h.clk.Advance(mediumTarget)
		wantPolls := 1 // the pinned repository, due at every step
		if elapsed := time.Duration(step+1) * mediumTarget; elapsed%slowTarget == 0 {
			wantPolls++ // and the unpinned one's single slow tick
		}
		h.waitPolls(t, wantPolls)
		h.waitSettle(t, mediumTarget)
	}

	pinnedPolls := h.counting.countPath("/repos/acme/pinned/")
	ambientPolls := h.counting.countPath("/repos/acme/ambient/")
	if pinnedPolls <= ambientPolls {
		t.Errorf("over %v of virtual time the pinned repository was polled %d times and the "+
			"unpinned one %d: the pin bought no extra liveness", slowTarget, pinnedPolls, ambientPolls)
	}
	// The cold-start poll plus one per medium interval. Asserting the figure rather than
	// only the ordering is what makes this the cadence rather than a coincidence.
	if want := 1 + steps; pinnedPolls != want {
		t.Errorf("pinned repository polled %d times over %v, want %d (one per %v)",
			pinnedPolls, slowTarget, want, mediumTarget)
	}
	if ambientPolls != 2 {
		t.Errorf("unpinned repository polled %d times over %v, want 2 (cold start and one %v tick)",
			ambientPolls, slowTarget, slowTarget)
	}
}
