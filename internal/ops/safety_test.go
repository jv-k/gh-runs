package ops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// TestLogPreconditionRefusesAndIssuesZeroDeletes pins AC20's precondition and R29's
// "no record, no deletion": a state directory that cannot be written makes Execute
// refuse to start, issue zero DELETEs, and name the log. The base transport fails any
// wire request, so a single leaked DELETE would fail the test loudly.
func TestLogPreconditionRefusesAndIssuesZeroDeletes(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	h = h.withLog(t, filepath.Join(blocker, "gh-runs", "deletions.log")) // parent is a file: unwritable

	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3), snapshot(writableRepo("o", "r")))
	sum, err := h.ops.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("Execute returned an error rather than a reported outcome: %v", err)
	}
	if !sum.LogFailed {
		t.Errorf("Execute did not report the log failure (R29, AC20)")
	}
	if !strings.Contains(sum.Reason, "deletion log") {
		t.Errorf("summary reason %q does not name the log (AC20)", sum.Reason)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("issued %d DELETEs with an unwritable log; it must issue zero (R29, AC20)", h.counting.deletes())
	}
}

// TestConfirmedIsSingleUse pins ADR-0019's spent cell: executing one Confirmed twice
// issues the DELETEs once, and the second Execute returns ErrSpent and issues nothing.
func TestConfirmedIsSingleUse(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))

	first := runPurge(t, h, c)
	if first.Deleted != 2 {
		t.Fatalf("first Execute deleted %d, want 2", first.Deleted)
	}
	before := h.counting.deletes()

	_, err := h.ops.Execute(context.Background(), c)
	if !errors.Is(err, ops.ErrSpent) {
		t.Errorf("second Execute = %v, want ErrSpent (ADR-0019)", err)
	}
	if h.counting.deletes() != before {
		t.Errorf("the second Execute issued %d further DELETEs; a spent Confirmed issues nothing", h.counting.deletes()-before)
	}
}

// TestOnlyExecuteWritesTheLogAndDeletes pins the property ADR-0011 exists for: the
// crawl and Plan issue no DELETE and write no log line, and only Execute does both.
// Nothing but the request-issuing call touches the record.
func TestOnlyExecuteWritesTheLogAndDeletes(t *testing.T) {
	// The crawl reads and never deletes, and never opens the log.
	hc := newHarness(t, "crawl_pages", 50, 50)
	if _, err := hc.ops.Crawl(context.Background(), []domain.RepoID{repoID("o", "r")}, filter.Filter{}); err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if hc.counting.deletes() != 0 {
		t.Errorf("the crawl issued %d DELETEs; only Execute issues a DELETE (ADR-0011)", hc.counting.deletes())
	}
	if hc.logExists() {
		t.Errorf("the crawl opened the deletion log; only Execute writes it (ADR-0011, R29)")
	}

	// Plan and Confirm issue no request and write no log line.
	hp := newHarness(t, "delete_ok", 50, 50)
	p, err := hp.ops.Plan(ops.OpDelete, items("o", "r", 1), snapshot(writableRepo("o", "r")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := hp.ops.Confirm(p, ops.NonInteractiveYes()); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(hp.counting.calls) != 0 || hp.logExists() {
		t.Errorf("Plan/Confirm touched the wire (%d calls) or the log (exists=%v); neither may (ADR-0019)", len(hp.counting.calls), hp.logExists())
	}

	// Execute is the one that writes the log, one line per attempt.
	c, err := hp.ops.Confirm(p, ops.NonInteractiveYes())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	runPurge(t, hp, c)
	if !hp.logExists() || len(hp.readLog(t)) != 1 {
		t.Errorf("Execute did not write the one expected log line (R29)")
	}
}

// TestPopulatedLogBehavesIdentically pins AC19's tail and R23/R24: a Purge run against
// a log that already carries lines behaves identically to one against an absent log,
// appends rather than reads, and offers no resume. Nothing in ops opens the log for
// reading, which the logSink interface makes structural.
func TestPopulatedLogBehavesIdentically(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	// Pre-populate the log with a prior session's lines.
	if err := os.MkdirAll(filepath.Dir(h.logPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prior := "2020-01-01T00:00:00Z\tgithub.com/old/repo\trun\t999\tdeleted\t\n"
	if err := os.WriteFile(h.logPath, []byte(prior), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2), snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)
	if sum.Deleted != 2 {
		t.Errorf("a populated log changed the pass: deleted %d, want 2 (R24)", sum.Deleted)
	}
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.HasPrefix(string(data), prior) {
		t.Errorf("the prior session's lines were not preserved; the log must be append-only (R29)")
	}
	// The prior line plus two new lines: appended, never rewritten.
	if got := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; got != 3 {
		t.Errorf("log has %d lines, want 3 (1 prior + 2 new, appended) (R29)", got)
	}
}

// TestCancellationStopsAndLeavesNoJobState pins AC11 and R16: cancelling mid-Purge
// stops promptly with no further DELETE, and the state directory holds only the
// deletion log afterwards, no job record, resolved-ID list or progress file (R23).
func TestCancellationStopsAndLeavesNoJobState(t *testing.T) {
	h := newHarness(t, "delete_ok", 50, 50)
	ctx, cancel := context.WithCancel(context.Background())
	h.counting.onDelete = func(n int) {
		if n == 3 {
			cancel() // cancel after the third DELETE lands
		}
	}
	c := h.confirmed(t, ops.OpDelete, items("o", "r", 1, 2, 3, 4, 5, 6, 7, 8), snapshot(writableRepo("o", "r")))
	sum := runPurgeCtx(t, h, ctx, c)

	if !sum.Cancelled {
		t.Errorf("a cancelled Purge did not report cancellation (R16, AC11)")
	}
	if got := h.counting.deletes(); got > 4 {
		t.Errorf("issued %d DELETEs after cancellation; at most one further in-flight may complete (R16, AC11)", got)
	}
	if sum.Deleted == 8 {
		t.Errorf("the whole set deleted despite cancellation (R16)")
	}
	// The state directory holds only the deletion log: no job record, no resolved-ID
	// list, no progress file (R23, AC11).
	assertOnlyLogInStateDir(t, h)
}

// assertOnlyLogInStateDir fails if the state directory holds anything but the
// deletion log and its rotated generations (R23, AC11).
func assertOnlyLogInStateDir(t *testing.T, h *harness) {
	t.Helper()
	dir := filepath.Dir(h.logPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != "deletions.log" && !strings.HasPrefix(name, "deletions.log.") {
			t.Errorf("state directory holds %q, which is not the deletion log; a Purge writes no job state (R23, AC11)", name)
		}
	}
}
