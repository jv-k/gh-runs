package workflows

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Column geometry for the Workflow list (R1). REPOSITORY, PATH and STATE are fixed; NAME
// flexes above a floor; ACTION is the free tail carrying the offered verb or the reason no
// action is (R6, R9, R11). STATE is wide enough for disabled_inactivity, the longest observed
// value, so the three disabled states render in full and distinct (R2).
const (
	repoW    = 18
	pathW    = 26
	stateW   = 20 // "disabled_inactivity" is 19, plus a column space
	actionW  = 10 // reserved in the flex calc; the ACTION cell itself is the free tail
	nameMin  = 16
	colSep   = "  "
	minWidth = 100 // lead 2 + the fixed columns, their separators and NAME's floor
	trunc    = "…"
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over
// View() is byte-stable on any machine (ADR-0013).
var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleActive   = lipgloss.NewStyle().Foreground(lipgloss.Color("#87d787"))
	styleDisabled = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf00"))
	styleDeleted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
	styleCursor   = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fafff"))
	styleAction   = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fafd7"))
	styleOk       = lipgloss.NewStyle().Foreground(lipgloss.Color("#87d787"))
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
)

// View renders the tab from held state alone, with no live terminal and no network. The
// column header names the Workflow's STATE and never its Status, and no Run or Job vocabulary
// appears, because State belongs to a Workflow and Status to a Run (R4, AC6).
func (m Model) View() string {
	if len(m.workflows) == 0 && len(m.errs) == 0 {
		return styleDim.Render("Workflows: press r to load the Workflows across your repositories.")
	}
	var lines []string
	lines = append(lines, m.summaryLine())
	if m.status != "" {
		style := styleOk
		if m.statusErr {
			style = styleErr
		}
		lines = append(lines, style.Render("  "+textsan.Sanitize(m.status)))
	}
	lines = append(lines, "")
	lines = append(lines, m.listLines()...)
	if hint := m.hintLine(); hint != "" {
		lines = append(lines, "")
		lines = append(lines, styleDim.Render(hint))
	}
	return strings.Join(lines, "\n")
}

// summaryLine states the scope and the state tallies, leading with the deleted count when any
// exist because a deleted Workflow's Runs are Orphaned Runs, the cruft this surface exists to
// find (R11). It uses the API's state vocabulary and never the word Status (R4).
func (m Model) summaryLine() string {
	active, disabled, deleted, other := m.stateCounts()
	scope := strconv.Itoa(len(m.order)) + " repositories"
	if len(m.order) == 1 {
		scope = textsan.Sanitize(m.order[0].Owner + "/" + m.order[0].Name)
	}
	parts := []string{
		styleTitle.Render("Workflows") + " " + styleDim.Render(scope),
		styleActive.Render(strconv.Itoa(active)) + styleDim.Render(" active"),
		styleDisabled.Render(strconv.Itoa(disabled)) + styleDim.Render(" disabled"),
	}
	if deleted > 0 {
		parts = append(parts, styleDeleted.Render(strconv.Itoa(deleted))+styleDim.Render(" deleted, Runs orphaned"))
	}
	if other > 0 {
		parts = append(parts, styleDim.Render(strconv.Itoa(other)+" other"))
	}
	line := strings.Join(parts, styleDim.Render("   "))
	if label := m.incompleteLabel(); label != "" {
		line += "\n" + styleDim.Render("  "+label)
	}
	return line
}

// stateCounts tallies the in-scope Workflows by state category, keeping the three disabled
// states summed into one count for the headline while the list keeps them distinct (R2, R10).
// An unrecognised state falls to other rather than to active (R3).
func (m Model) stateCounts() (active, disabled, deleted, other int) {
	for _, id := range m.order {
		for _, w := range m.workflows[id.String()] {
			switch w.State {
			case domain.StateActive:
				active++
			case domain.StateDeleted:
				deleted++
			case domain.StateDisabledManually, domain.StateDisabledInactivity, domain.StateDisabledFork:
				disabled++
			default:
				other++
			}
		}
	}
	return active, disabled, deleted, other
}

// incompleteLabel is honest that the list is not the whole story where a repository's
// enumeration was truncated or could not be read, so a partial name-to-id map is never
// presented as complete (R1, R7).
func (m Model) incompleteLabel() string {
	var incomplete, failed int
	for _, id := range m.order {
		k := id.String()
		if m.errs[k] != nil {
			failed++
			continue
		}
		if !m.complete[k] {
			incomplete++
		}
	}
	switch {
	case failed > 0 && incomplete > 0:
		return "the list could not be read in " + strconv.Itoa(failed) + " repos and is partial in " + strconv.Itoa(incomplete)
	case failed > 0:
		return strconv.Itoa(failed) + " repos could not be read; a 403 despite push is a permission outcome (R7)"
	case incomplete > 0:
		return "the list is partial in " + strconv.Itoa(incomplete) + " repos; more Workflows exist than shown"
	default:
		return ""
	}
}

// listLines is the cross-repository Workflow list (R1), a header and the windowed rows.
func (m Model) listLines() []string {
	rows := m.displayRows()
	out := []string{m.listHeader()}
	if len(rows) == 0 {
		out = append(out, styleDim.Render("  no Workflows in scope"))
		return out
	}
	capacity := m.listCapacity()
	end := m.top + capacity
	if end > len(rows) {
		end = len(rows)
	}
	for i := m.top; i < end; i++ {
		out = append(out, m.listRow(rows[i], i == m.cursor))
	}
	return out
}

// listHeader labels the list's columns. STATE is the Workflow's own vocabulary, never Status
// (R4, AC6).
func (m Model) listHeader() string {
	cells := []string{
		pad("REPOSITORY", repoW),
		pad("NAME", m.nameWidth()),
		pad("PATH", pathW),
		pad("STATE", stateW),
		"ACTION",
	}
	return styleHeader.Render("  " + strings.Join(cells, colSep))
}

// listRow renders one row: the owning repository (R0, R1), the Workflow's name and path
// (sanitised, R1), its state rendered verbatim and coloured by category (R2, R3), and the
// action offered or the reason none is (R6, R9, R11). The name and path are untrusted, an
// author on a fanned-out repository controls them, so they pass through textsan before paint.
func (m Model) listRow(r wfRow, cursor bool) string {
	lead := "  "
	if cursor {
		lead = styleCursor.Render("> ")
	}
	repo := textsan.Sanitize(r.repo.Owner + "/" + r.repo.Name)
	cells := []string{
		pad(repo, repoW),
		pad(textsan.Sanitize(r.wf.Name), m.nameWidth()),
		pad(textsan.Sanitize(r.wf.Path), pathW),
		m.stateStyle(r.wf.State).Render(pad(string(r.wf.State), stateW)),
		m.actionCell(m.actionFor(r)),
	}
	return lead + strings.Join(cells, colSep)
}

// stateStyle colours a state by category, keeping the three disabled states one colour while
// the text keeps them distinct (R2). An unrecognised state is dimmed and rendered verbatim
// rather than coerced to a known one (R3).
func (m Model) stateStyle(s domain.State) lipgloss.Style {
	switch s {
	case domain.StateActive:
		return styleActive
	case domain.StateDeleted:
		return styleDeleted
	case domain.StateDisabledManually, domain.StateDisabledInactivity, domain.StateDisabledFork:
		return styleDisabled
	default:
		return styleDim
	}
}

// actionCell renders the ACTION column: the offered verb when a toggle is available, else the
// reason none is, distinct between an archived repository (permanent), a read-only one (might
// change), and a deleted Workflow whose Runs are Orphaned (R6, R9, R11).
func (m Model) actionCell(act rowAction) string {
	if act.offered {
		return styleAction.Render(act.label)
	}
	switch act.label {
	case "orphaned Runs":
		return styleDeleted.Render(textsan.Sanitize(act.label))
	case "archived":
		return styleDeleted.Render(act.label)
	default:
		return styleDim.Render(act.label)
	}
}

// hintLine names the keys the tab acts on, drawn from the registry so it advertises exactly
// what it matches (R7a, AC18).
func (m Model) hintLine() string {
	tog := m.profile.ToggleWorkflow.Help()
	return "  " + tog.Key + " " + tog.Desc + "   " + m.profile.Refresh.Help().Key + " refresh"
}

// nameWidth is the flex NAME column: the width less the fixed columns, their separators and
// the tail, floored so a narrow terminal still lays out.
func (m Model) nameWidth() int {
	fixed := 2 + repoW + pathW + stateW + actionW + 4*len(colSep)
	w := m.contentWidth() - fixed
	if w < nameMin {
		return nameMin
	}
	return w
}

func (m Model) contentWidth() int {
	if m.width < minWidth {
		return minWidth
	}
	return m.width
}

// listCapacity is the number of list rows the viewport shows, the height less the summary,
// the optional status and incomplete lines, the list header and the hint. It floors at one so
// a tiny terminal still pages.
func (m Model) listCapacity() int {
	chrome := 1 // summary
	if m.incompleteLabel() != "" {
		chrome++
	}
	if m.status != "" {
		chrome++
	}
	chrome += 1 // blank before the list
	chrome += 1 // list header
	chrome += 2 // blank + hint
	n := m.height - chrome
	if n < 1 {
		return 1
	}
	return n
}

// actionVerb is the lowercase verb for a toggle op, for the status line's reason.
func actionVerb(op ops.Operation) string {
	if op == ops.OpEnable {
		return "enable"
	}
	return "disable"
}

// actionPast is the past-tense verb for a toggle op, for the status line's confirmation.
func actionPast(op ops.Operation) string {
	if op == ops.OpEnable {
		return "Enabled"
	}
	return "Disabled"
}

// pad right-pads or truncates s to exactly w columns; lpad is unused here. Widths are rune
// counts, which equal display width for the ASCII the states, ids and paths use.
func pad(s string, w int) string {
	r := []rune(s)
	switch {
	case len(r) > w:
		if w <= 1 {
			return trunc
		}
		return string(r[:w-1]) + trunc
	case len(r) < w:
		return s + strings.Repeat(" ", w-len(r))
	default:
		return s
	}
}
