package feed

import (
	"errors"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// feedFailure sends one repository's failed poll, the fourth member of ADR-0015's
// catalog.
func feedFailure(m Model, id domain.RepoID, err error) Model {
	m, _ = m.Update(scheduler.RepoPollFailed{Repo: id, Err: err})
	return m
}

// TestFailedPollIsVisible pins the reason RepoPollFailed exists. Until the Feed says
// so, a repository whose polls are failing looks exactly like one that has not
// answered yet, and its rows sit on screen going quietly stale.
func TestFailedPollIsVisible(t *testing.T) {
	m := newFeed(100, 24)
	id := repoID("acme", "api")
	m = feedRuns(m, id, mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	if got := m.View(); strings.Contains(got, "not responding") {
		t.Fatalf("a healthy Feed already claims a repository is failing:\n%s", got)
	}

	m = feedFailure(m, id, errors.New("HTTP 502: Bad Gateway"))

	got := m.View()
	if !strings.Contains(got, "not responding") {
		t.Errorf("the Feed does not surface a failed poll:\n%s", got)
	}
	if !strings.Contains(got, "acme/api") {
		t.Errorf("the failed-repo indicator does not name the repository:\n%s", got)
	}
}

// TestFailedPollClearsOnNextSuccess pins the heal. The engine retries on the
// repository's own cadence and cancels nothing, so the indicator has to be cleared by
// the next Update rather than by anything the Feed does. A marker that outlived the
// failure would be worse than none.
func TestFailedPollClearsOnNextSuccess(t *testing.T) {
	m := newFeed(100, 24)
	id := repoID("acme", "api")
	m = feedFailure(m, id, errors.New("HTTP 502: Bad Gateway"))
	if !strings.Contains(m.View(), "not responding") {
		t.Fatal("precondition: the indicator did not appear")
	}

	m = feedRuns(m, id, mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	if got := m.View(); strings.Contains(got, "not responding") {
		t.Errorf("the indicator survived a successful poll of the same repository:\n%s", got)
	}
}

// TestFailedPollsAreCountedNotListed pins the bound. The poll set is around 26
// repositories, so a wide outage must not paint 26 names across the chrome and push
// the list off screen. The count is the honest figure and a couple of names carry the
// detail, which is the same shape R24's cap label uses.
func TestFailedPollsAreCountedNotListed(t *testing.T) {
	m := newFeed(100, 24)
	names := []string{"api", "web", "infra", "docs", "cli"}
	for _, n := range names {
		m = feedFailure(m, repoID("acme", n), errors.New("HTTP 502: Bad Gateway"))
	}

	got := m.View()
	if !strings.Contains(got, "5 repositories not responding") {
		t.Errorf("the indicator does not count every failed repository:\n%s", got)
	}
	named := 0
	for _, n := range names {
		if strings.Contains(got, "acme/"+n) {
			named++
		}
	}
	if named == 0 {
		t.Errorf("the indicator names no repository at all:\n%s", got)
	}
	if named == len(names) {
		t.Errorf("the indicator listed all %d names instead of bounding them:\n%s", len(names), got)
	}
}

// TestFailedPollDoesNotDiscardHeldRuns pins that the report is additive. The Runs the
// repository last returned stay on screen, because they are still the newest thing
// known about it; the indicator is what says how old they now are.
func TestFailedPollDoesNotDiscardHeldRuns(t *testing.T) {
	m := newFeed(100, 24)
	id := repoID("acme", "api")
	m = feedRuns(m, id, mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	before := m.displayedOrder()

	m = feedFailure(m, id, errors.New("HTTP 502: Bad Gateway"))

	after := m.displayedOrder()
	if len(after) != len(before) {
		t.Errorf("a failed poll changed the displayed Runs from %v to %v", before, after)
	}
}
