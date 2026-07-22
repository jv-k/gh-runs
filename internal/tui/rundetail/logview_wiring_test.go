package rundetail

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// press builds a key press whose String() matches a registry binding, so the wiring test
// drives the pane through the same key.Matches path the runtime does (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// TestOpenLogFromJobFocus pins the chain the stage builds: Run detail opens a Job's log
// (log-viewer R1, "from Run detail"). The pane does not capture keys until the operator
// descends into job-focus; then the Job cursor moves and enter opens the selected Job's log,
// which fetches and renders. esc pops one level to job-focus, then another to the Feed-driven
// detail, so focus unwinds the way it wound (ADR-0011's recursive focus).
func TestOpenLogFromJobFocus(t *testing.T) {
	log := []byte("2026-07-15T03:11:52.0835958Z hello from the job log\n")
	fetch := func(domain.RepoID, int64) ([]byte, error) { return log, nil }
	m := New(Options{Profile: keys.Standard, Clock: clockwork.NewFakeClockAt(t0), LogFetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	r := gRun(4675883901, "cli", "cli", "CI", 4821, 1, completed, success)
	m, _ = m.Open(r)
	m = m.SetRepoCapability(domain.Repo{ID: repoID("cli", "cli"), Permissions: domain.Permissions{Push: true}}, true)
	jobs := []domain.Job{
		{ID: 111, Name: "build", Status: completed, Conclusion: success},
		{ID: 222, Name: "test", Status: completed, Conclusion: success},
	}
	m, _ = m.Update(jobsMsg{runID: r.ID, jobs: jobs})

	// Feed-focused: the pane does not capture keys, so the Feed still drives its Run cursor.
	if m.CapturesKeys() {
		t.Fatal("the pane captures keys before the operator descends; the Feed would lose its cursor")
	}
	// Descend into job-focus.
	m = m.Focus()
	if !m.CapturesKeys() {
		t.Fatal("Focus did not put the pane into job-focus (recursive focus)")
	}
	// Move the Job cursor to the second Job, then open its log.
	m, _ = m.HandleKey(press("down"))
	m, cmd := m.HandleKey(press("enter"))
	if !m.log.IsOpen() {
		t.Fatal("enter in job-focus did not open the log view (log-viewer R1)")
	}
	if cmd == nil {
		t.Fatal("opening the log did not arm a fetch (R1)")
	}
	// The View is now the log view's frame, and the fetch result renders into it.
	m, _ = m.Update(cmd())
	if !strings.Contains(m.View(), "hello from the job log") {
		t.Errorf("the log view did not render the fetched content:\n%s", m.View())
	}

	// esc closes the log back to job-focus.
	m, _ = m.HandleKey(press("esc"))
	if m.log.IsOpen() {
		t.Error("esc did not close the log view")
	}
	if !m.active {
		t.Error("closing the log did not return to job-focus")
	}
	// esc again leaves job-focus, handing the cursor back to the Feed.
	m, _ = m.HandleKey(press("esc"))
	if m.active {
		t.Error("esc did not leave job-focus")
	}
}

// TestCapturesInputPropagatesFromLog pins that the pane reports input capture up the chain
// while the log view's search input is open, so the root routes every key to it and a typed
// query is not stolen as a global key (R7, log-viewer R21). Opening the log then the search
// makes CapturesInput true; cancelling the search clears it.
func TestCapturesInputPropagatesFromLog(t *testing.T) {
	log := []byte("2026-07-15T03:11:52.0835958Z searchable content here\n")
	fetch := func(domain.RepoID, int64) ([]byte, error) { return log, nil }
	m := New(Options{Profile: keys.Standard, Clock: clockwork.NewFakeClockAt(t0), LogFetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	r := gRun(1, "cli", "cli", "CI", 1, 1, completed, success)
	m, _ = m.Open(r)
	m, _ = m.Update(jobsMsg{runID: r.ID, jobs: []domain.Job{{ID: 111, Name: "build", Status: completed, Conclusion: success}}})
	m = m.Focus()
	m, cmd := m.HandleKey(press("enter")) // open the log
	m, _ = m.Update(cmd())

	if m.CapturesInput() {
		t.Fatal("the pane captures input before any search is open")
	}
	m, _ = m.HandleKey(press("/")) // open the in-log search
	if !m.CapturesInput() {
		t.Error("the pane does not report input capture while the log search is open (R7, R21)")
	}
	m, _ = m.HandleKey(press("esc")) // cancel the search
	if m.CapturesInput() {
		t.Error("the pane still captures input after the search was cancelled")
	}
}
