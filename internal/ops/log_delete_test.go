package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// The log-deletion suite proves log-viewer R17: deleting a Run's logs travels the same
// single mutation entry a Run, a Cache and an Artifact deletion do, ops.Execute over the
// store-governor-limiter chain, against a go-vcr cassette and never a live account (purge
// R28). There is no second delete site: Execute's deletePath resolves the endpoint per Item
// Kind, so a log DELETE targets runs/{id}/logs while a Run DELETE targets runs/{id}, and the
// two are told apart by the deletion log's kind column, not the id (purge R29). That column
// is the whole point of log-viewer R17: "the logs die and the Run lives" is one line of
// kind log, and deleting the Run later writes another of kind run carrying the same id.

// TestExecuteDeletesLogsViaTheOneMutationEntry pins log-viewer R17 and its binding of purge
// R29: a Plan over a single LogItem deletes the Run's logs through ops.Execute, the DELETE
// carries the Run's id against the logs endpoint and never the Run's own endpoint, and the
// attempt writes one deletion-log line whose kind is log and whose id is the Run's (R29).
func TestExecuteDeletesLogsViaTheOneMutationEntry(t *testing.T) {
	h := newHarness(t, "delete_log_ok", 50, 50)
	run := completedRun(4675883901, "o", "r")
	sel := []ops.Item{ops.LogItem(run)}
	c := h.confirmed(t, ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Deleted != 1 || sum.FailedCount() != 0 || sum.Skipped != 0 {
		t.Errorf("summary = deleted %d, failed %d, skipped %d; want 1/0/0 (R17)", sum.Deleted, sum.FailedCount(), sum.Skipped)
	}
	if h.counting.deletes() != 1 {
		t.Fatalf("issued %d DELETEs, want 1 (one per Run's logs)", h.counting.deletes())
	}
	// The DELETE targets the Run's logs endpoint, deleting the logs and leaving the Run: a
	// DELETE against runs/{id} would destroy the Run itself, which is the operation R17
	// exists to be distinct from (AC6).
	urls := h.counting.urls("DELETE")
	if len(urls) != 1 || !strings.HasSuffix(urls[0], "/actions/runs/4675883901/logs") {
		t.Fatalf("log DELETE URL = %v, want it to target runs/4675883901/logs (R17)", urls)
	}
	for _, u := range urls {
		if strings.HasSuffix(u, "/actions/runs/4675883901") {
			t.Errorf("a DELETE targeted the Run itself, not its logs: %s (R17, AC6)", u)
		}
	}

	// R29: one line per attempt, carrying kind log and the Run's id. The kind column is what
	// keeps this line distinct from a later Run deletion carrying the same id (R29).
	log := h.readLog(t)
	if len(log) != 1 {
		t.Fatalf("log has %d lines, want one per attempt (R29): %+v", len(log), log)
	}
	if log[0].kind != "log" {
		t.Errorf("log line kind = %q, want log (R29: the kind separates deleting logs from deleting the Run)", log[0].kind)
	}
	if log[0].id != "4675883901" {
		t.Errorf("log line id = %q, want the Run's id 4675883901 (R17, R29)", log[0].id)
	}
	if log[0].outcome != "deleted" {
		t.Errorf("log line outcome = %q, want deleted (R29)", log[0].outcome)
	}
	if log[0].repo != "github.com/o/r" {
		t.Errorf("log line repo = %q, want host-qualified github.com/o/r (R29)", log[0].repo)
	}
}

// TestLogDeletionLogGatesTheDelete pins log-viewer R17's binding of purge R29's failure
// mode: a deletion log that cannot be written stops the log deletion before the first
// DELETE. The transport fails any wire request, so a single leaked DELETE would fail the
// test loudly, which is also the no-live-DELETE proof (purge R28). R17 spells this out:
// "a log that cannot be written stops the deletion", and R29 does not ride in on R17's
// R4-to-R9 citation, so this is the test that pins it.
func TestLogDeletionLogGatesTheDelete(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	h = h.withLog(t, filepath.Join(blocker, "gh-runs", "deletions.log")) // parent is a file: unwritable

	sel := []ops.Item{ops.LogItem(completedRun(4675883901, "o", "r"))}
	c := h.confirmed(t, ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	sum, err := h.ops.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("Execute returned an error rather than a reported outcome: %v", err)
	}
	if !sum.LogFailed {
		t.Errorf("Execute did not report the log failure; R17 binds R29's no-record-no-deletion to log deletion")
	}
	if !strings.Contains(sum.Reason, "deletion log") {
		t.Errorf("summary reason %q does not name the log", sum.Reason)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("issued %d DELETEs with an unwritable log; log deletion must issue zero (R17, R29)", h.counting.deletes())
	}
}

// TestLogDeletionSkipsArchivedRepo pins log-viewer R17's eligibility gate: a Run in an
// archived repository has its logs untouched, because an archived repository is permanently
// read-only and its logs can never be deleted (R17, constraints table). The skip is worded
// for archived, which R11 distinguishes from merely read-only. No DELETE is issued.
func TestLogDeletionSkipsArchivedRepo(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	sel := []ops.Item{ops.LogItem(completedRun(4675883901, "o", "archived"))}
	repos := snapshot(domain.Repo{
		ID:          repoID("o", "archived"),
		Permissions: domain.Permissions{Push: true},
		Archived:    true,
	})
	c := h.confirmed(t, ops.OpDelete, sel, repos)
	sum := runPurge(t, h, c)

	if sum.Skipped != 1 || sum.Deleted != 0 {
		t.Errorf("summary = skipped %d, deleted %d; want 1/0 (R17: archived repos are read-only)", sum.Skipped, sum.Deleted)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("issued %d DELETEs against an archived repo; R17 must issue zero", h.counting.deletes())
	}
	log := h.readLog(t)
	if len(log) != 1 || log[0].kind != "log" || log[0].outcome != "skipped" {
		t.Fatalf("log = %+v, want one skipped log line (R29)", log)
	}
	if !strings.Contains(log[0].reason, "archived") {
		t.Errorf("skip reason = %q, want it to name archived (R11)", log[0].reason)
	}
}
