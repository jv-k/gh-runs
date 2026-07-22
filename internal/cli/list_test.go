package cli_test

import (
	"strings"
	"testing"
)

// TestListSingleRepoTable drives the read half's happy path: -R selects one
// repository, the command lists its Runs through the transport chain, and prints
// a table. It pins the base of AC2 (a default invocation fetches at most 20) and
// AC3 (a completed Run's Conclusion reaches the output).
func TestListSingleRepoTable(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	out := h.stdout.String()
	// Both Runs reach the table, identified by their database IDs.
	for _, want := range []string{"101", "102", "Fix the bug", "Break the build"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n%s", want, out)
		}
	}
	// Exactly one listing request left the process: one repository, one page.
	if n := h.counting.count(); n != 1 {
		t.Errorf("wire requests = %d, want 1", n)
	}
}

// TestLsAlias pins R3: ls is an alias of list, matching gh's own gh run ls.
func TestLsAlias(t *testing.T) {
	h := newHarness(t, "list_single")
	if code := h.run("ls", "-R", "octo/hello"); code != 0 {
		t.Fatalf("ls exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "101") {
		t.Errorf("ls did not list runs\n%s", h.stdout.String())
	}
}

// TestCappedListingLabelled pins R16's affirmative half through the public surface: a
// per-repository listing that fills -L while a rel="next" page still remains is
// labelled capped on stderr, stdout carries the runs it did return, and the next page
// is not fetched (cli-surface R16, ADR-0022). The label names no count, only that more
// may exist.
func TestCappedListingLabelled(t *testing.T) {
	h := newHarness(t, "list_capped")

	code := h.run("list", "-R", "octo/hello", "-L", "2")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "capped") {
		t.Errorf("a listing capped at --limit was not labelled; stderr=%q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "501") {
		t.Errorf("the returned runs are missing from stdout\n%s", h.stdout.String())
	}
	if n := h.counting.count(); n != 1 {
		t.Errorf("wire requests = %d, want 1 (the next page is not fetched once the limit is met)", n)
	}
}

// TestEmptyResultExitsZero pins that a list matching no Runs exits 0 with an empty
// stdout and a note on stderr, so a pipeline is not fed a spurious row and a
// zero-match list is not a failure.
func TestEmptyResultExitsZero(t *testing.T) {
	h := newHarness(t, "list_empty")
	if code := h.run("list", "-R", "octo/empty"); code != 0 {
		t.Fatalf("exit = %d, want 0 on an empty result; stderr=%q", code, h.stderr.String())
	}
	if h.stdout.String() != "" {
		t.Errorf("stdout should be empty on no runs, got %q", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "no runs found") {
		t.Errorf("expected a note on stderr; got %q", h.stderr.String())
	}
}
