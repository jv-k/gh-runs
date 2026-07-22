package cli_test

import (
	"os"
	"strings"
	"testing"
)

// TestDeleteRefusesWithoutFilterOrAll pins cli-surface R26 and AC19: a delete with no
// filter and no --all fails with a usage diagnostic and issues zero requests, so the
// zero filter is unreachable by omission. The offline transport fails any wire request,
// so a leaked crawl or DELETE would fail the test loudly.
func TestDeleteRefusesWithoutFilterOrAll(t *testing.T) {
	h := newHarnessOffline(t)
	code := h.run("delete", "--yes")
	if code == 0 {
		t.Errorf("delete with no filter and no --all exited 0; it must refuse (R26, AC19)")
	}
	if h.counting.count() != 0 {
		t.Errorf("delete refused but issued %d requests; it must issue zero (AC19)", h.counting.count())
	}
}

// TestDeleteRefusesWithoutYes pins R11 and AC8: a destructive delete without --yes
// deletes nothing and exits non-zero, and --yes is never waivable.
func TestDeleteRefusesWithoutYes(t *testing.T) {
	h := newHarnessOffline(t)
	code := h.run("delete", "--all")
	if code == 0 {
		t.Errorf("delete --all without --yes exited 0; --yes is always required (R11, AC8)")
	}
	if h.counting.deletes() != 0 {
		t.Errorf("delete without --yes issued %d DELETEs; it must issue zero (R11, AC8)", h.counting.deletes())
	}
}

// TestDeleteAllYesDeletes pins R10, R11 and AC20: --all --yes crawls the affected set,
// deletes each Run, writes the deletion log one line per attempt, and exits 0.
func TestDeleteAllYesDeletes(t *testing.T) {
	h := newHarness(t, "delete_all").withCurrent(gh("o", "r"))
	code := h.runDriven("delete", "--all", "--yes")
	if code != 0 {
		t.Errorf("delete --all --yes exited %d, want 0 (AC20). stderr: %s", code, h.stderr.String())
	}
	if h.counting.deletes() != 3 {
		t.Errorf("issued %d DELETEs, want 3 (one per crawled Run)", h.counting.deletes())
	}
	lines := h.deletionLogLines(t)
	if len(lines) != 3 {
		t.Errorf("deletion log has %d lines, want 3 (one per attempt) (R29)", len(lines))
	}
	for _, l := range lines {
		if !strings.Contains(l, "\tdeleted\t") {
			t.Errorf("log line not a clean deletion: %q", l)
		}
	}
}

// TestDeleteDryRunResolvesAndWritesNoLog pins R10 and AC9: --dry-run resolves the same
// set through the same crawl, emits one row per Run naming its repository and Run ID,
// issues no DELETE, writes no log line, and exits 0.
func TestDeleteDryRunResolvesAndWritesNoLog(t *testing.T) {
	h := newHarness(t, "delete_all").withCurrent(gh("o", "r"))
	code := h.runDriven("delete", "--all", "--dry-run")
	if code != 0 {
		t.Errorf("delete --all --dry-run exited %d, want 0 (R10, AC9)", code)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("dry-run issued %d DELETEs; it must issue none (R10, AC9)", h.counting.deletes())
	}
	if h.logExists() {
		t.Errorf("dry-run wrote the deletion log; it must write none (R10, AC9)")
	}
	out := h.stdout.String()
	for _, want := range []string{"github.com/o/r\t1", "github.com/o/r\t2", "github.com/o/r\t3"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing a row %q; each row names its repository and Run ID (R10, AC9):\n%s", want, out)
		}
	}
}

// TestDeletePartialFailureExitsOne pins AC20: a Purge with a real failure among the
// deletes exits 1, while the clean deletes still land.
func TestDeletePartialFailureExitsOne(t *testing.T) {
	h := newHarness(t, "delete_partial").withCurrent(gh("o", "r"))
	code := h.runDriven("delete", "--all", "--yes")
	if code != 1 {
		t.Errorf("a partially failed Purge exited %d, want 1 (cli-surface R17, AC20)", code)
	}
	if h.counting.deletes() != 3 {
		t.Errorf("issued %d DELETEs, want 3 (all attempted; the breaker default of 50 does not fire)", h.counting.deletes())
	}
}

// TestDryRunAndRealAgree pins AC9's equivalence: --dry-run reports N Runs, and the real
// delete over the same cassette deletes exactly N and writes N log lines. Two harnesses
// over one cassette, because the two paths issue different requests.
func TestDryRunAndRealAgree(t *testing.T) {
	dry := newHarness(t, "delete_all").withCurrent(gh("o", "r"))
	if code := dry.runDriven("delete", "--all", "--dry-run"); code != 0 {
		t.Fatalf("dry-run exited %d", code)
	}
	dryRows := strings.Count(strings.TrimRight(dry.stdout.String(), "\n"), "\n") + 1

	real := newHarness(t, "delete_all").withCurrent(gh("o", "r"))
	if code := real.runDriven("delete", "--all", "--yes"); code != 0 {
		t.Fatalf("real delete exited %d", code)
	}
	if got := real.counting.deletes(); got != dryRows {
		t.Errorf("dry-run reported %d Runs but the real delete issued %d DELETEs; they must agree (AC9)", dryRows, got)
	}
	if got := len(real.deletionLogLines(t)); got != dryRows {
		t.Errorf("dry-run reported %d Runs but the real delete wrote %d log lines; they must agree (AC9)", dryRows, got)
	}
}

// deletionLogLines reads the deletion log's non-empty lines.
func (h *harness) deletionLogLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(h.logDir + "/gh-runs/deletions.log")
	if err != nil {
		t.Fatalf("read deletion log: %v", err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
