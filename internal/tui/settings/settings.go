// Package settings is the Settings pane, a view over the config file stage 0 already
// loads (settings R17, BUILD-ORDER stage 13). It is a pane, not a tab and not a
// tea.Model: it exposes View() string and an Update the root drives, and the root opens
// it over whichever tab is focused because a setting reachable from any tab cannot belong
// to one (ADR-0011's pane contract, R2). It imports config and keys and no tab, and
// nothing routes back to whatever opened it.
//
// It is the view, never the loader: config owns the file's precedence, defaults and
// diagnostics (R3, R4, R14), and this pane edits the resolved Config and writes changed
// keys back through config.Save, which preserves comments, key order and keys this version
// does not recognise (R17, AC11). The only write is that local file write; the pane issues
// no request and touches no API. No secret is written, because config.Save marshals only
// display and behaviour choices and tokens never enter the file (R2).
//
// The 2.0.0 menu carries no notification options. R11 renders notifications R4's events,
// and both defer to 2.1 ([ADR-0013]); a toggle with no subsystem behind it is the
// do-nothing switch notifications R13 refuses, and R18's golden asserts the view never
// renders one.
package settings

import (
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// SaveFunc persists the settings the view changed, writing only the keys that differ
// between prev and next (settings R17, AC11). main.go binds it to config.Save over the
// resolved config path; a test injects a fake that records the write. A nil SaveFunc makes
// the pane edit in memory alone, which is what a headless render test wants.
type SaveFunc func(prev, next config.Config) error

// row identifies one setting the view shows, in the fixed top-to-bottom order the
// constants declare. The order is the view's, and CursorKey exposes each row's config.yml
// key so the root and the tests name a setting rather than an index. None of these rows is
// a rejected setting (R13): the struct config models has no field for one, so none can
// appear here, which is half of what makes R18's absence hold by construction.
type row int

const (
	rowBudget row = iota
	rowProfile
	rowWorkflowsScope
	rowStorageScope
	rowConfirmThreshold
	rowBreakerFailures
	rowDiscoveryRefresh
	rowCount
)

// isSelector reports whether the row cycles through a small fixed set (space changes it),
// as opposed to a numeric row that opens an editor (enter).
func (r row) isSelector() bool {
	switch r {
	case rowBudget, rowProfile, rowWorkflowsScope, rowStorageScope:
		return true
	default:
		return false
	}
}

// isNumber reports whether the row holds a bounded integer, edited by typing (R12, R20,
// R21), the confirm-pane's typed-count pattern applied to a setting.
func (r row) isNumber() bool {
	switch r {
	case rowConfirmThreshold, rowBreakerFailures, rowDiscoveryRefresh:
		return true
	default:
		return false
	}
}

// configKey is the config.yml key the row maps to, the same spelling config.Save writes
// and Load reads. It is what CursorKey returns.
func (r row) configKey() string {
	switch r {
	case rowBudget:
		return "budget"
	case rowProfile:
		return "keybinding_profile"
	case rowWorkflowsScope:
		return "workflows_scope"
	case rowStorageScope:
		return "storage_scope"
	case rowConfirmThreshold:
		return "confirm_threshold"
	case rowBreakerFailures:
		return "purge_breaker_failures"
	case rowDiscoveryRefresh:
		return "discovery_refresh_minutes"
	default:
		return ""
	}
}

// Model is the pane's state: the keybinding profile it navigates by, the Config it edits,
// the baseline it diffs a save against, the persister, and the transient cursor and edit
// state. It holds no client and issues no request.
type Model struct {
	profile keys.Profile
	cfg     config.Config
	// initial is the last state known to be on disk, the prev config.Save diffs next
	// against so a write touches only the changed key (AC11). It advances only on a
	// successful save, so a failed write is retried by the next edit rather than dropped.
	initial config.Config
	save    SaveFunc

	open   bool
	cursor row
	width  int
	height int

	// editing and editBuf hold a numeric edit in progress (R12, R20, R21). editBuf
	// collects digits like the confirm pane's typed count, and commit clamps it to the
	// setting's bound before it is adopted.
	editing bool
	editBuf string

	// saveErr is the last write's failure, surfaced in the view rather than swallowed so
	// the operator is not misled into believing a change persisted (R17's spirit).
	saveErr error
}

// New returns a closed pane over the resolved config and the persister. The pane holds the
// Config as the authority for the running instance (R17): it edits this copy and writes it
// back, and does not re-read the file while running.
func New(profile keys.Profile, cfg config.Config, save SaveFunc) Model {
	return Model{profile: profile, cfg: cfg, initial: cfg, save: save}
}

// Open shows the pane, resetting the cursor to the top and clearing any edit or error
// state so a reopened pane never carries a stale entry.
func (m Model) Open() Model {
	m.open = true
	m.cursor = 0
	m.editing = false
	m.editBuf = ""
	m.saveErr = nil
	return m
}

// Close hides the pane. The root calls it when the pane signals it is done (esc), and
// returns focus to the tab underneath.
func (m Model) Close() Model {
	m.open = false
	m.editing = false
	m.editBuf = ""
	return m
}

// IsOpen reports whether the pane is showing, which the root reads to paint it and route
// keys to it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// Config is the settings as the view currently holds them, the authority for the running
// instance (R17). The root reads it, and a test asserts an edit over it.
func (m Model) Config() config.Config { return m.cfg }

// CursorKey is the config.yml key of the setting under the cursor, exposed so the root can
// label the focused row and a test can name a setting rather than count rows.
func (m Model) CursorKey() string { return m.cursor.configKey() }

// Update handles one message the root routed here. It lays out on size, closes on esc,
// moves the cursor, and edits the focused setting. Every action is matched from the
// keybinding registry with key.Matches, never a key literal of its own (R7a, AC18); the
// digits a numeric edit collects are text input, not a binding, exactly as the confirm
// pane's typed count and the Feed's filter are. The pane's only side effect is the local
// config write, done synchronously because it is a few hundred bytes on a user keystroke,
// not per-frame work; it issues no Cmd.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if !m.open {
			return m, nil
		}
		if m.editing {
			return m.handleEditKey(msg), nil
		}
		return m.handleNavKey(msg), nil
	}
	return m, nil
}

// handleNavKey drives navigation and the selector rows. esc closes the pane; the motion
// bindings move the cursor and reach both ends; space cycles the selector under the cursor
// (the dispatch pane's choice-cycling key); enter opens the numeric editor on a number
// row. A key the pane does not bind is ignored, so a stray press cannot mutate a setting.
func (m Model) handleNavKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc: close the pane
		return m.Close()
	case key.Matches(k, m.profile.RowUp):
		m.cursor = clampRow(m.cursor - 1)
	case key.Matches(k, m.profile.RowDown):
		m.cursor = clampRow(m.cursor + 1)
	case key.Matches(k, m.profile.FirstRow):
		m.cursor = 0
	case key.Matches(k, m.profile.LastRow):
		m.cursor = rowCount - 1
	case key.Matches(k, m.profile.ToggleSelect): // space: cycle a selector
		if m.cursor.isSelector() {
			m = m.applyCycle()
			m = m.persist()
		}
	case key.Matches(k, m.profile.OpenDetail): // enter: edit a number
		if m.cursor.isNumber() {
			m.editing = true
			m.editBuf = ""
		}
	}
	return m
}

// handleEditKey drives a numeric edit in progress (R12, R20, R21). Digits build the buffer
// and backspace trims it, mirroring the confirm pane; enter commits, clamping to the
// setting's bound; esc cancels, leaving the setting as it was. esc here does not close the
// pane, exactly as the Feed's esc cancels the filter before it closes anything.
func (m Model) handleEditKey(k tea.KeyPressMsg) Model {
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc: cancel the edit
		m.editing = false
		m.editBuf = ""
	case key.Matches(k, m.profile.OpenDetail): // enter: commit the edit
		m = m.commitNumber()
		m.editing = false
		m.editBuf = ""
	case isDigit(k):
		if len(m.editBuf) < 6 { // no setting exceeds three digits; six is slack against a fat finger
			m.editBuf += k.String()
		}
	case k.Code == tea.KeyBackspace:
		if n := len(m.editBuf); n > 0 {
			m.editBuf = m.editBuf[:n-1]
		}
	}
	return m
}

// applyCycle advances the selector under the cursor to the next value in its set, wrapping
// at the end (settings R5, R8, R19). Cycling the keybinding profile re-seeds the pane's own
// motion at once, so Vim's j and Standard's arrows take effect live for the very next
// keystroke, the one setting a running view can apply to itself without reaching a tab.
func (m Model) applyCycle() Model {
	switch m.cursor {
	case rowBudget:
		m.cfg.Budget = nextTier(m.cfg.Budget)
	case rowProfile:
		m.cfg.KeybindingProfile = nextProfile(m.cfg.KeybindingProfile)
		if p, ok := keys.ForName(string(m.cfg.KeybindingProfile)); ok {
			m.profile = p
		}
	case rowWorkflowsScope:
		m.cfg.WorkflowsScope = nextScope(m.cfg.WorkflowsScope)
	case rowStorageScope:
		m.cfg.StorageScope = nextScope(m.cfg.StorageScope)
	}
	return m
}

// commitNumber adopts the typed buffer for the numeric row under the cursor, clamped to the
// setting's bound so the running view holds a value the file would accept (R12, R20, R21).
// An empty buffer is no change: enter on an untouched editor leaves the setting alone.
func (m Model) commitNumber() Model {
	if m.editBuf == "" {
		return m
	}
	v, err := strconv.Atoi(m.editBuf)
	if err != nil {
		return m
	}
	switch m.cursor {
	case rowConfirmThreshold:
		m.cfg.ConfirmThreshold = config.ClampConfirmThreshold(v)
	case rowBreakerFailures:
		m.cfg.BreakerFailures = config.ClampBreakerFailures(v)
	case rowDiscoveryRefresh:
		m.cfg.DiscoveryRefreshMinutes = config.ClampDiscoveryRefresh(v)
	}
	return m.persist()
}

// persist writes the changed keys back through config.Save (R17, AC11). The baseline
// advances only on success, so a failed write is retried by the next edit rather than
// silently dropped, and the failure is held for the view to state.
func (m Model) persist() Model {
	if m.save == nil {
		return m
	}
	if err := m.save(m.initial, m.cfg); err != nil {
		m.saveErr = err
		return m
	}
	m.saveErr = nil
	m.initial = m.cfg
	return m
}

// clampRow keeps the cursor within the row range, so the motion keys stop at the ends
// rather than wrap; g and G reach the ends outright.
func clampRow(r row) row {
	if r < 0 {
		return 0
	}
	if r >= rowCount {
		return rowCount - 1
	}
	return r
}

// nextTier, nextProfile and nextScope advance a value to the next in its valid set,
// wrapping, over the exported set config validates against so the view offers exactly what
// the loader accepts (R5, R8, R19).
func nextTier(t config.Tier) config.Tier {
	set := config.Tiers()
	for i, v := range set {
		if v == t {
			return set[(i+1)%len(set)]
		}
	}
	return set[0]
}

func nextProfile(p config.KeybindingProfile) config.KeybindingProfile {
	set := config.KeybindingProfiles()
	for i, v := range set {
		if v == p {
			return set[(i+1)%len(set)]
		}
	}
	return set[0]
}

func nextScope(s config.Scope) config.Scope {
	set := config.Scopes()
	for i, v := range set {
		if v == s {
			return set[(i+1)%len(set)]
		}
	}
	return set[0]
}

// isDigit reports whether k is a plain digit press, the only text a numeric edit accepts,
// the same predicate the confirm pane uses for its typed count (R7).
func isDigit(k tea.KeyPressMsg) bool {
	s := k.String()
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}
