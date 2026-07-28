package confirm

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/palette"
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
	styleWarn    = lipgloss.NewStyle().Bold(true).Foreground(palette.Warn)
	stylePrompt  = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(palette.Muted)
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleTyped   = lipgloss.NewStyle().Bold(true).Foreground(palette.Accent)
	styleArchive = lipgloss.NewStyle().Foreground(palette.Danger)
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
	if note, ok := m.unreachedLine(); ok {
		lines = append(lines, "")
		lines = append(lines, note)
	}
	lines = append(lines, "")
	lines = append(lines, m.promptLine())
	if offer, ok := m.escalationLine(); ok {
		lines = append(lines, offer)
	}
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(m.inspectHint()))
	return strings.Join(lines, "\n")
}

// escalationLine is run-lifecycle R5 and R6's offer of force-cancel, shown on a cancel
// modal alone. It states what the escalation is for, in the words R5 uses about a Run
// that is not cancelable, and names the key from the registry so the modal advertises
// exactly the binding the pane matches (R7a, AC18).
//
// It is an offer and never a substitution (R6). The operator presses the key, the opener
// re-prices the same frozen set as a force-cancel, and the graduated confirmation runs
// again in front of the harder verb.
func (m Model) escalationLine() (string, bool) {
	if !m.offersEscalation() {
		return "", false
	}
	return styleDim.Render(indent + "A Run that is not cancelable needs force-cancel: press " +
		m.profile.ForceCancel.Help().Key + " to escalate."), true
}

// unreachedLine is run-lifecycle R17a's one-line non-blocking note, shown where a by-name
// resolution did not reach every selected Run. The frozen count above it is what resolution
// resolved, which is smaller than the set the operator named, and R7's ladder has no rung
// that expresses "this count is a lower bound". So the note names how many were not reached
// and why, on R14b's precedent: it does not block and it does not confirm.
//
// It is a note rather than a refusal because the resolved portion is a real set the operator
// can act on, and discarding it would throw away work the resolution already paid for.
//
// The reason carries the API's own words, which a hostile third-party repository can
// influence, so it is sanitised at the terminal boundary like every other untrusted string
// this pane renders.
func (m Model) unreachedLine() (string, bool) {
	if m.unreached <= 0 {
		return "", false
	}
	noun := "runs"
	if m.unreached == 1 {
		noun = "run"
	}
	line := indent + strconv.Itoa(m.unreached) + " selected " + noun + " were not reached, so they are not in this set"
	if m.unreachedWhy != "" {
		line += ": " + textsan.Sanitize(m.unreachedWhy)
	}
	return styleWarn.Render(line), true
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
	// The Item-less members get R11's treatment too, because they are the same thing to an
	// operator reading this modal: members of the count above that nothing will be written
	// for. Their reason is authored by the resolution rather than derived from the
	// eligibility gate, so it is rendered rather than named by a constant, and it is one
	// string per resolution so the line reads once with a count (ADR-0019, amended).
	//
	// This is where the reason is stated in full. The inspect viewport's WORKFLOW cell
	// carries it too, truncated to the column as any over-long cell is, so a row can be told
	// apart without leaving the table.
	if um := m.plan.Unmatched(); len(um) > 0 {
		out = append(out, styleWarn.Render(indent+strconv.Itoa(len(um))+" of "+strconv.Itoa(total)+
			" will be skipped: "+textsan.Sanitize(um[0].Reason)))
	}
	return out
}

// promptLine is R7's friction prompt: y/N below the threshold, and the exact typed
// count at or above it or across repositories. The typed buffer is echoed so the
// operator sees what they have entered (R7, AC6, AC7). The y/N verb tracks the
// operation, so a reused pane names what it will do: "Cancel these Runs?" for a cancel,
// "Delete these Runs?" for a Purge (run-lifecycle R17 reuses this pane, and the headline
// is already verb-aware). operationVerb(OpDelete) is "Delete", so a Purge's prompt is
// unchanged.
func (m Model) promptLine() string {
	if m.plan.Friction() == ops.FrictionTypedCount {
		prompt := stylePrompt.Render(indent + "Type " + strconv.Itoa(m.plan.Total()) + " to confirm: ")
		return prompt + styleTyped.Render(m.typed)
	}
	return stylePrompt.Render(indent + operationVerb(m.plan.Operation()) + " these " + pluralNoun(m.plan) + "? [y/N]")
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
// A set may hold a second kind of member that has no Item, and those rows append after the
// Items in resolution order rather than interleaving by Run: interleaving would put a
// member the operator cannot act on between two they can, and the Items keep the selection
// order among themselves, which is the property R30's viewport shows (ADR-0019, amended).
func (m Model) inspectView() string {
	items := m.plan.Items()
	unmatched := m.plan.Unmatched()
	total := m.plan.Total()
	var lines []string
	lines = append(lines, styleTitle.Render("Frozen set: "+strconv.Itoa(total)+" "+pluralNoun(m.plan)))
	lines = append(lines, styleHeader.Render(m.inspectHeader()))
	rows := m.inspectPage()
	end := m.top + rows
	if end > total {
		end = total
	}
	for i := m.top; i < end; i++ {
		if i < len(items) {
			lines = append(lines, m.inspectRow(items[i], i == m.cursor))
			continue
		}
		lines = append(lines, m.unmatchedRow(unmatched[i-len(items)], i == m.cursor))
	}
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(indent+"row "+strconv.Itoa(m.cursor+1)+" of "+strconv.Itoa(total)+".  "+
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
	case it.Job != nil:
		// A Job carries its own Status and Conclusion and its own name, and it is a row a
		// by-name re-run fills the viewport with. Reading them off the Job is the same rule
		// the Run arm follows one level down, and without it every Job row rendered as a
		// repository and an id beside four empty cells.
		j := it.Job
		status = string(j.Status)
		if j.Status == domain.StatusCompleted {
			conclusion = string(j.Conclusion)
		}
		workflow = j.Name
		if !j.StartedAt.IsZero() {
			started = j.StartedAt.UTC().Format(startLayout)
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

// unmatchedRow renders one Item-less member, and purge AC22's claim narrows to admit it.
// That criterion says every row carries its owning repository, its Run ID, and Status and
// Conclusion in separate cells, and that the last row is the oldest Run in the set by
// run_started_at. This row satisfies the first two cells and none of the rest: it is handed
// to nothing, it holds no Run and so has neither a Status nor a Conclusion to put in a
// cell, and it carries no run_started_at to sort on.
//
// The claim the types actually make is narrower and still exact: every row that names a
// write is a tuple Execute is handed, carries all four cells, and sorts. This row names the
// absence of a write. It leaves Status and Conclusion empty on the same reading that
// already empties Conclusion for a Run that is not completed, and puts the reason in the
// WORKFLOW cell, which is the flex column and the one every non-Run Kind already fills with
// what it carries instead (ADR-0019, amended).
func (m Model) unmatchedRow(um ops.Unmatched, cursor bool) string {
	cells := []string{
		truncPad(textsan.Sanitize(um.Repo.Owner+"/"+um.Repo.Name), repoW),
		truncPad(strconv.FormatInt(um.RunID, 10), idW),
		truncPad("", statusW),
		truncPad("", conclusionW),
		truncPad(textsan.Sanitize(um.Reason), m.workflowWidth()),
		truncPad("", startedW),
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
	case ops.OpRerunJob:
		return "Re-run"
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
	case ops.KindJob:
		return "Jobs"
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
