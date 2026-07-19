# The filter representation: one struct, two projections

[ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed the filter engine's home (`internal/filter`, over `domain` alone) and named its consumers, and [ADR-0014](./0014-domain-types-and-the-budget-readout.md) fixed the types its predicates read while leaving the engine's own shape to stage 5. This ADR fixes the shape. It adds no product decisions: every semantic below is a requirement's, and the decisions here are about where each one lives in a type.

## The type

```go
package filter // imports domain, and nothing else of ours

// Filter is one stated filter over Runs: AND across axes, OR within
// an axis's values. The zero value matches every Run.
type Filter struct {
	Branch   string
	Commit   string
	Actor    string
	Event    string
	Workflow string // the raw selector: a name, a filename, or a numeric ID

	Created DateRange

	// The permissive pair. One -s input parses into exactly one of
	// these two sets, and a Run matches the pair when its Status is
	// in Statuses or its Conclusion is in Conclusions.
	Statuses    []domain.Status
	Conclusions []domain.Conclusion

	// Client-side only, like Conclusions. Empty means every repository.
	Repos []domain.RepoID
}

// Match evaluates the whole Filter against one Run, client-side.
func (f Filter) Match(r domain.Run) bool

// Query emits the server-side half: the query parameters that can be
// pushed without changing the result. It never reads Conclusions or Repos.
func (f Filter) Query() url.Values

// ParseStatus classifies one permissive -s value into the set that owns
// it, and rejects an unrecognised value by name (cli-surface R6).
func (f *Filter) ParseStatus(value string) error

// ParseCreated validates gh's date syntax and returns the range.
func ParseCreated(s string) (DateRange, error)

// DateRange is a parse-validated Created clause. It holds the verbatim
// string for the wire and the typed bounds for Match, both produced by
// the one parse, and its fields are unexported so the pair cannot
// drift. The zero value is no clause.
type DateRange struct { /* unexported */ }
```

**A flat struct, deliberately.** The domain has exactly one disjunction, the Status and Conclusion pair, and it is fixed rather than user-composed, so a pair of typed sets encodes it structurally. A struct is inspectable (the Feed renders its active filters from it, tests compare it with `==` on the scalar axes), and both projections derive from it. Combinator predicates and expression trees buy generality no requirement asks for, and a closure cannot be rendered back into a query string or a filter label.

## The permissive pair is two typed sets, and the parse is a classification

gh's `-s` spans fifteen values, exactly the six Statuses plus the nine Conclusions with nothing left over ([cli-surface](../features/cli-surface/requirements.md) R4), and the sets are disjoint. So one input value belongs to exactly one enum, and `ParseStatus` is a membership lookup: found in one list, appended to that set. Found in neither, rejected by name before any request is built (R6). One input, two typed outputs, and no type anywhere holds a value that might be either, which is what [ADR-0014](./0014-domain-types-and-the-budget-readout.md)'s considered options demanded of exactly this parse.

The pair also carries the one cross-field predicate the canon has. [approvals](../features/approvals/requirements.md) R2's badge matches a Run whose Status is `waiting` or whose Conclusion is `action_required`, and that saved filter is `Filter{Statuses: waiting, Conclusions: action_required}`. The disjunction lives in the type's matching rule, not in a combinator a caller wires.

**The membership lists move to `domain`.** `domain` gains `StatusValues()` and `ConclusionValues()` beside the constants, an additive diff under [ADR-0014](./0014-domain-types-and-the-budget-readout.md)'s own rule. `filter` validates against them and is the single validation point for every consumer: a typo is rejected by the same code with the same message whether it arrived from a flag, the Feed's filter input, or a Purge command.

**Only the enum axes validate.** Branch, commit, actor, workflow and event are free-form and pass through. Event nominally has a known value set, but the set is GitHub's to grow, and a hardcoded list would reject tomorrow's valid event. That is the unknown-member principle [ADR-0014](./0014-domain-types-and-the-budget-readout.md) applied at decode, pointed at input. R6's letter is kept: an axis without an enum of ours has nothing to validate against.

## Match is total, and Query is the pushable half

`Match` evaluates every axis, because a Purge must: it crawls unfiltered past the 1,000 cap and filters entirely client-side ([ADR-0005](./0005-hybrid-filtered-live-unfiltered-purge.md), [cli-surface](../features/cli-surface/requirements.md) R15). `Query` emits the parameters the server honours: `branch`, `actor`, `event`, `created`, `head_sha`, and `status` under the singleton rule below. It never emits a conclusion parameter, because none exists and the API ignores one silently (measured, [live-run-feed](../features/live-run-feed/requirements.md) R23), and it never emits the repository axis, which has no parameter form at all.

**The guarantee that Conclusion never reaches the wire is held by the transport, not by a type.** `Query()` has no code path that reads `Conclusions`, and [cli-surface](../features/cli-surface/requirements.md) AC4 asserts at the counting transport that no request ever carries the parameter. The transport seam is the stronger one: it also catches the regressions a type split could not see, such as a misspelled parameter name.

**The contract that makes one representation serve three consumers: `Match` is idempotent over server-filtered results.** For every axis `Query` pushes, the client predicate reads the same field the server filters on, so applying `Match` to a page the server already filtered changes nothing. The CLI's one-shot `list` builds a request from `Query()` and applies `Match` for whatever stayed client-side. The Feed does the same continuously. A Purge skips `Query()` and runs `Match` over the crawl. Same value, three uses, no second implementation, which is [cli-surface](../features/cli-surface/requirements.md) R20's demand extended to the whole engine.

## The status pushdown is a singleton rule

The API's `status` parameter takes one value per request. The clause is two sets, and two callers legitimately hold more than one value: the approvals saved filter, and any multi-select filter input. The rule: **`Query` emits `status=<v>` exactly when the two sets hold one value between them. Otherwise it emits nothing and the clause rides in `Match`.**

One value is the CLI's only case, since `-s` takes a single value, so `gh runs list -s failure` produces the request gh produces. The multi-value case is already ruled: [live-run-feed](../features/live-run-feed/requirements.md)'s resolved open question 1 weighed a request per value for the approvals predicate and chose client-side evaluation, because the Feed holds both fields and the badge should cost no Budget. Fanning requests is also I/O, and this package has none.

The rule keeps R24 honest for free: a pushed query is never narrower than the Filter, only equal or broader, so the server's `total_count` still bounds the match set from above and the "1,000 of ~18,258" label stays a true upper bound. A per-value fan-out would have made that label a merge problem.

## The server-side half is Query plus an endpoint

There is no `workflow` query parameter on the Run listing. Filtering by Workflow server-side means a different endpoint, `/actions/workflows/{id}/runs`, with the same parameters. So the server-side half of a Filter is `Query()` plus the endpoint the `Workflow` field selects, and the field holds the raw selector because resolving it needs the repository's Workflow list, which is state this package must never hold.

Resolution belongs to the consumer that holds the list. The engine resolves it live, from the Workflow lists it already keeps for the name join ([ADR-0015](./0015-the-async-model.md)). `cli` resolves it one-shot, fetching the list when `-w` is present, which is also where gh's rule that `-w` misses disabled Workflows without `-a` lives. Client-side, the selector matches a Run when it equals the stamped `WorkflowName`, or `WorkflowID` when the selector is numeric, which is gh's own contract for the flag.

## The engine speaks Filter

[live-run-feed](../features/live-run-feed/requirements.md) R22 applies the Feed's filters server-side, and nothing in the TUI schedules a poll ([ADR-0015](./0015-the-async-model.md)). So the scheduler's control surface takes the active `filter.Filter` value: it derives `Query()`, resolves the `Workflow` selector, and re-polls. [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s import table gains the `scheduler` to `filter` edge, a new arrow onto a package that imports `domain` alone, which the table's closing rule prices as cheap. The alternative, handing the engine opaque `url.Values`, would force selector resolution into the view over a rendering projection of the Workflow list, and request shaping back into the TUI, which is the exact motion [ADR-0015](./0015-the-async-model.md) exists to forbid. How the value travels into the engine (constructor, setter, control channel) is implementation below decision grade.

**A filtered listing's event carries the claimed total.** R24's label needs `total_count`, and it exists only in the response the engine consumed. When the active Filter makes a poll a filtered listing, `RunsFetched` carries the reported total alongside the page. [ADR-0015](./0015-the-async-model.md)'s catalog row is amended in place, in this decision's commit.

## Created reads CreatedAt, and a doc said otherwise

The server's `created` parameter filters on `created_at`, and [cli-surface](../features/cli-surface/requirements.md) R2 fixes `--created` as "the date it was created" with semantics identical to gh's. The client half must read the same field, or `Match` breaks its idempotence contract on the one axis where the server was already honest: a re-run diverges `run_started_at` from `created_at` (measured, 3 hours on the one observed re-run), so an `EffectiveStart` predicate would evict Runs the server admitted. **The Created bounds compare against `CreatedAt`, in UTC, everywhere.**

[ADR-0014](./0014-domain-types-and-the-budget-readout.md) wrote that the filter engine's date predicates read `EffectiveStart`. That sentence is amended in the same commit as this ADR: the sort-key claim stands, and the filter claim was a bug by this canon's own rule that a gh flag's semantics may not be quietly redefined. The safety argument for `EffectiveStart` (a Purge sparing a recently re-run old Run) was considered and rejected: R12 already skips every non-completed Run, and R10's `--dry-run` shows the operator every affected Run ID before anything is deleted.

`DateRange` parses at construction, which is where R6's "reject by name, before any request" naturally lives. It keeps the validated verbatim string for the wire, so the server sees exactly what the user typed and what we accepted, and no re-serialisation can shift a boundary. The typed bounds for `Match` come from the same parse, so the two cannot drift.

## The repository axis is client-side scoping made filterable

[live-run-feed](../features/live-run-feed/requirements.md) R3 makes the owning repository a Feed filter. Server-side, repository is not a parameter but the choice of endpoints, and the CLI's `-R` and a Purge's target make that choice before any filter runs, so those consumers leave `Repos` empty. The Feed's filter input is the consumer that needs it: `Repos` matches the stamped `Repo`, OR within the set, and the Feed's one filter surface drives one engine for every axis rather than growing a private repository predicate beside it. The axis has no `Query()` form, exactly like Conclusions.

## One measurement flagged, not taken

Which field the server's `actor` parameter matches is unmeasured: `Actor` and `TriggeringActor` diverge only on a re-run by a different user. `Match` reads `Actor.Login` provisionally. One conditional GET against a re-run Run settles it when stage 5 builds, and the discrepancy is recorded here so it is a known unknown rather than a surprise.

## Considered Options

**Composable predicates** (`func(domain.Run) bool` with `And`/`Or`). Maximum flexibility, but a closure cannot be rendered back into a query string or a filter label, so the server half would need a parallel structure anyway, and the flexibility models disjunctions the domain does not have.

**An expression tree.** A query-string emitter and an evaluator walking an AST. General, inspectable, and far more machinery than seven axes and one fixed disjunction need.

**Compiling into two types**, a `Remote` that can only be a query string and a `Local` that can only match. The split is a fixed fact about the GitHub API, not a per-consumer policy, so the ceremony recurs at every call site while the guarantee it encodes is already held by a stronger seam, AC4's counting transport.

**Fan-out per status value.** One request per value in the pair's sets, unioned. A request per value for a predicate the canon already ruled client-side, and R24's cap label becomes a merge across requests.

**Dates on `EffectiveStart`.** Covered above: breaks idempotence against the server's field, and silently redefines a gh flag R2 forbids redefining.

**`filter` keeps its own enum tables.** Two fifteen-value truths one shelf apart, which is the drift [ADR-0014](./0014-domain-types-and-the-budget-readout.md) exists to prevent.

**Opaque `url.Values` into the engine.** Covered above: resolution and request shaping migrate into the TUI, against [ADR-0015](./0015-the-async-model.md).

## Consequences

**`filter` stays free of I/O.** Parse, classify, compare. Nothing in the package issues a request, resolves a selector, or holds a list it did not receive as an argument.

**The idempotence contract is testable and load-bearing.** For every axis `Query` pushes, one table-driven test can assert that `Match` agrees with the server's field on the same Run. Any future axis that cannot keep the contract does not get pushed.

**The catalog opened once, as predicted.** [ADR-0015](./0015-the-async-model.md) closed its catalog "until a decision opens it". This is that decision, and the amendment is one field on one event.

**The import table gains one arrow.** `scheduler` may import `filter`. No reversal, no cycle, amended in [ADR-0011](./0011-package-layout-and-dependency-direction.md) in this commit.

**`domain` gains the value lists.** Additive, under ADR-0014's rule that adding what a requirement reads is a diff.

**`cli` stays the thin adapter cli-surface says it is.** Flags fill the struct, `ParseStatus` and `ParseCreated` do the rejecting, and R6's by-name errors come from `filter` for every consumer.

**Axes are additive, semantics are decisions.** A new axis a requirement names is a diff: a field, its `Match` arm, and a `Query` arm if the server honours one. Changing an existing axis's field or matching rule is a change to the contract three consumers and the engine compile against, and earns its way back through here.
