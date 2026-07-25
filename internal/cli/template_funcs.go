package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
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
	return template.FuncMap{
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

		"color":       unsupportedTemplateFunc("color"),
		"autocolor":   unsupportedTemplateFunc("autocolor"),
		"tablerow":    unsupportedTemplateFunc("tablerow"),
		"tablerender": unsupportedTemplateFunc("tablerender"),
	}
}

// supportedTemplateFuncs names what -t does carry, listed in an unsupported
// function's error so the operator can rewrite the template without leaving the
// terminal.
var supportedTemplateFuncs = []string{
	"contains", "hasPrefix", "hasSuffix", "hyperlink", "join",
	"pluck", "regexMatch", "replace", "timeago", "timefmt", "truncate",
}

// unsupportedTemplateFunc builds the stub registered for each of the four gh
// functions this build drops (ADR-0023). It is variadic so any call shape parses,
// which is the point: a template ported from gh parses exactly as it does under gh and
// fails at execution with a message naming the function, rather than the standard
// library's bare function "color" not defined at parse time. Because the failure is at
// execution, a call inside a branch the template never takes never fires.
func unsupportedTemplateFunc(name string) func(...any) (string, error) {
	return func(...any) (string, error) {
		return "", fmt.Errorf(
			"template function %q is not supported: color, autocolor, tablerow and tablerender need a colour library or gh's table printer, which this build does not carry (ADR-0023). Supported functions: %s",
			name, strings.Join(supportedTemplateFuncs, ", "))
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
// template reaches across rows, and it composes with join.
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
	r := truncateWithTail(s, maxWidth, tail)
	if lipgloss.Width(r) < maxWidth {
		r += " "
	}
	return r
}

// truncateWithTail cuts s to width cells including the tail, then appends the tail.
// It spends the budget on printable cells only: an ANSI escape sequence costs nothing
// and is copied through, so a coloured value is cut on what the terminal shows rather
// than on the bytes carrying it. A cut that lands inside an unreset colour emits a
// reset, so the escape cannot leak into whatever the template prints next. This is
// what gh gets from reflow, which is not in this module's graph (ADR-0013).
func truncateWithTail(s string, width int, tail string) string {
	tailWidth := lipgloss.Width(tail)
	if width < tailWidth {
		return tail
	}
	budget := width - tailWidth

	var out, seq strings.Builder
	var used int
	inEscape := false
	openColour := ""
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
				openColour = ""
			case c == 'm':
				openColour += esc
			}
			out.WriteString(esc)
			continue
		}
		// A rune only lands if the whole of it fits, so a double-width rune is dropped
		// rather than half-printed.
		used += lipgloss.Width(string(c))
		if used > budget {
			out.WriteString(tail)
			if openColour != "" {
				out.WriteString(resetSeq)
			}
			return out.String()
		}
		out.WriteRune(c)
	}
	return out.String()
}

// isEscapeTerminator reports whether c closes an ANSI escape sequence, the final byte
// range reflow uses.
func isEscapeTerminator(c rune) bool {
	return (c >= 0x40 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a)
}
