package domain_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestReclaimableBytesForLiveArtifact pins the honest case: a live Artifact's
// reclaimable bytes are its size_in_bytes, because deleting it recovers them.
func TestReclaimableBytesForLiveArtifact(t *testing.T) {
	a := domain.Artifact{SizeInBytes: 1024, Expired: false}
	if got := a.ReclaimableBytes(); got != 1024 {
		t.Fatalf("ReclaimableBytes() = %d, want 1024 for a live artifact", got)
	}
}

// TestReclaimableBytesForTombstone pins the measured trap ADR-0014 encodes: an
// expired Artifact still reports its original size_in_bytes, but deleting it
// recovers nothing, so ReclaimableBytes is 0. Believing size_in_bytes here is the
// mistake that had 15.50 of cli/cli's 15.55 GB reclaim nothing (PRD).
func TestReclaimableBytesForTombstone(t *testing.T) {
	a := domain.Artifact{SizeInBytes: 1024, Expired: true}
	if got := a.ReclaimableBytes(); got != 0 {
		t.Fatalf("ReclaimableBytes() = %d, want 0 for a tombstoned artifact", got)
	}
}

// TestTombstoneReflectsExpiry pins that Tombstone reports whether the bytes are
// already gone, which is exactly the expired flag.
func TestTombstoneReflectsExpiry(t *testing.T) {
	if (domain.Artifact{Expired: true}).Tombstone() != true {
		t.Fatal("Tombstone() = false for an expired artifact, want true")
	}
	if (domain.Artifact{Expired: false}).Tombstone() != false {
		t.Fatal("Tombstone() = true for a live artifact, want false")
	}
}
