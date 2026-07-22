package rundetail

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Column geometry for the Jobs table. STATUS and CONCLUSION are each sized to the longest
// member of their enum, so no known value is ever truncated (R2, R3): in_progress is the
// longest Status, action_required and startup_failure the longest Conclusions. ELAPSED
// holds up to "23h59m59s". JOB / STEP flexes above a floor into whatever the terminal
// gives. A value the API invents after we ship is marked rather than renamed, never
// collapsed to a word.
const (
	statusW     = 11
	conclusionW = 15
	elapsedW    = 9
	nameFloor   = 10
	colSep      = " "
	truncMarker = "…"
	minContentW = 40
	identityPad = 28 // room the identity line leaves for the Run number and the Attempt badge
	stepIndent  = "    "
	clockLayout = "15:04"
	partSep     = "   "
	idSep       = " · "
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over
// View() is byte-stable on any machine (ADR-0013). The Attempt badge is styled to stand
// out, because it carries the most weight of anything the pane paints: after a re-run it is
// the only evidence on screen that anything happened (R17, R19).
var (
	styleIdentity  = lipgloss.NewStyle().Bold(true)
	styleRunNumber = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffaf00"))
	styleHeader    = lipgloss.NewStyle().Bold(true)
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleStepName  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleDeleted   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f5f"))
	stylePaused    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f5f"))
)

// statusColour maps each known Status to a distinct colour (R6's principle, applied in the
// pane). An unknown Status renders in the default foreground, verbatim.
var statusColour = map[domain.Status]string{
	domain.StatusQueued:     "#d7af00",
	domain.StatusInProgress: "#00afff",
	domain.StatusCompleted:  "#8a8a8a",
	domain.StatusWaiting:    "#af87ff",
	domain.StatusRequested:  "#00d7af",
	domain.StatusPending:    "#ffaf00",
}

// conclusionColour maps each known Conclusion to a distinct colour.
var conclusionColour = map[domain.Conclusion]string{
	domain.ConclusionSuccess:        "#5faf5f",
	domain.ConclusionFailure:        "#ff5f5f",
	domain.ConclusionCancelled:      "#8a8a8a",
	domain.ConclusionSkipped:        "#8a8a8a",
	domain.ConclusionTimedOut:       "#ff875f",
	domain.ConclusionNeutral:        "#8a8a8a",
	domain.ConclusionActionRequired: "#ffaf00",
	domain.ConclusionStale:          "#8a8a8a",
	domain.ConclusionStartupFailure: "#ff5f5f",
}

// View renders the pane to a frame from held state alone, with no live terminal and no
// network (R19). It is empty until a Run is selected; the Feed paints it only while it is
// open, so an empty frame is never shown.
func (m Model) View() string {
	if !m.haveRun {
		return ""
	}
	var lines []string
	lines = append(lines, m.identityLine())

	// The deleted marker sits against the Run's identity, so a Run with no possible
	// successor is distinguishable from one whose Workflow still exists (R8, AC11).
	if m.wfState == domain.StateDeleted {
		lines = append(lines, styleDeleted.Render("Workflow deleted. This Run has no possible successor."))
	}
	// The pause banner states that refreshing has stopped and when it resumes, so held Jobs
	// are not read as live (R16, AC12).
	if m.readout.Exhausted {
		lines = append(lines, m.pausedLine())
	}

	lines = append(lines, m.runStatusLine())

	// previous_attempt_url is prior-Attempt metadata only. The pane says so plainly and
	// offers no navigation, because prior Attempts' Jobs are not served (R5, R7).
	if m.run.RunAttempt > 1 {
		lines = append(lines, styleDim.Render("Prior attempts are not retrievable."))
	}

	lines = append(lines, "")
	lines = append(lines, m.body()...)
	return strings.Join(lines, "\n")
}

// identityLine is the Run's identity with the Attempt badge attached to it, never inside
// the Jobs list where it would read as a property of a Job (R4, AC3). The badge renders
// only when the Run has more than one Attempt. The repository and Workflow text is
// sanitised, because a Workflow name is author-controlled on any polled repository and a
// terminal escape in it would rewrite the operator's screen (security review).
func (m Model) identityLine() string {
	r := m.run
	label := truncate(textsan.Sanitize(repoLabel(r))+idSep+textsan.Sanitize(workflowLabel(r)), m.contentWidth()-identityPad)
	parts := []string{
		styleIdentity.Render(label),
		styleRunNumber.Render("Run #" + strconv.Itoa(r.RunNumber)),
	}
	if r.RunAttempt > 1 {
		parts = append(parts, styleBadge.Render("Attempt "+strconv.Itoa(r.RunAttempt)))
	}
	return strings.Join(parts, partSep)
}

// runStatusLine renders the Run's own Status and Conclusion as two separate fields, with an
// empty Conclusion until the Status reaches completed, so the split that R2 and R3 fix for
// Jobs and Steps holds at the Run level too (R3's note: the split runs Run, Job and Step
// alike). No field renders a Status value and a Conclusion value together (AC5).
func (m Model) runStatusLine() string {
	st := statusStyle(m.run.Status).Render(textsan.Sanitize(string(m.run.Status)))
	cc := concludedText(m.run.Status, m.run.Conclusion)
	if cc == "" {
		return st
	}
	return st + "  " + conclusionStyle(m.run.Conclusion).Render(textsan.Sanitize(cc))
}

// pausedLine states the pause and its resume instant, matching the Feed's exhaustion banner
// so the two surfaces read the same on one Budget (R16, live-run-feed R30).
func (m Model) pausedLine() string {
	msg := "Updates paused: rate limit reached."
	if m.readout.Reset.IsZero() {
		msg += " Resume time unknown."
	} else {
		msg += " Resumes " + m.readout.Reset.Format(clockLayout) + "."
	}
	return stylePaused.Render(msg)
}

// body is the Jobs area, one of three states. Pending is the explicit "loading" R12
// requires on a selection change, never the previous Run's Jobs. No-Jobs is R12a's uniform
// empty state, reached alike from an empty list, a partial one or an error (AC14). Loaded
// is the Jobs with their Steps (R1).
func (m Model) body() []string {
	switch m.state {
	case statePending:
		return []string{styleDim.Render("Loading jobs…")}
	case stateNoJobs:
		return []string{styleDim.Render("No jobs yet.")}
	default:
		return m.jobLines()
	}
}

// jobLines renders the Jobs table: a header, then each Job with its Steps indented beneath
// it (R1). Steps arrive inline on the Job, at no extra request (resolved open question 1).
func (m Model) jobLines() []string {
	nameW := m.nameWidth()
	out := make([]string, 0, 1+len(m.jobs)*4)
	out = append(out, m.jobsHeader(nameW))
	for i := range m.jobs {
		j := m.jobs[i]
		out = append(out, m.jobRow(j, nameW))
		for k := range j.Steps {
			out = append(out, m.stepRow(j.Steps[k], nameW))
		}
	}
	return out
}

// jobsHeader labels the four columns in the fixed order the rows follow.
func (m Model) jobsHeader(nameW int) string {
	cells := []string{
		truncPad("JOB / STEP", nameW),
		truncPad("STATUS", statusW),
		truncPad("CONCLUSION", conclusionW),
		truncPad("ELAPSED", elapsedW),
	}
	return styleHeader.Render(strings.Join(cells, colSep))
}

// jobRow paints one Job with its Status and Conclusion in their own separately styled cells
// and an empty Conclusion until it is completed (R2, AC5). The Job name is sanitised.
func (m Model) jobRow(j domain.Job, nameW int) string {
	cells := []string{
		truncPad(textsan.Sanitize(j.Name), nameW),
		statusStyle(j.Status).Render(truncPad(textsan.Sanitize(string(j.Status)), statusW)),
		conclusionStyle(j.Conclusion).Render(truncPad(textsan.Sanitize(concludedText(j.Status, j.Conclusion)), conclusionW)),
		styleDim.Render(truncPad(m.elapsed(j.Status, j.StartedAt, j.CompletedAt), elapsedW)),
	}
	return strings.Join(cells, colSep)
}

// stepRow paints one Step indented under its Job, numbered, with the same Status and
// Conclusion split and the same empty-until-completed rule (R3, AC5). The Step name is
// sanitised; its number and indent are the pane's own and need none.
func (m Model) stepRow(s domain.Step, nameW int) string {
	label := stepIndent + strconv.Itoa(s.Number) + " " + textsan.Sanitize(s.Name)
	cells := []string{
		styleStepName.Render(truncPad(label, nameW)),
		statusStyle(s.Status).Render(truncPad(textsan.Sanitize(string(s.Status)), statusW)),
		conclusionStyle(s.Conclusion).Render(truncPad(textsan.Sanitize(concludedText(s.Status, s.Conclusion)), conclusionW)),
		styleDim.Render(truncPad(m.elapsed(s.Status, s.StartedAt, s.CompletedAt), elapsedW)),
	}
	return strings.Join(cells, colSep)
}

// elapsed is the timing column, read from the injected clock so a golden is deterministic.
// A completed unit shows its wall duration; a live one with a start shows how long it has
// been running; a unit that has not started shows nothing (run-detail Purpose: timing).
func (m Model) elapsed(status domain.Status, started, completed time.Time) string {
	if started.IsZero() {
		return ""
	}
	if status == domain.StatusCompleted && !completed.IsZero() {
		return formatDuration(completed.Sub(started))
	}
	return formatDuration(m.clk.Now().Sub(started))
}

// formatDuration renders a duration compactly, dropping the leading zero units: "1m4s",
// "58s", "1h1m1s". A non-positive duration renders empty rather than "0s".
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	mnt := d / time.Minute
	d -= mnt * time.Minute
	s := d / time.Second
	switch {
	case h > 0:
		return strconv.Itoa(int(h)) + "h" + strconv.Itoa(int(mnt)) + "m" + strconv.Itoa(int(s)) + "s"
	case mnt > 0:
		return strconv.Itoa(int(mnt)) + "m" + strconv.Itoa(int(s)) + "s"
	default:
		return strconv.Itoa(int(s)) + "s"
	}
}

// concludedText is a Status/Conclusion pair's Conclusion cell: empty until the Status
// reaches completed, because Conclusion is null until then, for a Job and a Step as for a
// Run (R2, R3). It never substitutes a Conclusion-like value in an empty cell's place.
func concludedText(status domain.Status, c domain.Conclusion) string {
	if status != domain.StatusCompleted {
		return ""
	}
	return string(c)
}

// statusStyle colours a Status cell, defaulting to the plain foreground for an unrecognised
// value so it renders verbatim rather than being collapsed to a word.
func statusStyle(s domain.Status) lipgloss.Style {
	if c, ok := statusColour[s]; ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return lipgloss.NewStyle()
}

// conclusionStyle colours a Conclusion cell, defaulting to the plain foreground for an
// unrecognised value.
func conclusionStyle(c domain.Conclusion) lipgloss.Style {
	if col, ok := conclusionColour[c]; ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(col))
	}
	return lipgloss.NewStyle()
}

// repoLabel is the owning repository as owner/name.
func repoLabel(r domain.Run) string { return r.Repo.Owner + "/" + r.Repo.Name }

// workflowLabel is the Run's Workflow name where the join found one, else its run name, so
// a ruleset Run with no Workflow name keeps the identity populated (ADR-0014).
func workflowLabel(r domain.Run) string {
	if r.WorkflowName != "" {
		return r.WorkflowName
	}
	return r.Name
}

// contentWidth is the width the pane renders to, floored so the column arithmetic never
// goes negative before the Feed has sent a size.
func (m Model) contentWidth() int {
	if m.width < minContentW {
		return minContentW
	}
	return m.width
}

// nameWidth is the flex JOB / STEP column: the content width less the three fixed columns
// and their separators, floored so a very narrow terminal still paints a name cell.
func (m Model) nameWidth() int {
	w := m.contentWidth() - statusW - conclusionW - elapsedW - 3*len(colSep)
	if w < nameFloor {
		return nameFloor
	}
	return w
}

// truncPad fits s to exactly w display columns, right-padding a short value and truncating
// a long one to its leading characters plus a marker. Width is measured by rune count,
// which equals display width for the ASCII the enums, numbers and durations use.
func truncPad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	runes := []rune(s)
	switch {
	case len(runes) > w:
		if w == 1 {
			return truncMarker
		}
		return string(runes[:w-1]) + truncMarker
	case len(runes) < w:
		return s + strings.Repeat(" ", w-len(runes))
	default:
		return s
	}
}

// truncate caps s to w display columns with a marker, without padding, for a header line
// that is not a fixed-width cell.
func truncate(s string, w int) string {
	if w < 1 {
		w = 1
	}
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w == 1 {
		return truncMarker
	}
	return string(runes[:w-1]) + truncMarker
}
