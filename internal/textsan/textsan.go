// Package textsan strips terminal control bytes from untrusted text before it is painted.
// Both the CLI's human table (internal/cli) and the live Feed (internal/tui/feed) render
// run-derived strings, a workflow name, a run title, a branch, an event, that an author on
// a fanned-out third-party repository controls. A hostile value carrying ANSI escape
// sequences could move the cursor, erase the screen, rewrite prior lines or forge chrome in
// the operator's terminal (security review). In a tool whose headline is that deletion is
// one operation, a spoofable dashboard is a decision-integrity problem, not a cosmetic one.
//
// The package is a leaf: it imports nothing of ours, so both surfaces depend on it without a
// cycle and neither can drift from the other (ADR-0011). The -q and --json data paths are
// deliberately not routed through it: that output is data a script consumes, not a terminal
// rendering, and gh treats it the same.
package textsan

import "strings"

// Sanitize drops each ESC-introduced CSI sequence whole, drops a bare ESC, and drops every C0
// and C1 control code, including the tab and newline that would otherwise tear a row across
// columns and the 8-bit CSI (U+009B) a terminal in UTF-8 mode would read as a control
// introducer with no ESC in sight (security review). It never removes a printable rune, so the
// visible text keeps its characters. The fast path returns the input unchanged and allocates
// nothing when it carries no control rune, which is the overwhelmingly common case. isStripped
// is the single predicate for both the fast-path scan and the strip below, so the two cannot
// drift, and the loop is over runes so a C1 code point in its two-byte UTF-8 form is one unit.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, isStripped) {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == 0x1b: // ESC: drop a CSI sequence whole, else drop the bare ESC alone
			i++
			if i < len(runes) && runes[i] == '[' {
				i++
				for i < len(runes) && (runes[i] < 0x40 || runes[i] > 0x7e) {
					i++
				}
				if i < len(runes) {
					i++ // the final byte of the CSI sequence
				}
			}
		case isStripped(r): // C0, DEL and C1 control codes
			i++
		default:
			b.WriteRune(r) // every printable rune passes through
			i++
		}
	}
	return b.String()
}

// isStripped reports whether a rune is a control code the sanitiser removes: the C0 range and
// DEL (U+0000 to U+001F, U+007F) and the C1 range (U+0080 to U+009F), which includes the 8-bit
// CSI and OSC. ESC (U+001B) matches too, so the fast path detects a lone ESC, but the strip
// gives ESC its own case so it can consume a trailing CSI sequence whole rather than one rune.
func isStripped(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
