package palette_test

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/palette"
)

// These pin settings R15, R15a and AC9. The resolver is ours because the pinned library
// resolves NO_COLOR through strconv.ParseBool and against R15 (ADR-0013), so every case
// here is one the library gets wrong or one that proves we did not inherit its answer.

// envMap returns an Env backed by a map, the same shape config.Load takes, so a test names
// the variables it sets without touching the process environment.
func envMap(m map[string]string) config.Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// TestDetectCapabilityIgnoresTheColourVariables pins the input R15a's ladder needs: what
// the output stream can carry, and nothing about what the operator asked for. The pinned
// library applies its own NO_COLOR, CLICOLOR and CLICOLOR_FORCE limbs inside Detect, so
// passing its answer into the ladder lets the library's reading of those variables through
// the back door. Measured: a piped stream with NO_COLOR=1 and CLICOLOR_FORCE=1 detects as
// ANSI256, which our cap would then lower to Ascii, where R15a wants NoTTY left alone.
func TestDetectCapabilityIgnoresTheColourVariables(t *testing.T) {
	var piped bytes.Buffer
	environ := []string{"TERM=xterm-256color", "NO_COLOR=1", "CLICOLOR_FORCE=1", "CLICOLOR=0"}

	if got := colorprofile.Detect(&piped, environ); got == colorprofile.NoTTY {
		t.Skip("the library no longer raises a piped stream on CLICOLOR_FORCE; the guard below is moot")
	}
	if got := palette.DetectCapability(&piped, environ); got != colorprofile.NoTTY {
		t.Errorf("DetectCapability on a piped stream = %v, want NoTTY; the colour variables must not reach Detect", got)
	}
	if got, want := palette.DetectCapability(&piped, environ), colorprofile.Detect(&piped, []string{"TERM=xterm-256color"}); got != want {
		t.Errorf("DetectCapability = %v, want %v: the same answer as detection with the variables absent", got, want)
	}

	// A terminal still detects what it can carry. TTY_FORCE is the library's own way to say
	// the stream is a terminal, and it is capability rather than preference, so it stays.
	tty := []string{"TERM=xterm-256color", "TTY_FORCE=1", "NO_COLOR=1"}
	if got := palette.DetectCapability(&piped, tty); got != colorprofile.ANSI256 {
		t.Errorf("DetectCapability on a forced terminal = %v, want ANSI256", got)
	}
}

// TestPipedNoColorStaysNoTTY pins R15a's "cap is never set" through the composition the
// program actually runs, which is where it broke: detection, then the ladder. The middle
// row is reachable in production through GH_FORCE_TTY, which makes go-gh report a terminal
// while the stream is a file, and it must not write bold escapes into that file.
func TestPipedNoColorStaysNoTTY(t *testing.T) {
	var piped bytes.Buffer
	for _, tc := range []struct {
		name string
		vars map[string]string
	}{
		{"no colour variables", map[string]string{}},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1"}},
		{"NO_COLOR and CLICOLOR_FORCE", map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}},
	} {
		environ := []string{"TERM=xterm-256color"}
		for k, v := range tc.vars {
			environ = append(environ, k+"="+v)
		}
		got := palette.ColorProfile(envMap(tc.vars), palette.DetectCapability(&piped, environ))
		if got != colorprofile.NoTTY {
			t.Errorf("%s piped resolved to %v, want NoTTY left alone (R15a)", tc.name, got)
		}
	}
}

// TestNoColorCapsAtAscii pins R15 and AC9: NO_COLOR at any value, the empty string and yes
// and 0 among them, caps a terminal's profile at Ascii. Accessibility is not a preference
// to be configured away.
func TestNoColorCapsAtAscii(t *testing.T) {
	for _, value := range []string{"", "1", "yes", "0", "true", "false"} {
		env := envMap(map[string]string{"NO_COLOR": value})
		if got := palette.ColorProfile(env, colorprofile.TrueColor); got > colorprofile.Ascii {
			t.Errorf("NO_COLOR=%q resolved to %v, want no higher than Ascii (R15, AC9)", value, got)
		}
	}
}

// TestNoColorBeatsCliColorForce pins AC9's second half, the case the library gets wrong:
// with both set, no colour is emitted. colorprofile v0.4.3 leaves CLICOLOR_FORCE ungated
// and returns full truecolour there.
func TestNoColorBeatsCliColorForce(t *testing.T) {
	env := envMap(map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"})
	if got := palette.ColorProfile(env, colorprofile.TrueColor); got != colorprofile.Ascii {
		t.Errorf("NO_COLOR with CLICOLOR_FORCE resolved to %v, want Ascii (AC9)", got)
	}
	if got := palette.ColorProfile(env, colorprofile.NoTTY); got != colorprofile.NoTTY {
		t.Errorf("piped NO_COLOR with CLICOLOR_FORCE resolved to %v, want NoTTY (R15a)", got)
	}
}

// TestCliColorForceKeepsColourWhenPiped pins R15a step 2: CLICOLOR_FORCE at a value that is
// not false keeps colour where the output is not a terminal, and a false value does not force.
func TestCliColorForceKeepsColourWhenPiped(t *testing.T) {
	forced := envMap(map[string]string{"CLICOLOR_FORCE": "1"})
	if got := palette.ColorProfile(forced, colorprofile.NoTTY); got < colorprofile.ANSI {
		t.Errorf("CLICOLOR_FORCE=1 on a piped stream resolved to %v, want at least ANSI (R15a)", got)
	}
	if got := palette.ColorProfile(forced, colorprofile.TrueColor); got != colorprofile.TrueColor {
		t.Errorf("CLICOLOR_FORCE=1 lowered a detected TrueColor to %v; it raises and never lowers", got)
	}

	off := envMap(map[string]string{"CLICOLOR_FORCE": "0"})
	if got := palette.ColorProfile(off, colorprofile.NoTTY); got != colorprofile.NoTTY {
		t.Errorf("CLICOLOR_FORCE=0 forced colour, resolving to %v; a false value does not force (R15a)", got)
	}
}

// TestCliColorZeroCapsAtAscii pins R15a step 3, the limb the library treats as inert:
// CLICOLOR=0 caps at Ascii, and any other value leaves detection alone.
func TestCliColorZeroCapsAtAscii(t *testing.T) {
	off := envMap(map[string]string{"CLICOLOR": "0"})
	if got := palette.ColorProfile(off, colorprofile.TrueColor); got != colorprofile.Ascii {
		t.Errorf("CLICOLOR=0 resolved to %v, want Ascii (R15a)", got)
	}

	on := envMap(map[string]string{"CLICOLOR": "1"})
	if got := palette.ColorProfile(on, colorprofile.TrueColor); got != colorprofile.TrueColor {
		t.Errorf("CLICOLOR=1 resolved to %v, want the detected TrueColor to stand", got)
	}
}

// TestDetectionStandsWhenNothingCaps pins R15a step 4: with none of the three variables
// set, what the stream can carry is what the program is handed.
func TestDetectionStandsWhenNothingCaps(t *testing.T) {
	env := envMap(map[string]string{})
	for _, want := range []colorprofile.Profile{
		colorprofile.NoTTY, colorprofile.Ascii, colorprofile.ANSI,
		colorprofile.ANSI256, colorprofile.TrueColor,
	} {
		if got := palette.ColorProfile(env, want); got != want {
			t.Errorf("with no colour variables set, %v was resolved to %v; detection stands (R15a)", want, got)
		}
	}
}
