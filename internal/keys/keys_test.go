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
