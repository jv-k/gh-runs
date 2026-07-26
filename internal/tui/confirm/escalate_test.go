package confirm_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
)

// cancelPlan is a small single-repository cancel set, priced FrictionYN.
func cancelPlan(t *testing.T) ops.Plan {
	t.Helper()
	items := []ops.Item{ops.RunItem(run(1, "o", "a")), ops.RunItem(run(2, "o", "a"))}
	p, err := planOps(50).Plan(ops.OpCancel, items, map[domain.RepoID]domain.Repo{repoID("o", "a"): writable("o", "a")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// TestCancelModalOffersForceCancel is run-lifecycle R5 and AC6 at the point of decision:
// a cancel modal names force-cancel as the escalation and names the key that takes it, so
// the operator who has just been told a Run is not cancelable has somewhere to go. R6
// makes force-cancel an escalation the user chooses and never a silent substitution, so
// it is offered here rather than applied.
func TestCancelModalOffersForceCancel(t *testing.T) {
	m := confirm.New(keys.Standard).Open(cancelPlan(t))
	got := m.View()
	if !strings.Contains(got, "force-cancel") {
		t.Errorf("the cancel modal does not offer force-cancel (R5, R6, AC6):\n%s", got)
	}
	if !strings.Contains(got, keys.Standard.ForceCancel.Help().Key) {
		t.Errorf("the offer does not name the key that takes it (R7a, AC18):\n%s", got)
	}
}

// TestEscalateKeyReportsEscalation pins that the offer is actionable: the force-cancel
// key takes the modal to its own terminal Outcome, which the opener reads to re-price the
// same frozen set as force-cancel. It is not a Confirmed, because force-cancel is a
// distinct operation that must never be substituted for the cancel the operator asked for
// (R6).
func TestEscalateKeyReportsEscalation(t *testing.T) {
	m := confirm.New(keys.Standard).Open(cancelPlan(t))
	m = send(m, "C")
	if m.Outcome() != confirm.Escalated {
		t.Fatalf("the force-cancel key left the outcome at %v, want Escalated (R5, R6, AC6)", m.Outcome())
	}
	if m.Plan().Operation() != ops.OpCancel {
		t.Errorf("the pane rewrote its own Plan to %q; the opener owns the re-price (ADR-0019)", m.Plan().Operation())
	}
}

// TestOnlyACancelEscalates pins that the escalation is cancel's alone. Nothing escalates
// out of a Purge or a re-run, and a Purge modal that read C as anything would be a
// destructive key with a second meaning.
func TestOnlyACancelEscalates(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan ops.Plan
	}{
		{"delete", ynPlan(t)},
		{"force-cancel", planForOp(t, ops.OpForceCancel, 50, []ops.Item{ops.RunItem(run(1, "o", "a")), ops.RunItem(run(2, "o", "a"))}, writable("o", "a"))},
	} {
		m := confirm.New(keys.Standard).Open(tc.plan)
		m = send(m, "C")
		if m.Outcome() != confirm.Pending {
			t.Errorf("a %s modal reached outcome %v on the force-cancel key, want Pending", tc.name, m.Outcome())
		}
		if got := m.View(); strings.Contains(got, "force-cancel") && tc.name == "delete" {
			t.Errorf("a delete modal offers force-cancel:\n%s", got)
		}
	}
}

// TestEscalationDoesNotDisturbATypedCount pins that a cross-repository cancel, which
// prices at TypedCount, still collects its digits: C is not a digit, so the buffer is
// untouched, and the escalation carries the friction with it rather than around it.
func TestEscalationDoesNotDisturbATypedCount(t *testing.T) {
	items := []ops.Item{ops.RunItem(run(1, "o", "a")), ops.RunItem(run(2, "o", "b"))}
	p, err := planOps(50).Plan(ops.OpCancel, items, map[domain.RepoID]domain.Repo{
		repoID("o", "a"): writable("o", "a"),
		repoID("o", "b"): writable("o", "b"),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Friction() != ops.FrictionTypedCount {
		t.Fatalf("a cross-repository cancel priced %d, want TypedCount (purge R7)", p.Friction())
	}
	m := confirm.New(keys.Standard).Open(p)
	m = send(m, "C")
	if m.Outcome() != confirm.Escalated {
		t.Errorf("a typed-count cancel did not escalate on the force-cancel key")
	}
}
