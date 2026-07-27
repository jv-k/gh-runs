package feed

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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

	if got := m.View(); strings.Contains(got, "failed to update") {
		t.Fatalf("a healthy Feed already claims a repository is failing:\n%s", got)
	}

	m = feedFailure(m, id, errors.New("HTTP 502: Bad Gateway"))

	got := m.View()
	if !strings.Contains(got, "failed to update") {
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
	if !strings.Contains(m.View(), "failed to update") {
		t.Fatal("precondition: the indicator did not appear")
	}

	m = feedRuns(m, id, mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	if got := m.View(); strings.Contains(got, "failed to update") {
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
	if !strings.Contains(got, "5 repositories failed to update") {
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

// TestSingleFailureNamesItsReason pins that the carried error reaches the operator.
// A repository that is gone and one whose API had a bad minute want different things
// done about them, and the status is the only thing that tells them apart. With one
// failure there is room to say which.
func TestSingleFailureNamesItsReason(t *testing.T) {
	m := newFeed(100, 24)
	m = feedFailure(m, repoID("acme", "api"), errors.New("poll returned HTTP 404 Not Found"))

	if got := m.View(); !strings.Contains(got, "404") {
		t.Errorf("the indicator does not carry the reason it was given:\n%s", got)
	}
}

// TestFailureReasonIsSanitised pins that API text cannot break the frame. The reason
// is the API's words, so it is untrusted content in a fixed-width line.
func TestFailureReasonIsSanitised(t *testing.T) {
	m := newFeed(100, 24)
	m = feedFailure(m, repoID("acme", "api"), errors.New("bad\r\nInjected: header"))

	got := m.View()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "Injected:") {
			t.Fatalf("a control byte in the reason started a new line:\n%s", got)
		}
	}
}

// TestFailureClearsWhenTheRepositoryLeavesDiscovery pins the other way out. A
// repository deleted or made private upstream fails once, then leaves the poll set, so
// no Update can ever arrive to clear it. Without this the indicator would assert a
// live condition nothing is testing anymore, undismissable for the rest of the session.
//
// The first membership set is what makes this the real sequence rather than a vacuous one:
// a repository can only leave the set if it was in it, and a prune read against the
// accumulated union of every snapshot would report it as still known and drop nothing.
//
// It reads RepoMembership rather than ReposDiscovered, which is R37: the capability
// snapshot legitimately omits a repository awaiting enumeration, so absence there is not a
// departure. discovery.Membership is what shrinks, once repo-discovery R23 retires.
func TestFailureClearsWhenTheRepositoryLeavesDiscovery(t *testing.T) {
	m := newFeed(100, 24)
	gone, stays := repoID("acme", "gone"), repoID("acme", "stays")
	m, _ = m.Update(RepoMembership{gone, stays})
	m = feedFailure(m, gone, errors.New("poll returned HTTP 404 Not Found"))
	m = feedFailure(m, stays, errors.New("poll returned HTTP 502 Bad Gateway"))

	// Discovery retires the first, so the next membership set omits it.
	m, _ = m.Update(RepoMembership{stays})

	got := m.View()
	if strings.Contains(got, "acme/gone") {
		t.Errorf("a repository that left discovery kept its indicator:\n%s", got)
	}
	if !strings.Contains(got, "acme/stays") {
		t.Errorf("a repository still discovered lost its indicator:\n%s", got)
	}
}

// TestEmptyDiscoveryClearsNothing is the guard on the prune above. At cold start
// discovery holds nothing, and an empty set is not evidence that a repository has
// gone.
func TestEmptyDiscoveryClearsNothing(t *testing.T) {
	m := newFeed(100, 24)
	m = feedFailure(m, repoID("acme", "api"), errors.New("poll returned HTTP 502 Bad Gateway"))

	m, _ = m.Update(RepoMembership{})

	if got := m.View(); !strings.Contains(got, "acme/api") {
		t.Errorf("an empty membership set cleared a live failure:\n%s", got)
	}
}

// TestWideOutageStaysOneLine pins the clamp. chromeLineCount counts this indicator as
// exactly one line, and its content is repository names and API text, neither of which
// the Feed chooses the length of. A wrap would overrun the frame by a row.
func TestWideOutageStaysOneLine(t *testing.T) {
	m := newFeed(100, 24)
	for _, n := range []string{"vscode-python-environments", "vscode-jupyter-powertoys", "api"} {
		m = feedFailure(m, repoID("microsoft", n), errors.New("poll returned HTTP 502 Bad Gateway"))
	}

	line, ok := m.failedLine()
	if !ok {
		t.Fatal("precondition: no indicator")
	}
	if w := lipgloss.Width(line); w > 100 {
		t.Errorf("the indicator rendered %d columns wide at a 100-column frame:\n%s", w, line)
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
