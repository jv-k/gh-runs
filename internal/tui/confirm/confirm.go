// Package confirm is the graduated-friction confirmation pane (purge R4 to R9). It
// is a pane, not a tab and not a tea.Model: it exposes View() string and an Update
// its opener drives, and it is imported by the tabs that open it over a frozen set
// (ADR-0011's pane contract). It renders an ops.Plan it cannot forge and collects an
// ops.Input it cannot fake, and it reaches Execute through neither: the arrow runs
// tab to ops, and this pane only gathers the operator's answer (ADR-0019).
//
// It is one shared component, not four copies. purge R4 to R9 define the friction,
// run-lifecycle R17, storage-reclamation R17 and log-viewer R17 each reuse it
// unchanged, so it is generic over what is being deleted: the only per-Kind fact it
// owns is which cells a row prints, which ADR-0019 names as the one type switch a
// shared component legitimately carries. Its friction is read from the Plan, never
// re-derived, so a set priced at TypedCount cannot be confirmed with a keystroke.
//
// R30's inspect view is a viewport over the same Items Execute is handed, opened on
// the key the modal names. It issues no request and revalidates nothing: the set is
// frozen at Plan time, so there is nothing to update (R30, AC22).
package confirm

import (
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// Outcome is where the operator has taken the confirmation. The opener reads it after
// every key: Pending keeps the modal up, Aborted dismisses it having issued nothing,
// and Confirmed hands back the Input the opener passes to ops.Confirm (purge AC6, AC7).
// Escalated is run-lifecycle R6's chosen escalation from a cancel to a force-cancel: it
// confirms nothing, and the opener re-prices the same frozen set for the harder verb.
type Outcome int

const (
	Pending Outcome = iota
	Confirmed
	Aborted
	Escalated
)

// Model is the pane's state: the Plan it renders, the friction-driven collection
// (the typed count buffer for FrictionTypedCount), and the inspect viewport. It holds
// no client and issues no request; the inspect view is a viewport over held state
// (R30).
type Model struct {
	profile keys.Profile
	plan    ops.Plan
	open    bool

	width  int
	height int

	outcome Outcome
	input   ops.Input

	// typed is the digit buffer for a FrictionTypedCount confirmation. y never starts
	// such a set: only the exact count, typed, does (R7, R7a).
	typed string

	// inspecting is R30's viewport over the frozen set. cursor and top page it, and it
	// offers no selection and no action of its own (AC22).
	inspecting bool
	cursor     int
	top        int

	// unreached and unreachedWhy are run-lifecycle R17a's note: how many selected Runs a
	// by-name resolution never reached, and why. They are not part of the Plan, because an
	// unreached Run is not a member of the frozen set: folding it in would put it in Total
	// and price a set larger than the one being confirmed. Zero means the resolution
	// reached everything, or the operation had no resolution at all.
	unreached    int
	unreachedWhy string
}

// New returns a closed pane over the operator's keybinding profile. It holds no Plan
// until Open.
func New(profile keys.Profile) Model {
	return Model{profile: profile}
}

// Open shows the pane over a Plan, resetting the collection state so a reopened pane
// never carries a prior answer. The friction the Plan priced is what the pane
// enforces (R7, R8).
func (m Model) Open(p ops.Plan) Model {
	m.plan = p
	m.open = true
	m.outcome = Pending
	m.input = ops.NoInput()
	m.typed = ""
	m.inspecting = false
	m.cursor = 0
	m.top = 0
	// The note belongs to one resolution. A pane reopened over a set that resolved cleanly
	// must not still be claiming Runs went unreached, so it resets with the rest.
	m.unreached = 0
	m.unreachedWhy = ""
	return m
}

// WithUnreached carries run-lifecycle R17a's fact into the pane: how many of the selected
// Runs the by-name resolution never reached, and why. It is called after Open, which
// resets it, and it returns a copy so the value stays immutable.
//
// It is a separate call rather than a field on the Plan because an unreached Run is not a
// member of the frozen set. An Unmatched was asked and answered no; an unreached Run was
// never asked, and counting the two together would price a set larger than the one the
// operator confirms, which is the ruling R17a exists to make.
//
// The note it produces neither blocks nor confirms, on R14b's terms. A zero count renders
// nothing, so every other operation is unaffected.
func (m Model) WithUnreached(n int, why string) Model {
	m.unreached = n
	m.unreachedWhy = why
	return m
}

// Close hides the pane. The opener calls it once it has acted on a terminal Outcome.
func (m Model) Close() Model {
	m.open = false
	return m
}

// IsOpen reports whether the pane is showing, which the opener reads to paint it and
// route keys to it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// Outcome reports where the confirmation stands (purge AC6, AC7).
func (m Model) Outcome() Outcome { return m.outcome }

// Input is the explicit act the operator made, valid when Outcome is Confirmed. The
// opener passes it to ops.Confirm, which is the authority that validates it against
// the Plan's friction; the pane's own accept is only the UX gate (ADR-0019).
func (m Model) Input() ops.Input { return m.input }

// Plan is the frozen set the pane is confirming, which the opener passes to
// ops.Confirm and ops.Execute (ADR-0019).
func (m Model) Plan() ops.Plan { return m.plan }

// Inspecting reports whether the R30 viewport is showing rather than the modal.
func (m Model) Inspecting() bool { return m.inspecting }

// Update handles one message the opener routed here. It lays out on size and drives
// the confirmation and the inspect view on keys; it consumes no other message and
// issues no Cmd, because the pane holds no client and the frozen set never updates
// (R30). A key never reaches here once the pane has reached a terminal Outcome,
// because the opener stops routing to it and acts on the Outcome instead.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if !m.open || m.outcome != Pending {
			return m, nil
		}
		if m.inspecting {
			return m.handleInspectKey(msg), nil
		}
		return m.handleModalKey(msg), nil
	}
	return m, nil
}

// handleModalKey drives the confirmation itself, matching against the registry's
// confirm-modal bindings with key.Matches, never a key literal of its own (R7a, AC18).
// The inspect key opens R30's viewport, and the abort keys dismiss the modal having
// issued nothing (AC6). Below that the behaviour is friction-specific.
func (m Model) handleModalKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.ConfirmInspect):
		m.inspecting = true
		m.cursor = 0
		m.top = 0
		return m
	case key.Matches(k, m.profile.ConfirmAbort): // n, esc
		m.outcome = Aborted
		return m
	case m.offersEscalation() && key.Matches(k, m.profile.ForceCancel):
		// run-lifecycle R5, R6, AC6: force-cancel is offered as the escalation the operator
		// chooses, and this is where they choose it. It confirms nothing. The pane does not
		// rewrite its own Plan, because a Plan is ops's to build and its friction is priced
		// for the verb it was built for (ADR-0019); the opener re-prices the same frozen
		// Items, so the escalation carries the friction rather than skipping it.
		m.outcome = Escalated
		return m
	}
	if m.plan.Friction() == ops.FrictionTypedCount {
		return m.handleTypedCountKey(k)
	}
	return m.handleYNKey(k)
}

// offersEscalation reports whether this Plan may escalate to force-cancel, which is a
// plain cancel and nothing else (run-lifecycle R6). A Purge has no escalation, a re-run
// has none, and a force-cancel is already the escalation, so the key is inert on all
// three and cannot pick up a second meaning on a destructive modal.
func (m Model) offersEscalation() bool {
	return m.plan.Operation() == ops.OpCancel
}

// handleYNKey is the below-threshold confirmation: y confirms, and Enter aborts on
// the default, which is no (purge R7, AC6). FrictionNone also lands here and accepts
// y or Enter-as-abort; ops.Confirm accepts any Input at that level, and OpDelete
// never reaches it (ADR-0019).
func (m Model) handleYNKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.ConfirmAccept): // y
		m.outcome = Confirmed
		m.input = ops.Answer("y")
	case key.Matches(k, m.profile.ConfirmAbortDefault): // enter: the default is no
		m.outcome = Aborted
	}
	return m
}

// handleTypedCountKey collects the exact count a high-blast-radius set demands (R7,
// R8). Digits build the buffer and backspace trims it; Enter submits it, confirming
// only when it equals the frozen total the modal displays. y is inert here, so it
// cannot start a typed-count set (R7a: "y must not start it"). The pane's match is
// the UX gate; ops.Confirm re-validates the same string as the authority (ADR-0019).
func (m Model) handleTypedCountKey(k tea.KeyPressMsg) Model {
	switch {
	case isDigit(k):
		m.typed += k.String()
	case k.String() == "backspace":
		if n := len(m.typed); n > 0 {
			m.typed = m.typed[:n-1]
		}
	case key.Matches(k, m.profile.ConfirmAbortDefault): // enter submits the typed count
		if m.typed == strconv.Itoa(m.plan.Total()) {
			m.outcome = Confirmed
			m.input = ops.Answer(m.typed)
		}
	}
	return m
}

// handleInspectKey drives R30's viewport. Motion pages it and reaches both ends
// (g/G, home/end), which is how an operator checks a date filter's boundary. The
// inspect and abort keys return to the modal with the frozen set and the confirmation
// state unchanged; nothing here selects, deletes or confirms (R30, AC22).
func (m Model) handleInspectKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.ConfirmInspect), key.Matches(k, m.profile.ConfirmAbort):
		m.inspecting = false
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
	case key.Matches(k, m.profile.PageUp):
		m.moveCursor(-m.inspectPage())
	case key.Matches(k, m.profile.PageDown):
		m.moveCursor(m.inspectPage())
	case key.Matches(k, m.profile.FirstRow):
		m.cursor = 0
		m.scrollToCursor()
	case key.Matches(k, m.profile.LastRow):
		m.cursor = m.plan.Total() - 1
		m.clampCursor()
		m.scrollToCursor()
	}
	return m
}

// moveCursor moves the inspect cursor by delta, clamped, scrolling the viewport.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	if m.plan.Total() == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= m.plan.Total() {
		m.cursor = m.plan.Total() - 1
	}
}

func (m *Model) scrollToCursor() {
	rows := m.inspectPage()
	if rows <= 0 {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// isDigit reports whether k is a plain digit press, the only text a typed-count
// confirmation accepts (R7).
func isDigit(k tea.KeyPressMsg) bool {
	s := k.String()
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}

// SetProfile adopts the keybinding profile the operator chose in Settings. Motion keys
// change only which keystroke a held frame answers, so the change applies from the next
// keystroke rather than at the next launch, and the root pushes it down the tree it holds
// (settings R5, R17). The pane's own state is untouched: a confirmation mid-typing keeps
// its typed count.
func (m Model) SetProfile(p keys.Profile) Model {
	m.profile = p
	return m
}
