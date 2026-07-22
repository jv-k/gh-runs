package logview

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// The goldens render the pane's frame from held content alone, at 100 columns, with no
// terminal and no network (R20, AC9). lipgloss v2 renders truecolour regardless of the
// environment, so these bytes are stable on any machine (ADR-0013). Every one is a byte-level
// check of a transformation applied to content the tool did not author and cannot predict, so
// a fold that swallowed a line, a prefix returned one character short, or a log line painted
// unsanitised fails its golden. Run with -update to regenerate:
// go test ./internal/tui/logview/ -run Golden -update.

// goldenPane builds a loaded pane at 100x30 over the injected log, so a golden asserts what
// the state machine actually paints.
func goldenPane(raw []byte) Model {
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Open(testRun(), testJob(), writableRepo(), true)
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: raw})
	return m
}

// TestGoldenDefaultView fixes AC9's default case: the trivial log with no U+FEFF, no timestamp
// prefix, and twelve folds collapsed and labelled, with two warning lines visible between them
// (R3, R4, R5, AC2).
func TestGoldenDefaultView(t *testing.T) {
	goldie.New(t).Assert(t, "default_view", []byte(goldenPane(trivialLog()).View()))
}

// TestGoldenTimestampsOn fixes AC9's timestamps-on case: the same log with the 29-char ISO
// prefix restored byte-identically on every line (R4, AC3).
func TestGoldenTimestampsOn(t *testing.T) {
	m := goldenPane(trivialLog()).key("t")
	goldie.New(t).Assert(t, "timestamps_on", []byte(m.View()))
}

// TestGoldenFoldExpanded fixes AC9's fold-expanded case: the first fold expanded to reveal its
// body under a labelled header, the other eleven still collapsed (R5).
func TestGoldenFoldExpanded(t *testing.T) {
	m := goldenPane(trivialLog()).key("space") // expand the fold under the cursor
	goldie.New(t).Assert(t, "fold_expanded", []byte(m.View()))
}

// TestGoldenStyledMarkers fixes AC9's marker case: ##[error] and ##[warning] lines styled
// apart from ordinary lines and from each other, plus the rest of the family and an
// unrecognised marker rendered verbatim (R7, R8, R9).
func TestGoldenStyledMarkers(t *testing.T) {
	goldie.New(t).Assert(t, "styled_markers", []byte(goldenPane(styledLog()).View()))
}

// TestGoldenSanitized is the security golden: a log line carrying ANSI escapes and control
// bytes renders with every one stripped, the visible text intact, so the frame proves R19 at
// the byte level. A regression that let a control sequence through would change these bytes.
func TestGoldenSanitized(t *testing.T) {
	goldie.New(t).Assert(t, "sanitized", []byte(goldenPane(hostileLog()).View()))
}
