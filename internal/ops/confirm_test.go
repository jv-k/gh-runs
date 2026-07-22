package ops_test

import (
	"errors"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// planAt builds a Plan of n single-repo completed Runs at the given threshold, for
// the confirmation-table properties. n below the threshold prices YN, at or above
// prices TypedCount (R7).
func planAt(t *testing.T, n, threshold int) ops.Plan {
	t.Helper()
	p, err := newPlanOps(threshold).Plan(ops.OpDelete, runItems("o", "a", n, 1), snapshot(writableRepo("o", "a")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// TestConfirmYNTable pins AC6: y confirms a below-threshold single-repo set, and
// n, an empty answer (Enter on the default), and a stray key are all declined.
func TestConfirmYNTable(t *testing.T) {
	o := newPlanOps(50)
	p := planAt(t, 49, 50) // FrictionYN
	cases := []struct {
		name string
		in   ops.Input
		ok   bool
	}{
		{"y confirms", ops.Answer("y"), true},
		{"n aborts", ops.Answer("n"), false},
		{"enter-on-default aborts", ops.Answer(""), false},
		{"the typed count does not confirm a y/N set as if it were y", ops.Answer("49"), false},
		{"--yes confirms", ops.NonInteractiveYes(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := o.Confirm(p, c.in)
			if c.ok && err != nil {
				t.Errorf("Confirm(%v) = %v, want accepted", c.in, err)
			}
			if !c.ok && !errors.Is(err, ops.ErrDeclined) {
				t.Errorf("Confirm(%v) = %v, want ErrDeclined", c.in, err)
			}
		})
	}
}

// TestConfirmTypedCountTable pins AC7 and R7's typed-count limb: only the exact
// count string starts a set at the threshold, y does not, and a wrong number is
// declined.
func TestConfirmTypedCountTable(t *testing.T) {
	o := newPlanOps(50)
	p := planAt(t, 50, 50) // FrictionTypedCount, Total 50
	cases := []struct {
		name string
		in   ops.Input
		ok   bool
	}{
		{"the exact count confirms", ops.Answer("50"), true},
		{"y does not start a typed-count set", ops.Answer("y"), false},
		{"a wrong number is declined", ops.Answer("49"), false},
		{"an empty answer is declined", ops.Answer(""), false},
		{"--yes confirms", ops.NonInteractiveYes(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := o.Confirm(p, c.in)
			if c.ok && err != nil {
				t.Errorf("Confirm(%v) = %v, want accepted", c.in, err)
			}
			if !c.ok && !errors.Is(err, ops.ErrDeclined) {
				t.Errorf("Confirm(%v) = %v, want ErrDeclined", c.in, err)
			}
		})
	}
}

// TestDeclinedConfirmLeavesThePlanUsable pins AC6's tail: a declined Confirm changes
// nothing, so the same Plan can be presented again and confirmed.
func TestDeclinedConfirmLeavesThePlanUsable(t *testing.T) {
	o := newPlanOps(50)
	p := planAt(t, 10, 50) // FrictionYN
	if _, err := o.Confirm(p, ops.Answer("n")); !errors.Is(err, ops.ErrDeclined) {
		t.Fatalf("first Confirm = %v, want ErrDeclined", err)
	}
	if _, err := o.Confirm(p, ops.Answer("y")); err != nil {
		t.Fatalf("second Confirm after a decline = %v, want accepted; a decline must not consume the Plan", err)
	}
}
