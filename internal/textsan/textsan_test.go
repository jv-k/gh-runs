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
