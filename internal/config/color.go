package config

import "github.com/charmbracelet/colorprofile"

// ColorProfile resolves the colour profile the tool hands Bubble Tea through
// tea.WithColorProfile, rather than leaving it to colorprofile.Detect (settings R15a).
// detected is what detection reported for the output stream, so the caller owns the one
// thing that touches a terminal and this stays a pure function of the environment.
//
// The order is R15a's, and every step of it is a case the pinned library resolves
// differently (ADR-0013):
//
//  1. NO_COLOR present at any value, the empty string included, caps the profile at Ascii.
//     The library reads it through strconv.ParseBool, so NO_COLOR=yes and NO_COLOR= do not
//     suppress there and NO_COLOR=0 does not either, and it gates the test on the output
//     being a terminal. R15 follows gh, which is any value, and gates on nothing.
//  2. Else CLICOLOR_FORCE at a non-zero value keeps colour where the output is not a
//     terminal. It sits below NO_COLOR, where the library lets it override NO_COLOR outright.
//  3. Else CLICOLOR=0 caps at Ascii. The library's CLICOLOR limb only ever raises a profile,
//     so this value is inert there.
//  4. Else what detection reported stands.
//
// Cap is the operative word and it is never set: a profile already lower stays lower, so
// NO_COLOR on a piped stream leaves NoTTY alone rather than promoting it to Ascii. Ascii
// keeps text decoration, which is what no-color.org asks for and what R15 means: bold
// survives, colour does not.
//
// The theme is an input and never an influence. R6's three members choose a palette, and a
// palette is painted through a profile rather than being one, so no theme raises a capped
// profile and none lowers an uncapped one. That is R15's direction stated in the one place
// it could be got backwards: NO_COLOR overrides the theme, and the theme overrides nothing
// (AC9, TestThemeNeverMovesTheProfile).
func ColorProfile(env Env, theme Theme, detected colorprofile.Profile) colorprofile.Profile {
	switch {
	case isSet(env, "NO_COLOR"):
		return capProfile(detected, colorprofile.Ascii)
	case isNonZero(env, "CLICOLOR_FORCE"):
		return raiseProfile(detected, colorprofile.ANSI)
	case isZero(env, "CLICOLOR"):
		return capProfile(detected, colorprofile.Ascii)
	default:
		return detected
	}
}

// isSet reports whether a variable is present at any value, including the empty string.
// This is gh's reading of NO_COLOR, verified on gh 2.92.0, and it is deliberately not
// no-color.org's (which wants a non-empty value) nor ParseBool's (which rejects yes).
func isSet(env Env, key string) bool {
	_, ok := env(key)
	return ok
}

// isNonZero reports whether a variable is present at a value that is neither empty nor 0,
// which is bixense's reading of CLICOLOR_FORCE.
func isNonZero(env Env, key string) bool {
	v, ok := env(key)
	return ok && v != "" && v != "0"
}

// isZero reports whether a variable is present at exactly 0, which is bixense's reading of
// CLICOLOR=0.
func isZero(env Env, key string) bool {
	v, ok := env(key)
	return ok && v == "0"
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
