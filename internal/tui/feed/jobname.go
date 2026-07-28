package feed

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// jobNameResolved carries one by-name resolution back into the update loop (run-lifecycle
// R17a). It is the Feed's own message rather than an ops type because it pairs the
// resolution with the selection it was made against, which is what Plan needs next and
// what a resolution alone does not carry.
type jobNameResolved struct {
	res ops.Resolution
	err error
}

// openJobNameForm opens the by-name per-Job re-run's text input over the frozen selection
// (run-lifecycle R14a, R17a). The selection is frozen here rather than at accept, because
// the Feed's projections are rewritten under every poll and the operator's set is the one
// they were looking at when they pressed the key (R5).
//
// It is inert with no planner wired and with nothing to act on, so the key offers the
// operation only where one exists. There is no capability gate here: an archived or
// read-only repository is a skip Plan stamps, not a reason to refuse the whole form, and
// refusing here would discard the writable repositories selected beside it.
func (m Model) openJobNameForm() (Model, tea.Cmd) {
	if m.planner == nil {
		return m, nil
	}
	sel := m.frozenSelection()
	if len(sel) == 0 {
		return m, nil
	}
	// Opening the form is idle in R10's sense, exactly as entering the filter input is: the
	// cursor leaves the list, so deferred changes apply rather than staying frozen behind a
	// form the operator may sit in.
	m.applyView(m.liveView())
	m.jobNameSelection = sel
	m.jobNameActive = true
	m.jobNameNotice = "" // the prior resolution's note belongs to the prior resolution
	m.jobNameInput.SetValue("")
	return m, m.jobNameInput.Focus()
}

// closeJobNameForm dismisses the form and drops the selection it froze, so nothing is held
// across the close. It is a plain state reset rather than a Cmd, because blurring an input
// issues nothing.
func (m Model) closeJobNameForm() Model {
	m.jobNameActive = false
	m.jobNameInput.Blur()
	m.jobNameInput.SetValue("")
	m.jobNameSelection = nil
	return m
}

// handleJobNameKey drives the by-name form. It reuses the filter input's accept and cancel
// bindings rather than minting a second pair, because they are the registry's names for
// "commit this text input" and "abandon it" and a second pair would be the same two keys
// under different names (R7a, AC18).
//
// Accept resolves; cancel closes and resolves nothing, which matters more here than for the
// filter: the resolution costs one Jobs request per selected Run, drawn from the Budget, so
// an abandoned form must spend none of them.
func (m Model) handleJobNameKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.FilterCancel):
		return m.closeJobNameForm(), nil
	case key.Matches(k, m.profile.FilterAccept):
		name := m.jobNameInput.Value()
		if name == "" {
			// An empty name matches no Job in any Run, so accepting it would spend one request
			// per selected Run to freeze a set of nothing but Item-less members. The form stays
			// open rather than closing, because the operator is mid-way through typing one.
			return m, nil
		}
		sel := m.jobNameSelection
		m = m.closeJobNameForm()
		return m, m.resolveJobName(sel, name)
	}
	var cmd tea.Cmd
	m.jobNameInput, cmd = m.jobNameInput.Update(k)
	return m, cmd
}

// resolveJobName runs the resolution off the update loop and hands the result back as a
// message. It has to leave the loop: it issues one Jobs request per selected Run, and
// Update must stay non-blocking, which is the same rule launch follows for Confirm and
// Start.
//
// context.Background rather than a context threaded from main.go, for launch's reason
// unchanged: the resolution's lifetime is its own. It is bounded by the selection's size
// rather than by a caller's scope, and nothing cancels it, because the operator's next act
// is the modal it opens.
func (m Model) resolveJobName(sel []ops.Item, name string) tea.Cmd {
	planner := m.planner
	if planner == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := planner.ResolveJobsByName(context.Background(), sel, name)
		return jobNameResolved{res: res, err: err}
	}
}

// handleJobNameResolved freezes what the resolution resolved and opens the confirmation
// over it (run-lifecycle R17a).
//
// The frozen set is the resolution's own output and not the selection: a Run the resolution
// never reached is not a member of it, and pricing the whole selection instead would make
// R17's count name a set that cannot be acted on. The Runs it did reach and answered no for
// are members, as Item-less ones, and they are inside the count on the same reading that
// already puts a skipped Item inside it.
//
// The note is set after Open, which resets it, and it renders only when the resolution left
// Runs unreached. That is R17a's requirement on this surface: the operator is confirming a
// number smaller than the set they named, and R7's ladder has no rung that says so.
func (m Model) handleJobNameResolved(msg jobNameResolved) (Model, tea.Cmd) {
	if msg.err != nil || m.planner == nil {
		return m, nil // fail closed: no set was frozen, so nothing is offered
	}
	if len(msg.res.Items) == 0 && len(msg.res.Unmatched) == 0 {
		// The resolution reached nothing it could freeze. There is no set to confirm and no
		// count to show, so the modal does not open over an empty one.
		return m, nil
	}
	plan, err := m.planner.Plan(ops.OpRerunJob, msg.res.Items, m.repoSnapshot(), msg.res.Unmatched...)
	if err != nil {
		return m, nil // fail closed: an unknown repository keeps the action disabled (repo-discovery R8)
	}
	if plan.Friction() == ops.FrictionNone {
		// A single-member set takes no confirmation, and R18 says so as a MUST NOT: "A
		// single-Job re-run MUST NOT [take a confirmation] either. R14b's note is what that
		// operation carries instead, and a note is not a confirmation."
		//
		// R17a's fact is still owed where the resolution stopped early, and the modal is not
		// how it gets paid: the pane's FrictionNone prompt is a y/N that has to be answered,
		// so routing the note through it would make a note block, which R17a forbids in the
		// same sentence that requires it. The Feed's own notice line carries it instead,
		// which is the same move R17a makes for the CLI's --yes: where there is no confirm
		// surface, state the fact beside the operation rather than in front of it.
		m.jobNameNotice = unreachedNotice(msg.res)
		return m, m.launch(plan, ops.NoInput())
	}
	m.confirm = m.confirm.Open(plan).WithUnreached(msg.res.Unreached, msg.res.UnreachedReason)
	m.confirmOpen = true
	return m, nil
}

// unreachedNotice is R17a's fact as a Feed line, for the one path that has no modal to
// render it: a resolution that stopped early and froze a set small enough to take no
// confirmation. It is empty where the resolution reached everything, so the line is absent
// rather than blank.
func unreachedNotice(res ops.Resolution) string {
	if !res.StoppedEarly() {
		return ""
	}
	notice := fmt.Sprintf("%d selected %s were not reached, so they were not re-run",
		res.Unreached, plural(res.Unreached, "run", "runs"))
	if res.UnreachedReason != "" {
		notice += ": " + res.UnreachedReason
	}
	return notice
}
