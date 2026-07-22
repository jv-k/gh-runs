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

// TestCreatedGrammarBoundary documents the --created parity carry-forward, NOT a
// settled behaviour. filter.ParseCreated accepts YYYY-MM-DD and full RFC3339 and
// rejects a bare year, a year-month, and a zoneless datetime. gh's own --created
// grammar may forward some of those to the API, so this test pins what the engine
// does today rather than asserting it is what gh does. The verify step drives a
// read-only live pass that can measure gh's actual grammar; do not broaden
// ParseCreated on a guess (cli-surface R2, filter DateRange doc).
func TestCreatedGrammarBoundary(t *testing.T) {
	// Accepted forms parse into a filter. Routed through an empty fan-out (no -R,
	// no discovered repositories) they issue no request, so the offline harness is
	// never reached and the assertion is only that no --created rejection fired.
	for _, form := range []string{"2026-07-20", "2026-07-20T10:00:00Z", ">=2026-01-01", "2026-01-01..2026-02-01"} {
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

	// Rejected forms are caught before the wire. A bare year is the headline parity
	// question: gh may accept 2026, ParseCreated does not.
	for _, form := range []string{"2026", "2026-07", "not-a-date"} {
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
