package logview

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// The pane tests are white-box so they can inject a fetched result directly and read the view
// state, exactly as the rundetail goldens inject a jobsMsg: the fetch state machine is proved
// against the cassette in fetch_test, and these tests are about the rendering and the key
// handling over held content, with no terminal and no network (R20).

func testRepoID() domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}
}

func testRun() domain.Run {
	return domain.Run{ID: 4675883901, RunNumber: 4821, RunAttempt: 1, WorkflowName: "CI", Repo: testRepoID()}
}

func testJob() domain.Job {
	return domain.Job{ID: 123456789, Name: "build", RunID: 4675883901}
}

func writableRepo() domain.Repo {
	return domain.Repo{ID: testRepoID(), Permissions: domain.Permissions{Push: true}}
}

// press builds a key press whose String() matches a registry binding, so a test drives the
// pane through the same key.Matches path the runtime does (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// key drives one key through HandleKey and returns the pane, dropping the Cmd and the handled
// flag for a test that reads state.
func (m Model) key(s string) Model {
	nm, _, _ := m.HandleKey(press(s))
	return nm
}

// typeText presses each rune of s through HandleKey, for the search input.
func (m Model) typeText(s string) Model {
	for _, r := range s {
		m = m.key(string(r))
	}
	return m
}

// loaded builds a pane at 100x40 opened over a Run and Job, with raw injected as the fetched
// log, so the state machine paints what a fetch would have produced (no network).
func loaded(t *testing.T, raw []byte) Model {
	t.Helper()
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(testRun(), testJob(), writableRepo(), true)
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: raw})
	if m.state != stateLoaded {
		t.Fatalf("pane is in state %d after a fetch, want loaded", m.state)
	}
	return m
}

// TestOpenIssuesOneFetchForTheJob pins R1 and AC1: opening a Job arms exactly one fetch for
// that Job's log. The returned Cmd is the fetch; opening a Run detail without opening a Job
// issues none, which is a property of the opener never calling Open.
func TestOpenIssuesOneFetchForTheJob(t *testing.T) {
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	_, cmd := m.Open(testRun(), testJob(), writableRepo(), true)
	if cmd == nil {
		t.Fatal("Open did not arm a fetch for the opened Job (R1)")
	}
	msg := cmd()
	fetched, ok := msg.(logFetchedMsg)
	if !ok {
		t.Fatalf("the Open Cmd produced %T, want a logFetchedMsg (R1)", msg)
	}
	if fetched.jobID != testJob().ID {
		t.Errorf("the fetch was tagged with Job %d, want %d (R1)", fetched.jobID, testJob().ID)
	}
}

// TestEmptyStateOnNoContent pins R18 and AC7: an empty body renders the empty state, not a
// blank pane and not an error, whatever the cause. An error resolves the same way.
func TestEmptyStateOnNoContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  logFetchedMsg
	}{
		{"empty body", logFetchedMsg{jobID: testJob().ID, data: nil}},
		{"error", logFetchedMsg{jobID: testJob().ID, err: errNoExporter}},
	} {
		m := New(Options{Profile: keys.Standard})
		m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m, _ = m.Open(testRun(), testJob(), writableRepo(), true)
		m = m.onFetched(tc.msg)
		if m.state != stateEmpty {
			t.Errorf("%s: state = %d, want empty (R18)", tc.name, m.state)
		}
		if !strings.Contains(m.View(), "No log content") {
			t.Errorf("%s: view does not render the empty state (R18):\n%s", tc.name, m.View())
		}
	}
}

// sgrRE matches the SGR colour and attribute sequences lipgloss paints, which are ours and
// safe. Stripping them leaves only text and any control byte a log line smuggled past the
// sanitiser, so the assertion below can tell our styling from an attacker's escape.
var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestRenderCarriesNoControlBytes is the single most important test in the pane: every log
// line painted is sanitised through textsan, so a Workflow author's ANSI escapes and control
// bytes never reach the terminal (R19). An unsanitised line rewrites or spoofs the operator's
// terminal, which in a delete tool is a decision-integrity failure. lipgloss paints its own
// SGR colour sequences, which are ours; stripping those, nothing of the attacker's payload
// survives, and the visible text between the escapes does.
func TestRenderCarriesNoControlBytes(t *testing.T) {
	m := loaded(t, hostileLog())
	view := m.View()

	// With lipgloss's own SGR sequences removed, no ESC, carriage return or tab remains: those
	// would be the attacker's, and textsan stripped them before the line was ever styled.
	plain := sgrRE.ReplaceAllString(view, "")
	for _, r := range plain {
		if r == 0x1b {
			t.Errorf("an attacker ESC survived into the painted view (R19):\n%q", view)
		}
		if (r < 0x20 && r != '\n') || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("a control byte U+%04X survived into the painted view (R19)", r)
		}
	}
	// The dangerous sequences specifically, which lipgloss never emits: clear-screen and
	// cursor-home would let a log repaint the operator's terminal.
	for _, seq := range []string{"\x1b[2J", "\x1b[H"} {
		if strings.Contains(view, seq) {
			t.Errorf("the view carries the attacker sequence %q (R19)", seq)
		}
	}
	// The sanitised content itself, before any styling, carries no control byte at all.
	for _, l := range m.visibleLines() {
		if strings.ContainsAny(l.search, "\x1b\r\t") {
			t.Errorf("a visible line's content was not sanitised before painting: %q (R19)", l.search)
		}
	}
	// The visible text of the hostile line survives once the controls are dropped. The ESC, CSI,
	// clear-screen, cursor-home, CR and bare ESC are stripped whole, so the words they separated
	// run together; the one TAB is expanded to spaces, the single control kept as layout (R19),
	// so "retreat" and "tail" survive spaced, never fused and never deleted.
	if !strings.Contains(plain, "Building projectclearedretreat tail") {
		t.Errorf("the sanitiser dropped or split visible log text (R19):\n%q", plain)
	}
	if strings.ContainsRune(plain, '\t') {
		t.Errorf("a raw TAB reached the painted view; it must expand to spaces, not survive (R19):\n%q", plain)
	}
}

// TestDefaultViewIsCleanPerAC2 pins AC2: rendering the trivial log produces a view with no
// U+FEFF at any offset, twelve collapsed folds, two warning lines, and no literal ##[group],
// ##[endgroup] or ##[warning] anywhere. The counts are read from the fixture, never a
// constant.
func TestDefaultViewIsCleanPerAC2(t *testing.T) {
	raw := trivialLog()
	m := loaded(t, raw)
	view := m.View()

	if strings.ContainsRune(view, '\uFEFF') {
		t.Errorf("the view carries U+FEFF; the BOM reached the display (R3, AC2)")
	}
	for _, lit := range []string{"##[group]", "##[endgroup]", "##[warning]"} {
		if strings.Contains(view, lit) {
			t.Errorf("the view renders the literal marker %q (R8, AC2)", lit)
		}
	}
	wantFolds := strings.Count(string(raw), "##[group]")
	if got := strings.Count(view, foldCollapsed); got != wantFolds {
		t.Errorf("the view shows %d collapsed folds, want %d (AC2)", got, wantFolds)
	}
	wantWarnings := strings.Count(string(raw), "##[warning]")
	if got := strings.Count(view, "WARNING"); got != wantWarnings {
		t.Errorf("the view shows %d warning lines, want %d (AC2)", got, wantWarnings)
	}
}

// TestTimestampsToggle pins R4 and AC3: the timestamp prefix is absent by default, restored
// byte-identically when toggled on, and gone again after a second toggle. The restored prefix
// is diffed against the exact 29-char prefix the fixture built.
func TestTimestampsToggle(t *testing.T) {
	m := loaded(t, trivialLog())

	if strings.Contains(m.View(), tsA) {
		t.Errorf("the default view shows a timestamp prefix; R4 strips it")
	}
	m = m.key("t") // toggle on
	if !strings.Contains(m.View(), tsA) {
		t.Errorf("toggling timestamps on did not restore the byte-identical %q prefix (R4, AC3)", tsA)
	}
	m = m.key("t") // toggle off again
	if strings.Contains(m.View(), tsA) {
		t.Errorf("toggling timestamps twice did not return to the stripped view (AC3)")
	}
}

// TestFoldToggle pins R5: a fold is collapsed by default and expands and re-collapses on the
// fold key. Expanding the fold under the cursor reveals its body lines; re-collapsing hides
// them again.
func TestFoldToggle(t *testing.T) {
	m := loaded(t, trivialLog())
	// The cursor opens on the first fold header. Its first body line is "step 0 starting".
	body := "step 0 starting"
	if strings.Contains(m.View(), body) {
		t.Fatalf("a collapsed fold showed its body (R5)")
	}
	m = m.key("space") // expand the fold under the cursor
	if !strings.Contains(m.View(), body) {
		t.Errorf("expanding the fold did not reveal its body (R5):\n%s", m.View())
	}
	m = m.key("space") // re-collapse
	if strings.Contains(m.View(), body) {
		t.Errorf("re-collapsing the fold did not hide its body (R5)")
	}
}

// TestNoColorLegibility pins R10 and AC4: with colour suppressed, the two warning lines are
// still distinguished from ordinary lines by text alone, because R9 carries the distinction
// in the "WARNING" label as well as the colour.
func TestNoColorLegibility(t *testing.T) {
	m := loaded(t, trivialLog())
	m.noColor = true // simulate NO_COLOR without touching the process environment
	view := m.View()
	if strings.Contains(view, "\x1b[") {
		t.Errorf("NO_COLOR did not suppress colour; the view carries an ANSI sequence (R10)")
	}
	if got := strings.Count(strings.ToUpper(view), "WARNING"); got != 2 {
		t.Errorf("with colour off, %d warning lines are distinguishable by text, want 2 (R9, AC4)", got)
	}
}

// TestSearchFindsAndMovesBetweenMatches pins R21 and AC10: searching for a substring present
// on several lines marks every match and moves between them, expanding folds so no match
// hides, and issues no request. The match count is read from the fixture: each of the twelve
// folds carries one "starting" body line.
func TestSearchFindsAndMovesBetweenMatches(t *testing.T) {
	raw := trivialLog()
	m := loaded(t, raw)
	m = m.key("/").typeText("starting") // open search, type the query
	m, cmd, _ := m.HandleKey(press("enter"))
	if cmd != nil {
		t.Errorf("accepting a search issued a Cmd; R21's search must issue no request (AC10)")
	}

	want := strings.Count(string(raw), "starting")
	if len(m.matches) != want {
		t.Fatalf("search found %d matches, want %d (one per fold's starting line) (R21, AC10)", len(m.matches), want)
	}
	first := m.matchIdx
	m = m.key("n") // next match
	if m.matchIdx == first {
		t.Errorf("the next-match key did not move between matches (R21, AC10)")
	}
	m = m.key("N") // previous match returns
	if m.matchIdx != first {
		t.Errorf("the previous-match key did not return to the prior match (R21, AC10)")
	}
}

// TestLongLineWraps pins R22 and AC11: a line wider than the pane wraps rather than being
// truncated, so no character is hidden. The wrapped rows together carry every character of
// the line.
func TestLongLineWraps(t *testing.T) {
	long := strings.Repeat("abcdefghij", 20) // 200 chars, wider than the 100-column pane
	raw := []byte(tsA + long + "\n")
	m := loaded(t, raw)
	m.noColor = true // strip styling so the wrapped fragments are contiguous in the plain text
	view := m.View()
	if strings.Contains(view, "…") {
		t.Errorf("the long line was truncated with an ellipsis; R22 wraps rather than truncating")
	}
	// The wrapped rows together reproduce the whole line, so no character was dropped (AC11).
	joined := strings.ReplaceAll(view, "\n", "")
	if !strings.Contains(joined, long) {
		t.Errorf("the wrapped rows do not reproduce the whole line; a character was hidden (R22, AC11)")
	}
}

// TestDeleteOpensConfirmationThatNamesTheSurvivor pins R17 and AC6: the delete key freezes the
// Run's logs into a one-item Plan of Kind log and opens the confirmation, whose wording names
// both what is destroyed (the logs) and what survives (the Run and its metadata). The set is
// the logs, never the Run: the Item's Kind is log, so Execute would resolve the logs endpoint.
func TestDeleteOpensConfirmationThatNamesTheSurvivor(t *testing.T) {
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	m := New(Options{Profile: keys.Standard, Planner: planner})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(testRun(), testJob(), writableRepo(), true)
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: trivialLog()})

	m = m.key("D")
	if !m.confirmOpen {
		t.Fatalf("the delete key did not open the confirmation (R17)")
	}
	if !m.CapturesInput() {
		t.Errorf("the pane does not capture input while the modal is up; a typed count or y would leak (R7)")
	}
	items := m.confirm.Plan().Items()
	if len(items) != 1 || items[0].Kind != ops.KindLog || items[0].ID != testRun().ID {
		t.Fatalf("the frozen set is not one LogItem for the Run: %+v (R17, ADR-0019)", items)
	}
	view := m.View()
	if !strings.Contains(view, "logs") {
		t.Errorf("the confirmation does not name the logs being destroyed (R17):\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "survive") {
		t.Errorf("the confirmation does not name what survives (the Run and its metadata) (R17):\n%s", view)
	}
	if strings.Contains(view, "Runs") {
		t.Errorf("the log-deletion confirmation reads like a Run deletion; AC6 requires different text:\n%s", view)
	}
}

// TestDeleteInertWithoutPlanner pins that with no planner wired (a golden pane, or before
// discovery), the delete key is inert, keeping the destructive action disabled (R8, R17).
func TestDeleteInertWithoutPlanner(t *testing.T) {
	m := loaded(t, trivialLog()) // built with no Planner
	m = m.key("D")
	if m.confirmOpen {
		t.Errorf("the delete key opened a modal with no planner; it must be inert (repo-discovery R8)")
	}
}

// TestDeleteInertForUnknownRepo pins the fail-closed gate: an unknown repository (its
// capability not yet discovered) leaves the delete disabled, because ops.Plan fails closed on
// a repository absent from the eligibility snapshot (repo-discovery R8, R17).
func TestDeleteInertForUnknownRepo(t *testing.T) {
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	m := New(Options{Profile: keys.Standard, Planner: planner})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(testRun(), testJob(), domain.Repo{}, false) // known == false
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: trivialLog()})
	m = m.key("D")
	if m.confirmOpen {
		t.Errorf("the delete key opened a modal for an unknown repository; the gate must fail closed (R8, R17)")
	}
}

// TestDeleteInertForReadOnlyRepo pins R17's eligibility gate: in a read-only repository every
// row of the one-log set is ineligible, so no modal opens and no request can follow, and the
// pane says why. The gate is advisory, but the modal never appears when nothing can be
// deleted.
func TestDeleteInertForReadOnlyRepo(t *testing.T) {
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	m := New(Options{Profile: keys.Standard, Planner: planner})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	readOnly := domain.Repo{ID: testRepoID(), Permissions: domain.Permissions{Push: false}}
	m, _ = m.Open(testRun(), testJob(), readOnly, true)
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: trivialLog()})
	m = m.key("D")
	if m.confirmOpen {
		t.Errorf("the delete key opened a modal in a read-only repository; R17 offers no delete there")
	}
	if !strings.Contains(m.View(), "read-only") {
		t.Errorf("the pane does not say why the logs cannot be deleted (R17)")
	}
}

// TestRefetchIssuesARequestNotATail pins R15: the pane offers an explicit refetch and never
// presents the log as live. The refetch key arms a fresh fetch Cmd; there is no tailing loop.
func TestRefetchIssuesARequestNotATail(t *testing.T) {
	m := loaded(t, trivialLog())
	_, cmd, handled := m.HandleKey(press("r"))
	if !handled {
		t.Fatalf("the refetch key was not handled (R15)")
	}
	if cmd == nil {
		t.Fatalf("the refetch key did not arm a fetch (R15)")
	}
	msg := cmd()
	if _, ok := msg.(logFetchedMsg); !ok {
		t.Errorf("the refetch produced %T, want a logFetchedMsg (R15)", msg)
	}
}

// TestExportUsesTheArchiveSeamNotTheRenderPath pins R11 and AC5: the export key triggers the
// injected Exporter, which is a seam distinct from the render fetch. No render path can reach
// the archive, because only the export key calls the Exporter, and the Fetch seam never does.
func TestExportUsesTheArchiveSeamNotTheRenderPath(t *testing.T) {
	var exported bool
	var gotRepo domain.RepoID
	var gotRun int64
	exporter := func(repo domain.RepoID, runID int64) (string, error) {
		exported = true
		gotRepo, gotRun = repo, runID
		return "/tmp/cli-cli-run-4675883901-logs.zip", nil
	}
	m := New(Options{Profile: keys.Standard, Exporter: exporter})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(testRun(), testJob(), writableRepo(), true)
	m = m.onFetched(logFetchedMsg{jobID: testJob().ID, data: trivialLog()})

	_, cmd, handled := m.HandleKey(press("e"))
	if !handled || cmd == nil {
		t.Fatalf("the export key did not trigger the archive export (R11)")
	}
	msg := cmd()
	m = m.updateOnly(msg)
	if !exported {
		t.Fatalf("the export key did not call the Exporter seam (R11)")
	}
	if gotRepo != testRepoID() || gotRun != testRun().ID {
		t.Errorf("the export targeted %v run %d, want the open Run's repository and id (R11)", gotRepo, gotRun)
	}
	if !strings.Contains(m.View(), "Exported") {
		t.Errorf("the pane does not report the export result path (R11):\n%s", m.View())
	}
}

// updateOnly drives one message through Update, dropping the Cmd, for a test that reads the
// resulting view.
func (m Model) updateOnly(msg tea.Msg) Model {
	nm, _ := m.Update(msg)
	return nm
}
