package storage_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// twoCaches is a repository holding two reclaimable Caches, with R1's figures reporting both.
// The sizes are the canon's measured Cache and a small one beside it, so the adjusted total
// is checkable to the byte and neither rounds to the other's unit (R6).
func twoCaches(owner, name string) storage.RepoStorage {
	return storage.RepoStorage{
		Repo:                    rid(owner, name),
		ActiveCachesSizeInBytes: 302460229 + 145212,
		ActiveCachesCount:       2,
		Caches: []domain.Cache{
			{ID: 1, Repo: rid(owner, name), Key: "setup-go", SizeInBytes: 302460229, LastAccessedAt: day},
			{ID: 2, Repo: rid(owner, name), Key: "npm-lock", SizeInBytes: 145212, LastAccessedAt: day},
		},
		ArtifactsComplete: true,
	}
}

// finished is the terminal frame of a pass that deleted the given objects, the shape
// ADR-0015 broadcasts to every tab and the shape a golden fabricates.
func finished(items ...ops.Item) ops.Progress {
	return ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindCache,
		Done: true,
		Sum:  ops.Summary{Total: len(items), Deleted: len(items), Succeeded: items},
	}
}

func cacheItem(owner, name string, id int64, key string, size int64) ops.Item {
	return ops.CacheItem(domain.Cache{ID: id, Repo: rid(owner, name), Key: key, SizeInBytes: size, LastAccessedAt: day})
}

// frame routes a progress frame to the tab, which the root broadcasts whether or not this
// tab is focused, because an operation outlives the operator's attention (ADR-0015).
func frame(m storage.Model, p ops.Progress) storage.Model {
	m, _ = m.Update(p)
	return m
}

// ansi matches the colour escapes lipgloss renders unconditionally (ADR-0013), which sit
// between the label and its figure and would break a plain substring match on the two
// together.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// summaryOf is the frame's first line with the styling stripped: the scope and the grand
// totals, which is where R24's adjustment has to show.
func summaryOf(m storage.Model) string {
	return ansi.ReplaceAllString(strings.SplitN(m.View(), "\n", 2)[0], "")
}

// TestTheTotalAdjustsByTheDeletedBytes is AC12: deleting a Cache of 302,460,229 bytes
// decreases the displayed total by exactly 302,460,229, and the row it deleted leaves the
// list. The figures R1 makes authoritative come from the cache-usage endpoint rather than
// from the enumerated list, so removing the row alone would leave the headline unchanged and
// a completed reclamation reading as though it had recovered nothing.
func TestTheTotalAdjustsByTheDeletedBytes(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	if got := summaryOf(m); !strings.Contains(got, "Caches 302.61 MB (2)") {
		t.Fatalf("the starting total is not the endpoint's own figure for both Caches: %s", got)
	}

	m = frame(m, finished(cacheItem("cli", "cli", 1, "setup-go", 302460229)))

	if got := summaryOf(m); !strings.Contains(got, "Caches 145.21 KB (1)") {
		t.Errorf("the total did not fall by exactly the deleted Cache's bytes (R24, AC12): %s", got)
	}
	got := m.View()
	if strings.Contains(got, "setup-go") {
		t.Errorf("the deleted Cache is still a row in the list (R24):\n%s", got)
	}
	if !strings.Contains(got, "npm-lock") {
		t.Errorf("the Cache that was not deleted left the list:\n%s", got)
	}
}

// TestADeletedArtifactLeavesTheReclaimableTotal pins R24 over the other Kind. An Artifact's
// figures are summed from the enumerated list rather than read off an endpoint, so the row
// leaving is the whole adjustment, and the reclaimable total falls by the bytes that were
// actually recoverable (R10).
func TestADeletedArtifactLeavesTheReclaimableTotal(t *testing.T) {
	live := domain.Artifact{ID: 7, Repo: rid("cli", "cli"), Name: "build-output", SizeInBytes: 145212, ExpiresAt: day.AddDate(0, 0, 30)}
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{Repo: rid("cli", "cli"), Artifacts: []domain.Artifact{live}, ArtifactsComplete: true})
	if got := summaryOf(m); !strings.Contains(got, "Artifacts 145.21 KB reclaimable, 1 live") {
		t.Fatalf("the starting Artifact total is not the live Artifact's bytes: %s", got)
	}

	m = frame(m, ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindArtifact, Done: true,
		Sum: ops.Summary{Total: 1, Deleted: 1, Succeeded: []ops.Item{ops.ArtifactItem(live)}},
	})

	if got := summaryOf(m); !strings.Contains(got, "Artifacts 0 B reclaimable, 0 live") {
		t.Errorf("the deleted Artifact still counts toward the reclaimable total (R24): %s", got)
	}
	if got := m.View(); strings.Contains(got, "build-output") {
		t.Errorf("the deleted Artifact is still a row in the list (R24):\n%s", got)
	}
}

// TestARefreshDoesNotRaiseTheTotalBack is R24's second sentence. The cache-usage endpoint's
// figures can lag a deletion, so a refresh that still counts a Cache this session destroyed
// must not put its bytes back on the total: the operator would read a completed reclamation
// as a failed one.
func TestARefreshDoesNotRaiseTheTotalBack(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	m = frame(m, finished(cacheItem("cli", "cli", 1, "setup-go", 302460229)))

	// The endpoint answers exactly as it did before the deletion.
	m = fetched(m, twoCaches("cli", "cli"))

	if got := summaryOf(m); !strings.Contains(got, "Caches 145.21 KB (1)") {
		t.Errorf("a lagging refresh raised the total back over the deleted Cache (R24): %s", got)
	}
	if got := m.View(); strings.Contains(got, "setup-go") {
		t.Errorf("a lagging refresh brought the deleted Cache back into the list (R24):\n%s", got)
	}
}

// TestACaughtUpRefreshIsNotDeductedTwice pins the other side of the same rule. Once the
// endpoint stops reporting the Cache, its figures have already come down, and deducting the
// session's record again would report less storage than the repository has. The enumerated
// list is the oracle for which of the two states the endpoint is in, which is the same
// reconciliation R2 already makes it.
func TestACaughtUpRefreshIsNotDeductedTwice(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	m = frame(m, finished(cacheItem("cli", "cli", 1, "setup-go", 302460229)))

	m = fetched(m, oneCache("cli", "cli", 2, "npm-lock", 145212)) // the endpoint has caught up

	if got := summaryOf(m); !strings.Contains(got, "Caches 145.21 KB (1)") {
		t.Errorf("the deleted Cache was deducted a second time, under-reporting the storage: %s", got)
	}
}

// TestTheReclaimedBytesAreReported is R24's first clause: a completed reclamation says how
// much it recovered. The count travels with it, because the figure alone cannot distinguish
// a large Cache from a set of expired Artifacts that recovered nothing (R10, AC8).
func TestTheReclaimedBytesAreReported(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	m = frame(m, finished(
		cacheItem("cli", "cli", 1, "setup-go", 302460229),
		cacheItem("cli", "cli", 2, "npm-lock", 145212),
	))

	if got := m.View(); !strings.Contains(got, "Reclaimed 302.61 MB across 2 objects") {
		t.Errorf("the completed reclamation did not report what it recovered (R24):\n%s", got)
	}
}

// TestAnotherSurfacesOperationChangesNothingHere pins the broadcast rule's other half. Every
// tab sees every frame, so a Purge over Runs reaches this one, and it holds no Runs: nothing
// in its figures may move on account of an operation over objects it does not show.
func TestAnotherSurfacesOperationChangesNothingHere(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	before := summaryOf(m)

	purge := ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindRun,
		Done: true,
		Sum: ops.Summary{
			Total: 1, Deleted: 1,
			Succeeded: []ops.Item{ops.RunItem(domain.Run{ID: 1, Repo: rid("cli", "cli")})},
		},
	}
	m = frame(m, purge)

	if got := summaryOf(m); got != before {
		t.Errorf("a Purge over Runs moved the storage figures:\n got %s\nwant %s", got, before)
	}
	if got := m.View(); strings.Contains(got, "Reclaimed") {
		t.Errorf("a Purge over Runs reported reclaimed bytes on the Storage tab:\n%s", got)
	}
}

// TestAnIncompleteFrameChangesNothing pins that the adjustment waits for the terminal frame.
// A running pass emits a frame per attempt, and the tally is not final until Done: adjusting
// on the way would move the figures under a pass that the breaker, a log failure or a cancel
// can still stop.
func TestAnIncompleteFrameChangesNothing(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, twoCaches("cli", "cli"))
	before := summaryOf(m)

	running := ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindCache,
		Sum:  ops.Summary{Total: 2, Deleted: 1, Succeeded: []ops.Item{cacheItem("cli", "cli", 1, "setup-go", 302460229)}},
	}
	m = frame(m, running)

	if got := summaryOf(m); got != before {
		t.Errorf("a mid-pass frame adjusted the totals:\n got %s\nwant %s", got, before)
	}
}
