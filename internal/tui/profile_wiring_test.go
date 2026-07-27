package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
)

// These pin settings R5 and R17 for the keybinding profile, in the shape of
// TestThemeChangeAppliesImmediately: cycling the row changes the keys the tabs answer from
// the next keystroke, not at the next launch. Before this, applyCycle re-seeded the pane
// alone, so the pane showed the operator that the change had applied and esc returned them
// to a Feed that still answered the old keys.

// profileRoot returns a root over the default config with two Runs in the Feed, so a motion
// key has somewhere to move the cursor to.
func profileRoot(t *testing.T) Model {
	t.Helper()
	m := New(Options{Profile: keys.Standard, Config: wiringConfig()})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	id := domain.RepoID{Host: domain.HostGitHub, Owner: "acme", Name: "api"}
	m = step(t, m, scheduler.Update{Repo: id, Runs: []domain.Run{
		{ID: 1, Name: "CI", WorkflowName: "CI", Status: domain.StatusQueued, Repo: id, RunStartedAt: time.Unix(1000, 0)},
		{ID: 2, Name: "CI", WorkflowName: "CI", Status: domain.StatusQueued, Repo: id, RunStartedAt: time.Unix(500, 0)},
	}})
	return m
}

// cycleProfile opens the Settings pane, cycles the keybinding row once and closes it again,
// which is the sequence the operator performs. It walks the pane with whichever motion key
// the profile in force binds, because the pane has always applied a cycle to itself: after
// one cycle to Vim the pane answers j, and a helper hard-coded to down would stall on the
// first row.
func cycleProfile(t *testing.T, m Model) Model {
	t.Helper()
	down := "down"
	if m.profile.Name == "Vim" {
		down = "j"
	}
	m = step(t, m, press(","))
	for i := 0; i < 16 && m.settings.CursorKey() != "keybinding_profile"; i++ {
		m = step(t, m, press(down))
	}
	if m.settings.CursorKey() != "keybinding_profile" {
		t.Fatalf("never reached the keybinding row; cursor at %q", m.settings.CursorKey())
	}
	m = step(t, m, press("space"))
	return step(t, m, press("esc"))
}

// cursorRow returns the index of the Run row the Feed is painting its cursor on, read off
// the frame rather than the model, because the cursor is the Feed's own state and this test
// lives in the root's package. The cursor row is the one lipgloss renders in italic, which
// is the marker renderRow reserves for it.
func cursorRow(t *testing.T, frame string) int {
	t.Helper()
	n := -1
	for _, line := range strings.Split(frame, "\n") {
		if !strings.Contains(line, "acme/api") {
			continue
		}
		n++
		if strings.Contains(line, "\x1b[3;7m") {
			return n
		}
	}
	t.Fatalf("no cursor row in the frame:\n%s", frame)
	return -1
}

// TestKeybindingProfileChangeAppliesImmediately is the defect: cycling to Vim makes the
// focused Feed answer k and j from the next keystroke, and stop answering the arrows, with
// no relaunch.
func TestKeybindingProfileChangeAppliesImmediately(t *testing.T) {
	m := profileRoot(t)
	m = step(t, m, press("down"))
	if got := cursorRow(t, m.View().Content); got != 1 {
		t.Fatalf("Standard's down did not move the cursor before the change: row %d", got)
	}
	m = step(t, m, press("up"))

	m = cycleProfile(t, m) // Standard to Vim

	m = step(t, m, press("j"))
	if got := cursorRow(t, m.View().Content); got != 1 {
		t.Errorf("Vim's j did not move the focused tab's cursor after the cycle: row %d (R5, R17)", got)
	}
	m = step(t, m, press("k"))
	if got := cursorRow(t, m.View().Content); got != 0 {
		t.Errorf("Vim's k did not move the cursor back: row %d (R5, R17)", got)
	}
	if got := cursorRow(t, step(t, m, press("down")).View().Content); got != 0 {
		t.Errorf("Standard's down still moved the cursor under Vim: row %d (R7a)", got)
	}
}

// TestKeybindingProfileChangeReachesTheRootAndEveryTab pins the rest of the surface. The
// root's own global keys and the running surface's take the change too, and every tab takes
// it rather than the focused one, so a tab answers the new profile the moment focus reaches
// it rather than a keystroke later.
func TestKeybindingProfileChangeReachesTheRootAndEveryTab(t *testing.T) {
	m := profileRoot(t)
	if m.profile.Name != "Standard" {
		t.Fatalf("the root did not launch on the config's profile: %q", m.profile.Name)
	}

	// The whole cycle happens with the Feed unfocused, so what it ends holding it took while
	// it was not the key target.
	m = step(t, m, press("tab"))
	if m.active == feedTabIndex {
		t.Fatal("focus did not leave the Feed")
	}
	m = cycleProfile(t, m)

	// The root's own global navigation keys and the running surface's two keys are shared
	// across the profiles, because R7a makes the two differ on motion and nowhere else. What
	// is assertable here is that the root re-resolved the profile it matches those keys
	// against, rather than keeping the launch copy.
	if m.profile.Name != "Vim" {
		t.Errorf("the root kept the launch profile after the cycle: %q (R17)", m.profile.Name)
	}
	if !switchesTab(m, "tab") {
		t.Error("the root stopped answering its own tab-switch key after the cycle")
	}

	// The Feed's own frame, read off the held tab rather than off the root's, so it is the
	// unfocused tab's state being asserted. Its help line names the profile it answers.
	if frame := feedFrame(t, m); !strings.Contains(frame, "Vim") {
		t.Errorf("a tab that was unfocused throughout the cycle kept the launch profile:\n%s", frame)
	}
	m = step(t, m, press("shift+tab")) // back to Runs
	if got := cursorRow(t, m.View().Content); got != 0 {
		t.Fatalf("the refocused Feed did not open on its first row: %d", got)
	}
	if got := cursorRow(t, step(t, m, press("j")).View().Content); got != 1 {
		t.Errorf("the refocused Feed did not answer Vim's j: row %d (R17)", got)
	}
}

// switchesTab reports whether the root still matches s against its own global bindings,
// which is what a tab switch rides on.
func switchesTab(m Model, s string) bool {
	before := m.active
	next, _ := m.handleKey(press(s))
	nm, ok := next.(Model)
	return ok && nm.active != before
}

// TestKeybindingProfileHasExactlyTwoMembers pins R5 and AC4 through the live path: cycling
// twice returns to the launch profile, so no third member is reachable and the change is
// still live on the way back.
func TestKeybindingProfileHasExactlyTwoMembers(t *testing.T) {
	m := profileRoot(t)
	m = cycleProfile(t, m)
	if m.profile.Name != "Vim" {
		t.Fatalf("the first cycle resolved %q, want Vim", m.profile.Name)
	}
	m = cycleProfile(t, m)
	if m.profile.Name != "Standard" {
		t.Errorf("the second cycle resolved %q, want Standard: a third member is reachable (R5, AC4)", m.profile.Name)
	}
	m = step(t, m, press("down"))
	if got := cursorRow(t, m.View().Content); got != 1 {
		t.Errorf("cycling back to Standard did not restore the arrows: row %d (R17)", got)
	}
}
