# gh-runs

Watching GitHub Actions runs across a person's repositories as they happen, and reclaiming what those runs leave behind.

## Language

### GitHub Actions

**Workflow**:
An automation definition stored as YAML in a repository. Its Runs outlive it.
_Avoid_: Action, pipeline (an Action is a reusable Step. A pipeline is another product's word.)

**Run**:
One invocation of a Workflow, and the central object of this tool.
_Avoid_: Build, execution, workflow run (redundant here, because Run is unambiguous)

**Status**:
Where a Run is in its lifecycle: `queued`, `in_progress`, `completed`, `waiting`, `requested`, `pending`. A separate field from Conclusion.
_Avoid_: State (State belongs to a Workflow)

**Conclusion**:
How a Run ended: `success`, `failure`, `cancelled`, `skipped`, `timed_out`, `neutral`, `action_required`, `stale`, `startup_failure`. Null until Status reaches `completed`.
_Avoid_: Result, outcome, and above all Status. Status and Conclusion are two different fields, and conflating them is the defining bug of the tools that came before this one.

**State**:
A Workflow's lifecycle value: `active`, `disabled_manually`, `disabled_inactivity`, `disabled_fork`, `deleted`. A Workflow has a State. A Run, Job or Step has a Status and a Conclusion. Three different fields, and the API never shares one across them.
_Avoid_: Status (Status is a Run's, never a Workflow's), enabled, disabled (those name two of five values, not the field).

**Attempt**:
One execution of a Run. Re-running never creates a Run, only another Attempt on the one that already exists.
_Avoid_: Retry, rerun (a re-run is the act. An Attempt is what the act produces.)

**Job**:
A unit of work within an Attempt, executed on a single runner. Carries its own Status and Conclusion.
_Avoid_: Task, stage

**Step**:
One command or Action invocation within a Job.

**Artifact**:
A file bundle uploaded by a Run, retained for a bounded period. An expired Artifact becomes a Tombstone.
_Avoid_: Asset, output, upload

**Tombstone**:
An expired Artifact. The listable record survives, the bytes are already gone, and deleting it reclaims nothing. It still reports its original `size_in_bytes`, which is the number Reclamation must not believe.
_Avoid_: Expired artifact (say Tombstone, because the point is that it looks deletable and is not), ghost, stale artifact.

**Cache**:
A keyed blob scoped to a repository and a ref, reused across Runs and evicted by age and size.

**Dispatch**:
Triggering a Workflow by hand, supplying the typed inputs that Workflow declares.
_Avoid_: Trigger, invoke, workflow_dispatch (that's the event's name, not the domain's word)

**Orphaned Run**:
A Run whose Workflow file has been removed, leaving that Workflow in a `deleted` state rather than gone. Its history persists indefinitely, with nothing remaining that could ever produce another.

### gh-runs

**Feed**:
The live, cross-repository list of Runs. This tool's primary surface.
_Avoid_: Dashboard, list, table

**Purge**:
A filtered bulk deletion of Runs, measured in minutes or hours rather than seconds.
_Avoid_: Clean, prune, mass delete

**Crawl**:
Walking the Run list unfiltered to enumerate every Run and filtering client-side, the only way past the 1,000-result cap a filtered listing hits silently. A Purge crawls. The Feed does not.
_Avoid_: Scan, paginate, list (a plain list trusts the server's filter and stops at 1,000).

**Frozen set**:
The exact Runs a destructive operation will act on, captured the instant its confirmation opens and unchanged by anything the Feed does afterward. A filter is only how the set was resolved, never what the operation reads.
_Avoid_: Selection, batch (those keep changing. A Frozen set does not.)

**Capability**:
What the token may do in a given repository, recorded per repo from its `permissions` and `archived` fields. A tri-state, never a boolean: permitted, refused, or not yet known. The third is not the absence of the first two. A repo whose Runs have listed has not thereby proven its Capability, and destructive actions stay disabled until enumeration returns.
_Avoid_: Permission (that is GitHub's field. Capability is our recorded tri-state over it), access, can-delete.

**Gate**:
The advisory pre-check that hides a destructive action on a repo lacking `push` or marked `archived`. Advisory is the whole point: it is never a guarantee, the API is the final authority, and a 403 can still arrive on a repo the Gate passed.
_Avoid_: Guard, permission check (it reads a Capability, but it decides nothing the API cannot overturn).

**Reclamation**:
Recovering repository storage by deleting Caches and Artifacts.
_Avoid_: Cleanup, garbage collection

**Local store**:
This tool's own on-disk record of what it has already seen: ETags, last-seen response bodies, discovery results, kept under the XDG cache directory ([ADR-0017](adr/0017-the-local-store-on-disk-contract.md)). Derived, never authoritative, and always safe to delete.
_Avoid_: Cache (a Cache is a GitHub Actions Cache, the thing Reclamation deletes. The store is never called one, and that is the entire reason Cache is defined so narrowly.), database, state file.

**Budget**:
The share of a person's primary GitHub API rate allowance that this tool is permitted to spend. A policy the person sets, and an **input**.
_Avoid_: Quota, rate limit (the rate limit is GitHub's. The Budget is the portion we are allowed.), Budget state (see below)

**Budget Readout**:
What the rate governor publishes about the account's rate limiting at a moment: remaining primary allowance, the reset or resume time, whether consumption is under pressure, and whether it is exhausted, which a secondary-limit backoff also sets ([ADR-0018](adr/0018-the-fanout-concurrency-shape.md)). An **observation**, and never a policy.
_Avoid_: Budget state (it names the Readout with the Budget's word, and the two are opposites: one is what we are allowed to spend, the other is what is left. Code that conflates them will eventually throttle on the wrong number.)

**Pressure**:
The condition where the current burn rate would exhaust the remaining primary allowance before it resets: `remaining / burn < time_to_reset`. A predicate, not a percentage, and the rate governor is its sole authority. Zero burn is never Pressure, however low the remaining count.
_Avoid_: Load, congestion, running low (a small remaining count is not Pressure if nothing is spending it).

**Correlation**:
The best-effort linking of a Dispatch to the Run it triggered, when the trigger returns no Run ID and the link must be inferred from event type and timestamp. Probable, never certain: a correlated Run is our best guess, not a fact the API confirmed. **Superseded for 2.0.0**: `return_run_details: true` returns the Run ID in the Dispatch response (measured, #27), so 2.0.0 does not correlate. The term is preserved only for the deferred [notifications](./features/notifications/requirements.md) feature (2.1), whose R6 was built on it and which will be revisited when 2.1 is built.
_Avoid_: Match, association (those imply a certainty Correlation deliberately withholds).

**Baseline**:
The first observation of a repository's Runs in a session. It fires no notifications, so launching to a night of finished Runs does not announce them all at once. Notifications describe what changed against the Baseline, never the Baseline itself. Notifications are deferred to 2.1 ([PRD](./PRD.md) Scope), so this term is preserved for them: the Baseline is the silent first read they will measure against.
_Avoid_: Initial state, snapshot (Baseline is specifically the silent first read).
