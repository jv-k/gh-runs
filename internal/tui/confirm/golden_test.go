package confirm_test

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
)

// The goldens render the modal and the inspect view from held state alone, at 100
// columns, with no terminal and no network (R30, AC22). lipgloss v2 renders truecolour
// regardless of the environment, so these bytes are stable on any machine (ADR-0013).
// Regenerate with: go test ./internal/tui/confirm/ -run Golden -update.

func gStart(day, hour int) time.Time {
	return time.Date(2026, 7, day, hour, 0, 0, 0, time.UTC)
}

// gRun builds a Run with a fixed start so the STARTED column is deterministic.
func gRun(id int64, owner, name, workflow string, st domain.Status, cc domain.Conclusion, start time.Time) domain.Run {
	return domain.Run{ID: id, Repo: repoID(owner, name), Name: workflow, WorkflowName: workflow, Status: st, Conclusion: cc, RunStartedAt: start}
}

func planFrom(t *testing.T, threshold int, items []ops.Item, repos ...domain.Repo) ops.Plan {
	t.Helper()
	return planForOp(t, ops.OpDelete, threshold, items, repos...)
}

func planForOp(t *testing.T, op ops.Operation, threshold int, items []ops.Item, repos ...domain.Repo) ops.Plan {
	t.Helper()
	m := make(map[domain.RepoID]domain.Repo)
	for _, r := range repos {
		m[r.ID] = r
	}
	p, err := planOps(threshold).Plan(op, items, m)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

func sized(p ops.Plan, w, h int) confirm.Model {
	m := confirm.New(keys.Standard).Open(p)
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// TestGoldenYNModal fixes the below-threshold single-repository modal: a count, a
// one-line breakdown, and the y/N prompt naming the inspect key (R6, R7, R30).
func TestGoldenYNModal(t *testing.T) {
	items := []ops.Item{
		ops.RunItem(gRun(101, "octo", "hello", "CI", domain.StatusCompleted, domain.ConclusionSuccess, gStart(20, 10))),
		ops.RunItem(gRun(102, "octo", "hello", "CI", domain.StatusCompleted, domain.ConclusionFailure, gStart(20, 9))),
		ops.RunItem(gRun(103, "octo", "hello", "Release", domain.StatusCompleted, domain.ConclusionSuccess, gStart(20, 8))),
	}
	p := planFrom(t, 50, items, writable("octo", "hello"))
	goldie.New(t).Assert(t, "yn_modal", []byte(sized(p, 100, 20).View()))
}

// TestGoldenCancelModal fixes the reused pane rendering a bulk cancel (run-lifecycle
// R17): the same modal shape as a Purge, with the verb tracking the operation, so the
// y/N prompt reads "Cancel these Runs?" rather than "Delete these Runs?". A single-repo
// set of three cancels below the threshold confirms with y/N (R18's bulk case still
// confirms, and the single-Run asymmetry is the Feed's, not the pane's).
func TestGoldenCancelModal(t *testing.T) {
	items := []ops.Item{
		ops.RunItem(gRun(201, "octo", "hello", "CI", domain.StatusInProgress, "", gStart(20, 10))),
		ops.RunItem(gRun(202, "octo", "hello", "CI", domain.StatusInProgress, "", gStart(20, 9))),
		ops.RunItem(gRun(203, "octo", "hello", "Release", domain.StatusQueued, "", gStart(20, 8))),
	}
	p := planForOp(t, ops.OpCancel, 50, items, writable("octo", "hello"))
	goldie.New(t).Assert(t, "cancel_modal", []byte(sized(p, 100, 20).View()))
}

// TestGoldenTypedCountModal fixes the at-threshold single-repository modal: the typed
// count prompt echoing a partial entry, which y cannot start (R7, R7a, AC7).
func TestGoldenTypedCountModal(t *testing.T) {
	items := make([]ops.Item, 60)
	for i := range items {
		items[i] = ops.RunItem(gRun(int64(200+i), "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionSuccess, gStart(20, 10)))
	}
	p := planFrom(t, 50, items, writable("cli", "cli"))
	m := sized(p, 100, 20)
	m = send(m, "6") // a partial typed count, echoed
	goldie.New(t).Assert(t, "typed_count_modal", []byte(m.View()))
}

// TestGoldenEligibilitySplit fixes AC1, AC11 and AC15: a cross-repository set whose
// breakdown sums to the total, with the read-only and archived skips stated before the
// Purge and distinguished from each other, and the cross-repo typed-count prompt.
func TestGoldenEligibilitySplit(t *testing.T) {
	var items []ops.Item
	for i := 0; i < 40; i++ {
		items = append(items, ops.RunItem(gRun(int64(1000+i), "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionSuccess, gStart(20, 10))))
	}
	for i := 0; i < 4; i++ {
		items = append(items, ops.RunItem(gRun(int64(2000+i), "acme", "api", "Deploy", domain.StatusCompleted, domain.ConclusionSuccess, gStart(19, 8))))
	}
	for i := 0; i < 3; i++ {
		items = append(items, ops.RunItem(gRun(int64(3000+i), "old", "legacy", "Nightly", domain.StatusCompleted, domain.ConclusionSuccess, gStart(18, 6))))
	}
	repos := []domain.Repo{
		writable("cli", "cli"),
		{ID: repoID("acme", "api"), Permissions: domain.Permissions{Push: false}},                  // read-only
		{ID: repoID("old", "legacy"), Permissions: domain.Permissions{Push: true}, Archived: true}, // archived
	}
	p := planFrom(t, 50, items, repos...)
	goldie.New(t).Assert(t, "eligibility_split", []byte(sized(p, 100, 24).View()))
}

// TestGoldenInspectView fixes AC22: the frozen set's rows, the Feed's columns and no
// new ones, Conclusion empty on any row whose Status is not completed, at 100 columns.
func TestGoldenInspectView(t *testing.T) {
	items := []ops.Item{
		ops.RunItem(gRun(4675883901, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionSuccess, gStart(20, 10))),
		ops.RunItem(gRun(4675883902, "cli", "cli", "CI", domain.StatusCompleted, domain.ConclusionFailure, gStart(20, 9))),
		ops.RunItem(gRun(4675883903, "acme", "api", "Deploy", domain.StatusInProgress, "", gStart(20, 8))),
		ops.RunItem(gRun(4675883904, "acme", "api", "Deploy", domain.StatusQueued, "", gStart(20, 7))),
		ops.RunItem(gRun(4675883905, "cli", "cli", "Release", domain.StatusCompleted, domain.ConclusionCancelled, gStart(19, 6))),
	}
	p := planFrom(t, 50, items, writable("cli", "cli"), writable("acme", "api"))
	m := sized(p, 100, 20)
	m = send(m, "v") // open the inspect view
	goldie.New(t).Assert(t, "inspect_view", []byte(m.View()))
}
