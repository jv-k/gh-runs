package cli_test

import (
	"strings"
	"testing"
)

// TestCreatedFilterPushesAndMatches drives --created through filter.ParseCreated
// and filter.Query: an accepted date is pushed to the server as created= and
// applied client-side, so a Run outside the day is excluded (cli-surface R6).
func TestCreatedFilterPushesAndMatches(t *testing.T) {
	h := newHarness(t, "list_created")

	code := h.run("list", "-R", "octo/hello", "--created", "2026-07-20")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if n := h.counting.countMatching("created=2026-07-20"); n != 1 {
		t.Errorf("requests carrying created=2026-07-20 = %d, want 1 (server-side push)", n)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "401") {
		t.Errorf("Run on the day (401) missing\n%s", out)
	}
	if strings.Contains(out, "402") {
		t.Errorf("Run from the day before (402) was not excluded by the client-side Match\n%s", out)
	}
}

// TestCreatedGrammarBoundary pins --created parity with gh's grammar, now measured
// read-only against cli/cli (cli-surface R2). gh accepts five date widths and
// gh-runs accepts the same five: a bare year, a year-month, a full date, an RFC3339
// datetime with a zone, and a zone-less datetime. The bare year and year-month were
// the headline parity questions and are now accepted, so a --created value that
// works in gh no longer errors in gh-runs. Only a genuinely malformed value is
// rejected, and it is rejected before the wire (cli-surface R6).
func TestCreatedGrammarBoundary(t *testing.T) {
	// Accepted forms parse into a filter. Routed through an empty fan-out (no -R,
	// no discovered repositories) they issue no request, so the offline harness is
	// never reached and the assertion is only that no --created rejection fired.
	for _, form := range []string{
		"2026",                   // bare year, now accepted for gh parity
		"2026-07",                // year-month, now accepted for gh parity
		"2026-07-20",             // full date
		"2026-07-20T10:00:00Z",   // RFC3339 with a zone
		"2026-07-20T10:00:00",    // zone-less datetime, read as UTC
		">=2026-01-01",           // an operator on a date
		"2026-01-01..2026-02-01", // a range
		">=2026",                 // an operator on the new bare-year atom
	} {
		h := newHarnessOffline(t).withDiscovered()
		if code := h.run("list", "--created", form); code != 0 {
			t.Errorf("--created %q exited %d over an empty fan-out, want 0; stderr=%q", form, code, h.stderr.String())
		}
		if strings.Contains(h.stderr.String(), "invalid created date") {
			t.Errorf("--created %q was rejected but should be accepted; stderr=%q", form, h.stderr.String())
		}
		if n := h.counting.count(); n != 0 {
			t.Errorf("--created %q issued %d requests over an empty fan-out, want 0", form, n)
		}
	}

	// A genuinely malformed value is still caught before the wire: a non-padded
	// month, an out-of-range month, and a non-date all reject by name (R6).
	for _, form := range []string{"2026-1", "2026-13", "not-a-date"} {
		h := newHarnessOffline(t)
		code := h.run("list", "-R", "octo/hello", "--created", form)
		if code == 0 {
			t.Errorf("--created %q exited 0, want rejection", form)
		}
		if n := h.counting.count(); n != 0 {
			t.Errorf("--created %q issued %d requests, want 0 (rejected before the wire)", form, n)
		}
	}
}
