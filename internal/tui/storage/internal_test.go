package storage

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// TestFormatBytesSuitedToMagnitude pins R6 and AC6: each figure renders in units suited to
// its own magnitude, and neither the 302,460,229-byte Cache nor the 145,212-byte Artifact
// rounds to zero. The GB figure matches the canon's measured 10.59 GB for 10,587,236,096
// bytes, which is decimal units, not binary.
func TestFormatBytesSuitedToMagnitude(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{302460229, "302.46 MB"},
		{145212, "145.21 KB"},
		{10587236096, "10.59 GB"},
		{47680000, "47.68 MB"},
		{999, "999 B"},
		{0, "0 B"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q (R6, AC6)", c.n, got, c.want)
		}
	}
}

// storageWith builds a Storage tab holding one repository's given Caches and Artifacts, at
// a known size, for the pure-logic tests.
func storageWith(caches []domain.Cache, arts []domain.Artifact) Model {
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Update(StorageFetched(RepoStorage{
		Repo: rid("cli", "cli"), ActiveCachesSizeInBytes: sumCaches(caches), ActiveCachesCount: len(caches),
		Caches: caches, Artifacts: arts, ArtifactsComplete: true,
	}))
	return m
}

func sumCaches(cs []domain.Cache) int64 {
	var s int64
	for _, c := range cs {
		s += c.SizeInBytes
	}
	return s
}

// TestDisplayRowsSortedBySizeDescending pins R4 and AC4: the merged list orders Caches and
// Artifacts by size_in_bytes descending irrespective of kind, so a 302 MB Cache sits above a
// 145 KB Artifact and a large Tombstone sits by its still-reported size.
func TestDisplayRowsSortedBySizeDescending(t *testing.T) {
	m := storageWith(
		[]domain.Cache{{ID: 1, Key: "small-cache", SizeInBytes: 200000}},
		[]domain.Artifact{
			{ID: 2, Name: "big-artifact", SizeInBytes: 302460229},
			{ID: 3, Name: "tiny", SizeInBytes: 145212},
		},
	)
	rows := m.displayRows()
	if len(rows) != 3 {
		t.Fatalf("displayRows returned %d rows, want 3", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].size() < rows[i].size() {
			t.Errorf("rows not sorted by size descending: row %d (%d) < row %d (%d) (R4, AC4)",
				i-1, rows[i-1].size(), i, rows[i].size())
		}
	}
	if rows[0].id() != 2 {
		t.Errorf("largest row id = %d, want the 302 MB artifact id 2 (R4)", rows[0].id())
	}
}

// TestReclaimableExcludesTombstones pins R10 and AC7: an expired Artifact keeps reporting its
// original size_in_bytes, but the reclaimable total the view presents excludes it, so a naive
// sum that believed the Tombstone would be wrong by exactly its bytes.
func TestReclaimableExcludesTombstones(t *testing.T) {
	m := storageWith(nil, []domain.Artifact{
		{ID: 1, Name: "live", SizeInBytes: 1000, Expired: false},
		{ID: 2, Name: "tombstone", SizeInBytes: 9_000_000, Expired: true},
	})
	_, _, artifactReclaim, live, tombstones := m.totals()
	if artifactReclaim != 1000 {
		t.Errorf("reclaimable artifact bytes = %d, want 1000 (the Tombstone's 9 MB reclaims nothing) (R10, AC7)", artifactReclaim)
	}
	if live != 1 || tombstones != 1 {
		t.Errorf("live/tombstone counts = %d/%d, want 1/1 (R9, R10)", live, tombstones)
	}
}

// TestCacheTotalsComeFromUsageNotTheList pins R1 and AC1: the Cache figures are the
// cache-usage endpoint's, never recomputed by summing the enumerated list, so a truncated
// list does not understate the total. Here the held list carries one Cache while the usage
// reports 83 Caches and the full byte figure.
func TestCacheTotalsComeFromUsageNotTheList(t *testing.T) {
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Update(StorageFetched(RepoStorage{
		Repo:                    rid("cli", "cli"),
		ActiveCachesSizeInBytes: 10587236096, // R1: the endpoint's figure
		ActiveCachesCount:       83,
		Caches:                  []domain.Cache{{ID: 1, Key: "one", SizeInBytes: 302460229}},
		ArtifactsComplete:       true,
	}))
	cacheBytes, cacheCount, _, _, _ := m.totals()
	if cacheBytes != 10587236096 || cacheCount != 83 {
		t.Errorf("cache totals = %d bytes/%d, want the usage figures 10587236096/83, not the one-item list (R1, AC1)", cacheBytes, cacheCount)
	}
	// AC2: the list under-accounts the count, so it is labelled incomplete while R1 stands.
	if got := m.incompleteLabel(); got == "" {
		t.Errorf("a one-item list against 83 Caches must be labelled incomplete (R2, AC2)")
	}
}

// TestArtifactsOnlyFilterDropsCaches pins R8: the Artifacts-only filter removes the Caches,
// which the bytes-descending sort would otherwise file above nearly every Artifact.
func TestArtifactsOnlyFilterDropsCaches(t *testing.T) {
	m := storageWith(
		[]domain.Cache{{ID: 1, Key: "c", SizeInBytes: 5_000_000}},
		[]domain.Artifact{{ID: 2, Name: "a", SizeInBytes: 1000}},
	)
	m, _ = m.Update(press("a")) // ArtifactsOnly
	rows := m.displayRows()
	if len(rows) != 1 || rows[0].kind != ops.KindArtifact {
		t.Fatalf("Artifacts-only left %d rows, want 1 Artifact (R8)", len(rows))
	}
	m, _ = m.Update(press("a")) // toggle back
	if len(m.displayRows()) != 2 {
		t.Errorf("toggling off Artifacts-only did not restore the Caches (R8)")
	}
}

// press builds a single-key press message, the internal-test analogue of the Feed's helper.
func press(s string) tea.KeyPressMsg {
	if s == " " {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}
