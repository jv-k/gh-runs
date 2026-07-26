package feed

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// launchedWith builds a Feed opened on a launch filter, the value settings R9 resolves and
// the composition root hands down.
func launchedWith(f filter.Filter) Model {
	m := New(Options{Profile: keys.Standard, Filter: f})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	return m
}

// TestLaunchFilterNarrowsTheFirstRunsThatArrive pins settings R9's Feed half: a Feed opened
// on a launch filter is already narrowed by it, so the first Runs to arrive are filtered as
// they land rather than after the operator types anything.
func TestLaunchFilterNarrowsTheFirstRunsThatArrive(t *testing.T) {
	m := launchedWith(filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure}})
	m = feedRuns(m, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
	)

	if got := m.displayedOrder(); len(got) != 1 || got[0] != 2 {
		t.Errorf("displayed Runs = %v, want only the failing one (settings R9)", got)
	}
}

// TestNoLaunchFilterShowsEveryRun pins the default the zero Filter carries (R3, AC1): with
// no launch filter set, nothing is narrowed and the Feed opens exactly as it did before the
// setting existed.
func TestNoLaunchFilterShowsEveryRun(t *testing.T) {
	m := launchedWith(filter.Filter{})
	m = feedRuns(m, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
	)

	if got := m.displayedOrder(); len(got) != 2 {
		t.Errorf("displayed Runs = %v, want both (no launch filter narrows nothing)", got)
	}
}

// TestLaunchFilterStatesItselfInTheFrameAndTheInput pins that a narrowing the operator did
// not type is visible and editable rather than silent. It is the same requirement an
// arriving ShowRuns meets (R22, R23): the held filter, the line the frame states, and the
// text in the / input are one state. A launch filter that narrowed invisibly would read as
// a broken tool on the first empty Feed.
func TestLaunchFilterStatesItselfInTheFrameAndTheInput(t *testing.T) {
	m := launchedWith(filter.Filter{Branch: "main"})

	if got := m.filterInput.Value(); got != "branch:main" {
		t.Errorf("filter input = %q, want the launch filter stated in it", got)
	}
	if view := m.View(); !strings.Contains(view, "branch:main") {
		t.Errorf("the frame does not state the launch filter:\n%s", view)
	}
}
