package keys_test

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"

	"github.com/jv-k/gh-runs/v2/internal/keys"
)

// containsKey reports whether a binding carries the given keystroke.
func containsKey(b key.Binding, want string) bool {
	for _, k := range b.Keys() {
		if k == want {
			return true
		}
	}
	return false
}

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

// motionFields names R7a's motion axis, the only fields on which Vim and
// Standard may disagree. It is transcribed from R7a's motion table, not read
// back off the profiles, so TestProfilesDifferOnlyOnMotion checks the registry
// against the requirement rather than against itself.
var motionFields = map[string]bool{
	"RowUp":    true,
	"RowDown":  true,
	"PageUp":   true,
	"PageDown": true,
	"FirstRow": true,
	"LastRow":  true,
}

// TestProfilesDifferOnlyOnMotion computes, by reflecting over the two profiles,
// the exact set of key.Binding fields on which Vim and Standard disagree, and
// asserts it is the motion set and nothing more (R7a: "differ on motion, and
// nowhere else"). assertShared pins the shared bindings by hand, so a future
// shared binding a contributor forks by bypassing shared() and never adds to
// that helper would keep the suite green. This closes the gap structurally: any
// non-motion field that diverges, or a motion field that stops diverging, fails
// here, with no hand-maintained enumeration to remember to extend.
func TestProfilesDifferOnlyOnMotion(t *testing.T) {
	bindingType := reflect.TypeOf(key.Binding{})
	vim := reflect.ValueOf(keys.Vim)
	std := reflect.ValueOf(keys.Standard)
	rt := vim.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type != bindingType {
			continue
		}
		vb := vim.Field(i).Interface().(key.Binding)
		sb := std.Field(i).Interface().(key.Binding)
		differs := !reflect.DeepEqual(vb, sb)
		switch {
		case differs && !motionFields[f.Name]:
			t.Errorf("field %s differs between Vim and Standard (Vim %v, Standard %v) but is not a motion binding; R7a permits divergence only on motion", f.Name, vb.Keys(), sb.Keys())
		case !differs && motionFields[f.Name]:
			t.Errorf("field %s is a motion binding but is identical in both profiles; R7a requires the motion axis to differ", f.Name)
		}
	}
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
	assertKeys(t, name+".FilterAccept", p.FilterAccept, "enter") // R23: the filter input's accept, in the registry (R7a)
	assertKeys(t, name+".FilterCancel", p.FilterCancel, "esc")   // R23: the filter input's cancel, in the registry (R7a)
}

// TestSharedBindings pins R7a's "Everything else" table, the bindings that are
// identical in both profiles. Asserting the same helper over both profiles is
// how "and nowhere else" is checked: a forked binding fails one of the two runs.
func TestSharedBindings(t *testing.T) {
	assertShared(t, "Vim", keys.Vim)
	assertShared(t, "Standard", keys.Standard)
}

// assertConfirm pins R7a's "Confirm modal" table, also identical in both
// profiles. y accepts below the threshold, n/esc abort, and enter aborts on the
// default (the default is no), so enter opens Run detail in the Feed and cancels
// in the modal, two surfaces, one key. v inspects the frozen set, which purge
// R30 requires the modal to name on its face. There is deliberately no binding
// for the typed count at or above the threshold: it is numeric input, not a
// keystroke, and purge R7 forbids y from starting it.
func assertConfirm(t *testing.T, name string, p keys.Profile) {
	t.Helper()
	assertKeys(t, name+".ConfirmAccept", p.ConfirmAccept, "y")                 // purge R7, AC6
	assertKeys(t, name+".ConfirmAbort", p.ConfirmAbort, "n", "esc")            // purge AC6
	assertKeys(t, name+".ConfirmAbortDefault", p.ConfirmAbortDefault, "enter") // purge AC6, default is no
	assertKeys(t, name+".ConfirmInspect", p.ConfirmInspect, "v")               // purge R30, named on the modal
}

// TestConfirmBindings pins R7a's confirm-modal table over both profiles.
func TestConfirmBindings(t *testing.T) {
	assertConfirm(t, "Vim", keys.Vim)
	assertConfirm(t, "Standard", keys.Standard)
}

// TestExactlyTwoSelectableProfiles pins AC18's "exactly two profiles are
// selectable" and R7's "offer no others". Profiles enumerates them, ForName is
// how settings turns a stored value into one, and a third name resolves to
// nothing.
func TestExactlyTwoSelectableProfiles(t *testing.T) {
	got := keys.Profiles()
	if len(got) != 2 {
		t.Fatalf("Profiles() returned %d profiles, want exactly 2 (R7)", len(got))
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if len(names) != 2 || !names["Vim"] || !names["Standard"] {
		t.Fatalf("Profiles() names = %v, want exactly {Vim, Standard} (R7)", names)
	}
	for _, name := range []string{"Vim", "vim", "Standard", "standard"} {
		if _, ok := keys.ForName(name); !ok {
			t.Errorf("ForName(%q) not found; both profiles must be selectable (AC18)", name)
		}
	}
	if _, ok := keys.ForName("emacs"); ok {
		t.Errorf("ForName(\"emacs\") resolved a profile; R7 permits no third")
	}
}

// TestBindingsCoversEveryField pins AC18's premise that the check runs over the
// whole registry. The no-Cmd and ctrl+c invariants below iterate Bindings(), so
// a field Bindings() forgot would escape them, which is the scattered-binding
// gap R7a's registry exists to close. The want count is read from the struct by
// reflection, not hardcoded, so adding a field without enumerating it fails here.
func TestBindingsCoversEveryField(t *testing.T) {
	bindingType := reflect.TypeOf(key.Binding{})
	rt := reflect.TypeOf(keys.Profile{})
	want := 0
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Type == bindingType {
			want++
		}
	}
	for _, p := range keys.Profiles() {
		if got := len(p.Bindings()); got != want {
			t.Errorf("%s.Bindings() enumerates %d bindings, but the struct has %d; the enumeration is incomplete", p.Name, got, want)
		}
	}
}

// TestNoBindingUsesCmd pins AC18: no binding in either profile may carry a key
// naming Cmd, which terminals do not send (R7). The forbidden tokens are AC18's
// own list, and the check runs over the whole enumerated registry.
func TestNoBindingUsesCmd(t *testing.T) {
	forbidden := []string{"cmd+", "super+", "meta+"} // AC18's list, verbatim
	for _, p := range keys.Profiles() {
		for _, b := range p.Bindings() {
			for _, k := range b.Keys() {
				for _, bad := range forbidden {
					if strings.Contains(k, bad) {
						t.Errorf("profile %s binds %q, which names Cmd; AC18 forbids %q", p.Name, k, bad)
					}
				}
			}
		}
	}
}

// TestCtrlCBoundToQuitOnly pins R7 and AC18: ctrl+c appears in both profiles
// bound to quitting and to nothing else, because the terminal sends it as
// SIGINT. Counting its appearances across the whole registry is how "nothing
// else" is asserted: exactly one binding carries it, and that binding is Quit.
func TestCtrlCBoundToQuitOnly(t *testing.T) {
	for _, p := range keys.Profiles() {
		count := 0
		for _, b := range p.Bindings() {
			if containsKey(b, "ctrl+c") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("profile %s: ctrl+c appears in %d bindings, want exactly 1 (R7: bound to nothing but quitting)", p.Name, count)
		}
		if !containsKey(p.Quit, "ctrl+c") {
			t.Errorf("profile %s: Quit does not bind ctrl+c (R7, AC18)", p.Name)
		}
	}
}

// TestAccessorsReturnCallerOwnedCopies pins the read-only contract the package
// documents: a Profile taken from ForName or Profiles, and a slice taken from
// Bindings, is the caller's own copy, and mutating it cannot reach the registry.
// key.Binding.Keys() hands out its internal slice, so a shallow struct copy still
// shares that backing array and "a caller cannot alter the registry" would be
// only theoretical; these accessors deep-copy the key slices so it is real.
func TestAccessorsReturnCallerOwnedCopies(t *testing.T) {
	const sentinel = "MUTATED-BY-TEST"

	// A profile from ForName is independent of keys.Vim.
	p, ok := keys.ForName("Vim")
	if !ok {
		t.Fatal(`ForName("Vim") not found`)
	}
	if ks := p.RowUp.Keys(); len(ks) > 0 {
		ks[0] = sentinel
	}
	if containsKey(keys.Vim.RowUp, sentinel) {
		t.Errorf("mutating a ForName profile reached keys.Vim.RowUp: %v", keys.Vim.RowUp.Keys())
	}

	// A profile from Profiles is independent of keys.Vim.
	if ks := keys.Profiles()[0].Quit.Keys(); len(ks) > 0 {
		ks[0] = sentinel
	}
	if containsKey(keys.Vim.Quit, sentinel) {
		t.Errorf("mutating a Profiles profile reached keys.Vim.Quit: %v", keys.Vim.Quit.Keys())
	}

	// A slice from Bindings is independent of the profile it came from.
	if ks := keys.Vim.Bindings()[0].Keys(); len(ks) > 0 {
		ks[0] = sentinel
	}
	if containsKey(keys.Vim.Bindings()[0], sentinel) {
		t.Error("mutating a Bindings slice reached the registry")
	}
}
