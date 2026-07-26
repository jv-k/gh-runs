// Package palette is what the interface paints with. It holds the two colour sets the
// theme setting chooses between (settings R6) and resolves the colour profile the tool
// hands Bubble Tea (R15a). Both are presentation, and neither is the config file, which is
// why they live here and not in config: a colour library has no business in the import
// path of the governor, the scheduler and the CLI, which merely read a setting
// (ADR-0011's Consequences).
//
// It imports config for the Theme and the Env, and nothing else of ours. Every tui package
// draws its colours from here, and main.go resolves the profile through it.
//
// # The ambient appearance
//
// A colour role here is a pair, a dark-background value and a light-background one, and it
// resolves at render time through the ambient appearance rather than at construction. That
// is what lets a style stay a package-level var in each view while the theme still reaches
// it, and it is the pattern lipgloss ships for the same problem in its own compat package.
// The appearance is written once at startup and again only when the terminal answers with
// its background or the operator changes the theme in the Settings view, all three on the
// Bubble Tea update loop. It is stored atomically so a test may set it, and Use returns the
// restore so a test never leaves one behind.
package palette

import (
	"image/color"
	"sync/atomic"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/config"
)

// Appearance is which of the two colour sets is painted.
type Appearance int32

const (
	// Dark is the set for a dark terminal background, and the default. It is what gh
	// falls back to, and every colour in it is the value the views carried before the
	// theme setting existed, so a golden taken under it is unchanged by this package.
	Dark Appearance = iota
	// Light is the set for a light terminal background.
	Light
)

// String names the appearance for a diagnostic or a test failure.
func (a Appearance) String() string {
	if a == Light {
		return "light"
	}
	return "dark"
}

// current is the ambient appearance. It is atomic because the Settings view writes it from
// the update loop while a golden test may set it, and because a data race here would be a
// race on every frame the product paints.
var current atomic.Int32

// Use switches the ambient appearance and returns the restore for the previous one, so a
// caller that only needs it for a frame cannot leak it. The root model is the only
// production caller: it applies the theme as it is constructed, again when the terminal
// answers with its background, and again when the operator changes the setting. main.go
// resolves the colour profile and deliberately leaves the appearance to the root, because
// the theme the running Settings view holds is the authority (settings R17).
func Use(a Appearance) (restore func()) {
	prev := current.Swap(int32(a))
	return func() { current.Store(prev) }
}

// Current reports the ambient appearance. Every Colour reads it as it renders.
func Current() Appearance { return Appearance(current.Load()) }

// ResolveAppearance is settings R6 in one line: auto derives the palette from the terminal
// background as gh does, and dark and light state it outright. An unrecognised theme cannot
// reach here through config.Load, which rejects one and keeps the default, so it is treated
// as auto rather than painted as a zero value.
func ResolveAppearance(theme config.Theme, terminalIsDark bool) Appearance {
	switch theme {
	case config.ThemeDark:
		return Dark
	case config.ThemeLight:
		return Light
	default:
		if terminalIsDark {
			return Dark
		}
		return Light
	}
}

// Colour is one role in both appearances. It is a color.Color, so a lipgloss style takes it
// wherever a fixed colour went, and it resolves as the style renders rather than when the
// style is built. That is the whole mechanism by which the theme reaches every view.
type Colour struct {
	dark  color.Color
	light color.Color
}

// RGBA resolves the role against the ambient appearance. lipgloss calls it once per render
// of a styled string, and it is a single atomic load.
func (c Colour) RGBA() (r, g, b, a uint32) {
	if Current() == Light {
		return c.light.RGBA()
	}
	return c.dark.RGBA()
}

// pair builds a role from its two values, dark first, because dark is the default and its
// values are the ones the product already shipped.
func pair(dark, light string) Colour {
	return Colour{dark: lipgloss.Color(dark), light: lipgloss.Color(light)}
}

// The roles. Every dark value is the literal the views carried before this package existed,
// so the dark appearance is byte-identical to what every golden already holds. The light
// values are the same hues darkened to carry on white, because a mid-tone chosen against a
// black background is the one that disappears on a light one.
//
// Meaning never rides on colour alone (R16), so none of these carries a distinction that
// its text label does not also carry. They exist to make a frame readable, not to encode.
//
// Four light values sit below WCAG AA's 4.5:1 on white, Failing worst at 3.80:1, measured
// and tracked in issue #93. A role added here should beat that line rather than match these.
// The dark values must not move: every golden in the tree is taken under them.
var (
	// Muted is secondary text: help lines, column rules, a completed or cancelled state.
	Muted = pair("#8a8a8a", "#6c6c6c")
	// Danger is failure, deletion and anything irreversible.
	Danger = pair("#ff5f5f", "#d70000")
	// Warn is a caution short of failure: a timed-out Run, a confirmation's warning line.
	Warn = pair("#ff875f", "#af5f00")
	// Failing is a repository whose polling is failing. It is deliberately not Danger:
	// exhaustion and a failing repository can be on screen together, and in one colour they
	// would read as a single condition (live-run-feed's failed-poll indicator).
	Failing = pair("#ff8700", "#d75f00")
	// Attention is a state waiting on a person: pending, action required, a disabled Workflow.
	Attention = pair("#ffaf00", "#875f00")
	// Queued is the queued Status, distinct from Attention so the two do not read alike.
	Queued = pair("#d7af00", "#6f5300")
	// Accent is the interface's own voice: an affordance, a fold header, the typed count.
	Accent = pair("#00afff", "#005faf")
	// Selected is the cursor and the selection, and the Cache rows in Storage.
	Selected = pair("#5fafff", "#0057d7")
	// Info is an action offered on a row, a step below Accent.
	Info = pair("#5fafd7", "#005f87")
	// Positive is an enabled Workflow and an Artifact row.
	Positive = pair("#87d787", "#008700")
	// Success is an operation that finished cleanly, in a pane that reports one.
	Success = pair("#5fd75f", "#00875f")
	// Passed is the successful Conclusion, and a command line in a log.
	Passed = pair("#5faf5f", "#005f00")
	// Waiting is a Run held for a deployment or an approval, and the approvals badge.
	Waiting = pair("#af87ff", "#5f00d7")
	// Requested is the requested Status, and a notice line in a log.
	Requested = pair("#00d7af", "#005f5f")
	// CursorBackground is the log viewer's current line.
	CursorBackground = pair("#303030", "#d0d0d0")
	// MatchBackground is a search hit in the log viewer.
	MatchBackground = pair("#5f5f00", "#ffff87")
)

// Roles returns every role by name, so a test can assert a property over the whole set
// rather than over the entries someone remembered to list. The map is built per call, so a
// caller cannot reach the package's own values.
func Roles() map[string]Colour {
	return map[string]Colour{
		"Muted":            Muted,
		"Danger":           Danger,
		"Warn":             Warn,
		"Failing":          Failing,
		"Attention":        Attention,
		"Queued":           Queued,
		"Accent":           Accent,
		"Selected":         Selected,
		"Info":             Info,
		"Positive":         Positive,
		"Success":          Success,
		"Passed":           Passed,
		"Waiting":          Waiting,
		"Requested":        Requested,
		"CursorBackground": CursorBackground,
		"MatchBackground":  MatchBackground,
	}
}
