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

**R13.** Report the declared limits in the form rather than letting the API reject the payload opaquely: a Workflow declaring more input properties than the maximum, or a submission whose serialised inputs exceed the character limit, must be surfaced by the form. Both limits are UNKNOWN (see Constraints).

**R14.** Gate Dispatch on `permissions.push && !archived` for the repository, and treat the API as the final authority. A 403 can arrive despite `permissions.push: true` because fine-grained PATs expose no scopes.

**R15.** Offer no Dispatch for a Workflow in state `deleted`. There is no YAML at the head of any branch to fetch, so R2's order cannot complete.

**R16.** Report a Dispatch as accepted on a 204 and display no Run ID. The response carries none, and inventing one would be a lie about the central object of this tool.

**R17.** Correlate a Dispatch to its Run on a best-effort basis by polling for Runs of that Workflow with `event=workflow_dispatch` at or after the Dispatch's timestamp, and label the match as probable, never as certain.

**R18.** State the ambiguity when correlation finds more than one candidate. Two people dispatching the same Workflow at once cannot be reliably disambiguated, and the tool must say so rather than pick.

**R19.** Never report a Dispatch as failed because correlation failed or timed out. The 204 already established that it was accepted. Only the linkage is unknown, and the Feed remains the source of truth.

**R20.** Provide a non-interactive Dispatch. [ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md) ships a standalone binary, and a user without `gh` installed has no `gh workflow run` to fall back on. Command and flag spelling belong to [cli-surface](../cli-surface/requirements.md).

**R21.** Render the generated form to a frame from held state alone, with no live terminal and no network, and verify that frame with golden-file tests covering one control of each type R6 declares: free text for `string`, a toggle for `boolean`, a select for `choice` and for `environment`, and numeric entry for `number`. The form is generated from YAML the tool did not write and cannot predict, so the painted frame is the only evidence R6's mapping held. A golden over `cli/cli`'s `deployment.yml` fixes the four-control case this document is specified against, and R10's promise that a `choice` is a select rather than free text is a claim about a widget, which is a thing only a rendering test can check.

## Acceptance criteria

**AC1: The ref comes first.** No form is rendered, and no Contents request is issued, before a ref is selected. Selecting a second ref for a Workflow whose inputs differ between two branches produces two visibly different forms.

**AC2: The measured form generates four typed controls.** Rendering the form for `cli/cli`'s `deployment.yml` produces exactly four controls: `tag_name` as free text marked required, `platforms` as free text pre-filled with its declared default, `release` as a toggle, and `environment` as a select pre-filled with `production` and populated from the repository's environments. Rendering it issues exactly one Contents request and exactly one environments request. Rendering a form for a Workflow declaring no `environment` input issues zero environments requests.

**AC3: Required and `choice` constraints bind.** Submitting that form with `tag_name` empty is refused by the form and issues no request. A `choice` input cannot be submitted with a value outside its `options` by any interaction the form offers.

**AC4: No untyped fallback exists.** A Workflow whose YAML cannot be parsed produces an error naming the ref and the `path`, and no key=value entry surface appears anywhere in the product.

**AC5: Correlation is probable, never certain.** On a 204, the UI reports the Dispatch accepted and shows no Run ID. Where a correlated Run is found it is labelled probable. Where two candidates match the window, the UI states the ambiguity rather than selecting one. Where none is found before the window closes, the Dispatch is still reported as accepted.

**AC6: The gate costs no request.** Dispatch is unavailable for an archived repository and for one where `permissions.push` is false, determined with no API request beyond the repository listing that already ran. A Workflow in state `deleted` offers no Dispatch.

**AC7: Goldens hold the generated form.** Rendering the form for `cli/cli`'s `deployment.yml` from held state, with no terminal and no network, reproduces the stored golden byte for byte: `tag_name` as free text marked required, `platforms` as free text pre-filled with its default, `release` as a toggle, and `environment` as a select pre-filled with `production`. A further golden covers a Workflow declaring `choice` and `number` inputs, rendering a select over the declared `options` and a numeric entry, and one declaring an unrecognised type, rendering free text labelled as unrecognised. Changing any control's type fails its golden.

## Constraints

The Workflow object's keys are **exactly** `badge_url, created_at, html_url, id, name, node_id, path, state, updated_at, url` (measured). There is no `inputs` and no list of declared events. This single fact forces R1, R2 and R5: the YAML is the only source of the input schema, it must be fetched via the Contents API, and it must be fetched **at the target ref**, because inputs can differ per branch.

The real example the form is specified against, from `cli/cli`'s `deployment.yml`:

| Input | Type | Notes |
|---|---|---|
| `tag_name` | `string` | required |
| `platforms` | `string` | has a `default` |
| `release` | `boolean` | n/a |
| `environment` | `environment` | `default: production`. Needs the environments call |

| Fact | Source | Consequence |
|---|---|---|
| `type: environment` requires a separate call to `/repos/{o}/{r}/environments` | Measured | R7. One extra request, and only when the type appears |
| **Dispatch returns 204 with no Run ID** | Measured | R16–R19: correlation is best-effort and **racy by construction** |
| Max 25 input properties. Total payload ≤ 65,535 chars | Research, **not directly verified (UNKNOWN)** | R13 is written to surface the limits, not to trust the numbers |
| Repo permissions and `archived` ride along free on `/user/repos` | PRD | Gating R14 costs nothing |
| Fine-grained PATs expose no `x-oauth-scopes` | PRD | Pre-flight checks are impossible. The API is final authority (R14) |
| Archived repos are permanently read-only | PRD | They can never be dispatched to |
| A 304 costs zero primary rate limit | [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) | R17's correlation poll is nearly free, and a 304 is itself the "no new Run yet" signal |
| Filtered listing caps at 1,000, reaching the **newest** matches | [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) | Harmless for R17: the Run just dispatched is the newest match, which is the end of the list the cap does reach. `total_count` is still not to be trusted |
| 2.0.0 serves github.com only | [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) | n/a |

## Open questions

**UNKNOWN: are the 25-property and 65,535-character limits real?** Both come from research and neither was verified against the API. R13 surfaces them defensively. If they are wrong, R13's thresholds are wrong and the API's rejection is the only true signal.

**UNKNOWN: does the API accept a Dispatch for a Workflow in a `disabled_*` state?** Unmeasured. It decides whether [workflow-management](../workflow-management/requirements.md) should offer "enable, then dispatch" as one flow or two.

**UNKNOWN: is a `deleted` Workflow's YAML still fetchable at an older ref, and would a Dispatch at that ref be accepted?** R15 declines to ask, but the question is real: `deleted` means the file is gone from the head of the branch, not from history.

**UNKNOWN: can Actions-write be separated from `push` for a fine-grained PAT?** R14 gates on `push` as the conservative choice. The question is unanswerable pre-flight regardless, since fine-grained PATs expose no scopes.

**Undecided: what ref does the picker default to?** The current branch when launched inside a repository, or the repository's default branch. The first matches where you already are. The second matches what most Dispatch forms expect.

**Undecided: does the ref picker offer tags as well as branches?** Unasked.

**Undecided: how long is the correlation window, and what happens when a Dispatch queues behind a concurrency group and no Run appears for minutes?** R19 makes a timeout non-fatal, which bounds the damage but does not answer it.

**Undecided: should the last-used inputs for a Workflow be remembered?** It would be the largest single ergonomic win after the typed form itself, and it is state, not intent, so it does not obviously belong in [settings](../settings/requirements.md) (PRD: settings are intent-level only).

## Related

- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md). The standalone-coherence argument behind R20.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). Why R17's poll is affordable.
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). Why the 1,000 cap does not hurt R17.
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Host-qualified repo identity](../../adr/0009-host-qualified-repo-identity.md)
- [workflow-management](../workflow-management/requirements.md) lists the Workflows and the `path` and `state` this form depends on.
- [live-run-feed](../live-run-feed/requirements.md) is where R17's correlated Run appears, and the source of truth when correlation fails.
- [run-detail](../run-detail/requirements.md) is where a correlated Run leads.
- [repo-discovery](../repo-discovery/requirements.md) supplies the free `permissions` and `archived` behind R14.
- [cli-surface](../cli-surface/requirements.md) owns R20's command and flag spelling.
