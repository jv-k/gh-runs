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
// Confirmed is proof of confirmation. Unexported fields, single use.
type Confirmed struct { /* unexported */ }

func (o *Ops) Execute(ctx context.Context, c Confirmed) error
```

A `Confirmed` in a caller's hands proves:

1. **Its set came out of `Plan`**: frozen at modal open (purge R5), eligibility stamped (R10 to R12), friction priced (R7, R8). Both links are unforgeable, so this holds for every Confirmed in existence.
2. **The priced friction was satisfied by an explicit act**: the modal's answer or the CLI's flag, once per invocation, never a stored setting (purge R9).
3. **At most one execution**: it carries a spent flag, and a second `Execute` returns `ErrSpent` and issues nothing.

**Single use is cheap, and the alternative is quietly wrong.** Executing one confirmation twice issues every DELETE twice: mostly 404s the second time, each logged, each spent against the write budget, and none of it anything anyone meant. The flag makes "one act, one operation" a runtime property instead of a habit.

**No expiry, deliberately.** R5 freezes the set, and purge R12 already weighed revalidation and refused it: the API's write-time rejection is the guard, synchronous with the write in a way no check of ours can be. A TTL on `Confirmed` would be a policy no requirement asks for, aimed at a race somebody else already closes.

**Execute's preconditions stay Execute's.** For `OpDelete` it proves the deletion log writable before the first request and writes one line per attempt, skip lines included, from the Item's fields verbatim (purge R29, [ADR-0011](./0011-package-layout-and-dependency-direction.md)). Cancellation is the context, honouring R16. Progress travels as [ADR-0015](./0015-the-async-model.md)'s broadcast, and none of that is this ADR's to fix.

## Considered Options

**A generic `Plan[T]`.** Covered above: Go has no generic methods, so the surface degrades to free functions, and R15's mixed Cache and Artifact selection is unrepresentable without two Plans and two confirmations for one deletion.

**A sealed Item interface.** Three concrete types behind an unexported method. Structurally sound, structurally heavier: every consumer type-switches to reach the tuple the struct simply carries, and the confirm pane still switches for row cells either way.

**An open `Plan` struct.** Covered above: `Confirm` launders it, and R7 and R10 fall back to convention.

**Two rails for the unconfirmed writes.** Covered above: amends ADR-0011's only-request-issuer sentence and reopens the routing question for every future write.

**Bare tuples with rows on the side.** AC22 says the rows are the tuples `Execute` is handed, and parallel collections are the drift that wording exists to forbid.

**A TTL on `Confirmed`.** Covered above: revalidation was already refused where it was cheaper.

**Dispatch, and Workflow enable and disable, in `Operation` now.** Both are stage 11's, their confirmation shape is that fog's to decide, and both are additive when it does: a new `Operation` value, and for the Workflow pair a fourth object pointer on Item. [ADR-0014](./0014-domain-types-and-the-budget-readout.md)'s rule prices that as a diff.

## Consequences

**ADR-0011's property survives with signatures.** `Execute` is still the only request issuer and the only deletion log writer, `tui/confirm` still renders a Plan it cannot forge and collects an Input it cannot fake, and the arrow still runs `tui` to `ops` and never back.

**`domain` gains nothing.** The constructors read exported fields the types already declare. No new method, no new field, no import.

**The confirm pane renders accessors.** `Total`, `Breakdown`, `Friction` and the skip counts are the modal, and R30's viewport is a viewport over `Items()`. The only type switch in `tui/confirm` is row-cell rendering, which is the one per-kind fact a shared component legitimately owns.

**The CLI is the same three calls.** `--dry-run` stops after `Plan` and prints its Items, resolving through the same code path as the real operation with no second implementation ([cli-surface](../features/cli-surface/requirements.md) R10, R20). `--yes` is `Confirm(plan, NonInteractiveYes())`. One resolution, two presentations, exactly as purge R30 words it.

**The friction table is one table-driven test.** Operation, set size, repository span and threshold in, level out, with [settings](../features/settings/requirements.md) R12's clamp exercised at the same seam. Laundering attempts are compile errors rather than review comments, which is this tree's usual trade.
