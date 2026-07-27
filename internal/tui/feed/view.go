package feed

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
	"github.com/jv-k/gh-runs/v2/internal/timefmt"
)

// The Feed's column geometry (R4a). A fixed gutter leads the row, four columns are fixed
// and never shrink, and two flex above their floors. The gutter is 2, the four fixed sum
// to 57, five single-space separators make 62, and the two floors give 2 + 57 + 5 + 20 + 16
// = 100, which is minWidth. Below it the Feed paints no rows and states the width it needs
// (AC20). No width truncates a value in either enum, because each column is sized to the
// longest member its enum has (R6a).
//
// The gutter is funded out of workflowFloor rather than added to minWidth, so the 100-column
// golden convention holds and no terminal that painted the Feed before stops doing so. It
// carries row state that belongs to neither Status nor Conclusion: run-lifecycle R7 holds
// those two fields to the API's own values, and a cancellation the tool has requested and the
// API has not yet acted on is a fact about this process, not about either field (R4, AC5).
const (
	minWidth      = 100
	gutterW       = 2  // one marker plus its trailing space; joined without a separator
	statusW       = 11 // in_progress, the longest Status
	conclusionW   = 15 // action_required and startup_failure, the longest Conclusions
	runIDW        = 11 // measured: cli/cli serves 11-digit ids
	startedW      = 20 // measured: 2026-07-15T16:39:00Z
	repoFloor     = 20 // home-assistant/core is 19, kubernetes/kubernetes is 21
	workflowFloor = 16 // chosen: what is left once the gutter is funded
	colSep        = " "

	// cancelRequestedMarker is the gutter's cancellation-requested indicator (R4, AC5). It is
	// the cancel key's own letter, so the mark names the keystroke that produced it, and it is
	// ASCII, so its display width is its rune count and the row arithmetic above holds on any
	// terminal. blankMarker keeps an unmarked row's columns in the same places.
	cancelRequestedMarker = "c"
	blankMarker           = "  "
	truncMarker           = "…"
	startedLayout         = "2006-01-02T15:04:05Z"
	clockLayout           = "15:04"

	// failedNamesShown bounds how many repository names the failed-poll indicator
	// spells out before it falls back to a count. The poll set runs to roughly 26
	// repositories, so an unbounded list would wrap the chrome over several lines and
	// take the rows with it. Two names fit the width and are enough to recognise a
	// single failure, which is the common case.
	failedNamesShown = 2
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden
// over View() is byte-stable on any machine (ADR-0013). Colour distinguishes each
// Status and Conclusion value (R6), and the repository cell's decoration distinguishes
// the four action states the gate reports (R17, R18, R21). Degradation to a poorer
// terminal happens downstream in colorprofile.Writer, never here.
var (
	styleHeader     = lipgloss.NewStyle().Bold(true)
	styleExhausted  = lipgloss.NewStyle().Bold(true).Foreground(palette.Danger)
	stylePressure   = lipgloss.NewStyle().Foreground(palette.Attention)
	styleAffordance = lipgloss.NewStyle().Foreground(palette.Accent)
	styleCapLabel   = lipgloss.NewStyle().Foreground(palette.Muted)
	// styleFailed is the failed-poll indicator. It is deliberately not styleExhausted:
	// exhaustion and a failing repository can be on screen together, and in one weight
	// and one colour they would read as a single condition.
	styleFailed   = lipgloss.NewStyle().Bold(true).Foreground(palette.Failing)
	styleSelected = lipgloss.NewStyle().Foreground(palette.Selected)
	styleDim      = lipgloss.NewStyle().Foreground(palette.Muted)
	// styleBadge is the approvals badge, in the same purple the waiting Status carries, so the
	// count of Runs awaiting a decision reads as the awaiting hue (approvals R8).
	styleBadge = lipgloss.NewStyle().Bold(true).Foreground(palette.Waiting)

	// styleCancelRequested is the gutter's cancellation-requested marker (R4, AC5). It takes
	// the attention hue rather than the danger one: an accepted cancel is a request in flight
	// and an expected state, not a failure, and the Run it marks is still running.
	styleCancelRequested = lipgloss.NewStyle().Bold(true).Foreground(palette.Attention)

	// Action-state decoration on the repository cell, four visibly distinct renderings
	// (R36's third golden). Offered is plain; the three refusals each differ.
	styleOffered   = lipgloss.NewStyle()                     // R17: push and not archived
	styleReadOnly  = lipgloss.NewStyle().Faint(true)         // R20: refused, dimmed
	styleUnknown   = lipgloss.NewStyle().Italic(true)        // R18: permission not yet known
	stylePermanent = lipgloss.NewStyle().Strikethrough(true) // R21: archived, never writable
)

// statusColour maps each known Status to a distinct colour (R6). An unknown Status
// renders in the default foreground, verbatim, never collapsed to a word (R6a).
var statusColour = map[domain.Status]palette.Colour{
	domain.StatusQueued:     palette.Queued,
	domain.StatusInProgress: palette.Accent,
	domain.StatusCompleted:  palette.Muted,
	domain.StatusWaiting:    palette.Waiting,
	domain.StatusRequested:  palette.Requested,
	domain.StatusPending:    palette.Attention,
}

// conclusionColour maps each known Conclusion to a distinct colour (R6).
var conclusionColour = map[domain.Conclusion]palette.Colour{
	domain.ConclusionSuccess:        palette.Passed,
	domain.ConclusionFailure:        palette.Danger,
	domain.ConclusionCancelled:      palette.Muted,
	domain.ConclusionSkipped:        palette.Muted,
	domain.ConclusionTimedOut:       palette.Warn,
	domain.ConclusionNeutral:        palette.Muted,
	domain.ConclusionActionRequired: palette.Attention,
	domain.ConclusionStale:          palette.Muted,
	domain.ConclusionStartupFailure: palette.Danger,
}

// View renders the Feed to a frame from held state alone, with no live terminal and no
// network (R36). Below minWidth it refuses rather than abridging (R4a, AC20).
func (m Model) View() string {
	// The confirmation is a modal: while it is up it replaces the list, so the operator
	// reads the frozen set and the friction rather than the Feed behind it (purge R7).
	// R14's non-modal rule is about the Purge running, not the confirmation gating it.
	if m.confirmOpen {
		return m.confirm.View()
	}
	// The decision pane is a modal too: while it is open it replaces the list, so the operator
	// reads the awaiting Run and its environments rather than the Feed behind it (approvals R11,
	// R12).
	if m.approvalOpen {
		return m.approval.View()
	}
	if m.width < minWidth {
		return m.narrowMessage()
	}

	var top, bottom []string
	if b, ok := m.bannerLine(); ok {
		top = append(top, b)
	}
	if f, ok := m.failedLine(); ok {
		top = append(top, f)
	}
	if a, ok := m.approvalBadgeLine(); ok {
		top = append(top, a)
	}
	if c, ok := m.cancelRequestedLine(); ok {
		top = append(top, c)
	}
	if f, ok := m.filterLine(); ok {
		top = append(top, f)
	}
	if f, ok := m.thisRepoFallbackLine(); ok {
		top = append(top, f)
	}
	if c, ok := m.capLabelLine(); ok {
		top = append(top, c)
	}
	top = append(top, m.headerLine())

	if a, ok := m.affordanceLine(); ok {
		bottom = append(bottom, a)
	}
	if m.filterActive {
		bottom = append(bottom, m.filterInput.View())
	}
	bottom = append(bottom, m.statusLine())
	if m.showHelp {
		bottom = append(bottom, m.helpLine())
	}
	bottom = append(bottom, m.detailBlock()...)

	rows := m.renderRows(m.rowCapacityFor(len(top) + len(bottom)))

	lines := make([]string, 0, len(top)+len(rows)+len(bottom))
	lines = append(lines, top...)
	lines = append(lines, rows...)
	lines = append(lines, bottom...)
	return strings.Join(lines, "\n")
}

// narrowMessage states that the terminal is too narrow and names the width the Feed
// needs, painting no rows (R4a, AC20). The tool's defining bug is conflating Status and
// Conclusion, so it refuses rather than drop one to fit.
func (m Model) narrowMessage() string {
	return fmt.Sprintf(
		"Terminal too narrow: the Feed needs at least %d columns to show Status and Conclusion in full (this terminal is %d).",
		minWidth, m.width)
}

// rowCapacityFor is how many run rows fit given the chrome line count.
func (m Model) rowCapacityFor(chrome int) int {
	if m.height <= 0 {
		return len(m.displayedIDs) // no height yet: show what we hold, deterministically
	}
	n := m.height - chrome
	if n < 0 {
		return 0
	}
	return n
}

// rowCapacity is the viewport height in rows, used by page motion and viewport
// publication. It counts the chrome the current state renders.
func (m Model) rowCapacity() int {
	return m.rowCapacityFor(m.chromeLineCount())
}

// chromeLineCount counts the non-row lines the current state renders, so the row
// capacity and the View agree on the split.
func (m Model) chromeLineCount() int {
	n := 1 // header
	if _, ok := m.bannerLine(); ok {
		n++
	}
	if _, ok := m.failedLine(); ok {
		n++
	}
	if _, ok := m.approvalBadgeLine(); ok {
		n++
	}
	if _, ok := m.cancelRequestedLine(); ok {
		n++
	}
	if _, ok := m.filterLine(); ok {
		n++
	}
	if _, ok := m.thisRepoFallbackLine(); ok {
		n++
	}
	if _, ok := m.capLabelLine(); ok {
		n++
	}
	if _, ok := m.affordanceLine(); ok {
		n++
	}
	if m.filterActive {
		n++
	}
	n++ // status line
	if m.showHelp {
		n++
	}
	n += len(m.detailBlock()) // the detail pane and its divider, when open (stage 8)
	return n
}

// detailBlock is the Run detail pane painted below the list when it is open, preceded by a
// divider (run-detail, BUILD-ORDER stage 8). It is empty while the pane is closed, so the
// list occupies the whole frame and R36's closed-state goldens are unchanged. Its lines
// count against the row capacity through chromeLineCount, so opening the pane shrinks the
// list rather than overrunning the frame.
func (m Model) detailBlock() []string {
	if !m.detailOpen {
		return nil
	}
	view := m.detail.View()
	if view == "" {
		return nil
	}
	rule := m.width
	if rule < 0 {
		rule = 0 // strings.Repeat panics on a negative count; Bubble Tea never emits one (defence in depth)
	}
	block := []string{styleDim.Render(strings.Repeat("─", rule))}
	return append(block, strings.Split(view, "\n")...)
}

// viewportBounds returns the half-open row range the viewport shows, scrolling to keep
// the cursor visible without moving a row a poll did not (R10 governs polls, not the
// cursor's own motion, AC1).
func (m Model) viewportBounds(capRows int) (int, int) {
	n := len(m.displayedIDs)
	if capRows <= 0 || n == 0 {
		return 0, 0
	}
	if n <= capRows {
		return 0, n
	}
	top := m.top
	if top > n-capRows {
		top = n - capRows
	}
	if top < 0 {
		top = 0
	}
	return top, top + capRows
}

// renderRows paints the viewport's run rows, or the one line that says why there are none.
// An empty list is otherwise a blank area under a header, where a filter matching nothing, a
// Feed nothing has arrived in yet and a broken tool all look identical.
func (m Model) renderRows(capRows int) []string {
	if len(m.displayedIDs) == 0 {
		if capRows < 1 {
			return nil
		}
		return []string{m.emptyLine()}
	}
	start, end := m.viewportBounds(capRows)
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		id := m.displayedIDs[i]
		out = append(out, m.renderRow(m.current[id], i == m.cursor, m.selected[id]))
	}
	return out
}

// emptyLine says why the list is empty, distinguishing the two reasons. Under a filter it is
// the filter, and the filter line above already states which one and how to lift it. With no
// filter nothing has arrived yet, which at cold start is the ordinary first frame.
func (m Model) emptyLine() string {
	if m.narrowed() {
		return styleDim.Render("  No Runs match the active filter.")
	}
	return styleDim.Render("  No Runs yet.")
}

// narrowed reports whether anything is hiding Runs the Feed holds: the text filter, or the
// approvals saved filter behind the badge (approvals R9). Both are client-side predicates
// over the held Runs, so either can empty the list while the Feed holds plenty.
func (m Model) narrowed() bool {
	return m.filter.QueryString() != "" || m.approvalsFilter
}

// filterLine states the active filter, so a narrowing the operator did not type is visible
// rather than silent. The Workflows tab's navigation to a deleted Workflow's Orphaned Runs
// arrives that way (workflow-management R13, AC4), and an unstated filter is why an empty
// result reads as a broken tool. It names the keys that edit and clear it, from the registry
// (R7a), and stands down while the input is focused, where the input itself shows the text.
func (m Model) filterLine() (string, bool) {
	if m.filterActive {
		return "", false
	}
	q := m.filter.QueryString()
	if q == "" {
		return "", false
	}
	// A filter's values are not ours: an actor, a branch and a Workflow name are API data any
	// author on a polled repository controls, so the line is sanitised like every run-derived
	// cell. It is clamped for the same reason failedLine is, because chromeLineCount counts it
	// as exactly one line and a commit SHA alone is 47 columns.
	label := fmt.Sprintf("filter %s  (%s edit, then %s clears)",
		q, hint(m.profile.Filter), hint(m.profile.FilterCancel))
	return styleDim.Render(truncPad(textsan.Sanitize(label), m.width)), true
}

// thisRepoFallbackLine states that a stated repo:this-repo resolved to no repository, so the
// axis contributed nothing and the Feed is showing every repository. settings R19 requires
// the fallback be said rather than answered with an empty view or a picker, and a pin that is
// not in force must not read the same as one that is (ADR-0016).
//
// It is its own line rather than a clause on filterLine because it does not fit beside the
// query and the key hints at R4a's 100 columns, and a say-so obligation discharged by a
// message truncated off the end of a line is not discharged. The wording mirrors the
// Workflows tab's scopeLabel: the two are the same fallback on two surfaces, and a reader
// should not have to learn it twice.
//
// It stands down while the input is focused, as filterLine does, because the input shows the
// text being edited rather than the applied filter.
func (m Model) thisRepoFallbackLine() (string, bool) {
	if m.filterActive || !m.filter.ThisRepo {
		return "", false
	}
	if _, ok := m.thisRepo(); ok {
		return "", false
	}
	const label = "repo:this-repo found no repository in this working directory, showing all-repos"
	return styleDim.Render(truncPad(label, m.width)), true
}

// headerLine is the column header, in the fixed order the rows follow.
func (m Model) headerLine() string {
	repoW, workflowW := m.flexWidths()
	cells := []string{
		truncPad("REPOSITORY", repoW),
		truncPad("WORKFLOW", workflowW),
		truncPad("STATUS", statusW),
		truncPad("CONCLUSION", conclusionW),
		truncPad("RUN ID", runIDW),
		truncPad("STARTED", startedW),
	}
	// The gutter carries no heading. It holds row state rather than a field, and a heading
	// over it would name a column the rows do not have.
	return styleHeader.Render(blankMarker + strings.Join(cells, colSep))
}

// renderRow paints one Run in the fixed column order, with Status and Conclusion in
// their own separately styled cells and an empty Conclusion below completed (R4, R5).
// Cursor and selection decorate each cell so the row keeps its exact width (R4a); the
// leading gutter carries run-lifecycle R4's cancellation-requested indicator.
func (m Model) renderRow(r domain.Run, isCursor, isSelected bool) string {
	repoW, workflowW := m.flexWidths()

	decorate := func(base lipgloss.Style) lipgloss.Style {
		s := base
		if isSelected {
			s = s.Bold(true)
		}
		if isCursor {
			s = s.Reverse(true)
		}
		return s
	}

	// Sanitise every run-derived string before it is measured and painted: a workflow or
	// run name is free-form text an author on any polled repository controls, and a C0 or
	// CSI sequence in it would rewrite or spoof the operator's terminal (security review).
	// The colour lookups above read the raw Status and Conclusion, so an unknown value still
	// renders in the default foreground; only the painted text is cleaned. The Run ID and
	// the timestamp are machine-generated and need no cleaning. Sanitising before truncPad
	// keeps the width arithmetic measuring exactly what is drawn.
	repoCell := decorate(m.actionStyle(r.Repo)).Render(truncPad(textsan.Sanitize(repoLabel(r)), repoW))
	workflowCell := decorate(lipgloss.NewStyle()).Render(truncPad(textsan.Sanitize(workflowLabel(r)), workflowW))
	statusCell := decorate(statusStyle(r.Status)).Render(truncPad(textsan.Sanitize(string(r.Status)), statusW))
	conclusionCell := decorate(conclusionStyle(r.Conclusion)).Render(truncPad(textsan.Sanitize(conclusionText(r)), conclusionW))
	runIDCell := decorate(lipgloss.NewStyle()).Render(truncPad(strconv.FormatInt(r.ID, 10), runIDW))
	startedCell := decorate(lipgloss.NewStyle()).Render(truncPad(m.formatStarted(r), startedW))

	// The gutter says a cancel or force-cancel was accepted for this Run and no poll has
	// observed the transition yet (R4, AC5). It is deliberately not a Conclusion and not a
	// Status: both of those cells go on showing exactly what the API last said, which for a
	// Run still running means an empty Conclusion, and the mark disappears the moment a poll
	// moves the Run to completed.
	gutter := blankMarker
	if _, requested := m.cancelRequested[r.ID]; requested {
		gutter = decorate(styleCancelRequested).Render(cancelRequestedMarker) + " "
	}

	return gutter + strings.Join([]string{repoCell, workflowCell, statusCell, conclusionCell, runIDCell, startedCell}, colSep)
}

// flexWidths gives the two flex columns their width at the current terminal width: the
// floors at 100, and the surplus split above them beyond it (R4a).
func (m Model) flexWidths() (repoW, workflowW int) {
	repoW, workflowW = repoFloor, workflowFloor
	if m.width > minWidth {
		extra := m.width - minWidth
		addRepo := extra/2 + extra%2
		repoW += addRepo
		workflowW += extra - addRepo
	}
	return repoW, workflowW
}

// actionStyle is the repository cell's decoration for its recorded capability (R17,
// R18, R21). A repository the enumeration has not reached is not-yet-known, and its
// destructive actions stay disabled, never inferred from the fact that its Runs listed
// (R18). The capability is derived from held domain.Repo data, so this needs no request.
func (m Model) actionStyle(id domain.RepoID) lipgloss.Style {
	repo, ok := m.repos[id.String()]
	if !ok {
		return styleUnknown // R18: permission not yet known
	}
	if repo.Archived {
		return stylePermanent // R21: archived, permanently read-only
	}
	if repo.Capability() == domain.CapabilityPermitted {
		return styleOffered // R17: push and not archived
	}
	return styleReadOnly // R20: refused, but kept live
}

// statusStyle colours a Status cell, defaulting to no colour for an unrecognised value
// so it renders verbatim (R6, R6a).
func statusStyle(s domain.Status) lipgloss.Style {
	if c, ok := statusColour[s]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return lipgloss.NewStyle()
}

// conclusionStyle colours a Conclusion cell, defaulting to no colour for an
// unrecognised value so it renders verbatim (R6, R6a).
func conclusionStyle(c domain.Conclusion) lipgloss.Style {
	if col, ok := conclusionColour[c]; ok {
		return lipgloss.NewStyle().Foreground(col)
	}
	return lipgloss.NewStyle()
}

// conclusionText is the Conclusion cell's text: empty for any Run not completed, because
// Conclusion is null until Status reaches completed (R5). It never substitutes a
// Conclusion-like value in its place.
func conclusionText(r domain.Run) string {
	if r.Status != domain.StatusCompleted {
		return ""
	}
	return string(r.Conclusion)
}

// repoLabel is the owning repository as owner/name (R3).
func repoLabel(r domain.Run) string {
	return repoName(r.Repo)
}

// repoName is how a repository is spelled anywhere the Feed shows one to the operator:
// owner and name, without the host. The host is part of the identity (ADR-0009) and of
// every map key here, but it is not what the rows read, so a second surface spelling it
// differently would read as a second repository.
func repoName(id domain.RepoID) string {
	return id.Owner + "/" + id.Name
}

// workflowLabel is the Run's Workflow name where the join found one, else its run name,
// so a ruleset Run with no Workflow name keeps the column populated (ADR-0014).
func workflowLabel(r domain.Run) string {
	if r.WorkflowName != "" {
		return r.WorkflowName
	}
	return r.Name
}

// formatStarted renders the sort key in the configured format ([settings] R10): the
// measured 20-character instant by default, or its age under the relative form, which R4a
// requires to be narrower and never wider. A zero time renders empty rather than the epoch.
//
// Relative needs a clock, and without one it renders absolute rather than reading the wall
// clock here. A golden builds this model with no clock (R36: held state alone), and a
// renderer that reached for time.Now to cover the gap would make every frame it paints a
// function of when the suite ran.
func (m Model) formatStarted(r domain.Run) string {
	t := r.EffectiveStart()
	if t.IsZero() {
		return ""
	}
	if m.timestamp == TimestampRelative && m.clk != nil {
		return timefmt.Age(m.clk.Now(), t)
	}
	return t.UTC().Format(startedLayout)
}

// bannerLine is the Budget banner. It is silent while consumption is nominal (R29,
// AC14), states pressure when the governor projects it (R29), and on exhaustion states
// that updates have paused, when they resume, and what the Feed is showing and as of
// when (R30, local-store R7). Pressure is the governor's to decide; the Feed reads the
// flag and never re-derives it (R29).
func (m Model) bannerLine() (string, bool) {
	r := m.readout
	switch {
	case r.Exhausted:
		msg := "Updates paused: rate limit reached."
		if !r.Reset.IsZero() {
			msg += " Resumes " + r.Reset.Format(clockLayout) + "."
		} else {
			msg += " Resume time unknown."
		}
		if !m.asOf.IsZero() {
			msg += " Showing data as of " + m.asOf.Format(clockLayout) + "."
		}
		return styleExhausted.Render(msg), true
	case r.Pressure:
		msg := "Budget under pressure."
		if r.Remaining >= 0 {
			msg += fmt.Sprintf(" %s requests remaining", commafy(r.Remaining))
		}
		if !r.Reset.IsZero() {
			msg += ", resets " + r.Reset.Format(clockLayout)
		}
		return stylePressure.Render(msg + "."), true
	default:
		return "", false
	}
}

// approvalBadgeLine is the approvals badge: the count of held Runs awaiting a human decision,
// shown whenever the count exceeds zero and absent when it returns to zero, so it never becomes
// permanent chrome (approvals R8, AC7). Its wording is neutral, Runs awaiting a decision rather
// than Runs the account must act on, because reviewer standing is not knowable from Feed data
// (R10). It names the saved-filter key, so the badge is activatable to narrow the Feed to
// exactly these Runs (R9). The count is held data, not a fresh request (R5, AC4).
func (m Model) approvalBadgeLine() (string, bool) {
	n := m.approvalCount()
	if n == 0 {
		return "", false
	}
	label := fmt.Sprintf("%d %s awaiting a decision (press %s to filter)",
		n, plural(n, "run", "runs"), m.profile.ApprovalsFilter.Help().Key)
	return styleBadge.Render(label), true
}

// cancelRequestedLine explains the gutter's marker while any row carries it (R4, AC5). It
// is what makes a one-character gutter legible: the mark says which rows, and this says what
// the mark means and that the answer has not arrived yet, which is the whole distinction R4
// draws between a request being accepted and a Run being cancelled.
//
// It follows the approvals badge's rule and appears only while it is true, so it never
// becomes permanent chrome. The count is of marked Runs held anywhere, not of marked rows on
// screen: a filter can hide the Run whose cancel is outstanding, and a line that vanished
// with it would report the request as over.
func (m Model) cancelRequestedLine() (string, bool) {
	n := len(m.cancelRequested)
	if n == 0 {
		return "", false
	}
	label := fmt.Sprintf("%s  cancellation requested for %d %s, awaiting the poll that observes it",
		cancelRequestedMarker, n, plural(n, "run", "runs"))
	return styleCancelRequested.Render(label), true
}

// failedLine is the failed-repository indicator, from ADR-0015's RepoPollFailed. It
// is the one thing that distinguishes a repository whose polls are failing from one
// that has simply not answered yet, and it clears when that repository's next poll
// succeeds.
//
// It counts first and names second, bounded at failedNamesShown. The poll set runs to
// roughly 26 repositories, so a wide outage would otherwise paint 26 names across the
// chrome and push the list off screen. The count is the honest figure, which is the
// same shape R24's cap label uses.
//
// Names are sorted, so the line is stable across repaints rather than reordering with
// Go's map iteration.
func (m Model) failedLine() (string, bool) {
	n := len(m.failed)
	if n == 0 {
		return "", false
	}
	names := make([]string, 0, n)
	for _, f := range m.failed {
		names = append(names, f.label)
	}
	sort.Strings(names)

	// One failure has room for its reason, and the reason is the whole difference
	// between a repository that is gone and one whose API had a bad minute. Past one
	// there is no room, so the count carries it and the names identify who.
	label := fmt.Sprintf("%d %s failed to update: ", n, plural(n, "repository", "repositories"))
	if n == 1 {
		label += names[0]
		if why := m.failed[m.failedKeyOf(names[0])].err; why != nil {
			label += " (" + textsan.Sanitize(why.Error()) + ")"
		}
	} else {
		shown := names
		if len(shown) > failedNamesShown {
			shown = shown[:failedNamesShown]
		}
		label += strings.Join(shown, ", ")
		if rest := n - len(shown); rest > 0 {
			label += fmt.Sprintf(" and %d more", rest)
		}
	}
	// The reason is API text and a repository name is API data, so the line is the one
	// piece of chrome whose length is not ours to bound. Clamp it, because
	// chromeLineCount counts this as exactly one line and a wrap would overrun the
	// frame by a row.
	return styleFailed.Render(truncPad(label+".", m.width)), true
}

// failedKeyOf finds the host-qualified key whose label is name. The map is keyed by
// identity and the line is drawn in label order, so the single-failure case needs the
// one step back. The map holds one entry per repository in a poll set of roughly 26,
// so the scan is not worth an index.
func (m Model) failedKeyOf(name string) string {
	for k, f := range m.failed {
		if f.label == name {
			return k
		}
	}
	return ""
}

// capLabelLine is R24's honest cap label. It shows the reachable count first and the
// claimed count second, marked approximate, and never total_count alone. A merged view
// names how many repositories are capped; a one-repository view degenerates to the pair;
// nothing capped carries no label. The counts are held data, not a fresh request.
func (m Model) capLabelLine() (string, bool) {
	var reachable, claimed, capped int
	for _, t := range m.totals {
		reachable += t.reachable
		claimed += t.claimed
		if t.claimed > t.reachable {
			capped++
		}
	}
	if capped == 0 {
		return "", false
	}
	label := fmt.Sprintf("%s of ~%s", commafy(reachable), commafy(claimed))
	if len(m.totals) > 1 {
		label += fmt.Sprintf(", %d repos capped", capped)
	}
	return styleCapLabel.Render(label), true
}

// affordanceLine states what is deferred and the key that applies it, counting each
// kind under its own word and never folding them into one number (R11). The
// insertion-only case reads exactly "N new runs", the copy R36's golden fixes.
func (m Model) affordanceLine() (string, bool) {
	if !m.pending.any() {
		return "", false
	}
	var parts []string
	if m.pending.added > 0 {
		parts = append(parts, fmt.Sprintf("%d new %s", m.pending.added, plural(m.pending.added, "run", "runs")))
	}
	if m.pending.removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", m.pending.removed))
	}
	if m.pending.moved > 0 {
		parts = append(parts, fmt.Sprintf("%d moved", m.pending.moved))
	}
	text := strings.Join(parts, ", ") + fmt.Sprintf(" (press %s to apply)", m.profile.Refresh.Help().Key)
	return styleAffordance.Render(text), true
}

// statusLine is the persistent footer. The selected count is always shown when the
// selection is non-empty, independent of the active filter and scroll position, so an
// off-filter selection is never invisible (R13a). It also names the profile and the
// key hints, drawn from the registry (R7a).
func (m Model) statusLine() string {
	var left string
	if n := m.selectedCount(); n > 0 {
		left = styleSelected.Render(fmt.Sprintf("%d selected", n)) + "  "
	}
	hints := strings.Join([]string{
		hint(m.profile.RowDown) + "/" + hint(m.profile.RowUp) + " move",
		hint(m.profile.ToggleSelect) + " select",
		hint(m.profile.Filter) + " filter",
		hint(m.profile.Refresh) + " refresh",
		hint(m.profile.Help) + " help",
		hint(m.profile.Quit) + " quit",
	}, "  ")
	right := styleDim.Render(m.profile.Name)
	line := left + styleDim.Render(hints) + "  " + right
	return line
}

// helpLine renders the full binding set from the active profile's registry (R7a). It is
// one line of key hints, so bubbles/help's KeyMap is not needed to satisfy the '?'
// affordance at this stage.
func (m Model) helpLine() string {
	bindings := m.profile.Bindings()
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		h := b.Help()
		if h.Key == "" {
			continue
		}
		parts = append(parts, h.Key+" "+h.Desc)
	}
	return styleDim.Render(strings.Join(parts, "  "))
}

// hint is a binding's display key from the registry, so no view names a key literal of
// its own (R7a).
func hint(b key.Binding) string { return b.Help().Key }

// truncPad fits s to exactly w display columns: it right-pads a short value with spaces,
// and truncates a long one to its leading characters plus a marker (R6a, AC21). A known
// enum value never reaches truncation, because each column is sized to its enum's
// longest member; only a value the API invented after we shipped can, and it is marked
// rather than renamed or dropped. Width is measured by rune count, which equals display
// width for the ASCII the enums, ids and timestamps use.
func truncPad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) > w {
		if w == 1 {
			return truncMarker
		}
		return string(runes[:w-1]) + truncMarker
	}
	if len(runes) < w {
		return s + strings.Repeat(" ", w-len(runes))
	}
	return s
}

// commafy renders a non-negative integer with thousands separators, the form R24's
// label uses ("18,258").
func commafy(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// plural picks the singular or plural word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
