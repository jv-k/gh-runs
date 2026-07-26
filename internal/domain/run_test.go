package domain_test

import (
	"encoding/json"
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

// TestWorkflowStateIsStampedNotDecoded pins ADR-0014's stamping rule for the field
// run-detail R8's deleted marker reads. The run object carries no Workflow state
// key: all 35 were enumerated and none is one, exactly as none carries the Workflow
// name. The State is therefore resolved client-side by joining WorkflowID against
// the repository's Workflow list, where the fan-out already holds both sides, and a
// payload that happened to carry a state key must never fill it.
func TestWorkflowStateIsStampedNotDecoded(t *testing.T) {
	body := `{"id":42,"workflow_id":7,"state":"active","workflow_state":"active"}`

	var r domain.Run
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if r.WorkflowState != "" {
		t.Errorf("WorkflowState decoded to %q from the run object; it is stamped, never decoded", r.WorkflowState)
	}
	if r.WorkflowID != 7 {
		t.Errorf("WorkflowID = %d, want the 7 the join reads", r.WorkflowID)
	}
}

// TestUnresolvedWorkflowStateIsNotDeleted pins the honest zero value. A Run whose
// join found nothing keeps the empty State, and the empty State is not the deleted
// one: an unresolved Workflow must never mark a Run as Orphaned (run-detail R8).
func TestUnresolvedWorkflowStateIsNotDeleted(t *testing.T) {
	var r domain.Run
	if r.WorkflowState == domain.StateDeleted {
		t.Fatal("the zero WorkflowState reads as deleted; an unresolved join would mark every Run Orphaned")
	}
}
