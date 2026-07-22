package rundetail

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// t0 is the fake clock's fixed instant, so a golden's elapsed durations are deterministic
// under held state alone (R19).
var t0 = time.Date(2026, 7, 15, 16, 39, 0, 0, time.UTC)

const (
	queued     = domain.StatusQueued
	inProgress = domain.StatusInProgress
	completed  = domain.StatusCompleted
	success    = domain.ConclusionSuccess
	failure    = domain.ConclusionFailure
)

func repoID(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// run builds a selected Run for the state-machine tests, where only its ID and Status
// matter. The golden tests build their Runs with explicit fields instead.
func run(id int64, st domain.Status) domain.Run {
	return domain.Run{ID: id, RunNumber: 100, Name: "CI", WorkflowName: "CI", Status: st, Repo: repoID("acme", "api")}
}

func runAttempt(id int64, st domain.Status, attempt int) domain.Run {
	r := run(id, st)
	r.RunAttempt = attempt
	return r
}

func mkJob(name string, st domain.Status, cc domain.Conclusion) domain.Job {
	return domain.Job{Name: name, Status: st, Conclusion: cc}
}

// fakeFetch is the injected Fetch under the pane's own tests: no transport, a call counter
// AC1 reads, and a per-Run canned result. It stands in for ClientFetch, which is exercised
// on its own by fetch_test.go (ADR-0015: the pane fetches over an injected function).
type fakeFetch struct {
	calls int
	byRun map[int64][]domain.Job
	err   error
}

func (f *fakeFetch) fn() Fetch {
	return func(_ domain.RepoID, id int64) ([]domain.Job, error) {
		f.calls++
		if f.err != nil {
			return nil, f.err
		}
		return f.byRun[id], nil
	}
}

func newPane(f Fetch) Model {
	return New(Options{Fetch: f, Clock: clockwork.NewFakeClockAt(t0)})
}

// TestDebounceCoalescesToOneFetch pins R10 and AC1: arrow-keying through the Feed arms a
// debounce per row but fetches nothing until a settle, and only the settle for the row the
// cursor rests on issues a request, so a 100-row walk costs exactly one Job request. The
// debounce is fabricated, not slept through, exactly as the constraint prescribes.
func TestDebounceCoalescesToOneFetch(t *testing.T) {
	f := &fakeFetch{}
	m := newPane(f.fn())
	m, _ = m.Open(run(1, completed))
	for i := int64(2); i <= 100; i++ {
		m, _ = m.SelectRun(run(i, completed)) // the cursor walks the Feed; each arms a debounce
	}
	if f.calls != 0 {
		t.Fatalf("the selection walk issued %d fetches before any settle, want 0 (R10, AC1)", f.calls)
	}
	// A settle for a row the cursor already left issues nothing (AC1).
	var c tea.Cmd
	m, c = m.Update(settleMsg{runID: 50})
	if c != nil {
		t.Fatalf("a settle for a row the cursor left returned a fetch (AC1)")
	}
	// The settle for the resting row issues exactly one fetch.
	_, c = m.Update(settleMsg{runID: 100})
	if c == nil {
		t.Fatalf("the settle for the resting row issued no fetch (R10)")
	}
	c() // execute the fetch; a fake Fetch is instant, so this never sleeps
	if f.calls != 1 {
		t.Fatalf("the debounced walk issued %d fetches, want exactly 1 (AC1)", f.calls)
	}
}

// TestStaleResponseDiscarded pins R11 and AC2: a Job response for the previously selected
// Run, arriving after the selection moved, is discarded rather than rendered under the new
// Run, and the new Run's own response loads.
func TestStaleResponseDiscarded(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{
		1: {mkJob("build-A", completed, success)},
		2: {mkJob("build-B", completed, success)},
	}}
	m := newPane(f.fn())
	m, _ = m.Open(run(1, inProgress))
	m, _ = m.SelectRun(run(2, inProgress)) // selection moves to Run 2 before Run 1's response

	m, _ = m.Update(jobsMsg{runID: 1, jobs: f.byRun[1]}) // Run 1's late response
	if m.state == stateLoaded {
		t.Fatalf("a stale response for Run 1 was rendered under Run 2 (R11, AC2)")
	}
	m, _ = m.Update(jobsMsg{runID: 2, jobs: f.byRun[2]}) // Run 2's own response
	if m.state != stateLoaded || len(m.jobs) != 1 || m.jobs[0].Name != "build-B" {
		t.Fatalf("Run 2's own Jobs did not load after the stale discard (AC2)")
	}
}

// TestFastTierWhileLiveStopsOnCompleted pins R13 and AC6: a live Run arms the ~3s fast
// tier, each tick refreshes it, and once the Run reaches completed the loop ends and no
// further Job request is issued.
func TestFastTierWhileLiveStopsOnCompleted(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{7: {mkJob("build", inProgress, "")}}}
	m := newPane(f.fn())
	m, _ = m.Open(run(7, inProgress))

	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 7})
	if cmd == nil {
		t.Fatal("the settle issued no fetch")
	}
	m, cmd = m.Update(cmd()) // apply the fetched Jobs; a live Run arms the fast tier
	if cmd == nil {
		t.Fatalf("a live Run did not arm the fast tier (R13, AC6)")
	}
	// The ~3s tick refreshes a live Run (fabricated, not slept).
	before := f.calls
	m, cmd = m.Update(refreshMsg{runID: 7})
	if cmd == nil {
		t.Fatalf("the fast-tier tick did not refresh a live Run (AC6)")
	}
	cmd()
	if f.calls != before+1 {
		t.Fatalf("the fast-tier refresh did not fetch (AC6)")
	}
	// The Run completes; the next tick stops the loop.
	m, _ = m.SelectRun(run(7, completed))
	_, cmd = m.Update(refreshMsg{runID: 7})
	if cmd != nil {
		t.Fatalf("the fast tier kept refreshing a completed Run (AC6)")
	}
}

// TestDeselectionStopsPolling pins R14 and AC7: once the selection moves off a Run, a
// fast-tier tick for the old Run stops rather than refreshing it.
func TestDeselectionStopsPolling(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{7: {mkJob("build", inProgress, "")}}}
	m := newPane(f.fn())
	m, _ = m.Open(run(7, inProgress))
	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 7})
	m, _ = m.Update(cmd()) // Run 7 loaded, fast tier armed

	m, _ = m.SelectRun(run(8, completed)) // selection moves to Run 8
	before := f.calls
	_, cmd = m.Update(refreshMsg{runID: 7}) // the old Run's tick fires
	if cmd != nil || f.calls != before {
		t.Fatalf("a fast-tier tick kept polling a deselected Run (R14, AC7)")
	}
}

// TestCloseStopsPolling pins R14: closing the pane stops its fast tier, so a tick in flight
// becomes a no-op.
func TestCloseStopsPolling(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{7: {mkJob("build", inProgress, "")}}}
	m := newPane(f.fn())
	m, _ = m.Open(run(7, inProgress))
	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 7})
	m, _ = m.Update(cmd())

	m = m.Close()
	_, cmd = m.Update(refreshMsg{runID: 7})
	if cmd != nil {
		t.Fatalf("a closed pane kept polling (R14)")
	}
}

// TestBudgetPauseAndResume pins R16 and AC12: under exhaustion the pane stops refreshing
// and states that it paused and when it resumes, and when the Budget recovers it resumes.
func TestBudgetPauseAndResume(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{7: {mkJob("build", inProgress, "")}}}
	m := newPane(f.fn())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100})
	m, _ = m.Open(run(7, inProgress))
	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 7})
	m, _ = m.Update(cmd()) // loaded, armed

	reset := t0.Add(30 * time.Minute)
	m, _ = m.Update(governor.Readout{Exhausted: true, Reset: reset})
	before := f.calls
	m, cmd = m.Update(refreshMsg{runID: 7})
	if cmd != nil || f.calls != before {
		t.Fatalf("the pane kept refreshing under Budget exhaustion (R16, AC12)")
	}
	view := m.View()
	if !strings.Contains(view, "paused") {
		t.Fatalf("the pane did not state it paused (AC12):\n%s", view)
	}
	if !strings.Contains(view, reset.Format("15:04")) {
		t.Fatalf("the pane did not state when it resumes (AC12):\n%s", view)
	}

	// Recovery: the Readout clears and the pane resumes its fetch.
	_, cmd = m.Update(governor.Readout{Exhausted: false})
	if cmd == nil {
		t.Fatalf("the pane did not resume when the Budget recovered (R16, AC12)")
	}
	cmd()
	if f.calls != before+1 {
		t.Fatalf("resume did not re-issue the fetch (AC12)")
	}
}

// TestResumeDoesNotDoubleFetchWhileFetchOutstanding pins the resume guard: when a fetch is
// still in flight, an exhaust-then-recover flip within that one round trip must not inject a
// second fetch, because the outstanding fetch already re-arms the fast tier on arrival. Two
// injected fetches would leave two concurrent ~3s loops (code review). The recover returns no
// Cmd while the fetch is outstanding, and the single in-flight response arms exactly one loop.
func TestResumeDoesNotDoubleFetchWhileFetchOutstanding(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{7: {mkJob("build", inProgress, "")}}}
	m := newPane(f.fn())
	m, _ = m.Open(run(7, inProgress))

	var fetchC tea.Cmd
	m, fetchC = m.Update(settleMsg{runID: 7}) // the settle issues a fetch; it is now in flight
	if fetchC == nil {
		t.Fatal("the settle issued no fetch")
	}
	// The Budget exhausts and recovers before the in-flight fetch returns.
	m, _ = m.Update(governor.Readout{Exhausted: true, Reset: t0.Add(30 * time.Minute)})
	var resumeC tea.Cmd
	m, resumeC = m.Update(governor.Readout{Exhausted: false})
	if resumeC != nil {
		t.Fatalf("resume injected a second fetch while one was outstanding, risking a double refresh loop (code review)")
	}
	// The one outstanding fetch returns and arms exactly one fast-tier loop.
	before := f.calls
	var armC tea.Cmd
	_, armC = m.Update(fetchC()) // execute the in-flight fetch and deliver its response
	if f.calls != before+1 {
		t.Fatalf("the outstanding fetch did not run exactly once: calls delta %d", f.calls-before)
	}
	if armC == nil {
		t.Fatalf("the outstanding fetch did not re-arm the fast tier on arrival (R13)")
	}
}

// TestSelectionChangeShowsPendingNotPreviousJobs pins R12: a selection change replaces the
// previous Run's Jobs with an explicit pending state, never leaves them on screen under the
// new identity.
func TestSelectionChangeShowsPendingNotPreviousJobs(t *testing.T) {
	f := &fakeFetch{byRun: map[int64][]domain.Job{1: {mkJob("build", completed, success)}}}
	m := newPane(f.fn())
	m, _ = m.Open(run(1, completed))
	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 1})
	m, _ = m.Update(cmd())
	if m.state != stateLoaded {
		t.Fatal("Run 1 did not load")
	}
	m, _ = m.SelectRun(run(2, completed))
	if m.state != statePending {
		t.Fatalf("a selection change did not clear to the pending state (R12)")
	}
	if len(m.jobs) != 0 {
		t.Fatalf("the previous Run's Jobs survived a selection change (R12)")
	}
	if !strings.Contains(m.View(), "Loading") {
		t.Fatalf("the pending state is not explicit (R12)")
	}
}

// TestEmptyAndErrorAreNoJobsAlike pins R12a and AC14: an empty Jobs list and an error
// response both resolve to the same "no Jobs yet" state, with no distinct error surface.
func TestEmptyAndErrorAreNoJobsAlike(t *testing.T) {
	empty := &fakeFetch{byRun: map[int64][]domain.Job{}}
	m := newPane(empty.fn())
	m, _ = m.Open(run(9, queued))
	if m.state != statePending {
		t.Fatalf("a fresh selection is not pending (R12)")
	}
	var cmd tea.Cmd
	m, cmd = m.Update(settleMsg{runID: 9})
	m, _ = m.Update(cmd())
	if m.state != stateNoJobs {
		t.Fatalf("an empty response was not treated as no-Jobs (R12a)")
	}
	if !strings.Contains(m.View(), "No jobs yet") {
		t.Fatalf("the no-Jobs state is not explicit (R12a, AC14)")
	}

	errored := &fakeFetch{err: errors.New("transport blew up")}
	m2 := newPane(errored.fn())
	m2, _ = m2.Open(run(9, inProgress))
	m2, cmd = m2.Update(settleMsg{runID: 9})
	m2, _ = m2.Update(cmd())
	if m2.state != stateNoJobs {
		t.Fatalf("an error response was not treated alike to empty (R12a, AC14)")
	}
}

// TestReRunUpdatesBadgeInPlace pins R17: a re-run mutates the same Run in place, and a
// same-Run refresh tracks the incremented Attempt without re-arming the debounce or
// re-fetching, because it is not a new selection.
func TestReRunUpdatesBadgeInPlace(t *testing.T) {
	m := newPane(nil)
	m, _ = m.Open(runAttempt(1, completed, 1))
	m, cmd := m.SelectRun(runAttempt(1, queued, 2)) // re-run: same ID, Attempt up, Status back to queued
	if cmd != nil {
		t.Fatalf("a same-Run refresh re-armed the debounce (R10, R17)")
	}
	if m.run.RunAttempt != 2 {
		t.Fatalf("the badge did not track the re-run in place (R17)")
	}
	if !strings.Contains(m.View(), "Attempt 2") {
		t.Fatalf("the re-run's Attempt badge did not update (R17, R4)")
	}
}

// TestSelectRunWhileClosedIsNoop pins that the pane follows the Feed's cursor only while it
// is open: a report of the cursor to a closed pane neither opens it nor fetches.
func TestSelectRunWhileClosedIsNoop(t *testing.T) {
	f := &fakeFetch{}
	m := newPane(f.fn())
	m, cmd := m.SelectRun(run(1, completed))
	if cmd != nil || m.IsOpen() {
		t.Fatalf("SelectRun opened or armed a closed pane")
	}
	if f.calls != 0 {
		t.Fatalf("a closed pane fetched")
	}
}

// TestViewSanitisesJobAndStepText pins the same terminal-escape defence the Feed uses: a
// hostile Job or Step name, which an author on any polled repository controls, is stripped
// of its control sequences before it is painted, so a CSI clear-screen or a carriage return
// cannot survive into the pane's View to rewrite or spoof the operator's terminal (security
// review). lipgloss wraps cells in its own SGR escapes, so the assertion targets the hostile
// sequences by shape rather than the mere presence of an ESC byte.
func TestViewSanitisesJobAndStepText(t *testing.T) {
	f := &fakeFetch{}
	m := newPane(f.fn())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120})
	m, _ = m.Open(run(1, inProgress))
	hostileJob := "build\x1b[2J\x1b[Hpwned"
	hostileStep := "compile\rowned"
	m, _ = m.Update(jobsMsg{runID: 1, jobs: []domain.Job{
		{Name: hostileJob, Status: inProgress, Steps: []domain.Step{
			{Number: 1, Name: hostileStep, Status: inProgress},
		}},
	}})
	view := m.View()

	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[H") {
		t.Fatalf("a hostile CSI sequence survived into the pane's View:\n%q", view)
	}
	if strings.ContainsRune(view, '\r') {
		t.Fatalf("a carriage return survived into the pane's View:\n%q", view)
	}
	if !strings.Contains(view, "buildpwned") || !strings.Contains(view, "compileowned") {
		t.Fatalf("the sanitiser dropped or split visible Job or Step text:\n%q", view)
	}
}

// TestIsOrphanedGatesReRun pins run-detail R18's Workflow-State limb (AC15, resolved open
// question 8): a Run whose Workflow is deleted is Orphaned, so the pane reports it and the
// Feed keeps the re-run key inert over it. An unresolved Workflow State reads as a live
// successor, so a Run is not treated as Orphaned until the Feed resolves a deleted State.
func TestIsOrphanedGatesReRun(t *testing.T) {
	m := New(Options{})
	if m.IsOrphaned() {
		t.Errorf("a pane with no resolved Workflow State reported Orphaned; the default is a live successor (R8)")
	}
	if !m.SetWorkflowState(domain.StateDeleted).IsOrphaned() {
		t.Errorf("a deleted Workflow did not read as Orphaned (R18, AC15)")
	}
	if m.SetWorkflowState(domain.StateActive).IsOrphaned() {
		t.Errorf("a live Workflow read as Orphaned; only a deleted Workflow gates re-run (R18)")
	}
}
