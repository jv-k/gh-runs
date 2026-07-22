package keys_test

import (
	"reflect"
	"testing"

	"charm.land/bubbles/v2/key"

	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// assertKeys pins a binding's keystrokes against the exact set the canon names.
// The want values are transcribed from live-run-feed R7a's tables, never read
// back off the binding, so the test fails if the registry drifts from the spec.
func assertKeys(t *testing.T, name string, b key.Binding, want ...string) {
	t.Helper()
	if got := b.Keys(); !reflect.DeepEqual(got, want) {
		t.Errorf("%s keys = %v, want %v", name, got, want)
	}
}

// TestMotionProfilesDiffer pins the one axis on which Vim and Standard disagree
// (live-run-feed R7a: "The two profiles differ on motion, and nowhere else").
// Every row here is the motion table verbatim: k/j vim, up/down standard, the
// page pair, and the first/last pair purge R30 depends on outright. AC18 asserts
// the k/j, up/down, g/G and home/end rows by name, so they are pinned by name.
func TestMotionProfilesDiffer(t *testing.T) {
	// Row up, row down: run-detail AC1 walks 100 rows with arrows.
	assertKeys(t, "Vim.RowUp", keys.Vim.RowUp, "k")
	assertKeys(t, "Vim.RowDown", keys.Vim.RowDown, "j")
	assertKeys(t, "Standard.RowUp", keys.Standard.RowUp, "up")
	assertKeys(t, "Standard.RowDown", keys.Standard.RowDown, "down")

	// Page up, page down: purge R30.
	assertKeys(t, "Vim.PageUp", keys.Vim.PageUp, "ctrl+b")
	assertKeys(t, "Vim.PageDown", keys.Vim.PageDown, "ctrl+f")
	assertKeys(t, "Standard.PageUp", keys.Standard.PageUp, "pgup")
	assertKeys(t, "Standard.PageDown", keys.Standard.PageDown, "pgdown")

	// First row, last row: purge R30 depends on reaching both ends.
	assertKeys(t, "Vim.FirstRow", keys.Vim.FirstRow, "g")
	assertKeys(t, "Vim.LastRow", keys.Vim.LastRow, "G")
	assertKeys(t, "Standard.FirstRow", keys.Standard.FirstRow, "home")
	assertKeys(t, "Standard.LastRow", keys.Standard.LastRow, "end")
}

// assertShared pins every binding that R7a declares identical in both profiles
// ("differ on motion, and nowhere else"). Running it against Vim and against
// Standard asserts both the keys and the sameness: if either profile forked one
// of these, one call would fail. Keys are transcribed from R7a's "Everything
// else" table.
func assertShared(t *testing.T, name string, p keys.Profile) {
	t.Helper()
	assertKeys(t, name+".NextTab", p.NextTab, "tab")             // R2's three tabs
	assertKeys(t, name+".PrevTab", p.PrevTab, "shift+tab")       // R2
	assertKeys(t, name+".SelectTab", p.SelectTab, "1", "2", "3") // R2, addressed directly
	assertKeys(t, name+".Settings", p.Settings, ",")             // R2, never a fourth tab
	assertKeys(t, name+".ToggleSelect", p.ToggleSelect, "space") // purge R4
	assertKeys(t, name+".Refresh", p.Refresh, "r")               // R10, R11 name this key
	assertKeys(t, name+".OpenDetail", p.OpenDetail, "enter")     // BUILD-ORDER stage 8
	assertKeys(t, name+".Filter", p.Filter, "/")                 // R22, R23
	assertKeys(t, name+".Help", p.Help, "?")                     // bubbles/help renders the registry
	assertKeys(t, name+".Quit", p.Quit, "q", "ctrl+c")           // R7: ctrl+c quits and binds nothing else
}

// TestSharedBindings pins R7a's "Everything else" table, the bindings that are
// identical in both profiles. Asserting the same helper over both profiles is
// how "and nowhere else" is checked: a forked binding fails one of the two runs.
func TestSharedBindings(t *testing.T) {
	assertShared(t, "Vim", keys.Vim)
	assertShared(t, "Standard", keys.Standard)
}
