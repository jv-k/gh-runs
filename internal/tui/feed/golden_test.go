package feed

import (
	"testing"
	"time"

	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// The goldens render the Feed's frame from held state alone, at R4a's 100 columns, with
// no terminal and no network (R36, AC19). lipgloss v2 renders truecolour regardless of
// the environment, so these bytes are stable on any machine (ADR-0013). Run with
// -update to regenerate: go test ./internal/tui/feed/ -run Golden -update.

// discovered feeds the capability gate a repository's permissions (R17, R18, R21).
func discovered(m Model, repos ...domain.Repo) Model {
	m, _ = m.Update(ReposDiscovered(repos))
	return m
}

func repo(owner, name string, push, archived bool) domain.Repo {
	return domain.Repo{
		ID:          repoID(owner, name),
		Permissions: domain.Permissions{Push: push},
		Archived:    archived,
	}
}

// TestGoldenRowRendering fixes R36's first case: Status and Conclusion in their own
// cells, an empty Conclusion below an in_progress row, and a Conclusion carried by a
// completed row (R4, R5, R6).
func TestGoldenRowRendering(t *testing.T) {
	m := newFeed(100, 4)
	id := repoID("home-assistant", "core")
	m = discovered(m, repo("home-assistant", "core", true, false))
	m = feedRuns(m, id,
		mkRun(29516338954, "home-assistant", "core", "CI", domain.StatusInProgress, "", t0),
		mkRun(29516338001, "home-assistant", "core", "Release", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	goldie.New(t).Assert(t, "row_rendering", []byte(m.View()))
}

// TestGoldenCapLabel fixes R36's second case: R24's honest cap label, "1,000 of
// ~18,258", with no bare 18,258 anywhere (AC10).
func TestGoldenCapLabel(t *testing.T) {
	m := newFeed(100, 5)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id,
		mkRun(1, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Hour)),
	)
	// One filtered repository, reachable 1,000 of a claimed 18,258 (R24, held state).
	m.totals[id.String()] = capTotal{reachable: 1000, claimed: 18258}
	goldie.New(t).Assert(t, "cap_label", []byte(m.View()))
}

// TestGoldenActionStates fixes R36's third case: the four action states the gate
// distinguishes, offered, read-only, not-yet-known and permanently read-only, each
// visibly different (R17, R18, R21).
func TestGoldenActionStates(t *testing.T) {
	m := newFeed(100, 6)
	offered := repoID("acme", "offered")
	readonly := repoID("acme", "readonly")
	unknown := repoID("acme", "unknown")
	archived := repoID("acme", "archived")
	// Enumeration has reached three of the four; the fourth stays not-yet-known (R18).
	m = discovered(m,
		repo("acme", "offered", true, false),
		repo("acme", "readonly", false, false),
		repo("acme", "archived", true, true),
	)
	m = feedRuns(m, offered, mkRun(1, "acme", "offered", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	m = feedRuns(m, readonly, mkRun(2, "acme", "readonly", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)))
	m = feedRuns(m, unknown, mkRun(3, "acme", "unknown", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-2*time.Hour)))
	m = feedRuns(m, archived, mkRun(4, "acme", "archived", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-3*time.Hour)))
	goldie.New(t).Assert(t, "action_states", []byte(m.View()))
}

// TestGoldenDeferredInsertion fixes R36's fourth case: the deferred-insertion affordance
// reads exactly "3 new runs" (R11, AC3).
func TestGoldenDeferredInsertion(t *testing.T) {
	m := newFeed(100, 5)
	id := repoID("acme", "api")
	m = discovered(m, repo("acme", "api", true, false))
	m = feedRuns(m, id,
		mkRun(100, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(101, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	m = m.Update2(press("down")) // engage the list so an arrival defers (R10)
	m = feedRuns(m, id,
		mkRun(200, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(3*time.Hour)),
		mkRun(201, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(2*time.Hour)),
		mkRun(202, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(time.Hour)),
		mkRun(100, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(101, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	goldie.New(t).Assert(t, "deferred_insertion", []byte(m.View()))
}

// TestGoldenPausedBanner fixes R30's exhaustion banner: paused, when it resumes
// ("resumes 14:32"), and what it is showing and as of when (local-store R7).
func TestGoldenPausedBanner(t *testing.T) {
	m := newFeed(100, 5)
	id := repoID("acme", "api")
	m = discovered(m, repo("acme", "api", true, false))
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusInProgress, "", t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	m, _ = m.Update(governor.Readout{
		Exhausted: true,
		Reset:     time.Date(2026, 7, 19, 14, 32, 0, 0, time.UTC),
	})
	m, _ = m.Update(RevalidatedAt(time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC)))
	goldie.New(t).Assert(t, "paused_banner", []byte(m.View()))
}

// TestGoldenUnknownConclusion fixes AC21: an unrecognised 30-character Conclusion is
// truncated with a marker, never renamed to "unknown" nor blanked, and every other row's
// layout is unchanged (R6, R6a).
func TestGoldenUnknownConclusion(t *testing.T) {
	m := newFeed(100, 4)
	id := repoID("acme", "api")
	m = discovered(m, repo("acme", "api", true, false))
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.Conclusion("a_brand_new_conclusion_the_api_invented"), t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	goldie.New(t).Assert(t, "unknown_conclusion", []byte(m.View()))
}

// TestGoldenApprovalBadge fixes the approvals badge (approvals R8, R10): the count of Runs
// awaiting a decision atop the list, worded neutrally and naming the saved-filter key, over one
// fork-PR Run (completed, action_required) and one pending deployment (waiting). A healthy
// completed Run is present too and is not counted.
func TestGoldenApprovalBadge(t *testing.T) {
	m := newFeed(100, 7)
	id := repoID("o", "r")
	m = discovered(m, repo("o", "r", true, false))
	m = feedRuns(m, id,
		mkRun(29516338954, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionActionRequired, t0),
		mkRun(29516338001, "o", "r", "Deploy", domain.StatusWaiting, "", t0.Add(-time.Hour)),
		mkRun(29516337000, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-2*time.Hour)),
	)
	goldie.New(t).Assert(t, "approval_badge", []byte(m.View()))
}

// TestGoldenNarrowTerminal fixes AC20: below 100 columns the Feed paints no rows and
// states the width it needs.
func TestGoldenNarrowTerminal(t *testing.T) {
	m := newFeed(99, 20)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "CI", domain.StatusInProgress, "", t0))
	goldie.New(t).Assert(t, "narrow_terminal", []byte(m.View()))
}
