# Workflow dispatch: three live measurements

Research note. Measured 2026-07-21 against `jv-k/gh-runs`, a repository the maintainer owns. Unlike the DELETE-status note, these required live writes: each probe added a throwaway `workflow_dispatch` workflow to the default branch (a `workflow_dispatch` trigger is only dispatchable from the default branch), issued one Dispatch, and removed the workflow. One Run was created (by the first probe) and deleted as cleanup. No Run, Cache, or Artifact was deleted beyond that single self-created Run, and no rate limit was probed. Tracked as issues #27, #33, and #34.

The request lines below elide two identifiers that belonged to workflows that no longer exist: `{id}` is each probe's throwaway workflow ID, and `C` is the deleted-workflow probe's add-commit SHA. The response status lines and bodies are recorded verbatim.

## Short answer

1. **`return_run_details: true` returns the Run ID.** A Dispatch with the parameter true returns HTTP 200 carrying `{workflow_run_id, run_url, html_url}`, all three populated. The 204-with-no-Run-ID response is only the parameter-false path.
2. **A disabled Workflow rejects a Dispatch.** A Dispatch against a `disabled_manually` Workflow returns HTTP 422, and no Run is created.
3. **A deleted Workflow cannot be dispatched, though its YAML survives in history.** At a commit predating the deletion the YAML is fetchable (200), but a Dispatch by workflow ID at that same ref is rejected (404), because the Workflow no longer exists to target.

## Measurement 1: return_run_details (issue #27)

Request: `POST /repos/jv-k/gh-runs/actions/workflows/{id}/dispatches`, body `{"ref":"main","return_run_details":true}`.

Response status: `HTTP/2.0 200 OK`.

Response body, verbatim:

```json
{"workflow_run_id":29803635501,"run_url":"https://api.github.com/repos/jv-k/gh-runs/actions/runs/29803635501","html_url":"https://github.com/jv-k/gh-runs/actions/runs/29803635501"}
```

The `workflow_run_id` resolved to a real Run (`event=workflow_dispatch`, `status=completed`, `conclusion=success`, `created_at` matching the dispatch to the second), so it is the dispatched Run's own ID, not a placeholder. The response also carried generic `Deprecation` and `Sunset` headers, plus a `Link` header whose value included a `rel="deprecation"` parameter, all pointing at the api-versions documentation. That is GitHub's standard API-version deprecation notice for the selected version `2022-11-28`, not a deprecation of this endpoint or this parameter.

Consequence: the tool sends `return_run_details: true` on every Dispatch and reads the Run ID from the 200 response. Correlating a Dispatch to its Run by polling is no longer needed. This discharges the canon's own conditional, that if the 200 path worked, [workflow-dispatch](../features/workflow-dispatch/requirements.md) R16 to R19 would delete outright.

## Measurement 2: dispatching a disabled Workflow (issue #33)

Method: added a probe workflow, disabled it (`PUT /repos/jv-k/gh-runs/actions/workflows/{id}/disable`, confirmed state `disabled_manually`), then attempted a Dispatch.

Response status: `HTTP/2.0 422 Unprocessable Entity`.

Response body, verbatim:

```json
{"message":"Cannot trigger a 'workflow_dispatch' on a disabled workflow","documentation_url":"https://docs.github.com/rest/actions/workflows#create-a-workflow-dispatch-event","status":"422"}
```

No Run was created. The 422 is explicit and names the disabled state.

Consequence: dispatching a disabled Workflow is rejected, so reaching a Run from a disabled Workflow is two steps, enable then dispatch, not one. The tool detects the state and surfaces enabling first rather than issuing a Dispatch that will fail.

Caveat on the other disabled states. This measures `disabled_manually`, which is settable via the disable endpoint. `disabled_inactivity` is induced only by roughly 60 days of repository inactivity and cannot be set on demand, so it stays inferred. The endpoint's message says "disabled workflow" without distinguishing the states, so the same 422 is the likely behaviour for `disabled_inactivity`, but that is inference, not measurement.

## Measurement 3: a deleted Workflow at an older ref (issue #34)

Method: added a probe workflow at commit `C`, confirmed it registered active, then deleted the file from the default branch (the deletion is the probe), then measured fetch and dispatch at `C`.

Measurement 3a, fetch the YAML at the old ref: `GET /repos/jv-k/gh-runs/contents/.github/workflows/dispatch-probe-deleted.yml?ref=C` returned `HTTP/2.0 200 OK`, and the decoded body was the workflow YAML. Deletion removes the file from the branch head, not from history.

Measurement 3b, Dispatch at the old ref: `POST /repos/jv-k/gh-runs/actions/workflows/{id}/dispatches` with `ref=C` returned:

Status: `HTTP/2.0 404 Not Found`.

Body, verbatim:

```json
{"message":"Not Found","documentation_url":"https://docs.github.com/rest/actions/workflows#create-a-workflow-dispatch-event","status":"404"}
```

Bonus observation: right after the file deletion, `GET /actions/workflows/{id}` returned 404, and the Workflow was absent from the workflows list scanned to `per_page=100`. This run-less Workflow did not surface an observable `deleted` state, it disappeared. Caveat: this probe's Workflow had zero Runs. A Workflow with Run history may instead persist as state `deleted` so its Runs are not orphaned, and that case is not measured here.

Consequence for [workflow-dispatch](../features/workflow-dispatch/requirements.md) R15: its outcome stands, offer no Dispatch for a `deleted` Workflow, because the Dispatch is rejected (404). Its stated reason is corrected. R15 read that there is no YAML at the head of any branch to fetch. The YAML is in fact fetchable at an older ref (200 above), so unfetchability is not why the Dispatch fails. It fails because the Workflow no longer exists to target: the workflow ID itself 404s, independent of ref.

## What these do not establish

The behaviour of `disabled_inactivity` and `disabled_fork`, neither inducible on demand here. The `deleted` state of a Workflow that carries Run history. The provenance of the community-sourced 65,535-character input limit, which remains unverified and is a separate open question.
