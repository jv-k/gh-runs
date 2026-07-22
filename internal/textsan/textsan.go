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

// Sanitize drops each ESC-introduced CSI sequence whole, drops a bare ESC, and drops the
// remaining C0 control bytes, including the tab and newline that would otherwise tear a row
// across columns. It never removes a printable rune, so the visible text keeps its
// characters. The fast path returns the input unchanged and allocates nothing when it
// carries no control byte, which is the overwhelmingly common case.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b: // ESC: drop a CSI sequence whole, else drop the bare ESC alone
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++ // the final byte of the CSI sequence
				}
			}
		case c < 0x20 || c == 0x7f: // other C0 control: tab, newline, CR, DEL
			i++
		default:
			b.WriteByte(c) // printable ASCII and UTF-8 continuation bytes pass through
			i++
		}
	}
	return b.String()
}
