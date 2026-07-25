package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"charm.land/lipgloss/v2"
)

// templateFuncs is the -t function map: gh's pure, deterministic template functions,
// reimplemented here because go-gh's pkg/template, where they live, cannot be
// imported against this module's charm.land v2 pin (ADR-0023, ADR-0013). The four
// gh functions that need a colour library or gh's cellbuf-backed table printer are
// registered as stubs that error by name, so a template ported from gh parses as it
// does under gh and fails with a message an operator can act on.
//
// The map is built per render because timeago closes over the injected clock, read
// once here so every row of one listing measures against the same instant, as gh's
// own Parse-time time.Now() does (ADR-0023).
func templateFuncs(deps Deps) template.FuncMap {
	now := deps.Clock.Now()
	funcs := template.FuncMap{
		// The five sprig string functions gh curates, reimplemented rather than pulling
		// sprig in. Their argument order is sprig's, needle before haystack, so a value
		// pipes into them.
		"contains":   func(substr, str string) bool { return strings.Contains(str, substr) },
		"hasPrefix":  func(substr, str string) bool { return strings.HasPrefix(str, substr) },
		"hasSuffix":  func(substr, str string) bool { return strings.HasSuffix(str, substr) },
		"regexMatch": regexMatchFunc,
		"replace":    func(old, replacement, src string) string { return strings.ReplaceAll(src, old, replacement) },

		"hyperlink": hyperlinkFunc,
		"join":      joinFunc,
		"pluck":     pluckFunc,
		"timeago":   func(input string) (string, error) { return timeAgoFunc(now, input) },
		"timefmt":   timeFormatFunc,
		"truncate":  truncateFunc,
	}
	// The stubs are registered last, over the finished supported set, so their error
	// lists what -t carries without a second copy of that list to drift out of date.
	// knownFieldList derives from jsonProjectors for the same reason.
	supported := strings.Join(funcNames(funcs), ", ")
	for _, name := range unsupportedTemplateFuncs {
		funcs[name] = unsupportedTemplateFunc(name, supported)
	}
	return funcs
}

// unsupportedTemplateFuncs names the four gh functions this build drops, each needing
// a colour library or gh's cellbuf-backed table printer (ADR-0023).
var unsupportedTemplateFuncs = []string{"autocolor", "color", "tablerender", "tablerow"}

// funcNames returns a function map's names, sorted, for an error message.
func funcNames(funcs template.FuncMap) []string {
	names := make([]string, 0, len(funcs))
	for name := range funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// unsupportedTemplateFunc builds the stub registered for each dropped function. It is
// variadic so any call shape parses, which is the point: a template ported from gh
// parses exactly as it does under gh and fails at execution with a message naming the
// function, rather than the standard library's bare function "color" not defined at
// parse time. Because the failure is at execution, a call inside a branch the template
// never takes never fires.
func unsupportedTemplateFunc(name, supported string) func(...any) (string, error) {
	return func(...any) (string, error) {
		return "", fmt.Errorf(
			"template function %q is not supported: %s need a colour library or gh's table printer, which this build does not carry (ADR-0023). Supported functions: %s",
			name, strings.Join(unsupportedTemplateFuncs, ", "), supported)
	}
}

// regexMatchFunc is sprig's regexMatch: it reports whether s matches the expression,
// and an expression that does not compile is a non-match rather than an error, which is
// sprig's own behaviour (its must variant is the one that reports the compile error,
// and gh registers only this one).
func regexMatchFunc(regex, s string) bool {
	match, _ := regexp.MatchString(regex, s)
	return match
}

// pluckFunc lifts one field out of every object in a list, gh's pluck. It is how a
// template reaches across rows, and it composes with join. Its output matches gh's on
// every input that succeeds, including an empty list and a field no row carries.
//
// One deviation, on the failing input: gh type-asserts and panics, which text/template
// recovers into an opaque error, and this returns a typed one naming what it got. Both
// fail the render, so only the message differs, and this message says which value was
// wrong.
func pluckFunc(field string, input []any) ([]any, error) {
	results := make([]any, 0, len(input))
	for _, item := range input {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pluck: expected an object, got %T", item)
		}
		results = append(results, obj[field])
	}
	return results, nil
}

// joinFunc concatenates a list of JSON scalars with a separator, gh's join.
func joinFunc(sep string, input []any) (string, error) {
	results := make([]string, 0, len(input))
	for _, item := range input {
		text, err := jsonScalarToString(item)
		if err != nil {
			return "", err
		}
		results = append(results, text)
	}
	return strings.Join(results, sep), nil
}

// timeFormatFunc reformats an RFC3339 timestamp with a Go layout, gh's timefmt. It
// reads no clock, so it is deterministic without one.
func timeFormatFunc(format, input string) (string, error) {
	t, err := time.Parse(time.RFC3339, input)
	if err != nil {
		return "", err
	}
	return t.Format(format), nil
}

// hyperlinkFunc wraps text in an OSC 8 terminal hyperlink pointing at link, falling
// back to the link as its own text. It emits the escape unconditionally, as gh's does:
// -t output is the operator's own format and stays raw and unsanitised, unlike the
// human table, whose content is untrusted and is stripped of control bytes (ADR-0023).
func hyperlinkFunc(link, text string) string {
	if text == "" {
		text = link
	}
	// https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", link, text)
}

// jsonScalarToString renders a decoded JSON scalar the way gh's template functions do.
// It carries one deliberate difference: renderTemplate decodes with UseNumber, so a
// number arrives as a json.Number and prints as its source digits, which is what keeps
// a Run's database ID out of float64 scientific notation. The float64 case is gh's own
// rounding, kept for a value that reaches here already converted.
func jsonScalarToString(input any) (string, error) {
	switch tt := input.(type) {
	case string:
		return tt, nil
	case json.Number:
		return tt.String(), nil
	case float64:
		if math.Trunc(tt) == tt {
			return strconv.FormatFloat(tt, 'f', 0, 64), nil
		}
		return strconv.FormatFloat(tt, 'f', 2, 64), nil
	case nil:
		return "", nil
	case bool:
		return fmt.Sprintf("%v", tt), nil
	default:
		return "", fmt.Errorf("cannot convert type to string: %v", tt)
	}
}

// timeAgoFunc renders an RFC3339 timestamp as gh's relative wording, measured from
// now. gh binds its own time.Now() at Parse time; this binds the injected clock, which
// is what makes a -t golden deterministic (ADR-0023).
func timeAgoFunc(now time.Time, input string) (string, error) {
	t, err := time.Parse(time.RFC3339, input)
	if err != nil {
		return "", err
	}
	return timeAgo(now.Sub(t)), nil
}

// timeAgo is gh's bucketing and gh's wording, boundary for boundary: minutes below an
// hour, hours below a day, days below thirty days, months below a year, years above it.
// Each bucket truncates rather than rounds, and the unit is pluralised only when the
// count is not one. The table's terse age() renderer is deliberately a separate
// function with separate wording (ADR-0023): one clock, two renderers.
func timeAgo(ago time.Duration) string {
	switch {
	case ago < time.Minute:
		return "just now"
	case ago < time.Hour:
		return pluralize(int(ago.Minutes()), "minute") + " ago"
	case ago < 24*time.Hour:
		return pluralize(int(ago.Hours()), "hour") + " ago"
	case ago < 30*24*time.Hour:
		return pluralize(int(ago.Hours())/24, "day") + " ago"
	case ago < 365*24*time.Hour:
		return pluralize(int(ago.Hours())/24/30, "month") + " ago"
	default:
		return pluralize(int(ago.Hours()/24/365), "year") + " ago"
	}
}

// pluralize is gh's text.Pluralize: the count, a space, and the thing with an s unless
// the count is exactly one.
func pluralize(num int, thing string) string {
	if num == 1 {
		return fmt.Sprintf("%d %s", num, thing)
	}
	return fmt.Sprintf("%d %ss", num, thing)
}

const (
	// ellipsis and minWidthForEllipsis are gh's, the second being len(ellipsis)+2:
	// below five cells there is no room to spend three of them on a marker, so the
	// cut is silent (ADR-0023).
	ellipsis            = "..."
	minWidthForEllipsis = len(ellipsis) + 2
	// escMarker opens an ANSI escape sequence, and resetSeq closes any colour left
	// open by one.
	escMarker = '\x1b'
	resetSeq  = "\x1b[0m"
)

// truncateFunc is gh's truncate: a string shortened to a maximum display width, a nil
// left empty, and anything else an error naming the type it got.
func truncateFunc(maxWidth int, v any) (string, error) {
	if v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid value; expected string, got %T", v)
	}
	return truncateToWidth(maxWidth, s), nil
}

// truncateToWidth shortens s to maxWidth rendered cells, gh's text.Truncate over
// lipgloss v2's Width rather than gh's classic-lipgloss one (ADR-0023). Display width,
// not rune count, is the measure: the CJK and emoji run titles this tool lists against
// real repositories occupy two cells each, and a rune-count cut mangles the column.
// A result narrower than the request is padded by one space, which is how a cut that
// lands mid-way through a double-width cell still fills its column.
func truncateToWidth(maxWidth int, s string) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	tail := ""
	if maxWidth >= minWidthForEllipsis {
		tail = ellipsis
	}
	r := cutAtWidth(s, maxWidth, tail)
	if lipgloss.Width(r) < maxWidth {
		r += " "
	}
	return r
}

// cutAtWidth keeps as much of s as fits in width cells once the tail is paid for, then
// appends the tail. It spends the budget on printable cells only: an ANSI escape
// sequence costs nothing and is copied through, so a coloured value is cut on what the
// terminal shows rather than on the bytes carrying it. A cut that lands inside an
// unreset colour emits a reset, so the escape cannot leak into whatever the template
// prints next. This is what gh gets from reflow, which is not in this module's graph
// (ADR-0013).
//
// The budget is charged by remeasuring the whole kept prefix with lipgloss rather than
// by summing per-rune widths, which is what makes it grapheme-cluster aware. A flag is
// two regional indicators rendering as one two-cell glyph, and only a whole-string
// measure prices it at two: per-rune arithmetic charges four and cuts the value a cell
// short of its column (code review). It also keeps this loop and truncateToWidth's
// guard on one measure instead of two that can disagree. reflow does sum per rune, and
// this is the one place the port deliberately improves on it, because the ADR's whole
// reason for width awareness is the CJK and emoji titles that expose the difference.
// The remeasure is quadratic in the length of one value, which is nothing at the length
// of a Run title.
func cutAtWidth(s string, width int, tail string) string {
	// gh casts the width to a uint before cutting (its text.Truncate), so a negative
	// one wraps to an enormous budget and the value comes back whole. Reproduced rather
	// than clamped, so a template that computes a width gets gh's answer.
	if width < 0 {
		return s
	}
	tailWidth := lipgloss.Width(tail)
	if width < tailWidth {
		return tail
	}
	budget := width - tailWidth

	// out is what is emitted, escapes included; kept is the printable text alone, which
	// is what the budget is measured over.
	var out, kept, seq strings.Builder
	inEscape, colourOpen := false, false
	for _, c := range s {
		if c == escMarker {
			inEscape = true
			seq.WriteRune(c)
			continue
		}
		if inEscape {
			seq.WriteRune(c)
			if !isEscapeTerminator(c) {
				continue
			}
			inEscape = false
			esc := seq.String()
			seq.Reset()
			switch {
			case strings.HasSuffix(esc, "[0m"):
				colourOpen = false
			case c == 'm':
				colourOpen = true
			}
			out.WriteString(esc)
			continue
		}
		// A character only lands if the whole of it fits, so a wide glyph is dropped
		// rather than half-printed.
		kept.WriteRune(c)
		if lipgloss.Width(kept.String()) > budget {
			out.WriteString(tail)
			if colourOpen {
				out.WriteString(resetSeq)
			}
			return out.String()
		}
		out.WriteRune(c)
	}
	return out.String()
}

// isEscapeTerminator reports whether c closes an ANSI escape sequence, in reflow's
// final-byte range, which is any letter.
//
// The range is wrong for OSC sequences and is kept wrong on purpose: the "h" of an OSC
// 8 hyperlink's https ends the sequence here, so the rest of the URL is charged as
// printable text and a truncated hyperlink comes out mangled. That is what gh does, R7
// asks for gh's behaviour, and TestTruncateToWidthReproducesGhsOSC8Mangling pins the
// output byte for byte. Widening the range would be a deliberate deviation from gh and
// needs its own decision, not a drive-by fix.
//
// textsan.Sanitize scans escapes too, over the wider 0x40 to 0x7e range, and the two
// are meant to differ: that one is stripping hostile bytes out of the human table,
// this one is reproducing a specific upstream parser.
func isEscapeTerminator(c rune) bool {
	return (c >= 0x40 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a)
}
