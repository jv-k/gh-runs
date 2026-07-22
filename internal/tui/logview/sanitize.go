package logview

import (
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// tabWidth is the tabstop a log body's TAB expands to, the conventional eight columns a
// terminal and go test lay their columns out against. R19 strips control bytes, but a TAB is
// the alignment carrier of the commonest CI log format (this repo is Go), so a stripped TAB
// fuses columns that were meant to line up. Expanding it to spaces keeps the columns legible,
// which is what R19's "nothing legible is lost by stripping" actually promises.
const tabWidth = 8

// sanitizeParsed returns a copy of p with every author-controlled string tab-expanded and
// sanitised, computed once when a fetch lands rather than on every frame. parseLog stays the
// pure transformation a golden pins (R20): it strips no control byte and knows nothing of
// textsan. This pass is where R19's stripping and the tab expansion happen, once, before any
// paint, so the render and the in-log search read pre-processed text and never re-sanitise the
// whole log per keystroke (security review, the O(log size) per-frame cost). The blocks are
// rebuilt rather than mutated in place, so a prior model value is never aliased, matching the
// copy-on-write the fold toggles already rely on.
func sanitizeParsed(p parsed) parsed {
	blocks := make([]block, len(p.blocks))
	for i, b := range p.blocks {
		if b.isFold {
			b.fold.prefix = cleanText(b.fold.prefix)
			b.fold.label = cleanText(b.fold.label)
			body := make([]logLine, len(b.fold.body))
			for j, l := range b.fold.body {
				body[j] = cleanLine(l)
			}
			b.fold.body = body
		} else {
			b.line = cleanLine(b.line)
		}
		blocks[i] = b
	}
	return parsed{blocks: blocks}
}

// cleanLine tab-expands and sanitises a content line's timestamp prefix and its display text.
func cleanLine(l logLine) logLine {
	l.prefix = cleanText(l.prefix)
	l.text = cleanText(l.text)
	return l
}

// cleanText expands a string's tabs to spaces, then strips its terminal control bytes (R19).
// The expansion runs first on purpose: textsan removes a TAB as a C0 control, so a TAB left for
// the sanitiser is deleted, columns and all. Expanding it beforehand hands the sanitiser only
// spaces, which it keeps, so alignment survives and no raw control byte does. ESC, CSI, CR and
// the rest of C0 and C1 are stripped exactly as before, so TAB is the only control this pass
// turns into anything visible (security review).
func cleanText(s string) string {
	return textsan.Sanitize(expandTabs(s, tabWidth))
}

// expandTabs replaces every TAB with the spaces that advance to the next multiple of width,
// counting columns from the start of s by rune, the same measure wrapPlain wraps by. The fast
// path returns s untouched when it holds no TAB, the overwhelmingly common line, so a tabless
// log allocates nothing here. Column counting by rune equals display width for the ASCII a log
// is overwhelmingly made of; a wide rune (CJK, emoji) shifts a following tabstop by a column,
// which nudges alignment and never hides or fuses a character, the same caveat wrapPlain carries.
func expandTabs(s string, width int) string {
	if width < 1 {
		width = 1
	}
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + width)
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := width - col%width
			for k := 0; k < n; k++ {
				b.WriteByte(' ')
			}
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}
