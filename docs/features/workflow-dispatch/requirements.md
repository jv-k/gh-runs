# Workflow dispatch

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Constraints cite [PRD.md](../../PRD.md).

## Purpose

Dispatch a Workflow from a typed form generated from that Workflow's YAML at the ref you are about to run. Not having to remember what a Workflow's inputs are, or which values are legal, is the whole reason to reach for a TUI instead of `gh workflow run`.

## Requirements

**R1.** Resolve a Workflow's dispatchability and its input schema by parsing its YAML. The Workflow object carries neither the events it declares nor its inputs, so nothing about the form can be inferred from it.

**R2.** Follow a fixed order: pick the ref, fetch the YAML at that ref, parse it, then render the form. No form may be rendered before a ref is chosen, because a Workflow's inputs can differ per branch.

**R3.** Re-fetch and re-render the form whenever the ref changes. The form on the screen must always be the form for the ref that will run.

**R4.** Display the target ref alongside the inputs at all times. Which ref a Dispatch will run must never be ambiguous.

**R5.** Fetch the YAML at the target ref using the Workflow's `path` from the Workflow object.

**R6.** Generate one typed control per declared input, by the input's declared type:

| YAML `type` | Control | Options from |
|---|---|---|
| `string` | Free text | n/a |
| `boolean` | Toggle | n/a |
| `choice` | Select | the input's `options` |
| `number` | Numeric entry | n/a |
| `environment` | Select | the repository's environments |

**R7.** Populate every `environment` select from the repository's environments, fetched separately, and fetch them at most once per form render however many `environment` inputs the Workflow declares. A form with no `environment` input must not fetch them at all.

**R8.** Pre-fill each input with its declared `default`, show its `description`, and mark inputs declared `required`.

**R9.** Refuse to submit until every `required` input has a value, and issue no request while refusing.

**R10.** Never permit a `choice` input to carry a value absent from its declared `options`. The control is a select. Free text is not a fallback.

**R11.** Render an input whose declared type is unrecognised as free text, labelled as unrecognised, rather than blocking the Dispatch. An unknown type is the Workflow author's business, not a reason to make the Workflow undispatchable.

**R12.** Never fall back to a flat key=value entry surface when the YAML cannot be fetched or parsed. Report an explicit failure naming the ref and the `path`. Degrading to untyped entry would restore exactly the memory work the typed form exists to remove.

**R13.** Report the declared limits in the form rather than letting the API reject the payload opaquely: a Workflow declaring more input properties than the maximum, or a submission whose serialised inputs exceed the character limit, must be surfaced by the form. The two limits do not carry equal weight, and the form must not pretend they do. **25 inputs is authoritative**: GitHub's own OpenAPI spec carries `maxProperties: 25` on this endpoint's `inputs`, so the schema enforces it. **65,535 characters is community-sourced and unverified**, absent from the official REST documentation for this endpoint, and must be surfaced as such rather than attributed to GitHub (see Constraints).

**R14.** Gate Dispatch on `permissions.push && !archived` for the repository, and treat the API as the final authority. A 403 can arrive despite `permissions.push: true` because fine-grained PATs expose no scopes.

**R15.** Offer no Dispatch for a Workflow in state `deleted`. A Dispatch by workflow ID is rejected with a 404 whatever ref it names (measured), because the Workflow no longer exists to target. The YAML itself survives in history and is fetchable at an older ref, so the barrier is the missing Workflow, not missing YAML. See [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md).

**R16.** Send `return_run_details: true` on every Dispatch, and report the Run from the 200 response's `workflow_run_id`. The response carries the Run ID (measured), so a Dispatch resolves to a known Run with no inference. The 204-with-no-Run-ID response is only the `return_run_details: false` path, which the tool never takes. See [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md).

**R17 through R19 (retired).** The Dispatch-to-Run correlation poll, its probable-not-certain label, and its ambiguity handling are superseded by R16's `return_run_details` path, which returns the Run ID directly. Their numbers are retired rather than reused. Correlation survives only as the deferred [notifications](../notifications/requirements.md) feature's 2.1 starting point, defined in [CONTEXT.md](../../CONTEXT.md) under *Correlation*.

**R20.** Provide a non-interactive Dispatch, and surface the Run ID R16 returns. [ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md) ships a standalone binary, and a user without `gh` installed has no `gh workflow run` to fall back on. Command and flag spelling belong to [cli-surface](../cli-surface/requirements.md).

**R21.** Render the generated form to a frame from held state alone, with no live terminal and no network, and verify that frame with golden-file tests covering one control of each type R6 declares: free text for `string`, a toggle for `boolean`, a select for `choice` and for `environment`, and numeric entry for `number`. The form is generated from YAML the tool did not write and cannot predict, so the painted frame is the only evidence R6's mapping held. A golden over the checked-in `deployment.yml` fixture fixes the five-control case this document is specified against, and R10's promise that a `choice` is a select rather than free text is a claim about a widget, which is a thing only a rendering test can check.

**R22.** Offer no direct Dispatch for a Workflow in a disabled state. A Dispatch to a disabled Workflow is rejected with a 422 (measured), so the tool surfaces enabling the Workflow first rather than issuing a Dispatch that will fail. Enabling is [workflow-management](../workflow-management/requirements.md) R5's operation, which makes reaching a Run two steps, not one. See [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md).

**R23.** Default the ref picker to the repository's default branch, for every repository. This matches `gh workflow run` ([ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md)), it is the only ref where a `workflow_dispatch` is guaranteed present, and it is consistent whether the tool was launched inside the target repository or not, where a current-branch default has no meaning cross-repo. R4 keeps the chosen ref visible and R3 re-renders on a change, so selecting a working branch instead is one action.

**R24.** Offer both branches and tags in the ref picker, labelled distinctly, with tags fetched lazily and at most once per picker session. `gh`'s `--ref` accepts a branch or a tag, so the non-interactive Dispatch (R20) resolves a tag ref regardless, and a picker offering only branches would make the interactive surface a strict subset of the CLI. A repository with no tags shows branches alone.

**R25.** Remember a Workflow's last-used inputs and pre-fill them on the next Dispatch of that Workflow. The values persist in the [local-store](../local-store/requirements.md) R2a, keyed by the host-qualified repository and the Workflow `path`, never in [settings](../settings/requirements.md), which holds intent rather than captured state (settings R2). This is read-back cache, not the job-state [ADR-0006](../../adr/0006-stateless-bulk-jobs.md) forbids, whose line is reading job-state rather than the filesystem. On recall, pre-fill each input from its remembered value only where that value still validates against the current ref's schema: the input still exists, a `choice` value is still among its `options` (R10), and a `required` input stays required (R9). Anything that no longer fits falls back to the declared `default` (R8). The store is derived and safe to delete (local-store R11), so a lost cache costs only the pre-fill. Remembering is on by default, and no 2.0.0 setting governs it. An opt-out would be intent-level and can be added later without reworking the mechanism.

## Acceptance criteria

**AC1: The ref comes first.** No form is rendered, and no Contents request is issued, before a ref is selected. Selecting a second ref for a Workflow whose inputs differ between two branches produces two visibly different forms.

**AC2: The form generates one typed control per declared input.** Rendering the form for a **held YAML fixture** produces exactly one control per input the YAML declares, of the type R6's table maps, and no others. Rendering it issues exactly one Contents request, and exactly one environments request when the YAML declares an `environment` input. A fixture declaring no `environment` input issues zero environments requests.

**The count comes from the fixture, never from a live third-party file.** An earlier form of this criterion asserted that `cli/cli`'s `deployment.yml` declares "exactly four controls". It declares **five**: `tag_name`, `environment`, `platforms`, `release`, and `dry_run`, which upstream added after the measurement. The criterion was falsified by someone else's commit, which is the failure mode of pinning an acceptance test to a repository we do not control. The fixture is checked in. `cli/cli` is where it came from, not what it tests against.

**AC3: Required and `choice` constraints bind.** Submitting that form with `tag_name` empty is refused by the form and issues no request. A `choice` input cannot be submitted with a value outside its `options` by any interaction the form offers.

**AC4: No untyped fallback exists.** A Workflow whose YAML cannot be parsed produces an error naming the ref and the `path`, and no key=value entry surface appears anywhere in the product.

**AC5: The Run ID comes from the response.** On a 200, the UI reports the Dispatch accepted and shows the Run ID from `workflow_run_id`, linked to its Run. The tool sends `return_run_details: true` on every Dispatch, so it never depends on the 204 path and never labels a Run as probable or states a correlation ambiguity.

**AC6: The gate costs no request.** Dispatch is unavailable for an archived repository and for one where `permissions.push` is false, determined with no API request beyond the repository listing that already ran. A Workflow in state `deleted` offers no Dispatch, and a disabled Workflow offers enabling first rather than a direct Dispatch (R22).

**AC7: Goldens hold the generated form.** Rendering the form from the held `deployment.yml` fixture, with no terminal and no network, reproduces the stored golden byte for byte: `tag_name` as free text marked required, `platforms` as free text pre-filled with its default, `release` and `dry_run` as toggles each pre-filled with their declared `true`, and `environment` as a select pre-filled with `production`. A further golden covers a Workflow declaring `choice` and `number` inputs, rendering a select over the declared `options` and a numeric entry, and one declaring an unrecognised type, rendering free text labelled as unrecognised. Changing any control's type fails its golden.

**AC8: The ref picker defaults safely and offers tags.** Opening the form pre-selects the repository's default branch, whatever branch the working directory is on and whether or not the tool is inside the target repository. The picker lists both branches and tags, distinguishable from one another, and a repository with no tags lists branches only.

**AC9: Remembered inputs reconcile with the live schema.** After a Dispatch of a Workflow, re-opening its form pre-fills each input with the value last submitted, except where the current ref's schema no longer admits it: an input the schema dropped is absent, a `choice` value no longer among its `options` falls back to the declared `default`, and a `required` input is still enforced (R9). Deleting the local-store returns every input to its declared `default`.

## Constraints

The Workflow object's keys are **exactly** `badge_url, created_at, html_url, id, name, node_id, path, state, updated_at, url` (measured). There is no `inputs` and no list of declared events. This single fact forces R1, R2 and R5: the YAML is the only source of the input schema, it must be fetched via the Contents API, and it must be fetched **at the target ref**, because inputs can differ per branch.

The real example the form is specified against, copied from `cli/cli`'s `deployment.yml` **into a checked-in fixture**. It is a snapshot of a file we do not own, and it drifts: this table read four inputs until upstream added a fifth.

| Input | Type | Notes |
|---|---|---|
| `tag_name` | `string` | required |
| `environment` | `environment` | `default: production`. Needs the environments call |
| `platforms` | `string` | has a `default` |
| `release` | `boolean` | `default: true` |
| `dry_run` | `boolean` | `default: true`. Added upstream after the original measurement |

Five inputs, five controls, three of R6's five types. Neither `choice` nor `number` appears here, which is why AC7 carries a second fixture for those.

| Fact | Source | Consequence |
|---|---|---|
| `type: environment` requires a separate call to `/repos/{o}/{r}/environments` | Measured | R7. One extra request, and only when the type appears |
| **Dispatch with `return_run_details: true` returns 200 with `{workflow_run_id, run_url, html_url}`** | Measured (#27) | R16: the Run ID comes from the response, so there is no correlation poll. The 204 is only the `return_run_details: false` path |
| **A Dispatch to a disabled Workflow is rejected with 422** | Measured (#33) | R22: enable then dispatch is two steps, not one |
| **A deleted Workflow's YAML is fetchable at an older ref, but a Dispatch there is rejected with 404** | Measured (#34) | R15: the barrier is the missing Workflow, not missing YAML |
| **Max 25 input properties** | GitHub's official OpenAPI spec, `maxProperties: 25` on `inputs` | R13 reports 25 as authoritative. The schema enforces it, so the API's rejection is predictable rather than opaque |
| Total payload ≤ 65,535 chars | Community discussion 120093. **Not in the official REST docs for this endpoint (UNVERIFIED)** | R13 reports it labelled community-sourced. The official spec carries 65,535 for gist comments, check-run output and advisory descriptions, never for Dispatch inputs |
| Repo permissions and `archived` ride along free on `/user/repos` | PRD | Gating R14 costs nothing |
| Fine-grained PATs expose no `x-oauth-scopes` | PRD | Pre-flight checks are impossible. The API is final authority (R14) |
| Archived repos are permanently read-only | PRD | They can never be dispatched to |
| 2.0.0 serves github.com only | [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) | The `return_run_details` 200 path is available at API version 2022-11-28, so R16 needs no fallback |

## Open questions

**Resolved for the 25-input limit: it is real, and it is machine-enforced.** GitHub's official OpenAPI spec carries `maxProperties: 25` on this endpoint's `inputs` object, so the schema itself refuses a 26th. R13 now states it as authoritative rather than defensively.

**Resolved: the 65,535-character limit is community-sourced and unverified, and R13 treats it as such.** The two limits arrived together as "research" and only one survived being checked. 65,535 is not in the official REST documentation for this endpoint. The official spec carries that number for gist comments, check-run output and advisory descriptions, and never for Dispatch inputs. It traces to community discussion 120093 rather than to documentation. R13 surfaces it as community-sourced and unverified rather than attributing it to GitHub, enforces nothing on it, and leaves the API's rejection as the only true signal for it. The 25-input limit is the machine-enforced one, per the Constraints table.

**Resolved: `return_run_details` works, and R16 to R19 collapse (#27).** A live dispatch with the parameter `true` returned 200 carrying `{workflow_run_id, run_url, html_url}`, all three populated, the id resolving to the dispatched Run. R16 now sends the parameter and reads the Run ID from the response, and R17 to R19 are retired: the correlation poll, its probable-not-certain label, and its ambiguity case are gone rather than hedged. This was the largest single simplification available to this feature. Verbatim in [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md), and the PRD's constraints table records the same. Correlation survives only as the deferred [notifications](../notifications/requirements.md) feature's 2.1 starting point.

**Resolved: no, a disabled Workflow rejects a Dispatch with 422 (#33).** So [workflow-management](../workflow-management/requirements.md) offers "enable, then dispatch" as two flows, not one, and R22 carries the rule. This measures `disabled_manually`. `disabled_inactivity` is not inducible on demand and stays inferred. Verbatim in [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md).

**Resolved: the YAML is fetchable at an older ref, the Dispatch there is not (#34).** At a commit predating the deletion the YAML returns 200, but a Dispatch by workflow ID at that ref is rejected with 404, because the Workflow no longer exists to target. R15's outcome stands and its rationale is corrected accordingly. A run-less deleted Workflow also vanishes from the Actions API rather than showing a `deleted` state, though a Workflow with Run history was not measured. Verbatim in [docs/research/workflow-dispatch-measurements.md](../../research/workflow-dispatch-measurements.md).

**Resolved: it cannot be separated pre-flight, so R14 gates on `push` and treats the API as final authority.** Fine-grained PATs expose no scopes, so nothing this tool can read before a request distinguishes Actions-write from `push`. R14 therefore takes `push` as the conservative gate and lets a 403 at Dispatch time be the true signal, exactly as it already treats a 403 that arrives despite `permissions.push: true`.

**Resolved: the repository's default branch, always (R23).** It matches `gh workflow run`, it is the only ref where a `workflow_dispatch` is guaranteed dispatchable, and it avoids an inside-versus-outside-a-repository inconsistency, since a cross-repo Dispatch has no current branch. The chosen ref stays visible (R4), and switching to a working branch is one action.

**Resolved: yes, branches and tags (R24).** `gh`'s `--ref` accepts either, so the non-interactive path resolves a tag regardless, and a branches-only picker would make the interactive surface a subset of the CLI. Tags are fetched lazily.

**Resolved as moot: there is no correlation window.** `return_run_details` returns the Run ID in the Dispatch response (#27), so nothing polls and nothing waits. A Dispatch that queues behind a concurrency group still returns its Run ID immediately.

**Resolved: yes, remembered in the local-store (R25).** It is the largest ergonomic win after the typed form, and it is read-back cache rather than forbidden job-state, since [ADR-0006](../../adr/0006-stateless-bulk-jobs.md) draws its line at reading job-state rather than at disk. So it lives in the local-store (R2a) beside window state, not in settings, which holds intent rather than captured state (settings R2). Recall reconciles each remembered value against the current ref's schema. Remembering is on by default with no 2.0.0 setting, an opt-out being intent-level and addable later.

## Related

- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md). The standalone-coherence argument behind R20.
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Host-qualified repo identity](../../adr/0009-host-qualified-repo-identity.md)
- [workflow-management](../workflow-management/requirements.md) lists the Workflows and the `path` and `state` this form depends on.
- [live-run-feed](../live-run-feed/requirements.md) is where the dispatched Run appears, and the source of truth for Run state.
- [run-detail](../run-detail/requirements.md) is where the dispatched Run leads.
- [repo-discovery](../repo-discovery/requirements.md) supplies the free `permissions` and `archived` behind R14.
- [local-store](../local-store/requirements.md) persists R25's remembered inputs (its R2a), and is safe to delete.
- [settings](../settings/requirements.md) holds intent, not R25's captured inputs, which is why they live in the local-store (settings R2).
- [cli-surface](../cli-surface/requirements.md) owns R20's command and flag spelling.
