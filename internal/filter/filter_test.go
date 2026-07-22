package filter_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestZeroFilterMatchesEveryRun pins ADR-0016's foundational property: "the zero
// value matches every Run". cli-surface R26 rests on it, making the match-all
// Purge reachable only by an explicit --all rather than by an omitted filter. If
// any axis constrained by default, --all would delete less than everything and
// R26's guard would be a lie.
func TestZeroFilterMatchesEveryRun(t *testing.T) {
	var f filter.Filter // the zero value: every axis empty

	runs := []domain.Run{
		{}, // a wholly zero Run
		{Status: domain.StatusInProgress},
		{
			Status:     domain.StatusCompleted,
			Conclusion: domain.ConclusionFailure,
			HeadBranch: "main",
			HeadSHA:    "deadbeef",
			Event:      "push",
			Actor:      domain.User{Login: "octocat"},
			Repo:       domain.RepoID{Host: "github.com", Owner: "cli", Name: "cli"},
		},
	}

	for i, r := range runs {
		if !f.Match(r) {
			t.Fatalf("zero Filter did not match run %d (%+v); the zero value must match every Run", i, r)
		}
	}
}

// TestMatchScalarAxes pins the free-form scalar axes (cli-surface R2's -b, -c,
// -u, -e). Each reads the domain field the server's parameter filters on, so
// Query can push it and Match agrees (ADR-0016's idempotence contract): Branch
// reads head_branch, Commit reads head_sha, Actor reads actor.login, Event reads
// event. A set axis excludes a Run whose field differs; an empty axis is no
// constraint.
func TestMatchScalarAxes(t *testing.T) {
	cases := []struct {
		name string
		f    filter.Filter
		r    domain.Run
		want bool
	}{
		{"branch matches", filter.Filter{Branch: "main"}, domain.Run{HeadBranch: "main"}, true},
		{"branch differs", filter.Filter{Branch: "main"}, domain.Run{HeadBranch: "dev"}, false},
		{"branch absent on run", filter.Filter{Branch: "main"}, domain.Run{}, false},
		{"commit matches", filter.Filter{Commit: "abc123"}, domain.Run{HeadSHA: "abc123"}, true},
		{"commit differs", filter.Filter{Commit: "abc123"}, domain.Run{HeadSHA: "def456"}, false},
		{"actor matches login", filter.Filter{Actor: "octocat"}, domain.Run{Actor: domain.User{Login: "octocat"}}, true},
		{"actor differs", filter.Filter{Actor: "octocat"}, domain.Run{Actor: domain.User{Login: "hubot"}}, false},
		{"event matches", filter.Filter{Event: "push"}, domain.Run{Event: "push"}, true},
		{"event differs", filter.Filter{Event: "push"}, domain.Run{Event: "pull_request"}, false},
		{
			name: "all scalars set and all match",
			f:    filter.Filter{Branch: "main", Commit: "abc123", Actor: "octocat", Event: "push"},
			r:    domain.Run{HeadBranch: "main", HeadSHA: "abc123", Actor: domain.User{Login: "octocat"}, Event: "push"},
			want: true,
		},
		{
			name: "all scalars set but one differs fails the AND",
			f:    filter.Filter{Branch: "main", Commit: "abc123", Actor: "octocat", Event: "push"},
			r:    domain.Run{HeadBranch: "main", HeadSHA: "abc123", Actor: domain.User{Login: "octocat"}, Event: "pull_request"},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.f.Match(c.r); got != c.want {
				t.Fatalf("Match() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestMatchReposAxis pins the repository axis (ADR-0016, live-run-feed R3). It is
// client-side scoping made filterable: Repos matches the stamped Repo, OR within
// the set, and an empty Repos means every repository. It has no Query() form,
// exactly like Conclusions, and the Feed's one filter surface drives it rather
// than growing a private repository predicate beside the engine.
func TestMatchReposAxis(t *testing.T) {
	cli := domain.RepoID{Host: "github.com", Owner: "cli", Name: "cli"}
	core := domain.RepoID{Host: "github.com", Owner: "home-assistant", Name: "core"}
	k8s := domain.RepoID{Host: "github.com", Owner: "kubernetes", Name: "kubernetes"}

	cases := []struct {
		name string
		f    filter.Filter
		r    domain.Run
		want bool
	}{
		{"single repo matches its stamped Run", filter.Filter{Repos: []domain.RepoID{cli}}, domain.Run{Repo: cli}, true},
		{"single repo excludes another repo's Run", filter.Filter{Repos: []domain.RepoID{cli}}, domain.Run{Repo: core}, false},
		{"OR within the set matches either repo", filter.Filter{Repos: []domain.RepoID{cli, core}}, domain.Run{Repo: core}, true},
		{"a repo outside the set is excluded", filter.Filter{Repos: []domain.RepoID{cli, core}}, domain.Run{Repo: k8s}, false},
		{
			name: "the repo axis is AND-ed with the pair",
			f:    filter.Filter{Repos: []domain.RepoID{cli}, Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			r:    domain.Run{Repo: cli, Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.f.Match(c.r); got != c.want {
				t.Fatalf("Match() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestMatchWorkflowSelector pins ADR-0016's client-side Workflow contract: the
// raw selector matches a Run when it equals the stamped WorkflowName, or the
// WorkflowID when the selector is numeric, which is gh's own contract for -w. A
// filename selector (ci.yml) matches nothing client-side: domain.Run carries no
// path, so a filename is resolved by the consumer that holds the Workflow list
// (server-side, via the /workflows/{id}/runs endpoint), never here.
func TestMatchWorkflowSelector(t *testing.T) {
	cases := []struct {
		name string
		f    filter.Filter
		r    domain.Run
		want bool
	}{
		{"name matches the stamped name", filter.Filter{Workflow: "CI"}, domain.Run{WorkflowName: "CI"}, true},
		{"name excludes a different name", filter.Filter{Workflow: "CI"}, domain.Run{WorkflowName: "Release"}, false},
		{"numeric selector matches by WorkflowID regardless of name", filter.Filter{Workflow: "161335"}, domain.Run{WorkflowID: 161335, WorkflowName: "CI"}, true},
		{"numeric selector excludes a different WorkflowID", filter.Filter{Workflow: "161335"}, domain.Run{WorkflowID: 999, WorkflowName: "CI"}, false},
		{"a numeric-looking name still matches by name equality", filter.Filter{Workflow: "161335"}, domain.Run{WorkflowID: 999, WorkflowName: "161335"}, true},
		{"a filename selector matches nothing client-side", filter.Filter{Workflow: "ci.yml"}, domain.Run{WorkflowID: 161335, WorkflowName: "CI"}, false},
		{"a ruleset Run with no name is excluded by a name selector", filter.Filter{Workflow: "CI"}, domain.Run{WorkflowID: 161335, WorkflowName: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.f.Match(c.r); got != c.want {
				t.Fatalf("Match() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestMatchPermissivePair pins the one cross-field predicate the canon has
// (ADR-0016): a Run matches the pair when its Status is in Statuses OR its
// Conclusion is in Conclusions. The disjunction is OR within the pair and lives
// in the matching rule, not in a combinator a caller wires. It is AND-ed with
// every other axis. The approvals R2 badge is exactly Filter{Statuses:[waiting],
// Conclusions:[action_required]}, and it must match both a pending-deployment
// Run (Status waiting) and a fork-PR Run (Conclusion action_required, Status
// completed), which a Status-only predicate would miss.
func TestMatchPermissivePair(t *testing.T) {
	approvals := filter.Filter{
		Statuses:    []domain.Status{domain.StatusWaiting},
		Conclusions: []domain.Conclusion{domain.ConclusionActionRequired},
	}
	cases := []struct {
		name string
		f    filter.Filter
		r    domain.Run
		want bool
	}{
		{
			name: "status half matches",
			f:    filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}},
			r:    domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess},
			want: true,
		},
		{
			name: "status half excludes a different status",
			f:    filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}},
			r:    domain.Run{Status: domain.StatusInProgress},
			want: false,
		},
		{
			name: "conclusion half matches even against a Status not in the set",
			f:    filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}, Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			r:    domain.Run{Status: domain.StatusInProgress, Conclusion: domain.ConclusionFailure},
			want: true,
		},
		{
			name: "status matches even when the conclusion is not in the set (OR, not AND)",
			f:    filter.Filter{Statuses: []domain.Status{domain.StatusCompleted}, Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			r:    domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess},
			want: true,
		},
		{
			name: "conclusion half excludes a different conclusion",
			f:    filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			r:    domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess},
			want: false,
		},
		{
			name: "OR within an axis matches either value",
			f:    filter.Filter{Statuses: []domain.Status{domain.StatusQueued, domain.StatusInProgress}},
			r:    domain.Run{Status: domain.StatusInProgress},
			want: true,
		},
		{"approvals badge matches a pending-deployment Run", approvals, domain.Run{Status: domain.StatusWaiting}, true},
		{"approvals badge matches a fork-PR Run", approvals, domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionActionRequired}, true},
		{"approvals badge excludes an ordinary completed Run", approvals, domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess}, false},
		{
			name: "the pair is AND-ed with a scalar axis",
			f:    filter.Filter{Branch: "main", Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			r:    domain.Run{HeadBranch: "dev", Status: domain.StatusCompleted, Conclusion: domain.ConclusionFailure},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.f.Match(c.r); got != c.want {
				t.Fatalf("Match() = %v, want %v", got, c.want)
			}
		})
	}
}
