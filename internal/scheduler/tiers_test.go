package scheduler

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// gh builds a github.com-qualified RepoID for the tests.
func gh(owner, name string) domain.RepoID {
	return domain.RepoID{Host: "github.com", Owner: owner, Name: name}
}

// run is a Run reduced to the one field tier selection reads: its Status (R6).
func run(status domain.Status) domain.Run {
	return domain.Run{Status: status}
}

// TestTierByStatus is AC3 and R6. A repository whose known Runs include one at
// Status in_progress is fast-tier; when that Run reaches completed the repository
// falls back; and a Conclusion appearing never by itself changes the tier, because
// tier selection reads Status and never Conclusion.
func TestTierByStatus(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "live")

	s.setLastRuns(id, []domain.Run{run(domain.StatusInProgress)})
	if got := s.tierOf(id); got != tierFast {
		t.Fatalf("in_progress run: tier = %v, want fast", got)
	}

	// The Run reaches completed. A Conclusion is now populated, but the fall-back
	// is driven by Status alone (R6): the repository leaves the fast tier.
	done := run(domain.StatusCompleted)
	done.Conclusion = domain.ConclusionFailure
	s.setLastRuns(id, []domain.Run{done})
	if got := s.tierOf(id); got == tierFast {
		t.Errorf("completed run: tier = %v, want not fast (a Conclusion must not hold the fast tier)", got)
	}
}

// TestQueuedIsFast is AC3's queued half. A queued Run is moving, so it is fast-tier
// exactly as in_progress is.
func TestQueuedIsFast(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "queued")
	s.setLastRuns(id, []domain.Run{run(domain.StatusQueued)})
	if got := s.tierOf(id); got != tierFast {
		t.Errorf("queued run: tier = %v, want fast", got)
	}
}

// TestParkedRunRatesNoTier is AC18 and ADR-0021's narrowed fast rule. A repository
// whose only non-completed Run sits at waiting is parked: its next event is a human
// acting, so it rates the fast tier for nothing. It polls at the medium tier while
// visible and the slow tier otherwise. When that Run reaches queued it becomes
// fast-tier at the next decision.
func TestParkedRunRatesNoTier(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "parked")

	for _, parked := range []domain.Status{domain.StatusWaiting, domain.StatusRequested, domain.StatusPending} {
		s.setLastRuns(id, []domain.Run{run(parked)})

		s.SetViewport(nil)
		if got := s.tierOf(id); got != tierSlow {
			t.Errorf("%s run, off screen: tier = %v, want slow", parked, got)
		}
		s.SetViewport([]domain.RepoID{id})
		if got := s.tierOf(id); got != tierMedium {
			t.Errorf("%s run, on screen: tier = %v, want medium", parked, got)
		}
	}

	// The approval lands: the Run flips to queued and the repository is fast-tier.
	s.setLastRuns(id, []domain.Run{run(domain.StatusQueued)})
	if got := s.tierOf(id); got != tierFast {
		t.Errorf("queued run: tier = %v, want fast", got)
	}
}

// TestFastestTierWins is AC4 and R7. A repository that is both on screen and holds
// a queued Run is fast, never medium: being visible must never slow a repository
// with a live Run.
func TestFastestTierWins(t *testing.T) {
	s := New(Options{})
	id := gh("acme", "both")
	s.setLastRuns(id, []domain.Run{run(domain.StatusQueued)})
	s.SetViewport([]domain.RepoID{id})
	if got := s.tierOf(id); got != tierFast {
		t.Errorf("on screen and queued: tier = %v, want fast (fastest wins)", got)
	}
}

// TestViewportIsMedium is AC17 and R5's medium rule. With A on screen and B scrolled
// out, and neither holding a live Run, A is medium and B is slow. Scrolling B into
// view moves it to medium at the next decision, with no restart.
func TestViewportIsMedium(t *testing.T) {
	s := New(Options{})
	a, b := gh("acme", "a"), gh("acme", "b")
	s.setLastRuns(a, []domain.Run{run(domain.StatusCompleted)})
	s.setLastRuns(b, []domain.Run{run(domain.StatusCompleted)})

	s.SetViewport([]domain.RepoID{a})
	if got := s.tierOf(a); got != tierMedium {
		t.Errorf("A on screen: tier = %v, want medium", got)
	}
	if got := s.tierOf(b); got != tierSlow {
		t.Errorf("B off screen: tier = %v, want slow", got)
	}

	// Scroll B into view.
	s.SetViewport([]domain.RepoID{a, b})
	if got := s.tierOf(b); got != tierMedium {
		t.Errorf("B scrolled into view: tier = %v, want medium", got)
	}
}

// TestUnknownRepoIsSlow is R3's entry rule. A repository the scheduler has never
// polled, so holds no Runs for, and is not on screen, is slow-tier: it enters the
// rotation at the slow tier.
func TestUnknownRepoIsSlow(t *testing.T) {
	s := New(Options{})
	if got := s.tierOf(gh("acme", "fresh")); got != tierSlow {
		t.Errorf("never-polled repo: tier = %v, want slow", got)
	}
}

// TestCapabilityIsNotATierInput is AC5 and R4. Tier selection reads Status and
// visibility alone and never capability: the scheduler's inputs are the poll set of
// RepoIDs, each repository's Runs, and the viewport, none of which carries a
// permission. So two repositories at the same Status and visibility poll at the same
// tier whatever the token may do to them, because the capability that would
// distinguish them is not among the scheduler's inputs at all. Watching CI on a
// repository you cannot write to is a primary use case.
func TestCapabilityIsNotATierInput(t *testing.T) {
	s := New(Options{})
	writable, readOnly := gh("acme", "writable"), gh("acme", "read-only")

	// Same Status (a live Run) and same visibility (both on screen) is all the
	// scheduler can see: there is no push flag anywhere in its state.
	s.setLastRuns(writable, []domain.Run{run(domain.StatusInProgress)})
	s.setLastRuns(readOnly, []domain.Run{run(domain.StatusInProgress)})
	s.SetViewport([]domain.RepoID{writable, readOnly})

	if s.tierOf(writable) != s.tierOf(readOnly) {
		t.Errorf("tiers differ (%v vs %v) for repositories identical in every input the scheduler has", s.tierOf(writable), s.tierOf(readOnly))
	}
	if got := s.tierOf(readOnly); got != tierFast {
		t.Errorf("read-only repo with a live Run: tier = %v, want fast (capability never slows a tier)", got)
	}
}
