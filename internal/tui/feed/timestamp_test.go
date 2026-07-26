package feed

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// now0 is the instant the relative tests measure from, three hours after the t0 the Runs
// start at, so an age is a fixed number rather than a function of when the suite runs.
var now0 = t0.Add(3 * time.Hour)

// relativeFeed builds a Feed rendering [settings] R10's relative format against a fake
// clock, which is what makes an age deterministic under a golden (R36: held state alone).
func relativeFeed(width, height int) Model {
	m := New(Options{
		Profile:   keys.Standard,
		Timestamp: TimestampRelative,
		Clock:     clockwork.NewFakeClockAt(now0),
	})
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

// TestRelativeTimestampBuckets pins the relative format's wording bucket by bucket, at the
// boundaries where it changes unit. Every bucket truncates rather than rounds, which is why
// 23h59m is still hours and 29d23h is still days, and a timestamp ahead of this machine's
// clock reads as "just now" rather than as a negative age: skew against GitHub's clock is
// real, and a Run that has not started yet has not started long ago.
func TestRelativeTimestampBuckets(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{0, "just now"},
		{59 * time.Second, "just now"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{day, "1d"},
		{29*day + 23*time.Hour, "29d"},
		{30 * day, "1mo"},
		{364 * day, "12mo"},
		{365 * day, "1y"},
	}
	for _, tc := range cases {
		m := relativeFeed(100, 4)
		id := repoID("cli", "cli")
		m = discovered(m, repo("cli", "cli", true, false))
		m = feedRuns(m, id, mkRun(31415926535, "cli", "cli", "CI", domain.StatusInProgress, "", now0.Add(-tc.ago)))

		if got := startedCell(t, m); got != tc.want {
			t.Errorf("a Run started %s ago rendered %q, want %q", tc.ago, got, tc.want)
		}
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
