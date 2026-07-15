# Workflow management

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Constraints cite [PRD.md](../../PRD.md).

## Purpose

List Workflows with their state, across every repository or the one you are in, and enable or disable them. Because Runs outlive the Workflow that produced them, this surface is also the only honest source of Orphaned Runs, the cruft a cleanup tool exists to find.

## Requirements

**R0.** This surface MUST operate under **either scope**, `all-repos` or `this-repo`, and MUST default to `all-repos` ([settings](../settings/requirements.md) R19). Both code paths must exist and both must be correct. `all-repos` is one request per repository over [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)'s existing fan-out, and it is what a tool whose thesis is cross-repo should open with. `this-repo` answers "which Workflows are disabled in the repo I am in", which a rollup answers badly. Every requirement below reads "a repository" as **each repository in scope**, and R1's list MUST carry the owning repository on every row under `all-repos`, exactly as [live-run-feed](../live-run-feed/requirements.md) R3 requires of the Feed and for the same reason.

**R1.** List every Workflow in each repository in scope, showing at minimum its name, its `path`, and its state.

**R2.** Display state using the API's own values and keep the three disabled states distinct from one another. `disabled_manually`, `disabled_inactivity` and `disabled_fork` have different causes and different remedies. Collapsing them to "disabled" destroys the only signal that says why.

**R3.** Render an unrecognised state value verbatim rather than coercing it to a known one. The five observed values are those observed, not a guarantee of the enumeration's shape.

**R4.** Never describe a Workflow's state as its Status, and never describe a Run's or a Job's Status as its state. State belongs to a Workflow. Status and Conclusion belong to a Run and a Job.

**R5.** Enable a Workflow that is disabled, and disable a Workflow that is active.

**R6.** Gate enable and disable on `permissions.push && !archived` for the repository. When the gate fails, the action must be absent or visibly unavailable with the reason stated. Archived repositories are permanently read-only, and that is a different sentence from "you lack push".

**R7.** Treat the API as the final authority on permission. A 403 can arrive despite `permissions.push: true` because fine-grained PATs expose no scopes, so a rejected toggle must be reported as a permission failure, not as a bug.

**R8.** Reflect the API's reported state after a toggle rather than optimistically flipping the displayed value. What the API says the state is, is what the list shows.

**R9.** Offer neither enable nor disable for a Workflow in state `deleted`. Its YAML is gone, so the operation has no meaning. Whether the API would accept the request is UNKNOWN and the tool does not ask.

**R10.** List Workflows in every state by default in the TUI, including all disabled states and `deleted`. Filtering them out would hide exactly the rows this surface exists to show.

**R11.** Surface Workflows in state `deleted`, and label their Runs as Orphaned Runs wherever those Runs appear.

**R12.** Identify Orphaned Runs from Workflow state alone. Never infer them by inspecting the repository for a missing YAML file. A code-search-based discovery misses precisely this case, which is why state is the source of truth.

**R13.** Offer navigation from a `deleted` Workflow to its Runs, presented as a [Feed](../live-run-feed/requirements.md) filtered to that Workflow.

**R14.** Keep a Workflow's Runs listable and, subject to R6's gate, deletable regardless of that Workflow's state. Runs outlive Workflows. A disabled or deleted Workflow's Runs are ordinary Runs.

**R15.** Never present a per-Workflow Run count from a filtered listing as exact. Filtering caps at 1,000 silently while `total_count` keeps reporting the true match count, so a capped count must be labelled as capped.

**R16.** Mirror `gh`'s behaviour in the non-interactive surface: `gh run list -w` does not reach disabled Workflows without `-a`, and ours must not either. This is compatibility, not endorsement. [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) commits to a superset, never a divergence. Exact flag spelling belongs to [cli-surface](../cli-surface/requirements.md).

## Acceptance criteria

**AC1: Each state renders distinctly and verbatim.** A repository with Workflows in several states renders each state distinctly: a `disabled_inactivity` Workflow is never displayed as `disabled_manually` and never as a bare "disabled". A Workflow whose state is a value not in the table below renders that value as text and does not crash or fall back to `active`.

**AC2: The gate costs no request.** For an archived repository, or one where `permissions.push` is false, the enable and disable actions are unavailable with a stated reason, and determining that issues no API request beyond the repository listing that already ran.

**AC3: The API's state is the displayed state.** Toggling a Workflow and re-reading the list shows the state the API reports. A 403 on a toggle leaves the displayed state unchanged and surfaces the failure to the user.

**AC4: A `deleted` Workflow leads to its Orphaned Runs.** A Workflow in state `deleted` appears in the list, exposes neither enable nor disable, and exposes navigation to its Runs. Those Runs are labelled Orphaned Runs in the Feed. Identifying them issues no request against the repository's contents.

**AC5: A capped count is labelled capped.** A per-Workflow Run count derived from a filtered listing that hits the cap is displayed as capped (for example "1,000 of ~18,258") and never as a bare total.

**AC6: State and Status keep their own vocabulary.** Snapshot assertion on vocabulary: the string "Status" never appears against a Workflow row, and the string "state" never appears against a Run or Job row.

**AC7: `-w` matches gh without `-a`.** `gh runs list -w <name>` without `-a` returns the same set of Runs as `gh run list -w <name>` does for the same repository and filter.

## Constraints

The Workflow object's keys are **exactly** `badge_url, created_at, html_url, id, name, node_id, path, state, updated_at, url` (measured). There is no Run count, no reference to a latest Run, and no `inputs`. Every enrichment this surface might want costs a separate request, and any Run-derived enrichment inherits the 1,000-result cap (R15).

| `state` | Meaning | Action offered |
|---|---|---|
| `active` | The Workflow runs on the events it declares | Disable |
| `disabled_manually` | Turned off by a person | Enable |
| `disabled_inactivity` | Turned off by GitHub for inactivity | Enable |
| `disabled_fork` | Not enabled in this fork. Never observed, and possibly unreachable through this endpoint: a fork with Actions un-enabled serves an empty list instead (see Open questions) | UNKNOWN (see Open questions) |
| `deleted` | The YAML file is gone. Its Runs persist forever | Neither (R9) |

The list is described as inclusive, not exhaustive, which is why R3 exists.

| Fact | Source | Consequence |
|---|---|---|
| Runs outlive their Workflow. A `deleted` Workflow's Runs persist indefinitely | [CONTEXT.md](../../CONTEXT.md), *Orphaned Run* | R11–R14: the cleanup case this tool exists for |
| Repo permissions and `archived` ride along free on `/user/repos` | PRD | Gating R6 costs nothing |
| Archived repos are permanently read-only | PRD | Their Workflows can never be toggled and their Runs can never be cleaned |
| Fine-grained PATs expose no `x-oauth-scopes` | PRD | Pre-flight checks are impossible. R7 |
| Any filter caps listing at 1,000 while `total_count` reports 18,258 | [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) | R15, "the tool never lies about counts" (PRD success criterion 6) |
| No cross-repository Run query exists | [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md) | A cross-repo Workflow view would be a fan-out too (see Open questions) |
| `gh run list -w` excludes disabled Workflows without `-a` | gh's behaviour, per [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) | R16 |
| 2.0.0 serves github.com only | [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) | Repo identity is `host/owner/name` here as everywhere |

## Open questions

**UNKNOWN: can a `disabled_fork` Workflow be enabled by the same operation as `disabled_manually`?** Still unmeasured, and the state now looks unreachable through this endpoint. `disabled_fork` is in the official enum and was never observed across ~80 forks. The decisive finding is what a fork with Actions un-enabled actually serves: `{"total_count": 0, "workflows": []}`, despite carrying 8 to 24 workflow YAML files on disk (`lee-dohm/cli` and `openCEC/CEC-IDE` among them). The fork case arrives as an **empty list**, not as a `disabled_fork` row, so the table's UNKNOWN row may never paint at all. R3 is what carries this either way: the enumeration is inclusive rather than exhaustive, an unrecognised state renders verbatim, and a state that never arrives needs no handling. That is why the question does not block, and why it should not be answered by disabling Actions on a fork to find out.

**UNKNOWN: is the `state` enumeration exhaustive?** The five values are the ones observed. R3 makes the answer non-blocking.

**UNKNOWN: does disabling a Workflow affect its in-progress Runs?** Unmeasured. It decides whether the disable confirmation needs to say anything about Runs already going.

**UNKNOWN: does a Workflow's `updated_at` or ETag change when its state is toggled elsewhere?** If it does, conditional revalidation ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)) makes a live Workflow list nearly free, the same way it does for the Feed. If it does not, the list is refresh-on-demand.

**UNKNOWN: how common are Orphaned Runs in the reference account?** The reference user has 163 repositories, ~26 with Runs (PRD). Whether any of them carries a `deleted` Workflow decides whether R11–R13 is a headline cleanup win or a rare curiosity, and there is currently no measured answer.

**Resolved: both, and the person chooses. Cross-repository is the default.** The fan-out is affordable, being one request per repository over the machinery [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md) already builds, and a cross-repo list is what a tool whose thesis is cross-repo should open with. But "which Workflows are disabled in the repo I am in" is a real question a rollup answers badly, so `this-repo` stays available as a scope rather than a tab. [settings](../settings/requirements.md) R19 owns the setting and defines what `this-repo` resolves to.

**R0 now carries this into the requirements.** This resolution said "both code paths must exist" while R1 and the Purpose line specified one, describing a single repository throughout. An implementer reads the requirements first and the open questions last, if at all, so the decision has to live in R0 or it does not live anywhere. The scope also changes R1's row shape, because a cross-repo list must name the owning repository on every row.

**Undecided: does the CLI surface extend to `gh workflow`'s commands?** ADR-0008 enumerates `gh run`'s flags only, but its standalone-coherence argument applies just as well here: a Homebrew user without `gh` installed has no `gh workflow enable` to fall back on. Owned by [cli-surface](../cli-surface/requirements.md).

## Related

- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md)
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). The 1,000 cap behind R15.
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md). The `-a` behaviour in R16.
- [ADR-0009: Host-qualified repo identity](../../adr/0009-host-qualified-repo-identity.md)
- [workflow-dispatch](../workflow-dispatch/requirements.md) is the sibling action on the same object. Its form needs the `path` this surface lists.
- [live-run-feed](../live-run-feed/requirements.md) is where Orphaned Runs are labelled and R13 navigates to.
- [purge](../purge/requirements.md) is what R14's Runs are ultimately for.
- [repo-discovery](../repo-discovery/requirements.md) supplies the free `permissions` and `archived` behind R6.
- [cli-surface](../cli-surface/requirements.md) owns R16's flag spelling.
