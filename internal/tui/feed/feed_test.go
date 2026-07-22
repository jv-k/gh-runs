package feed

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// t0 is a fixed instant so every run's rendered start and every golden is deterministic
// under no clock (R36: held state alone).
var t0 = time.Date(2026, 7, 15, 16, 39, 0, 0, time.UTC)

// repoID builds a github.com-qualified repository identity.
func repoID(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// mkRun builds a Run stamped with its repository and workflow name, the shape the
// fan-out emits (ADR-0014, ADR-0018).
func mkRun(id int64, owner, name, workflow string, st domain.Status, cc domain.Conclusion, started time.Time) domain.Run {
	return domain.Run{
		ID:           id,
		Name:         workflow,
		WorkflowName: workflow,
		Status:       st,
		Conclusion:   cc,
		RunStartedAt: started,
		Repo:         repoID(owner, name),
	}
}

// press builds a key press whose String() matches a registry binding's key, so a test
// drives the Feed through the same key.Matches path the runtime does (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// newFeed builds a Feed sized to width x height with the Standard profile.
func newFeed(width, height int) Model {
	m := New(Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// feedRuns sends one repository's Runs as a RunsFetched, the unit ADR-0015 fixes.
func feedRuns(m Model, id domain.RepoID, runs ...domain.Run) Model {
	m, _ = m.Update(scheduler.Update{Repo: id, Runs: runs})
	return m
}

func (m Model) displayedOrder() []int64 {
	return append([]int64(nil), m.displayedIDs...)
}

// TestColdStartRevealAppliesUntilEngaged pins R32, R33 and AC16: the local repository
// paints first, and other repositories appear as they arrive with no user interaction,
// because deferral begins only once the user engages the list.
func TestColdStartRevealAppliesUntilEngaged(t *testing.T) {
	m := newFeed(120, 20)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "CI", domain.StatusInProgress, "", t0))
	if got := len(m.displayedIDs); got != 1 {
		t.Fatalf("after local repo: displayed %d rows, want 1 (R32)", got)
	}
	// A second repository reveals in the background: it appears without interaction.
	m = feedRuns(m, repoID("acme", "web"), mkRun(2, "acme", "web", "Deploy", domain.StatusQueued, "", t0.Add(time.Minute)))
	if got := len(m.displayedIDs); got != 2 {
		t.Fatalf("background reveal: displayed %d rows, want 2 (R33, AC16)", got)
	}
	if m.pending.any() {
		t.Fatalf("cold-start reveal must not defer before engagement (AC16); pending=%+v", m.pending)
	}
}

// TestRepaintInPlace pins R9 and AC2: a poll transitioning a row's Status repaints that
// row in place, leaves every row index unchanged, and leaves the Conclusion empty below
// completed.
func TestRepaintInPlace(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id,
		mkRun(10, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(11, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-time.Hour)),
		mkRun(12, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-2*time.Hour)),
	)
	m = m.Update2(press("down")) // engage the list; cursor to row 1
	before := m.displayedOrder()

	// Row 12 (last) transitions queued -> in_progress. Same run_started_at, so no move.
	m = feedRuns(m, id,
		mkRun(10, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(11, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-time.Hour)),
		mkRun(12, "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-2*time.Hour)),
	)
	if got := m.displayedOrder(); !equalIDs(got, before) {
		t.Fatalf("row indices moved on a Status change: %v -> %v (AC2)", before, got)
	}
	if m.current[12].Status != domain.StatusInProgress {
		t.Fatalf("row 12 not repainted to in_progress (AC2)")
	}
	if conclusionText(m.current[12]) != "" {
		t.Fatalf("in_progress row rendered a Conclusion (R5, AC2)")
	}
	if m.pending.any() {
		t.Fatalf("a pure Status change must not defer (AC2); pending=%+v", m.pending)
	}
}

// TestDeferredInsertion pins R10, R11, R12 and AC3: three new Runs invoked elsewhere are
// deferred with the affordance counting them, no row index changes, and an explicit
// refresh applies them with the cursor kept on its Run ID.
func TestDeferredInsertion(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id,
		mkRun(100, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(101, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	m = m.Update2(press("down")) // engage; cursor on row 1 (id 101)
	cursorID := m.displayedIDs[m.cursor]

	// Three newer Runs arrive.
	m = feedRuns(m, id,
		mkRun(200, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(3*time.Hour)),
		mkRun(201, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(2*time.Hour)),
		mkRun(202, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(time.Hour)),
		mkRun(100, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(101, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)
	if m.pending.added != 3 || m.pending.removed != 0 || m.pending.moved != 0 {
		t.Fatalf("pending = %+v, want added:3 (R11, AC3)", m.pending)
	}
	if got := len(m.displayedIDs); got != 2 {
		t.Fatalf("deferred insertion changed the painted rows to %d, want 2 (AC3)", got)
	}
	if !strings.Contains(m.View(), "3 new runs") {
		t.Fatalf("affordance does not read %q (R11, AC3)", "3 new runs")
	}

	// Explicit refresh applies the deferred changes.
	m = m.Update2(press("r"))
	if got := len(m.displayedIDs); got != 5 {
		t.Fatalf("after refresh: %d rows, want 5 (AC3)", got)
	}
	if m.displayedIDs[0] != 200 || m.displayedIDs[1] != 201 || m.displayedIDs[2] != 202 {
		t.Fatalf("after refresh: new Runs not at top in run_started_at desc: %v (AC3)", m.displayedIDs)
	}
	if m.displayedIDs[m.cursor] != cursorID {
		t.Fatalf("cursor moved off its Run ID %d to %d (R12, AC3)", cursorID, m.displayedIDs[m.cursor])
	}
}

// TestReRunRepaintsNowMovesLater pins AC4: a re-run repaints its row in place at once
// and its reorder waits for idle or refresh.
func TestReRunRepaintsNowMovesLater(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id,
		mkRun(300, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(301, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Hour)),
		mkRun(302, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-2*time.Hour)),
	)
	m = m.Update2(press("down")) // engage
	before := m.displayedOrder()

	// Run 302 is re-run: status back to queued, conclusion cleared, attempt up, and its
	// run_started_at advances past every other row.
	m = feedRuns(m, id,
		mkRun(300, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(301, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0.Add(-time.Hour)),
		rerun(mkRun(302, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(time.Hour))),
	)
	if got := m.displayedOrder(); !equalIDs(got, before) {
		t.Fatalf("re-run moved a row immediately: %v -> %v (AC4)", before, got)
	}
	if m.current[302].Status != domain.StatusQueued || conclusionText(m.current[302]) != "" {
		t.Fatalf("re-run row not repainted in place (AC4)")
	}
	if m.pending.moved != 1 {
		t.Fatalf("re-run reorder not deferred: pending=%+v, want moved:1 (AC4)", m.pending)
	}
	m = m.Update2(press("r"))
	if m.displayedIDs[0] != 302 {
		t.Fatalf("after refresh: re-run not resurfaced to top: %v (AC4)", m.displayedIDs)
	}
}

// TestSelectionSurvivesRepaintAndFilter pins R13, R13a, AC5: selection is keyed by Run
// ID and survives repaint, deferred insertions and a filter change.
func TestSelectionSurvivesRepaintAndFilter(t *testing.T) {
	m := newFeed(120, 40)
	id := repoID("acme", "api")
	var runs []domain.Run
	for i := 0; i < 5; i++ {
		runs = append(runs, mkRun(int64(400+i), "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Duration(i)*time.Hour)))
	}
	m = feedRuns(m, id, runs...)
	// Select rows 0, 1, 2.
	m = m.Update2(press("space"))
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	if m.selectedCount() != 3 {
		t.Fatalf("selected %d, want 3", m.selectedCount())
	}
	want := map[int64]bool{400: true, 401: true, 402: true}
	// Repaint all five with new Status, defer an insertion.
	var repainted []domain.Run
	for i := 0; i < 5; i++ {
		repainted = append(repainted, mkRun(int64(400+i), "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-time.Duration(i)*time.Hour)))
	}
	repainted = append(repainted, mkRun(999, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(time.Hour)))
	m = feedRuns(m, id, repainted...)
	if !sameKeys(m.selected, want) {
		t.Fatalf("selection changed after repaint: %v, want %v (AC5)", m.selected, want)
	}
	// Change the filter: selection must survive (R13a).
	m.filter = filter.Filter{Statuses: []domain.Status{domain.StatusQueued}}
	m.applyView(m.liveView())
	if !sameKeys(m.selected, want) {
		t.Fatalf("selection cleared on filter change: %v, want %v (R13a)", m.selected, want)
	}
}

// TestFrozenSelectionOrdersCrossFilterByStart pins purge R30 and AC22 for a cross-filter
// (R13a) selection: the frozen set carries the Feed's order, EffectiveStart descending, so
// its last row is the oldest Run in the set by run_started_at, even when the selection spans
// Runs the active filter has hidden. Before the sort those off-filter Runs were appended in
// Go map-iteration order, which is randomised, so the tail and the last row were
// non-deterministic. Two off-filter Runs bracket an in-filter one, so a correct order can
// come only from sorting the whole set, not from appending an off-filter tail.
func TestFrozenSelectionOrdersCrossFilterByStart(t *testing.T) {
	m := newFeed(120, 40)
	id := repoID("acme", "api")
	// Five Runs, newest to oldest. The Completed ones survive the filter below; the
	// InProgress ones become off-filter but stay selected (R13a).
	m = feedRuns(m, id,
		mkRun(10, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(11, "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-1*time.Hour)),
		mkRun(12, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-2*time.Hour)),
		mkRun(13, "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-3*time.Hour)),
		mkRun(14, "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-4*time.Hour)),
	)
	// Select all five while they are all displayed.
	for i := 0; i < 5; i++ {
		m = m.Update2(press("space"))
		if i < 4 {
			m = m.Update2(press("down"))
		}
	}
	if m.selectedCount() != 5 {
		t.Fatalf("selected %d, want 5", m.selectedCount())
	}
	// Filter to Completed: Runs 11, 13 and 14 are now off-filter but still selected.
	m.filter = filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}}
	m.applyView(m.liveView())
	if got := len(m.displayedIDs); got != 2 {
		t.Fatalf("filter left %d displayed, want 2 (10, 12) (R13a)", got)
	}

	want := []int64{10, 11, 12, 13, 14} // EffectiveStart descending, the Feed's order (R8)
	// Map iteration is randomised per range, so a missing sort surfaces as a wrong or
	// unstable order. Repeat so a regression fails loudly rather than flakily.
	for iter := 0; iter < 32; iter++ {
		items := m.frozenSelection()
		got := make([]int64, len(items))
		for i, it := range items {
			got[i] = it.ID
		}
		if !equalIDs(got, want) {
			t.Fatalf("frozenSelection order = %v, want %v (R30, AC22: last row is the oldest by run_started_at)", got, want)
		}
	}
	// AC22 stated directly: no Run in the set starts before the last row.
	items := m.frozenSelection()
	last := items[len(items)-1].Run.EffectiveStart()
	for _, it := range items {
		if it.Run.EffectiveStart().Before(last) {
			t.Fatalf("Run %d starts before the last row; the last row is not the oldest in the set (AC22)", it.ID)
		}
	}
}

// TestFilterIsClientSide pins R23: the Conclusion filter is a client-side predicate over
// held Runs, and a permissive Status input matches either field.
func TestFilterIsClientSide(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
		mkRun(3, "acme", "api", "CI", domain.StatusInProgress, "", t0.Add(-2*time.Hour)),
	)
	f, err := parseFilterQuery("failure")
	if err != nil {
		t.Fatalf("parseFilterQuery: %v", err)
	}
	m.filter = f
	m.applyView(m.liveView())
	if got := m.displayedOrder(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("conclusion filter failure matched %v, want [1] (R23)", got)
	}
}

// TestSortByEffectiveStart pins R8: newest run_started_at first, with the created_at
// fallback where run_started_at is null.
func TestSortByEffectiveStart(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	// Run 3 has a null run_started_at but the newest created_at, so EffectiveStart puts
	// it first (the requested-Status fallback, R8).
	fallback := mkRun(3, "acme", "api", "CI", domain.StatusRequested, "", time.Time{})
	fallback.CreatedAt = t0.Add(time.Hour)
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
		fallback,
	)
	if got := m.displayedOrder(); len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("sort order %v, want [3 1 2] (R8)", got)
	}
}

// TestNarrowTerminalRefused pins R4a and AC20: below 100 columns the Feed paints no rows
// and states the width it needs.
func TestNarrowTerminalRefused(t *testing.T) {
	m := newFeed(99, 20)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "CI", domain.StatusInProgress, "", t0))
	view := m.View()
	if strings.Contains(view, "CI") || strings.Contains(view, "acme/api") {
		t.Fatalf("narrow terminal painted rows (AC20):\n%s", view)
	}
	if !strings.Contains(view, "100") {
		t.Fatalf("narrow message does not name the width it needs (AC20):\n%s", view)
	}
}

// TestMotionUsesActiveProfile pins that the Feed matches motion from the registry, and
// that the two profiles' motion keys both reach it (R7a, AC18's premise applied at the
// consumer).
func TestMotionUsesActiveProfile(t *testing.T) {
	std := New(Options{Profile: keys.Standard})
	std, _ = std.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	std = feedRuns(std, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusQueued, "", t0),
		mkRun(2, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-time.Hour)),
	)
	std = std.Update2(press("down"))
	if std.cursor != 1 {
		t.Fatalf("Standard down did not move cursor: %d", std.cursor)
	}

	vim := New(Options{Profile: keys.Vim})
	vim, _ = vim.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	vim = feedRuns(vim, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusQueued, "", t0),
		mkRun(2, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-time.Hour)),
	)
	vim = vim.Update2(press("j"))
	if vim.cursor != 1 {
		t.Fatalf("Vim j did not move cursor: %d", vim.cursor)
	}
	// Standard's arrow must not be Vim's motion and vice versa: 'j' is inert in Standard.
	std2 := feedRuns(newFeed(120, 20), repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusQueued, "", t0),
		mkRun(2, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(-time.Hour)),
	)
	if std2.Update2(press("j")).cursor != 0 {
		t.Fatalf("Standard profile moved on Vim's 'j' (R7a)")
	}
}

// TestSetActiveAppliesDeferred pins R10's idle rule: losing focus applies the deferred
// changes (resolved open question 5).
func TestSetActiveAppliesDeferred(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id, mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))
	m = m.Update2(press("down")) // engage
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusQueued, "", t0.Add(time.Hour)),
	)
	if m.pending.added != 1 {
		t.Fatalf("expected a deferred insertion, got %+v", m.pending)
	}
	m = m.SetActive(false) // focus leaves the list: idle (R10, OQ5)
	if m.pending.any() {
		t.Fatalf("SetActive(false) did not apply deferred changes (R10)")
	}
	if len(m.displayedIDs) != 2 {
		t.Fatalf("idle apply painted %d rows, want 2", len(m.displayedIDs))
	}
}

// TestBudgetBanner pins R29, R30 and AC14: no readout while nominal, a pressure line
// under pressure, and a pause line on exhaustion that states when updates resume and what
// the Feed is showing.
func TestBudgetBanner(t *testing.T) {
	m := newFeed(120, 20)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "CI", domain.StatusInProgress, "", t0))

	if _, ok := m.bannerLine(); ok {
		t.Fatalf("nominal consumption rendered a Budget readout (R29, AC14)")
	}

	reset := time.Date(2026, 7, 19, 14, 32, 0, 0, time.UTC)
	m, _ = m.Update(governor.Readout{Pressure: true, Remaining: 1234, Reset: reset})
	line, ok := m.bannerLine()
	if !ok || !strings.Contains(line, "pressure") {
		t.Fatalf("pressure did not render a pressure readout (R29): %q", line)
	}

	m, _ = m.Update(governor.Readout{Exhausted: true, Reset: reset})
	line, ok = m.bannerLine()
	if !ok || !strings.Contains(line, "paused") || !strings.Contains(line, "14:32") {
		t.Fatalf("exhaustion did not render the pause banner with a resume time (R30): %q", line)
	}
}

// TestActionStyleDistinct pins AC19's "each visibly different": the four action states
// the gate reports render differently, independent of the cursor (R17, R18, R21).
func TestActionStyleDistinct(t *testing.T) {
	m := New(Options{Profile: keys.Standard})
	offered := repoID("acme", "offered")
	readonly := repoID("acme", "readonly")
	archived := repoID("acme", "archived")
	unknown := repoID("acme", "unknown") // deliberately not discovered: not-yet-known
	m, _ = m.Update(ReposDiscovered{
		{ID: offered, Permissions: domain.Permissions{Push: true}},
		{ID: readonly, Permissions: domain.Permissions{Push: false}},
		{ID: archived, Permissions: domain.Permissions{Push: true}, Archived: true},
	})
	rendered := map[string]string{
		"offered":  m.actionStyle(offered).Render("acme/x"),
		"readonly": m.actionStyle(readonly).Render("acme/x"),
		"archived": m.actionStyle(archived).Render("acme/x"),
		"unknown":  m.actionStyle(unknown).Render("acme/x"),
	}
	seen := map[string]string{}
	for name, out := range rendered {
		if other, dup := seen[out]; dup {
			t.Fatalf("action states %q and %q render identically; AC19 needs each visibly different", name, other)
		}
		seen[out] = name
	}
}

// TestFilterAcceptAndCancelUseRegistry pins the blocker-2 fix at the consumer: the filter
// input's accept (enter) and cancel (esc) are matched from the profile's registry with
// key.Matches, not a key literal of its own, so accepting narrows the view and cancelling
// restores it (R7a, R23, AC18).
func TestFilterAcceptAndCancelUseRegistry(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	m = feedRuns(m, id,
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionFailure, t0),
		mkRun(2, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0.Add(-time.Hour)),
	)

	m = m.Update2(press("/"))
	if !m.filterActive {
		t.Fatal("pressing / did not open the filter input")
	}
	for _, r := range "failure" {
		m = m.Update2(press(string(r)))
	}
	m = m.Update2(press("enter")) // FilterAccept
	if m.filterActive {
		t.Fatal("FilterAccept (enter) did not close the filter input")
	}
	if got := m.displayedOrder(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("accepted filter did not narrow to the failing Run: %v (R23)", got)
	}

	m = m.Update2(press("/"))
	m = m.Update2(press("esc")) // FilterCancel
	if m.filterActive {
		t.Fatal("FilterCancel (esc) did not close the filter input")
	}
	if got := m.displayedOrder(); len(got) != 2 {
		t.Fatalf("cancel did not restore the unfiltered view: %v (R23)", got)
	}
}

// TestViewSanitisesRunDerivedText pins the security fix: a hostile workflow or run name
// carrying an ESC CSI clear-screen and a carriage return is stripped before it is painted,
// so those bytes cannot survive into View() to move the cursor, erase the screen or forge
// the pause and selection chrome the operator reads (security review). lipgloss wraps cells
// in its own SGR colour escapes, so the assertion targets the hostile control sequences by
// shape rather than the mere presence of any ESC byte.
func TestViewSanitisesRunDerivedText(t *testing.T) {
	m := newFeed(120, 20)
	id := repoID("acme", "api")
	hostile := "CI\x1b[2J\x1b[Hpwned\rowned"
	m = feedRuns(m, id, mkRun(1, "acme", "api", hostile, domain.StatusInProgress, "", t0))
	view := m.View()

	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[H") {
		t.Fatalf("a hostile CSI sequence survived into the Feed's View:\n%q", view)
	}
	if strings.ContainsRune(view, '\r') {
		t.Fatalf("a carriage return survived into the Feed's View:\n%q", view)
	}
	// The visible text keeps its characters, contiguous, once the controls are dropped.
	if !strings.Contains(view, "CIpwnedowned") {
		t.Fatalf("the sanitiser dropped or split visible workflow text:\n%q", view)
	}
}

// feedWithDetail builds a Feed whose detail pane has a no-op fetch and a fixed clock, so a
// wiring test can open the pane without a live transport (ADR-0015). The pane stays in its
// pending state, because the debounce Cmd the open returns is not run: these tests exercise
// the Feed-to-pane wiring, and the pane's own fetch state machine is tested in rundetail.
func feedWithDetail(width, height int) Model {
	m := New(Options{
		Profile:     keys.Standard,
		DetailFetch: func(domain.RepoID, int64) ([]domain.Job, error) { return nil, nil },
		Clock:       clockwork.NewFakeClockAt(t0),
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// TestDetailPaneOpensAndCloses pins the Feed-to-pane wiring: the OpenDetail key opens the
// pane over the Run under the cursor and the CloseDetail key closes it (ADR-0011, stage 8).
func TestDetailPaneOpensAndCloses(t *testing.T) {
	m := feedWithDetail(120, 40)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "Build", domain.StatusInProgress, "", t0))
	if m.detailOpen {
		t.Fatal("the detail pane was open before any key")
	}

	m = m.Update2(press("enter")) // OpenDetail
	if !m.detailOpen || !m.detail.IsOpen() {
		t.Fatalf("enter did not open the detail pane (stage 8)")
	}
	if !strings.Contains(m.View(), "Loading") {
		t.Fatalf("the freshly opened pane did not paint its pending state below the list (R12):\n%s", m.View())
	}

	m = m.Update2(press("esc")) // CloseDetail
	if m.detailOpen || m.detail.IsOpen() {
		t.Fatalf("esc did not close the detail pane")
	}
	if strings.Contains(m.View(), "Loading") {
		t.Fatalf("the closed pane was still painted below the list")
	}
}

// TestDetailPaneFollowsCursor pins the "feed reports where its cursor is" half of the split
// (ADR-0011): with the pane open, moving the cursor onto another Run re-points the pane at
// it, which is the motion R10 debounces and AC1 counts one fetch for. The Run numbers are
// distinct and appear only in the pane's identity, so the assertion reads the pane, not the
// list.
func TestDetailPaneFollowsCursor(t *testing.T) {
	m := feedWithDetail(120, 40)
	id := repoID("acme", "api")
	alpha := mkRun(1, "acme", "api", "Alpha", domain.StatusInProgress, "", t0)
	alpha.RunNumber = 11
	bravo := mkRun(2, "acme", "api", "Bravo", domain.StatusInProgress, "", t0.Add(-time.Hour))
	bravo.RunNumber = 22
	m = feedRuns(m, id, alpha, bravo)

	m = m.Update2(press("enter")) // open over the top row, Alpha
	if !strings.Contains(m.View(), "Run #11") {
		t.Fatalf("the pane did not open over the cursor's Run (Alpha):\n%s", m.View())
	}
	m = m.Update2(press("down")) // cursor moves to Bravo; the pane follows
	if !strings.Contains(m.View(), "Run #22") {
		t.Fatalf("the pane did not follow the cursor to Bravo (R10, AC1):\n%s", m.View())
	}
	if strings.Contains(m.View(), "Run #11") {
		t.Fatalf("the pane still shows the previous Run after the cursor moved (R11, R12)")
	}
}

// TestDetailPaneReceivesBudgetBroadcast pins that a broadcast reaches the open pane through
// the Feed's forwarding (ADR-0015): the Budget Readout pauses the pane on the same Budget as
// the Feed (R16). The pane's own View is read, so the Feed's own banner cannot mask it.
func TestDetailPaneReceivesBudgetBroadcast(t *testing.T) {
	m := feedWithDetail(120, 40)
	m = feedRuns(m, repoID("acme", "api"), mkRun(1, "acme", "api", "Build", domain.StatusInProgress, "", t0))
	m = m.Update2(press("enter"))

	reset := time.Date(2026, 7, 15, 17, 9, 0, 0, time.UTC)
	m, _ = m.Update(governor.Readout{Exhausted: true, Reset: reset})
	if !strings.Contains(m.detail.View(), "paused") {
		t.Fatalf("the Budget broadcast did not reach the open pane (R16, ADR-0015):\n%s", m.detail.View())
	}
}

// Update2 is a test convenience that drops the Cmd, since these assertions read state
// rather than run commands.
func (m Model) Update2(msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next
}

// rerun bumps a Run's attempt to model a re-run (AC4).
func rerun(r domain.Run) domain.Run {
	r.RunAttempt = 2
	return r
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameKeys(got map[int64]bool, want map[int64]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}
