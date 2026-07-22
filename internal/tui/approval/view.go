package approval

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Layout constants. The pane renders from held state alone at a fixed geometry, so a golden
// over View() is byte-stable (AC-golden). lipgloss v2 renders truecolour regardless of TERM
// or NO_COLOR, so the bytes are stable on any machine (ADR-0013).
const (
	indent = "  "
)

// Styles mirror the confirm and dispatch panes' palette so the three read as one product.
var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleErr   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f5f"))
	styleOK    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fd75f"))
	styleFocus = lipgloss.NewStyle().Bold(true)
	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf00"))
)

// View renders the decision from held state alone, with no live terminal and no network. It
// is empty while closed, a fork-PR approval prompt for that Kind, and the environments review
// for a pending deployment. Every API-derived string is sanitised before it is painted
// (security review).
func (m Model) View() string {
	if !m.open {
		return ""
	}
	switch m.kind {
	case approvals.KindForkPR:
		return m.forkPRView()
	case approvals.KindPendingDeployment:
		return m.deploymentView()
	default:
		return ""
	}
}

// forkPRView renders the fork-PR approval prompt (R11): the Run it approves and a single
// approve action, with no comment and no environments, because /approve takes none.
func (m Model) forkPRView() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Approve fork-PR run"))
	b.WriteString("\n")
	b.WriteString(m.runLine())
	b.WriteString("\n\n")
	b.WriteString(indent + "Approving lets this fork pull request's workflow run proceed.")
	b.WriteString("\n")
	b.WriteString(m.footer(fmt.Sprintf("%s approve   %s cancel",
		m.profile.Approve.Help().Key, m.profile.CloseDetail.Help().Key)))
	return b.String()
}

// deploymentView renders the pending-deployment review (R12): the environments the Run
// awaits, the approve-or-reject decision, and the required comment (R13). It is a pending
// line while loading, an explicit error when the environments could not be read, and the
// review once they are held.
func (m Model) deploymentView() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Review pending deployments"))
	b.WriteString("\n")
	b.WriteString(m.runLine())
	b.WriteString("\n\n")

	switch {
	case m.loadErr != "":
		b.WriteString(styleErr.Render(indent + textsan.Sanitize(m.loadErr)))
		b.WriteString("\n")
		b.WriteString(m.footer(fmt.Sprintf("%s close", m.profile.CloseDetail.Help().Key)))
		return b.String()
	case m.loading:
		b.WriteString(styleDim.Render(indent + "loading the environments this run awaits…"))
		b.WriteString("\n")
		b.WriteString(m.footer(fmt.Sprintf("%s close", m.profile.CloseDetail.Help().Key)))
		return b.String()
	}

	b.WriteString(styleDim.Render(indent + "Environments awaiting a decision:"))
	b.WriteString("\n")
	if len(m.deployments) == 0 {
		b.WriteString(styleDim.Render(indent + indent + "none"))
		b.WriteString("\n")
	}
	for _, d := range m.deployments {
		b.WriteString(m.environmentLine(d))
	}
	b.WriteString("\n")
	b.WriteString(m.decisionLine())
	b.WriteString("\n")
	b.WriteString(m.commentLine())
	b.WriteString("\n")
	b.WriteString(m.footer(fmt.Sprintf("%s decision   %s comment   %s submit   %s close",
		m.profile.ToggleSelect.Help().Key,
		m.profile.OpenDetail.Help().Key,
		m.profile.Approve.Help().Key,
		m.profile.CloseDetail.Help().Key,
	)))
	return b.String()
}

// runLine names the repository and the Run the decision targets, with the sanitised title
// so the operator confirms which Run they are acting on.
func (m Model) runLine() string {
	line := m.repo.Owner + "/" + m.repo.Name + " run " + strconv.FormatInt(m.runID, 10)
	if m.title != "" {
		line += "  " + textsan.Sanitize(m.title)
	}
	return styleDim.Render(indent + line)
}

// environmentLine renders one awaited environment: its sanitised name, whether the current
// user can approve it (R10's current_user_can_approve at the row), and its reviewers. A
// review is still submittable when the user cannot approve, because R14 discovers reviewer
// standing by acting rather than pre-gating on this flag.
func (m Model) environmentLine(d PendingDeployment) string {
	name := textsan.Sanitize(d.EnvironmentName)
	if name == "" {
		name = "(unnamed)"
	}
	line := indent + indent + name
	if d.CurrentUserCanApprove {
		line += styleOK.Render("  (you can approve)")
	} else {
		line += styleWarn.Render("  (not your review)")
	}
	if len(d.Reviewers) > 0 {
		line += styleDim.Render("  reviewers: " + strings.Join(sanitizeAll(d.Reviewers), ", "))
	}
	return line + "\n"
}

// decisionLine renders the approve-or-reject toggle with the current decision highlighted
// (R12). Space flips it.
func (m Model) decisionLine() string {
	var approve, reject string
	if m.state == ops.ReviewApproved {
		approve = styleFocus.Render("[approve]")
		reject = styleDim.Render(" reject ")
	} else {
		approve = styleDim.Render(" approve ")
		reject = styleFocus.Render("[reject]")
	}
	return indent + "Decision: " + approve + " " + reject
}

// commentLine renders the required comment (R13): the text input while editing, else the
// current comment or a prompt that one is required.
func (m Model) commentLine() string {
	if m.editing {
		return indent + "Comment: " + m.editInput.View()
	}
	if strings.TrimSpace(m.comment) == "" {
		return indent + "Comment: " + styleWarn.Render("required")
	}
	return indent + "Comment: " + textsan.Sanitize(m.comment)
}

// footer renders the status line and the key hints. A successful write or R14's outcome is
// stated in the status; a refusal or a failure is styled as an error.
func (m Model) footer(hints string) string {
	var b strings.Builder
	b.WriteString("\n")
	if m.status != "" {
		style := styleOK
		if m.statusErr {
			style = styleErr
		}
		b.WriteString(style.Render(indent + textsan.Sanitize(m.status)))
		b.WriteString("\n")
	}
	b.WriteString(styleDim.Render(indent + hints))
	return b.String()
}

// sanitizeAll sanitises every string in a slice, for the author-controlled reviewer labels.
func sanitizeAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = textsan.Sanitize(x)
	}
	return out
}
