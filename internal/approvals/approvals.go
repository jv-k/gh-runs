// Package approvals classifies a Run blocked on a human decision, the two-field
// predicate the Feed's badge and saved filter evaluate client-side (approvals R2 to R7).
// It is a leaf over domain alone (ADR-0011): it issues no request and holds no list it
// did not receive as an argument, which is what makes R5's "the badge and the filter must
// issue no request of their own and spend no Budget" a property of the code rather than a
// promise. Both fields it reads already ride on every Feed Run (ADR-0003's fan-out), so
// there is no new data source.
//
// Status and Conclusion are two different fields, and this is the feature where conflating
// them does the most damage: the field that matches decides which request is sent. A
// fork-PR Run awaiting approval has Status completed and Conclusion action_required, so a
// predicate reading Status alone silently misses every one of them (R4). notifications
// reuses this predicate for its one default approval event when 2.1 builds it, so the
// classification has one home.
package approvals

import "github.com/jv-k/gh-runs/v2/internal/domain"

// Kind is which human decision a Run is blocked on, classified by which field matched
// (R2, R3). The two kinds take different API requests, /approve and /pending_deployments,
// so a caller that knows a Run is blocked but not which field said so cannot choose
// between them (R3). Conflating the two here does not merely mislabel a row: it sends the
// wrong request to the API.
type Kind int

const (
	// KindNone is a Run blocked on no decision this feature acts on.
	KindNone Kind = iota
	// KindForkPR is a fork-PR Run awaiting maintainer approval: its Conclusion is
	// action_required and its Status is completed (R2, measured on cli/cli and
	// home-assistant/core). The action is to approve the Run.
	KindForkPR
	// KindPendingDeployment is a Run whose Status is waiting on a deployment approval
	// (R2). The action is to review the Run's pending deployments.
	KindPendingDeployment
)

// String names a Kind for a status line or a test failure.
func (k Kind) String() string {
	switch k {
	case KindForkPR:
		return "fork-pr-approval"
	case KindPendingDeployment:
		return "pending-deployment"
	default:
		return "none"
	}
}

// Classify returns the Kind of decision a Run is blocked on, by which field matched (R2,
// R3). It is a two-field predicate: the fork-PR case reads Conclusion with the Status
// completed that accompanies it, and the pending-deployment case reads Status. It never
// reads Status alone, because a fork-PR Run awaiting approval has Status completed and a
// Status-only predicate silently misses every one of them (R4).
func Classify(r domain.Run) Kind {
	// R2's first row is a Conclusion match. Conclusion is null until Status reaches
	// completed (CONTEXT.md), so action_required implies completed; the Status guard
	// states R2's two-field rule rather than leaning on that invariant, and it keeps the
	// two kinds disjoint by construction.
	if r.Status == domain.StatusCompleted && r.Conclusion == domain.ConclusionActionRequired {
		return KindForkPR
	}
	// R2's second row is a Status match. waiting is a Status (CONTEXT.md, measured), and a
	// Run in it carries a null Conclusion, so it can never also match the fork-PR branch.
	if r.Status == domain.StatusWaiting {
		return KindPendingDeployment
	}
	return KindNone
}

// Awaiting reports whether a Run is blocked on a decision of either kind, the badge and
// saved-filter predicate the Feed evaluates client-side over held Runs (R5, R7). It reads
// the two fields already held, so it issues no request and spends no Budget (R5).
func Awaiting(r domain.Run) bool {
	return Classify(r) != KindNone
}

// Count is the number of Runs held that await a decision (R7). It counts the Runs
// actually held that match the predicate, and never derives a count from total_count,
// which in a filtered view reports matches rather than reachable matches (R7).
func Count(runs []domain.Run) int {
	n := 0
	for i := range runs {
		if Awaiting(runs[i]) {
			n++
		}
	}
	return n
}
