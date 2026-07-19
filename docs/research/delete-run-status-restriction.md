# Deleting a workflow run: which Statuses does the API reject?

Research note. Retrieved 2026-07-19. Documentation and source reading only. **No DELETE was issued**, per the standing rule that no test and no probe may issue a live DELETE.

## Short answer

**GitHub documents no Status restriction at all.** The OpenAPI description for `DELETE /repos/{owner}/{repo}/actions/runs/{run_id}` declares exactly one response, `204`, and its prose reads only "Deletes a specific workflow run" plus a write-access note. The rendered documentation page adds a generic `403` and `500` and still says nothing about a Run's Status. There is no documented `409`, and no documented statement that a Run must be `completed` before it can be deleted.

The only public evidence that a restriction exists at all sits in gh's own client, which handles a `409 Conflict` from this endpoint. Its message asserts a reading that cannot be correct.

So the answer is a documented absence. The restriction is real, its exact Status set is unknown, and [purge](../features/purge/requirements.md) R12 stays conservative on the record rather than by omission.

## Evidence

### The OpenAPI description documents one response

`github/rest-api-description`, dereferenced `api.github.com` description, path `/repos/{owner}/{repo}/actions/runs/{run_id}`, `delete`:

- summary: "Delete a workflow run"
- description: "Deletes a specific workflow run. Anyone with write access to the repository can use this endpoint. If the repository is private, OAuth tokens and personal access tokens (classic) need the `repo` scope to use this endpoint."
- documented responses: `204` only

No `409`. No mention of Status, state, or in-progress Runs.

### The rendered docs add two generic codes and no restriction

The REST reference page for Delete a workflow run documents `204`, `403` and `500`, with no note about run state and no guidance on deleting a Run that has not completed.

### gh handles a 409, and its message is inverted

`cli/cli`, `pkg/cmd/run/delete/delete.go`, trunk:

```go
err = deleteWorkflowRun(client, repo, fmt.Sprintf("%d", run.ID))
if err != nil {
    var httpErr api.HTTPError
    if errors.As(err, &httpErr) {
        if httpErr.StatusCode == http.StatusConflict {
            err = fmt.Errorf("cannot delete a workflow run that is completed")
        }
    }
    return err
}
```

**That message cannot be right.** Deleting completed Runs is the entire purpose of `gh run delete`, and it is what this product does at a scale of tens of thousands. If a `409` meant "the Run is completed", the command would fail on every ordinary use of it. The likely intent is "cannot delete a workflow run that is **not** completed", which agrees with R12's exclusion and with the canon's existing claim that DELETE rejects a Run still in progress.

Two further observations. The handler is **untested**: `delete_test.go` carries no Conflict case. And a search of `cli/cli` surfaced no issue reporting the wording.

## What this does not establish

The `409`'s exact trigger set. `queued`, `waiting`, `requested` and `pending` are each "not completed", so the inferred rule would reject all four. Inference is not measurement, and nothing here measures it. This project cannot measure it either, because doing so means issuing a live DELETE against a real Run. It stays unknown by policy rather than by neglect.

## Consequence for the canon

R12 stands unchanged: exclude every Status other than `completed`. What changes is its footing. It was conservative because the answer was unknown. It is now conservative because the answer is undocumented, the one public implementation that touches it describes it wrongly, and the measurement is forbidden here. That is a stronger position, and a citable one.
