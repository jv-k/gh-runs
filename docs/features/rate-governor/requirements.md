# Rate Governor

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

The governor owns all write throughput and all Budget accounting: it reads the rate-limit headers that arrive free on every response, paces every write to what the account actually tolerates, and backs off before GitHub decides to stop us. It exists because the penalty for guessing wrong is a block on the user's account, not a retry.

## Requirements

### Ownership

**R1.** The governor must be the single authority for Budget accounting. Every request the tool issues must be accounted through it: discovery probes, scheduler polls, detail fetches, a Purge's crawl, and every write. No other component may parse rate-limit headers or maintain a parallel tally.

**R2.** The governor must be the single authority for write throughput. Every write must be paced by it: Run deletion, cancel, force-cancel, re-run, Dispatch, Cache and Artifact deletion.

**R3.** The governor must be account-scoped and global, not per repository. The Budget is a property of the token, not of a repository, so a per-repository throttle would multiply its own rate by the number of repositories a Purge happens to span. One cross-repo Purge is one throttle. (This settles [purge](../purge/requirements.md) open question 7.)

**R4.** The governor must pace writes but must not schedule reads. It publishes Budget state. The [polling-scheduler](../polling-scheduler/requirements.md) sets its own intervals from it. The division is deliberate: read frequency is a function of tiers and poll-set size that only the scheduler knows, while write throughput is a single global rate.

### Reading the Budget

**R5.** The governor must read `x-ratelimit-remaining` and `x-ratelimit-reset` from every response it sees. They arrive on every response at no cost and are currently discarded. The entire Budget model is available for free and is simply not being picked up.

**R6.** The governor must treat the headers as authoritative and its own projection as provisional. A token shared with CI has less headroom than the limit suggests, and the account's consumption includes traffic this tool never issued. So a local tally can only ever be a lower bound between responses.

**R7.** The governor must account a conditional request returning 304 as costing zero primary allowance and a 200 as costing one. Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), attributable entirely to the 200.

**R8.** The governor must publish, to any component that asks: remaining Budget, the reset time, whether consumption is under pressure, and whether it is exhausted. [live-run-feed](../live-run-feed/requirements.md) R30 and [polling-scheduler](../polling-scheduler/requirements.md) R16 consume this to say "resumes 14:32" rather than going quietly stale.

**R9.** The governor must derive the resumption time from `x-ratelimit-reset` where the response supplies it, and from `Retry-After` where a rate-limit response supplies that. Where neither is available it must report exhaustion without a time rather than inventing one.

### Adaptive throttle

**R10.** The governor must start writes at the documented-safe rate of one per second and ramp upward only while responses stay clean.

**R11.** The governor must ramp toward the points ceiling (~180 writes/min, from DELETE at 5 points against ~900 points/min) and must retain headroom below it rather than converging on it. That ceiling is derived from the more permissive of two published limits that disagree by 3×. Treating it as the whole truth is what a fixed-rate design would do, and the reason to reject one.

**R12.** The governor must back off hard on any 403, any 429, and any `Retry-After`, and must honour a `Retry-After` interval where one is supplied.

**R13.** The governor must never count a rate-limit response as a failure of the operation. It feeds backoff, and the affected request must be re-attempted rather than recorded as failed. Continuing to issue requests while limited risks a ban, and recording a limit as a failure would trip [purge](../purge/requirements.md) R21's circuit breaker on an account that is merely busy.

**R14.** The governor must expose, per response, whether it was a rate-limit response, so consumers can apply R13 without re-deriving the classification themselves.

**R15.** The governor must adapt to the account it is running against rather than to a compiled constant. Enterprise Cloud has 3× the primary allowance, GitHub Apps scale differently, and a token shared with CI has less than it appears. Any fixed number is wrong for somebody, and the governor is the only party in a position to observe which.

**R16.** The governor must not assume the point cost of any write it has not measured. Only DELETE has been measured, at 5 points. Where a cost is unknown the governor must pace conservatively rather than extrapolate. See open question 3.

### What is not configurable

**R17.** Deletes-per-second must not be exposed as a setting, by flag, config key or environment variable. The only thing that knob does is let someone raise it and get their account blocked, and the adaptive governor already beats any number a person could pick, because it observes what their account actually tolerates.

**R18.** Requests-per-second, concurrency and backoff parameters must likewise not be exposed. The single permitted user-facing knob is intent: what share of the Budget gh-runs may spend. That is a question answerable from the user's own context, unlike one that requires the points model, their token tier and their repo count.

**R19.** The governor must honour the intent-level Budget share over reads alone, and must never let it throttle a Purge. The Budget is a share of the **primary** limit, which is observable and is what polling spends. A Purge's throughput is bound by the **secondary** limit, which exposes no counter and is a different pool. The two are not the same currency, so a share of one says nothing about the other. A Purge running under a 25% Budget deletes at the governor's full ramped rate. See [ADR-0007](../../adr/0007-adaptive-delete-throttle.md), which settles this on measurement.

### Delivery and seams

**R20.** The governor must pace a Purge of 18,260 Runs to completion without being rate limited and without manual babysitting. At the prose-advised rate that is ~5 hours. At the points ceiling it is ~100 minutes. Adaptive lands between and self-tunes.

**R21.** All of the governor's timing (the inter-write interval, the ramp's dwell, every backoff and every reset wait) must come from an injected clock. This is the highest-value target for that seam in the whole project: ramp and backoff logic is pure timing, and a test that sleeps through it is a test nobody runs.

**R22.** Tests of the ramp and backoff must be deterministic and instant. A test of R20's 18,260-Run Purge must complete in milliseconds of real time while virtual time advances across hours, and no test may sleep through a real interval.

**R23.** The governor must be exercisable end-to-end against recorded HTTP fixtures, with no live network, including recorded 403, 429 and `Retry-After` responses and the interleaved 200/304 measurement from [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md). Cassettes replay true payloads, which is what catches the API drifting. A hand-written fake would encode our beliefs about rate limiting and stay green while reality moved. And this API has real surprises.

## Acceptance criteria

**AC1: Cold start rate.** From cold, the first write is issued at ~1/second. The interval is measured on the injected clock and no test sleeps.

**AC2: Ramp.** After a run of consecutive clean responses, the issue rate is strictly higher than 1/second. Across any 60s window of virtual time, the number of writes issued never reaches the ~180 points ceiling, and never exceeds it.

**AC3: Purge at reference scale.** A cassette of 18,260 successful DELETEs completes in milliseconds of real time, advancing virtual time across the run. No 60s window of virtual time exceeds the ceiling.

**AC4: Backoff on 429.** Given a 429 with `Retry-After: 60`, no write is issued until virtual time has advanced 60s, the affected Run is re-attempted, and the operation's failure count remains 0.

**AC5: Backoff on 403.** Given a 403 carrying rate-limit signals, the issue rate falls, the request is re-attempted, and it is not recorded as a failure.

**AC6: Rate limits are not failures.** Given a sustained sequence of rate-limit responses, none is reported as a failure to any consumer, and R14's classification marks each as a rate-limit response.

**AC7: Header authority.** Given responses whose `x-ratelimit-remaining` falls faster than the governor's own tally predicts (traffic from another consumer of the same token), the published remaining Budget matches the header, not the tally.

**AC8: 304 is free.** Replaying ADR-0004's interleaved measurement, the governor's primary accounting advances by exactly one per round, attributable to the 200. Each 304 advances it by zero.

**AC9: Exhaustion.** Given `x-ratelimit-remaining: 0` and `x-ratelimit-reset` at 14:32, the governor publishes exhaustion with a resumption time of 14:32, and issues nothing until virtual time passes it.

**AC10: Exhaustion without a time.** Given exhaustion with neither `x-ratelimit-reset` nor `Retry-After`, the governor publishes exhaustion with no time. It does not synthesise one.

**AC11: Pressure gates the readout.** With consumption nominal, the governor reports no pressure and no Budget readout is rendered anywhere. Below the pressure threshold, it reports pressure.

**AC12: One global throttle.** A Purge spanning 3 repositories issues writes at the same aggregate rate as a Purge of the same size confined to 1. The rate does not scale with repository count.

**AC13: Nothing to turn up.** No flag, config key or environment variable sets deletes-per-second, requests-per-second, concurrency, or any backoff parameter. Setting the intent-level Budget share changes the observed rate. Nothing sets the rate itself.

**AC14: The Budget governs polling, never a Purge.** A configured Budget share is respected across a read workload. The same share applied to a workload mixing polls and deletes leaves the deletes at the governor's full ramped rate, because a Purge is bound by the secondary limit and the Budget is a share of the primary one.

## Constraints

**GitHub gives two answers for how fast you may write, and they disagree by 3×:**

| Source | Rule | Implied ceiling | An 18,260-Run Purge |
|---|---|---|---|
| Points model | DELETE costs 5 points against ~900 points/min | ~180/min | ~100 minutes |
| Written guidance | wait at least one second between writes | ~60/min | ~5 hours |

That disagreement is the entire reason this component is adaptive rather than a constant ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)). Neither number can be dismissed: the points model is GitHub's own arithmetic, and the prose is GitHub's own advice.

**The penalty for being wrong is asymmetric.** Too slow costs the user time. Too fast costs them a block on their account. That asymmetry is why R10 starts at the documented-safe rate and ramps, rather than starting at the ceiling and backing off.

**`x-ratelimit-remaining` and `x-ratelimit-reset` arrive on every response at no cost**, and are currently discarded. The Budget model is free.

**A 304 costs zero primary allowance. A 200 costs one.** Measured by interleaving: `used` 120 → 121 → 122, the increment belonging entirely to the 200 ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)). We assume conservatively that 304s do count against the **secondary** limit (PRD risk R4).

**Only DELETE's point cost has been measured.** Cancel, force-cancel, re-run and Dispatch have not.

**Enterprise Cloud has 3× the primary allowance**, GitHub Apps scale differently, and a token shared with CI has less headroom than its limit suggests. There is no fixed number that is right for every account.

**v1 chose `sleep 0.25`**, ~4 writes/sec, faster than *both* published numbers. It works on small repositories and would be blocked on a large one. That is the failure mode this component replaces.

**A Purge cannot be a modal**, because at reference scale it runs for 100 minutes to 5 hours. The governor's pacing is what makes that duration a background fact rather than a hostage situation.

## Open questions

1. **Resolved by body, not by header, and it fails safe toward backoff.** R12 sends a 403 to hard backoff. [repo-discovery](../repo-discovery/requirements.md) R10 and [purge](../purge/requirements.md) R20 send a 403 to an authorization outcome. Both are canon, and they demand opposite handling of the same status code.

    **An authorization 403 is measured.** `GET /repos/cli/cli/actions/permissions` on a token without admin returns HTTP 403 carrying a `documentation_url` that points at **the endpoint's own reference page**, a `message` naming the missing permission, no `retry-after`, and `x-ratelimit-remaining: 4976`.

    **A secondary-limit violation is documented rather than measured**, deliberately, because provoking one is the hazard PRD R4 refuses. GitHub documents 403 **or 429**, "an error message that indicates that you exceeded a secondary rate limit", and an optional `retry-after`. It does **not** publish the exact message text, so we must not match on a string we have never seen.

    **`x-ratelimit-remaining` is not the discriminator, and the temptation to use it is a trap.** Those headers describe the **primary** limit only (PRD). A secondary-limit 403 can arrive with a completely healthy primary remaining, exactly as the authorization 403 above did. The two are indistinguishable on that header.

    **The rule: a 403 or 429 is an authorization outcome only when it matches the measured authorization shape.** Everything else is rate limiting. `retry-after` present means rate limiting outright. This is asymmetric on purpose. Misreading a rate limit as authorization keeps issuing and risks the account block this component exists to avoid, while misreading authorization as a rate limit costs one backoff before the retry fails the same way and resolves. Default to backing off. Verify the shape against a live secondary limit only if one is ever hit in the wild, never by provoking one.

2. **Whether the secondary limit is observable at all is UNKNOWN, and much of the design depends on the answer.** The interleaved measurement behind R7 tracked *primary* consumption, and nothing in the canon establishes that any header reports remaining *secondary* points. Nor does it establish, strictly, which limit `x-ratelimit-remaining` describes when both are in play. If none does, then all secondary accounting (including every number in [polling-scheduler](../polling-scheduler/requirements.md) R11) is a projection from our own issued requests and the points model, computed by a client that cannot see the other consumers of the same token, and the only feedback signal is the 403 or 429 that means we already overshot. That is precisely why R11's headroom must be real and R12's backoff must be hard. Verify what a rate-limit response carries.

3. **The point cost of cancel, force-cancel, re-run and Dispatch is UNKNOWN.** Only DELETE was measured, at 5 points. R16 requires conservative pacing in the absence of measurement, but "conservative" has no number until someone measures. Raised by [run-lifecycle](../run-lifecycle/requirements.md) open question 3 and owned here.

4. **How much headroom to retain below ~180/min is UNKNOWN.** R11 requires headroom and does not size it. Too little and the ramp's whole purpose (never discovering the ceiling the hard way) is defeated. Too much and the governor is a slower fixed rate wearing an adaptive costume.

5. **The ramp's shape is UNKNOWN.** How many clean responses before increasing, by how much, and how far to drop on a backoff. The canon fixes the endpoints (start at 1/sec, ramp toward the ceiling, back off hard) and nothing between them.

6. **The pressure threshold at which the Budget readout appears is UNKNOWN.** R8 and [live-run-feed](../live-run-feed/requirements.md) R29 require silence when consumption is nominal and a readout under pressure. No number separates the two.

7. **Resolved: one Budget share, over the primary limit, and a Purge is not subject to it.** R19 previously applied one share to reads and writes alike, which contradicted [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) outright. The ADR wins, on measurement: the two limits are different currencies, so a share of the primary one has nothing to say about a Purge bound by the secondary one. R19 and AC14 are corrected. The secondary limit gets no share of its own, because it exposes no counter to take a share of, and its control is the ramp rather than a budget. The observation that a workload can be comfortable on one limit while pinned against the other still holds and is exactly why the two are governed differently.

## Related

- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R10, R11, R12, R17, R21.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). R7's measurement. Why the secondary limit binds.
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R20's duration is survivable because a resume is free.
- [purge](../purge/requirements.md). R2 paces its R17. R13 implements its R19. R3 settles its open question 7.
- [polling-scheduler](../polling-scheduler/requirements.md). Consumes R8's Budget state. R4 draws the line between them.
- [repo-discovery](../repo-discovery/requirements.md). Its probe burst is accounted here. Shares open question 1.
- [run-lifecycle](../run-lifecycle/requirements.md). Paced by R2. Owns open question 3's origin.
- [cli-surface](../cli-surface/requirements.md). Its R14 throttle is this component.
- [live-run-feed](../live-run-feed/requirements.md). R8 and R9 supply its R29 and R30.
- [settings](../settings/requirements.md). Owns R19's intent-level share, and owns what R17 and R18 refuse to expose.
