package feed

import (
	"strings"
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

// feedWithOpenLog builds a Feed, opens the detail over its one Run, drives the detail's
// debounced Job fetch to load a Job, descends into job-focus, and opens that Job's log, so a
// test can press a key while the log view itself holds focus, the case distinct from job-focus.
// It drives the real selection-settle debounce the runtime would, so it forwards each Cmd's
// message the way the program loop does.
func feedWithOpenLog(t *testing.T) Model {
	t.Helper()
	jobs := []domain.Job{{ID: 111, Name: "build", Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess}}
	logBytes := []byte("2026-07-15T03:11:52.0835958Z hello from the job log\n")
	m := New(Options{
		Profile:     keys.Standard,
		DetailFetch: func(domain.RepoID, int64) ([]domain.Job, error) { return jobs, nil },
		Clock:       clockwork.NewFakeClockAt(t0),
		LogFetch:    func(domain.RepoID, int64) ([]byte, error) { return logBytes, nil },
		Ops:         ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50}),
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("acme", "api"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "Build", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	var cmd tea.Cmd
	m, cmd = m.Update(press("enter")) // open the detail Feed-focused; arms the selection-settle debounce
	m, cmd = m.Update(cmd())          // the settle fires and issues the Job fetch
	m, _ = m.Update(cmd())            // the Job fetch lands and the Jobs load
	m = m.Update2(press("enter"))     // descend into job-focus
	m, cmd = m.Update(press("enter")) // open the selected Job's log; arms the log fetch
	m, _ = m.Update(cmd())            // the log fetch lands and the log renders

	if !m.detail.CapturesKeys() {
		t.Fatal("precondition: the detail should hold key focus with the log open")
	}
	if !strings.Contains(m.detail.View(), "hello from the job log") {
		t.Fatalf("precondition: the log view is not open and rendering its content:\n%s", m.detail.View())
	}
	return m
}

// TestRecursiveFocusSwallowsDeleteWhileLogOpen closes the log-open half of the recursive-focus
// contract the security review found unasserted: pressing the Run-delete key d while the log
// view itself holds focus (not merely job-focus) opens no Purge and no confirmation. The Feed
// hands the key to the detail because log.IsOpen keeps CapturesKeys true, the detail hands it to
// the log, and the log binds no d (its delete is D), so it is inert and nothing opens (ADR-0011,
// log-viewer AC6).
func TestRecursiveFocusSwallowsDeleteWhileLogOpen(t *testing.T) {
	m := feedWithOpenLog(t)
	m = m.Update2(press("d")) // the Feed's Purge key, now the log view's to swallow
	if m.confirmOpen {
		t.Error("d opened a Feed Purge while the log view was open; recursive focus must swallow it (ADR-0011)")
	}
	if m.CapturesInput() {
		t.Error("d opened a modal inside the log view; the log binds no d, so nothing should open (log-viewer AC6)")
	}
	if !strings.Contains(m.detail.View(), "hello from the job log") {
		t.Errorf("d disturbed the open log view; it must be inert there:\n%s", m.detail.View())
	}
}
