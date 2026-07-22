// Package keys is the single registry of the tool's two keybinding profiles,
// Vim and Standard, declared as data (live-run-feed R7, R7a). It imports nothing
// internal and lives in its own package for the reason clock does: it precedes
// the TUI, everything above it reads it, and its AC18 invariants are a property
// of a data table that needs no terminal to check (ADR-0011). key.Binding comes
// from charm.land/bubbles/v2/key, so the package depends on bubbles and on
// nothing else.
//
// The two profiles differ on motion and nowhere else (R7a): Vim moves the cursor
// with k/j and Standard with the arrow keys, and every binding that is not a
// motion is identical in both. That sameness is a single source in code too: the
// shared bindings are built once by shared() and each profile sets only its six
// motion bindings on top, so a fork is impossible rather than merely discouraged.
//
// Every binding the product matches on is drawn from here, and no view may match
// a key literal of its own (R7a); a binding that lives anywhere else is outside
// AC18's reach, which is the only thing the requirement protects. Feature-local
// bindings a later stage owns (log deletion, a Purge-summary retry, the log
// view's fold and timestamp toggles) join this registry when their features name
// them (R7a); the canon names no key literal for them yet, so none is invented
// here.
package keys

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// Profile is one complete set of bindings the user can select (R7: exactly two,
// Vim and Standard, and no others). The motion fields differ between the two
// profiles; every other field is identical in both (R7a).
type Profile struct {
	// Name identifies the profile a user selects. It is Vim or Standard, the
	// only two R7 permits.
	Name string

	// Motion. The one axis on which Vim and Standard disagree (R7a).
	RowUp    key.Binding // Vim k, Standard up
	RowDown  key.Binding // Vim j, Standard down
	PageUp   key.Binding // Vim ctrl+b, Standard pgup
	PageDown key.Binding // Vim ctrl+f, Standard pgdown
	FirstRow key.Binding // Vim g, Standard home
	LastRow  key.Binding // Vim G, Standard end

	// Navigation and actions. Identical in both profiles (R7a's "Everything
	// else" table).
	NextTab      key.Binding // tab: next tab (R2)
	PrevTab      key.Binding // shift+tab: previous tab (R2)
	SelectTab    key.Binding // 1/2/3: jump to a tab by position (R2)
	Settings     key.Binding // ,: settings, reachable from any tab (R2)
	ToggleSelect key.Binding // space: toggle row selection (purge R4)
	Refresh      key.Binding // r: apply deferred changes, refresh (R10, R11)
	OpenDetail   key.Binding // enter: open Run detail (BUILD-ORDER stage 8)
	Filter       key.Binding // /: filter (R22, R23)
	Help         key.Binding // ?: help (bubbles/help renders the registry)
	Quit         key.Binding // q, ctrl+c: quit, and ctrl+c binds nothing else (R7)

	// Confirm modal. Identical in both profiles (R7a's "Confirm modal" table).
	// The count typed at or above the threshold (purge R7) is numeric input, not
	// a keystroke, so it has no binding here.
	ConfirmAccept       key.Binding // y: confirm below the threshold (purge R7, AC6)
	ConfirmAbort        key.Binding // n, esc: abort (purge AC6)
	ConfirmAbortDefault key.Binding // enter: abort on the default, which is no (purge AC6)
	ConfirmInspect      key.Binding // v: inspect the frozen set (purge R30)
}

// shared returns a Profile carrying every binding that is identical in both
// profiles (R7a). Vim and Standard each start from this and set only their six
// motion bindings, so "differ on motion, and nowhere else" holds by construction.
func shared(name string) Profile {
	return Profile{
		Name:         name,
		NextTab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		PrevTab:      key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous tab")),
		SelectTab:    key.NewBinding(key.WithKeys("1", "2", "3"), key.WithHelp("1/2/3", "jump to tab")),
		Settings:     key.NewBinding(key.WithKeys(","), key.WithHelp(",", "settings")),
		ToggleSelect: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select")),
		Refresh:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		OpenDetail:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open detail")),
		Filter:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		ConfirmAccept:       key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
		ConfirmAbort:        key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel")),
		ConfirmAbortDefault: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "cancel (default)")),
		ConfirmInspect:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "inspect")),
	}
}

// Vim is the Vim-motion profile: k/j to move a row, ctrl+b/ctrl+f to page,
// g/G to reach the first and last row (R7a).
var Vim = vimProfile()

// Standard is the arrow-key profile: up/down to move a row, pgup/pgdown to page,
// home/end to reach the first and last row (R7a).
var Standard = standardProfile()

func vimProfile() Profile {
	p := shared("Vim")
	p.RowUp = key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "up"))
	p.RowDown = key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "down"))
	p.PageUp = key.NewBinding(key.WithKeys("ctrl+b"), key.WithHelp("ctrl+b", "page up"))
	p.PageDown = key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("ctrl+f", "page down"))
	p.FirstRow = key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "first row"))
	p.LastRow = key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "last row"))
	return p
}

func standardProfile() Profile {
	p := shared("Standard")
	p.RowUp = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up"))
	p.RowDown = key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down"))
	p.PageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up"))
	p.PageDown = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down"))
	p.FirstRow = key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first row"))
	p.LastRow = key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last row"))
	return p
}

// Profiles returns the two selectable profiles, Vim then Standard. R7 permits
// exactly these two and no others, so AC18's "exactly two profiles are
// selectable" is asserted over this. It returns a fresh slice so a caller cannot
// alter the registry.
func Profiles() []Profile {
	return []Profile{Vim, Standard}
}

// ForName returns the profile a stored settings value selects, matching Name
// without regard to case, and reports whether one was found. A name that is
// neither Vim nor Standard resolves to nothing, because R7 permits no third
// profile.
func ForName(name string) (Profile, bool) {
	for _, p := range Profiles() {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Profile{}, false
}

// Bindings returns every binding in the profile in a stable order: the six
// motion bindings, then the shared navigation and action bindings, then the
// confirm-modal bindings. It is the enumeration AC18 checks its invariants over.
// "No binding in either profile carries a key naming Cmd" is a claim about the
// whole registry, and a binding this method omitted would escape that check,
// which is the scattered-binding gap R7a's single registry exists to close. It
// returns a fresh slice.
func (p Profile) Bindings() []key.Binding {
	return []key.Binding{
		p.RowUp, p.RowDown, p.PageUp, p.PageDown, p.FirstRow, p.LastRow,
		p.NextTab, p.PrevTab, p.SelectTab, p.Settings, p.ToggleSelect,
		p.Refresh, p.OpenDetail, p.Filter, p.Help, p.Quit,
		p.ConfirmAccept, p.ConfirmAbort, p.ConfirmAbortDefault, p.ConfirmInspect,
	}
}
