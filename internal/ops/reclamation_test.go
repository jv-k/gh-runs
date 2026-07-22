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

// The reclamation suite proves Cache and Artifact deletion travels the same single
// mutation entry a Run deletion does, ops.Execute over the store-governor-limiter chain,
// against go-vcr cassettes and never a live account (storage-reclamation R17, purge R28).
// There is no second delete site: Execute's deletePath resolves the endpoint per Item
// Kind, so a Cache, an Artifact and a Run share one code path, one failure contract and
// one deletion log. Every DELETE below is taped; a request that reached the wire without a
// tape would error under ModeReplayOnly, and the archived and log-gated paths issue zero
// requests against a transport that would fail loudly if one leaked.

// cacheItem freezes a Cache into an Item for a reclamation Plan (storage-reclamation R17).
func cacheItem(id int64, owner, name, key string) ops.Item {
	return ops.CacheItem(domain.Cache{ID: id, Key: key, Repo: repoID(owner, name)})
}

// artifactItem freezes an Artifact into an Item. expired marks a Tombstone, whose
// deletion is logged like any other because R29 records what was destroyed, not what was
// reclaimed (storage-reclamation R17).
func artifactItem(id int64, owner, name, artname string, expired bool) ops.Item {
	return ops.ArtifactItem(domain.Artifact{ID: id, Name: artname, Expired: expired, Repo: repoID(owner, name)})
}

// TestExecuteDeletesCacheAndArtifactViaTheOneMutationEntry pins storage-reclamation R17
// and AC10: a Plan mixing one Cache and one Artifact deletes both through ops.Execute, the
// Cache DELETE carries the Cache's id and never its key, and each attempt writes one
// deletion-log line carrying its own kind (cache, artifact) and R16's id (purge R29).
func TestExecuteDeletesCacheAndArtifactViaTheOneMutationEntry(t *testing.T) {
	h := newHarness(t, "reclaim_ok", 50, 50)
	sel := []ops.Item{
		cacheItem(987654321, "o", "r", "setup-go-macOS-arm64-go-1.26.5-06fc251f3"),
		artifactItem(112233445, "o", "r", "build-logs", false),
	}
	c := h.confirmed(t, ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Deleted != 2 || sum.FailedCount() != 0 || sum.Skipped != 0 {
		t.Errorf("summary = deleted %d, failed %d, skipped %d; want 2/0/0 (R17)", sum.Deleted, sum.FailedCount(), sum.Skipped)
	}
	if h.counting.deletes() != 2 {
		t.Fatalf("issued %d DELETEs, want 2 (one per row)", h.counting.deletes())
	}
	// AC10: the Cache DELETE targets the id, not the key. The key names a repository and a
	// ref, so it may name more than one Cache; the id is precise (R16).
	urls := h.counting.urls("DELETE")
	var sawCacheByID bool
	for _, u := range urls {
		if strings.Contains(u, "/actions/caches/987654321") {
			sawCacheByID = true
		}
		if strings.Contains(u, "setup-go-macOS-arm64") {
			t.Errorf("a DELETE carried the Cache key rather than its id: %s (R16, AC10)", u)
		}
	}
	if !sawCacheByID {
		t.Errorf("no DELETE targeted the Cache's id 987654321; URLs were %v (AC10)", urls)
	}

	// R29: one line per attempt, each carrying its own kind and R16's id.
	log := h.readLog(t)
	if len(log) != 2 {
		t.Fatalf("log has %d lines, want one per attempt (R29): %+v", len(log), log)
	}
	got := map[string]logField{}
	for _, f := range log {
		got[f.kind] = f
	}
	if got["cache"].id != "987654321" || got["cache"].outcome != "deleted" {
		t.Errorf("cache log line = %+v, want id 987654321 outcome deleted (R29)", got["cache"])
	}
	if got["artifact"].id != "112233445" || got["artifact"].outcome != "deleted" {
		t.Errorf("artifact log line = %+v, want id 112233445 outcome deleted (R29)", got["artifact"])
	}
	if got["cache"].repo != "github.com/o/r" {
		t.Errorf("cache log repo = %q, want host-qualified github.com/o/r (R29)", got["cache"].repo)
	}
}

// TestReclamationGoneAndForbidden pins storage-reclamation R22 and R21 via R23a's reuse of
// the Purge's failure contract: a Cache DELETE answering 404 is gone-and-counted-success,
// and an Artifact DELETE answering an authorization 403 is a failure recorded with its
// reason. The outcome vocabulary is R29's, applied to the Cache and Artifact kinds.
func TestReclamationGoneAndForbidden(t *testing.T) {
	h := newHarness(t, "reclaim_mixed", 50, 50)
	sel := []ops.Item{
		cacheItem(987654321, "o", "r", "setup-go-macOS-arm64-go-1.26.5-06fc251f3"),
		artifactItem(112233445, "o", "r", "build-logs", false),
	}
	c := h.confirmed(t, ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	sum := runPurge(t, h, c)

	if sum.Gone != 1 {
		t.Errorf("Gone = %d, want 1 (a 404 on a Cache DELETE is success) (R22)", sum.Gone)
	}
	if sum.FailedCount() != 1 {
		t.Errorf("FailedCount = %d, want 1 (an authorization 403 is a failure) (R21)", sum.FailedCount())
	}
	if sum.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0", sum.Deleted)
	}
	log := h.readLog(t)
	byKind := map[string]logField{}
	for _, f := range log {
		byKind[f.kind] = f
	}
	if byKind["cache"].outcome != "gone" {
		t.Errorf("cache outcome = %q, want gone (R22, R29)", byKind["cache"].outcome)
	}
	if byKind["artifact"].outcome != "failed" || !strings.Contains(byKind["artifact"].reason, "403") {
		t.Errorf("artifact line = %+v, want outcome failed with a 403 reason (R21, R29)", byKind["artifact"])
	}
}

// TestReclamationSkipsArchivedAndReadOnly pins storage-reclamation R20 and AC13: a Cache in
// an archived repository and an Artifact in a read-only one are skipped with no DELETE
// issued, and the two skips are worded differently because archived is permanent while
// read-only might change (R20). The one eligible Cache still deletes, so the skip is
// selective rather than the whole Plan refusing.
func TestReclamationSkipsArchivedAndReadOnly(t *testing.T) {
	h := newHarness(t, "reclaim_ok", 50, 50)
	sel := []ops.Item{
		cacheItem(987654321, "o", "r", "eligible"),        // deletes
		cacheItem(200, "o", "archived", "archived-cache"), // skip: archived
		artifactItem(300, "o", "readonly", "logs", false), // skip: read-only
	}
	repos := snapshot(
		writableRepo("o", "r"),
		domain.Repo{ID: repoID("o", "archived"), Permissions: domain.Permissions{Push: true}, Archived: true},
		domain.Repo{ID: repoID("o", "readonly"), Permissions: domain.Permissions{Push: false}},
	)
	c := h.confirmed(t, ops.OpDelete, sel, repos)
	sum := runPurge(t, h, c)

	if sum.Deleted != 1 || sum.Skipped != 2 {
		t.Errorf("summary = deleted %d, skipped %d; want 1/2 (R20, AC13)", sum.Deleted, sum.Skipped)
	}
	if h.counting.deletes() != 1 {
		t.Errorf("issued %d DELETEs; the archived and read-only rows must not be attempted (AC13)", h.counting.deletes())
	}
	var archivedReason, readOnlyReason string
	for _, f := range h.readLog(t) {
		switch f.id {
		case "200":
			archivedReason = f.reason
		case "300":
			readOnlyReason = f.reason
		}
	}
	if !strings.Contains(archivedReason, "archived") {
		t.Errorf("archived skip reason = %q, want it to name archived (R20, AC13)", archivedReason)
	}
	if !strings.Contains(readOnlyReason, "read-only") {
		t.Errorf("read-only skip reason = %q, want it to name read-only (R20, AC13)", readOnlyReason)
	}
	if archivedReason == readOnlyReason {
		t.Errorf("the archived and read-only skips read identically; AC13 requires them worded differently")
	}
}

// TestReclamationLogGatesTheDelete pins storage-reclamation R17's binding of purge R29's
// failure mode: a deletion log that cannot be written stops the reclamation before the
// first DELETE. The transport fails any wire request, so a single leaked DELETE would fail
// the test loudly, which is also the no-live-DELETE proof (purge R28).
func TestReclamationLogGatesTheDelete(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	h = h.withLog(t, filepath.Join(blocker, "gh-runs", "deletions.log")) // parent is a file: unwritable

	sel := []ops.Item{
		cacheItem(987654321, "o", "r", "eligible"),
		artifactItem(112233445, "o", "r", "build-logs", false),
	}
	c := h.confirmed(t, ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	sum, err := h.ops.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("Execute returned an error rather than a reported outcome: %v", err)
	}
	if !sum.LogFailed {
		t.Errorf("Execute did not report the log failure; R17 binds R29's no-record-no-deletion here")
	}
	if !strings.Contains(sum.Reason, "deletion log") {
		t.Errorf("summary reason %q does not name the log", sum.Reason)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("issued %d DELETEs with an unwritable log; reclamation must issue zero (R17, R29)", h.counting.deletes())
	}
}
