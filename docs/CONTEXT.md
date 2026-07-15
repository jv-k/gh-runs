# gh-runs

Watching GitHub Actions runs across a person's repositories as they happen, and reclaiming what those runs leave behind.

## Language

### GitHub Actions

**Workflow**:
An automation definition stored as YAML in a repository. Its Runs outlive it. A Workflow whose file is removed enters a `deleted` state rather than disappearing.
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

**Attempt**:
One execution of a Run. Re-running never creates a Run. It adds an Attempt to the Run that already exists.
_Avoid_: Retry, rerun (a re-run is the act. An Attempt is what the act produces.)

**Job**:
A unit of work within an Attempt, executed on a single runner. Carries its own Status and Conclusion.
_Avoid_: Task, stage

**Step**:
One command or Action invocation within a Job.

**Artifact**:
A file bundle uploaded by a Run, retained for a bounded period. An expired Artifact leaves behind a listable record whose bytes are already gone.
_Avoid_: Asset, output, upload

**Cache**:
A keyed blob scoped to a repository and a ref, reused across Runs and evicted by age and size.

**Dispatch**:
Triggering a Workflow by hand, supplying the typed inputs that Workflow declares.
_Avoid_: Trigger, invoke, workflow_dispatch (that's the event's name, not the domain's word)

**Orphaned Run**:
A Run whose Workflow is in the `deleted` state. Its history persists indefinitely, with nothing remaining that could ever produce another.

### gh-runs

**Feed**:
The live, cross-repository list of Runs. This tool's primary surface.
_Avoid_: Dashboard, list, table

**Purge**:
A filtered bulk deletion of Runs, measured in minutes or hours rather than seconds.
_Avoid_: Clean, prune, mass delete

**Reclamation**:
Recovering repository storage by deleting Caches and Artifacts.
_Avoid_: Cleanup, garbage collection

**Budget**:
The share of a person's primary GitHub API rate allowance that this tool is permitted to spend. Only the primary allowance is observable, so only it can be budgeted.
_Avoid_: Quota, rate limit (the rate limit is GitHub's. The Budget is the portion we are allowed.)
