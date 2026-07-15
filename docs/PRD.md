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
- **No storage answer.** Nothing tells you that Caches are holding 10.59 GB you can actually reclaim, while 15.55 GB of Artifacts is almost entirely tombstones whose bytes are already gone.

The naive workaround for bulk deletion exists and mostly works:

```sh
gh run list --status failure -L 100 --json databaseId -q '.[].databaseId' | xargs -n1 gh run delete
```

It runs near the secondary rate limit on a fast connection, has no retry, silently stops at 1,000 results when filtered, and fails on in-progress Runs. Those four gaps are what justify code. The listing is not.

## Who it's for

People who own or contribute to enough repositories that Actions activity stops being observable by hand, and who accumulate Runs, Caches and Artifacts faster than they clean them. The reference user has **163 repositories, roughly 26 with Runs**. The reference repository has **28,694 Runs** and **10.59 GB of Caches**.

## Stack

Go, rendered with **Bubble Tea** and **Lipgloss**. The Elm-style model/update/view loop and first-class async commands are what a Feed of tiered pollers wants, and gh-dash is the proof it carries this exact domain. tview was the real alternative and lost on the async story: mixing API polling into a callback toolkit is clunkier than a message loop, and theming is harder. gocui and raw tcell would mean hand-rolling lists, viewports and key handling that bubbles supplies.

None of that is surprising enough to warrant an ADR (see [adr/](./adr/) on the three-part test), which is why it lives here. It is recorded because it is the one stack decision a reader would otherwise have to infer.

The client is go-gh, which is a different question and does warrant one. See [ADR-0002](./adr/0002-go-gh-with-dual-distribution.md).

## Testing

Three seams, designed in from day one rather than retrofitted:

| Seam | Why |
|---|---|
| **Recorded HTTP fixtures** | Cassettes replay what the API actually said, so tests catch drift. Hand-written fakes encode what we believe and stay green while reality moves. This API has real surprises, and every one of them is in the table below. |
| **Injected clock** | The polling scheduler and rate governor are timing-dependent. Their tests must be deterministic and instant, never sleeping through a real interval. |
| **Golden files** | TUI rendering. A live Feed's correctness is largely what it puts on screen, and nothing else in this list can see that. |

## Constraints that shape everything

Every one of these changed a design decision, and they are not all the same kind of claim. **Most were measured** against the live API and against the client we build on. **A few are documented rather than observed**: the points model, Dispatch's `return_run_details`, and fine-grained PAT scopes. Each row says which it is. Naming the distinction does not weaken the measured rows, it protects them: a canon that cannot tell an experiment from a citation ends up defending the citation just as hard.

**The absolute counts below are point-in-time.** `cli/cli` invokes Runs continuously, so its totals drift, and this repository's Run count has been recorded at 28,694, 28,707 and 28,710 across three measurement sessions, each correct when taken. Every count here is a reference measurement rather than a constant. **No test may hardcode one.** A fixture pins the shape of a response. It must never pin the size of somebody else's repository.

| Constraint | Consequence |
|---|---|
| **No cross-repo Run query exists.** Not in REST, not in GraphQL. `Repository` has no workflow field; `search` doesn't cover Runs; `WorkflowRun` in GraphQL is only reachable via `CheckSuite` and lacks Status and Conclusion entirely. | Fan out one request per repository and merge client-side. gh-dash's "section = search query" model is unavailable. → [ADR-0003](./adr/0003-multi-repo-via-client-side-fanout.md) |
| **Conditional requests are free.** A 304 consumes zero primary rate limit; a 200 consumes one. Verified by interleaved measurement. | Polling ~26 repos every few seconds costs approximately nothing while idle. This is what makes a live dashboard affordable. → [ADR-0004](./adr/0004-conditional-polling-for-liveness.md) |
| **go-gh's cache is TTL-only and never revalidates.** A cache hit returns without touching the network, and freshness is file mtime against a TTL. Verified by source read, by exhaustive grep, and against real go-gh v2.9.0, where two identical GETs produced 1 network hit and 0 requests carrying `If-None-Match`. `EnableCache: false` does not disable it. Only `CacheTTL: 0` does. | The free 304s above are ours to send, not go-gh's to provide. We supply our own `http.RoundTripper` as `api.ClientOptions.Transport` and leave go-gh's cache off. Verified end to end. → [ADR-0004](./adr/0004-conditional-polling-for-liveness.md) |
| **Filtered listing silently caps at 1,000.** `total_count` still reports 18,258 on the reachable pages, and page 11 returns `[]` with no error. Unfiltered listing reaches the full 28,694. | Never trust `total_count` in a filtered view. Live Feed filters server-side and labels honestly, and a Purge crawls unfiltered. → [ADR-0005](./adr/0005-hybrid-filtered-live-unfiltered-purge.md) |
| **`Link`'s `rel="last"` lies in a filtered view exactly as `total_count` does, and past the cap `total_count` collapses to 0.** Measured on `cli/cli`, `status=success`: page 1 claims `total_count` 18,258 and `rel="last"` **page 183**, while only 10 pages are ever served. Page 11 returns HTTP 200, `[]`, and **`total_count: 0`**, not 18,258. | `rel="last"` is derived from `total_count` and inherits its dishonesty, so it cannot detect the cap either. Neither number may seed a crawl. The 0 is not exhaustion, it is the cap. Note that `rel="next"` does **not** vanish at the cap, which is what keeps the cap silent. |
| **Unfiltered, `Link` is honest, and `rel="next"` is the terminal signal.** Measured on `cli/cli` unfiltered, `total_count` 28,694: `rel="last"` is page 287 and agrees (287 x 100 = 28,700). Page 400 returns HTTP 200, `[]`, `total_count` **preserved at 28,694**, and a `Link` with `prev`, `last` and `first` but **no `next`**. | A Purge crawls on `next` and stops when it is gone. It knows its own length from request one. This is the exact asymmetry that makes ADR-0005's split work: unfiltered pagination can be trusted, filtered pagination cannot. → [ADR-0005](./adr/0005-hybrid-filtered-live-unfiltered-purge.md) |
| **DELETE costs 5 points against ~900/min**, while GitHub's prose separately advises ≥1s between writes. A 3× disagreement. **Documented, not measured**: establishing a point cost by experiment means tripping the secondary limit, which risk R4 forbids permanently. Re-verified as current. | ~180/min against ~60/min, which is the **published** band of ~100 minutes to ~5 hours on 18,258 Runs. The governor reaches neither end: [rate-governor](./features/rate-governor/requirements.md) R11 caps at 150/min and floors at 30/min, so a real Purge runs ~2 hours to ~10 hours, and ~155 minutes in the normal case. Bulk deletion cannot be a modal. → [ADR-0007](./adr/0007-adaptive-delete-throttle.md) |
| **`/rate_limit` disagrees with the response headers, and reads high.** Measured: `core` sat frozen at `used: 90` for minutes while the header counter on the same resource climbed 62 to 78, with near-identical resets. A frozen number reading **above** the truth is not a lagging snapshot. `/rate_limit` is itself free (3 back-to-back calls did not advance it). | Track the response headers. Never `/rate_limit`. [rate-governor](./features/rate-governor/requirements.md) R7 accounts `used`, so code seeding that baseline from `/rate_limit` starts wrong by 12 to 36 and cannot correct itself. → [rate-governor](./features/rate-governor/requirements.md) R5a |
| **Prior Attempts' Jobs are not served.** `/runs/{id}/attempts/1/jobs` returns `total_count: 0`. | Attempt history is not buildable. Attempt is a badge, never a view. |
| **`created_at` ≠ `run_started_at` on re-runs.** Identical on 8/8 normal Runs; 3 hours apart on a re-run. | Sort the Feed by `run_started_at`: as stable as `created_at`, but a re-run surfaces where you can see it. |
| **Caches dwarf what an Artifact deletion can actually reclaim.** Measured on `cli/cli` by summing **all 581 Artifacts across 6 pages**, not by sampling: Caches hold 10,587,236,096 bytes (10.59 GB) across 83, while Artifacts hold 15,548,177,058 bytes (15.55 GB) across 581. **Artifacts exceed Caches on raw bytes.** But 279 of the 581 (48%) are expired, holding 15.50 GB of that 15.55 GB, and deleting an expired Artifact reclaims nothing. **Live Artifacts total 47.68 MB.** | Storage is one bytes-first Reclamation view led by Caches, not two browsers. Caches lead because **expired Artifacts reclaim nothing**, not because Artifacts are small. On raw bytes they are the bigger number. On reclaimable bytes, 10.59 GB against 47.68 MB is a ~220× gap. |
| **Cache totals are exact and free. Artifact totals must be enumerated in full.** `/actions/cache/usage` returns `active_caches_size_in_bytes` to the byte in one request. Artifacts have no equivalent, and sampling them is not a substitute: an earlier ~80 MB figure here was **extrapolated from 100 of 557** and was wrong by ~194×. Enumerating all 581 cost 6 requests and produced 15.55 GB. | Per success criterion 6, an Artifact total must be summed from a full enumeration or labelled an estimate. The ~194× miss is the exact failure [storage-reclamation](./features/storage-reclamation/requirements.md) R3 warns about, and it is now a measured failure rather than a hypothetical one. `active_caches_count` doubles as a truth oracle for the Cache list. |
| **The secondary rate limit is not observable.** Headers expose `x-ratelimit-*` for the primary limit only. `/rate_limit` lists `core`, `graphql`, `search`, `code_search` and friends, and nothing for the secondary limit. | There is no counter to read. The governor's ramp is open-loop and the only feedback is the 403 that means we already overshot, which is why headroom and backoff are the whole control mechanism. It also means the Budget (a share of the *primary* limit, spent by polling) never throttles a Purge (bound by the *secondary* limit). **Different currencies, one shared pool**: polling and purging both spend the same ~900 points/min, which is why [rate-governor](./features/rate-governor/requirements.md) R11's write ceiling is computed against the reads in flight. → [ADR-0007](./adr/0007-adaptive-delete-throttle.md) |
| **Repo permissions ride along free.** `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request. | Gate destructive actions per repo at zero cost. Archived repos are permanently read-only: their Runs can never be cleaned. |
| **Fine-grained PATs expose no scopes.** `x-oauth-scopes` exists only for classic tokens. **Documented, not measured**: the observation was made with a classic token, where the header was present. That a fine-grained token omits it is documented belief ([repo-discovery](./features/repo-discovery/requirements.md) open question 1). | Pre-flight permission checks are impossible for fine-grained tokens. The API is always the final authority, and a 403 can arrive despite `push: true`. |
| **go-gh cannot reach a keyring token without the gh binary.** It has no keyring code and no keyring dependency. Its only keyring path is shelling out to `gh auth token`, so with gh off PATH the reference token (which lives in the keyring, not `hosts.yml`) resolves empty, source `"default"`. | `GH_TOKEN` is required for users who do not have gh, and is documented as the contract. Anyone with gh keeps getting their token free through the shellout. → [ADR-0002](./adr/0002-go-gh-with-dual-distribution.md) |
| **Dispatch inputs live only in YAML.** The Workflow object carries `path`, never `inputs`. `type: environment` needs a separate `/environments` call. | A typed Dispatch form must fetch and parse the YAML at the target ref. |
| **Dispatch returns 204 with no Run ID. That measurement is superseded, and the replacement awaits live confirmation.** GitHub's current OpenAPI spec (v1.1.4, committed 2026-07-14) adds a `return_run_details: boolean` body parameter to "Create a workflow dispatch event". 204 is now documented as "Empty response when `return_run_details` parameter is `false`", and a 200 returns `{workflow_run_id, run_url, html_url}`, all three required, when it is `true`. The spec and the rendered docs agree independently. **Neither was verified by dispatching**, because a Dispatch is a write and was out of the measurement's remit. | Until one live dispatch confirms the 200 path, correlating a Dispatch to its Run stays best-effort polling on `event=workflow_dispatch` plus a timestamp, racy by construction. If the 200 path works, [workflow-dispatch](./features/workflow-dispatch/requirements.md) R16 to R19 delete outright rather than get hedged. |
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
| Log deletion | Destroys a Run's logs and leaves the Run. Distinct from deleting the Run, and a write like any other: confirmed through the Purge's graduated friction, paced by the governor |
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
5. **A Purge of ~18,000 Runs completes** without being rate limited, without manual babysitting, and resumes correctly by re-running the same filter. At the governor's reachable rates that is ~2 hours to ~10 hours, and ~155 minutes with the Feed running.
6. **The tool never lies about counts.** A capped view says it is capped.

## Open risks

R1, R2 and R3 are **resolved**, and each resolved into a build requirement rather than an unknown to design around. R5 stays an open build-time check. R4 can never be resolved, so it is a permanent assumption rather than a pending question.

| # | Risk | Verdict |
|---|---|---|
| **R2** | Does go-gh's client do real ETag revalidation, or only TTL caching? | **Resolved: TTL-only. It never revalidates.** Verified three ways, including two identical GETs against real v2.9.0 producing 1 network hit and 0 requests carrying `If-None-Match`. [ADR-0004](./adr/0004-conditional-polling-for-liveness.md)'s design survives, but only over our own `http.RoundTripper` passed as `api.ClientOptions.Transport`, with go-gh's cache left off (`CacheTTL: 0`). Verified end to end. A build requirement now, not a risk. |
| **R1** | Does go-gh's token resolution work with **no gh binary installed**? The reference token lives in the OS keyring, not `hosts.yml`. | **Resolved: it does not.** go-gh has no keyring code, and its only keyring path is shelling out to `gh auth token`. With gh off PATH the reference token resolves empty. **Decision: require `GH_TOKEN` for users without gh, and document it.** [ADR-0002](./adr/0002-go-gh-with-dual-distribution.md) and [ADR-0008](./adr/0008-full-cli-surface-despite-gh-overlap.md) both stand: the binary needs no gh, only a token. A build requirement now, not a risk. |
| **R3** | Exact asset naming required for `gh extension install` to find a precompiled Go binary. | **Resolved: the name must end with `{GOOS}-{GOARCH}`**, plus `.exe` on Windows. gh selects with `HasSuffix(name, platform+ext)` (`pkg/cmd/extension/manager.go`, `installBin`), so `darwin-arm64` matches and goreleaser's stock `Darwin_arm64` does not. Case and separator are both load-bearing, and a mismatch ships a release that looks complete and installs nowhere. Release automation uses `cli/gh-extension-precompile`, which already names to the convention. A build requirement now, not a risk. [ADR-0002](./adr/0002-go-gh-with-dual-distribution.md) |
| **R4** | Do 304s count against the **secondary** limit? | **Permanently assumed, never to be resolved.** Testing it means deliberately tripping a limit and risking a block on the user's account, so it will not be tested. Assumed yes throughout, which makes the polling scheduler's budget maths pessimistic by construction. |
| **R5** | In-progress Run log behaviour. | Open. Log viewer needs an empty state. Live tailing is impossible regardless. |

## Features

Requirements live in [features/](./features/), one directory per capability.

| Group | Features |
|---|---|
| **Surfaces** | [live-run-feed](./features/live-run-feed/requirements.md), [run-detail](./features/run-detail/requirements.md), [log-viewer](./features/log-viewer/requirements.md), [workflow-management](./features/workflow-management/requirements.md), [workflow-dispatch](./features/workflow-dispatch/requirements.md), [storage-reclamation](./features/storage-reclamation/requirements.md), [approvals](./features/approvals/requirements.md), [settings](./features/settings/requirements.md) |
| **Operations** | [purge](./features/purge/requirements.md), [run-lifecycle](./features/run-lifecycle/requirements.md), [notifications](./features/notifications/requirements.md) |
| **Infrastructure** | [repo-discovery](./features/repo-discovery/requirements.md), [polling-scheduler](./features/polling-scheduler/requirements.md), [rate-governor](./features/rate-governor/requirements.md), [local-store](./features/local-store/requirements.md), [cli-surface](./features/cli-surface/requirements.md) |

Directory names follow the glossary. `purge` is the capability's name in the language, not "bulk-delete". `local-store` is the on-disk ETag and payload store, deliberately not named "cache", because **Cache** is reserved for a GitHub Actions Cache, the thing Reclamation deletes.
