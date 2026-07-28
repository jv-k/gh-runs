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

// Unmatched is a member of a frozen set that resolved to no object: cli-surface R28b's
// Run holding no Job of the named name. It carries the tuple and the reason it will be
// reported under, and no Item, because no Item of a Kind this Operation accepts exists
// for it (ADR-0019, amended).
//
// The Reason is the caller's, and it is the one field in the whole planning surface a
// caller authors. That is not an exception to "a value the caller sets is overwritten",
// because that rule is about an Item and this is not one: "no Job of that name in this
// Run" is derivable from neither the operation, the Item nor the repository, so skipFor
// could not have produced it. It rides the free-form string Summary.Skips already takes,
// so SkipReason stays closed.
//
// A Run the resolution never reached is not one of these. That is the absence of an
// answer where this is a definite one, and run-lifecycle R17a keeps them apart precisely
// so the unreached stay out of Total.
type Unmatched struct {
	Repo   domain.RepoID
	RunID  int64
	Reason string
}

// Plan is one frozen, eligibility-stamped, friction-priced set. Its fields are
// unexported, so ops.Plan is its only constructor: a hand-rolled Plan of 18,258
// Runs priced at y/N cannot exist, and Confirm cannot launder one into a Confirmed
// (ADR-0019). Every Plan in existence carries R7's pricing and R10's gate as
// properties of the type rather than of a well-behaved caller.
type Plan struct {
	op        Operation
	items     []Item
	unmatched []Unmatched
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

// Unmatched is the set's Item-less members in resolution order, a copy so a caller
// cannot mutate the Plan's held set. They append after the Items rather than
// interleaving with them, because interleaving by Run would put a member the operator
// cannot act on between two they can, and the Items keep the selection order among
// themselves, which is what R30's viewport shows (ADR-0019, amended).
func (p Plan) Unmatched() []Unmatched {
	out := make([]Unmatched, len(p.unmatched))
	copy(out, p.unmatched)
	return out
}

// Total is R6's displayed count and R7's typed count: the whole frozen set,
// including the ineligible, because AC15 counts the skipped inside the 47. The
// Item-less members are inside it on the same reading, which is what makes the CLI's
// accepted, skipped and failed figures sum to it (run-lifecycle AC14c).
func (p Plan) Total() int { return len(p.items) + len(p.unmatched) }

// Kind is the object this set holds, or the empty Kind where it is empty or mixes Kinds.
// It exists because the Operation alone cannot name what is happening: a delete over Runs
// is a Purge and a delete over Caches and Artifacts is a Reclamation, and CONTEXT.md holds
// those two words apart deliberately. A surface reading only the verb would label one the
// other. A mixed set gets no single noun rather than an arbitrary one, which is honest:
// Reclamation's list is Caches and Artifacts together (storage-reclamation R7).
func (p Plan) Kind() Kind {
	if len(p.items) == 0 {
		return ""
	}
	kind := p.items[0].Kind
	for i := range p.items {
		if p.items[i].Kind != kind {
			return ""
		}
	}
	return kind
}

// Breakdown is R6's per-repository split, a copy in first-seen order.
func (p Plan) Breakdown() []RepoCount {
	out := make([]RepoCount, len(p.breakdown))
	copy(out, p.breakdown)
	return out
}

// Friction is the confirmation friction this blast radius prices at (R7, R8).
func (p Plan) Friction() FrictionLevel { return p.friction }

// Skipped is how many members of the set Execute will not act on, R11's "3 of 47"
// numerator: the Items Plan stamped ineligible, plus every Item-less member, which is
// a skip by construction and can never become anything else (ADR-0019, amended).
func (p Plan) Skipped() int {
	n := len(p.unmatched)
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
//
// unmatched carries the Item-less members a by-name resolution produced. It is variadic
// so the existing call sites keep passing three arguments: a fourth positional parameter
// would put a nil at every one of them, and all but one can never have such a member.
// The parameter is not free, because a variadic method does not satisfy a non-variadic
// interface declaration and the four Planner declarations gain it too (ADR-0019).
func (o *Ops) Plan(op Operation, sel []Item, repos map[domain.RepoID]domain.Repo, unmatched ...Unmatched) (Plan, error) {
	if err := checkOneJobPerRun(op, sel); err != nil {
		return Plan{}, err
	}
	// The second kind of member is bounded at the one operation whose resolution can fail
	// to produce an Item, so it cannot spread by being available (ADR-0019, amended).
	if len(unmatched) > 0 && op != OpRerunJob {
		return Plan{}, fmt.Errorf("ops: operation %q was handed %d Item-less members; only a "+
			"by-name per-Job re-run resolves to a Run that matched nothing (cli-surface R28b)", op, len(unmatched))
	}
	items := make([]Item, len(sel))
	copy(items, sel)
	for i := range items {
		repo, ok := repos[items[i].Repo]
		if !ok {
			return Plan{}, unknownRepository(items[i].Repo)
		}
		items[i].Skip = skipFor(op, items[i], repo) // a value the caller set is overwritten (ADR-0019)
	}
	// An Item-less member's repository is gated too, on the same fail-closed rule: it is
	// reported to the operator as part of this set, and a repository the caller did not
	// hand over is the caller failing to hand over data it holds (repo-discovery R8).
	frozen := make([]Unmatched, len(unmatched))
	copy(frozen, unmatched)
	for i := range frozen {
		if _, ok := repos[frozen[i].Repo]; !ok {
			return Plan{}, unknownRepository(frozen[i].Repo)
		}
	}
	return Plan{
		op:        op,
		items:     items,
		unmatched: frozen,
		friction:  frictionFor(op, items, frozen, o.confirmThreshold),
		breakdown: breakdownOf(items, frozen),
	}, nil
}

// unknownRepository is the fail-closed refusal both kinds of member take: not-yet-known
// keeps destructive actions disabled, and a missing entry is the caller failing to hand
// over data it holds (ADR-0019, repo-discovery R8).
func unknownRepository(id domain.RepoID) error {
	return fmt.Errorf("ops: repository %s is not in the eligibility snapshot; refusing to plan a destructive action against an unknown repository", id)
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
	// The same rule one Kind over: a re-run of an Orphaned Run has no Workflow left to run
	// (run-detail R18, AC15). It is a skip rather than a refusal of the operation, so the
	// eligible Runs selected beside it still run and the modal states the exclusion in its
	// skip lines, exactly as it does for a read-only repository. Deletion is deliberately
	// not here: an Orphaned Run's Runs are ordinary Runs and stay deletable whatever their
	// Workflow's state (workflow-management R14).
	if (op == OpRerun || op == OpRerunFailed) && it.Kind == KindRun && it.Run != nil && it.Run.WorkflowState == domain.StateDeleted {
		return SkipDeleted
	}
	return SkipNone
}

// checkOneJobPerRun refuses a per-Job re-run set holding two Jobs of the same Run. R14a
// bounds the operation at one Job per Run, R12 makes the second id's fate unverifiable, and
// R28 bars the write that would settle it, so the set is refused rather than half-attempted
// (AC14b).
//
// It is an error rather than a skip because the operator's set came from a name that
// matched twice, and no member of the pair is the one they meant. The message names the
// Run, because that is where they would go to look.
func checkOneJobPerRun(op Operation, sel []Item) error {
	if op != OpRerunJob {
		return nil
	}
	seen := make(map[int64]bool, len(sel))
	for _, it := range sel {
		// A non-Job Item is refused rather than skipped past. lifecycleRequest builds the Job
		// endpoint from item.ID unconditionally, so a RunItem planned under this operation
		// would POST /actions/jobs/{runID}/rerun: a write against whatever Job happens to
		// carry that id. R14a forbids this operation being silently widened, and an Item of
		// the wrong Kind is the widest way to widen it.
		if it.Kind != KindJob || it.Job == nil {
			return fmt.Errorf("ops: a per-Job re-run set holds a %q Item; this operation "+
				"addresses the Job endpoint and can act on nothing else (run-lifecycle R14a)", it.Kind)
		}
		if seen[it.Job.RunID] {
			return fmt.Errorf("ops: the selection holds more than one Job of Run %d; a per-Job "+
				"re-run supersedes the whole Attempt, so re-running two of its Jobs is not a thing "+
				"the API can be asked for (run-lifecycle R14a, AC14b)", it.Job.RunID)
		}
		seen[it.Job.RunID] = true
	}
	return nil
}

// frictionFor prices the confirmation friction (ADR-0019's one table). A single re-run
// prices at None (run-lifecycle R18). A set spanning repositories, or reaching the
// threshold, prices at TypedCount (R7, R8). Everything else prices at YN, and
// OpDelete never reaches None at any size, because it is never a re-run.
//
// It prices the Item-less members in, because R7's typed count is Total and this is
// where Total is priced. The consequence is stated rather than left to be rediscovered:
// an operator confirming a by-name set types a number larger than the writes that
// follow. That is already true of every skipped Item today (ADR-0019, amended).
func frictionFor(op Operation, items []Item, unmatched []Unmatched, threshold int) FrictionLevel {
	total := len(items) + len(unmatched)
	// Enable and disable are reversible, so the workflow-management canon asks for no
	// confirmation: disabling stops future triggered Runs and cancels none already going,
	// and re-enabling is one keystroke away (R5, R8). They price at None regardless of the
	// set, the same level a single re-run takes for the same reason (run-lifecycle R18).
	if op == OpEnable || op == OpDisable {
		return FrictionNone
	}
	if (op == OpRerun || op == OpRerunFailed || op == OpRerunJob) && total == 1 {
		return FrictionNone
	}
	if repoSpan(items, unmatched) > 1 || total >= threshold {
		return FrictionTypedCount
	}
	return FrictionYN
}

// repoSpan is the number of distinct repositories the set touches. A cross-repository
// set types its count at any size (R7). An Item-less member carries a repository, so it
// widens the span exactly as an Item does.
func repoSpan(items []Item, unmatched []Unmatched) int {
	seen := make(map[domain.RepoID]bool)
	for i := range items {
		seen[items[i].Repo] = true
	}
	for i := range unmatched {
		seen[unmatched[i].Repo] = true
	}
	return len(seen)
}

// breakdownOf builds R6's per-repository breakdown in first-seen order, so the
// modal's rows are deterministic and follow the selection. The Count parts sum to
// the total (AC1), and Skipped counts the ineligible inside each repository's slice
// (R11, AC15).
//
// The Item-less members are counted after the Items, in the same first-seen order they
// append in, and each lands in RepoCount.Skipped because it is a skip by construction.
// R6's per-repository split is over the whole set, so leaving them out would give the
// modal a breakdown that does not sum to the count above it (ADR-0019, amended).
func breakdownOf(items []Item, unmatched []Unmatched) []RepoCount {
	index := make(map[domain.RepoID]int)
	var out []RepoCount
	row := func(id domain.RepoID) *RepoCount {
		at, ok := index[id]
		if !ok {
			at = len(out)
			index[id] = at
			out = append(out, RepoCount{Repo: id})
		}
		return &out[at]
	}
	for i := range items {
		rc := row(items[i].Repo)
		rc.Count++
		if items[i].Skip != SkipNone {
			rc.Skipped++
		}
	}
	for i := range unmatched {
		rc := row(unmatched[i].Repo)
		rc.Count++
		rc.Skipped++
	}
	return out
}
