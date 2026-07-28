package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/cli"
	"github.com/jv-k/gh-runs/v2/internal/version"
)

// TestAsksVersionAgreesWithCobra is the reason main.go may answer --version
// before cobra sees it. The short-circuit exists so the flag needs no token
// (ADR-0026), and its cost is a second place that names the spelling. This test
// removes the cost by pinning the two together: every spelling cobra answers,
// the short-circuit must answer too. One that cobra grew and this missed would
// fall through to the client construction that fails without a token, which is
// the exact failure the short-circuit exists to prevent.
func TestAsksVersionAgreesWithCobra(t *testing.T) {
	for _, spelling := range []string{"--version", "-v"} {
		var out, errOut bytes.Buffer
		code := cli.Execute(cli.Deps{Stdout: &out, Stderr: &errOut}, []string{spelling})

		if code != 0 || !strings.Contains(out.String(), version.String()) {
			t.Errorf("cobra no longer answers %s (exit %d, stdout %q, stderr %q); "+
				"if the flag moved, asksVersion must move with it",
				spelling, code, out.String(), errOut.String())
			continue
		}
		if !asksVersion([]string{spelling}) {
			t.Errorf("cobra answers %s but asksVersion does not, so the invocation would "+
				"reach the client construction that needs a token", spelling)
		}
	}
}

func TestAsksVersionIgnoresEverythingElse(t *testing.T) {
	// Anything carrying a subcommand or a second argument is a real invocation,
	// and must build the chain it needs rather than print a version and exit 0.
	for _, args := range [][]string{
		nil,
		{"list"},
		{"delete"},
		{"list", "--version"},
		{"--version", "list"},
		{"--versions"},
		{"-vv"},
	} {
		if asksVersion(args) {
			t.Errorf("asksVersion(%q) = true, want false", args)
		}
	}
}

// TestVersionIsAlwaysReported guards the one thing a bug report needs from this
// flag: a non-empty answer. version.String falls through several cases depending
// on how the binary was built, and every one of them has to say something.
func TestVersionIsAlwaysReported(t *testing.T) {
	if v := strings.TrimSpace(version.String()); v == "" {
		t.Error("version.String() is empty, so --version would print nothing")
	}
}
