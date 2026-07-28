package confirm_test

import (
	"strings"
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
// confirms, and the single-Run asymmetry is the Feed's, not the pane's). It also fixes
// R5 and AC6's offer: the frame carries force-cancel as the named escalation, which is
// the one line that distinguishes a cancel modal from a Purge's.
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

// jobItemFor freezes a Job of the named Run into an Item, the shape a by-name resolution
// produces where the name matched.
func jobItemFor(jobID, runID int64, owner, name, job string) ops.Item {
	return ops.JobItem(domain.Job{ID: jobID, RunID: runID, Repo: repoID(owner, name), Name: job})
}

// planByName builds the Plan a by-name per-Job re-run produces: Job Items for the Runs that
// matched, and Item-less members for the Runs that did not.
func planByName(t *testing.T, threshold int, items []ops.Item, unmatched []ops.Unmatched, repos ...domain.Repo) ops.Plan {
	t.Helper()
	m := make(map[domain.RepoID]domain.Repo)
	for _, r := range repos {
		m[r.ID] = r
	}
	p, err := planOps(threshold).Plan(ops.OpRerunJob, items, m, unmatched...)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// TestGoldenUnmatchedInspectRows fixes the inspect viewport over a frozen set holding both
// kinds of member (ADR-0019 amended, purge AC22 as narrowed).
//
// The narrowing is what this golden pins. Every row that names a write is a tuple Execute is
// handed and carries all four cells. An Item-less row names the absence of a write: it
// renders its repository and its Run ID, and leaves the Status and Conclusion cells empty on
// the same reading that already empties Conclusion for a Run that is not completed. It takes
// no part in the run_started_at ordering, and it appends after the Items rather than
// interleaving by Run, which would put a member the operator cannot act on between two they
// can.
func TestGoldenUnmatchedInspectRows(t *testing.T) {
	const absent = `no job named "build" in this run`
	items := []ops.Item{
		jobItemFor(9001, 501, "octo", "hello", "build"),
		jobItemFor(9002, 502, "octo", "hello", "build"),
	}
	unmatched := []ops.Unmatched{
		{Repo: repoID("octo", "hello"), RunID: 503, Reason: absent},
		{Repo: repoID("octo", "hello"), RunID: 504, Reason: absent},
	}
	m := sized(planByName(t, 50, items, unmatched, writable("octo", "hello")), 100, 24)
	m = send(m, "v")
	if !m.Inspecting() {
		t.Fatal("the inspect key did not open R30's viewport")
	}
	goldie.New(t).Assert(t, "unmatched_inspect", []byte(m.View()))
}

// TestGoldenUnmatchedModal fixes the modal over a set holding both kinds of member. The
// headline's count and the breakdown row both read the whole set, so the Item-less members
// are inside the 4 rather than beside it, and R11's eligibility split gains a line for them
// carrying the resolution's own reason in full (ADR-0019 amended, run-lifecycle AC14c).
func TestGoldenUnmatchedModal(t *testing.T) {
	const absent = `no job named "build" in this run`
	items := []ops.Item{
		jobItemFor(9001, 501, "octo", "hello", "build"),
		jobItemFor(9002, 502, "octo", "hello", "build"),
	}
	unmatched := []ops.Unmatched{
		{Repo: repoID("octo", "hello"), RunID: 503, Reason: absent},
		{Repo: repoID("octo", "hello"), RunID: 504, Reason: absent},
	}
	m := sized(planByName(t, 50, items, unmatched, writable("octo", "hello")), 100, 24)
	goldie.New(t).Assert(t, "unmatched_modal", []byte(m.View()))
}

// TestGoldenUnreachedNote fixes run-lifecycle R17a's one-line non-blocking note. A by-name
// resolution the API cut short freezes what it resolved, so the operator is asked to confirm
// a count smaller than the set they named. R7's ladder has no rung that says "this count is
// a lower bound", so the pane says it instead: how many selected Runs were not reached, and
// why. It neither blocks nor confirms, on R14b's terms.
func TestGoldenUnreachedNote(t *testing.T) {
	items := []ops.Item{
		jobItemFor(9001, 501, "octo", "hello", "build"),
		jobItemFor(9002, 502, "octo", "hello", "build"),
		jobItemFor(9003, 601, "octo", "world", "build"),
	}
	p := planByName(t, 50, items, nil, writable("octo", "hello"), writable("octo", "world"))
	m := confirm.New(keys.Standard).Open(p).
		WithUnreached(28, "the API rate-limited the jobs listing: HTTP 403")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	goldie.New(t).Assert(t, "unreached_note", []byte(m.View()))
}

// TestUnreachedNoteNeitherBlocksNorConfirms pins R17a's and R14b's shared terms directly
// rather than through a golden: the note is text, and the confirmation behaves exactly as it
// would without it. A set spanning two repositories prices at the typed count either way,
// and typing that count still starts it (AC14d).
func TestUnreachedNoteNeitherBlocksNorConfirms(t *testing.T) {
	items := []ops.Item{
		jobItemFor(9001, 501, "octo", "hello", "build"),
		jobItemFor(9003, 601, "octo", "world", "build"),
	}
	p := planByName(t, 50, items, nil, writable("octo", "hello"), writable("octo", "world"))
	m := confirm.New(keys.Standard).Open(p).WithUnreached(28, "the API rate-limited the jobs listing")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	if p.Friction() != ops.FrictionTypedCount {
		t.Fatalf("a cross-repository set priced at %v, want the typed count (R17)", p.Friction())
	}
	if m.Outcome() != confirm.Pending {
		t.Fatalf("the note confirmed by itself; it must not (R17a, R14b)")
	}
	for _, r := range "2" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Outcome() != confirm.Confirmed {
		t.Errorf("typing the frozen count did not confirm; the note must not block (R17a, R14b)")
	}
}

// TestOpenClearsAStaleUnreachedNote pins the note's lifetime. It belongs to one resolution,
// so a pane reopened over a set that resolved cleanly must not still be claiming Runs went
// unreached. Open resets the collection state for the same reason.
func TestOpenClearsAStaleUnreachedNote(t *testing.T) {
	items := []ops.Item{jobItemFor(9001, 501, "octo", "hello", "build")}
	p := planByName(t, 50, items, nil, writable("octo", "hello"))
	m := confirm.New(keys.Standard).Open(p).WithUnreached(28, "rate limited")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if !strings.Contains(m.View(), "28") {
		t.Fatal("the note did not render at all")
	}
	m = m.Open(p)
	if strings.Contains(m.View(), "28") {
		t.Error("a reopened pane still shows the prior resolution's unreached note")
	}
}
