package storage_test

import (
	"strconv"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// manyCaches is one repository holding n Caches, with the cache-usage endpoint reporting
// more than the enumeration returned so the list is labelled incomplete (R2). That label is
// a second summary line, and it is the case the viewport arithmetic got most wrong.
func manyCaches(owner, name string, n int) storage.RepoStorage {
	rs := storage.RepoStorage{
		Repo:                    rid(owner, name),
		ActiveCachesSizeInBytes: 10587236096,
		ActiveCachesCount:       n + 40, // the endpoint knows of more than the list holds (R2)
		ArtifactsComplete:       true,
	}
	for i := range n {
		rs.Caches = append(rs.Caches, domain.Cache{
			ID:             int64(i + 1),
			Key:            "setup-go-macOS-arm64-go-1.26.5-" + strconv.Itoa(i),
			SizeInBytes:    int64(302460229 - i),
			LastAccessedAt: day,
		})
	}
	return rs
}

// TestTheFrameNeverOverrunsTheTerminal pins the viewport arithmetic against the frame it
// actually paints. A list longer than the viewport is windowed to listCapacity rows, so the
// whole frame must fit the height the root laid the tab out in: a row drawn past the bottom
// pushes the hint line off screen, which is where the keys the tab acts on are named.
//
// The incomplete label is the case that made it visible. It is a second summary line the
// chrome count did not include, so a labelled frame overran by two rows rather than one.
func TestTheFrameNeverOverrunsTheTerminal(t *testing.T) {
	for h := 8; h <= 20; h++ {
		m := newStorage(t, 100, h, writable("cli", "cli"))
		m = fetched(m, manyCaches("cli", "cli", 40))
		if got := lipgloss.Height(m.View()); got > h {
			t.Errorf("at height %d the frame painted %d rows, overrunning the terminal by %d", h, got, got-h)
		}
	}
}

// TestTheFrameNeverOverrunsTheTerminalWithAStatusLine pins the same invariant with the
// download status line showing, which costs the list another row (R13, R14).
func TestTheFrameNeverOverrunsTheTerminalWithAStatusLine(t *testing.T) {
	for h := 10; h <= 20; h++ {
		m := newStorage(t, 100, h, writable("cli", "cli"))
		m = fetched(m, storage.RepoStorage{
			Repo:              rid("cli", "cli"),
			Artifacts:         manyTombstones(30),
			ArtifactsComplete: true,
		})
		m = send(m, "w") // refused over a Tombstone, which states the bytes are gone (R14)
		if got := lipgloss.Height(m.View()); got > h {
			t.Errorf("at height %d the frame painted %d rows with a status line, overrunning by %d", h, got, got-h)
		}
	}
}

// TestTheFrameNeverOverrunsTheTerminalUnderARollup pins it under the multi-repository scope,
// where the rollup's own lines sit between the summary and the list (R0).
func TestTheFrameNeverOverrunsTheTerminalUnderARollup(t *testing.T) {
	for h := 14; h <= 24; h++ {
		m := newStorage(t, 100, h, writable("cli", "cli"), writable("octo", "hello"))
		m = fetched(m, manyCaches("cli", "cli", 20))
		m = fetched(m, manyCaches("octo", "hello", 20))
		if got := lipgloss.Height(m.View()); got > h {
			t.Errorf("at height %d the rollup frame painted %d rows, overrunning by %d", h, got, got-h)
		}
	}
}

// manyTombstones is n expired Artifacts, each of which renders as a tombstone (R9).
func manyTombstones(n int) []domain.Artifact {
	out := make([]domain.Artifact, 0, n)
	for i := range n {
		out = append(out, domain.Artifact{
			ID:          int64(i + 1),
			Name:        "old-test-logs-" + strconv.Itoa(i),
			SizeInBytes: int64(234131 - i),
			Expired:     true,
			ExpiresAt:   day,
		})
	}
	return out
}
