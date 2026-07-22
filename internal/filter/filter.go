// Package filter is the filter engine: one stated filter over Runs, with a
// client-side matcher and a server-side query projection derived from the same
// value (ADR-0016). It is one implementation three consumers adapt, cli's flags,
// ops's Purge and the Feed's filter input, so a typo is rejected by the same
// code with the same message wherever it arrives from.
//
// It is pure logic over domain alone (ADR-0011): parse, classify, compare.
// Nothing here issues a request, resolves a Workflow selector, or holds a list
// it did not receive as an argument. The one guarantee the package does not
// hold is that Conclusion never reaches the wire: Query has no path that emits a
// conclusion parameter, and cli-surface AC4 asserts that at the counting
// transport, the stronger seam.
package filter

import (
	"slices"
	"strconv"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Filter is one stated filter over Runs: AND across axes, OR within an axis's
// values. The zero value matches every Run (ADR-0016). It is a flat struct
// deliberately: the domain has exactly one disjunction, the Status and
// Conclusion pair, and a pair of typed sets encodes it structurally.
type Filter struct {
	Branch   string
	Commit   string
	Actor    string
	Event    string
	Workflow string // the raw selector: a name, a filename, or a numeric ID

	// The permissive pair. One -s input parses into exactly one of these two
	// sets (ParseStatus), and a Run matches the pair when its Status is in
	// Statuses or its Conclusion is in Conclusions.
	Statuses    []domain.Status
	Conclusions []domain.Conclusion

	// Client-side only, like Conclusions. Empty means every repository.
	Repos []domain.RepoID
}

// Match evaluates the whole Filter against one Run, client-side. It is total:
// every axis is checked, because a Purge crawls unfiltered past the 1,000 cap
// and filters entirely here (ADR-0005, cli-surface R15). An empty axis is no
// constraint, so the zero Filter matches every Run.
func (f Filter) Match(r domain.Run) bool {
	if f.Branch != "" && r.HeadBranch != f.Branch {
		return false
	}
	if f.Commit != "" && r.HeadSHA != f.Commit {
		return false
	}
	if f.Actor != "" && r.Actor.Login != f.Actor {
		return false
	}
	if f.Event != "" && r.Event != f.Event {
		return false
	}
	// The permissive pair, ADR-0016's one disjunction: when either set is
	// constrained the Run must land in one of them, OR across the two fields.
	if len(f.Statuses) > 0 || len(f.Conclusions) > 0 {
		if !slices.Contains(f.Statuses, r.Status) && !slices.Contains(f.Conclusions, r.Conclusion) {
			return false
		}
	}
	// The repository axis: client-side scoping, OR within the set, empty means
	// every repository (live-run-feed R3). No Query() form, like Conclusions.
	if len(f.Repos) > 0 && !slices.Contains(f.Repos, r.Repo) {
		return false
	}
	if f.Workflow != "" && !matchesWorkflow(f.Workflow, r) {
		return false
	}
	return true
}

// matchesWorkflow is ADR-0016's client-side selector rule: a Run matches when
// the selector equals its stamped WorkflowName, or its WorkflowID when the
// selector is numeric. A filename selector matches nothing here, because a Run
// carries no path to compare it against, and resolving one needs the Workflow
// list this package must never hold.
func matchesWorkflow(selector string, r domain.Run) bool {
	if selector == r.WorkflowName {
		return true
	}
	if id, err := strconv.ParseInt(selector, 10, 64); err == nil {
		return id == r.WorkflowID
	}
	return false
}
