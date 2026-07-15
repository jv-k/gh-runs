# gh-runs 2.0.0: Product Requirements

> Terms in this document are defined in [CONTEXT.md](./CONTEXT.md). Decisions are recorded in [adr/](./adr/).

## What this is

**A live GitHub Actions dashboard across your repositories, where deletion is one operation.**

That framing is the single most important sentence here, because it is not what version 1 was. v1 was a bash script that piped a filtered run list into `fzf --multi` and deleted whatever you selected. v2 keeps that capability and subordinates it: the primary surface is a Feed that updates itself as Runs are invoked anywhere, by anyone.

## The problem

`gh` covers the one-shot operations well: `gh run list`, `view`, `watch`, `rerun`, `cancel`, `download`, and `gh workflow run`. It has no answer for the things this tool exists to do.

- **No multi-select and no bulk action.** `gh run delete` takes exactly one ID. `gh run view` single-selects.
- **No cross-repository view.** Everything is one repo at a time.
- **No live surface.** `gh run watch` follows a single Run you already know about.
- **No storage answer.** Nothing tells you that Caches are consuming 12.8 GB while you worry about Artifacts consuming 80 MB.

The naive workaround for bulk deletion exists and mostly works:

```sh
gh run list --status failure -L 100 --json databaseId -q '.[].databaseId' | xargs -n1 gh run delete
```

It runs near the secondary rate limit on a fast connection, has no retry, silently stops at 1,000 results when filtered, and fails on in-progress Runs. Those four gaps are what justify code. The listing is not.

## Who it's for

People who own or contribute to enough repositories that Actions activity stops being observable by hand, and who accumulate Runs, Caches and Artifacts faster than they clean them. The reference user has **163 repositories, roughly 26 with Runs**. The reference repository has **28,707 Runs** and **12.8 GB of Caches**.

## Constraints that shape everything

These are empirical, measured against the live API. Every one of them changed a design decision.

| Constraint | Consequence |
|---|---|
| **No cross-repo Run query exists.** Not in REST, not in GraphQL. `Repository` has no workflow field; `search` doesn't cover Runs; `WorkflowRun` in GraphQL is only reachable via `CheckSuite` and lacks Status and Conclusion entirely. | Fan out one request per repository and merge client-side. gh-dash's "section = search query" model is unavailable. → [ADR-0003](./adr/0003-multi-repo-via-client-side-fanout.md) |
| **Conditional requests are free.** A 304 consumes zero primary rate limit; a 200 consumes one. Verified by interleaved measurement. | Polling ~26 repos every few seconds costs approximately nothing while idle. This is what makes a live dashboard affordable. → [ADR-0004](./adr/0004-conditional-polling-for-liveness.md) |
| **Filtered listing silently caps at 1,000.** `total_count` still reports 18,260; page 11 returns `[]` with no error. Unfiltered listing reaches ~27k+. | Never trust `total_count` in a filtered view. Live Feed filters server-side and labels honestly; a Purge crawls unfiltered. → [ADR-0005](./adr/0005-hybrid-filtered-live-unfiltered-purge.md) |
| **DELETE costs 5 points against ~900/min**, while GitHub's prose separately advises ≥1s between writes. A 3× disagreement. | ~180/min ceiling against ~60/min advice. Purging 18,260 Runs takes 100 minutes at best, 5 hours at worst. Bulk deletion cannot be a modal. → [ADR-0007](./adr/0007-adaptive-delete-throttle.md) |
| **Prior Attempts' Jobs are not served.** `/runs/{id}/attempts/1/jobs` returns `total_count: 0`. | Attempt history is not buildable. Attempt is a badge, never a view. |
| **`created_at` ≠ `run_started_at` on re-runs.** Identical on 8/8 normal Runs; 3 hours apart on a re-run. | Sort the Feed by `run_started_at`: as stable as `created_at`, but a re-run surfaces where you can see it. |
| **Caches dwarf Artifacts.** `cli/cli` holds 12.8 GB of Caches (exact, one free request) against roughly 80 MB of Artifacts. 10% of sampled Artifacts were already expired, and deleting an expired Artifact reclaims nothing. | Storage is one bytes-first Reclamation view led by Caches, not two browsers. |
| **Cache totals are exact and free; Artifact totals are neither.** `/actions/cache/usage` returns `active_caches_size_in_bytes` to the byte in one request. Artifacts have no equivalent: the ~80 MB above is **extrapolated from 100 of 557** and is an estimate, not a measurement. | Per success criterion 6, an Artifact total must either be summed from a full enumeration or labelled an estimate. `active_caches_count` doubles as a truth oracle for the Cache list. |
| **The secondary rate limit is not observable.** Headers expose `x-ratelimit-*` for the primary limit only; `/rate_limit` lists `core`, `graphql`, `search`, `code_search` and friends, and nothing for the secondary limit. | There is no counter to read. The governor's ramp is open-loop and the only feedback is the 403 that means we already overshot, which is why headroom and backoff are the whole control mechanism. It also means the Budget (a share of the *primary* limit, spent by polling) and a Purge's throughput (bound by the *secondary* limit) are different currencies. → [ADR-0007](./adr/0007-adaptive-delete-throttle.md) |
| **Repo permissions ride along free.** `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request. | Gate destructive actions per repo at zero cost. Archived repos are permanently read-only: their Runs can never be cleaned. |
| **Fine-grained PATs expose no scopes.** `x-oauth-scopes` exists only for classic tokens. | Pre-flight permission checks are impossible for fine-grained tokens. The API is always the final authority; a 403 can arrive despite `push: true`. |
| **Dispatch inputs live only in YAML.** The Workflow object carries `path`, never `inputs`. `type: environment` needs a separate `/environments` call. | A typed Dispatch form must fetch and parse the YAML at the target ref. |
| **Dispatch returns 204 with no Run ID.** | Correlating a Dispatch to its Run is best-effort polling on `event=workflow_dispatch` plus a timestamp, and is racy by construction. |
| **Live log streaming does not exist.** Logs are a zip (per Run) or plain text (per Job), delivered on completion. | Job and Step *status* can be live. Log *content* cannot. |

## Scope

### In

| Capability | Notes |
|---|---|
| Live Feed | Cross-repo, auto-discovered, tiered polling, stable ordering |
| Run detail | Jobs and Steps; Attempt as a badge |
| Purge | Filtered bulk deletion, resumable by re-running the filter |
| Run lifecycle | Cancel, force-cancel, re-run, re-run failed Jobs |
| Log viewer | Per-Job, folded by `##[group]`, timestamps stripped by default |
| Workflow management | List, enable, disable |
| Dispatch | Typed form generated from the Workflow's YAML |
| Reclamation | Caches and Artifacts, bytes-first |
| Approvals | Badge and saved filter over the Feed; approve and review inline |
| Notifications | OS-native, gated in Settings, conservative default |
| Settings | Intent-level only |
| CLI | Drop-in superset of `gh run`'s flags |

### Out

- **GHES / multi-host.** Repo identity is host-qualified from day one so this is additive later, but 2.0.0 serves github.com and rejects other hosts explicitly. → [ADR-0009](./adr/0009-host-qualified-repo-identity.md)
- **Webhooks.** They need a public endpoint. Irrelevant to a local TUI.
- **Live log tailing.** Not possible. The API has no streaming endpoint.
- **Attempt history.** Not possible. Prior Attempts' Jobs return empty.
- **npm as a distribution channel for v2.** A Go binary on npm means a postinstall that downloads a platform binary. That is an anti-pattern which breaks behind proxies and offline. v1.0.7 stays published and functional, and `npm deprecate` points at the successor.

## Success criteria

1. **Cold start paints in under a second** when launched inside a repository, with the rest of the Feed filling in behind it.
2. **A Run invoked elsewhere appears without interaction**, within ~30s idle and ~3s when something is already in progress.
3. **Idle polling consumes ~0 primary rate limit**, because revalidation returns 304.
4. **You cannot delete the wrong Run.** Selection is ID-keyed, the confirm set is frozen, and rows never move while the cursor is in the list.
5. **A Purge of ~18,000 Runs completes** without being rate limited, without manual babysitting, and resumes correctly by re-running the same filter.
6. **The tool never lies about counts.** A capped view says it is capped.

## Open risks

Unverified. Each is a build-time check, not a guess to be written into the design.

| # | Risk | If it goes badly |
|---|---|---|
| **R2** | Does go-gh's client do real ETag revalidation, or only TTL caching? | **The one that forces a redesign.** Free 304s are the economic basis of the entire live layer. If go-gh is TTL-only, we need our own `http.RoundTripper`. Verify first. |
| **R1** | Does go-gh's token resolution work with **no gh binary installed**? The reference token lives in the OS keyring, not `hosts.yml`. | "Standalone" is a shim, not a product. Would reopen [ADR-0002](./adr/0002-go-gh-with-dual-distribution.md) and [ADR-0008](./adr/0008-full-cli-surface-despite-gh-overlap.md). |
| **R3** | Exact asset naming required for `gh extension install` to find a precompiled Go binary. | Release automation is wrong. Contained; blocks the release, not the design. |
| **R4** | Do 304s count against the **secondary** limit? Assumed yes. Untestable without deliberately tripping a limit and risking a block. | The polling scheduler's budget maths is optimistic. Mitigated by assuming the worst. |
| **R5** | In-progress Run log behaviour. | Log viewer needs an empty state. Live tailing is impossible regardless. |

## Features

Requirements live in [features/](./features/), one directory per capability.

| Group | Features |
|---|---|
| **Surfaces** | [live-run-feed](./features/live-run-feed/requirements.md), [run-detail](./features/run-detail/requirements.md), [log-viewer](./features/log-viewer/requirements.md), [workflow-management](./features/workflow-management/requirements.md), [workflow-dispatch](./features/workflow-dispatch/requirements.md), [storage-reclamation](./features/storage-reclamation/requirements.md), [approvals](./features/approvals/requirements.md), [settings](./features/settings/requirements.md) |
| **Operations** | [purge](./features/purge/requirements.md), [run-lifecycle](./features/run-lifecycle/requirements.md), [notifications](./features/notifications/requirements.md) |
| **Infrastructure** | [repo-discovery](./features/repo-discovery/requirements.md), [polling-scheduler](./features/polling-scheduler/requirements.md), [rate-governor](./features/rate-governor/requirements.md), [local-store](./features/local-store/requirements.md), [cli-surface](./features/cli-surface/requirements.md) |

Directory names follow the glossary. `purge` is the capability's name in the language, not "bulk-delete". `local-store` is the on-disk ETag and payload store, deliberately not named "cache", because **Cache** is reserved for a GitHub Actions Cache, the thing Reclamation deletes.
