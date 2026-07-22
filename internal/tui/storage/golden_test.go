package storage_test

import (
	"testing"
	"time"

	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// The goldens render the Storage tab's frame from held state alone, at 100 columns, with no
// terminal and no network (R25). lipgloss v2 renders truecolour regardless of the
// environment, so these bytes are stable on any machine (ADR-0013). Regenerate with:
// go test ./internal/tui/storage/ -run Golden -update.

func gDay(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }

// TestGoldenListUnits fixes AC17's first case and AC6: a list holding the 302,460,229-byte
// Cache and the 145,212-byte Artifact, each rendered in units suited to its own magnitude and
// neither shown as zero, with the Cache above the Artifact under the bytes-descending sort
// (R4, R6). The Cache shows last_accessed_at and the live Artifact shows expires_at (R7, R12).
func TestGoldenListUnits(t *testing.T) {
	m := newStorage(t, 100, 12, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{
		Repo:                    rid("cli", "cli"),
		ActiveCachesSizeInBytes: 302460229,
		ActiveCachesCount:       1,
		Caches: []domain.Cache{
			{ID: 1, Key: "setup-go-macOS-arm64-go-1.26.5-06fc251f3", SizeInBytes: 302460229, LastAccessedAt: gDay(10)},
		},
		Artifacts: []domain.Artifact{
			{ID: 2, Name: "build-logs", SizeInBytes: 145212, Expired: false, ExpiresAt: gDay(31)},
		},
		ArtifactsComplete: true,
	})
	goldie.New(t).Assert(t, "list_units", []byte(m.View()))
}

// TestGoldenTombstone fixes AC17's second case and R9: an Artifact with expired: true renders
// as a tombstone that states on its face that deleting it reclaims nothing, and it is excluded
// from the reclaimable total though it still reports its original size_in_bytes (R10).
func TestGoldenTombstone(t *testing.T) {
	m := newStorage(t, 100, 12, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{
		Repo: rid("cli", "cli"),
		Artifacts: []domain.Artifact{
			{ID: 1, Name: "release-binaries", SizeInBytes: 258000, Expired: false, ExpiresAt: gDay(31)},
			{ID: 2, Name: "old-test-logs", SizeInBytes: 234131, Expired: true, ExpiresAt: gDay(1)},
		},
		ArtifactsComplete: true,
	})
	goldie.New(t).Assert(t, "tombstone", []byte(m.View()))
}

// TestGoldenRollup fixes R0 and R1: under a multi-repository scope the view leads with a
// per-repository rollup ordered by Cache bytes, answering "which of my repositories is
// hoarding Caches?", and the grand total is the sum of the per-repository figures. An archived
// repository is called out as never reclaimable, distinct from a read-only one (R20).
func TestGoldenRollup(t *testing.T) {
	m := newStorage(t, 100, 16, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{
		Repo: rid("cli", "cli"), ActiveCachesSizeInBytes: 10587236096, ActiveCachesCount: 83,
		Caches:            []domain.Cache{{ID: 1, Key: "setup-go", SizeInBytes: 302460229, LastAccessedAt: gDay(10)}},
		Artifacts:         []domain.Artifact{{ID: 2, Name: "logs", SizeInBytes: 47680000, ExpiresAt: gDay(31)}},
		ArtifactsComplete: true,
	})
	m = fetched(m, storage.RepoStorage{
		Repo: rid("octo", "hello"), ActiveCachesSizeInBytes: 302460229, ActiveCachesCount: 2,
		Caches:            []domain.Cache{{ID: 3, Key: "npm", SizeInBytes: 302460229, LastAccessedAt: gDay(9)}},
		ArtifactsComplete: true,
	})
	goldie.New(t).Assert(t, "rollup", []byte(m.View()))
}

// TestGoldenReclaimConfirmTombstone fixes R11 and AC8 over the shared confirmation: confirming
// deletion of an expired Artifact shows a reclaim figure of zero bytes above the modal, while
// the modal itself is the shared pane naming the count and the inspect key, unchanged (R17).
func TestGoldenReclaimConfirmTombstone(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{
		Repo:              rid("cli", "cli"),
		Artifacts:         []domain.Artifact{{ID: 2, Name: "old-test-logs", SizeInBytes: 234131, Expired: true, ExpiresAt: gDay(1)}},
		ArtifactsComplete: true,
	})
	m = send(m, "r")     // populate the eligibility gate from the discovered repositories
	m = send(m, "space") // select the Tombstone
	m = send(m, "d")     // open the confirmation
	goldie.New(t).Assert(t, "reclaim_confirm_tombstone", []byte(m.View()))
}
