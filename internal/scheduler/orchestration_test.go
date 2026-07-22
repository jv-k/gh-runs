package scheduler

import (
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestPollSetChangesLive is AC7 and R3. A repository discovery adds enters the
// rotation at the slow tier with no restart, and one it removes stops being polled
// within one interval. The poll set is read every scheduling decision, so the change
// is adopted live.
func TestPollSetChangesLive(t *testing.T) {
	a, b := gh("acme", "a"), gh("acme", "b")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}})
	h.start(t)

	// A is polled at the cold start; B is not in the set yet.
	h.waitPolls(t, 1)
	h.waitSettle(t, slowTarget)
	if got := h.counting.countPath("/repos/acme/a/"); got != 1 {
		t.Fatalf("A polled %d times at cold start, want 1", got)
	}
	if got := h.counting.countPath("/repos/acme/b/"); got != 0 {
		t.Fatalf("B polled %d times before it was added, want 0", got)
	}

	// Add B live. At the next scheduling decision it enters at the slow tier.
	h.ps.set(a, b)
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 2) // A due again, B for the first time
	h.waitSettle(t, slowTarget)
	if got := h.counting.countPath("/repos/acme/b/"); got < 1 {
		t.Errorf("B polled %d times after being added, want it to have begun polling (AC7)", got)
	}

	// Remove A live. It stops being polled within one interval.
	aBefore := h.counting.countPath("/repos/acme/a/")
	h.ps.set(b)
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 1) // only B
	if got := h.counting.countPath("/repos/acme/a/"); got != aBefore {
		t.Errorf("A polled %d times after removal, want it held at %d (stopped within one interval)", got, aBefore)
	}
}

// TestExhaustionStopsAndResumes is AC11 and R16. With the Budget Readout reporting
// exhaustion and a reset instant, the scheduler issues zero polls until virtual time
// reaches that instant and publishes it as the resume time. A paused Feed that says
// when it resumes is correct; one that looks live and is not is the failure R16
// prevents.
func TestExhaustionStopsAndResumes(t *testing.T) {
	a := gh("acme", "a")
	budget := &fakeBudget{}
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}, budget: budget})

	resume := t0.Add(20 * time.Minute)
	budget.fn = func() governor.Readout {
		if h.clk.Now().Before(resume) {
			return governor.Readout{Exhausted: true, Reset: resume}
		}
		return governor.Readout{Remaining: 5000}
	}
	h.start(t)

	// Exhausted: the loop pauses, publishes the resume instant, and issues no poll.
	h.waitSettle(t, resume.Sub(t0))
	gotResume, paused := h.s.Paused()
	if !paused || !gotResume.Equal(resume) {
		t.Fatalf("Paused() = (%v, %v), want (%v, true)", gotResume, paused, resume)
	}
	if got := h.counting.count(); got != 0 {
		t.Errorf("wire requests while exhausted = %d, want 0", got)
	}

	// At the resume instant, scheduling resumes and the pause clears.
	h.blockUntil(1)
	h.clk.Advance(20 * time.Minute)
	h.waitPolls(t, 1)
	if got := h.counting.count(); got != 1 {
		t.Errorf("wire requests after the resume instant = %d, want 1", got)
	}
	if _, paused := h.s.Paused(); paused {
		t.Errorf("still paused after the resume instant")
	}
}

// TestExhaustionWithZeroResetWaitsTheSlowInterval is R16's degenerate case and the
// untilResume slow-target branch. A Readout that is exhausted but carries no resume
// instant (a zero or already-past reset) must not busy-spin: the loop stays paused,
// issues no poll, and re-checks on the slow-tier interval rather than immediately. The
// governor may supply a resume time on a later response, so the loop re-reads the
// Readout each slow tick rather than sleeping forever.
func TestExhaustionWithZeroResetWaitsTheSlowInterval(t *testing.T) {
	a := gh("acme", "a")
	budget := &fakeBudget{}
	budget.set(governor.Readout{Exhausted: true}) // zero Reset: no resume instant known
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}, budget: budget})
	h.start(t)

	// Exhausted with no reset: the loop pauses and waits the slow interval, not zero.
	h.waitSettle(t, slowTarget)
	if _, paused := h.s.Paused(); !paused {
		t.Fatalf("Paused() = false, want true (exhausted with a zero reset must still pause)")
	}
	if got := h.counting.count(); got != 0 {
		t.Errorf("wire requests while exhausted = %d, want 0", got)
	}

	// It stays paused across the slow-interval re-check: still no poll, still paused.
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitSettle(t, slowTarget)
	if _, paused := h.s.Paused(); !paused {
		t.Errorf("Paused() = false after a slow-interval re-check, want it still paused")
	}
	if got := h.counting.count(); got != 0 {
		t.Errorf("wire requests after the re-check = %d, want 0 (still exhausted)", got)
	}
}

// TestDemotesUnderForeignPressure is AC10 and AC12. With the Budget Readout reporting
// pressure the scheduler did not itself cause, the scheduler demotes exactly as it
// would for its own consumption: the slow tier stretches from 30s to 60s, so requests
// per minute fall. The scheduler reacts to the Readout, never to a tally of its own
// requests (R13).
func TestDemotesUnderForeignPressure(t *testing.T) {
	a := gh("acme", "a")
	budget := &fakeBudget{}
	budget.set(governor.Readout{Pressure: true, Reset: t0.Add(time.Hour)})
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}, budget: budget})
	h.start(t)

	// Cold start under pressure: the slow tier is demoted to 60s from onset, so A is
	// not due again at 30s.
	h.waitPolls(t, 1)
	h.waitSettle(t, 60*time.Second)
	if got := h.counting.count(); got != 1 {
		t.Fatalf("cold-start wire requests = %d, want 1", got)
	}

	h.blockUntil(1)
	h.clk.Advance(30 * time.Second)
	if got := h.counting.count(); got != 1 {
		t.Errorf("wire requests at 30s under pressure = %d, want 1 (the slow tier is demoted to 60s)", got)
	}

	h.blockUntil(1)
	h.clk.Advance(30 * time.Second) // reach the demoted 60s interval
	h.waitPolls(t, 1)
	if got := h.counting.count(); got != 2 {
		t.Errorf("wire requests at 60s = %d, want 2 (the demoted slow interval elapsed)", got)
	}
}
