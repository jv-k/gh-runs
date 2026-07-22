package logview

import (
	"regexp"
	"strings"
)

// markerKind classifies a log line for R7's distinct styling and R9's text-first label.
// The zero value is an ordinary line. The five styled kinds are exactly the non-structural
// members of R8's marker family; group and endgroup are structural and drive folds instead.
type markerKind int

const (
	markerNone markerKind = iota
	markerError
	markerWarning
	markerNotice
	markerCommand
	markerDebug
)

// bom is the UTF-8 byte-order mark the first line carries (R3). It is stripped before the
// timestamp is split, so it is never counted as part of the first line's content and never
// reaches the display. It is spelled as an escape so the source carries no invisible rune.
const bom = "\uFEFF"

// tsPattern matches the ISO-8601 timestamp prefix every line carries, the measured
// "2026-07-15T03:11:52.0835958Z " that is 29 characters including its trailing space (R4,
// constraints table). The fractional part is optional and its digit count is not fixed, so
// the pattern matches the measured seven-digit form and any other, and the whole match is
// the byte-identical prefix a timestamps-on view restores. It is anchored at the start and
// ends at the single space that separates the timestamp from the content.
var tsPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z `)

// logLine is one rendered line of log content: its byte-identical timestamp prefix (R4),
// the display text with any recognised marker syntax stripped (R8), and the styling class
// R7 paints it in. No control bytes are stripped here and no tab is expanded: that is
// sanitizeParsed's job, run once over author-controlled content after the parse (R19), so the
// parse stays a pure transformation a golden can pin.
type logLine struct {
	prefix string     // the 29-char timestamp prefix incl trailing space, "" when absent (R4)
	text   string     // display content, marker syntax stripped (R8)
	marker markerKind // R7 styling class
}

// fold is one ##[group]...##[endgroup] span rendered as a collapsible unit (R5). label is
// the text following the ##[group] marker, prefix is that line's timestamp for a
// timestamps-on view, and body is the lines between the markers. Steps are derived from
// these markers alone and the boundaries are approximate (R6).
type fold struct {
	prefix   string
	label    string
	body     []logLine
	expanded bool
}

// block is one top-level element of a parsed log: either a fold or a single line. A value
// discriminated by isFold rather than an interface keeps the whole parse a plain value a
// golden pins and a toggle copies (R20).
type block struct {
	isFold bool
	fold   fold
	line   logLine
}

// parsed is the whole transformed log: an ordered list of blocks. It is derived from the
// fetched bytes alone, with no terminal and no network, which is what makes R20's goldens a
// byte-level check of every transformation the API's content passes through.
type parsed struct {
	blocks []block
}

// parseLog transforms fetched log bytes into the structured, foldable model the view paints
// (R3 to R8). It strips the leading BOM (R3), splits each line's timestamp prefix (R4),
// folds each ##[group]/##[endgroup] span (R5, R6), classifies the styled markers and strips
// their syntax (R7, R8), and renders an unrecognised ##[...] marker verbatim because
// swallowing it would destroy content the tool does not understand (R8). It interprets no
// control sequence and strips none: R19's stripping and the tab expansion run once in
// sanitizeParsed after the parse.
func parseLog(raw []byte) parsed {
	s := strings.TrimPrefix(string(raw), bom) // R3: the BOM is the file's first bytes
	lines := splitLines(s)

	var blocks []block
	var open *fold // the currently open ##[group], nil when none
	flush := func() {
		if open != nil {
			blocks = append(blocks, block{isFold: true, fold: *open})
			open = nil
		}
	}
	appendLine := func(l logLine) {
		if open != nil {
			open.body = append(open.body, l)
			return
		}
		blocks = append(blocks, block{line: l})
	}

	for _, raw := range lines {
		prefix, rest := splitTimestamp(raw)
		name, content, isCmd := splitCommand(rest)
		switch {
		case isCmd && name == "group":
			flush() // a new group closes any still-open one; boundaries are approximate (R6)
			open = &fold{prefix: prefix, label: content}
		case isCmd && name == "endgroup":
			flush() // an orphan endgroup with no open fold is dropped, never shown (R8)
		case isCmd && name == "error":
			appendLine(logLine{prefix: prefix, text: content, marker: markerError})
		case isCmd && name == "warning":
			appendLine(logLine{prefix: prefix, text: content, marker: markerWarning})
		case isCmd && name == "notice":
			appendLine(logLine{prefix: prefix, text: content, marker: markerNotice})
		case isCmd && name == "command":
			appendLine(logLine{prefix: prefix, text: content, marker: markerCommand})
		case isCmd && name == "debug":
			appendLine(logLine{prefix: prefix, text: content, marker: markerDebug})
		default:
			// An ordinary line, or an unrecognised ##[...] marker rendered verbatim (R8).
			appendLine(logLine{prefix: prefix, text: rest})
		}
	}
	flush() // a group with no endgroup still closes at end of file (R6)

	return parsed{blocks: blocks}
}

// splitLines splits log bytes into lines on the newline, dropping the single trailing empty
// line a file ending in a newline produces, so a well-formed log gains no spurious blank
// last row. An interior blank line is preserved, because it is content.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// splitTimestamp splits a line into its byte-identical timestamp prefix and the remaining
// content (R4). A line without the measured prefix keeps its whole text and an empty prefix,
// so a timestamps-on view restores exactly what the API sent and a line the API did not
// prefix is left alone rather than losing its first 29 characters.
func splitTimestamp(line string) (prefix, rest string) {
	if loc := tsPattern.FindStringIndex(line); loc != nil {
		return line[:loc[1]], line[loc[1]:]
	}
	return "", line
}

// splitCommand splits a leading ##[name]content workflow-command token, returning the name,
// the content after the closing bracket, and whether the line carried such a token. It is
// the one place the ##[...] syntax is recognised, so the caller decides per name whether the
// token is structural (group, endgroup), a styled marker, or an unrecognised token to render
// verbatim (R8).
func splitCommand(rest string) (name, content string, ok bool) {
	if !strings.HasPrefix(rest, "##[") {
		return "", "", false
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return "", "", false
	}
	return rest[len("##["):end], rest[end+1:], true
}
