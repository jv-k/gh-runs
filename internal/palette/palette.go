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
	// falls back to. Every colour in it but Muted is the value the views carried before
	// the theme setting existed; Muted moved to clear the contrast floor, which is why
	// most goldens in the tree were regenerated once (settings R22, ADR-0024).
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

// The reference backgrounds settings R22 measures against. They are exported so R22's numbers
// exist in code rather than only in prose, and so the property test reads the same values the
// canon names instead of a copy of them.
//
// They are an assumption and are named as one. The tool paints onto the terminal's own
// background and never sets one: it asks at startup and keeps only whether the answer is dark.
// So these are the worst case committed to rather than the best case citable. Contrast to a
// dark foreground rises as the background lightens, so a role clearing #faf4f2 clears
// solarized-light, #fafafa and white by construction (ADR-0024).
const (
	// ReferenceBackgroundDark is Monokai Pro, the dark appearance's reference.
	ReferenceBackgroundDark = "#2d2a2e"
	// ReferenceBackgroundLight is Monokai Pro Light, the light appearance's reference.
	ReferenceBackgroundLight = "#faf4f2"
)

// Highlight is a foreground and a background painted together, in both appearances. It is one
// thing rather than two Roles, because applying its background applies its foreground with it,
// which is what makes its contrast exactly known instead of a cross product against whatever
// colour the text already carried (CONTEXT.md, settings R22).
//
// That collapse is the point. Six foreground roles could land on the log viewer's search
// match, and the worst of them painted at 1.95:1, which is the worst contrast in the tool and
// the one case it controls completely. A highlighted line loses its severity colour for as
// long as it is highlighted, and that costs nothing the canon requires: R16 already forbids
// meaning riding on colour alone.
type Highlight struct {
	fg Colour
	bg Colour
}

// Foreground is the colour a highlighted run of text is painted in.
func (h Highlight) Foreground() Colour { return h.fg }

// Background is the colour painted behind it.
func (h Highlight) Background() Colour { return h.bg }

// highlight builds a Highlight from its four values, each appearance's foreground beside its
// own background, dark first for the reason pair takes dark first.
func highlight(darkFg, darkBg, lightFg, lightBg string) Highlight {
	return Highlight{
		fg: pair(darkFg, lightFg),
		bg: pair(darkBg, lightBg),
	}
}

// The roles. Every dark value but Muted is the literal the views carried before this package
// existed. The light values are the same hues darkened to carry on a light background, because
// a mid-tone chosen against a black background is the one that disappears on a light one.
//
// Several values are free hex rather than xterm-256 cube entries, and that is a real cost
// accepted rather than an oversight. At the dark end the cube has too few distinct saturated
// colours to give fourteen roles distinct values above the floor: darkening Failing and Warn
// inside it lands both on Attention's value, and Positive and Success land on Passed and
// Requested. On a 256-colour terminal colorprofile quantizes these back toward those same cube
// entries, so that terminal sees a compression the palette cannot prevent (ADR-0024).
//
// Meaning never rides on colour alone (R16), so none of these carries a distinction that
// its text label does not also carry. They exist to make a frame readable, not to encode.
//
// Every value here clears settings R22's floor against its appearance's reference background,
// and a role or highlight added here MUST clear it too. The floor is 4.5:1 by WCAG 2.x for a
// Role, and for a Highlight it is 4.5:1 between its own pair plus CIE76 ΔE 10 from the
// reference background, so the highlight is discernible at all. Both are enforced over the
// whole set by TestEveryRoleClearsTheContrastFloor and TestEveryHighlightClearsBothFloors,
// and the golden beside them records the measured figure for each. Aim at 5.0 rather than at 4.5 where the
// hue allows: the margin is what stops a later adjustment to a reference background dropping
// a role below the line (ADR-0024).
//
// The dark values are no longer frozen. This comment used to say they must not move because
// every golden in the tree is taken under them, and dark Muted at 4.10 was the one role below
// the floor. A colour change never moves a cell width, so regenerating those goldens is a pure
// byte substitution of one SGR triple rather than a review of 65 frames, and the invariant was
// protecting the goldens' convenience rather than anything a user could observe (ADR-0024).
var (
	// Muted is secondary text: help lines, column rules, a completed or cancelled state.
	Muted = pair("#9e9e9e", "#6c6c6c")
	// Danger is failure, deletion and anything irreversible.
	Danger = pair("#ff5f5f", "#d70000")
	// Warn is a caution short of failure: a timed-out Run, a confirmation's warning line.
	Warn = pair("#ff875f", "#8c4a00")
	// Failing is a repository whose polling is failing. It is deliberately not Danger:
	// exhaustion and a failing repository can be on screen together, and in one colour they
	// would read as a single condition (live-run-feed's failed-poll indicator).
	Failing = pair("#ff8700", "#a34700")
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
	Positive = pair("#87d787", "#007a00")
	// Success is an operation that finished cleanly, in a pane that reports one.
	Success = pair("#5fd75f", "#00785a")
	// Passed is the successful Conclusion, and a command line in a log.
	Passed = pair("#5faf5f", "#005f00")
	// Waiting is a Run held for a deployment or an approval, and the approvals badge.
	Waiting = pair("#af87ff", "#5f00d7")
	// Requested is the requested Status, and a notice line in a log.
	Requested = pair("#00d7af", "#005f5f")
)

// The highlights. Each is a foreground and a background painted together, so the contrast a
// highlighted line paints at is exactly these two values rather than whatever severity colour
// the line already carried. The paired foregrounds are #ffffff on both dark backgrounds and
// #1c1c1c on both light ones. Their measured figures live in the golden beside the property
// test, never in this comment, because a figure typed beside a value drifts from it (AC15).
var (
	// Cursor is the log viewer's current line. Its dark background moved from #303030, which
	// is CIE76 ΔE 3.9 from the dark reference background and therefore invisible on the very
	// terminal theme that reference adopts (ADR-0024).
	Cursor = highlight("#ffffff", "#444444", "#1c1c1c", "#d0d0d0")
	// Match is a search hit in the log viewer. Both backgrounds are unchanged: they were
	// always discernible, and it was the foreground landing on them that was not.
	Match = highlight("#ffffff", "#5f5f00", "#1c1c1c", "#ffff87")
)

// Roles returns every role by name, and foregrounds only, so a test can assert a property over
// the whole set rather than over the entries someone remembered to list. The highlights are
// Highlights' to return, because each is a pair and R22 measures a pair differently. The map is
// built per call, so a caller cannot reach the package's own values.
func Roles() map[string]Colour {
	return map[string]Colour{
		"Muted":     Muted,
		"Danger":    Danger,
		"Warn":      Warn,
		"Failing":   Failing,
		"Attention": Attention,
		"Queued":    Queued,
		"Accent":    Accent,
		"Selected":  Selected,
		"Info":      Info,
		"Positive":  Positive,
		"Success":   Success,
		"Passed":    Passed,
		"Waiting":   Waiting,
		"Requested": Requested,
	}
}

// Highlights returns every highlight by name, the companion to Roles and for the same reason:
// a test asserts R22 over the whole set rather than over the entries someone remembered to
// list. Both accessors exist because the two carry different obligations, and a Highlight
// measured as though it were a Role would be measured against the terminal background it never
// touches. The map is built per call, so a caller cannot reach the package's own values.
func Highlights() map[string]Highlight {
	return map[string]Highlight{
		"Cursor": Cursor,
		"Match":  Match,
	}
}
