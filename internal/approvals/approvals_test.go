package approvals_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// run builds a Run carrying only the two fields the predicate reads, which is exactly
// what R5 relies on: both fields already ride on every Feed Run, so the predicate needs
// nothing else.
func run(status domain.Status, concl domain.Conclusion) domain.Run {
	return domain.Run{Status: status, Conclusion: concl}
}

// TestClassifyForkPR pins R2's first row: a Run whose Status is completed and whose
// Conclusion is action_required is a fork-PR Run awaiting approval, routed to the approve
// action. This is the counter-intuitive case the constraints table measured on cli/cli
// and home-assistant/core: the Run has stopped, and its outcome is "needs action".
func TestClassifyForkPR(t *testing.T) {
	got := approvals.Classify(run(domain.StatusCompleted, domain.ConclusionActionRequired))
	if got != approvals.KindForkPR {
		t.Fatalf("Classify(completed/action_required) = %v, want KindForkPR (R2)", got)
	}
}

// TestClassifyPendingDeployment pins R2's second row: a Run whose Status is waiting is a
// pending deployment, routed to the review action. Its Conclusion is null, because
// Conclusion stays null until Status reaches completed (CONTEXT.md).
func TestClassifyPendingDeployment(t *testing.T) {
	got := approvals.Classify(run(domain.StatusWaiting, domain.ConclusionNone))
	if got != approvals.KindPendingDeployment {
		t.Fatalf("Classify(waiting/none) = %v, want KindPendingDeployment (R2)", got)
	}
}

// TestKindsAreDistinct pins R3: the two kinds never collapse, because they take
// different requests. A predicate that knew a Run was blocked but not which field said
// so could not choose between /approve and /pending_deployments.
func TestKindsAreDistinct(t *testing.T) {
	fork := approvals.Classify(run(domain.StatusCompleted, domain.ConclusionActionRequired))
	pending := approvals.Classify(run(domain.StatusWaiting, domain.ConclusionNone))
	if fork == pending {
		t.Fatalf("the two kinds collapsed to %v; R3 routes them to different requests", fork)
	}
	if fork == approvals.KindNone || pending == approvals.KindNone {
		t.Fatalf("a blocked Run classified as KindNone: fork=%v pending=%v", fork, pending)
	}
}

// TestStatusAloneMissesForkPR pins AC3 and R4, the case the prior generation of tools got
// wrong. A Status-only predicate matches the pending deployment and misses the fork-PR
// Run entirely, because the fork-PR Run's Status is completed. Awaiting must catch it,
// because it reads Conclusion, never Status alone.
func TestStatusAloneMissesForkPR(t *testing.T) {
	fork := run(domain.StatusCompleted, domain.ConclusionActionRequired)
	pending := run(domain.StatusWaiting, domain.ConclusionNone)

	// A hypothetical Status-only predicate: it reads Status alone, as R4 forbids.
	statusOnly := func(r domain.Run) bool { return r.Status == domain.StatusWaiting }
	if statusOnly(fork) {
		t.Fatalf("premise: a Status-only predicate must not match the fork-PR Run (its Status is completed)")
	}
	if !statusOnly(pending) {
		t.Fatalf("premise: a Status-only predicate matches the waiting Run")
	}

	// The real predicate reads both fields, so it catches the fork-PR Run the Status-only
	// one missed (R4, AC3).
	if !approvals.Awaiting(fork) {
		t.Fatalf("Awaiting missed the fork-PR Run; R4 forbids a Status-only predicate (AC3)")
	}
	if approvals.Classify(fork) != approvals.KindForkPR {
		t.Fatalf("the fork-PR Run did not classify as a fork-PR approval (AC3)")
	}
}

// TestClassifyNone pins that a Run blocked on no decision classifies as none, so the
// badge counts neither a healthy completed Run nor an in-flight one.
func TestClassifyNone(t *testing.T) {
	cases := []domain.Run{
		run(domain.StatusCompleted, domain.ConclusionSuccess),
		run(domain.StatusCompleted, domain.ConclusionFailure),
		run(domain.StatusInProgress, domain.ConclusionNone),
		run(domain.StatusQueued, domain.ConclusionNone),
		run(domain.StatusRequested, domain.ConclusionNone),
		run(domain.StatusPending, domain.ConclusionNone),
	}
	for _, r := range cases {
		if got := approvals.Classify(r); got != approvals.KindNone {
			t.Errorf("Classify(%s/%s) = %v, want KindNone", r.Status, r.Conclusion, got)
		}
		if approvals.Awaiting(r) {
			t.Errorf("Awaiting(%s/%s) = true, want false", r.Status, r.Conclusion)
		}
	}
}

// TestCountBothKinds pins AC1 and R7: the count is the number of held Runs that match the
// predicate, and it counts both kinds. Two of the three Runs held await a decision.
func TestCountBothKinds(t *testing.T) {
	runs := []domain.Run{
		run(domain.StatusCompleted, domain.ConclusionActionRequired), // fork-PR
		run(domain.StatusWaiting, domain.ConclusionNone),             // pending deployment
		run(domain.StatusCompleted, domain.ConclusionSuccess),        // neither
	}
	if got := approvals.Count(runs); got != 2 {
		t.Fatalf("Count = %d, want 2 (AC1, R7)", got)
	}
}
