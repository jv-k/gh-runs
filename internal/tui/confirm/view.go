package confirm

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Column geometry for the R30 inspect table, sized to the longest member of each
// enum so no known value truncates (live-run-feed R3, purge R30). STATUS holds
// in_progress; CONCLUSION holds action_required and startup_failure; STARTED holds a
// full timestamp. REPO and WORKFLOW flex within a floor.
const (
	repoW       = 22
	idW         = 12
	statusW     = 11
	conclusionW = 15
	startedW    = 16
	workflowMin = 10
	colSep      = "  "
	truncMarker = "…"
	minWidth    = 60
	startLayout = "2006-01-02 15:04"
	indent      = "  "
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden
// over View() is byte-stable on any machine (ADR-0013).
var (
	styleTitle   = lipgloss.NewStyle().Bold(true)
	styleWarn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff875f"))
	stylePrompt  = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleTyped   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00afff"))
	styleArchive = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
)

// View renders the pane from held state alone, with no live terminal and no network
// (R30). It is empty while closed. It shows R30's inspect viewport when inspecting,
// otherwise the confirmation modal.
func (m Model) View() string {
	if !m.open {
		return ""
	}
	if m.inspecting {
		return m.inspectView()
	}
	return m.modalView()
}

// modalView renders R6's count and breakdown, R11's eligibility split, R7's friction
// prompt, and R30's inspect affordance naming the key the registry gives it.
func (m Model) modalView() string {
	var lines []string
	lines = append(lines, styleTitle.Render(m.headline()))
	lines = append(lines, "")
	lines = append(lines, m.breakdownLines()...)
	if split := m.eligibilityLines(); len(split) > 0 {
		lines = append(lines, "")
		lines = append(lines, split...)
	}
	lines = append(lines, "")
	lines = append(lines, m.promptLine())
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(m.inspectHint()))
	return strings.Join(lines, "\n")
}

// headline is R6's count with the operation and the noun: "Delete 47 Runs across 3
// repositories", or "in owner/name" for a single-repository set.
func (m Model) headline() string {
	verb := operationVerb(m.plan.Operation())
	noun := pluralNoun(m.plan)
	total := m.plan.Total()
	bd := m.plan.Breakdown()
	if len(bd) == 1 {
		return verb + " " + strconv.Itoa(total) + " " + noun + " in " + textsan.Sanitize(bd[0].Repo.Owner+"/"+bd[0].Repo.Name)
	}
	return verb + " " + strconv.Itoa(total) + " " + noun + " across " + strconv.Itoa(len(bd)) + " repositories"
}

// breakdownLines is R6's per-repository breakdown, whose counts sum to the total
// (AC1). Each row names the repository and its count, and its skipped share when any.
func (m Model) breakdownLines() []string {
	out := make([]string, 0, len(m.plan.Breakdown()))
	for _, rc := range m.plan.Breakdown() {
		repo := textsan.Sanitize(rc.Repo.Owner + "/" + rc.Repo.Name)
		line := indent + truncPad(repo, repoW) + colSep + strconv.Itoa(rc.Count)
		if rc.Skipped > 0 {
			line += styleDim.Render(" (" + strconv.Itoa(rc.Skipped) + " skipped)")
		}
		out = append(out, line)
	}
	return out
}

// eligibilityLines is R11's split, stated before the Purge starts and distinguishing
// archived from merely read-only, because archived is permanent (R11, AC15). Each
// reason present gets its own line, phrased as R11 words it.
func (m Model) eligibilityLines() []string {
	var readOnly, archived, notCompleted int
	for _, it := range m.plan.Items() {
		switch it.Skip {
		case ops.SkipReadOnly:
			readOnly++
		case ops.SkipArchived:
			archived++
		case ops.SkipNotCompleted:
			notCompleted++
		}
	}
	total := m.plan.Total()
	noun := pluralNoun(m.plan)
	var out []string
	if readOnly > 0 {
		out = append(out, styleWarn.Render(indent+strconv.Itoa(readOnly)+" of "+strconv.Itoa(total)+" selected "+noun+" are in read-only repos and will be skipped"))
	}
	if archived > 0 {
		out = append(out, styleArchive.Render(indent+strconv.Itoa(archived)+" of "+strconv.Itoa(total)+" are in archived repos and can never be cleaned"))
	}
	if notCompleted > 0 {
		out = append(out, styleWarn.Render(indent+strconv.Itoa(notCompleted)+" of "+strconv.Itoa(total)+" are still running and will be skipped"))
	}
	return out
}

// promptLine is R7's friction prompt: y/N below the threshold, and the exact typed
// count at or above it or across repositories. The typed buffer is echoed so the
// operator sees what they have entered (R7, AC6, AC7).
func (m Model) promptLine() string {
	if m.plan.Friction() == ops.FrictionTypedCount {
		prompt := stylePrompt.Render(indent + "Type " + strconv.Itoa(m.plan.Total()) + " to confirm: ")
		return prompt + styleTyped.Render(m.typed)
	}
	return stylePrompt.Render(indent + "Delete these " + pluralNoun(m.plan) + "? [y/N]")
}

// inspectHint names the key that opens R30's viewport, drawn from the registry so the
// modal advertises exactly the binding the pane matches (R30, AC18). It also names the
// abort keys.
func (m Model) inspectHint() string {
	return indent + "Press " + m.profile.ConfirmInspect.Help().Key + " to inspect the frozen set.  " +
		m.profile.ConfirmAbort.Help().Key + " to cancel."
}

// inspectView is R30's viewport over the frozen set: the Feed's columns and no new
// ones, one row each, paged to reach both ends (R30, AC22). It issues no request; the
// rows are the same tuples Execute is handed.
func (m Model) inspectView() string {
	items := m.plan.Items()
	var lines []string
	lines = append(lines, styleTitle.Render("Frozen set: "+strconv.Itoa(len(items))+" "+pluralNoun(m.plan)))
	lines = append(lines, styleHeader.Render(m.inspectHeader()))
	rows := m.inspectPage()
	end := m.top + rows
	if end > len(items) {
		end = len(items)
	}
	for i := m.top; i < end; i++ {
		lines = append(lines, m.inspectRow(items[i], i == m.cursor))
	}
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(indent+"row "+strconv.Itoa(m.cursor+1)+" of "+strconv.Itoa(len(items))+".  "+
		m.profile.ConfirmInspect.Help().Key+"/"+m.profile.ConfirmAbort.Help().Key+" to return."))
	return strings.Join(lines, "\n")
}

// inspectHeader labels the columns in the Feed's order (live-run-feed R3, purge R30).
func (m Model) inspectHeader() string {
	cells := []string{
		truncPad("REPOSITORY", repoW),
		truncPad("RUN ID", idW),
		truncPad("STATUS", statusW),
		truncPad("CONCLUSION", conclusionW),
		truncPad("WORKFLOW", m.workflowWidth()),
		truncPad("STARTED", startedW),
	}
	return "  " + strings.Join(cells, colSep)
}

// inspectRow renders one Item's cells. The type switch on Kind is the one per-Kind
// fact a shared component owns (ADR-0019): a Run row prints Status, Conclusion and
// the Workflow, and the other Kinds print what they carry. Conclusion is empty on any
// row whose Status is not completed (R30, AC22). Untrusted text is sanitised.
func (m Model) inspectRow(it ops.Item, cursor bool) string {
	repo := textsan.Sanitize(it.Repo.Owner + "/" + it.Repo.Name)
	var status, conclusion, workflow, started string
	switch {
	case it.Run != nil:
		r := it.Run
		status = string(r.Status)
		if r.Status == domain.StatusCompleted {
			conclusion = string(r.Conclusion)
		}
		workflow = workflowLabel(r)
		if !r.EffectiveStart().IsZero() {
			started = r.EffectiveStart().UTC().Format(startLayout)
		}
	case it.Cache != nil:
		workflow = it.Cache.Key
	case it.Artifact != nil:
		workflow = it.Artifact.Name
	}
	cells := []string{
		truncPad(repo, repoW),
		truncPad(strconv.FormatInt(it.ID, 10), idW),
		truncPad(textsan.Sanitize(status), statusW),
		truncPad(textsan.Sanitize(conclusion), conclusionW),
		truncPad(textsan.Sanitize(workflow), m.workflowWidth()),
		truncPad(started, startedW),
	}
	marker := "  "
	if cursor {
		marker = "> "
	}
	return marker + strings.Join(cells, colSep)
}

// inspectPage is the number of Item rows the viewport shows, leaving lines for the
// title, header and footer. It floors at one so a tiny terminal still pages.
func (m Model) inspectPage() int {
	rows := m.height - 4
	if rows < 1 {
		return 1
	}
	return rows
}

// workflowWidth is the flex WORKFLOW column: the width less the fixed columns and
// their separators, floored.
func (m Model) workflowWidth() int {
	w := m.contentWidth() - repoW - idW - statusW - conclusionW - startedW - 6*len(colSep)
	if w < workflowMin {
		return workflowMin
	}
	return w
}

func (m Model) contentWidth() int {
	if m.width < minWidth {
		return minWidth
	}
	return m.width
}

// operationVerb is the human verb for the operation, capitalised for a headline.
func operationVerb(op ops.Operation) string {
	switch op {
	case ops.OpDelete:
		return "Delete"
	case ops.OpCancel:
		return "Cancel"
	case ops.OpForceCancel:
		return "Force-cancel"
	case ops.OpRerun:
		return "Re-run"
	case ops.OpRerunFailed:
		return "Re-run failed jobs of"
	default:
		return string(op)
	}
}

// pluralNoun is the noun for the set's Kind, "items" when the set mixes Kinds. A
// storage-reclamation deletion of Caches and Artifacts is one mixed set (R15), so the
// mixed case is real rather than defensive.
func pluralNoun(p ops.Plan) string {
	items := p.Items()
	if len(items) == 0 {
		return "items"
	}
	kind := items[0].Kind
	for _, it := range items {
		if it.Kind != kind {
			return "items"
		}
	}
	switch kind {
	case ops.KindRun:
		return "Runs"
	case ops.KindCache:
		return "Caches"
	case ops.KindArtifact:
		return "Artifacts"
	case ops.KindLog:
		return "logs"
	default:
		return "items"
	}
}

// workflowLabel is the Run's Workflow name, falling back to its run name where the
// join found none, so a ruleset Run keeps a populated cell (ADR-0014).
func workflowLabel(r *domain.Run) string {
	if r.WorkflowName != "" {
		return r.WorkflowName
	}
	return r.Name
}

// truncPad fits s to exactly w columns, right-padding a short value and truncating a
// long one with a marker. Width is rune count, which equals display width for the
// ASCII the ids, enums and timestamps use.
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
