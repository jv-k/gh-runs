package ops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// selectedRuns freezes the named Run IDs as RunItems in one repository, the shape both
// surfaces hand the by-name resolver: the CLI's crawl or positional IDs, and the Feed's
// multi-selection.
func selectedRuns(owner, name string, ids ...int64) []ops.Item {
	out := make([]ops.Item, 0, len(ids))
	for _, id := range ids {
		out = append(out, ops.RunItem(domain.Run{ID: id, Repo: repoID(owner, name), Status: domain.StatusCompleted}))
	}
	return out
}

// TestResolveJobNameAnswersMatchedAndUnmatched pins the by-name resolver's two definite
// answers over one Jobs request per Run: a Run holding the name resolves to an Item
// addressing that Job's own id, and a Run holding no Job of that name becomes an Item-less
// member carrying the Run's tuple and one shared reason (cli-surface R28b, AC14c).
func TestResolveJobNameAnswersMatchedAndUnmatched(t *testing.T) {
	h := newHarness(t, "resolve_job_name", 50, 50)

	res, err := h.ops.ResolveJobsByName(context.Background(), selectedRuns("o", "r", 11, 12, 13), "build")
	if err != nil {
		t.Fatalf("ResolveJobsByName: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("resolved %d Items, want 2", len(res.Items))
	}
	for i, want := range []int64{1101, 1201} {
		it := res.Items[i]
		if it.Kind != ops.KindJob || it.ID != want {
			t.Errorf("Items[%d] = kind %q id %d, want a job Item addressing %d", i, it.Kind, it.ID, want)
		}
		if it.Job == nil || it.Job.Repo != repoID("o", "r") {
			t.Errorf("Items[%d] carries no Job stamped with its repository; the tuple must derive from the object", i)
		}
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0].RunID != 13 || res.Unmatched[0].Repo != repoID("o", "r") {
		t.Fatalf("Unmatched = %+v, want the one Run holding no Job of that name", res.Unmatched)
	}
	if !strings.Contains(res.Unmatched[0].Reason, "build") {
		t.Errorf("unmatched reason %q does not name the absent Job (AC14c)", res.Unmatched[0].Reason)
	}
	if res.Unreached != 0 || res.Reason != "" {
		t.Errorf("resolution reached every Run but reports %d unreached (%q)", res.Unreached, res.Reason)
	}
	if got := h.counting.countMethod("GET"); got != 3 {
		t.Errorf("issued %d Jobs listings, want 3: one per selected Run", got)
	}
}

// TestResolveJobNameOneReasonPerInvocation pins ADR-0019's grouping premise: the reason is
// built once from the name, so groupByReason collapses every unmatched member into one
// group with a count rather than one line per Run (AC14c).
func TestResolveJobNameOneReasonPerInvocation(t *testing.T) {
	h := newHarness(t, "resolve_job_name", 50, 50)

	res, err := h.ops.ResolveJobsByName(context.Background(), selectedRuns("o", "r", 13), "build")
	if err != nil {
		t.Fatalf("ResolveJobsByName: %v", err)
	}
	if len(res.Unmatched) != 1 {
		t.Fatalf("Unmatched = %+v, want one member", res.Unmatched)
	}
	if strings.Contains(res.Unmatched[0].Reason, "13") {
		t.Errorf("unmatched reason %q names the Run; one reason per invocation is what makes the group collapse (AC14c)",
			res.Unmatched[0].Reason)
	}
}

// TestResolveJobNameStopsEarlyAndKeepsUnreachedApart pins R17a: a resolution the API cut
// short freezes what it resolved, and the Runs it never reached are counted separately from
// the unmatched, because those are a missing answer and a definite one. Folding them together
// would put them in Total and price a set larger than the one the operator confirms (AC14d).
func TestResolveJobNameStopsEarlyAndKeepsUnreachedApart(t *testing.T) {
	h := newHarness(t, "resolve_job_name", 50, 50)

	res, err := h.ops.ResolveJobsByName(context.Background(), selectedRuns("o", "r", 11, 12, 14, 13), "build")
	if err != nil {
		t.Fatalf("ResolveJobsByName: %v", err)
	}
	if len(res.Items) != 2 {
		t.Errorf("resolved %d Items, want 2: the frozen set is what the resolution resolved (R17a)", len(res.Items))
	}
	if len(res.Unmatched) != 0 {
		t.Errorf("Unmatched = %+v, want none: a Run the resolution never reached is not a Run holding no Job of that name (R17a)", res.Unmatched)
	}
	if res.Unreached != 2 {
		t.Errorf("Unreached = %d, want 2: the rate-limited Run and the one after it (R17a)", res.Unreached)
	}
	if res.Reason == "" {
		t.Error("a resolution that stopped early states no reason; R17a's note has to name why")
	}
	// The tape holds no interaction past the 403, so a resolver that carried on would fail
	// the replay. Counting the requests states the property directly.
	if got := h.counting.countMethod("GET"); got != 3 {
		t.Errorf("issued %d Jobs listings, want 3: the resolution stops at the first missing answer", got)
	}
}

// TestResolveJobNameRefusesANonRunSelection pins the resolver's one caller-misuse error. It
// resolves a name against Runs, and an Item of any other Kind is a caller handing it a set
// it cannot answer over.
func TestResolveJobNameRefusesANonRunSelection(t *testing.T) {
	h := newOfflineHarness(t, 50, 50)

	_, err := h.ops.ResolveJobsByName(context.Background(),
		[]ops.Item{jobIn("o", "r", 101, 555)}, "build")
	if err == nil {
		t.Error("ResolveJobsByName accepted a set of Job Items; it resolves a name against Runs")
	}
}
