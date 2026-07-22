package ops_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

func repoID(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// TestRunItemDerivesTuple pins that a constructor derives R4's tuple from the
// object rather than trusting a caller to set it, so the tuple and the object can
// never disagree (ADR-0019).
func TestRunItemDerivesTuple(t *testing.T) {
	r := domain.Run{ID: 4675883901, Repo: repoID("cli", "cli"), Status: domain.StatusCompleted}
	it := ops.RunItem(r)
	if it.Kind != ops.KindRun {
		t.Errorf("Kind = %q, want run", it.Kind)
	}
	if it.ID != 4675883901 {
		t.Errorf("ID = %d, want 4675883901", it.ID)
	}
	if it.Repo != repoID("cli", "cli") {
		t.Errorf("Repo = %v, want cli/cli", it.Repo)
	}
	if it.Run == nil || it.Run.ID != 4675883901 {
		t.Errorf("Run pointer not populated from the object")
	}
}

// TestRunItemCopies pins ADR-0019's freeze-is-a-memory-property rule: the Item
// holds a copy, so mutating the caller's Run afterwards cannot move the frozen
// set (purge R5).
func TestRunItemCopies(t *testing.T) {
	r := domain.Run{ID: 7, Repo: repoID("o", "r"), Status: domain.StatusCompleted}
	it := ops.RunItem(r)
	r.Status = domain.StatusInProgress // mutate the caller's copy after freezing
	if it.Run.Status != domain.StatusCompleted {
		t.Errorf("frozen Item followed a mutation of the source Run: got %q, the set is not frozen (R5)", it.Run.Status)
	}
}

// TestKindConstructorsCarryTheirOwnObject pins that each constructor sets exactly
// its own object pointer and its own Kind, so a log line and Execute's request
// builder read the kind column off one field (ADR-0019, R29).
func TestKindConstructorsCarryTheirOwnObject(t *testing.T) {
	log := ops.LogItem(domain.Run{ID: 9, Repo: repoID("o", "r")})
	if log.Kind != ops.KindLog || log.ID != 9 || log.Run == nil {
		t.Errorf("LogItem: kind=%q id=%d run=%v, want log/9/non-nil", log.Kind, log.ID, log.Run)
	}
	cache := ops.CacheItem(domain.Cache{ID: 11, Repo: repoID("o", "r")})
	if cache.Kind != ops.KindCache || cache.Cache == nil || cache.Run != nil {
		t.Errorf("CacheItem: kind=%q cache=%v run=%v, want cache/non-nil/nil", cache.Kind, cache.Cache, cache.Run)
	}
	art := ops.ArtifactItem(domain.Artifact{ID: 13, Repo: repoID("o", "r")})
	if art.Kind != ops.KindArtifact || art.Artifact == nil {
		t.Errorf("ArtifactItem: kind=%q artifact=%v, want artifact/non-nil", art.Kind, art.Artifact)
	}
}
