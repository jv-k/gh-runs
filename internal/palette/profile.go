package palette

import (
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/colorprofile"

	"github.com/jv-k/gh-runs/v2/internal/config"
)

// colourVars are the three variables R15a resolves itself. They are stripped before
// detection and read only by ColorProfile, so exactly one place in the program decides what
// they mean.
var colourVars = []string{"NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE"}

// DetectCapability reports what the output stream can carry: a terminal, its TERM, its
// terminfo, tmux. It is deliberately not what the operator asked for, so the three colour
// variables are stripped before the library sees them.
//
// Passing a bare colorprofile.Detect into ColorProfile would look right and be wrong.
// Detect applies its own NO_COLOR, CLICOLOR and CLICOLOR_FORCE limbs first, so the
// library's reading of those variables would arrive already baked into the number our
// ladder then caps. Measured: a piped stream with NO_COLOR=1 and CLICOLOR_FORCE=1 detects
// as ANSI256, which a cap lowers to Ascii, where R15a wants the NoTTY the stream actually
// is. That difference is bold escapes written into a redirected file.
func DetectCapability(out io.Writer, environ []string) colorprofile.Profile {
	return colorprofile.Detect(out, withoutColourVars(environ))
}

// withoutColourVars returns environ with the three colour variables removed, leaving
// everything detection legitimately reads (TERM, COLORTERM, TMUX, TTY_FORCE and the rest).
func withoutColourVars(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !isColourVar(name) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func isColourVar(name string) bool {
	for _, v := range colourVars {
		if name == v {
			return true
		}
	}
	return false
}

// ColorProfile resolves the colour profile the tool hands Bubble Tea through
// tea.WithColorProfile, rather than leaving it to colorprofile.Detect (settings R15a).
// detected is what the stream can carry, which DetectCapability supplies, so this stays a
// pure function of the environment.
//
// The order is R15a's, and every step of it is a case the pinned library resolves
// differently (ADR-0013):
//
//  1. NO_COLOR present at any value, the empty string included, caps the profile at Ascii.
//     The library reads it through strconv.ParseBool, so NO_COLOR=yes and NO_COLOR= do not
//     suppress there and NO_COLOR=0 does not either, and it gates the test on the output
//     being a terminal. R15 follows gh, which is any value, and gates on nothing.
//  2. Else CLICOLOR_FORCE at a value that is not false keeps colour where the output is not
//     a terminal. It sits below NO_COLOR, where the library lets it override NO_COLOR outright.
//  3. Else CLICOLOR=0 caps at Ascii. The library's CLICOLOR limb only ever raises a profile,
//     so this value is inert there.
//  4. Else what the stream can carry stands.
//
// Cap is the operative word and it is never set: a profile already lower stays lower, so
// NO_COLOR on a piped stream leaves NoTTY alone rather than promoting it to Ascii. Ascii
// keeps text decoration, which is what no-color.org asks for and what R15 means: bold
// survives, colour does not.
//
// The theme is not a parameter, and that is R15's direction expressed as a signature. A
// theme picks which colour set is painted (ResolveAppearance) and never whether colour is
// emitted, so this resolver cannot see one, and no arrangement of themes can raise a capped
// profile (AC9).
func ColorProfile(env config.Env, detected colorprofile.Profile) colorprofile.Profile {
	switch {
	case present(env, "NO_COLOR"):
		return capProfile(detected, colorprofile.Ascii)
	case truthy(env, "CLICOLOR_FORCE"):
		return raiseProfile(detected, colorprofile.ANSI)
	case falsey(env, "CLICOLOR"):
		return capProfile(detected, colorprofile.Ascii)
	default:
		return detected
	}
}

// present reports whether a variable is set at any value, including the empty string. This
// is gh's reading of NO_COLOR, verified on gh 2.92.0, and it is deliberately neither
// no-color.org's (which wants a non-empty value) nor ParseBool's (which rejects yes).
func present(env config.Env, key string) bool {
	_, ok := env(key)
	return ok
}

// truthy reports whether a variable is set to something that is not false. bixense's
// CLICOLOR_FORCE says any non-zero value forces, and an unset or empty variable does not.
func truthy(env config.Env, key string) bool {
	v, ok := env(key)
	if !ok || v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err != nil || b
}

// falsey reports whether a variable is set to something that reads as false. bixense's
// CLICOLOR=0 disables, and this accepts the other spellings ParseBool knows, so
// CLICOLOR=false disables too. A value ParseBool does not recognise is not a disable.
func falsey(env config.Env, key string) bool {
	v, ok := env(key)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && !b
}

// capProfile lowers p to at most limit, and never raises it.
func capProfile(p, limit colorprofile.Profile) colorprofile.Profile {
	if p > limit {
		return limit
	}
	return p
}

// raiseProfile lifts p to at least floor, and never lowers it.
func raiseProfile(p, floor colorprofile.Profile) colorprofile.Profile {
	if p < floor {
		return floor
	}
	return p
}
