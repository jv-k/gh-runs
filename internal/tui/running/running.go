// Package running is the surface a launched write runs behind: purge R15's live
// progress, R16's cancel, and R22's grouped summary with its retry keystroke. It is a
// pane, not a tab and not a tea.Model: it exposes View() string and an Update its opener
// drives (ADR-0011's pane contract).
//
// # It belongs to the root, and it is a strip rather than a modal
//
// R14 forbids a Purge from being modal, and AC10 requires the Feed to keep updating and
// stay navigable while one runs. So this pane does not replace a tab's body and does not
// capture input: the root paints it above the focused tab, takes its two chords before
// routing keys onward, and gives the tabs the remaining height. That placement is also
// what ADR-0015 requires of the progress stream, which is broadcast because "a Purge
// outlives the operator's attention and must keep painting its indicator whichever tab
// is focused".
//
// # It is generic over ops.Operation
//
// A Purge, a bulk lifecycle mutation (run-lifecycle R16) and a Reclamation
// (storage-reclamation R24) are the same walk over a frozen set under the same failure
// contract, and ops already returns the same Progress for all of them. So the pane reads
// the verb off the frame and renders the noun the operation acts on, and a feature
// joining this surface wires a launch rather than building a second indicator. Nothing
// here knows what a Run is.
//
// # Every frame it paints is fabricable
//
// The pane holds no client, issues no request and owns no timing: elapsed, the observed
// rate and the governor's two bounds all arrive stamped on the frame from the injected
// clock (purge R27). A golden is a fabricated ops.Progress and a window size, which is
// the seam ADR-0015 promises and the reason R15's range and R22's summary are checkable
// without a live Purge.
package running

import (
	"context"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// Retrier re-attempts a finished pass's recorded failures (purge R22). *ops.Ops
// satisfies it, and the exemption from a fresh confirmation is ops's to enforce: the
// retry set is read from a Summary only a real pass can produce, so this seam cannot be
// handed a set nobody confirmed.
type Retrier interface {
	Retry(ctx context.Context, sum ops.Summary) (ops.Started, error)
}

// phase is where the surface stands.
type phase int

const (
	idle     phase = iota // nothing launched, and the pane costs no rows
	running               // an operation is in flight (R15)
	finished              // the terminal frame landed, and the summary is up (R22)
	failed                // the launch was refused, and nothing was started
)

// Model is the pane's state: the launched operation's handle, the latest frame, and
// where the surface stands. It holds no client and no timer.
type Model struct {
	profile keys.Profile
	retrier Retrier

	// width and height are the whole terminal the root laid the strip out in. The height is
	// what the strip's row budget derives from, so a summary can never take more than its
	// share of the screen (R14).
	width  int
	height int
	phase  phase
	op     ops.Operation
	// kind is the object the frozen set holds, which the operation alone does not name: a
	// delete over Runs is a Purge and the same delete over Caches and Artifacts is a
	// Reclamation (CONTEXT.md).
	kind   ops.Kind
	total  int
	cancel context.CancelFunc

	// last is the most recent frame. Every frame is a complete snapshot, so holding one
	// is holding the whole truth about the pass (ADR-0015).
	last ops.Progress
	// have reports whether a frame has arrived yet. Between the launch and the first
	// frame the pane paints from the Started handle alone, so the surface appears on the
	// keystroke rather than a governor interval later.
	have bool
	// stopping records that the cancel key was pressed. The operation is over when its
	// terminal frame lands, not when the key is hit: R16 permits one in-flight request to
	// complete, and claiming otherwise would put a finished summary on screen while a
	// DELETE was still out.
	stopping bool
	// err is why a launch was refused, shown while the phase is failed.
	err error
}

// New returns an idle pane over the operator's keybinding profile.
func New(profile keys.Profile) Model { return Model{profile: profile} }

// WithRetrier wires R22's re-attempt seam. A pane without one renders the summary and
// offers no retry, which is what a golden test wants and what a build with no ops engine
// gets.
func (m Model) WithRetrier(r Retrier) Model {
	m.retrier = r
	return m
}

// Start puts the pane on screen over a launched operation. It resets the frame state, so
// a second operation never paints the previous one's tally, and it holds the handle's
// cancel, which is R16's stop.
func (m Model) Start(st ops.Started) Model {
	m.phase = running
	m.op = st.Op
	m.kind = st.Kind
	m.total = st.Total
	m.cancel = st.Cancel
	m.last = ops.Progress{}
	m.have = false
	m.stopping = false
	m.err = nil
	return m
}

// Active reports whether the surface is on screen, which the root reads to paint it and
// to reserve its rows. It stays true through the summary, because R22's retry is offered
// from there.
func (m Model) Active() bool { return m.phase != idle }

// Running reports whether an operation is in flight. R22's retry set is a finished pass's
// record, so the retry key reads this.
func (m Model) Running() bool { return m.phase == running }

// Handles reports whether this key press is the surface's, which the root reads to route
// it here and to no tab, so one keystroke does one thing (ADR-0011). It is false while the
// surface is idle, so the two chords fall through to the focused tab rather than being
// reserved from it for the whole session.
func (m Model) Handles(k tea.KeyPressMsg) bool {
	if m.phase == idle {
		return false
	}
	return key.Matches(k, m.profile.CancelRunning) || key.Matches(k, m.profile.RetryFailures)
}

// Fail puts a refused launch on screen (ADR-0019's declined Confirm, a spent one, a
// launch refused because one is already running, or a retry with nothing left).
//
// While an operation is running the refusal is a note beside the live line and nothing
// more. It must not become the surface's state, because the handle this pane holds carries
// the only cancel the running operation has: replacing it is precisely the orphaning that
// makes a Purge invisible and unstoppable, and the commonest refusal there is (ErrBusy)
// arrives exactly when one is running. Reporting it is still required, because a keystroke
// that silently does nothing is the defect this surface exists to fix.
func (m Model) Fail(f ops.LaunchFailed) Model {
	if m.phase == running {
		m.err = f.Err
		return m
	}
	m.phase = failed
	m.op = f.Op
	// The Kind is taken from the refusal and never carried over from what was on screen.
	// The empty Kind is a real value, the mixed Cache-and-Artifact set that is
	// Reclamation's ordinary list, so it cannot also mean unknown: a carry-over would
	// relabel a genuine Reclamation refusal with whatever the strip was showing. Every
	// launcher stamps the Kind, which is the fix that does the work.
	m.kind = f.Kind
	m.err = f.Err
	m.cancel = nil
	m.last = ops.Progress{}
	m.have = false
	m.stopping = false
	return m
}

// Update handles one message the root routed here: the size it lays out at, a progress
// frame, and the two chords. It consumes nothing else. A frame arriving while the pane is
// idle is ignored, which is what discards the tail of a dismissed operation's stream.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case ops.Progress:
		return m.onProgress(msg), nil
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// onProgress records a frame. The terminal frame moves the surface to its summary, which
// is what R22's retry is offered from and what the CLI's printSummary is the headless
// twin of.
func (m Model) onProgress(p ops.Progress) Model {
	if m.phase == idle {
		return m
	}
	m.last = p
	m.have = true
	// The verb and the noun both come from the frame, which is what ADR-0015 makes the
	// stream carry and what a Reclamation joining this surface will rely on (#64). Reading
	// one from the frame and the other from the launch handle would let a label be built
	// from two sources that can disagree.
	m.op, m.kind = p.Op, p.Kind
	if p.Sum.Total > 0 {
		m.total = p.Sum.Total
	}
	if p.Done {
		m.phase = finished
	}
	return m
}

// onKey drives R16's cancel and R22's retry. Both are matched against the registry's
// bindings and never a key literal (live-run-feed R7a, AC18).
func (m Model) onKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.CancelRunning):
		if m.phase == finished || m.phase == failed {
			return New(m.profile).WithRetrier(m.retrier).sizedTo(m.width, m.height), nil // dismiss
		}
		if m.cancel == nil || m.stopping {
			return m, nil
		}
		// R16 is a stop, not a repaint: this cancels the context every request of the
		// operation is issued under, so a walk parked on the governor's pacing timer stops
		// waiting and no further write is issued. The surface stays up until the terminal
		// frame reports what the pass actually did.
		cancel := m.cancel
		m.stopping = true
		return m, func() tea.Msg {
			cancel()
			return nil
		}
	case key.Matches(k, m.profile.RetryFailures):
		return m, m.retryCmd()
	}
	return m, nil
}

// retryCmd is R22's keystroke: it hands the finished pass's Summary to the engine and
// returns the resulting handle as a message, which the root routes exactly as it routes
// a launch from a tab. It is nil unless there is a finished pass with recorded failures
// and a seam to re-attempt them through, so the key is inert where it would do nothing.
func (m Model) retryCmd() tea.Cmd {
	if m.phase != finished || m.retrier == nil || !m.have || m.last.Sum.FailedCount() == 0 {
		return nil
	}
	// The Kind travels with the refusal, because a refused launch is still a launch of a
	// named thing: without it the pane cannot tell a Purge's retry from a Reclamation's.
	r, sum, op, kind := m.retrier, m.last.Sum, m.op, m.kind
	return func() tea.Msg {
		st, err := r.Retry(context.Background(), sum)
		if err != nil {
			return ops.LaunchFailed{Op: op, Kind: kind, Err: err}
		}
		return st
	}
}

// sizedTo carries the laid-out terminal across a reset, so a dismissed pane relaunches at
// the terminal's size rather than at zero.
func (m Model) sizedTo(w, h int) Model {
	m.width, m.height = w, h
	return m
}

// SetProfile adopts the keybinding profile the operator chose in Settings. The surface
// outlives the operator's attention and keeps painting the key that stops a Purge (purge
// R16), so the key it names and the key it answers have to move together and at once.
func (m Model) SetProfile(p keys.Profile) Model {
	m.profile = p
	return m
}
