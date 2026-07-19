# Repo Discovery

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

Discovery establishes which of the account's repositories actually have Runs, so the Feed polls the ~26 that matter rather than all 163, and records what the token may do in each so every destructive action can be gated without spending a request to find out.

## Requirements

### Enumeration

**R1.** Discovery must enumerate every repository the token can see from the authenticated user's repository list, following pagination until it is exhausted. Enumeration covers `affiliation=owner,collaborator,organization_member` with `type=all`, forks included, which is the API default, named here explicitly per [ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md) rather than inherited. For the reference account this is 163 repositories across exactly two pages of 100, so enumeration itself costs two requests.

**R2.** Discovery must not infer Actions usage from any field of the repository object. There is no `has_actions` field. The object exposes `has_issues`, `has_discussions`, `has_pages`, `has_wiki`, `has_projects`, `has_downloads` and `has_pull_requests`, and none of them say anything about Actions. Whether a repository has Runs is answerable only by asking for its Runs.

### Classification

**R3.** Discovery must probe each enumerated repository with a single request for its most recent Runs, and must classify the repository as having Runs if and only if that response body contains at least one Run.

**R4.** The probe must be unfiltered. Per [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md), applying any filter caps a listing at 1,000 results silently and makes `total_count` report matches rather than reachable matches. A classification drawn from a filtered listing would inherit that lie for no benefit.

**R5.** Discovery must probe archived and disabled repositories on the same terms as any other. A repository being read-only is a reason to gate its destructive actions (R9), never a reason to hide its Runs. The reference account's one archived repository is exactly the kind of place Runs accumulate unattended.

**R6.** Discovery must not use code search as a prefilter, and must not use it as a supplementary signal. Code search finds Workflow **files**, not Runs. Its false negatives are this tool's subject matter: a repository whose Workflow file was deleted keeps its Run history (`deleted` is a documented Workflow state), and those **Orphaned Runs** are precisely the cruft a cleanup tool exists to find. Code search is blind to them, indexes only default branches, and skips forks.

### Capability

**R7.** Discovery must record, per repository, the `permissions` object (`admin`, `maintain`, `push`, `triage`, `pull`), `archived` and `disabled`. All of them arrive with the repository list at no additional request cost, so gating costs nothing.

**R8.** Discovery must expose recorded capability as three distinguishable values: permitted, refused, and **not yet known**. A consumer can then keep destructive actions disabled for a repository whose enumeration has not yet returned. A repository painted by the cold-start fast path (R14) reads as not-yet-known until enumeration completes, and its capability must not be inferred from the fact that its Runs listed.

**R9.** Discovery must mark an archived repository as permanently read-only rather than as temporarily ungated. Its Runs can never be cleaned, and no retry, token change or elevation will alter that.

**R10.** Discovery must present recorded capability as advisory, never as a guarantee. The API is always the final authority: a 403 can arrive despite `push: true`, and fine-grained PATs expose no scopes to pre-flight against. No consumer may treat a passing gate as licence to skip its error handling.

### Refresh

**R11.** Discovery must be re-runnable on a schedule and on demand. A repository with no Runs today may have Runs tomorrow, and a classification cached forever is a classification that is wrong forever.

**The schedule is two-tier, split by what each repository holds ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** A repository with a persisted ETag re-probes on the revalidation interval: settable ([settings](../settings/requirements.md) R20, `discovery_refresh_minutes`), default 5 minutes, floor 1 minute. A repository without one re-probes on a fixed hourly constant that no setting alters. The split is what makes the settable tier cheap by construction and bounds the expensive case at ~163 unconditional requests per hour, whatever open question 4's measurement finds. An on-demand refresh runs the full pass.

**R12.** A re-probe must be conditional, carrying the ETag persisted from the previous probe. Per [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) a 304 costs zero primary rate limit, so re-probing all ~163 repositories costs ~zero against the primary limit while nothing has changed, and a repository that acquires its first Run breaks its own ETag and reveals itself with a 200. This is what makes R11 affordable rather than a recurring 4% of the hourly allowance.

**R13.** Discovery must never be used as the steady-state poll set. The ~163-repo probe is a discovery pass. The poll set is the ~26 repositories it classified as having Runs. The distinction is the whole reason this feature exists. Polling 163 repositories at the Feed's tiers would exceed the secondary limit outright.

### Cold start

**R14.** When launched inside a git repository whose remote resolves to github.com, discovery must resolve that repository from the local git remote and yield it first, so its Runs can be painted from a single Run-listing request without waiting on enumeration or on any other repository's response.

R14 is where go-gh's `repository.Current()` trap lands, so it must not be met by calling that function naively. `Current()` shells out to git, which is fine, but it gates its answer on `auth.KnownHosts()`, which reads only environment variables and `hosts.yml` and never the keyring. On a machine where gh was never installed, it fails with "none of the git remotes point to a known GitHub host" even though git works and the remote is plainly github.com. The `GH_TOKEN` contract in [ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md) populates `KnownHosts()` and clears it, so the fast path must fail with that instruction rather than with the library's message, which names the wrong problem.

**R15.** Discovery must publish results incrementally as each probe returns, rather than as one batch when the last probe completes, so the Feed fills in progressively behind the fast path.

**R16.** Discovery must probe with bounded concurrency, and the bound must be chosen by the tool rather than by the user. At concurrency ~10 the full 163-repository pass completes in ~4s. Serially it would be minutes.

**R17.** Discovery's requests must be accounted through the [rate-governor](../rate-governor/requirements.md) like all other traffic, and must be subject to its backoff. A discovery pass is ~165 requests in a burst and is the largest single burst the tool issues outside a Purge's crawl.

### Identity and seams

**R18.** Discovery must key every repository as `host/owner/name` per [ADR-0009](../../adr/0009-host-qualified-repo-identity.md), including in everything it persists, even though 2.0.0 discovers github.com alone. A repository resolving to any other host must be rejected explicitly rather than silently attributed to github.com.

**R19.** Discovery's results (the classification and the recorded capability) must be persisted and reloadable, so a cold start does not re-probe 163 repositories before it can paint. See [local-store](../local-store/requirements.md).

**R20.** Discovery must be exercisable end-to-end against recorded HTTP fixtures, with no live network: enumeration paginating to two pages, probes returning populated and empty Run lists, and the conditional re-probe's 304s. Cassettes replay true payloads, which is what catches the API changing underneath us. A hand-written fake would encode our current beliefs and stay green while reality moved.

**R21.** Discovery's scheduled re-probe (R11) must take its timing from an injected clock, so that a test of the refresh cadence advances time explicitly and completes instantly rather than sleeping through the interval.

**R22.** When enumeration completes and the fast-path repository (R14) is not in the enumerated set, discovery must adopt it **for the session** ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)): spend one `GET /repos/{owner}/{repo}` to learn its `permissions`, `archived` and `disabled`, admit it to the Feed, and admit it to the poll set if it has Runs. Its classification, capability and ETags persist like any other repository's, so revalidation stays free, but membership does not: only a launch inside the repository re-admits it. A session launched elsewhere never sees it, and the Feed never accretes past clones.

## Acceptance criteria

**AC1: Enumeration cost.** Against a cassette of the reference account, discovery issues exactly two enumeration requests and yields 163 repositories. A third page is never requested.

**AC2: Classification.** Given a cassette in which 26 of 163 repositories return a non-empty Run list, the published poll set is exactly those 26. The other 137 appear in no poll set at any tier.

**AC3: Probe cost.** The full pass issues exactly one probe request per enumerated repository: 163 probes, 165 requests including enumeration.

**AC4: Orphaned Runs are discovered.** Given a repository whose Workflow list contains only Workflows in the `deleted` state, and whose Run list is non-empty, discovery classifies it as having Runs and it enters the poll set. No code-search request is issued by any code path.

**AC5: Unfiltered probe.** No probe request carries a filter parameter, and no classification is derived from `total_count`.

**AC6: Archived is probed and marked.** A repository with `archived: true` is probed, its Runs are published, and its capability is marked permanently read-only rather than merely refused.

**AC7: Capability is free.** Across the whole pass, the number of requests issued to learn `permissions`, `archived` or `disabled` for any repository is zero.

**AC8: Not yet known.** Before enumeration returns, the fast-path repository's capability reads as not-yet-known, and is not reported as permitted, refused, or inferred from its Runs having listed.

**AC9: Advisory, not authoritative.** Given `permissions.push: true` and a cassette returning 403 on a destructive request, the 403 is surfaced as an outcome of that request. Discovery's record is not treated as evidence that the 403 is impossible.

**AC10: Conditional re-probe is free.** Given persisted ETags and a cassette returning 304 for all 163 probes, the primary limit's `used` counter does not advance and the poll set is unchanged. Given one repository that has acquired its first Run and returns 200, only that repository is added to the poll set.

**AC11: Fast path.** Launched inside a git repository, the local repository is yielded and its Runs are painted after exactly one Run-listing request, before the remaining ~162 probes have completed.

**AC12: Incremental publication.** Results are observable for repositories that have responded while other probes are still in flight. Consumers are not blocked on the final probe.

**AC13: Bounded concurrency.** At no instant are more than the configured number of probe requests in flight. No user-facing setting alters that bound.

**AC14: Host qualification.** Every persisted and published key carries a host component. No key can be constructed without one. A repository resolving to a host other than github.com is rejected explicitly and contributes no entry.

**AC15: Deterministic refresh.** A test of the scheduled re-probe advances the injected clock and completes without sleeping.

## Constraints

**Reference scale is 163 repositories, ~26 with Runs.** `/user/repos` paginates at 100, so enumeration is two requests. The probe pass is ~163 requests (under 4% of the 5,000/hour primary allowance) and ~4s at concurrency 10. That is the cost of a discovery pass, paid once and then revalidated for free (R12). It is not the cost of the steady state.

**Auto-discovery was an override, not the recommendation.** The product owner was recommended cwd plus an explicit config list of repositories, on the argument that fan-out stays bounded and the account cares about ~10 repositories rather than 163, and chose auto-discovery from `/user/repos` instead. The ~163-repository probe pass above is the cost of that choice, and it is a decision rather than an oversight.

**There is no `has_actions` field.** The measured field set is `has_issues`, `has_discussions`, `has_pages`, `has_wiki`, `has_projects`, `has_downloads`, `has_pull_requests`. You cannot tell from the list, which is why probing exists.

**There is no cross-repository Run query.** Not in REST, not in GraphQL, not in `search` ([ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)). One request per repository is not a preference. It is the only mechanism available.

**Permissions ride along free.** `/user/repos` returns `{admin, maintain, push, triage, pull}` alongside `archived` and `disabled` with no extra request. Observed distribution across the reference account's 163 repositories:

| Capability | Repositories |
|---|---|
| `admin` and `push` | 159 |
| `push` but not `admin` | 3 |
| `archived` | 1 |

**Code search was rejected on correctness, not cost.** `path:.github/workflows user:jv-k` returns 26 distinct repositories from ~200, drawing on a separate 10/min `code_search` bucket that never touches the 5,000/hour core allowance. So it is cheap, and cheap is not the point. It answers "which repositories have Workflow files", and the question is "which repositories have Runs". Those differ exactly where this tool earns its keep. The reference account also happens to have ~26 repositories with Runs, but that is two different sets measured two different ways: the agreement of the counts is not evidence that the members agree.

**Fine-grained PATs expose no scopes.** `x-oauth-scopes` exists only for classic tokens, so pre-flight permission checking is impossible for fine-grained tokens and R10 must hold regardless of what R7 recorded.

**Conditional requests are free against the primary limit.** Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), and that one belonged entirely to the 200 ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)). We assume conservatively that 304s do count against the secondary limit (PRD risk R4), which bounds how aggressively a re-probe may fan out but not whether it is affordable.

## Open questions

1. **Resolved: moot by rule, no code path reads `x-oauth-scopes` ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** The canon's claim that the header exists only for classic tokens was observed with a classic token and never verified against a fine-grained one, and it never will be, because nothing may read the header. R10 already makes capability advisory and keeps every consumer's error handling, so ruling the header unread turns the verification task into a standing prohibition and closes the question without a measurement.

2. **Resolved: tell them apart by the body's shape, and bound the safe direction at three.** R10 requires a 403 to be surfaced as an authorization outcome. [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) requires a 403 to trigger hard backoff. Both are canon and they send the same status code to opposite handlers. [rate-governor](../rate-governor/requirements.md) open question 1 owns the resolution and this question defers to it.

    **The rule.** An authorization 403 is measured: `GET /repos/cli/cli/actions/permissions` on a token without admin returns a `documentation_url` pointing at the endpoint's own reference page, a `message` naming the missing permission, no `retry-after`, and a healthy `x-ratelimit-remaining`. A 403 matching that shape is authorization. Everything else is rate limiting, and `retry-after` present is rate limiting outright. **The header is not the discriminator**, because a secondary-limit 403 can arrive with a healthy primary remaining, exactly as this one did.

    **This question's own warning is what forced the bound.** It said an authorization 403 read as a rate limit "stalls a Purge behind a backoff that will never clear", and it was right where the rate-governor's own wording was wrong. So the safe default is capped: after three consecutive rate-limit classifications on one Run, the governor reclassifies as authorization and [purge](../purge/requirements.md) R20 skips it.

    **What is not resolved**, and never will be by us, is what a live secondary-limit 403 carries. Provoking one is the hazard PRD risk R4 refuses, so the rule is asymmetric on purpose and errs toward backing off.

3. **Resolved: two-tier, and the fast tier is settable ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** Repositories holding a persisted ETag revalidate on `discovery_refresh_minutes` ([settings](../settings/requirements.md) R20), default 5 minutes, floor 1 minute. Repositories without one re-probe hourly, a constant. R11 carries the cadence. The split decouples the cadence from question 4's unknown: whatever the measurement finds, the expensive case is bounded at ~163 unconditional requests per hour, about 3% of the primary allowance, and the burst runs under [ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md)'s bound of 10.

4. **Commissioned, and no longer load-bearing.** Question 3's two-tier resolution self-adapts to either answer, so the measurement now informs cost accounting rather than design. The observation (does a Run-list 200 with zero Runs carry an ETag?) is folded into [Measure whether a filtered listing's ETag is body-faithful](https://github.com/jv-k/gh-runs/issues/19), which uses the same instrumentation. This note records the answer when it lands.

5. **Resolved from documentation: `disabled` means disabled by GitHub, and it stays inert ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** The field marks a repository GitHub itself has disabled (DMCA takedown, billing failure, terms-of-service action). It is not "Actions disabled", which lives at `GET /repos/{owner}/{repo}/actions/permissions` as an `enabled` boolean, and it is not a synonym for `archived`. Only GitHub can produce a disabled repository, so this answer is unmeasurable by us and documentation is its only source. R7 keeps recording it because it arrives free, nothing acts on it in 2.0.0, and gating keys on `archived` and `permissions` alone.

6. **Resolved: the API default, named explicitly ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** Enumeration covers `affiliation=owner,collaborator,organization_member` with `type=all`, forks included, which is what the reference 163 was measured under. R1 carries the set. Narrowing was rejected because an organization repository the user works in would silently never appear, and widening beyond the default is not possible.

7. **Resolved: adopted for the session, at a cost of one request ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** R22 carries it: one `GET /repos/{owner}/{repo}` learns capability, the repository joins the Feed (and the poll set if it has Runs), its record and ETags persist for revalidation economy, and only a launch inside it re-admits it. Resolved jointly with [live-run-feed](../live-run-feed/requirements.md) open question 10.

## Related

- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). Why R3 probes per repository at all.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). R12.
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). R4.
- [ADR-0009: Repo identity is host-qualified](../../adr/0009-host-qualified-repo-identity.md). R18.
- [ADR-0020: Discovery's scope, ad-hoc adoption, and refresh cadence](../../adr/0020-discovery-scope-adoption-and-refresh.md). R1's affiliations, R11's cadence, R22, and the disposition of open questions 1, 4 and 5.
- [settings](../settings/requirements.md). Its R20 owns the `discovery_refresh_minutes` key R11's fast tier reads.
- [polling-scheduler](../polling-scheduler/requirements.md). Consumes R13's poll set. Must never inherit the probe set.
- [local-store](../local-store/requirements.md). Persists R19's results and R12's ETags.
- [rate-governor](../rate-governor/requirements.md). Accounts R17's burst. Shares open question 2.
- [live-run-feed](../live-run-feed/requirements.md). Consumes R8's tri-state capability (its R18) and R14's fast path (its R32).
- [purge](../purge/requirements.md), [run-lifecycle](../run-lifecycle/requirements.md), [workflow-management](../workflow-management/requirements.md), [workflow-dispatch](../workflow-dispatch/requirements.md), [log-viewer](../log-viewer/requirements.md). All gate destructive actions on R7's free capability data.
