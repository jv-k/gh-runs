package feed

import (
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// TestFilteredUpdatePopulatesTheLiveCapLabel pins R24 end to end on the Feed: a filtered
// poll carrying a claimed total renders the honest cap label from live data, not only the
// golden-injected state TestGoldenCapLabel fixes. reachable is what the Feed holds (the
// Runs the Update carried), claimed is the response's total_count, and the repository is
// capped because the claimed count exceeds what it reached.
func TestFilteredUpdatePopulatesTheLiveCapLabel(t *testing.T) {
	m := newFeed(100, 10)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m, _ = m.Update(scheduler.Update{
		Repo: id,
		Runs: []domain.Run{
			mkRun(1, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
			mkRun(2, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Hour)),
		},
		Filtered:     true,
		ClaimedTotal: 18258,
	})
	if !strings.Contains(m.View(), "2 of ~18,258") {
		t.Fatalf("filtered Update did not render the live cap label (R24):\n%s", m.View())
	}
}

// TestUnfilteredUpdateClearsTheCapLabel pins R24's restore: when the filter is lifted the
// next poll is unfiltered, and its Update clears the repository's cap total so no stale
// label lingers. An unfiltered view has no cap and never carries the label.
func TestUnfilteredUpdateClearsTheCapLabel(t *testing.T) {
	m := newFeed(100, 10)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	runs := []domain.Run{
		mkRun(1, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
		mkRun(2, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Hour)),
	}
	m, _ = m.Update(scheduler.Update{Repo: id, Runs: runs, Filtered: true, ClaimedTotal: 18258})
	if _, ok := m.capLabelLine(); !ok {
		t.Fatal("precondition: a filtered Update should show a cap label")
	}
	m, _ = m.Update(scheduler.Update{Repo: id, Runs: runs, Filtered: false})
	if _, ok := m.capLabelLine(); ok {
		t.Fatalf("an unfiltered Update did not clear the cap label (R24: an unfiltered view has no cap)")
	}
}
