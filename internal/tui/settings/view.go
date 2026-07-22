package settings

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Layout. The label column is sized to the longest label so the value column aligns.
const (
	labelWidth = 26
	markerCol  = 2
)

// Styles. lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over
// View() is byte-stable on any machine (ADR-0013). The palette matches the confirm pane's.
var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleWarn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff875f"))
	styleValue  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00afff"))
	styleActive = lipgloss.NewStyle().Bold(true)
	styleCaret  = lipgloss.NewStyle().Foreground(lipgloss.Color("#00afff"))
)

// View renders the pane from held state alone, with no live terminal and no network (R18).
// It is empty while closed, so the root never paints a stale settings view over a tab. The
// frame is the title, a persistence note, one row per setting, a description of the focused
// setting, and the keybinding help. Every value is sanitised before it is painted: the
// selectors are operator-controlled, but routing them through textsan keeps a single rule
// for displayed text and is the safe default a repository string echoed here would need.
func (m Model) View() string {
	if !m.open {
		return ""
	}
	var lines []string
	lines = append(lines, styleTitle.Render("Settings"))
	lines = append(lines, styleDim.Render("Changes are saved to your config file."))
	if m.saveErr != nil {
		lines = append(lines, styleWarn.Render("Settings could not be saved: "+textsan.Sanitize(m.saveErr.Error())))
	}
	lines = append(lines, "")
	for r := row(0); r < rowCount; r++ {
		lines = append(lines, m.rowLine(r))
	}
	lines = append(lines, "")
	lines = append(lines, styleDim.Render(m.description(m.cursor)))
	lines = append(lines, styleDim.Render(m.helpLine()))
	return strings.Join(lines, "\n")
}

// rowLine renders one setting's row: a cursor marker, the label, and the value. The focused
// row is bold and, when it is a selector, states the values it cycles through; a numeric row
// being edited shows the typed buffer with a caret. Meaning never rides on colour alone: the
// value is always a text label (R16).
func (m Model) rowLine(r row) string {
	focused := r == m.cursor
	marker := "  "
	label := padLabel(m.label(r))
	if focused {
		if m.editing && r.isNumber() {
			marker = styleCaret.Render("> ")
		} else {
			marker = styleActive.Render("> ")
		}
		label = styleActive.Render(label)
	}

	value := m.valueCell(r, focused)
	line := marker + label + value
	if focused && r.isSelector() {
		line += "  " + styleDim.Render("("+m.optionsHint(r)+")")
	}
	if focused && r.isNumber() && !m.editing {
		line += "  " + styleDim.Render("("+m.boundHint(r)+")")
	}
	return line
}

// valueCell renders the current value of the row, or the in-progress numeric entry when the
// row is being edited. Selectors show their chosen member; numbers show the integer, with
// the discovery interval carrying its unit so the value reads as intent.
func (m Model) valueCell(r row, focused bool) string {
	if focused && m.editing && r.isNumber() {
		return styleValue.Render(m.editBuf) + styleCaret.Render("_")
	}
	return styleValue.Render(textsan.Sanitize(m.rawValue(r)))
}

// rawValue is the plain text of a row's current value, before styling.
func (m Model) rawValue(r row) string {
	switch r {
	case rowBudget:
		return string(m.cfg.Budget)
	case rowProfile:
		return string(m.cfg.KeybindingProfile)
	case rowWorkflowsScope:
		return string(m.cfg.WorkflowsScope)
	case rowStorageScope:
		return string(m.cfg.StorageScope)
	case rowConfirmThreshold:
		return strconv.Itoa(m.cfg.ConfirmThreshold)
	case rowBreakerFailures:
		return strconv.Itoa(m.cfg.BreakerFailures)
	case rowDiscoveryRefresh:
		return strconv.Itoa(m.cfg.DiscoveryRefreshMinutes) + " min"
	default:
		return ""
	}
}

// label is the human name of a setting, expressed at the level of intent (Purpose): a
// person deciding it answers from their own context, without the mechanism behind it.
func (m Model) label(r row) string {
	switch r {
	case rowBudget:
		return "Budget"
	case rowProfile:
		return "Keybinding profile"
	case rowWorkflowsScope:
		return "Workflows scope"
	case rowStorageScope:
		return "Storage scope"
	case rowConfirmThreshold:
		return "Confirmation threshold"
	case rowBreakerFailures:
		return "Purge breaker threshold"
	case rowDiscoveryRefresh:
		return "Discovery refresh"
	default:
		return ""
	}
}

// description is the one-line help for the focused setting, intent-level and free of the
// mechanism R13 keeps out of the view: none names a poll interval, a delete rate, a cache
// TTL, a concurrency level, or a way to skip confirmation.
func (m Model) description(r row) string {
	switch r {
	case rowBudget:
		return "Share of your API allowance the background refresh may spend."
	case rowProfile:
		return "Motion keys: Vim (k/j) or Standard (arrows)."
	case rowWorkflowsScope:
		return "Which repositories the Workflows tab covers."
	case rowStorageScope:
		return "Which repositories the Storage tab covers."
	case rowConfirmThreshold:
		return "Deletions at or above this many make you type the count."
	case rowBreakerFailures:
		return "Consecutive failures before a Purge stops itself."
	case rowDiscoveryRefresh:
		return "How quickly a newly active repository shows up, in minutes."
	default:
		return ""
	}
}

// optionsHint lists the members a focused selector cycles through, drawn from the exported
// valid set so it matches what the loader accepts (R5, R8, R19).
func (m Model) optionsHint(r row) string {
	switch r {
	case rowBudget:
		return joinTiers(config.Tiers())
	case rowProfile:
		return joinProfiles(config.KeybindingProfiles())
	case rowWorkflowsScope, rowStorageScope:
		return joinScopes(config.Scopes())
	default:
		return ""
	}
}

// boundHint states a numeric setting's range, so a focused number row shows what a commit
// will clamp to (R12, R20, R21).
func (m Model) boundHint(r row) string {
	switch r {
	case rowConfirmThreshold:
		return "up to 500, type the count above it"
	case rowBreakerFailures:
		return "1 to 500"
	case rowDiscoveryRefresh:
		return "1 or more minutes"
	default:
		return ""
	}
}

// helpLine names the keys the pane uses, drawn from the profile's own help so it reflects
// the selected motion set rather than a hardcoded literal (R7a).
func (m Model) helpLine() string {
	move := m.profile.RowUp.Help().Key + "/" + m.profile.RowDown.Help().Key
	parts := []string{
		move + " move",
		m.profile.ToggleSelect.Help().Key + " change",
		m.profile.OpenDetail.Help().Key + " edit number",
		m.profile.CloseDetail.Help().Key + " close",
	}
	return strings.Join(parts, "   ")
}

// padLabel right-pads a label to the aligned column width.
func padLabel(s string) string {
	if len(s) >= labelWidth {
		return s
	}
	return s + strings.Repeat(" ", labelWidth-len(s))
}

func joinTiers(ts []config.Tier) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return strings.Join(out, " / ")
}

func joinProfiles(ps []config.KeybindingProfile) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return strings.Join(out, " / ")
}

func joinScopes(ss []config.Scope) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return strings.Join(out, " / ")
}
