package feed

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/timefmt"
)

// now0 is the instant the relative tests measure from, three hours after the t0 the Runs
// start at, so an age is a fixed number rather than a function of when the suite runs.
var now0 = t0.Add(3 * time.Hour)

// relativeFeed builds a Feed rendering [settings] R10's relative format against a fake
// clock, which is what makes an age deterministic under a golden (R36: held state alone).
// The format arrives through the setter rather than a construction option, because that is
// the one way the root sets it: the setting is live, so the root has to reach a held model.
func relativeFeed(width, height int) Model {
	m := New(Options{
		Profile: keys.Standard,
		Clock:   clockwork.NewFakeClockAt(now0),
	}).SetTimestampFormat(TimestampRelative)
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// TestRelativeTimestampsRenderAsAges pins settings R10 at the Feed's row formatter: under
// the relative format the STARTED column carries how long ago a Run began, measured from
// the injected clock, and the absolute instant the default renders is gone rather than
// merely joined.
func TestRelativeTimestampsRenderAsAges(t *testing.T) {
	m := relativeFeed(100, 5)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id,
		mkRun(1, "cli", "cli", "CI", domain.StatusInProgress, "", t0),
		mkRun(2, "cli", "cli", "Release", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-3*24*time.Hour)),
	)

	view := m.View()
	for _, want := range []string{"3h", "3d"} {
		if !strings.Contains(view, want) {
			t.Errorf("the relative format did not render the age %q (R10):\n%s", want, view)
		}
	}
	if strings.Contains(view, "2026-07-15T16:39:00Z") {
		t.Errorf("the relative format still rendered the absolute instant (R10):\n%s", view)
	}
}

// startedCell returns the text of the one displayed row's STARTED column, which is the last
// cell in R4a's fixed column order. A test reads it rather than searching the whole frame,
// so an age of "1m" cannot be matched inside a Run ID or another row's cell.
func startedCell(t *testing.T, m Model) string {
	t.Helper()
	const marker = "31415926535" // the Run ID every case below carries, the cell before STARTED
	for _, line := range strings.Split(ansiRE.ReplaceAllString(m.View(), ""), "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):])
		}
	}
	t.Fatalf("no row for Run %s in the frame:\n%s", marker, m.View())
	return ""
}

// TestRelativeTimestampReadsTheSharedLadder pins the seam rather than the ladder: under the
// relative format the STARTED cell carries what timefmt.Age renders for the same two
// instants, measured from the injected clock. The buckets themselves are asserted in
// internal/timefmt, over an injected now and no Model at all, because a bucket ladder is
// pure logic and building a whole Feed per boundary points the golden seam the wrong way.
// What is left here is the one thing only a Feed can prove: this cell reads that function.
func TestRelativeTimestampReadsTheSharedLadder(t *testing.T) {
	started := now0.Add(-3 * 24 * time.Hour)
	m := relativeFeed(100, 4)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id, mkRun(31415926535, "cli", "cli", "CI", domain.StatusInProgress, "", started))

	want := timefmt.Age(now0, started)
	if want == "" {
		t.Fatal("the fixture renders no age; the case proves nothing")
	}
	if got := startedCell(t, m); got != want {
		t.Errorf("the STARTED cell rendered %q, want timefmt's %q (R10)", got, want)
	}
}

// TestRelativeTimestampFallsBackWithoutAClock pins the other half of the seam: the relative
// form measures an age from the injected clock, so a Feed built without one paints the
// absolute instant rather than reaching for the wall clock. A renderer that covered the gap
// with time.Now would make every golden a function of when the suite ran.
func TestRelativeTimestampFallsBackWithoutAClock(t *testing.T) {
	m := New(Options{Profile: keys.Standard}).SetTimestampFormat(TimestampRelative)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 4})
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id, mkRun(31415926535, "cli", "cli", "CI", domain.StatusInProgress, "", t0))

	if got := startedCell(t, m); got != "2026-07-15T16:39:00Z" {
		t.Errorf("the relative format without a clock rendered %q, want the absolute instant", got)
	}
}

// TestAbsoluteTimestampsAreTheDefault pins settings R10's default at the same seam: a Feed
// built with no timestamp format stated renders the measured instant live-run-feed R4a sizes
// the column to, so every frame that existed before this setting is unchanged.
func TestAbsoluteTimestampsAreTheDefault(t *testing.T) {
	m := newFeed(100, 4)
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id, mkRun(1, "cli", "cli", "CI", domain.StatusInProgress, "", t0))

	if view := m.View(); !strings.Contains(view, "2026-07-15T16:39:00Z") {
		t.Errorf("the default format did not render the absolute instant (R10, R4a):\n%s", view)
	}
}

// TestTimestampFormatChangesOnAHeldModel pins the half of settings R17 the root depends on:
// the format is resolved as a row paints, so setting it on a Feed that already holds Runs
// repaints them, and the frame R10's deferral froze is repainted with the rest. Without
// this, the root could push a live change into a Feed that keeps painting the old form.
func TestTimestampFormatChangesOnAHeldModel(t *testing.T) {
	m := New(Options{Profile: keys.Standard, Clock: clockwork.NewFakeClockAt(now0)})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 4})
	id := repoID("cli", "cli")
	m = discovered(m, repo("cli", "cli", true, false))
	m = feedRuns(m, id, mkRun(31415926535, "cli", "cli", "CI", domain.StatusInProgress, "", t0))

	if got := startedCell(t, m); got != "2026-07-15T16:39:00Z" {
		t.Fatalf("the held frame opened rendering %q, want the absolute instant (R10)", got)
	}
	m = m.SetTimestampFormat(TimestampRelative)
	if got := startedCell(t, m); got != "3h" {
		t.Errorf("a format set on a held model rendered %q, want the age %q (R17)", got, "3h")
	}
	m = m.SetTimestampFormat(TimestampAbsolute)
	if got := startedCell(t, m); got != "2026-07-15T16:39:00Z" {
		t.Errorf("cycling back rendered %q, want the absolute instant (R17)", got)
	}
}
