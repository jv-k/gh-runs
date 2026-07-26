package feed

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// runLeafCmds executes cmd and every command a tea.Batch fans out to, so a wiring test
// can observe the side effects of the Cmds Update returns (here, the SetFilter publish
// that hands the active filter to the scheduler).
func runLeafCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runLeafCmds(c)
		}
	}
}

// feedWithFilterSink builds a Feed whose SetFilter publishes into sink, so a wiring test
// can assert the active filter reaches the scheduler (R22).
func feedWithFilterSink(width, height int, sink func(filter.Filter)) Model {
	m := New(Options{Profile: keys.Standard, SetFilter: sink})
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// TestAcceptingAFilterPublishesItToTheScheduler pins R22's Feed half: accepting a filter
// publishes it to the scheduler, which pushes it server-side. Narrowing client-side over
// held Runs is not enough, because a status:failure search would then see only the one-page
// held window rather than the newest matches (the defect issue #53 fixes).
func TestAcceptingAFilterPublishesItToTheScheduler(t *testing.T) {
	var got []filter.Filter
	m := feedWithFilterSink(100, 10, func(f filter.Filter) { got = append(got, f) })
	m = feedRuns(m, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	m = m.Update2(press("/"))
	for _, r := range "branch:main" {
		m = m.Update2(press(string(r)))
	}
	_, cmd := m.Update(press("enter")) // FilterAccept
	runLeafCmds(cmd)

	if len(got) == 0 {
		t.Fatal("accepting a filter published nothing to the scheduler (R22)")
	}
	if last := got[len(got)-1]; last.Branch != "main" {
		t.Fatalf("published filter = %+v, want Branch=main (R22)", last)
	}
}

// TestARepositoryOnlyFilterIsReportedActive pins the trap the repository axis used to be
// (#102). The Feed decides a filter is active by asking whether QueryString is non-empty,
// so while the axis had no token a filter carrying only repositories narrowed the rows
// while the chrome reported no filter at all: the operator saw an emptied list and nothing
// saying why. Typing the axis is the case that reaches this, now that the grammar has it.
func TestARepositoryOnlyFilterIsReportedActive(t *testing.T) {
	m := newFeed(100, 24)
	m = feedRuns(m, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	m = m.Update2(press("/"))
	for _, r := range "repo:acme/other" {
		m = m.Update2(press(string(r)))
	}
	m = m.Update2(press("enter"))

	if !m.narrowed() {
		t.Error("a filter carrying only the repository axis is not reported as narrowing the list")
	}
	line, ok := m.filterLine()
	if !ok {
		t.Fatal("a repository-only filter states no filter line, so the narrowing is silent")
	}
	if !strings.Contains(line, "acme/other") {
		t.Errorf("the filter line does not name the repository it narrowed to: %q", line)
	}
}

// TestCancellingAFilterPublishesTheUnfilteredListing pins the restore: cancelling the
// filter input publishes a filter with an empty server-side Query, so the scheduler drops
// back to the unfiltered listing rather than staying narrowed to an abandoned filter.
func TestCancellingAFilterPublishesTheUnfilteredListing(t *testing.T) {
	var got []filter.Filter
	m := feedWithFilterSink(100, 10, func(f filter.Filter) { got = append(got, f) })
	m = feedRuns(m, repoID("acme", "api"),
		mkRun(1, "acme", "api", "CI", domain.StatusCompleted, domain.ConclusionSuccess, t0))

	m = m.Update2(press("/"))
	for _, r := range "branch:main" {
		m = m.Update2(press(string(r)))
	}
	m = m.Update2(press("enter")) // accept branch:main
	m = m.Update2(press("/"))
	_, cmd := m.Update(press("esc")) // FilterCancel
	runLeafCmds(cmd)

	if len(got) == 0 {
		t.Fatal("cancelling a filter published nothing (R22)")
	}
	if last := got[len(got)-1]; len(last.Query()) != 0 {
		t.Fatalf("cancel published a filter with a server-side query %v, want the unfiltered listing", last.Query())
	}
}
