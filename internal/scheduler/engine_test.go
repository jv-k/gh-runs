package scheduler

import (
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestSlowTierIsDeterministicAndConditional is AC1, AC2 and AC6. Two slow-tier
// repositories are polled once each on the cold start, then exactly once each per 30s
// of virtual time, every steady-state poll carrying If-None-Match, and no repository
// outside the poll set is ever touched. The whole test advances virtual time and
// completes without sleeping through any interval.
func TestSlowTierIsDeterministicAndConditional(t *testing.T) {
	alpha, beta := gh("acme", "alpha"), gh("acme", "beta")
	h := newHarness(t, harnessConfig{
		base:    openCassette(t, "poll_slow"),
		pollSet: []domain.RepoID{alpha, beta},
	})
	h.start(t)

	// Cold start: each repository polled once, unconditionally, and its 200 delivered.
	h.waitPolls(t, 2)
	h.waitUpdates(t, 2)
	h.waitSettle(t, slowTarget)
	if got := h.counting.count(); got != 2 {
		t.Fatalf("cold-start wire requests = %d, want 2", got)
	}
	if got := h.counting.conditionalCount(); got != 0 {
		t.Errorf("cold-start conditional requests = %d, want 0 (no ETag held yet)", got)
	}

	// Short of the 30s interval, nothing is polled.
	h.blockUntil(1)
	h.clk.Advance(slowTarget - time.Second)
	if got := h.counting.count(); got != 2 {
		t.Errorf("wire requests at 29s = %d, want 2 (polled before the interval elapsed)", got)
	}

	// At the interval, each repository is polled exactly once more, conditionally.
	h.blockUntil(1)
	h.clk.Advance(time.Second)
	h.waitPolls(t, 2)
	h.waitSettle(t, slowTarget)
	if got := h.counting.count(); got != 4 {
		t.Errorf("wire requests after 30s = %d, want 4 (one poll per slow repo per interval)", got)
	}
	if got := h.counting.conditionalCount(); got != 2 {
		t.Errorf("steady-state conditional requests = %d, want 2 (AC2: every steady-state poll conditional)", got)
	}
	// The steady-state polls answered 304, so they did no re-render work: still two
	// updates, both from the cold start (AC16).
	if got := h.updateCount(); got != 2 {
		t.Errorf("updates = %d, want 2 (a 304 does no work)", got)
	}
	// AC6: nothing outside the poll set was ever polled.
	if u, ok := h.counting.sawOnly("repos/acme/alpha/", "repos/acme/beta/"); !ok {
		t.Errorf("polled a repository outside the poll set: %s", u)
	}
}

// TestViewportChangeWakesTheLoop is AC17 at the loop level. A repository scrolled
// into view is promoted from the slow tier to the medium tier promptly, because
// SetViewport wakes the sleeping loop rather than leaving the promotion to wait out
// the up-to-30s slow interval the loop was asleep on. It is symmetric with the
// poll-driven tier change that already wakes the loop (R8).
func TestViewportChangeWakesTheLoop(t *testing.T) {
	a := gh("acme", "a")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}})
	h.start(t)

	// Cold start: A holds only a completed Run and is off screen, so it is slow-tier
	// and the loop goes to sleep on the 30s slow interval.
	h.waitPolls(t, 1)
	h.waitSettle(t, slowTarget)

	// Scroll A into view. Without a wake this promotion would be adopted only at the
	// next 30s tick; with it the loop re-evaluates at once and settles on the 5s
	// medium interval, though no virtual time has passed.
	h.s.SetViewport([]domain.RepoID{a})
	h.waitSettle(t, mediumTarget)
}

// TestConditionalPollIsFreeAndSilent is AC16 and R22: a 200 with an ETag followed by
// a 304 for the same resource. The cold-start 200 reveals a live Run and is delivered
// to the Feed; the steady-state 304 costs zero primary allowance, is counted as a
// free revalidation, and triggers no re-render.
func TestConditionalPollIsFreeAndSilent(t *testing.T) {
	live := gh("acme", "live")
	h := newHarness(t, harnessConfig{
		base:    openCassette(t, "poll_change"),
		pollSet: []domain.RepoID{live},
	})
	h.start(t)

	// Cold start: the 200 with the in_progress Run is delivered, and the repository
	// becomes fast-tier, so the loop converges on the ~3s cadence.
	h.waitPolls(t, 1)
	h.waitUpdates(t, 1)
	h.waitSettle(t, fastTarget)
	if got := h.gov.PrimaryUsed(); got != 1 {
		t.Fatalf("primary used after the 200 = %d, want 1", got)
	}
	if got := h.gov.Revalidations(); got != 0 {
		t.Errorf("revalidations after the 200 = %d, want 0", got)
	}
	h.mu.Lock()
	first := h.updates[0]
	h.mu.Unlock()
	if first.Repo != live || len(first.Runs) != 1 || first.Runs[0].Status != domain.StatusInProgress {
		t.Errorf("delivered update = %+v, want live's single in_progress Run", first)
	}
	if first.Runs[0].Repo != live {
		t.Errorf("delivered Run's Repo = %v, want it stamped %v", first.Runs[0].Repo, live)
	}

	// Steady state: the conditional poll answers 304.
	h.blockUntil(1)
	h.clk.Advance(fastTarget)
	h.waitPolls(t, 1)

	if got := h.counting.count(); got != 2 {
		t.Fatalf("wire requests = %d, want 2", got)
	}
	if got := h.counting.conditionalCount(); got != 1 {
		t.Errorf("conditional requests = %d, want 1 (the steady-state poll carried If-None-Match)", got)
	}
	if got := h.gov.Revalidations(); got != 1 {
		t.Errorf("revalidations = %d, want 1 (the free 304)", got)
	}
	if got := h.gov.PrimaryUsed(); got != 1 {
		t.Errorf("primary used = %d, want 1 (the 304 cost zero)", got)
	}
	if got := h.updateCount(); got != 1 {
		t.Errorf("updates = %d, want 1 (the 304 triggered no re-render)", got)
	}
}
