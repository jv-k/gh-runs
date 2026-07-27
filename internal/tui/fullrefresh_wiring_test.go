package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// repo-discovery R11's on-demand full refresh is the root's, for the reason Settings is:
// discovery belongs to no tab, every tab reads what a pass rewrites, and three tabs owning
// it would be three ways to start three concurrent passes. R23 makes it load-bearing rather
// than convenient, because a retired repository returns through this and nothing else.

// TestFullRefreshKeyRunsThePass is the wiring. The key is intercepted by the root, and the
// Cmd it returns calls the seam.
func TestFullRefreshKeyRunsThePass(t *testing.T) {
	called := 0
	m := New(Options{
		Profile:     keys.Standard,
		FullRefresh: func() { called++ },
	})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if cmd == nil {
		t.Fatal("the full-refresh key returned no Cmd, so no pass can run")
	}
	// The root defers the pass into a Cmd rather than running it on the update loop,
	// because a pass issues one request per repository and the loop must not wait on it.
	if called != 0 {
		t.Error("the pass ran on the update loop, want it deferred into the Cmd")
	}
	cmd()
	if called != 1 {
		t.Errorf("the Cmd ran the pass %d times, want once", called)
	}
}

// TestFullRefreshKeyDoesNotReachATab is the other half of the root owning it. If the key
// reached the focused tab as well, a tab could bind u to something of its own and the two
// would both fire on one press, which is the bug the per-message-class routing rule exists
// to prevent (ADR-0011).
func TestFullRefreshKeyDoesNotReachATab(t *testing.T) {
	tab := &recordingTab{title: "Runs"}
	m := New(Options{Profile: keys.Standard, FullRefresh: func() {}})
	m.tabs[0] = tab
	m.active = 0

	m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})

	for _, k := range tab.keys {
		if k == "u" {
			t.Error("the full-refresh key reached the focused tab as well as the root")
		}
	}
}

// TestFullRefreshWithNoSeamIsInert covers a headless test and any surface built without
// discovery. A nil seam must yield a nil Cmd rather than a Cmd that panics when tea runs it.
func TestFullRefreshWithNoSeamIsInert(t *testing.T) {
	m := New(Options{Profile: keys.Standard})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if cmd != nil {
		t.Error("a nil full-refresh seam produced a Cmd, want none")
	}
}

// TestFullRefreshDoesNotFireWhileATabCapturesInput is the capture rule applied to this key.
// u is an ordinary letter, so it is filter text while the Feed's filter input is focused,
// exactly as q, a digit and a comma are (R7, R23). Firing a ~163-request pass because
// somebody typed a repository name containing u would be the worst instance of the bug that
// rule exists to prevent, because nothing on screen would explain the spend.
func TestFullRefreshDoesNotFireWhileATabCapturesInput(t *testing.T) {
	called := 0
	tab := &recordingTab{title: "Runs", captures: true}
	m := New(Options{Profile: keys.Standard, FullRefresh: func() { called++ }})
	m.tabs[0] = tab
	m.active = 0

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if cmd != nil {
		cmd()
	}
	if called != 0 {
		t.Error("the full refresh fired from a key typed into a focused text input")
	}
	var reached bool
	for _, k := range tab.keys {
		if k == "u" {
			reached = true
		}
	}
	if !reached {
		t.Error("u was swallowed by the root instead of reaching the capturing tab as text")
	}
}
