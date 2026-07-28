package ops_test

import (
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// writableRepo is an eligible repository: push and not archived (R10).
func writableRepo(owner, name string) domain.Repo {
	return domain.Repo{ID: repoID(owner, name), Permissions: domain.Permissions{Push: true}}
}

// completedRun is a Run eligible for deletion: Status completed in a writable repo.
func completedRun(id int64, owner, name string) domain.Run {
	return domain.Run{ID: id, Repo: repoID(owner, name), Status: domain.StatusCompleted}
}

// newPlanOps builds an Ops with the given confirm threshold and no transport, for
// the pure Plan/Confirm properties that issue no request.
func newPlanOps(threshold int) *ops.Ops {
	return ops.New(ops.Options{ConfirmThreshold: threshold, BreakerFailures: 50})
}

// snapshot builds the eligibility map Plan takes, keyed by RepoID.
func snapshot(repos ...domain.Repo) map[domain.RepoID]domain.Repo {
	m := make(map[domain.RepoID]domain.Repo)
	for _, r := range repos {
		m[r.ID] = r
	}
	return m
}

// runItems freezes n completed Runs in one repository, ids counting from base.
func runItems(owner, name string, n int, base int64) []ops.Item {
	items := make([]ops.Item, n)
	for i := 0; i < n; i++ {
		items[i] = ops.RunItem(completedRun(base+int64(i), owner, name))
	}
	return items
}

// TestFrictionTable is ADR-0019's one table-driven friction test: operation, set
// size, repository span and threshold in, level out (R7, R8, run-lifecycle R18).
func TestFrictionTable(t *testing.T) {
	oneRepo := snapshot(writableRepo("o", "a"))
	twoRepos := snapshot(writableRepo("o", "a"), writableRepo("o", "b"))

	cases := []struct {
		name      string
		op        ops.Operation
		items     []ops.Item
		threshold int
		repos     map[domain.RepoID]domain.Repo
		want      ops.FrictionLevel
	}{
		{"single-repo below threshold is y/N", ops.OpDelete, runItems("o", "a", 49, 1), 50, oneRepo, ops.FrictionYN},
		{"single-repo at the threshold types the count", ops.OpDelete, runItems("o", "a", 50, 1), 50, oneRepo, ops.FrictionTypedCount},
		{"single-repo above the threshold types the count", ops.OpDelete, runItems("o", "a", 51, 1), 50, oneRepo, ops.FrictionTypedCount},
		{"cross-repo types the count at any size", ops.OpDelete, append(runItems("o", "a", 1, 1), runItems("o", "b", 1, 100)...), 50, twoRepos, ops.FrictionTypedCount},
		{"delete of one is never None", ops.OpDelete, runItems("o", "a", 1, 1), 50, oneRepo, ops.FrictionYN},
		{"single re-run is None", ops.OpRerun, runItems("o", "a", 1, 1), 50, oneRepo, ops.FrictionNone},
		{"single-repo 500 at a clamped-500 threshold types the count", ops.OpDelete, runItems("o", "a", 500, 1), 500, oneRepo, ops.FrictionTypedCount},
		// The fifth operation takes the same row the other two re-runs do: None on a
		// single-Item set, the existing rules otherwise (run-lifecycle R17, R18). R18 names this
		// operation outright: a single-Job re-run takes no confirmation, on the same reasoning
		// that exempts a single Run, and R14b's note is what it carries instead.
		{"single per-Job re-run is None", ops.OpRerunJob, []ops.Item{jobIn("o", "a", 101, 555)}, 50, oneRepo, ops.FrictionNone},
		{"two per-Job re-runs below the threshold is y/N", ops.OpRerunJob, []ops.Item{jobIn("o", "a", 101, 555), jobIn("o", "a", 102, 556)}, 50, oneRepo, ops.FrictionYN},
		{"cross-repo per-Job re-run types the count at any size", ops.OpRerunJob, []ops.Item{jobIn("o", "a", 101, 555), jobIn("o", "b", 102, 556)}, 50, twoRepos, ops.FrictionTypedCount},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := newPlanOps(c.threshold).Plan(c.op, c.items, c.repos)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if p.Friction() != c.want {
				t.Errorf("Friction() = %d, want %d", p.Friction(), c.want)
			}
		})
	}
}

// TestClampIsThreadedThroughConfig pins AC8 end to end: a config setting the
// threshold to 5,000 clamps to 500, so a single-repository frozen set of 500 still
// types its count. The clamp lives in config; Plan reads the clamped value.
func TestClampIsThreadedThroughConfig(t *testing.T) {
	cfg, _ := config.Load(func(string) (string, bool) { return "", false }, config.Flags{})
	// config's default confirm threshold is 50; drive the clamp through a file would
	// need a temp dir, so assert the clamped ceiling directly here and let the config
	// suite own the file path. 500 items at the 500 ceiling must type the count.
	o := ops.New(ops.Options{ConfirmThreshold: cfg.ConfirmThreshold, BreakerFailures: cfg.BreakerFailures})
	p, err := o.Plan(ops.OpDelete, runItems("o", "a", 500, 1), snapshot(writableRepo("o", "a")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Friction() != ops.FrictionTypedCount {
		t.Errorf("500 Runs priced at %d, want TypedCount; friction has a floor (R8, AC8)", p.Friction())
	}
}

// TestEligibilityStampAndSplit pins AC15 and R11: a 47-Run set with 3 ineligible
// (one read-only, one archived, one in-progress) stamps each with its reason,
// keeps all 47 in the set, and reports 3 skipped. Archived is distinguished from
// read-only because archived is permanent (R11).
func TestEligibilityStampAndSplit(t *testing.T) {
	var items []ops.Item
	items = append(items, runItems("o", "ok", 44, 1)...)
	items = append(items, ops.RunItem(completedRun(900, "o", "ro")))   // read-only repo
	items = append(items, ops.RunItem(completedRun(901, "o", "arch"))) // archived repo
	inProg := domain.Run{ID: 902, Repo: repoID("o", "ok"), Status: domain.StatusInProgress}
	items = append(items, ops.RunItem(inProg)) // in-progress in a writable repo

	repos := snapshot(
		writableRepo("o", "ok"),
		domain.Repo{ID: repoID("o", "ro"), Permissions: domain.Permissions{Push: false}},
		domain.Repo{ID: repoID("o", "arch"), Permissions: domain.Permissions{Push: true}, Archived: true},
	)
	p, err := newPlanOps(1000).Plan(ops.OpDelete, items, repos) // high threshold, so friction is not what is under test
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Total() != 47 {
		t.Errorf("Total() = %d, want 47 (the whole frozen set, AC15)", p.Total())
	}
	if p.Skipped() != 3 {
		t.Errorf("Skipped() = %d, want 3 (R11's numerator)", p.Skipped())
	}
	got := skipReasons(p)
	if got[900] != ops.SkipReadOnly {
		t.Errorf("read-only Run stamped %q, want read-only (R10)", got[900])
	}
	if got[901] != ops.SkipArchived {
		t.Errorf("archived Run stamped %q, want archived, distinguished from read-only (R11)", got[901])
	}
	if got[902] != ops.SkipNotCompleted {
		t.Errorf("in-progress Run stamped %q, want not-completed (R12)", got[902])
	}
}

// TestBreakdownSumsToTotal pins AC1: 47 Runs from 3 repositories yield three rows
// whose counts sum to 47.
func TestBreakdownSumsToTotal(t *testing.T) {
	var items []ops.Item
	items = append(items, runItems("o", "a", 20, 1)...)
	items = append(items, runItems("o", "b", 17, 100)...)
	items = append(items, runItems("o", "c", 10, 200)...)
	repos := snapshot(writableRepo("o", "a"), writableRepo("o", "b"), writableRepo("o", "c"))
	p, err := newPlanOps(1000).Plan(ops.OpDelete, items, repos)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	bd := p.Breakdown()
	if len(bd) != 3 {
		t.Fatalf("Breakdown has %d rows, want 3 (AC1)", len(bd))
	}
	sum := 0
	for _, rc := range bd {
		sum += rc.Count
	}
	if sum != p.Total() || sum != 47 {
		t.Errorf("breakdown sums to %d, Total is %d, want both 47 (AC1)", sum, p.Total())
	}
}

// TestPlanFailsClosedOnUnknownRepo pins ADR-0019's fail-closed rule: an Item whose
// repository is absent from the snapshot makes Plan error rather than guess, because
// not-yet-known keeps destructive actions disabled (repo-discovery R8).
func TestPlanFailsClosedOnUnknownRepo(t *testing.T) {
	items := runItems("o", "unknown", 1, 1)
	_, err := newPlanOps(50).Plan(ops.OpDelete, items, snapshot(writableRepo("o", "known")))
	if err == nil {
		t.Fatal("Plan admitted an Item whose repository is not in the snapshot; it must fail closed (ADR-0019)")
	}
}

// skipReasons maps each Item's ID to its stamped SkipReason.
func skipReasons(p ops.Plan) map[int64]ops.SkipReason {
	m := make(map[int64]ops.SkipReason)
	for _, it := range p.Items() {
		m[it.ID] = it.Skip
	}
	return m
}

// orphanRun is a Run whose Workflow was deleted: an Orphaned Run, stamped by the fan-out
// (ADR-0014). Nothing remains that could ever produce another Run from that Workflow.
func orphanRun(id int64, owner, name string) domain.Run {
	r := completedRun(id, owner, name)
	r.WorkflowState = domain.StateDeleted
	return r
}

// TestRerunSkipsAnOrphanedRun pins run-detail R18 and AC15 where every other ineligibility
// already lives. A re-run of a Run whose Workflow is deleted cannot succeed, because there
// is no Workflow left to run, so Plan stamps it skipped and the modal states it in the skip
// lines. Refusing the whole operation instead would drop the eligible Runs selected beside
// it, which is what a batch of five healthy Runs and one orphan must not do.
//
// This is R9's argument one Kind over: the surface refuses to offer the action, and Plan
// refuses to build one for it too, so the rule is a property of the write path and not only
// of a well-behaved tab (ADR-0019).
func TestRerunSkipsAnOrphanedRun(t *testing.T) {
	for _, op := range []ops.Operation{ops.OpRerun, ops.OpRerunFailed} {
		t.Run(string(op), func(t *testing.T) {
			sel := []ops.Item{
				ops.RunItem(completedRun(1, "o", "r")),
				ops.RunItem(orphanRun(2, "o", "r")),
				ops.RunItem(completedRun(3, "o", "r")),
			}
			plan, err := newPlanOps(50).Plan(op, sel, snapshot(writableRepo("o", "r")))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got := plan.Total(); got != 3 {
				t.Errorf("Total() = %d, want 3 (the skipped are counted inside the total)", got)
			}
			if got := plan.Skipped(); got != 1 {
				t.Errorf("Skipped() = %d, want 1 (the Orphaned Run alone)", got)
			}
		})
	}
}

// TestDeleteDoesNotSkipAnOrphanedRun is the boundary. An Orphaned Run's Runs are ordinary
// Runs and stay deletable whatever their Workflow's state (workflow-management R14). The
// exclusion belongs to re-run, which needs a Workflow to run, and to nothing else.
func TestDeleteDoesNotSkipAnOrphanedRun(t *testing.T) {
	sel := []ops.Item{ops.RunItem(orphanRun(1, "o", "r"))}
	plan, err := newPlanOps(50).Plan(ops.OpDelete, sel, snapshot(writableRepo("o", "r")))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Skipped(); got != 0 {
		t.Errorf("Skipped() = %d, want 0; a deleted Workflow's Runs stay deletable (workflow-management R14)", got)
	}
}

// jobIn freezes a Job of the named Run, for the per-Run uniqueness rule.
func jobIn(owner, name string, jobID, runID int64) ops.Item {
	return ops.JobItem(domain.Job{ID: jobID, RunID: runID, Repo: repoID(owner, name), Name: "build"})
}

// TestPlanRefusesTwoJobsOfOneRun pins run-lifecycle R14a's one-Job-per-Run bound at the write path (AC14b). A per-Job re-run
// supersedes the whole Attempt, so re-running two Jobs of one Run is not a thing the API can
// be asked for, and R28 bars the live write that would establish what it does instead.
//
// It is an error rather than a skip because the operator's set came from a name that matched
// twice and no member of the pair is the one they meant. The message names the Run, because
// that is where they would go to look.
func TestPlanRefusesTwoJobsOfOneRun(t *testing.T) {
	o := newPlanOps(50)
	repos := snapshot(writableRepo("o", "r"))

	t.Run("two Jobs of one Run is an error naming the Run", func(t *testing.T) {
		_, err := o.Plan(ops.OpRerunJob, []ops.Item{
			jobIn("o", "r", 101, 555),
			jobIn("o", "r", 102, 555),
		}, repos)
		if err == nil {
			t.Fatal("Plan accepted two Jobs of one Run (R12)")
		}
		if !strings.Contains(err.Error(), "555") {
			t.Errorf("the error does not name the Run: %v", err)
		}
	})

	t.Run("two Jobs of two Runs is fine", func(t *testing.T) {
		if _, err := o.Plan(ops.OpRerunJob, []ops.Item{
			jobIn("o", "r", 101, 555),
			jobIn("o", "r", 102, 556),
		}, repos); err != nil {
			t.Errorf("Plan refused two Jobs of distinct Runs: %v", err)
		}
	})

	t.Run("the rule binds only the per-Job re-run", func(t *testing.T) {
		// Two whole-Run re-runs of the same Run are a different question, and not this
		// rule's. Plan must not start refusing sets it accepted before.
		if _, err := o.Plan(ops.OpRerun, items("o", "r", 1, 1), repos); err != nil {
			t.Errorf("the uniqueness rule leaked onto a whole-Run re-run: %v", err)
		}
	})
}

// TestPlanRefusesANonJobItemUnderTheJobRerun pins run-lifecycle R14a's "MUST NOT be silently
// widened to the whole Run". lifecycleRequest builds the Job endpoint from the Item's id
// without consulting its Kind, so a RunItem planned under this operation would POST
// /actions/jobs/{runID}/rerun: a write against whatever Job happens to carry that number.
// That is the widest possible widening, and it is refused before the wire.
func TestPlanRefusesANonJobItemUnderTheJobRerun(t *testing.T) {
	_, err := newPlanOps(50).Plan(ops.OpRerunJob,
		[]ops.Item{ops.RunItem(completedRun(1, "o", "r"))},
		snapshot(writableRepo("o", "r")))
	if err == nil {
		t.Fatal("Plan accepted a Run Item under a per-Job re-run (R14a)")
	}
	if !strings.Contains(err.Error(), "run") {
		t.Errorf("the error does not name the offending Kind: %v", err)
	}
}

// unmatchedIn builds an Item-less member for a Run that resolved to no Job of the named
// name, the shape ADR-0019's amendment adds (cli-surface R28b).
func unmatchedIn(owner, name string, runID int64, reason string) ops.Unmatched {
	return ops.Unmatched{Repo: repoID(owner, name), RunID: runID, Reason: reason}
}

// TestUnmatchedIsCountedAndBoundedToTheByNameOperation pins ADR-0019's second kind of
// member: it is accepted under OpRerunJob alone, it counts in Total, Breakdown and
// Skipped, and it never enters Items, because there is no Item (cli-surface R28b,
// run-lifecycle AC14c).
func TestUnmatchedIsCountedAndBoundedToTheByNameOperation(t *testing.T) {
	repos := snapshot(writableRepo("o", "a"))
	const reason = `no job named "build" in this run`

	t.Run("counted under a per-Job re-run", func(t *testing.T) {
		p, err := newPlanOps(50).Plan(ops.OpRerunJob,
			[]ops.Item{jobIn("o", "a", 101, 555), jobIn("o", "a", 102, 556)},
			repos,
			unmatchedIn("o", "a", 557, reason), unmatchedIn("o", "a", 558, reason))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if got := p.Total(); got != 4 {
			t.Errorf("Total() = %d, want 4: an unmatched member is inside the frozen count (AC14c)", got)
		}
		if got := p.Skipped(); got != 2 {
			t.Errorf("Skipped() = %d, want 2", got)
		}
		if got := len(p.Items()); got != 2 {
			t.Errorf("len(Items()) = %d, want 2: an unmatched member has no Item", got)
		}
		bd := p.Breakdown()
		if len(bd) != 1 || bd[0].Count != 4 || bd[0].Skipped != 2 {
			t.Errorf("Breakdown() = %+v, want one row counting 4 with 2 skipped (R6)", bd)
		}
		um := p.Unmatched()
		if len(um) != 2 || um[0].RunID != 557 || um[1].RunID != 558 {
			t.Errorf("Unmatched() = %+v, want the two members in resolution order", um)
		}
		um[0].RunID = 0
		if p.Unmatched()[0].RunID != 557 {
			t.Error("Unmatched() returned the held slice, not a copy")
		}
	})

	t.Run("refused under any other operation", func(t *testing.T) {
		for _, op := range []ops.Operation{ops.OpDelete, ops.OpCancel, ops.OpForceCancel, ops.OpRerun, ops.OpRerunFailed} {
			_, err := newPlanOps(50).Plan(op, runItems("o", "a", 1, 1), repos, unmatchedIn("o", "a", 557, reason))
			if err == nil {
				t.Errorf("Plan(%s) accepted an unmatched member; only a by-name per-Job re-run has one", op)
			}
		}
	})
}

// TestFrictionPricesUnmatchedMembers pins ADR-0019's consequence: friction is priced over
// Total, so a by-name set of 9 Items and 3 unmatched prices against 12 (run-lifecycle R17).
func TestFrictionPricesUnmatchedMembers(t *testing.T) {
	repos := snapshot(writableRepo("o", "a"))
	items := make([]ops.Item, 9)
	for i := range items {
		items[i] = jobIn("o", "a", int64(101+i), int64(555+i))
	}
	unmatched := []ops.Unmatched{
		unmatchedIn("o", "a", 601, "r"), unmatchedIn("o", "a", 602, "r"), unmatchedIn("o", "a", 603, "r"),
	}
	// A threshold of 12 sits above the 9 Items and exactly at the frozen total, so the
	// level moves only if the unmatched members are priced in.
	p, err := newPlanOps(12).Plan(ops.OpRerunJob, items, repos, unmatched...)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := p.Friction(); got != ops.FrictionTypedCount {
		t.Errorf("Friction() = %v, want FrictionTypedCount: 9 Items and 3 unmatched price against 12", got)
	}
}
