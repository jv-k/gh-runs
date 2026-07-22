package confirm_test

import (
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
)

func repoID(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

func writable(owner, name string) domain.Repo {
	return domain.Repo{ID: repoID(owner, name), Permissions: domain.Permissions{Push: true}}
}

func run(id int64, owner, name string) domain.Run {
	return domain.Run{ID: id, Repo: repoID(owner, name), Status: domain.StatusCompleted}
}

// planOps builds an Ops for planning alone, no transport.
func planOps(threshold int) *ops.Ops {
	return ops.New(ops.Options{ConfirmThreshold: threshold, BreakerFailures: 50})
}

// ynPlan is a single-repository set below the threshold, priced FrictionYN.
func ynPlan(t *testing.T) ops.Plan {
	t.Helper()
	items := []ops.Item{ops.RunItem(run(1, "o", "a")), ops.RunItem(run(2, "o", "a")), ops.RunItem(run(3, "o", "a"))}
	p, err := planOps(50).Plan(ops.OpDelete, items, map[domain.RepoID]domain.Repo{repoID("o", "a"): writable("o", "a")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// typedPlan is a single-repository set at the threshold, priced FrictionTypedCount,
// Total 50.
func typedPlan(t *testing.T) ops.Plan {
	t.Helper()
	items := make([]ops.Item, 50)
	for i := range items {
		items[i] = ops.RunItem(run(int64(i+1), "o", "a"))
	}
	p, err := planOps(50).Plan(ops.OpDelete, items, map[domain.RepoID]domain.Repo{repoID("o", "a"): writable("o", "a")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Friction() != ops.FrictionTypedCount {
		t.Fatalf("typedPlan priced %d, want TypedCount", p.Friction())
	}
	return p
}

// press builds a KeyPressMsg from a key name, mirroring the Feed's own test helper.
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
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// send drives one key through Update and returns the model.
func send(m confirm.Model, key string) confirm.Model {
	m, _ = m.Update(press(key))
	return m
}

// TestYNConfirmsOnY pins AC6: y confirms a below-threshold single-repository set, and
// the Input the pane hands back is accepted by ops.Confirm, the authority.
func TestYNConfirmsOnY(t *testing.T) {
	p := ynPlan(t)
	m := confirm.New(keys.Standard).Open(p)
	m = send(m, "y")
	if m.Outcome() != confirm.Confirmed {
		t.Fatalf("y did not confirm a y/N set (AC6)")
	}
	if _, err := planOps(50).Confirm(p, m.Input()); err != nil {
		t.Errorf("the pane's Input was rejected by ops.Confirm: %v; the pane and the authority must agree", err)
	}
}

// TestYNAborts pins AC6: n, esc and Enter-on-the-default all abort a y/N set, issuing
// nothing.
func TestYNAborts(t *testing.T) {
	for _, key := range []string{"n", "esc", "enter"} {
		m := confirm.New(keys.Standard).Open(ynPlan(t))
		m = send(m, key)
		if m.Outcome() != confirm.Aborted {
			t.Errorf("%q did not abort a y/N set (AC6)", key)
		}
	}
}

// TestTypedCountRejectsY pins R7a and AC7: y must not start a typed-count set. Only
// the exact count, typed and submitted, confirms it.
func TestTypedCountRejectsY(t *testing.T) {
	p := typedPlan(t)
	m := confirm.New(keys.Standard).Open(p)
	m = send(m, "y")
	if m.Outcome() != confirm.Pending {
		t.Fatalf("y started a typed-count set; only the exact count may (R7a, AC7)")
	}
	for _, d := range strconv.Itoa(p.Total()) {
		m = send(m, string(d))
	}
	if m.Outcome() != confirm.Pending {
		t.Fatalf("the typed count confirmed before Enter; it submits on Enter")
	}
	m = send(m, "enter")
	if m.Outcome() != confirm.Confirmed {
		t.Fatalf("the exact typed count did not confirm on Enter (R7, AC7)")
	}
	if _, err := planOps(50).Confirm(p, m.Input()); err != nil {
		t.Errorf("the pane's typed Input was rejected by ops.Confirm: %v", err)
	}
}

// TestTypedCountWrongNumberDoesNotConfirm pins AC7: a wrong number leaves the set
// unconfirmed and no request possible.
func TestTypedCountWrongNumberDoesNotConfirm(t *testing.T) {
	m := confirm.New(keys.Standard).Open(typedPlan(t))
	for _, d := range "49" { // the set is 50
		m = send(m, string(d))
	}
	m = send(m, "enter")
	if m.Outcome() != confirm.Pending {
		t.Errorf("a wrong count confirmed the Purge; only the exact count may (AC7)")
	}
}

// TestTypedCountBackspace pins that the buffer is editable: an over-typed count can be
// corrected to the exact one and then confirms.
func TestTypedCountBackspace(t *testing.T) {
	m := confirm.New(keys.Standard).Open(typedPlan(t))
	for _, d := range "509" { // one digit too many
		m = send(m, string(d))
	}
	m = send(m, "backspace") // back to "50"
	m = send(m, "enter")
	if m.Outcome() != confirm.Confirmed {
		t.Errorf("a corrected count did not confirm (R7)")
	}
}

// TestInspectOpensAndReturnsWithoutConfirming pins R30 and AC22: v opens the inspect
// viewport, and v or esc returns to the modal with the confirmation state unchanged.
// Nothing in the view confirms or aborts.
func TestInspectOpensAndReturnsWithoutConfirming(t *testing.T) {
	m := confirm.New(keys.Standard).Open(typedPlan(t))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = send(m, "v")
	if !m.Inspecting() {
		t.Fatalf("v did not open the inspect view (R30)")
	}
	if m.Outcome() != confirm.Pending {
		t.Errorf("opening inspect changed the confirmation state (R30, AC22)")
	}
	m = send(m, "esc") // esc returns to the modal, it does not abort
	if m.Inspecting() {
		t.Errorf("esc did not return from the inspect view to the modal (R30)")
	}
	if m.Outcome() != confirm.Pending {
		t.Errorf("returning from inspect changed the confirmation state; the Purge still needs its typed count (R30, AC22)")
	}
}

// TestInspectReachesBothEnds pins R30's "reach both ends", how an operator checks a
// date boundary: end jumps to the last row, home back to the first.
func TestInspectReachesBothEnds(t *testing.T) {
	m := confirm.New(keys.Standard).Open(typedPlan(t))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = send(m, "v")
	m = send(m, "end")
	// The last row must be visible: the view renders the last Item. A golden pins the
	// exact frame; here we assert the motion did not panic and the view is non-empty.
	if got := m.View(); got == "" {
		t.Fatalf("inspect view empty after reaching the last row (R30)")
	}
	m = send(m, "home")
	if m.View() == "" {
		t.Fatalf("inspect view empty after returning to the first row (R30)")
	}
}

// TestClosedPaneRendersNothing pins that a closed pane paints an empty frame, so the
// opener never shows a stale modal.
func TestClosedPaneRendersNothing(t *testing.T) {
	m := confirm.New(keys.Standard)
	if m.View() != "" {
		t.Errorf("a closed pane rendered %q, want empty", m.View())
	}
}
