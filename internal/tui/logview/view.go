package logview

import (
	"errors"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// errNoExporter is the export result when no Exporter seam is wired (a golden pane, or before
// main.go fills it). It reports that export is unavailable rather than panicking.
var errNoExporter = errors.New("log export is not available")

// Geometry and glyphs. The fold arrows carry R5's collapse state in a shape, and R9's text
// carries it again in the marker labels below, so the distinction survives without colour.
const (
	minWidth       = 40
	foldCollapsed  = "▸"
	foldExpanded   = "▾"
	foldBodyIndent = "  "
	chromeLines    = 4 // header, a blank, and two footer lines the body lays out around
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over
// View() is byte-stable on any machine (ADR-0013). NO_COLOR is honoured by rendering plain
// (R10): the styles below are dropped, and the text labels R9 carries keep every distinction
// legible, which is what AC4 asserts.
var (
	styleDefault = lipgloss.NewStyle()
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleFold    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00afff"))
	styleError   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f5f"))
	styleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff875f"))
	styleNotice  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d7af"))
	styleCommand = lipgloss.NewStyle().Foreground(lipgloss.Color("#5faf5f"))
	styleDebug   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	stylePrompt  = lipgloss.NewStyle().Bold(true)
	styleWarn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff875f"))
	cursorBg     = lipgloss.Color("#303030")
	matchBg      = lipgloss.Color("#5f5f00")
)

// visKind distinguishes a fold header, which the cursor toggles, from an ordinary content
// line.
type visKind int

const (
	visContent visKind = iota
	visFoldHeader
)

// visLine is one visible logical line: the full display text with its gutter, the searchable
// content without the gutter, and the style to paint it in. Both the text and the search
// string are drawn from the log body sanitizeParsed already cleaned and tab-expanded (R19), so
// composing a visLine adds no per-frame sanitising.
type visLine struct {
	kind      visKind
	foldIndex int // index into m.log.blocks when kind is visFoldHeader, else -1
	text      string
	search    string
	style     lipgloss.Style
}

// View renders the pane from held content alone, with no live terminal and no network (R20).
// It is empty while closed. It shows the delete confirmation when that is open (R17),
// otherwise the header, the log body in its load state, and the footer.
func (m Model) View() string {
	if !m.open {
		return ""
	}
	if m.confirmOpen {
		return m.deleteView()
	}
	var lines []string
	lines = append(lines, m.headerLine())
	lines = append(lines, "")
	switch m.state {
	case stateLoading:
		// R14: an indeterminate indicator, never a percentage or a predicted size, because the
		// size cannot be known before the download arrives.
		lines = append(lines, m.paint(styleDim, "Downloading log. The size is not known until it arrives."))
	case stateEmpty:
		// R18: one empty state, whatever the cause (in progress, no content, or already deleted).
		lines = append(lines, m.paint(styleDim, "No log content."))
	default:
		lines = append(lines, m.body())
	}
	lines = append(lines, "")
	lines = append(lines, m.footer())
	return strings.Join(lines, "\n")
}

// paint renders s in st, or returns it plain when NO_COLOR is set, so the whole pane suppresses
// colour together (R10). The body's per-line rendering makes the same choice in renderVisLine,
// so no styled byte escapes the NO_COLOR path.
func (m Model) paint(st lipgloss.Style, s string) string {
	if m.noColor {
		return s
	}
	return st.Render(s)
}

// headerLine names the Run and Job the log belongs to, sanitised because the repository,
// Workflow and Job names are author-controlled on any polled repository and a terminal escape
// in one would rewrite the operator's screen (R19). It appends the Attempt badge, because a
// log is only ever the latest Attempt's (R16), and the search prompt while a search is typed.
func (m Model) headerLine() string {
	repo := textsan.Sanitize(m.run.Repo.Owner + "/" + m.run.Repo.Name)
	wf := textsan.Sanitize(workflowLabel(m.run))
	job := textsan.Sanitize(m.job.Name)
	head := m.paint(styleHeader, repo+idSep+wf+idSep+"Job: "+job)
	if m.run.RunAttempt > 1 {
		head += "  " + m.paint(styleDim, "Attempt "+itoa(m.run.RunAttempt))
	}
	if m.searching {
		head += "\n" + m.paint(stylePrompt, "/"+textsan.Sanitize(m.searchInput))
	}
	return head
}

const idSep = " · "

// body renders the visible logical lines into the viewport, wrapping each to the pane width
// (R22). The lines carry the log body sanitizeParsed already cleaned (R19). The cursor line
// and any search match are marked, and the viewport shows a window of physical rows from the
// scroll top.
func (m Model) body() string {
	lines := m.visibleLines()
	if len(lines) == 0 {
		return m.paint(styleDim, "No log content.")
	}
	capacity := m.bodyCapacity()
	matchSet := m.matchSet()
	var rows []string
	for i := m.top; i < len(lines) && len(rows) < capacity; i++ {
		_, isMatch := matchSet[i]
		for _, r := range m.renderVisLine(lines[i], i == m.cursor, isMatch) {
			rows = append(rows, r)
			if len(rows) >= capacity {
				break
			}
		}
	}
	return strings.Join(rows, "\n")
}

// renderVisLine wraps one visible line to the content width and paints each physical row in
// the line's style (R22). NO_COLOR drops the style to plain, leaving R9's text labels to
// carry the meaning (R10). The cursor line and a search match get a background, so the
// fold-toggle target and the matches are visible without shifting the layout.
func (m Model) renderVisLine(l visLine, isCursor, isMatch bool) []string {
	st := l.style
	if m.noColor {
		st = styleDefault
	} else if isCursor {
		st = st.Background(cursorBg)
	} else if isMatch {
		st = st.Background(matchBg)
	}
	frags := wrapPlain(l.text, m.contentWidth())
	rows := make([]string, len(frags))
	for i, f := range frags {
		rows[i] = st.Render(f)
	}
	return rows
}

// footer is the status line, the search summary, and the key hints. The status carries the
// export's result path (R11); the search summary reports the match position (R21).
func (m Model) footer() string {
	var parts []string
	if m.status != "" {
		parts = append(parts, m.paint(styleDim, textsan.Sanitize(m.status)))
	}
	if m.searchQuery != "" && !m.searching {
		summary := "no matches for " + textsan.Sanitize(m.searchQuery)
		if len(m.matches) > 0 {
			summary = "match " + itoa(m.matchIdx+1) + " of " + itoa(len(m.matches)) + " for " + textsan.Sanitize(m.searchQuery)
		}
		parts = append(parts, m.paint(styleDim, summary+"  (n/N to move)"))
	}
	parts = append(parts, m.paint(styleDim, "t timestamps · space fold · / search · D delete logs · e export · r refetch · esc close"))
	return strings.Join(parts, "\n")
}

// deleteView renders the log-deletion confirmation (R17): the wording that names what dies
// and what survives, which no generic confirmation draws, above the reused graduated
// confirmation modal, which renders unchanged. "The logs die and the Run lives" is the
// distinction R17 exists to make on the operator's face at the moment of the decision.
func (m Model) deleteView() string {
	var lines []string
	lines = append(lines, m.paint(styleWarn, "Delete the logs of Run #"+itoa(m.run.RunNumber)+"?"))
	lines = append(lines, m.paint(styleDim, "Only the logs are destroyed. The Run and its metadata survive and stay listable in the Feed."))
	lines = append(lines, "")
	lines = append(lines, m.confirm.View())
	return strings.Join(lines, "\n")
}

// visibleLines derives the visible logical lines from the parsed blocks, the fold state and
// the timestamp toggle (R4, R5). A collapsed fold contributes only its labelled header; an
// expanded one contributes its header and its body. Every content string it composes was
// already sanitised and tab-expanded by sanitizeParsed when the fetch landed (R19), so this
// runs on every frame without re-sanitising the log.
func (m Model) visibleLines() []visLine {
	var out []visLine
	for bi := range m.log.blocks {
		b := m.log.blocks[bi]
		if b.isFold {
			arrow := foldCollapsed
			if b.fold.expanded {
				arrow = foldExpanded
			}
			search := m.linePrefix(b.fold.prefix) + b.fold.label
			out = append(out, visLine{
				kind:      visFoldHeader,
				foldIndex: bi,
				text:      arrow + " " + search,
				search:    search,
				style:     styleFold,
			})
			if b.fold.expanded {
				for _, l := range b.fold.body {
					out = append(out, m.contentLine(l, foldBodyIndent))
				}
			}
			continue
		}
		out = append(out, m.contentLine(b.line, ""))
	}
	return out
}

// contentLine builds a visible content line: its gutter, the timestamp prefix when timestamps
// are on (R4), R9's text label for a marker, and the content sanitizeParsed already cleaned
// (R19). The label is in both the display text and the search string, so a search for
// "warning" finds a warning line and the label survives without colour (R9, R10, AC4).
func (m Model) contentLine(l logLine, gutter string) visLine {
	label, style := markerLabelAndStyle(l.marker)
	search := m.linePrefix(l.prefix) + label + l.text
	return visLine{
		kind:      visContent,
		foldIndex: -1,
		text:      gutter + search,
		search:    search,
		style:     style,
	}
}

// linePrefix is the timestamp prefix a line shows, empty by default and the byte-identical
// prefix when timestamps are toggled on (R4). sanitizeParsed already sanitised the prefix when
// the fetch landed, defence in depth over an API-generated value, so nothing is re-sanitised
// here (R19).
func (m Model) linePrefix(prefix string) string {
	if !m.showTimestamps {
		return ""
	}
	return prefix
}

// matchSet is the set of visible-line indices that are search matches, for the body's
// highlighting.
func (m Model) matchSet() map[int]bool {
	if len(m.matches) == 0 {
		return nil
	}
	set := make(map[int]bool, len(m.matches))
	for _, i := range m.matches {
		set[i] = true
	}
	return set
}

// bodyCapacity is the number of physical rows the log body may fill, the height less the
// header, a blank, and the footer. It floors at one so a tiny terminal still paints a row.
func (m Model) bodyCapacity() int {
	rows := m.height - chromeLines
	if m.searching {
		rows-- // the search prompt takes a second header row
	}
	if rows < 1 {
		return 1
	}
	return rows
}

// contentWidth is the width the pane wraps to, floored so the wrap arithmetic never goes
// non-positive before the opener has sent a size.
func (m Model) contentWidth() int {
	if m.width < minWidth {
		return minWidth
	}
	return m.width
}

// markerLabelAndStyle maps a marker kind to R9's text label and R7's style. The labels are
// v1's SKIP/GOOD/FAIL choice made deliberate: an uppercase word carries the distinction in
// text, so it survives NO_COLOR (R9, R10), and the style carries it again in colour (R7). An
// ordinary line has no label and the default style.
func markerLabelAndStyle(m markerKind) (string, lipgloss.Style) {
	switch m {
	case markerError:
		return "ERROR   ", styleError
	case markerWarning:
		return "WARNING ", styleWarning
	case markerNotice:
		return "NOTICE  ", styleNotice
	case markerCommand:
		return "COMMAND ", styleCommand
	case markerDebug:
		return "DEBUG   ", styleDebug
	default:
		return "", styleDefault
	}
}

// wrapPlain hard-wraps a string to width by rune count, so no character is hidden, truncated
// or pushed off a horizontal scroll surface (R22, AC11). Wrapping by rune count equals display
// width for the ASCII logs are overwhelmingly made of. A continuation row starts at column
// zero, which keeps every character visible, which is all R22 requires.
func wrapPlain(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	runes := []rune(s)
	if len(runes) <= width {
		return []string{s}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	return append(out, string(runes))
}

// workflowLabel is the Run's Workflow name, falling back to its run name where the join found
// none, so a ruleset Run keeps a populated identity (ADR-0014).
func workflowLabel(r domain.Run) string {
	if r.WorkflowName != "" {
		return r.WorkflowName
	}
	return r.Name
}

// noColour reports whether NO_COLOR is set in the environment, read once at construction
// because it is a process-wide fact (R10). An empty value means colour is on, matching the
// NO_COLOR convention that any non-empty value disables colour.
func noColour() bool {
	return os.Getenv("NO_COLOR") != ""
}
