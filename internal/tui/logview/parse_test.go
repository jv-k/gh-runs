package logview

import (
	"strings"
	"testing"
)

// countFolds and countMarker read the parsed model directly, white-box, so the assertions
// are over the transformation itself rather than the painted frame (R20).
func countFolds(p parsed) int {
	n := 0
	for _, b := range p.blocks {
		if b.isFold {
			n++
		}
	}
	return n
}

func countMarker(p parsed, m markerKind) int {
	n := 0
	for _, b := range p.blocks {
		if b.isFold {
			for _, l := range b.fold.body {
				if l.marker == m {
					n++
				}
			}
			continue
		}
		if b.line.marker == m {
			n++
		}
	}
	return n
}

// TestTimestampPrefixIsTwentyNineChars pins the measured constraint that anchors R4: the
// timestamp prefix is 29 characters including its trailing space. The count is read off the
// fixture constant, so a fixture that drifted from the measurement fails here rather than
// silently changing what "strip the prefix" means.
func TestTimestampPrefixIsTwentyNineChars(t *testing.T) {
	if got := len([]rune(tsA)); got != 29 {
		t.Fatalf("timestamp prefix is %d chars, want the measured 29 (R4, constraints table)", got)
	}
}

// TestParseStripsBOM pins R3: the BOM is stripped before anything else, so it is never part
// of the first line's content. The first block's first rendered content carries no U+FEFF at
// any offset, and neither does its timestamp prefix.
func TestParseStripsBOM(t *testing.T) {
	p := parseLog(trivialLog())
	if len(p.blocks) == 0 {
		t.Fatal("parse produced no blocks")
	}
	first := p.blocks[0]
	var text string
	if first.isFold {
		text = first.fold.prefix + first.fold.label
	} else {
		text = first.line.prefix + first.line.text
	}
	if strings.ContainsRune(text, '\uFEFF') {
		t.Errorf("the first block still carries the BOM: %q (R3)", text)
	}
}

// TestParseSplitsTimestampByDefault pins R4 and AC3: every line's 29-char timestamp is split
// off into the prefix, so the display text never begins with an ISO-8601 timestamp, and the
// prefix restores the exact bytes the API sent. Concatenating the prefix and the content
// reproduces the raw line, which is what a timestamps-on view shows.
func TestParseSplitsTimestampByDefault(t *testing.T) {
	p := parseLog(trivialLog())
	sawPrefix := false
	for _, b := range p.blocks {
		lines := blockLines(b)
		for _, pair := range lines {
			if pair.prefix != "" {
				sawPrefix = true
				if pair.prefix != tsA {
					t.Errorf("timestamp prefix = %q, want the byte-identical %q (R4, AC3)", pair.prefix, tsA)
				}
			}
			if strings.HasPrefix(pair.text, "2026-") {
				t.Errorf("content still begins with a timestamp: %q (R4, AC3)", pair.text)
			}
		}
	}
	if !sawPrefix {
		t.Fatal("no line carried a timestamp prefix; the fixture or the split is wrong (R4)")
	}
}

// blockLines flattens a block to its label and body lines with their prefixes, for the
// timestamp assertions.
func blockLines(b block) []logLine {
	if !b.isFold {
		return []logLine{b.line}
	}
	out := []logLine{{prefix: b.fold.prefix, text: b.fold.label}}
	return append(out, b.fold.body...)
}

// TestParseFoldsGroupsAndCountsWarnings pins AC2's structural claims: the trivial log folds
// into exactly twelve ##[group] spans and carries exactly two ##[warning] lines. Both counts
// are read from the fixture the test built (the labels slice has twelve entries, two
// warnings are inserted), never a constant, so a fixture change moves the expectation with
// it.
func TestParseFoldsGroupsAndCountsWarnings(t *testing.T) {
	raw := trivialLog()
	p := parseLog(raw)

	wantFolds := strings.Count(string(raw), "##[group]")
	if got := countFolds(p); got != wantFolds {
		t.Errorf("parsed %d folds, want %d (one per ##[group], AC2)", got, wantFolds)
	}
	if wantFolds != 12 {
		t.Errorf("the trivial fixture has %d groups, but the measured log has 12 (AC2)", wantFolds)
	}

	wantWarnings := strings.Count(string(raw), "##[warning]")
	if got := countMarker(p, markerWarning); got != wantWarnings {
		t.Errorf("parsed %d warning lines, want %d (AC2)", got, wantWarnings)
	}
	if wantWarnings != 2 {
		t.Errorf("the trivial fixture has %d warnings, but the measured log has 2 (AC2)", wantWarnings)
	}
}

// TestParseFoldsCollapsedByDefault pins R5: a fold is collapsed by default, so its body is
// held but not shown until it is expanded.
func TestParseFoldsCollapsedByDefault(t *testing.T) {
	p := parseLog(trivialLog())
	for _, b := range p.blocks {
		if b.isFold && b.fold.expanded {
			t.Errorf("fold %q is expanded by default; R5 collapses them", b.fold.label)
		}
	}
}

// TestParseFoldLabelDropsGroupSyntax pins R8: a fold is labelled with the text after the
// ##[group] marker, and the literal ##[group] syntax never becomes the label. The endgroup
// marker is consumed entirely and never appears as a body line.
func TestParseFoldLabelDropsGroupSyntax(t *testing.T) {
	p := parseLog(trivialLog())
	for _, b := range p.blocks {
		if !b.isFold {
			continue
		}
		if strings.Contains(b.fold.label, "##[") {
			t.Errorf("fold label %q carries the ##[...] syntax (R8)", b.fold.label)
		}
		for _, l := range b.fold.body {
			if strings.Contains(l.text, "##[endgroup]") || strings.Contains(l.text, "##[group]") {
				t.Errorf("a fold body line carries a structural marker verbatim: %q (R8)", l.text)
			}
		}
	}
}

// TestParseClassifiesMarkerFamily pins R7 and R8: each styled marker in the family is
// classified and its ##[name] syntax stripped, and an unrecognised ##[set-output] token is
// rendered verbatim because swallowing it would destroy content the tool does not
// understand.
func TestParseClassifiesMarkerFamily(t *testing.T) {
	p := parseLog(styledLog())
	want := map[markerKind]int{
		markerError:   1,
		markerWarning: 1,
		markerNotice:  1,
		markerCommand: 1,
		markerDebug:   1,
	}
	for kind, n := range want {
		if got := countMarker(p, kind); got != n {
			t.Errorf("marker kind %d: parsed %d, want %d (R7, R8)", kind, got, n)
		}
	}
	// The styled markers strip their ##[name] syntax; the unrecognised one keeps it verbatim.
	var sawVerbatim bool
	for _, b := range p.blocks {
		if b.isFold {
			continue
		}
		if strings.Contains(b.line.text, "##[") {
			if !strings.Contains(b.line.text, "##[set-output]") {
				t.Errorf("a recognised marker leaked its ##[...] syntax into the text: %q (R8)", b.line.text)
			}
			sawVerbatim = true
		}
	}
	if !sawVerbatim {
		t.Error("the unrecognised ##[set-output] marker was not rendered verbatim (R8)")
	}
}

// TestParseUnprefixedLineKeepsAllContent pins that a line the API did not prefix keeps its
// whole text and loses no leading characters: the split only fires on the measured pattern
// (R4).
func TestParseUnprefixedLineKeepsAllContent(t *testing.T) {
	p := parseLog([]byte("a bare line with no timestamp\n"))
	if len(p.blocks) != 1 || p.blocks[0].isFold {
		t.Fatalf("expected one plain-line block, got %+v", p.blocks)
	}
	if p.blocks[0].line.prefix != "" {
		t.Errorf("a line with no timestamp gained a prefix %q (R4)", p.blocks[0].line.prefix)
	}
	if p.blocks[0].line.text != "a bare line with no timestamp" {
		t.Errorf("an unprefixed line lost content: %q (R4)", p.blocks[0].line.text)
	}
}
