package running

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Layout. The strip is two lines while an operation runs and a short block once it
// finishes, so a Purge that is not modal costs the focused tab a handful of rows rather
// than its whole body (R14, AC10).
const (
	// maxFailureGroups bounds the summary's height. R22 groups by reason, and the API
	// authors those reasons, so a pathological pass could produce dozens: the strip states
	// the first few and how many more there are rather than pushing the tab off screen.
	maxFailureGroups = 4
	fieldSep         = "   "
	groupIndent      = "  "
)

// Styles. Colours come from named palette roles, never hex literals, so the theme
// setting reaches this pane like every other (settings R6).
var (
	styleLabel  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Foreground(palette.Muted)
	styleCount  = lipgloss.NewStyle().Foreground(palette.Accent)
	styleFailed = lipgloss.NewStyle().Foreground(palette.Danger)
	styleDone   = lipgloss.NewStyle().Bold(true).Foreground(palette.Success)
	styleStop   = lipgloss.NewStyle().Foreground(palette.Warn)
)

// Height is how many rows the surface occupies, which the root subtracts from what it
// hands the tabs. Reading it off the rendered frame rather than from a constant is what
// keeps the reservation honest as the summary's failure groups change its height.
func (m Model) Height() int {
	v := m.View()
	if v == "" {
		return 0
	}
	return strings.Count(v, "\n") + 1
}

// View renders the surface from held state alone (ADR-0015's golden seam). It is empty
// while idle, R15's live progress while an operation runs, and R22's grouped summary once
// it has finished.
func (m Model) View() string {
	switch m.phase {
	case running:
		return strings.Join(m.progressLines(), "\n")
	case finished:
		return strings.Join(m.summaryLines(), "\n")
	case failed:
		return strings.Join(m.failureLines(), "\n")
	default:
		return ""
	}
}

// failureLines states that a launch was refused and why, with the dismiss on offer.
// Nothing was started, so there is nothing to stop and nothing to retry.
func (m Model) failureLines() []string {
	reason := "refused"
	if m.err != nil {
		reason = textsan.Sanitize(m.err.Error())
	}
	return []string{
		styleStop.Render(verb(m.op)+" did not start: ") + reason,
		styleDim.Render(m.profile.CancelRunning.Help().Key + " dismiss"),
	}
}

// progressLines is R15: deleted against the frozen total, skips and failures so far, the
// current rate, elapsed time, and remaining time. The first line is the tally and the
// second the timing, because all six on one line does not fit the 80-column terminal the
// tool is expected to run in.
func (m Model) progressLines() []string {
	p := m.last
	tally := []string{
		styleLabel.Render(verb(m.op)),
		fmt.Sprintf("%s %s of %s", styleCount.Render(commafy(m.actedCount())), pastTense(m.op), commafy(m.frozenTotal())),
	}
	if p.Sum.Skipped > 0 {
		tally = append(tally, fmt.Sprintf("%d skipped", p.Sum.Skipped))
	}
	if n := p.Sum.FailedCount(); n > 0 {
		tally = append(tally, styleFailed.Render(fmt.Sprintf("%d failed", n)))
	}

	var timing []string
	if p.Rate > 0 {
		timing = append(timing, fmt.Sprintf("%.1f/s", p.Rate))
	}
	if m.have {
		timing = append(timing, "elapsed "+humanDuration(p.Elapsed))
	}
	if r, ok := remaining(p); ok {
		timing = append(timing, r)
	}
	timing = append(timing, styleStop.Render(m.stopHint()))

	return []string{strings.Join(tally, fieldSep), styleDim.Render(strings.Join(timing, fieldSep))}
}

// stopHint names R16's key, or says the stop is in progress once it has been pressed:
// R16 permits one in-flight request to complete, so the surface says the Purge is
// stopping rather than claiming it has stopped.
func (m Model) stopHint() string {
	if m.stopping {
		return "stopping"
	}
	return m.profile.CancelRunning.Help().Key + " stop"
}

// actedCount is what this operation has done, which is the count R15 puts against the
// frozen total: deletions for a Purge, and the API-accepted mutations for a lifecycle
// operation, plus the 404s a Purge counts as successes (R18).
func (m Model) actedCount() int {
	return m.last.Sum.Deleted + m.last.Sum.Gone + m.last.Sum.Acted
}

// frozenTotal is the set's size, from the frame where one has arrived and from the launch
// handle before that.
func (m Model) frozenTotal() int {
	if m.last.Sum.Total > 0 {
		return m.last.Sum.Total
	}
	return m.total
}

// summaryLines is R22's end-of-operation account: the tally, the failures grouped by
// reason with a count for each, why the pass stopped early where it did, and the
// keystrokes on offer. It is the same shape the CLI prints headless (cli-surface R17),
// because both read one Summary.
func (m Model) summaryLines() []string {
	sum := m.last.Sum
	head := []string{styleDone.Render(verb(m.op) + " " + outcomeWord(sum))}
	head = append(head, fmt.Sprintf("%s deleted", styleCount.Render(commafy(sum.Deleted))))
	if sum.Gone > 0 {
		head = append(head, fmt.Sprintf("%s gone", commafy(sum.Gone)))
	}
	if sum.Acted > 0 {
		head = append(head, fmt.Sprintf("%s done", commafy(sum.Acted)))
	}
	if sum.Skipped > 0 {
		head = append(head, fmt.Sprintf("%s skipped", commafy(sum.Skipped)))
	}
	if n := sum.FailedCount(); n > 0 {
		head = append(head, styleFailed.Render(fmt.Sprintf("%s failed", commafy(n))))
	}
	head = append(head, styleDim.Render("of "+commafy(m.frozenTotal())))

	lines := []string{strings.Join(head, fieldSep)}
	// Both lists are labelled, and neither by omission, exactly as the CLI's summary
	// labels them: they print into one flat list, so an unlabelled group is read as
	// whichever kind the reader assumed, and a skip is not a failure.
	lines = append(lines, groupLines(sum.Failures, "failed: ", styleFailed)...)
	lines = append(lines, groupLines(sum.Skips, "skipped: ", styleDim)...)
	if sum.Reason != "" {
		// A circuit break, a cancellation or R29's log failure. The log failure above all
		// must never be a silent stop: AC20 requires the summary to name the log as the
		// reason it stopped, and the reason carries the words the write failed with.
		lines = append(lines, styleStop.Render(textsan.Sanitize(sum.Reason)))
	}
	lines = append(lines, styleDim.Render(strings.Join(m.summaryHints(), fieldSep)))
	return lines
}

// summaryHints names the keystrokes the summary offers: R22's retry where there is
// something to re-attempt, and the dismiss that clears the strip.
func (m Model) summaryHints() []string {
	var hints []string
	if n := m.last.Sum.FailedCount(); n > 0 && m.retrier != nil {
		hints = append(hints, fmt.Sprintf("%s retry %s", m.profile.RetryFailures.Help().Key, plural(n, "1 failure", commafy(n)+" failures")))
	}
	return append(hints, m.profile.CancelRunning.Help().Key+" dismiss")
}

// outcomeWord says how the pass ended. A pass that halted before the whole set says so
// rather than reading as a clean finish, because "stopped" and "finished" are different
// facts and the operator is about to decide whether to re-run (R21, R24, R29).
func outcomeWord(sum ops.Summary) string {
	switch {
	case sum.Cancelled:
		return "stopped"
	case sum.CircuitBroke:
		return "circuit-broke"
	case sum.LogFailed:
		return "stopped: the deletion log failed"
	default:
		return "finished"
	}
}

// groupLines renders one grouped-reason list, R22's "N x reason" with a count for each
// (AC18). A reason carries the API's own error message, which a hostile third-party
// repository controls, so it is sanitised at this terminal boundary exactly as the CLI
// sanitises the same field. The list is bounded, and states how many it did not print
// rather than growing without limit.
func groupLines(groups []ops.FailureGroup, label string, style lipgloss.Style) []string {
	var out []string
	for i, g := range groups {
		if i == maxFailureGroups && len(groups) > maxFailureGroups+1 {
			out = append(out, styleDim.Render(fmt.Sprintf("%s and %d more reasons", groupIndent, len(groups)-i)))
			break
		}
		out = append(out, groupIndent+style.Render(fmt.Sprintf("%d x %s%s", g.Count, label, textsan.Sanitize(g.Reason))))
	}
	return out
}

// remaining is R15's remaining-time figure, and the reason it is a range.
//
// The optimistic end is the outstanding count over the governor's current dynamic
// ceiling, and the pessimistic end is that count over the observed trailing rate clamped
// never worse than the governor's floor (AC23). Both ends therefore come from bounds the
// governor publishes, which is what makes the range contain the truth rather than merely
// look wide. When both round to the same displayed figure the range collapses to that
// figure, labelled an estimate: display granularity is the collapse rule, and no timer
// and no configuration participates.
//
// With no bounds on the frame there is no honest figure to give, so none is given. A
// point figure computed during the ramp is exactly the shape R15 forbids, and an
// "estimate" label does not make one honest.
func remaining(p ops.Progress) (string, bool) {
	if p.Outstanding <= 0 || p.Ceiling <= 0 {
		return "", false
	}
	optimistic := durationFor(p.Outstanding, p.Ceiling)
	rate := p.Rate
	if rate < p.Floor {
		rate = p.Floor // the pessimistic end is never worse than the floor (R15, AC23)
	}
	if rate <= 0 {
		return "", false
	}
	pessimistic := durationFor(p.Outstanding, rate)
	if pessimistic < optimistic {
		// The observed rate has overtaken the ceiling, which the ramp can do transiently.
		// The two ends have converged rather than inverted, so they collapse below.
		pessimistic = optimistic
	}
	lo, hi := humanDuration(optimistic), humanDuration(pessimistic)
	if lo == hi {
		return fmt.Sprintf("remaining ~%s (estimate)", lo), true
	}
	return fmt.Sprintf("remaining %s to %s", lo, hi), true
}

// durationFor is count items at rate per second, as a duration.
func durationFor(count int, rate float64) time.Duration {
	return time.Duration(float64(count) / rate * float64(time.Second))
}

// humanDuration renders a duration at the granularity a person reads it at, which is
// also R15's collapse rule: the range folds exactly when its ends stop differing at the
// precision on screen. The steps are coarse deliberately, because a range that can never
// collapse is a range that stays on screen through a Purge's last minute.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(5*time.Second).Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Round(time.Minute).Minutes())) + "m"
	default:
		d = d.Round(5 * time.Minute)
		h, mins := int(d.Hours()), int(d.Minutes())%60
		if mins == 0 {
			return strconv.Itoa(h) + "h"
		}
		return fmt.Sprintf("%dh %dm", h, mins)
	}
}

// verb names the operation the surface is showing. It is the glossary's word for each
// (CONTEXT.md): a delete is a Purge, and the rest are named for what they do.
func verb(op ops.Operation) string {
	switch op {
	case ops.OpDelete:
		return "Purge"
	case ops.OpCancel:
		return "Cancel"
	case ops.OpForceCancel:
		return "Force-cancel"
	case ops.OpRerun:
		return "Re-run"
	case ops.OpRerunFailed:
		return "Re-run failed jobs"
	case ops.OpEnable:
		return "Enable"
	case ops.OpDisable:
		return "Disable"
	default:
		return "Operation"
	}
}

// pastTense is what the operation has done to the count beside it, so R15's "Runs deleted
// against the frozen total" reads as such rather than as a bare fraction. Only a deletion
// is "deleted"; the rest are lifecycle mutations the API accepted, which is a request made
// and not an outcome observed (run-lifecycle R4, AC5), so they read as done.
func pastTense(op ops.Operation) string {
	if op == ops.OpDelete {
		return "deleted"
	}
	return "done"
}

// commafy renders a non-negative integer with thousands separators, the form the Feed's
// cap label uses (live-run-feed R24).
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

// plural picks the singular or plural phrasing for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
