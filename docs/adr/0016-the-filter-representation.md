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
// pushed without changing the result. It reads Conclusions only to count
// the permissive pair and push a lone Status-or-Conclusion value as
// status=. It never emits a conclusion= parameter, and it never reads Repos.
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

**The axis is spelled `repo:OWNER/REPO` in the filter input's grammar.** This section previously stated the lean above without deciding, and the gap it left ran for a release: the grammar carried no repository token, so the axis could be matched but never stated, and no config key could be added for a setting the Settings view could neither show nor edit ([settings](../features/settings/requirements.md) R17). Issue #102 took the decision.

`Match` plus the grammar are the axis's whole surface. `ParseQuery` accepts `repo:OWNER/REPO` and `repo:HOST/OWNER/REPO` through `domain.ParseRepoRef`, the one validation door ([ADR-0009](./0009-repository-identity-is-host-qualified.md)). Repeated tokens accumulate, OR within the axis. `QueryString` renders the bare `OWNER/REPO` form, which round-trips exactly because `NewRepoID` admits no host but github.com. `Query()` still emits nothing for the axis and cannot, because no such parameter exists.

### The working directory's repository is a marker, not an identity

A named repository is not "the repository of the working directory", and [settings](../features/settings/requirements.md) R19's note makes the second one the Runs tab's equivalent of the scope key the other two tabs carry. `Repos` holds parsed identities, so the unresolved value has nowhere to sit in it. Issue #117 took the decision, and it is a field.

**`Filter` gains one marker beside `Repos`, and a pure method that resolves it.** The marker states that the working directory's repository is part of the axis. The method takes that repository as an argument, as a value and a reported presence, and returns a `Filter` whose `Repos` carries it. Nothing in this package looks the repository up: the argument is the whole of what it is handed, which is the same rule that keeps the `Workflow` selector unresolved here.

**The marker is spelled `repo:this-repo`,** in the input's grammar and in `launch_filter.repos`, the word `workflows_scope` and `storage_scope` already use for the same idea. `ParseQuery` sets the marker, `QueryString` renders it, so a filter carrying it round-trips exactly as a named entry does. The token cannot collide with a repository reference, because `domain.ParseRepoRef` requires two segments and this has one.

**It is an OR member, with no special case.** `repo:this-repo repo:cli/cli` matches either, the resolved identity joins the set, and a duplicate collapses, which is the axis's stated rule applied rather than excepted. Exclusivity was considered and refused: one entry in a list silently annulling the others is a rule no other axis has and every reader would have to remember.

**`Match` ignores an unresolved marker,** so a consumer that never resolves widens rather than narrowing to nothing. That is the direction [settings](../features/settings/requirements.md) R19 already chose for the same failure (fall back and say so, never paint an empty view), and it is the reason the marker is a field rather than a sentinel `RepoID`: a structurally valid identity that matches no Run would fail closed and silently.

**Resolution is the consumer's, at match time, and the stored value stays unresolved.** The Feed takes the same `CurrentRepo func() (domain.RepoID, bool)` seam the Workflows and Storage tabs already take from `main.go`, and resolves where it matches, holding the stated filter for its input line and its filter label. Resolving on adoption instead would rewrite the operator's `repo:this-repo` into a name under them. Resolving into `config.Config` would be worse: that value is what the Settings view edits and saves, so the first save in any directory would write the resolved name over the marker, which is the R17 defect that kept the repository axis out of the file to begin with.

**The say-so obligation travels with the capability.** Where the marker resolves to nothing, the axis contributes nothing and the Feed states the fallback in its filter line, which is R19's rule for the other two tabs applied to the surface that now has their capability.

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

**A third scope key for the Runs tab**, `runs_scope: all-repos | this-repo` beside the other two. Costs this ADR nothing, reuses the wiring the other two tabs already have, and gets R17's round trip free because an enum row is not a text buffer. Refused because it gives the Feed two repository axes that can disagree, where [settings](../features/settings/requirements.md) R19's note already fixes the mechanism as the filter the Feed has and the other two tabs lack.

**A sentinel `domain.RepoID` inside `Repos`.** No new field, one list. Refused because the marker would be a structurally valid identity that no Run ever equals, so an unresolved filter narrows to nothing with no diagnostic, and every consumer of `Repos` (the config writer, the Settings row's renderer, the exclude list) would have to learn to spot it.

**`Repos` as a list of a ref sum type**, each entry an identity or the marker. The most honest modelling of an axis that genuinely holds two kinds of thing. Refused on this ADR's own closing rule: it changes the field three consumers and the engine compile against, and every read site pays the unwrap forever, to buy what one boolean buys.

**Resolving at config load.** Simplest type story, no marker past the loader. Refused because `config.Config` is what the Settings view edits and saves, so the first save writes the resolved name over the marker, in whatever directory the operator happens to be in.

## Consequences

**`filter` stays free of I/O.** Parse, classify, compare. Nothing in the package issues a request, resolves a selector, or holds a list it did not receive as an argument.

**The idempotence contract is testable and load-bearing.** For every axis `Query` pushes, one table-driven test can assert that `Match` agrees with the server's field on the same Run. Any future axis that cannot keep the contract does not get pushed.

**The catalog opened once, as predicted.** [ADR-0015](./0015-the-async-model.md) closed its catalog "until a decision opens it". This is that decision, and the amendment is one field on one event.

**The import table gains one arrow.** `scheduler` may import `filter`. No reversal, no cycle, amended in [ADR-0011](./0011-package-layout-and-dependency-direction.md) in this commit.

**`domain` gains the value lists.** Additive, under ADR-0014's rule that adding what a requirement reads is a diff.

**`cli` stays the thin adapter cli-surface says it is.** Flags fill the struct, `ParseStatus` and `ParseCreated` do the rejecting, and R6's by-name errors come from `filter` for every consumer.

**Axes are additive, semantics are decisions.** A new axis a requirement names is a diff: a field, its `Match` arm, and a `Query` arm if the server honours one. Changing an existing axis's field or matching rule is a change to the contract three consumers and the engine compile against, and earns its way back through here.

**`Filter` now holds a value that is not yet a filter.** The marker is the first field whose meaning depends on something outside the struct, and it is bounded on purpose: one field, one resolution method, `Match` widening where it is unresolved. A second such field would be a decision, not a diff, because the invariant "a `Filter` states its whole meaning" is what the first one spends.

**A tenth field, and the guard test that counts them.** `TestLaunchFilterAxisCountIsDeliberate` asserts `filter.Filter`'s field count against the sub-keys `launch_filter` carries, and the marker rides inside the existing `repos` key rather than adding a tenth. The test's constants have to say so, which is the test doing its job rather than the test being wrong.
