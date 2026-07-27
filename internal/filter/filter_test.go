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

// runIn is a Run stamped with the given repository, for the repository axis's tests.
func runIn(owner, name string) domain.Run {
	return domain.Run{Repo: domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}}
}

// TestUnresolvedThisRepoWidens is the direction ADR-0016 chose, and the reason the marker is
// a bool rather than a sentinel RepoID. A consumer that never calls Resolve must show every
// repository rather than none: settings R19 already made that choice for the same failure on
// the Workflows and Storage tabs, fall back and say so rather than paint an empty view.
//
// A sentinel identity would have failed the other way. It would be structurally valid, match
// no Run, and narrow the Feed to nothing with nothing on screen to say why.
func TestUnresolvedThisRepoWidens(t *testing.T) {
	f := filter.Filter{ThisRepo: true}
	for _, r := range []domain.Run{runIn("cli", "cli"), runIn("jv-k", "gh-runs")} {
		if !f.Match(r) {
			t.Errorf("an unresolved this-repo marker rejected %s: it must widen, not narrow to nothing (ADR-0016)", r.Repo)
		}
	}
}

// TestResolveAddsTheWorkingDirectoryRepository pins Resolve's whole contract: it is pure, the
// argument is the whole of what it is handed, and the resolved identity is an OR member of the
// repository axis with no special case.
func TestResolveAddsTheWorkingDirectoryRepository(t *testing.T) {
	here := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"}

	t.Run("the marker alone narrows to the working directory", func(t *testing.T) {
		f := filter.Filter{ThisRepo: true}.Resolve(here, true)
		if !f.Match(runIn("jv-k", "gh-runs")) {
			t.Error("a resolved marker rejected the working directory's own repository")
		}
		if f.Match(runIn("cli", "cli")) {
			t.Error("a resolved marker admitted another repository")
		}
	})

	t.Run("it ORs with a named entry rather than replacing it", func(t *testing.T) {
		f, err := filter.ParseQuery("repo:this-repo repo:cli/cli")
		if err != nil {
			t.Fatalf("ParseQuery: %v", err)
		}
		f = f.Resolve(here, true)
		for _, r := range []domain.Run{runIn("jv-k", "gh-runs"), runIn("cli", "cli")} {
			if !f.Match(r) {
				t.Errorf("the axis rejected %s: this-repo and a named entry are OR members of one axis", r.Repo)
			}
		}
		if f.Match(runIn("golang", "go")) {
			t.Error("the axis admitted a repository neither member names")
		}
	})

	t.Run("a duplicate collapses", func(t *testing.T) {
		f, err := filter.ParseQuery("repo:this-repo repo:jv-k/gh-runs")
		if err != nil {
			t.Fatalf("ParseQuery: %v", err)
		}
		if got := len(f.Resolve(here, true).Repos); got != 1 {
			t.Errorf("Repos holds %d entries after resolving onto its own name, want 1", got)
		}
	})

	t.Run("no working directory repository leaves the axis alone", func(t *testing.T) {
		f := filter.Filter{ThisRepo: true}.Resolve(domain.RepoID{}, false)
		if len(f.Repos) != 0 {
			t.Errorf("Resolve with no repository added %d entries, want none", len(f.Repos))
		}
		if !f.Match(runIn("cli", "cli")) {
			t.Error("Resolve with no repository narrowed the axis; it must widen (settings R19)")
		}
	})

	t.Run("it does not write through the caller's backing array", func(t *testing.T) {
		// Resolve has a value receiver, so the caller's len can never change and asserting on
		// it proves nothing. The hazard is append writing into a backing array the caller
		// shares: with spare capacity, appending at index len clobbers whatever another slice
		// of the same array holds there. That is the failure this reproduces.
		//
		// It matters because the Feed holds the stated filter and re-resolves it on every
		// view, so an in-place append would accumulate the working directory's repository into
		// the operator's own Repos, once per frame.
		backing := make([]domain.RepoID, 1, 4)
		backing[0] = domain.RepoID{Host: domain.HostGitHub, Owner: "cli", Name: "cli"}
		neighbour := backing[:2]
		sentinel := domain.RepoID{Host: domain.HostGitHub, Owner: "golang", Name: "go"}
		neighbour[1] = sentinel

		f := filter.Filter{ThisRepo: true, Repos: backing}
		got := f.Resolve(here, true)

		if neighbour[1] != sentinel {
			t.Errorf("Resolve wrote %v into the caller's backing array, overwriting %v: it must "+
				"copy before appending, because the receiver's slice is not its own", neighbour[1], sentinel)
		}
		if len(f.Repos) != 1 {
			t.Errorf("Resolve grew the receiver's Repos to %d, want 1", len(f.Repos))
		}
		if len(got.Repos) != 2 || got.Repos[1] != here {
			t.Errorf("the returned Repos = %v, want cli/cli then %v", got.Repos, here)
		}
	})

	t.Run("the stated marker survives resolution", func(t *testing.T) {
		f := filter.Filter{ThisRepo: true}.Resolve(here, true)
		if !f.ThisRepo {
			t.Error("Resolve cleared the marker; the Feed renders the stated filter, not the name it resolved to (ADR-0016)")
		}
		if got, want := f.QueryString(), "repo:this-repo repo:jv-k/gh-runs"; got != want {
			t.Errorf("QueryString() = %q, want %q", got, want)
		}
	})
}
