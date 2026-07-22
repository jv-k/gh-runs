// Package logview is the pane that views a single Job's log and deletes a Run's logs. It
// opens from Run detail over a selected Job, fetches that Job's plain text on open (R1), and
// renders it folded, de-noised and legible without leaving for a browser. It also deletes the
// Run's logs, an operation distinct from deleting the Run and routed through the Purge's
// graduated confirmation (R17).
//
// It is a pane, not a tab and not a tea.Model. It exposes View() string and an Update and a
// HandleKey its opener drives, and it is imported by rundetail, which opens it over a Job
// (ADR-0011's pane contract). It imports the confirm pane it opens over its one-log frozen
// set, which is the one import direction the pane contract permits, and the ops engine that
// freezes and executes that set. It never imports rundetail (its opener), the feed, or a tab
// (ADR-0011, BUILD-ORDER: "logview cannot reach back up to rundetail").
//
// Logs are the most attacker-controlled text in the tool: a Workflow author prints arbitrary
// bytes into them, ANSI escapes and control bytes included. Every line painted to the
// terminal is sanitised through internal/textsan before rendering (R19), because an
// unsanitised line rewrites or spoofs the operator's terminal, and in a tool whose headline
// is that deletion is one operation, a spoofable view is a decision-integrity failure, not a
// cosmetic one. The parse is a pure transformation a golden pins (R20); the sanitising is at
// paint time, over the content the author controls.
package logview

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
)

// Planner freezes a selection into an ops.Plan: the shared entry to the confirmation chain
// (ADR-0019). *ops.Ops satisfies it. It is a narrow interface so the pane depends on the one
// call it makes, and a golden test with no planner leaves it nil, where the delete key is
// inert (the destructive action stays disabled until the planner and the discovered
// capability data are wired, repo-discovery R8).
type Planner interface {
	Plan(op ops.Operation, sel []ops.Item, repos map[domain.RepoID]domain.Repo) (ops.Plan, error)
}

// Options carries the pane's construction seams, filled by main.go through rundetail. Fetch
// reads one Job's log over the shared client, Exporter downloads the whole-Run archive to
// disk (R11), Planner freezes the log-deletion set (R17), and Profile is the resolved
// keybinding set (R7a). A golden test leaves the seams nil, where the pane renders held
// content and the delete and export keys are inert.
type Options struct {
	Profile  keys.Profile
	Fetch    Fetch
	Exporter Exporter
	Planner  Planner
	Clock    clock.Clock
}

// loadState is where the pane is in fetching the Job's log. An error and an empty body both
// resolve to stateEmpty, so the pane renders one empty state whatever the cause: an
// in-progress Job, a Job that emitted nothing, or logs already deleted (R18, AC7).
type loadState int

const (
	stateLoading loadState = iota // fetch issued, first response not in yet (R14)
	stateLoaded                   // content present and parsed (R1)
	stateEmpty                    // empty body or an error, rendered as the empty state (R18)
)

// logFetchedMsg carries a fetch result, tagged with the Job it was issued for so a result
// for a Job the pane has moved off is discarded.
type logFetchedMsg struct {
	jobID int64
	data  []byte
	err   error
}

// exportDoneMsg carries the archive export's result: the path written, or the error (R11).
type exportDoneMsg struct {
	path string
	err  error
}

// Model is the pane's state. It holds the Run and Job it opened over, the parsed log, the
// view-session toggles (timestamps and per-fold expansion are mechanism, not settings, R4),
// the search state, and the delete confirmation. It holds no client: the fetch and export
// run off injected seams, and the frozen delete set is one LogItem.
type Model struct {
	profile  keys.Profile
	fetch    Fetch
	exporter Exporter
	planner  Planner

	open    bool
	width   int
	height  int
	noColor bool // NO_COLOR is set: suppress colour, staying legible on text alone (R9, R10)

	run       domain.Run
	job       domain.Job
	repo      domain.Repo
	repoKnown bool // the repository's capability is discovered; the delete gate fails closed otherwise

	state loadState
	log   parsed

	showTimestamps bool // R4: off by default, toggled on to restore the ISO prefix

	cursor int // index into the visible logical lines, the fold-toggle target
	top    int // first visible logical line, scrolled to keep the cursor on screen

	// search is R21's in-log find over the fetched content, issuing no request. Entering it
	// expands every fold so every match is visible and reachable, then n and N move between
	// matches.
	searching   bool   // the search input holds focus
	searchQuery string // the accepted query, empty when no search is active
	searchInput string // the text being typed while searching
	matches     []int  // visible-line indices that contain the query
	matchIdx    int    // which match is current

	// confirm is the graduated-confirmation pane, opened over the one-log frozen set on the
	// delete key (R17, ADR-0011: a pane may open the confirm pane). planner freezes the set
	// into the Plan it renders. While it is open the pane routes every key to it.
	confirm     confirm.Model
	confirmOpen bool

	status string // a transient line, e.g. the export's result path (R11)
}

// New returns a closed pane over opts. It holds no Run and fetches nothing until Open. NO_COLOR
// is read once here, because it is a process-wide environment fact, not a per-frame one.
func New(opts Options) Model {
	return Model{
		profile:  opts.Profile,
		fetch:    opts.Fetch,
		exporter: opts.Exporter,
		planner:  opts.Planner,
		noColor:  noColour(),
		confirm:  confirm.New(opts.Profile),
	}
}

// Open shows the pane over one Job of a Run and issues exactly one fetch for that Job's log
// (R1, AC1). repo is the Run's repository capability, gating log deletion (R17); known is
// false when discovery has not recorded it, which keeps the delete disabled (repo-discovery
// R8). Reopening resets the view session: timestamps off, folds collapsed, no search, cursor
// at the top (R4, R5).
func (m Model) Open(run domain.Run, job domain.Job, repo domain.Repo, known bool) (Model, tea.Cmd) {
	m.open = true
	m.run = run
	m.job = job
	m.repo = repo
	m.repoKnown = known
	m.state = stateLoading
	m.log = parsed{}
	m.showTimestamps = false
	m.cursor, m.top = 0, 0
	m.resetSearch()
	m.status = ""
	m.confirmOpen = false
	return m, m.fetchCmd(job.ID)
}

// Close hides the pane. The opener calls it when the operator backs out. It leaves the held
// content in place; IsOpen going false is what stops it being painted.
func (m Model) Close() Model {
	m.open = false
	return m
}

// IsOpen reports whether the pane is showing, which the opener reads to paint it and route
// keys to it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// CapturesInput reports whether the pane holds text-input focus, which is true while the
// search input is open or the delete confirmation is up. The opener and the root read it up
// the chain so a typed search query or a typed confirmation count is not stolen as a global
// key (R7, R21).
func (m Model) CapturesInput() bool { return m.searching || m.confirmOpen }

// Update handles one broadcast the opener forwarded: the size it lays out against, and the
// tagged fetch and export results. Keys arrive through HandleKey, not here, because the pane
// is reached by recursive focus rather than by the broadcast route (ADR-0011).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.confirm, _ = m.confirm.Update(msg)
		return m, nil
	case logFetchedMsg:
		return m.onFetched(msg), nil
	case exportDoneMsg:
		if msg.err != nil {
			m.status = "Export failed: " + msg.err.Error()
		} else {
			m.status = "Exported the whole-Run archive to " + msg.path
		}
		return m, nil
	}
	return m, nil
}

// onFetched consumes a fetch result. A result for a Job the pane has since moved off is
// discarded. An empty body or an error both resolve to the empty state (R18, AC7); otherwise
// the bytes are parsed into the foldable model (R1). The signed URL is not retained: the
// bytes are, the URL is not (R13).
func (m Model) onFetched(msg logFetchedMsg) Model {
	if !m.open || msg.jobID != m.job.ID {
		return m
	}
	if msg.err != nil || len(msg.data) == 0 {
		m.log = parsed{}
		m.state = stateEmpty
		return m
	}
	m.log = parseLog(msg.data)
	m.state = stateLoaded
	m.cursor, m.top = 0, 0
	m.resetSearch()
	return m
}

// HandleKey drives the pane while it holds key focus, returning whether it consumed the key.
// A key the pane does not own is left for the opener, but the opener swallows unconsumed keys
// while this pane captures focus, so nothing here needs to worry about what falls through.
// The delete confirmation and the search input are modal: while either is up, every key
// reaches it (R7, R21).
func (m Model) HandleKey(k tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	if m.confirmOpen {
		return m.handleConfirmKey(k)
	}
	if m.searching {
		return m.handleSearchKey(k)
	}
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc closes the log, back to the Job list
		return m.Close(), nil, true
	case key.Matches(k, m.profile.LogTimestamps):
		m.showTimestamps = !m.showTimestamps // R4: view-session toggle, not a setting
		return m, nil, true
	case key.Matches(k, m.profile.ToggleSelect): // space toggles the fold at the cursor (R5)
		m.toggleFoldAtCursor()
		return m, nil, true
	case key.Matches(k, m.profile.Filter): // '/' opens the in-log search (R21)
		m.searching = true
		m.searchInput = m.searchQuery
		return m, nil, true
	case key.Matches(k, m.profile.LogNextMatch):
		m.moveMatch(1)
		return m, nil, true
	case key.Matches(k, m.profile.LogPrevMatch):
		m.moveMatch(-1)
		return m, nil, true
	case key.Matches(k, m.profile.Refresh): // R15: an explicit refetch, never a live tail
		return m, m.fetchCmd(m.job.ID), true
	case key.Matches(k, m.profile.LogExport): // R11: download the whole-Run archive
		return m, m.exportCmd(), true
	case key.Matches(k, m.profile.LogDelete): // R17: delete the Run's logs, distinct from the Run
		return m.openDelete(), nil, true
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
		return m, nil, true
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
		return m, nil, true
	case key.Matches(k, m.profile.PageUp):
		m.moveCursor(-m.pageSize())
		return m, nil, true
	case key.Matches(k, m.profile.PageDown):
		m.moveCursor(m.pageSize())
		return m, nil, true
	case key.Matches(k, m.profile.FirstRow):
		m.cursor, m.top = 0, 0
		return m, nil, true
	case key.Matches(k, m.profile.LastRow):
		m.cursor = m.visibleCount() - 1
		m.clampCursor()
		return m, nil, true
	}
	return m, nil, false
}

// handleConfirmKey drives the delete confirmation while it is open, routing every key to the
// pane and acting on its Outcome. An abort dismisses it having issued nothing (AC6); a
// confirmation closes it holding the confirmed Plan, and launching Execute over that Plan is
// the running surface this stage defers, exactly as the Feed defers launching a Purge and the
// Storage tab a reclamation from a confirmed Plan (ADR-0011, ADR-0015). The single Execute
// mutation entry is proved end to end at the ops layer against a cassette (log-viewer R17).
func (m Model) handleConfirmKey(k tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	switch m.confirm.Outcome() {
	case confirm.Aborted:
		m.confirm = m.confirm.Close()
		m.confirmOpen = false
	case confirm.Confirmed:
		m.confirm = m.confirm.Close()
		m.confirmOpen = false
		m.status = "Log deletion confirmed for Run #" + itoa(m.run.RunNumber) + "."
	}
	return m, cmd, true
}

// handleSearchKey drives the search input (R21). Enter accepts the query and jumps to the
// first match; esc cancels it and restores the view; backspace trims; a printable key
// extends it. The search runs over the fetched content in memory and issues no request.
func (m Model) handleSearchKey(k tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch {
	case key.Matches(k, m.profile.FilterAccept): // enter
		m.applySearch(m.searchInput)
		m.searching = false
		return m, nil, true
	case key.Matches(k, m.profile.FilterCancel): // esc
		m.searching = false
		m.searchInput = ""
		return m, nil, true
	case k.String() == "backspace":
		if n := len(m.searchInput); n > 0 {
			m.searchInput = m.searchInput[:n-1]
		}
		return m, nil, true
	}
	if s := k.String(); len(s) == 1 {
		m.searchInput += s
	}
	return m, nil, true
}

// openDelete freezes the Run's logs into a one-item Plan and opens the confirmation over it
// (R17). The set is one LogItem carrying the Run's id (ADR-0019), and the gate is the
// repository's push-and-not-archived capability: with no planner, an unknown repository, or a
// repository where the whole set is ineligible, no modal opens and no request can follow,
// keeping the destructive action disabled (repo-discovery R8, R17, AC6). It never deletes the
// Run: the Item's Kind is log, so Execute resolves the logs endpoint, not the Run's (AC6).
func (m Model) openDelete() Model {
	if m.planner == nil {
		return m
	}
	sel := []ops.Item{ops.LogItem(m.run)}
	plan, err := m.planner.Plan(ops.OpDelete, sel, m.repoSnapshot())
	if err != nil {
		return m // fail closed on an unknown repository (repo-discovery R8, ADR-0019)
	}
	if plan.Skipped() == plan.Total() {
		m.status = "This repository is read-only, so its logs cannot be deleted."
		return m // every row ineligible: no delete offered, no request issued (R17)
	}
	m.confirm = m.confirm.Open(plan)
	m.confirmOpen = true
	return m
}

// repoSnapshot is the eligibility map Plan takes: the one repository the Run belongs to, when
// its capability is known, and empty otherwise so Plan fails closed (repo-discovery R8,
// ADR-0019). Passing an empty map for an unknown repository makes Plan return an error rather
// than guess, which is the fail-closed behaviour the delete gate wants.
func (m Model) repoSnapshot() map[domain.RepoID]domain.Repo {
	if !m.repoKnown {
		return map[domain.RepoID]domain.Repo{}
	}
	return map[domain.RepoID]domain.Repo{m.run.Repo: m.repo}
}

// fetchCmd runs the injected Fetch off the Update loop and tags the result with the Job id
// (R1). A nil Fetch resolves to the empty state rather than panicking, which is what a pane
// built for a golden with no transport holds.
func (m Model) fetchCmd(jobID int64) tea.Cmd {
	fetch := m.fetch
	repo := m.run.Repo
	if fetch == nil {
		return func() tea.Msg { return logFetchedMsg{jobID: jobID} }
	}
	return func() tea.Msg {
		data, err := fetch(repo, jobID)
		return logFetchedMsg{jobID: jobID, data: data, err: err}
	}
}

// exportCmd runs the injected Exporter off the Update loop (R11). A nil Exporter reports that
// export is unavailable rather than panicking, which is the golden path.
func (m Model) exportCmd() tea.Cmd {
	exporter := m.exporter
	repo, runID := m.run.Repo, m.run.ID
	if exporter == nil {
		return func() tea.Msg { return exportDoneMsg{err: errNoExporter} }
	}
	return func() tea.Msg {
		path, err := exporter(repo, runID)
		return exportDoneMsg{path: path, err: err}
	}
}

// toggleFoldAtCursor expands or re-collapses the fold under the cursor (R5). It is a no-op
// when the cursor is not on a fold header. The fold state is copied on write, so the change
// does not alias a prior model value.
func (m *Model) toggleFoldAtCursor() {
	lines := m.visibleLines()
	if m.cursor < 0 || m.cursor >= len(lines) {
		return
	}
	l := lines[m.cursor]
	if l.kind != visFoldHeader {
		return
	}
	blocks := append([]block(nil), m.log.blocks...)
	blocks[l.foldIndex].fold.expanded = !blocks[l.foldIndex].fold.expanded
	m.log.blocks = blocks
}

// applySearch accepts a query, expands every fold so each match is visible and reachable, and
// jumps to the first match (R21). An empty query clears the search. The search issues no
// request: it runs over the visible lines derived from the fetched content (AC10).
func (m *Model) applySearch(query string) {
	m.searchQuery = query
	if query == "" {
		m.resetSearch()
		return
	}
	m.expandAllFolds() // every match is visible, so none hides in a collapsed fold (R21)
	m.recomputeMatches()
	m.matchIdx = 0
	if len(m.matches) > 0 {
		m.cursor = m.matches[0]
		m.clampCursor()
	}
}

// recomputeMatches finds every visible line whose text contains the query, case-insensitively
// (R21). The search is over the fetched content in memory and issues no request (AC10).
func (m *Model) recomputeMatches() {
	m.matches = nil
	if m.searchQuery == "" {
		return
	}
	needle := strings.ToLower(m.searchQuery)
	for i, l := range m.visibleLines() {
		if strings.Contains(strings.ToLower(l.search), needle) {
			m.matches = append(m.matches, i)
		}
	}
}

// moveMatch advances the current match by delta and scrolls to it (R21, AC10). It is a no-op
// with no matches. The index wraps, so n past the last match returns to the first.
func (m *Model) moveMatch(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.matchIdx = (m.matchIdx + delta + len(m.matches)) % len(m.matches)
	m.cursor = m.matches[m.matchIdx]
	m.clampCursor()
}

// resetSearch clears the search state.
func (m *Model) resetSearch() {
	m.searching = false
	m.searchQuery = ""
	m.searchInput = ""
	m.matches = nil
	m.matchIdx = 0
}

// expandAllFolds expands every fold, copying the blocks on write so the change does not alias
// a prior model value. It is how a search reveals matches inside folds (R21).
func (m *Model) expandAllFolds() {
	blocks := append([]block(nil), m.log.blocks...)
	for i := range blocks {
		if blocks[i].isFold {
			blocks[i].fold.expanded = true
		}
	}
	m.log.blocks = blocks
}

// moveCursor moves the cursor by delta, clamps it, and scrolls the viewport to keep it
// visible.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
}

// clampCursor keeps the cursor within the visible lines and scrolls the viewport to it.
func (m *Model) clampCursor() {
	n := m.visibleCount()
	if n == 0 {
		m.cursor, m.top = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	m.scrollToCursor()
}

// scrollToCursor adjusts top so the cursor is on screen. It approximates each logical line as
// one row for the scroll arithmetic; a wrapped line may leave the cursor a row from the edge,
// which is a cosmetic slack a log viewer tolerates.
func (m *Model) scrollToCursor() {
	rows := m.bodyCapacity()
	if rows <= 0 {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// visibleCount is the number of visible logical lines, the cursor's range.
func (m Model) visibleCount() int { return len(m.visibleLines()) }

// pageSize is the number of lines a page-motion key moves.
func (m Model) pageSize() int {
	if n := m.bodyCapacity(); n > 1 {
		return n
	}
	return 1
}

// itoa renders a non-negative int without importing strconv at every call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
