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
// motion is identical in both. Every binding the product matches on is drawn from
// here, and no view may match a key literal of its own (R7a); a binding that
// lives anywhere else is outside AC18's reach, which is the only thing the
// requirement protects.
package keys

import "charm.land/bubbles/v2/key"

// Profile is one complete set of bindings the user can select (R7: exactly two,
// Vim and Standard, and no others). Its fields are the actions the canon names.
// The motion fields differ between the two profiles; every other field is
// identical in both (R7a).
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
}

// Vim is the Vim-motion profile: k/j to move a row, ctrl+b/ctrl+f to page,
// g/G to reach the first and last row (R7a).
var Vim = Profile{
	Name:     "Vim",
	RowUp:    key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "up")),
	RowDown:  key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "down")),
	PageUp:   key.NewBinding(key.WithKeys("ctrl+b"), key.WithHelp("ctrl+b", "page up")),
	PageDown: key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("ctrl+f", "page down")),
	FirstRow: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "first row")),
	LastRow:  key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "last row")),
}

// Standard is the arrow-key profile: up/down to move a row, pgup/pgdown to page,
// home/end to reach the first and last row (R7a).
var Standard = Profile{
	Name:     "Standard",
	RowUp:    key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
	RowDown:  key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
	PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
	PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
	FirstRow: key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first row")),
	LastRow:  key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last row")),
}
