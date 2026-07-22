package workflows

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

func iid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// modelWith builds a tab whose gate is populated from repos and whose held list is the given
// fetches, using the in-package seams so no keystroke or Cmd is involved.
func modelWith(repos []domain.Repo, rws ...RepoWorkflows) Model {
	m := New(Options{Profile: keys.Standard, Repos: func() []domain.Repo { return repos }})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.startFetch() // populate capability from repos; Fetch is nil, so no fan-out
	for _, rw := range rws {
		m.applyFetched(rw)
	}
	return m
}

// TestActionForCoversTheStateMatrix pins the action offered for each state under a writable
// repository (R5, R9, R11): active offers disable, every disabled state offers enable
// (disabled_fork best-effort per the resolved open question), deleted offers neither and
// labels its Runs Orphaned, and an unrecognised state offers nothing while the STATE column
// still renders it verbatim (R3).
func TestActionForCoversTheStateMatrix(t *testing.T) {
	repo := domain.Repo{ID: iid("o", "r"), Permissions: domain.Permissions{Push: true}}
	m := modelWith([]domain.Repo{repo})

	cases := []struct {
		state       domain.State
		wantOffered bool
		wantOp      ops.Operation
		wantLabel   string
	}{
		{domain.StateActive, true, ops.OpDisable, "Disable"},
		{domain.StateDisabledManually, true, ops.OpEnable, "Enable"},
		{domain.StateDisabledInactivity, true, ops.OpEnable, "Enable"},
		{domain.StateDisabledFork, true, ops.OpEnable, "Enable"},
		{domain.StateDeleted, false, "", "orphaned Runs"},
		{domain.State("frozen_by_the_future"), false, "", "-"},
	}
	for _, c := range cases {
		row := wfRow{repo: repo.ID, wf: domain.Workflow{State: c.state, Repo: repo.ID}}
		act := m.actionFor(row)
		if act.offered != c.wantOffered || act.label != c.wantLabel {
			t.Errorf("actionFor(%q) = {offered %v, label %q}, want {%v, %q}", c.state, act.offered, act.label, c.wantOffered, c.wantLabel)
		}
		if c.wantOffered && act.op != c.wantOp {
			t.Errorf("actionFor(%q).op = %q, want %q (R5)", c.state, act.op, c.wantOp)
		}
	}
}

// TestActionForGateReasons pins R6: an archived repository is called permanently unreclaimable
// and a read-only one is merely read-only, distinct reasons for the same absence of a toggle,
// and a repository whose capability is not yet known fails closed rather than guessing.
func TestActionForGateReasons(t *testing.T) {
	archived := domain.Repo{ID: iid("o", "arch"), Permissions: domain.Permissions{Push: true}, Archived: true}
	readonly := domain.Repo{ID: iid("o", "ro"), Permissions: domain.Permissions{Push: false}}
	m := modelWith([]domain.Repo{archived, readonly})

	arch := m.actionFor(wfRow{repo: archived.ID, wf: domain.Workflow{State: domain.StateActive, Repo: archived.ID}})
	if arch.offered || arch.label != "archived" {
		t.Errorf("an archived repo offered %v/%q, want no toggle and \"archived\" (R6)", arch.offered, arch.label)
	}
	ro := m.actionFor(wfRow{repo: readonly.ID, wf: domain.Workflow{State: domain.StateDisabledManually, Repo: readonly.ID}})
	if ro.offered || ro.label != "read-only" {
		t.Errorf("a read-only repo offered %v/%q, want no toggle and \"read-only\" (R6)", ro.offered, ro.label)
	}
	// A repository whose capability the gate has not recorded fails closed.
	unknown := m.actionFor(wfRow{repo: iid("o", "mystery"), wf: domain.Workflow{State: domain.StateActive}})
	if unknown.offered {
		t.Errorf("a repository with unknown capability offered a toggle; it must fail closed (repo-discovery R8)")
	}
}

// TestDisplayRowsGroupedAndDeterministic pins that the list is grouped by repository and
// sorted within each by name, so the order is stable across refreshes and the goldens are
// byte-stable (the sort files octo/hello's rows after cli/cli's, and within cli/cli files
// Build before Release by name).
func TestDisplayRowsGroupedAndDeterministic(t *testing.T) {
	cli := domain.Repo{ID: iid("cli", "cli"), Permissions: domain.Permissions{Push: true}}
	octo := domain.Repo{ID: iid("octo", "hello"), Permissions: domain.Permissions{Push: true}}
	m := modelWith(
		[]domain.Repo{cli, octo},
		RepoWorkflows{Repo: octo.ID, Complete: true, Workflows: []domain.Workflow{
			{ID: 3, Name: "Deploy", State: domain.StateActive, Repo: octo.ID},
		}},
		RepoWorkflows{Repo: cli.ID, Complete: true, Workflows: []domain.Workflow{
			{ID: 2, Name: "Release", State: domain.StateActive, Repo: cli.ID},
			{ID: 1, Name: "Build", State: domain.StateActive, Repo: cli.ID},
		}},
	)
	rows := m.displayRows()
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.repo.Owner + "/" + r.wf.Name
	}
	want := []string{"cli/Build", "cli/Release", "octo/Deploy"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("displayRows order = %v, want %v (grouped by repo, then by name)", got, want)
		}
	}
}
