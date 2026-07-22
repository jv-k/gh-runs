package textsan_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// TestSanitizeStripsControlSequences pins the strip: a CSI colour sequence, a clear-screen,
// a cursor-home, a bare ESC, a carriage return and a tab are all removed, and the visible
// text between them survives byte for byte. This is the hostile shape a workflow or run name
// from a third-party repository can carry (security review).
func TestSanitizeStripsControlSequences(t *testing.T) {
	in := "\x1b[31mpwned\x1b[0m\x1b[2J\x1b[Hkeep\rowned\ttail\x1b"
	got := textsan.Sanitize(in)
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("output still carries an ESC byte: %q", got)
	}
	if strings.ContainsRune(got, '\r') || strings.ContainsRune(got, '\t') {
		t.Errorf("output still carries a C0 control byte: %q", got)
	}
	if got != "pwnedkeepownedtail" {
		t.Errorf("Sanitize(%q) = %q, want %q (visible text preserved, controls dropped)", in, got, "pwnedkeepownedtail")
	}
}

// TestSanitizeStripsC1Controls pins that the C1 control codes U+0080 to U+009F are stripped,
// not only the C0 range and the ESC-introduced CSI. U+009B is the 8-bit CSI and U+009D the
// 8-bit OSC: a terminal in UTF-8 mode that honours 8-bit C1 controls would read a bare one as
// a control introducer, so a hostile name carrying it must not survive even though it has no
// ESC (security review). The visible text on either side is preserved.
func TestSanitizeStripsC1Controls(t *testing.T) {
	for _, r := range []rune{0x80, 0x9b, 0x9d, 0x9f} {
		in := "keep" + string(r) + "tail"
		got := textsan.Sanitize(in)
		if strings.ContainsRune(got, r) {
			t.Errorf("Sanitize did not strip C1 control U+%04X: %q", r, got)
		}
		if got != "keeptail" {
			t.Errorf("Sanitize(%q) = %q, want %q (C1 dropped, visible text kept)", in, got, "keeptail")
		}
	}
}

// TestSanitizeLeavesCleanTextUntouched pins that the strip is a no-op on ordinary data: a
// title with no control bytes, multibyte UTF-8 included, is returned unchanged, so no
// printable rune is dropped and the fast path allocates nothing.
func TestSanitizeLeavesCleanTextUntouched(t *testing.T) {
	for _, s := range []string{"Fix the bug", "release v2.0.0 (rc)", "café build works", "kubernetes/kubernetes"} {
		if got := textsan.Sanitize(s); got != s {
			t.Errorf("Sanitize(%q) = %q, want it unchanged", s, got)
		}
	}
}
