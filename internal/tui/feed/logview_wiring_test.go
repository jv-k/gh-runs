package feed

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// feedWithLog builds a Feed wired to the detail pane, the log seams, a real planner and a
// discovered writable repository with one Run, so a test can open the detail, descend into
// its Job list, and exercise the recursive-focus routing.
func feedWithLog(t *testing.T) Model {
	t.Helper()
	m := New(Options{
		Profile:     keys.Standard,
		DetailFetch: func(domain.RepoID, int64) ([]domain.Job, error) { return nil, nil },
		Clock:       clockwork.NewFakeClockAt(t0),
		LogFetch:    func(domain.RepoID, int64) ([]byte, error) { return nil, nil },
		Ops:         ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50}),
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("acme", "api"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "Build", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	return m
}

// TestFeedDescendsOnSecondOpenDetail pins the recursive-focus entry: the first open-detail
// press opens the pane Feed-focused, so the Feed still drives its Run cursor (the stage-8
// contract TestDetailPaneFollowsCursor fixes), and a second press descends into the pane, so
// motion and actions become the detail's (log-viewer R1, ADR-0011's recursive focus).
func TestFeedDescendsOnSecondOpenDetail(t *testing.T) {
	m := feedWithLog(t)
	m = m.Update2(press("enter")) // open the detail, Feed-focused
	if !m.detailOpen {
		t.Fatal("the first open-detail press did not open the pane")
	}
	if m.detail.CapturesKeys() {
		t.Fatal("the detail captures keys after one press; the Feed must keep its Run cursor (stage 8)")
	}
	m = m.Update2(press("enter")) // descend into the pane's Job list
	if !m.detail.CapturesKeys() {
		t.Fatal("the second open-detail press did not descend into the detail (recursive focus)")
	}
}

// TestRecursiveFocusSwallowsListActions pins the property the recursive focus exists for:
// while focus is in the detail (or the log view beneath it), a key the pane owns or ignores
// does not also fire as a Feed list action. The delete key, which opens a Purge over the
// Feed's selection when the Feed has focus, opens nothing while the operator is inside the
// detail, so the log view's own keys never collide with the list's (ADR-0011).
func TestRecursiveFocusSwallowsListActions(t *testing.T) {
	m := feedWithLog(t)
	m = m.Update2(press("enter")) // open
	m = m.Update2(press("enter")) // descend
	if !m.detail.CapturesKeys() {
		t.Fatal("precondition: the detail should capture keys after descending")
	}
	m = m.Update2(press("d")) // the Feed's Purge key, now the detail's to swallow
	if m.confirmOpen {
		t.Error("the delete key opened a Purge while focus was in the detail; recursive focus must swallow it (ADR-0011)")
	}
	if m.CapturesInput() && !m.detail.CapturesInput() {
		t.Error("the Feed reports input capture without the detail doing so; the propagation is wrong")
	}
}
