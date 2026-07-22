package cli_test

import (
	"strings"
	"testing"
)

// TestStatusSpansBothFields pins AC3: -s is permissive across the Status and
// Conclusion fields the domain holds distinct. -s failure surfaces a Run whose
// Conclusion is failure and excludes a success Run in the same response, proving
// the client-side Match runs; -s in_progress surfaces a Run whose Status is
// in_progress (cli-surface R4).
func TestStatusSpansBothFields(t *testing.T) {
	t.Run("failure matches the conclusion field", func(t *testing.T) {
		h := newHarness(t, "list_status")
		if code := h.run("list", "-R", "octo/hello", "-s", "failure"); code != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
		}
		out := h.stdout.String()
		if !strings.Contains(out, "201") {
			t.Errorf("failure Run 201 missing from output\n%s", out)
		}
		if strings.Contains(out, "202") {
			t.Errorf("success Run 202 was not filtered out by the client-side Match\n%s", out)
		}
	})

	t.Run("in_progress matches the status field", func(t *testing.T) {
		h := newHarness(t, "list_status")
		if code := h.run("list", "-R", "octo/hello", "-s", "in_progress"); code != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "203") {
			t.Errorf("in_progress Run 203 missing from output\n%s", h.stdout.String())
		}
	})
}

// TestStatusTypoRejectedOffline pins AC5: a misspelled -s value is rejected by
// name, client-side, before any request. The offline harness fails the test if a
// request ever reaches the wire, so this proves the typo is caught rather than
// forwarded to an API that would answer HTTP 200 with total_count 0, reading as
// "no failures" rather than "you typed it wrong" (cli-surface R6).
func TestStatusTypoRejectedOffline(t *testing.T) {
	h := newHarnessOffline(t)

	code := h.run("list", "-R", "octo/hello", "-s", "faliure")

	if code == 0 {
		t.Fatalf("exit = 0, want non-zero for a rejected status")
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("wire requests = %d, want 0 (the typo must be caught before the wire)", n)
	}
	if !strings.Contains(h.stderr.String(), "faliure") {
		t.Errorf("rejection did not name the offending value; stderr=%q", h.stderr.String())
	}
}

// TestConclusionFlagDoesNotExist pins AC4: --conclusion is not a flag. cobra
// rejects it as unknown, before RunE, so no request carrying a conclusion query
// parameter can ever be built. There is no server-side conclusion parameter to
// expose (cli-surface R5).
func TestConclusionFlagDoesNotExist(t *testing.T) {
	h := newHarnessOffline(t)

	code := h.run("list", "-R", "octo/hello", "--conclusion", "failure")

	if code == 0 {
		t.Fatalf("exit = 0, want non-zero: --conclusion must be rejected as unknown")
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("wire requests = %d, want 0", n)
	}
	if !strings.Contains(h.stderr.String(), "conclusion") {
		t.Errorf("error did not name the unknown flag; stderr=%q", h.stderr.String())
	}
}
