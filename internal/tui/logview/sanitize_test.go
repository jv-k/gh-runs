package logview

import (
	"strings"
	"testing"
)

// TestExpandTabsAlignsToEightColumns pins the tab expansion R19 keeps as layout: a TAB advances
// to the next eight-column tabstop measured from the start of the line, so go test output lines
// up rather than being fused when the sanitiser would otherwise delete the tab. The fast path
// leaves a tabless line untouched.
func TestExpandTabsAlignsToEightColumns(t *testing.T) {
	sp := func(n int) string { return strings.Repeat(" ", n) }
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a tabless line is untouched", "plain line", "plain line"},
		{"the empty string is untouched", "", ""},
		{"a tab at column zero fills a whole tabstop", "\tx", sp(8) + "x"},
		{"a tab advances to the next multiple of eight", "abc\td", "abc" + sp(5) + "d"},
		{"go test columns land on eight-column stops", "ok  \tgithub\t0.42s", "ok" + sp(6) + "github" + sp(2) + "0.42s"},
		{"a tab exactly on a stop still advances a full stop", "12345678\tx", "12345678" + sp(8) + "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandTabs(tc.in, tabWidth); got != tc.want {
				t.Errorf("expandTabs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExpandTabsThenSanitiseKeepsSpacesDropsControls pins the ordering cleanText relies on: the
// expansion runs before textsan, so the TAB becomes spaces textsan keeps, while ESC, CSI, CR
// and the rest of C0 and C1 are still stripped whole (R19). A TAB handed straight to textsan
// would be deleted, columns and all, which is the regression this guards.
func TestExpandTabsThenSanitiseKeepsSpacesDropsControls(t *testing.T) {
	got := cleanText("a\tb\x1b[31mc\x1b[0m\rd")
	if strings.ContainsAny(got, "\x1b\r\t") {
		t.Errorf("cleanText left a control byte in %q (R19)", got)
	}
	// The tab at column one expands to seven spaces (kept), while the CSI colour pair and the CR
	// are stripped whole, so "bcd" runs together where the escapes were.
	want := "a" + strings.Repeat(" ", 7) + "bcd"
	if got != want {
		t.Errorf("cleanText(%q) = %q, want %q (R19)", "a\tb\x1b[31mc\x1b[0m\rd", got, want)
	}
}

// TestFetchSanitisesAndCachesCleanLines pins that the sanitising and tab expansion happen once,
// when the fetch lands, and are cached in the parsed model the pane holds (R19, perf). No render
// or search needs to re-sanitise, because no stored line carries a control byte, and the TAB is
// already spaces. This is the caching the security review's per-frame O(log size) finding asked
// for.
func TestFetchSanitisesAndCachesCleanLines(t *testing.T) {
	m := loaded(t, hostileLog())
	var joined string
	for _, b := range m.log.blocks {
		for _, l := range blockLines(b) {
			if strings.ContainsAny(l.prefix+l.text, "\x1b\r\t") {
				t.Errorf("a cached line carries a raw control byte: prefix=%q text=%q (R19)", l.prefix, l.text)
			}
			joined += l.text
		}
	}
	if !strings.Contains(joined, "retreat tail") {
		t.Errorf("the cached line did not expand the TAB to a space: %q (R19)", joined)
	}
}

// TestGoTestTabsRenderAsAlignedColumns pins the payoff at the render: a go test log, the
// commonest tab-aligned CI output, paints in lined-up columns rather than with its fields fused
// by a deleted tab (R19). github starts at column eight and the timing after the next stop, so
// the eye reads the table the tool ran.
func TestGoTestTabsRenderAsAlignedColumns(t *testing.T) {
	m := loaded(t, styledLog())
	m.noColor = true // strip styling so the columns are plain in the text
	view := m.View()
	if strings.ContainsRune(view, '\t') {
		t.Errorf("a raw TAB reached the rendered go test line; R19 expands it to spaces:\n%q", view)
	}
	want := "ok" + strings.Repeat(" ", 6) + "github.com/example/pkg" + strings.Repeat(" ", 2) + "0.42s"
	if !strings.Contains(view, want) {
		t.Errorf("the go test columns did not align on eight-column tabstops (R19):\n%s", view)
	}
}
