package ops

import (
	"fmt"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// FrictionLevel is purge R7's graduated friction, plus the level run-lifecycle R18
// adds for a single re-run (ADR-0019).
type FrictionLevel int

const (
	FrictionNone FrictionLevel = iota
	FrictionYN
	FrictionTypedCount
)

// RepoCount is one row of R6's per-repository breakdown: the repository, how many
// Items in the frozen set belong to it, and how many of those Plan stamped as
// skipped. The Count parts sum to the Plan's Total (AC1).
type RepoCount struct {
	Repo    domain.RepoID
	Count   int
	Skipped int
}

// Plan is one frozen, eligibility-stamped, friction-priced set. Its fields are
// unexported, so ops.Plan is its only constructor: a hand-rolled Plan of 18,258
// Runs priced at y/N cannot exist, and Confirm cannot launder one into a Confirmed
// (ADR-0019). Every Plan in existence carries R7's pricing and R10's gate as
// properties of the type rather than of a well-behaved caller.
type Plan struct {
	op        Operation
	items     []Item
	friction  FrictionLevel
	breakdown []RepoCount
	debug     bool
}

// Operation is the verb this Plan was built for.
func (p Plan) Operation() Operation { return p.op }

// WithDebugLogging turns on enable_debug_logging for a re-run or re-run-failed Plan,
// the opt-in R14 offers at the point of invocation and AC14 pins (defaulting off). It
// returns a copy so the value stays immutable, and it is meaningful only for the two
// re-run operations: Execute sends the flag on their request bodies and nowhere else,
// so setting it on any other Operation's Plan is inert. It leaves the friction and the
// breakdown untouched, because debug logging changes the request body and not the blast
// radius (run-lifecycle R14, AC14).
func (p Plan) WithDebugLogging() Plan {
	p.debug = true
	return p
}

// DebugLogging reports whether this Plan carries R14's enable_debug_logging opt-in, so
// the confirm modal can echo the choice at the point of invocation (R14).
func (p Plan) DebugLogging() bool { return p.debug }

// Items is the frozen set in selection order, a copy so a caller cannot mutate the
// Plan's held set (ADR-0019). The confirm pane's inspect viewport pages this, and
// Execute is handed exactly these tuples (R30, AC22).
func (p Plan) Items() []Item {
	out := make([]Item, len(p.items))
	copy(out, p.items)
	return out
}

// Total is R6's displayed count and R7's typed count: the whole frozen set,
// including the ineligible, because AC15 counts the skipped inside the 47.
func (p Plan) Total() int { return len(p.items) }

// Breakdown is R6's per-repository split, a copy in first-seen order.
func (p Plan) Breakdown() []RepoCount {
	out := make([]RepoCount, len(p.breakdown))
	copy(out, p.breakdown)
	return out
}

// Friction is the confirmation friction this blast radius prices at (R7, R8).
func (p Plan) Friction() FrictionLevel { return p.friction }

// Skipped is how many Items Plan stamped ineligible, R11's "3 of 47" numerator.
func (p Plan) Skipped() int {
	n := 0
	for i := range p.items {
		if p.items[i].Skip != SkipNone {
			n++
		}
	}
	return n
}

// Plan freezes sel into a Plan: it copies each Item (the freeze is already the
// constructors' by-value copy, R5), stamps each with its eligibility under repos,
// prices the friction, and computes the breakdown. repos is a snapshot of the
// repositories the selection touches; an Item whose repository is absent makes Plan
// return an error rather than guess, because not-yet-known keeps destructive actions
// disabled and a missing entry is the caller failing to hand over data it holds
// (ADR-0019, repo-discovery R8). The threshold is read here, so R7's pricing is a
// property of the returned value (ADR-0019).
func (o *Ops) Plan(op Operation, sel []Item, repos map[domain.RepoID]domain.Repo) (Plan, error) {
	items := make([]Item, len(sel))
	copy(items, sel)
	for i := range items {
		repo, ok := repos[items[i].Repo]
		if !ok {
			return Plan{}, fmt.Errorf("ops: repository %s is not in the eligibility snapshot; refusing to plan a destructive action against an unknown repository", items[i].Repo)
		}
		items[i].Skip = skipFor(op, items[i], repo) // a value the caller set is overwritten (ADR-0019)
	}
	return Plan{
		op:        op,
		items:     items,
		friction:  frictionFor(op, items, o.confirmThreshold),
		breakdown: breakdownOf(items),
	}, nil
}

// skipFor stamps an Item's eligibility (R10, R11, R12). The repository gate runs
// first because it is the more fundamental refusal: an archived or read-only
// repository cannot be written whatever the Run's Status. Archived is distinguished
// from merely read-only because archived is permanent, and its Runs can never be
// cleaned (R11). The Status gate is R12, and applies to a Run deletion alone: a
// Cache or Artifact has no Status, and the not-completed skip is the DELETE-rejects-
// in-progress guard, which is a Run property.
func skipFor(op Operation, it Item, repo domain.Repo) SkipReason {
	if repo.Archived {
		return SkipArchived
	}
	if !repo.Permissions.Push {
		return SkipReadOnly
	}
	if op == OpDelete && it.Kind == KindRun && it.Run != nil && it.Run.Status != domain.StatusCompleted {
		return SkipNotCompleted
	}
	// R9: a deleted Workflow's YAML is gone, so enable and disable have no meaning and
	// neither is offered. The tab refuses to offer the action, and Plan refuses to build
	// one for it too, so R9 is a property of the write path and not only of a well-behaved
	// tab (ADR-0019, workflow-management R9).
	if (op == OpEnable || op == OpDisable) && it.Kind == KindWorkflow && it.Workflow != nil && it.Workflow.State == domain.StateDeleted {
		return SkipDeleted
	}
	return SkipNone
}

// frictionFor prices the confirmation friction (ADR-0019's one table). A single re-run
// prices at None (run-lifecycle R18). A set spanning repositories, or reaching the
// threshold, prices at TypedCount (R7, R8). Everything else prices at YN, and
// OpDelete never reaches None at any size, because it is never a re-run.
func frictionFor(op Operation, items []Item, threshold int) FrictionLevel {
	// Enable and disable are reversible, so the workflow-management canon asks for no
	// confirmation: disabling stops future triggered Runs and cancels none already going,
	// and re-enabling is one keystroke away (R5, R8). They price at None regardless of the
	// set, the same level a single re-run takes for the same reason (run-lifecycle R18).
	if op == OpEnable || op == OpDisable {
		return FrictionNone
	}
	if (op == OpRerun || op == OpRerunFailed) && len(items) == 1 {
		return FrictionNone
	}
	if repoSpan(items) > 1 || len(items) >= threshold {
		return FrictionTypedCount
	}
	return FrictionYN
}

// repoSpan is the number of distinct repositories the set touches. A cross-repository
// set types its count at any size (R7).
func repoSpan(items []Item) int {
	seen := make(map[domain.RepoID]bool)
	for i := range items {
		seen[items[i].Repo] = true
	}
	return len(seen)
}

// breakdownOf builds R6's per-repository breakdown in first-seen order, so the
// modal's rows are deterministic and follow the selection. The Count parts sum to
// the total (AC1), and Skipped counts the ineligible inside each repository's slice
// (R11, AC15).
func breakdownOf(items []Item) []RepoCount {
	index := make(map[domain.RepoID]int)
	var out []RepoCount
	for i := range items {
		id := items[i].Repo
		at, ok := index[id]
		if !ok {
			at = len(out)
			index[id] = at
			out = append(out, RepoCount{Repo: id})
		}
		out[at].Count++
		if items[i].Skip != SkipNone {
			out[at].Skipped++
		}
	}
	return out
}
