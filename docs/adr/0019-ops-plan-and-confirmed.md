# ops's Plan and Confirmed: the frozen set as a type

[ADR-0011](./0011-package-layout-and-dependency-direction.md) gave `ops` three calls, `Plan`, `Confirm` and `Execute`, and one property: a tab cannot reach the only request-issuing call without a `Confirmed` it cannot construct. [ADR-0014](./0014-domain-types-and-the-budget-readout.md) left the two types to stage 9, owed to [purge](../features/purge/requirements.md) R30 before that stage begins. This ADR fixes them. The semantics below are the requirements', and the decisions here are about which type carries each one.

## One Item, kind-tagged, carrying its own row

```go
package ops

// Kind is the class of object an Item names. The values are exactly
// purge R29's kind column, so a deletion log line is a field copy.
type Kind string

const (
	KindRun      Kind = "run"
	KindLog      Kind = "log"
	KindCache    Kind = "cache"
	KindArtifact Kind = "artifact"
)

// Operation is the verb a Plan was built for. Delete resolves its
// endpoint per Item Kind. The other four act on Runs alone
// (run-lifecycle R16).
type Operation string

const (
	OpDelete      Operation = "delete"
	OpCancel      Operation = "cancel"
	OpForceCancel Operation = "force-cancel"
	OpRerun       Operation = "rerun"
	OpRerunFailed Operation = "rerun-failed"
)

// SkipReason is why Execute will not attempt an Item. Stamped by
// Plan, and the vocabulary is purge R11's and R12's.
type SkipReason string

const (
	SkipNone         SkipReason = ""
	SkipReadOnly     SkipReason = "repository is read-only"
	SkipArchived     SkipReason = "repository is archived"
	SkipNotCompleted SkipReason = "run is not completed"
)

// Item is one member of a frozen set: purge R4's tuple, the Kind,
// and the domain object that is its display row. Exactly one object
// pointer is set, by the constructor that copies the object in.
type Item struct {
	Repo domain.RepoID
	Kind Kind
	ID   int64

	Run      *domain.Run
	Cache    *domain.Cache
	Artifact *domain.Artifact

	// Stamped by Plan. Values a caller sets are overwritten.
	Skip SkipReason
}

func RunItem(r domain.Run) Item
func LogItem(r domain.Run) Item // Kind "log", the Run's id (log-viewer R17)
func CacheItem(c domain.Cache) Item
func ArtifactItem(a domain.Artifact) Item
```

**Kind-tagged rather than generic, and Go's own rules made the choice.** A method cannot take a type parameter, so a generic `Plan[T]` cannot hang off the struct that holds the client, the governor, the clock and the log path, and the whole entry surface becomes free functions taking an `*Ops` first argument. Worse, [storage-reclamation](../features/storage-reclamation/requirements.md) R15 supports "selecting several rows for one deletion" on a tab whose rows are Caches and Artifacts, so one frozen set can legitimately mix kinds, which no single type parameter represents: a mixed selection would need two Plans and two confirmations for what R15 calls one deletion.

**The Item is the row, which is R30 and AC22 satisfied structurally.** AC22 requires the inspect view's rows to be "the same tuples `Execute` is handed". One slice serves both, so there is no parallel structure to drift. The tuple fields ride beside the object rather than behind a type switch because the two consumers that want them bare, `Execute`'s request building and R29's log line, are the two places a switch would otherwise repeat. The constructors derive the tuple from the object, so the pair cannot disagree.

**Constructors copy, because R5's freeze is a memory property.** The Feed's projections are rewritten under every poll. An Item pointing into a tab's live slice is a frozen set in name only, so each constructor takes its object by value and stores a pointer to its own copy. At reference scale that is 18,258 Runs held once, which R30 already priced at megabytes and accepted.

## Amendment: a Job is an Item, and a set holds at most one Job per Run

[run-lifecycle](../features/run-lifecycle/requirements.md) R1 admitted per-Job re-run as a fifth operation (issue #106), against `POST /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun`. The Considered Options below priced this class of addition as a diff, and it is one. `Operation` gains `OpRerunJob`, `Kind` gains `KindJob`, `Item` gains a fifth object pointer, and `JobItem(j domain.Job)` joins the constructors. The friction table gains one row, and `Plan` gains one validation that no other operation needs.

**The Job had to gain a repository before it could be an Item, and that is the load-bearing part.** `domain.Run` and `domain.Workflow` each carry a `Repo`, stamped at fetch, so `RunItem` and `WorkflowItem` derive the tuple from the object and the pair cannot disagree. `domain.Job` carried none, so the obvious constructor takes the repository as a second argument and can be handed one that contradicts its Job. That would make `JobItem` the only constructor of six that the derive-from-the-object rule does not hold for, and the rule exists precisely to make that contradiction unrepresentable. `domain.Job` therefore gains `Repo RepoID` with `json:"-"`, stamped where the Jobs fetch already receives it, and `JobItem` takes one argument like the rest.

**`KindJob` is the second Kind no deletion log line can carry, and the first was already there.** The Kind's stated tie to [purge](../features/purge/requirements.md) R29's kind column reads as though every Kind reaches a log line. `KindWorkflow` broke that when the Workflow toggles arrived: they are PUTs, `deletePath` has no Workflow arm, and no enable or disable writes R29's log. The claim the type actually makes is narrower and still exact: for every Kind that does reach a log line, the line is a field copy. `KindJob` is inside that narrowing rather than a fresh exception to it.

**At most one Job per Run, and the reason is a measurement we are not allowed to take.** `Plan` returns an error for an `OpRerunJob` set holding two Items whose Jobs share a `RunID`. A re-run adds an Attempt, and run-lifecycle R12's measured constraint is that prior Attempts' Jobs are not served, so the second Job id in such a set was read from the Attempt the first request has just superseded. Whether that id still addresses anything is unverified, and [run-lifecycle](../features/run-lifecycle/requirements.md) R28 bars the live write that would settle it. That is run-lifecycle's R28, the one that extends the rule to a live re-run, and not [purge](../features/purge/requirements.md)'s, which stops at the DELETE. Rather than encode a guess, the type refuses the set. Across Runs the interference does not arise, because each Run gains its own Attempt independently, so the constraint is per Run and not a return to single-Item operations.

This is the same move run-lifecycle's resolved open question 5 made for cancel: where the measurement is barred, choose the design whose correctness does not depend on it. The difference is where the refusal sits. Cancel could push the unknown to the API and read the 409, because a wrong guess there costs one skipped Run. Here a wrong guess costs an Attempt nobody asked for, so the refusal is at `Plan` and is a compile-time-adjacent error rather than a runtime reading.

**The friction row is `FrictionNone` on a single Item.** `OpRerunJob` joins `OpRerun` and `OpRerunFailed` in the table's first line, on R18's reasoning applied unchanged: one Job is smaller than one Run, and R18 already exempts one Run. A multi-Item set prices under the existing rules with no special case. What this ADR does not carry is the note the Run detail pane must render alongside the exemption, because a mandated pane element is a requirement rather than a type property, and it lives in run-lifecycle where the operation does.

## Amendment: a frozen set may hold a member that has no Item

[cli-surface](../features/cli-surface/requirements.md) R28b and AC14c fix `--job-name` exactly: one request per Run holding a Job of that name, every other Run reported as a skip naming the absent Job, exit 0. None of the observable behaviour was ever open. What had no owner was the layer that carries a Run which matched nothing, because three things stated above are each right and jointly leave it no seat (issue #161).

`Summary`'s skips are stamped by `Plan` onto Items, so a skip wants an Item. The amendment above makes `Plan` refuse any non-Job Item under `OpRerunJob`, so the Run cannot borrow a `RunItem` to ride in on. And `skipFor` derives every `SkipReason` from the operation, the Item and the repository alone, whereas "no Job of that name in this Run" is derivable from none of the three: it is knowledge only the by-name resolver holds.

**One premise that framed this was narrower than it looked.** The closed vocabulary is the planning one, not the reported one. `Summary.Skips` groups by a free-form `string`, `addSkip` takes a `string`, and `Execute` already records skips whose reasons are in no `SkipReason` constant, including the API's verbatim 403 under a retry bound. So a skip reaching the operator needs no new `SkipReason`. What is closed, and stays closed, is the vocabulary `Plan` stamps onto an `Item`.

**`Plan` gains a second kind of member, and it is not an Item.**

```go
// Unmatched is a member of a frozen set that resolved to no object: R28b's Run
// holding no Job of the named name. It carries the tuple and the reason it will
// be reported under, and no Item, because no Item of a Kind this Operation
// accepts exists for it.
type Unmatched struct {
	Repo   domain.RepoID
	RunID  int64
	Reason string
}

func (o *Ops) Plan(op Operation, sel []Item,
	repos map[domain.RepoID]domain.Repo, unmatched ...Unmatched) (Plan, error)

func (p Plan) Unmatched() []Unmatched // a copy, in resolution order
```

`Plan` returns an error for a non-empty `unmatched` under any operation but `OpRerunJob`. The second kind of member is bounded at the one operation whose resolution can fail to produce an Item, so it cannot spread by being available.

**What this preserves is four invariants, and each of them is one another shape spends.**

1. The `Kind` refusal in `checkOneJobPerRun` stays total. `lifecycleRequest` builds the Job endpoint from `item.ID` without consulting `Kind`, and it keeps relying on `Plan` having refused. A `RunItem` under `OpRerunJob` would POST `/actions/jobs/{runID}/rerun`, a write against whatever Job happens to carry that number, and no path to one exists.
2. `SkipReason` stays closed, because an `Unmatched` rides the free-form reason string `Summary.Skips` already takes.
3. "A value a caller sets is overwritten" stays true of every `Item`. An `Unmatched` is the one member whose reason the caller does author, and it is not an Item, so the rule that makes `Plan`'s stamping worth anything is not weakened by an exception to it.
4. No Item of the wrong Kind exists anywhere in the tree, so there is no path by which one reaches the request builder.

**The accessors count them, and that is what makes the CLI's summary line add up.** `Total` includes them, on the same reading it already takes for the ineligible: purge AC15 counts the skipped inside the 47, and ADR-0019's own "the stamp is per Item, and the set stays one slice" refused dropping a member at Plan time. `Breakdown` includes them, in `RepoCount.Skipped`, because they carry a repository and R6's per-repository split is over the whole set. `Skipped` includes them. `Items` does not, because there is no Item.

**Friction prices them in, and the consequence is stated here rather than left to be rediscovered.** R7's typed count is `Total`, so an operator confirming a by-name set types a number larger than the writes that follow. That is already true of every skipped Item today and C changes nothing about it. Saying so is what stops it being read as a defect the first time somebody counts the requests.

**AC22's claim narrows, exactly as the log line's claim narrowed for `KindWorkflow`.** AC22 requires the inspect view's rows to be "the same tuples `Execute` is handed", and an `Unmatched` row is rendered and never handed to anything. The claim the types actually make is narrower and still exact: every row that names a write is a tuple `Execute` is handed. An `Unmatched` row names the absence of one. This is the same narrowing `KindJob` arrived inside rather than a fresh exception.

**One reason string per invocation, because grouping is what reports them.** `groupByReason` collapses identical reasons into one group with a count, so the resolver builds the reason once from the Job name and stamps every `Unmatched` with it. AC14c's "reports the rest as skips naming the absent Job" is then one group reading `no job named "build" in this run`, with a count, rather than one line per Run.

**`executeSet` seeds the Summary before the walk.** `Summary.Skipped` and `Summary.Skips` take the `Unmatched` through the existing `addSkip` before the first request. The two reporting surfaces that already read `Plan.Items()` and `plan.Skipped()` fold the carrier into what they iterate, rather than merging a second channel into what they print, so `printLifecycleSummary`'s totals sum and `printLifecycleDryRun` renders the unmatched as skipped rows with no second implementation.

**The parameter is variadic, and that is a deliberate small ugliness.** Every existing `Plan` call compiles unchanged, and the one caller that has unmatched members passes them. A fourth positional parameter would put an empty slice at seven call sites that can never have one. A `WithUnmatched` method in `WithDebugLogging`'s shape would have to recompute the friction after pricing, which is the one thing that ADR's unexported fields exist to prevent.

## Amendment: by-name resolution prices what it resolved

The TUI's by-name form must resolve a name to Items before `Plan` can price R17's frozen count, which costs one Jobs request per selected Run, drawn from the Budget, in front of a modal that may be declined. No other lifecycle operation has a request before the frozen count. Issue #148's open point 7 asked what happens when that resolution is rate-limited midway, and this is the ruling.

**The frozen set is what resolution resolved.** A Run resolution never reached is not a member of it. The count the pane shows and the count R7 makes the operator type are both over the resolved portion, and the writes that follow are exactly that number, so R17's guarantee that the number confirmed is the number attempted holds literally.

**Unreached is not `Unmatched`, and the two must not be merged.** An `Unmatched` is a definite answer: this Run was probed and holds no Job of that name. An unreached Run is the absence of an answer. Folding the unreached into `Unmatched` would put them in `Total`, which would price a set larger than the one the operator confirms and undo the ruling above by the back door.

**What this costs is stated plainly, because it is the reason the question was raised.** An operator who selected 40 Runs confirms 12. The set is smaller than the one they named, R7's ladder has no rung that expresses "this count is a lower bound", and nothing in the friction machinery would tell them. So the surface tells them: the pane MUST render a one-line non-blocking note naming how many selected Runs resolution did not reach and why, on R14b's precedent for a mandated note that neither blocks nor confirms. [cli-surface](../features/cli-surface/requirements.md) R16 is what makes the note mandatory rather than a courtesy, because 12 presented unqualified is a count the output cannot stand behind.

**On the CLI the same resolution stopping early exits 1.** R28b's exit 0 covers a name that matched nothing, which is a definite answer and a skip. It does not cover a resolution that stopped early, which is R17's one bit of "not everything happened". The writes for the resolved portion still happen under `--yes`, the summary carries the detail, and no new exit code is minted.

## Plan is unforgeable, and Confirm would launder anything less

```go
// Plan is one frozen, eligibility-stamped, friction-priced set. Its
// fields are unexported, so ops.Plan is its only constructor.
type Plan struct { /* unexported */ }

func (p Plan) Operation() Operation
func (p Plan) Items() []Item // a copy, in selection order
func (p Plan) Total() int    // R6's displayed count, R7's typed count
func (p Plan) Breakdown() []RepoCount
func (p Plan) Friction() FrictionLevel

// RepoCount is one row of R6's per-repository breakdown.
type RepoCount struct {
	Repo    domain.RepoID
	Count   int
	Skipped int
}

func (o *Ops) Plan(op Operation, sel []Item,
	repos map[domain.RepoID]domain.Repo) (Plan, error)
```

**[ADR-0011](./0011-package-layout-and-dependency-direction.md) made `Confirmed` unforgeable and left `Plan` open, and open is a hole.** `Confirm` validates the operator's input against the friction the Plan carries, so a hand-rolled `Plan` of 18,258 Runs priced at `y`/`N` would be laundered into a valid `Confirmed` by the very call that exists to check it. Unexported fields close the hole one link earlier: every `Plan` in existence came out of `ops.Plan`, so R7's pricing and R10's gate are properties of the type rather than of well-behaved callers, and the chain is unforgeable end to end.

**Eligibility arrives as an argument, and an unknown repository fails closed.** `ops` may not import `discovery` ([ADR-0011](./0011-package-layout-and-dependency-direction.md)'s table), and the gate data it needs, `Permissions` and `Archived`, lives on `domain.Repo`, which it may. The caller passes a snapshot map of the repositories the selection touches. An Item whose repository is absent from the map makes `Plan` return an error rather than a guess: not-yet-known keeps destructive actions disabled ([repo-discovery](../features/repo-discovery/requirements.md) R8), and a missing entry is the caller failing to hand over data it holds.

**The stamp is per Item, and the set stays one slice.** Purge AC15 counts the ineligible inside the 47, so dropping them at Plan time is wrong, and splitting eligible from skipped into two slices breaks the selection order R30's viewport shows. `Plan` stamps each Item's `Skip` field instead: `Execute` attempts an Item exactly when the field is empty and writes the R29 skip line from it verbatim otherwise, and the modal's "3 of 47" and the archived call-out ([purge](../features/purge/requirements.md) R11) are counts over the same slice everything else reads.

## One friction table, and None is a level

```go
// FrictionLevel is purge R7's graduated friction, plus the level
// run-lifecycle R18 adds.
type FrictionLevel int

const (
	FrictionNone FrictionLevel = iota
	FrictionYN
	FrictionTypedCount
)
```

`Plan` computes the level from the operation, the set, and the configured threshold, and the table lives in `Plan` alone:

- `OpRerun` and `OpRerunFailed` on a single-Item set price at `FrictionNone` ([run-lifecycle](../features/run-lifecycle/requirements.md) R18).
- A set spanning repositories, or whose total reaches the threshold, prices at `FrictionTypedCount` (purge R7, R8: default 50, clamped at 500, read from `config` at Plan time, [settings](../features/settings/requirements.md) R12 owning both numbers).
- Everything else prices at `FrictionYN`, and `OpDelete` never prices at `FrictionNone` at any size.

**One rail, because two rails re-litigate R9 at every new write.** R18 forbids a modal on single re-run while [ADR-0011](./0011-package-layout-and-dependency-direction.md) says `Execute` is the only call that issues a request, and the reconciliation is a level rather than a bypass. An unconfirmed write is a Plan whose price is nothing: `Confirm(plan, NoInput())` succeeds trivially, the pane opens no modal for `FrictionNone`, and ADR-0011's sentence stays literally true for every write in the product. Direct methods beside the triple would amend that sentence and reopen "which rail" for every write stage 11 adds.

## Confirm's input is an act with a name

```go
// Input is one explicit act of confirmation. Constructors only.
type Input struct { /* unexported */ }

func NoInput() Input
func Answer(s string) Input    // what the modal collected: y, or a typed count
func NonInteractiveYes() Input // cli-surface R11's --yes, and nothing else

func (o *Ops) Confirm(p Plan, in Input) (Confirmed, error)
```

The validation table: `FrictionNone` accepts any Input. `FrictionYN` accepts `Answer("y")` and `NonInteractiveYes`. `FrictionTypedCount` accepts an `Answer` carrying exactly `Total()`'s decimal digits, and `NonInteractiveYes`. Everything else returns `ErrDeclined`, and a declined Confirm changes nothing: the Plan remains valid, the pane may collect again, and zero requests have been issued (purge AC6, AC7).

**`NonInteractiveYes` satisfies every level because [cli-surface](../features/cli-surface/requirements.md) R11 defines the flag as that surface's confirmation**, an explicit act made once per invocation where there is no modal to show, never a skip of one. The constructor's name is what keeps the TUI honest: a tab passing `NonInteractiveYes` is a greppable lie, and purge AC9's zero-request assertions are the tests that catch it. What no Input constructor expresses is a stored setting, which is exactly [settings](../features/settings/requirements.md) R13's line.

## Confirmed proves three things

```go
// Confirmed is proof of confirmation. Unexported fields, and a shared
// spent cell (below) makes it single use even when passed by value.
type Confirmed struct { /* unexported */ }

func (o *Ops) Execute(ctx context.Context, c Confirmed) error
```

A `Confirmed` in a caller's hands proves:

1. **Its set came out of `Plan`**: frozen at modal open (purge R5), eligibility stamped (R10 to R12), friction priced (R7, R8). Both links are unforgeable, so this holds for every Confirmed in existence.
2. **The priced friction was satisfied by an explicit act**: the modal's answer or the CLI's flag, once per invocation, never a stored setting (purge R9).
3. **At most one execution**: it carries a spent cell, a pointer that `Confirm` allocates and every copy of the `Confirmed` shares. The first `Execute` flips it with a compare-and-swap and proceeds. A second finds it already set, returns `ErrSpent`, and issues nothing.

**Single use is cheap, and the alternative is quietly wrong.** Executing one confirmation twice issues every DELETE twice: mostly 404s the second time, each logged, each spent against the write budget, and none of it anything anyone meant. The spent cell makes "one act, one operation" a runtime property instead of a habit.

**The cell is a pointer, and that is the load-bearing part.** `Execute` takes `Confirmed` by value, so a spent `bool` stored inline would flip only the callee's copy, and the next call would find it clear. A caller who copied the `Confirmed` first would launder a second execution whichever way the field was stored. A cell every copy points at is the one shape that survives both, so `Confirm` allocates an `atomic.Bool` and the value carries the pointer. `atomic.Bool` also makes the compare-and-swap safe under a concurrent `Execute`, and the pointer stays unexported like every other field, so only `Confirm` can produce one.

**No expiry, deliberately.** R5 freezes the set, and purge R12 already weighed revalidation and refused it: the API's write-time rejection is the guard, synchronous with the write in a way no check of ours can be. A TTL on `Confirmed` would be a policy no requirement asks for, aimed at a race somebody else already closes.

**Execute's preconditions stay Execute's.** For `OpDelete` it proves the deletion log writable before the first request and writes one line per attempt, skip lines included, from the Item's fields verbatim (purge R29, [ADR-0011](./0011-package-layout-and-dependency-direction.md)). Cancellation is the context, honouring R16. Progress travels as [ADR-0015](./0015-the-async-model.md)'s broadcast, and none of that is this ADR's to fix.

## Considered Options

**A generic `Plan[T]`.** Covered above: Go has no generic methods, so the surface degrades to free functions, and R15's mixed Cache and Artifact selection is unrepresentable without two Plans and two confirmations for one deletion.

**A sealed Item interface.** Three concrete types behind an unexported method. Structurally sound, structurally heavier: every consumer type-switches to reach the tuple the struct simply carries, and the confirm pane still switches for row cells either way.

**An open `Plan` struct.** Covered above: `Confirm` launders it, and R7 and R10 fall back to convention.

**Two rails for the unconfirmed writes.** Covered above: amends ADR-0011's only-request-issuer sentence and reopens the routing question for every future write.

**Bare tuples with rows on the side.** AC22 says the rows are the tuples `Execute` is handed, and parallel collections are the drift that wording exists to forbid.

**A TTL on `Confirmed`.** Covered above: revalidation was already refused where it was cheaper.

**An inline `spent bool` on `Confirmed`.** The obvious form, and it fails silently. `Execute` takes the value, so the flag flips on a copy while the caller's original stays clear, and copying the `Confirmed` before the first call defeats it outright. Recorded because a plain field is what a reader reaches for, and by-value single use needs a shared cell instead.

**A consumed-set on `*Ops`.** `Ops` remembers which `Confirmed`s have run, keyed by a nonce each carries. It works and keeps `Execute` by value, but it moves the guarantee off the type built to carry it and grows unbounded state on `Ops` for what one pointer already gives. Single use stays on `Confirmed`, where the confirmation it proves already lives.

**A `SkipReason` for the Run that matched no Job, carried on an Item.** The obvious shape, and the one issue #161 named first. It keeps the report in one place, which is where AC14c writes it, and it costs three invariants rather than the two the issue charged it: the `Kind` refusal stops being total, `SkipReason` stops being closed, and the caller would have to stamp a `Skip` that `Plan` then has to agree to keep, which is the rule that makes `Plan`'s stamping worth anything. `lifecycleRequest` would have to consult `Kind` rather than rely on a refusal that no longer holds.

**By-name resolution reporting its own skips outside the Summary.** Keeps the `Kind` refusal total and the vocabulary closed, and splits the reporting AC14c puts in one place. It costs four surfaces rather than one merge: `printLifecycleSummary`'s line, whose `%d of %d Runs` trailer is `Total` and would stop summing, the skip groups, `printLifecycleDryRun`'s listing, and that listing's trailer of `Total() - Skipped()`. Then the same four again for the TUI's by-name form. It is also "drop them at Plan time" and "split into two slices", which this ADR's own per-Item stamping paragraph refused for the ineligible.

**Refusing a by-name set whose resolution was rate-limited midway.** Safe, and a large by-name re-run is then defeated by ordinary rate-limit pressure with no partial progress available. Rejected in favour of pricing what resolved and mandating the note, which keeps the partial visible without making the operator's only recourse a retry of the whole set.

**A friction rung for "this count is a lower bound".** The third answer open point 7 offered. It needs a confirm-pane state no other operation has and a fourth `FrictionLevel`, to express something a one-line note expresses without touching the table every write in the product prices against.

**Dispatch, and Workflow enable and disable, in `Operation` now.** Both are stage 11's, their confirmation shape is that fog's to decide, and both are additive when it does: a new `Operation` value, and for the Workflow pair a fourth object pointer on Item. [ADR-0014](./0014-domain-types-and-the-budget-readout.md)'s rule prices that as a diff.

## Consequences

**ADR-0011's property survives with signatures.** `Execute` is still the only request issuer and the only deletion log writer, `tui/confirm` still renders a Plan it cannot forge and collects an Input it cannot fake, and the arrow still runs `tui` to `ops` and never back.

**`domain` gains nothing.** The constructors read exported fields the types already declare. No new method, no new field, no import.

**The confirm pane renders accessors.** `Total`, `Breakdown`, `Friction` and the skip counts are the modal, and R30's viewport is a viewport over `Items()`. The only type switch in `tui/confirm` is row-cell rendering, which is the one per-kind fact a shared component legitimately owns.

**The CLI is the same three calls.** `--dry-run` stops after `Plan` and prints its Items, resolving through the same code path as the real operation with no second implementation ([cli-surface](../features/cli-surface/requirements.md) R10, R20). `--yes` is `Confirm(plan, NonInteractiveYes())`. One resolution, two presentations, exactly as purge R30 words it.

**A frozen set is no longer a slice of Items alone.** Anything that renders a Plan renders two collections, and anything that counts one counts both. `Total`, `Breakdown` and `Skipped` already do it behind their existing signatures, so the surfaces that read those numbers are unaffected. The two that iterate `Items()` directly, the confirm pane's inspect viewport and the CLI's dry-run listing, gain the second collection in what they walk.

**The friction table is one table-driven test.** Operation, set size, repository span and threshold in, level out, with [settings](../features/settings/requirements.md) R12's clamp exercised at the same seam. Laundering attempts are compile errors rather than review comments, which is this tree's usual trade.
