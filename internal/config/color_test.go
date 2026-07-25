package config_test

import (
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/jv-k/gh-runs/v2/internal/config"
)

// These pin settings R15, R15a and AC9. The resolver is ours because the pinned library
// resolves NO_COLOR through strconv.ParseBool and against R15 (ADR-0013), so every case
// here is one the library gets wrong or one that proves we did not inherit its answer.

// TestNoColorCapsAtAsciiWhateverTheTheme pins R15 and AC9: NO_COLOR at any value, the
// empty string and yes and 0 among them, caps the profile at Ascii for every theme R6
// offers. Accessibility is not a preference to be configured away, so the theme never
// gets a say here.
func TestNoColorCapsAtAsciiWhateverTheTheme(t *testing.T) {
	for _, value := range []string{"", "1", "yes", "0", "true", "false"} {
		for _, theme := range config.Themes() {
			env := envMap(map[string]string{"NO_COLOR": value})
			got := config.ColorProfile(env, theme, colorprofile.TrueColor)
			if got > colorprofile.Ascii {
				t.Errorf("NO_COLOR=%q with theme %q resolved to %v, want no higher than Ascii (R15, AC9)", value, theme, got)
			}
		}
	}
}

// TestNoColorBeatsCliColorForce pins AC9's second half, the case the library gets wrong:
// a piped stream with NO_COLOR and CLICOLOR_FORCE both set renders no colour. colorprofile
// v0.4.3 leaves CLICOLOR_FORCE ungated there and returns full truecolour.
func TestNoColorBeatsCliColorForce(t *testing.T) {
	env := envMap(map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"})

	if got := config.ColorProfile(env, config.ThemeDark, colorprofile.NoTTY); got > colorprofile.Ascii {
		t.Errorf("piped NO_COLOR with CLICOLOR_FORCE resolved to %v, want no higher than Ascii (AC9)", got)
	}
	if got := config.ColorProfile(env, config.ThemeDark, colorprofile.TrueColor); got != colorprofile.Ascii {
		t.Errorf("NO_COLOR with CLICOLOR_FORCE on a terminal resolved to %v, want Ascii (AC9)", got)
	}
}

// TestCapNeverPromotes pins R15a's "cap is never set": a profile already below Ascii stays
// there, so NO_COLOR on a piped stream leaves NoTTY alone rather than raising it.
func TestCapNeverPromotes(t *testing.T) {
	env := envMap(map[string]string{"NO_COLOR": ""})
	if got := config.ColorProfile(env, config.ThemeAuto, colorprofile.NoTTY); got != colorprofile.NoTTY {
		t.Errorf("NO_COLOR on a piped stream resolved to %v, want NoTTY left alone (R15a)", got)
	}
}

// TestCliColorForceKeepsColourWhenPiped pins R15a step 2: CLICOLOR_FORCE at a non-zero
// value keeps colour where the output is not a terminal, and a zero value does not force.
func TestCliColorForceKeepsColourWhenPiped(t *testing.T) {
	forced := envMap(map[string]string{"CLICOLOR_FORCE": "1"})
	if got := config.ColorProfile(forced, config.ThemeAuto, colorprofile.NoTTY); got < colorprofile.ANSI {
		t.Errorf("CLICOLOR_FORCE=1 on a piped stream resolved to %v, want at least ANSI (R15a)", got)
	}
	if got := config.ColorProfile(forced, config.ThemeAuto, colorprofile.TrueColor); got != colorprofile.TrueColor {
		t.Errorf("CLICOLOR_FORCE=1 lowered a detected TrueColor to %v; it raises and never lowers", got)
	}

	off := envMap(map[string]string{"CLICOLOR_FORCE": "0"})
	if got := config.ColorProfile(off, config.ThemeAuto, colorprofile.NoTTY); got != colorprofile.NoTTY {
		t.Errorf("CLICOLOR_FORCE=0 forced colour, resolving to %v; only a non-zero value forces (R15a)", got)
	}
}

// TestCliColorZeroCapsAtAscii pins R15a step 3, the limb the library treats as inert:
// CLICOLOR=0 caps at Ascii, and any other value leaves detection alone.
func TestCliColorZeroCapsAtAscii(t *testing.T) {
	off := envMap(map[string]string{"CLICOLOR": "0"})
	if got := config.ColorProfile(off, config.ThemeAuto, colorprofile.TrueColor); got != colorprofile.Ascii {
		t.Errorf("CLICOLOR=0 resolved to %v, want Ascii (R15a)", got)
	}

	on := envMap(map[string]string{"CLICOLOR": "1"})
	if got := config.ColorProfile(on, config.ThemeAuto, colorprofile.TrueColor); got != colorprofile.TrueColor {
		t.Errorf("CLICOLOR=1 resolved to %v, want the detected TrueColor to stand", got)
	}
}

// TestDetectionStandsWhenNothingCaps pins R15a step 4: with none of the three variables
// set, what detection reported is what the program is handed.
func TestDetectionStandsWhenNothingCaps(t *testing.T) {
	env := envMap(map[string]string{})
	for _, want := range []colorprofile.Profile{
		colorprofile.NoTTY, colorprofile.Ascii, colorprofile.ANSI,
		colorprofile.ANSI256, colorprofile.TrueColor,
	} {
		if got := config.ColorProfile(env, config.ThemeAuto, want); got != want {
			t.Errorf("with no colour variables set, %v was resolved to %v; detection stands (R15a)", want, got)
		}
	}
}

// TestThemeNeverMovesTheProfile pins the direction R15 fixes: the theme is an input to the
// resolver and never an influence on it. R6's members choose a palette, and no member
// raises a capped profile or lowers an uncapped one. NO_COLOR overrides the theme, and the
// theme never overrides anything.
func TestThemeNeverMovesTheProfile(t *testing.T) {
	envs := []map[string]string{
		{},
		{"NO_COLOR": ""},
		{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"},
		{"CLICOLOR_FORCE": "1"},
		{"CLICOLOR": "0"},
	}
	for _, vars := range envs {
		for _, detected := range []colorprofile.Profile{colorprofile.NoTTY, colorprofile.TrueColor} {
			want := config.ColorProfile(envMap(vars), config.ThemeAuto, detected)
			for _, theme := range config.Themes() {
				if got := config.ColorProfile(envMap(vars), theme, detected); got != want {
					t.Errorf("theme %q moved the profile to %v under %v, want %v (R15)", theme, got, vars, want)
				}
			}
		}
	}
}
