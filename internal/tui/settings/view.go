package settings

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Layout. The label column is sized to the longest label so the value column aligns.
const (
	labelWidth = 26
	markerCol  = 2
)

// Styles. Every colour comes from the palette, so the theme setting reaches this frame
// (settings R6): a role resolves to its dark or light value as the style renders. lipgloss
// v2 renders truecolour regardless of TERM or NO_COLOR, so a golden over View() is
// byte-stable on any machine at a given appearance (ADR-0013), and the light golden beside
// the dark one is what pins the other half.
var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Foreground(palette.Muted)
	styleWarn   = lipgloss.NewStyle().Bold(true).Foreground(palette.Warn)
	styleValue  = lipgloss.NewStyle().Bold(true).Foreground(palette.Accent)
	styleActive = lipgloss.NewStyle().Bold(true)
	styleCaret  = lipgloss.NewStyle().Foreground(palette.Accent)
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
	if m.editErr != "" {
		// An entry the editor could not parse is named rather than dropped in silence,
		// which is the loader's R14 rule applied to input arriving by keystroke.
		lines = append(lines, styleWarn.Render(textsan.Sanitize(m.editErr)))
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
		if m.editing && r.isEditable() {
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
	if focused && r.isList() && !m.editing {
		line += "  " + styleDim.Render("(enter to edit, comma separated)")
	}
	if focused && r.isFilter() && !m.editing {
		line += "  " + styleDim.Render("(enter to edit)")
	}
	return line
}

// valueCell renders the current value of the row, or the in-progress numeric entry when the
// row is being edited. Selectors show their chosen member; numbers show the integer, with
// the discovery interval carrying its unit so the value reads as intent.
func (m Model) valueCell(r row, focused bool) string {
	if focused && m.editing && r.isEditable() {
		return styleValue.Render(textsan.Sanitize(m.tailOfBuffer())) + styleCaret.Render("_")
	}
	return styleValue.Render(textsan.Sanitize(m.rawValue(r)))
}

// tailOfBuffer is the part of the edit buffer that fits the value column, taken from the
// end so the caret and the characters just typed stay visible. A long exclude list is
// exactly the case that overflows, and scrolling sideways is what a person editing the end
// of a line expects.
func (m Model) tailOfBuffer() string {
	room := m.valueRoom()
	if room <= 0 || len(m.editBuf) <= room {
		return m.editBuf
	}
	return m.editBuf[len(m.editBuf)-room:]
}

// valueRoom is how many characters the value column has, given the frame width the last
// WindowSizeMsg set. It leaves one column for the caret. A pane that has not been sized
// yet reports no constraint, so a headless render prints the value whole.
func (m Model) valueRoom() int {
	if m.width <= 0 {
		return 0
	}
	return m.width - markerCol - labelWidth - 1
}

// rawValue is the plain text of a row's current value, before styling.
func (m Model) rawValue(r row) string {
	switch r {
	case rowBudget:
		return string(m.cfg.Budget)
	case rowProfile:
		return string(m.cfg.KeybindingProfile)
	case rowTheme:
		return string(m.cfg.Theme)
	case rowTimestamp:
		return string(m.cfg.Timestamp)
	case rowWorkflowsScope:
		return string(m.cfg.WorkflowsScope)
	case rowStorageScope:
		return string(m.cfg.StorageScope)
	case rowLaunchFilter:
		return filterLine(m.cfg.LaunchFilter)
	case rowExclude:
		return repoList(m.cfg.Exclude, m.valueRoom())
	case rowPin:
		return repoList(m.cfg.Pin, m.valueRoom())
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
	case rowTheme:
		return "Theme"
	case rowTimestamp:
		return "Timestamps"
	case rowWorkflowsScope:
		return "Workflows scope"
	case rowStorageScope:
		return "Storage scope"
	case rowLaunchFilter:
		return "Launch filter"
	case rowExclude:
		return "Excluded repositories"
	case rowPin:
		return "Pinned repositories"
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
	case rowTheme:
		return "Palette: auto follows your terminal background. NO_COLOR overrides all three."
	case rowTimestamp:
		return "When a run started: the instant itself, or how long ago it was."
	// Both scope rows say "starts" for the reason R9's launch filter does, and it is the
	// requirement rather than a hedge: each tab chooses its scope at construction, because
	// narrowing it also means dropping the held state, so a toggle takes effect at the next
	// launch. The present tense would state something a person would test by pressing refresh.
	case rowWorkflowsScope:
		return "The Workflows tab starts covering these repositories."
	case rowStorageScope:
		return "The Storage tab starts covering these repositories."
	case rowLaunchFilter:
		// It says "starts" rather than "now", and that is the requirement rather than a
		// hedge: R9 settles the filter the Feed opens with, and a running Feed keeps
		// whatever filter it is showing until its own / input changes it.
		return "The Runs feed starts filtered by this. Same syntax as its / filter."
	case rowExclude:
		return "Kept out of discovery and never polled. Naming one with -R still works."
	case rowPin:
		// The description states what the code does and no more (#97): the promotion is to
		// the medium tier, which is the cadence an on-screen repository already gets, and
		// exclusion wins because it applies at discovery before any tier is chosen.
		return "Polled as often as an on-screen one, even when scrolled away. Excluding wins."
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
	case rowTheme:
		return joinThemes(config.Themes())
	case rowTimestamp:
		return joinTimestampFormats(config.TimestampFormats())
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
	// The help names what the focused row actually takes, rather than the union of every
	// row's gestures. A footer offering "space change" on a row that ignores space, or
	// "edit number" on a row that takes text, is the same class of untruth as a label
	// promising behaviour the code does not have.
	parts := []string{move + " move"}
	switch {
	case m.editing:
		parts = append(parts,
			m.profile.OpenDetail.Help().Key+" commit",
			m.profile.CloseDetail.Help().Key+" cancel")
		return strings.Join(parts[1:], "   ")
	case m.cursor.isSelector():
		parts = append(parts, m.profile.ToggleSelect.Help().Key+" change")
	case m.cursor.isNumber():
		parts = append(parts, m.profile.OpenDetail.Help().Key+" edit number")
	case m.cursor.isList():
		parts = append(parts, m.profile.OpenDetail.Help().Key+" edit list")
	case m.cursor.isFilter():
		parts = append(parts, m.profile.OpenDetail.Help().Key+" edit filter")
	}
	parts = append(parts, m.profile.CloseDetail.Help().Key+" close")
	return strings.Join(parts, "   ")
}

// padLabel right-pads a label to the aligned column width.
func padLabel(s string) string {
	if len(s) >= labelWidth {
		return s
	}
	return s + strings.Repeat(" ", labelWidth-len(s))
}

// repoList renders R7's exclude list as text (R16: meaning never rides on colour). An
// empty list reads "none" rather than blank, because a blank cell reads as broken where
// "none" reads as a setting nobody has used. Each entry is spelled OWNER/REPO, the same
// short form the config file carries.
//
// It names as many entries as the value column holds and counts the rest, rather than
// stopping at a fixed number. The reference account excludes from 163 repositories to
// reach the ~10 it cares about, so which repositories are on the list is the interesting
// part and a small constant would hide most of it. room is the column width, and a room
// of zero means unconstrained, which is what a pane no WindowSizeMsg has reached reports.
func repoList(ids []domain.RepoID, room int) string {
	if len(ids) == 0 {
		return "none"
	}
	names := repoRefs(ids)
	if room <= 0 {
		return strings.Join(names, ", ")
	}

	shown, width := 0, 0
	for i, name := range names {
		next := width + len(name)
		if i > 0 {
			next += len(", ")
		}
		// Keep room for the ", and N more" tail whenever entries would be left over.
		if next+len(tailFor(len(names)-i-1)) > room {
			break
		}
		width, shown = next, i+1
	}
	if shown == 0 {
		// Not even one entry fits beside its own tail. Naming the count alone is the
		// honest fallback: a truncated repository name is a name that is not there.
		return strconv.Itoa(len(names)) + " repositories"
	}
	return strings.Join(names[:shown], ", ") + tailFor(len(names)-shown)
}

// filterLine renders R9's launch filter as one line of the grammar filter owns, which is
// the line the editor opens on and the line a person would type into the Feed's / input
// (R17: the view and the file are the same settings, and now the same vocabulary too). An
// empty filter reads "none" rather than blank, for the reason the exclude row does: a blank
// cell reads as broken where "none" reads as a setting nobody has used.
func filterLine(f filter.Filter) string {
	if q := f.QueryString(); q != "" {
		return q
	}
	return "none"
}

// repoRefs spells identities the way the config file does, OWNER/REPO. It is what the row
// renders and what the editor opens on, so the text a person edits is the text they would
// have typed into config.yml (R17: the view and the file are the same settings). Every
// identity 2.0.0 admits is a github.com one, so the host is never spelled (ADR-0009).
func repoRefs(ids []domain.RepoID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.Owner + "/" + id.Name
	}
	return out
}

// tailFor is the summary that follows the entries a row could name, empty when none were
// left over.
func tailFor(rest int) string {
	if rest <= 0 {
		return ""
	}
	return ", and " + strconv.Itoa(rest) + " more"
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

func joinThemes(ts []config.Theme) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return strings.Join(out, " / ")
}

func joinTimestampFormats(fs []config.TimestampFormat) string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f)
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
