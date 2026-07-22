package domain_test

import (
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestRepoIDString pins the host/owner/name spelling ADR-0014 fixes: it is the
// exact string purge R29's deletion log writes and R4's tuple carries, so the
// separators and their order are load-bearing.
func TestRepoIDString(t *testing.T) {
	id := domain.RepoID{Host: "github.com", Owner: "cli", Name: "cli"}
	if got := id.String(); got != "github.com/cli/cli" {
		t.Fatalf("RepoID.String() = %q, want %q", got, "github.com/cli/cli")
	}
}

// TestEffectiveStartPrefersRunStartedAt pins the Feed's sort key (live-run-feed
// R8): when the API served a run_started_at, EffectiveStart is that instant, not
// created_at.
func TestEffectiveStartPrefersRunStartedAt(t *testing.T) {
	created := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	started := time.Date(2026, 7, 16, 10, 5, 0, 0, time.UTC)
	r := domain.Run{CreatedAt: created, RunStartedAt: started}

	if got := r.EffectiveStart(); !got.Equal(started) {
		t.Fatalf("EffectiveStart() = %v, want the run_started_at %v", got, started)
	}
}

// TestEffectiveStartFallsBackToCreatedAt pins R8's fallback: where the API served
// null for run_started_at (the zero time), the sort key is created_at, so a Run
// that has not started still sorts by when it was created.
func TestEffectiveStartFallsBackToCreatedAt(t *testing.T) {
	created := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	r := domain.Run{CreatedAt: created} // RunStartedAt is the zero time

	if got := r.EffectiveStart(); !got.Equal(created) {
		t.Fatalf("EffectiveStart() = %v, want the created_at fallback %v", got, created)
	}
}
