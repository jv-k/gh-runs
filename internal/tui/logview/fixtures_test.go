package logview

import (
	"strconv"
	"strings"
)

// The fixtures are built in code rather than read from a binary file, so the leading BOM
// (R3) and the exact timestamp prefixes (R4) are visible in the source a reviewer reads and
// stable for the goldens, rather than hidden in an opaque testdata blob. Every assertion
// that reads a count reads it from the fixture it built, never a constant compiled in, which
// is this tree's rule for fixture-derived totals.

// tsA is the measured 29-character ISO timestamp prefix, "2026-07-15T03:11:52.0835958Z ",
// counting its trailing space (log-viewer constraints table). Its length is asserted in the
// parse test rather than trusted here.
const tsA = "2026-07-15T03:11:52.0835958Z "

// bomLiteral is the UTF-8 BOM the first line of a real Job log carries (R3).
const bomLiteral = "\uFEFF"

// trivialLog builds a log with the measured shape of the 4,153-byte trivial Job log (AC2):
// a leading BOM, a timestamp on every line, twelve ##[group]/##[endgroup] folds, and two
// ##[warning] lines at the top level so they stay visible when the folds are collapsed by
// default (R5). The two warnings sit between folds, never inside one, which is what lets the
// default collapsed view show exactly two warnings (AC2, AC4).
func trivialLog() []byte {
	labels := []string{
		"Set up job",
		"Run actions/checkout@v4",
		"Set up Go",
		"Restore cache",
		"Download dependencies",
		"Build",
		"Run tests",
		"Run go vet",
		"Upload coverage",
		"Save cache",
		"Post Run actions/checkout@v4",
		"Complete job",
	}
	var b strings.Builder
	b.WriteString(bomLiteral)
	for i, label := range labels {
		b.WriteString(tsA + "##[group]" + label + "\n")
		b.WriteString(tsA + "  step " + strconv.Itoa(i) + " starting\n")
		b.WriteString(tsA + "  step " + strconv.Itoa(i) + " ok\n")
		b.WriteString(tsA + "##[endgroup]\n")
		// Two warnings at the top level, between folds, so a collapsed default view shows
		// them (AC2, AC4). Placed after the fourth and eighth folds.
		if i == 3 {
			b.WriteString(tsA + "##[warning]node 16 is deprecated, upgrade to node 20\n")
		}
		if i == 7 {
			b.WriteString(tsA + "##[warning]set-output is deprecated, use $GITHUB_OUTPUT\n")
		}
	}
	return []byte(b.String())
}

// styledLog builds a log carrying an ##[error] line, an ##[warning] line, and an ordinary
// line so a golden can pin R7's "styled apart from ordinary lines and from each other", plus
// a ##[notice], a ##[command], a ##[debug], and an unrecognised ##[set-output] token that
// R8 renders verbatim because the tool does not understand it. No BOM and no fold, so the
// golden is only about marker styling. The go test line carries the tabs that lay go test's
// output out in columns, so the golden also pins that a TAB expands to spaces and the columns
// line up (R19), rather than being stripped and the fields fused.
func styledLog() []byte {
	lines := []string{
		tsA + "##[command]/usr/bin/go test ./...",
		tsA + "ok  \tgithub.com/example/pkg\t0.42s",
		tsA + "##[warning]this package is deprecated",
		tsA + "##[error]FAIL github.com/example/broken",
		tsA + "##[notice]coverage is 87 percent",
		tsA + "##[debug]cache key resolved to abc123",
		tsA + "##[set-output]name=result::ok",
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// hostileLog builds a log line carrying the shape a workflow author can print into their own
// logs (log-viewer R19, security review): a CSI colour sequence, a clear-screen, a
// cursor-home, a carriage return, a tab, and a bare ESC. Every one of the terminal-driving
// controls must be stripped before the line is painted, or the line rewrites or spoofs the
// operator's terminal, which in a delete tool is a decision-integrity failure. The tab is the
// one exception: it expands to spaces rather than being deleted, the single control kept as
// layout (R19). The visible text between the escapes is what must survive.
func hostileLog() []byte {
	line := tsA + "Building \x1b[31mproject\x1b[0m\x1b[2J\x1b[Hcleared\rretreat\ttail\x1b"
	return []byte(line + "\n")
}
