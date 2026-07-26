package feed

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// cancelFrame is a progress frame from a bulk cancel that the API accepted for ids. It is
// fabricated rather than driven from a real walk, because what is under test is the Feed's
// reading of the broadcast, not ops's production of it (which TestSucceededNamesTheRunsTheAPIAccepted
// pins against a cassette).
func cancelFrame(op ops.Operation, done bool, runs ...domain.Run) ops.Progress {
	items := make([]ops.Item, 0, len(runs))
	for _, r := range runs {
		items = append(items, ops.RunItem(r))
	}
	return ops.Progress{
		Op:   op,
		Kind: ops.KindRun,
		Sum:  ops.Summary{Total: len(items), Acted: len(items), Succeeded: items},
		Done: done,
	}
}

// running is a Run mid-flight, the only state a cancel can be outstanding against.
func runningRun(id int64, started time.Time) domain.Run {
	return mkRun(id, "o", "r", "CI", domain.StatusInProgress, "", started)
}

// gutterOf returns the two-character gutter of the rendered row carrying id, with ANSI
// styling stripped. Asserting the gutter by position rather than searching the frame for the
// marker matters because the marker is one character: "c" occurs in "completed", in
// "cancelled" and in the CONCLUSION heading, so a substring scan over the frame would pass on
// any of them and prove nothing about the row.
func gutterOf(t *testing.T, m Model, id int64) string {
	t.Helper()
	want := strconv.FormatInt(id, 10)
	for _, line := range strings.Split(ansiRE.ReplaceAllString(m.View(), ""), "\n") {
		// The Run ID column is its own cell, so match it as a padded field rather than
		// anywhere in the line: a timestamp or a repo name could otherwise carry the digits.
		if slices.Contains(strings.Fields(line), want) {
			return line[:gutterW]
		}
	}
	t.Fatalf("no row for Run %d in the frame:\n%s", id, m.View())
	return ""
}

// ansiRE strips SGR sequences so a gutter is compared as text.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestCancelRequestedMarksOnlyTheAcceptedRows is AC5's first half. A 202 means the request
// was accepted, not that the Run was cancelled (R4), so the row must say a cancellation was
// requested and must not claim a Conclusion. Only the Runs the API accepted are marked: a
// Run in the same frozen set that answered 409 or 404 was skipped, and nothing is outstanding
// against it.
func TestCancelRequestedMarksOnlyTheAcceptedRows(t *testing.T) {
	m := newFeed(100, 24)
	accepted := runningRun(1, t0)
	untouched := runningRun(2, t0.Add(-time.Minute))
	m = feedRuns(m, repoID("o", "r"), accepted, untouched)

	m = m.Update2(cancelFrame(ops.OpCancel, true, accepted))

	if _, marked := m.cancelRequested[1]; !marked {
		t.Errorf("Run 1 answered 202 and is not marked as cancellation-requested (R4, AC5)")
	}
	if _, marked := m.cancelRequested[2]; marked {
		t.Errorf("Run 2 was never accepted and is marked; only an accepted request is outstanding")
	}

	if got := gutterOf(t, m, 1); got != cancelRequestedMarker+" " {
		t.Errorf("Run 1's gutter = %q, want the cancellation-requested marker (AC5)", got)
	}
	if got := gutterOf(t, m, 2); got != blankMarker {
		t.Errorf("Run 2's gutter = %q, want blank; nothing is outstanding against it", got)
	}
	got := m.View()
	if !strings.Contains(got, "cancellation requested for 1 run") {
		t.Errorf("the frame does not explain the marker (AC5):\n%s", got)
	}
	// R4 and AC5's other half, and AC4's: an accepted request licenses no Conclusion at all.
	// The Run is still in_progress, so its Conclusion cell stays empty until a poll says
	// otherwise. Checked against the row, because the legend line names the word too.
	if strings.Contains(rowOf(t, m, 1), string(domain.ConclusionCancelled)) {
		t.Errorf("the row renders a cancelled Conclusion off an accepted request alone; only a poll may (R4, AC5):\n%s", got)
	}
}

// rowOf is the rendered row for id, ANSI stripped.
func rowOf(t *testing.T, m Model, id int64) string {
	t.Helper()
	want := strconv.FormatInt(id, 10)
	for _, line := range strings.Split(ansiRE.ReplaceAllString(m.View(), ""), "\n") {
		if slices.Contains(strings.Fields(line), want) {
			return line
		}
	}
	t.Fatalf("no row for Run %d", id)
	return ""
}

// TestForceCancelAlsoMarksTheRow pins that the indicator follows the requested end state and
// not the endpoint: force-cancel is a distinct operation (R6) but its accepted request leaves
// the same thing outstanding, so the row reports it the same way.
func TestForceCancelAlsoMarksTheRow(t *testing.T) {
	m := newFeed(100, 24)
	r := runningRun(1, t0)
	m = feedRuns(m, repoID("o", "r"), r)
	m = m.Update2(cancelFrame(ops.OpForceCancel, true, r))
	if _, marked := m.cancelRequested[1]; !marked {
		t.Errorf("a force-cancel the API accepted left no cancellation-requested mark (R6, AC5)")
	}
}

// TestRerunDoesNotMarkACancellation pins that the mark is cancel's alone. A re-run is also
// 'acted', and it also mutates the row, but nothing is being cancelled: marking it would put
// a cancellation-requested indicator on a Run that is starting, which is the opposite claim.
func TestRerunDoesNotMarkACancellation(t *testing.T) {
	for _, op := range []ops.Operation{ops.OpRerun, ops.OpRerunFailed, ops.OpDelete} {
		m := newFeed(100, 24)
		r := runningRun(1, t0)
		m = feedRuns(m, repoID("o", "r"), r)
		m = m.Update2(cancelFrame(op, true, r))
		if _, marked := m.cancelRequested[1]; marked {
			t.Errorf("%s marked the row as cancellation-requested; only cancel and force-cancel may (R4)", op)
		}
	}
}

// TestCancelRequestedClearsWhenThePollObservesTheTransition is AC5's point. The indicator
// says a request is outstanding, so it must stop saying so once the answer arrives. R4 puts
// the authority in the poll: the Run reaching Status completed is the transition being
// observed, and from then the row shows the real Conclusion and no marker.
func TestCancelRequestedClearsWhenThePollObservesTheTransition(t *testing.T) {
	m := newFeed(100, 24)
	r := runningRun(1, t0)
	m = feedRuns(m, repoID("o", "r"), r)
	m = m.Update2(cancelFrame(ops.OpCancel, true, r))
	if _, marked := m.cancelRequested[1]; !marked {
		t.Fatalf("precondition: the row was not marked")
	}

	// The poll observes the transition: Status completed, Conclusion cancelled (R7: they are
	// two fields, and cancelled is never a Status).
	settled := mkRun(1, "o", "r", "CI", domain.StatusCompleted, domain.ConclusionCancelled, t0)
	m = feedRuns(m, repoID("o", "r"), settled)

	if _, marked := m.cancelRequested[1]; marked {
		t.Errorf("the indicator survived the poll that observed the transition (R4, AC5)")
	}
	if got := gutterOf(t, m, 1); got != blankMarker {
		t.Errorf("Run 1's gutter = %q after the transition landed, want blank (R4, AC5)", got)
	}
	got := m.View()
	if strings.Contains(got, "cancellation requested for") {
		t.Errorf("the legend survived the transition it was waiting on:\n%s", got)
	}
	// R7 and AC4: the two fields stay their own. cancelled is the Conclusion, and the Status
	// beside it is completed, which is what a cancelled Run actually is.
	row := rowOf(t, m, 1)
	if !strings.Contains(row, string(domain.ConclusionCancelled)) {
		t.Errorf("the settled Run does not show its cancelled Conclusion (R7, AC4):\n%s", row)
	}
	if !strings.Contains(row, string(domain.StatusCompleted)) {
		t.Errorf("the settled Run does not show Status completed beside it (R7, AC4):\n%s", row)
	}
}

// TestCancelRequestedSurvivesAPollThatShowsNoTransitionYet is the same rule read the other
// way. Cancel is asynchronous (R4), so the polls between the 202 and the transition show a
// Run still running, and the indicator must persist across them. Clearing on any poll would
// make the mark flicker off one tick after it appeared.
func TestCancelRequestedSurvivesAPollThatShowsNoTransitionYet(t *testing.T) {
	m := newFeed(100, 24)
	r := runningRun(1, t0)
	m = feedRuns(m, repoID("o", "r"), r)
	m = m.Update2(cancelFrame(ops.OpCancel, true, r))

	m = feedRuns(m, repoID("o", "r"), runningRun(1, t0)) // still in_progress

	if _, marked := m.cancelRequested[1]; !marked {
		t.Errorf("the indicator cleared on a poll that observed no transition; cancel is asynchronous (R4)")
	}
}

// TestCancelRequestedClearsWhenTheRepositoryLeavesThePollSet closes the hole the Feed's
// failed-poll map already closes for the same reason. A repository deleted or made private
// upstream fails its next poll once and then leaves the poll set, so no Update can ever
// arrive carrying its Runs, so the transition that clears a mark can never be observed. The
// legend would then report a request outstanding for the rest of the session, against a
// repository the tool is no longer even asking about.
//
// The prune is against the discovered set, which arrives as a full set, so absence is
// meaningful. The empty case is not: at cold start nothing is discovered yet.
func TestCancelRequestedClearsWhenTheRepositoryLeavesThePollSet(t *testing.T) {
	gone := repoID("o", "gone")
	m := newFeed(100, 24)
	m, _ = m.Update(ReposDiscovered{
		{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}},
		{ID: gone, Permissions: domain.Permissions{Push: true}},
	})
	r := mkRun(1, "o", "gone", "CI", domain.StatusInProgress, "", t0)
	m = feedRuns(m, gone, r)
	m = m.Update2(cancelFrame(ops.OpCancel, true, r))
	if _, marked := m.cancelRequested[1]; !marked {
		t.Fatalf("precondition: the Run was not marked")
	}

	// The next discovery no longer carries the repository.
	m, _ = m.Update(ReposDiscovered{{ID: repoID("o", "r"), Permissions: domain.Permissions{Push: true}}})

	if _, marked := m.cancelRequested[1]; marked {
		t.Errorf("the mark survived its repository leaving the poll set; nothing can ever clear it (R4)")
	}
	if strings.Contains(m.View(), "cancellation requested for") {
		t.Errorf("the legend still reports a request nothing is testing anymore:\n%s", m.View())
	}
}

// TestCancelRequestedSurvivesAnEmptyDiscovery pins the other side of that prune: a cold
// start discovers nothing, and pruning against an empty set would clear a mark whose
// repository the poll set still holds.
func TestCancelRequestedSurvivesAnEmptyDiscovery(t *testing.T) {
	id := repoID("o", "r")
	m := newFeed(100, 24)
	m, _ = m.Update(ReposDiscovered{{ID: id, Permissions: domain.Permissions{Push: true}}})
	r := runningRun(1, t0)
	m = feedRuns(m, id, r)
	m = m.Update2(cancelFrame(ops.OpCancel, true, r))

	m, _ = m.Update(ReposDiscovered{})

	if _, marked := m.cancelRequested[1]; !marked {
		t.Errorf("an empty discovery cleared the mark; absence is only meaningful in a full set")
	}
}

// TestLegendIsCountedInTheChrome pins that the legend is accounted for in the row budget.
// View sizes its rows from the lines it is about to paint, so the frame is self-consistent
// whatever the chrome; rowCapacity recomputes the same number from chromeLineCount, and it
// is what pageSize and the viewport published to the scheduler's medium tier read. A line
// View paints and chromeLineCount does not know about puts those two out of step by one, so
// a page moves one row further than a screen and the viewport claims a row that is not on it.
func TestLegendIsCountedInTheChrome(t *testing.T) {
	id := repoID("o", "r")
	var runs []domain.Run
	for i := int64(1); i <= 12; i++ {
		runs = append(runs, runningRun(i, t0.Add(-time.Duration(i)*time.Minute)))
	}
	m := newFeed(100, 10) // deliberately shorter than the run count, so the budget binds
	m = feedRuns(m, id, runs...)

	before := m.rowCapacity()
	if got := paintedRows(t, m); got != before {
		t.Fatalf("precondition: painted %d rows against a capacity of %d", got, before)
	}

	m = m.Update2(cancelFrame(ops.OpCancel, true, runs[0]))

	if got, want := paintedRows(t, m), m.rowCapacity(); got != want {
		t.Errorf("with the legend up, View paints %d rows and rowCapacity says %d; paging and the "+
			"published viewport read the second number", got, want)
	}
	if m.rowCapacity() != before-1 {
		t.Errorf("rowCapacity = %d after the legend appeared, want %d: the legend costs one row",
			m.rowCapacity(), before-1)
	}
}

// paintedRows counts the Run rows in the frame. A row is the only line carrying the
// repository cell, so the chrome, the legend and the status line are all excluded.
func paintedRows(t *testing.T, m Model) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(ansiRE.ReplaceAllString(m.View(), ""), "\n") {
		if strings.Contains(line, "o/r ") {
			n++
		}
	}
	return n
}
