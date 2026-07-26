package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/tui/feed"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// TestNavigateToRunsSwitchesToTheFeedAndHandsItTheFilter pins workflow-management R13 and
// AC4's routing half, over recording tabs so the rule is asserted without any real tab: the
// Workflows tab's request moves focus to the Feed and delivers the filter there. The tab
// itself does neither, because a tab may import a pane but never another tab (ADR-0011), so
// the cross-tab move is the root's alone.
func TestNavigateToRunsSwitchesToTheFeedAndHandsItTheFilter(t *testing.T) {
	t0, t1, t2 := &recordingTab{title: "Runs"}, &recordingTab{title: "Workflows"}, &recordingTab{title: "Storage"}
	m := rootWithTabs(t0, t1, t2)
	m = step(t, m, press("tab")) // focus Workflows, where the request comes from
	if m.active != 1 {
		t.Fatalf("setup: active = %d, want the Workflows tab", m.active)
	}

	m = step(t, m, workflows.NavigateToRuns{Filter: filter.Filter{Workflow: "9004"}})

	if m.active != 0 {
		t.Errorf("active tab = %d after the navigation, want the Feed at 0 (R13)", m.active)
	}
	if !t0.active || t1.active {
		t.Errorf("focus flags = Runs %v, Workflows %v; want the Feed focused and the Workflows tab not", t0.active, t1.active)
	}
	var delivered []feed.ShowRuns
	for _, msg := range t0.msgs {
		if s, ok := msg.(feed.ShowRuns); ok {
			delivered = append(delivered, s)
		}
	}
	if len(delivered) != 1 || filter.Filter(delivered[0]).Workflow != "9004" {
		t.Fatalf("the Feed received %v, want exactly one ShowRuns filtered to Workflow 9004", delivered)
	}
}

// TestNavigateToRunsFiltersTheRealFeed pins AC4 end to end over the built tabs: pressing the
// navigation key on a deleted Workflow's row leaves the Feed focused and showing that
// Workflow's Runs alone. Those Runs are the Orphaned Runs a deleted Workflow leaves behind
// (R11), and reaching them issues no request against the repository's contents (AC4, R12).
func TestNavigateToRunsFiltersTheRealFeed(t *testing.T) {
	repo := domain.Repo{ID: repoID("cli", "cli"), Permissions: domain.Permissions{Push: true}}
	m := New(Options{
		Profile: keys.Standard,
		Repos:   func() []domain.Repo { return []domain.Repo{repo} },
	})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = step(t, m, scheduler.Update{Repo: repo.ID, Runs: []domain.Run{
		run(1, repo.ID, "Nightly Build", 9001),
		run(2, repo.ID, "Old Deploy", 9004),
	}})
	m = step(t, m, workflows.WorkflowsFetched(workflows.RepoWorkflows{
		Repo: repo.ID, Complete: true, Workflows: []domain.Workflow{
			{ID: 9001, Name: "Nightly Build", Path: ".github/workflows/nightly.yml", State: domain.StateActive, Repo: repo.ID},
			{ID: 9004, Name: "Old Deploy", Path: ".github/workflows/deploy.yml", State: domain.StateDeleted, Repo: repo.ID},
		},
	}))

	m = step(t, m, press("tab"))  // focus the Workflows tab
	m = step(t, m, press("r"))    // populate its gate from the discovered repositories
	m = step(t, m, press("down")) // move to the deleted Workflow's row
	m = drainInto(t, m, press("enter"))

	if m.active != 0 {
		t.Fatalf("active tab = %d, want the Feed at 0 (AC4)", m.active)
	}
	content := m.View().Content
	if !strings.Contains(content, "Old Deploy") {
		t.Errorf("the Feed does not show the deleted Workflow's Runs (AC4):\n%s", content)
	}
	if strings.Contains(content, "Nightly Build") {
		t.Errorf("the Feed still shows another Workflow's Runs, so it is not filtered to the deleted one (R13):\n%s", content)
	}
}

// run builds a Run stamped with the Workflow id its Workflow carries, which is what a
// numeric selector matches on (ADR-0016) and what survives the Workflow's deletion.
func run(id int64, repo domain.RepoID, workflow string, workflowID int64) domain.Run {
	return domain.Run{
		ID:           id,
		Name:         workflow,
		WorkflowName: workflow,
		WorkflowID:   workflowID,
		Status:       domain.StatusCompleted,
		Conclusion:   domain.ConclusionSuccess,
		Repo:         repo,
	}
}

func repoID(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// drainInto routes a message and applies every message the Cmds it returns produce, so a
// chain that crosses tabs (the tab's request, then the root's delivery) completes inside one
// call, as it does under a running program.
func drainInto(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(Model)
	return drainCmd(t, m, cmd, 0)
}

func drainCmd(t *testing.T, m Model, cmd tea.Cmd, depth int) Model {
	t.Helper()
	if cmd == nil || depth > 8 {
		return m
	}
	out := cmd()
	if out == nil {
		return m
	}
	if batch, ok := out.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = drainCmd(t, m, c, depth+1)
		}
		return m
	}
	next, nextCmd := m.Update(out)
	return drainCmd(t, next.(Model), nextCmd, depth+1)
}
