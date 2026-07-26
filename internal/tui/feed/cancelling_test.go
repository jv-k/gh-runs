package feed

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// feedWithRunningRun builds a Feed over the launch spy holding one in-progress Run, the
// only kind a cancel has anything to act on.
func feedWithRunningRun(t *testing.T) (Model, *launchSpy) {
	t.Helper()
	spy := newLaunchSpy()
	spy.startedOp, spy.startedSet = ops.OpCancel, 1
	m := New(Options{Profile: keys.Standard, Ops: spy})
	m = m.Update2(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("o", "r"), mkRun(7, "o", "r", "CI", domain.StatusInProgress, "", t0))
	return m, spy
}

// requestCancel drives the cancel key and its y/N confirmation, then runs the launch
// command so the operation is genuinely under way.
func requestCancel(t *testing.T, m Model, k string) Model {
	t.Helper()
	m = m.Update2(press("down"))
	m = m.Update2(press(k))
	m, cmd := m.Update(press("y"))
	if cmd == nil {
		t.Fatalf("the %q confirmation issued no launch command", k)
	}
	cmd()
	return m
}

// TestCancelRequestedIndicatorShowsAndNeverPreemptsTheConclusion is AC5. A cancel is
// asynchronous: a 202 says the request was accepted, not that the Run was cancelled (R4).
// So the row states that cancellation was requested, and it does not put `cancelled` in
// the Conclusion cell, which stays empty until a poll observes Status reaching completed.
func TestCancelRequestedIndicatorShowsAndNeverPreemptsTheConclusion(t *testing.T) {
	m, _ := feedWithRunningRun(t)
	before := m.View()
	if strings.Contains(before, cancelRequestedText) {
		t.Fatalf("the indicator was on screen before any cancel was requested:\n%s", before)
	}

	m = requestCancel(t, m, "c")

	got := m.View()
	if !strings.Contains(got, cancelRequestedText) {
		t.Errorf("the row shows no cancellation-requested indicator after a cancel was requested (R4, AC5):\n%s", got)
	}
	if strings.Contains(got, string(domain.ConclusionCancelled)) {
		t.Errorf("the row displays a cancelled Conclusion before a poll observed the transition (R4, AC5):\n%s", got)
	}
}

// TestForceCancelAlsoMarksTheRow pins that the escalation reports itself the same way.
// Force-cancel is the same asynchronous request against a different endpoint (R6), so a
// row that said nothing after one would be the same silence AC5 forbids after the other.
func TestForceCancelAlsoMarksTheRow(t *testing.T) {
	m, _ := feedWithRunningRun(t)
	m = requestCancel(t, m, "C")
	if got := m.View(); !strings.Contains(got, cancelRequestedText) {
		t.Errorf("a force-cancel left the row unmarked (R6, AC5):\n%s", got)
	}
}

// TestCancelRequestedIndicatorClearsOnTheObservedTransition is AC5's other half and R4's
// point: only a poll observing the Run's actual transition may show the Conclusion. Once
// it does, the indicator is gone and the Conclusion the API reported is what the row
// carries, with `cancelled` in the Conclusion field and never in the Status field (R7, AC4).
func TestCancelRequestedIndicatorClearsOnTheObservedTransition(t *testing.T) {
	m, _ := feedWithRunningRun(t)
	m = requestCancel(t, m, "c")

	// The next poll observes the transition. The row repaints in place even with the cursor
	// in the list, because deferral holds back insertions, evictions and reorders and never
	// a row's own fields (R10), which is what lets the marker and the Conclusion change over
	// together on one poll.
	m = feedRuns(m, repoID("o", "r"), mkRun(7, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionCancelled, t0))

	got := m.View()
	if strings.Contains(got, cancelRequestedText) {
		t.Errorf("the indicator outlived the observed transition (R4, AC5):\n%s", got)
	}
	if !strings.Contains(got, string(domain.ConclusionCancelled)) {
		t.Errorf("the completed Run does not show its cancelled Conclusion (AC4):\n%s", got)
	}
	if _, still := m.cancelRequested[7]; still {
		t.Errorf("the requested-cancellation record outlived the transition that settled it")
	}
}

// TestRerunLeavesTheCancelIndicatorAlone pins that the indicator names one thing. A
// re-run is not a cancellation, and a row marked by one would report a request that was
// never made.
func TestRerunLeavesTheCancelIndicatorAlone(t *testing.T) {
	spy := newLaunchSpy()
	spy.startedOp, spy.startedSet = ops.OpRerun, 1
	m := New(Options{Profile: keys.Standard, Ops: spy})
	m = m.Update2(tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})
	m = feedRuns(m, repoID("o", "r"), mkRun(7, "o", "r", "CI", domain.StatusQueued, "", t0.Add(-time.Minute)))

	m = m.Update2(press("down"))
	m2, cmd := m.Update(press("R"))
	if cmd == nil {
		t.Fatalf("the re-run key issued no launch command")
	}
	cmd()
	if got := m2.View(); strings.Contains(got, cancelRequestedText) {
		t.Errorf("a re-run marked the row as having a cancellation requested:\n%s", got)
	}
}
