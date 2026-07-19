# Rate Governor

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

The governor owns all write throughput and all Budget accounting: it reads the rate-limit headers that arrive free on every response, paces every write to what the account actually tolerates, and backs off before GitHub decides to stop us. It exists because the penalty for guessing wrong is a block on the user's account, not a retry.

It is an `http.RoundTripper`, nested under [local-store](../local-store/requirements.md)'s and above the network ([ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md)). That is not an implementation detail. It is the only layer where the headers R5 needs still exist.

## Requirements

### Ownership

**R1.** The governor must be the single authority for Budget accounting. Every request the tool issues must be accounted through it: discovery probes, scheduler polls, detail fetches, a Purge's crawl, and every write. No other component may parse rate-limit headers or maintain a parallel tally.

**R2.** The governor must be the single authority for write throughput. Every write must be paced by it: Run deletion, log deletion ([log-viewer](../log-viewer/requirements.md) R17), cancel, force-cancel, re-run, Dispatch, Workflow enable and disable ([workflow-management](../workflow-management/requirements.md) R5), and Cache and Artifact deletion. That is ten writes, and the list is exhaustive rather than illustrative. A write this requirement does not name is a gap in the list, never a write outside it.

**Workflow enable and disable are writes, and the list did not have them.** They are PUTs against `/actions/workflows/{id}/enable` and `/disable`, [ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md) already gave them to `ops` alongside the other nine, and [purge](../purge/requirements.md) R29 already counted flipping a Workflow's State among the writes this requirement paces. Two documents worked from ten while the list said eight. The omission never cost a request, because [ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md) puts the governor under every request as a RoundTripper that discriminates on method, and a PUT is a write whether or not this list names it. What it cost was the list's claim to be exhaustive, which is the only thing R2 has.

**R3.** The governor must be account-scoped and global, not per repository. The Budget is a property of the token, not of a repository, so a per-repository throttle would multiply its own rate by the number of repositories a Purge happens to span. One cross-repo Purge is one throttle. (This settles [purge](../purge/requirements.md) open question 7.)

**R4.** The governor must pace writes but must not schedule reads. It publishes the Budget Readout. The [polling-scheduler](../polling-scheduler/requirements.md) sets its own intervals from it. The division is deliberate: read frequency is a function of tiers and poll-set size that only the scheduler knows, while write throughput is a single global rate.

### Reading the Budget

**R5.** The governor must read `x-ratelimit-remaining` and `x-ratelimit-reset` at the **transport layer**, from every response returning from the network, and must not attempt to read them above go-gh's client. The governor is an `http.RoundTripper` nested inside [local-store](../local-store/requirements.md) R19's, per [ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md). This is not a preference. go-gh's `Get` and `Do` return an error alone and discard the `*http.Response`, so a governor sitting above the client sees no headers at all and this requirement is unimplementable there. At the transport layer the headers arrive on every response at no cost, and are currently discarded. The entire Budget model is available for free and is simply not being picked up.

**R5a.** The governor must take its primary-limit accounting from the response headers and must **never** seed or correct it from `/rate_limit`. The two disagree. Measured: `/rate_limit`'s `core` bucket sat frozen at `used: 90` for minutes while the header counter on the same resource climbed 62 to 78, with near-identical reset times. It reads **higher** than the headers, so it is not a lagging snapshot that catches up, and R7 accounts `used`. Any code seeding that baseline from `/rate_limit` starts wrong by 12 to 36 and stays wrong. `/rate_limit` is free (three back-to-back calls did not advance it), which makes it tempting and does not make it right.

**R6.** The governor must treat the headers as authoritative and its own projection as provisional. A token shared with CI has less headroom than the limit suggests, and the account's consumption includes traffic this tool never issued. So a local tally can only ever be a lower bound between responses.

**R7.** The governor must account a conditional request returning 304 as costing zero primary allowance and a 200 as costing one. Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), attributable entirely to the 200.

**R8.** The governor must publish a **Budget Readout** ([CONTEXT.md](../../CONTEXT.md)) to any component that asks: remaining allowance, the reset time, whether consumption is under pressure, and whether it is exhausted. The Readout is an observation and the Budget is a policy input, and the two must not be conflated in code any more than in prose. [live-run-feed](../live-run-feed/requirements.md) R30 and [polling-scheduler](../polling-scheduler/requirements.md) R16 consume the Readout to say "resumes 14:32" rather than going quietly stale.

**R8a.** Consumption is **under pressure when the current burn rate would exhaust the remaining allowance before it resets**, and by no other test. The predicate is `remaining / burn < time_to_reset`. `remaining` and the reset instant are R5's headers, which R9 already handles, and `time_to_reset` is that instant less the injected clock's now (R21). **Burn is the primary allowance consumed over the last five minutes, divided by the full five minutes and never by the elapsed part of them.** R7 supplies the arithmetic: a 200 costs one and a 304 costs zero. **Where burn is zero the governor must report no pressure and must evaluate no quotient.** Nothing is projected to run out at a rate of nothing, and no division is performed to discover it.

**A compiled percentage is exactly what [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) rejects for the throttle, and the argument does not weaken here.** Enterprise Cloud has 3× the primary allowance, GitHub Apps scale differently, and a token shared with CI has less headroom than its limit suggests (R15). So no percentage is right for every account. It is also wrong in both directions at once on a single account: 75% consumed with 50 minutes to reset is fine and a percentage shouts, while 60% consumed and burning fast is doomed and a percentage stays silent. Projection is what makes [live-run-feed](../live-run-feed/requirements.md) R29's silence mean something, because a readout that fires on routine work trains the operator to ignore it.

**The five-minute window is a constant, and pretending otherwise would be the same mistake in a smaller place.** It is chosen so a burst amortises instead of firing. The reference cold start issues ~189 primary requests (~163 discovery probes plus ~26 Feed payloads) inside a few seconds. Over five minutes that reads as ~38/min, which against a 5,000/hour allowance projects exhaustion ~127 minutes out, and no reset is ever more than 60 minutes away, so the cold start stays silent. A one-minute window reads the same burst as ~189/min, projects exhaustion in ~25 minutes, and fires on the most routine workload the tool has. A Purge's ~287-request crawl amortises the same way, to ~57/min and ~82 minutes. **Dividing by the window's full width rather than by the elapsed part is the other half of the choice**: it under-reports for the first five minutes of every session, and under-reporting is the safe direction here, because the failure R29 names is a readout nobody believes.

**Exhaustion satisfies this predicate rather than competing with it.** At `remaining: 0` the projection is immediate and pressure is true, and the Readout's exhaustion flag is a separate field that stays authoritative for R9, for [live-run-feed](../live-run-feed/requirements.md) R30 and for [polling-scheduler](../polling-scheduler/requirements.md) R16. A Readout that is exhausted and under pressure at once is consistent. The Feed's R29 renders its readout, and its R30 states the pause.

**R9.** The governor must derive the resumption time from `x-ratelimit-reset` where the response supplies it, and from `Retry-After` where a rate-limit response supplies that. Where neither is available it must report exhaustion without a time rather than inventing one.

**A rate-limit classification publishes exhaustion, whatever the primary headers say ([ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md)).** A secondary-limit 403 or 429 can arrive with a healthy `x-ratelimit-remaining` (open question 1), and it names the whole token rather than the request that tripped it. So on classifying a response as rate limiting (R14), the governor must publish the Readout as exhausted, with the resumption time this requirement derives, or without one where nothing supplies it. The [polling-scheduler](../polling-scheduler/requirements.md) then stops exactly as it does at primary exhaustion (its R16), which is what keeps R13 honoured for reads without a retry queue: a poll's re-attempt is its next scheduled tick after the resume. This holds whichever request tripped the limit, so a write's rate-limit response publishes the same exhaustion alongside R12's backoff. `Exhausted` therefore describes the account's rate limiting rather than the primary limit alone, and [ADR-0014](../../adr/0014-domain-types-and-the-budget-readout.md)'s comment and [CONTEXT.md](../../CONTEXT.md)'s entry carry the widened meaning.

### Adaptive throttle

**R10.** The governor must start writes at the documented-safe rate of 1.0 per second and ramp upward only while responses stay clean. The ramp is **additive increase, multiplicative decrease**: add 0.25/sec after every 20 consecutive clean responses, and halve the current rate on any rate-limit response, re-ramping from the halved rate. Open question 5 records why AIMD and not something else.

**R11.** The governor's write ceiling must be **dynamic**, computed against the secondary pool that reads and writes actually share:

```text
ceiling (deletes/min) = (900 - observed read points per minute) / 5
```

floored at **0.5/sec** and capped at **2.5/sec**. Note the unit: the expression yields deletes per **minute**. Divide by 60 for the per-second rate the ramp carries, or read it directly as `(900 - reads) / 300` deletes per second.

The reads figure is not an estimate. R1 already makes the governor account every request the tool issues, scheduler polls included, so it already holds the number. With the Feed at [polling-scheduler](../polling-scheduler/requirements.md) R11's 312 points/min the ceiling is ~117 deletes/min, which is **~1.96/sec**. With no reads in flight it is 180/min, and R11's 2.5/sec cap binds first.

The cap and the floor are unchanged and are the ramp's own limits rather than the pool's. 2.5/sec holds ~17% back from the points model's ~180/min. The floor keeps a run of backoffs slowing the writes rather than stalling them.

**A static ceiling was wrong, and by enough to matter.** At 2.5/sec a Purge spends 750 points/min (150 deletes x 5). The Feed spends 312. Their sum is 1,062 against a pool of ~900, and [purge](../purge/requirements.md) R14 **requires** the Feed to keep updating while a Purge runs, so that collision is the normal case and not an edge one. R10's additive increase crosses the wall about four steps in, at 2.0/sec. The published points model is a budget for the whole token, not a private allowance for deletion, and a ceiling that ignored the reads was spending the same points twice.

**R12.** The governor must back off hard on any 403, any 429, and any `Retry-After`, and must honour a `Retry-After` interval where one is supplied.

**R13.** The governor must never count a rate-limit response as a failure of the operation. It feeds backoff, and the affected request must be re-attempted rather than recorded as failed. Continuing to issue requests while limited risks a ban, and recording a limit as a failure would trip [purge](../purge/requirements.md) R21's circuit breaker on an account that is merely busy.

**R14.** The governor must expose, per response, whether it was a rate-limit response, so consumers can apply R13 without re-deriving the classification themselves.

**R15.** The governor must adapt to the account it is running against rather than to a compiled constant. Enterprise Cloud has 3× the primary allowance, GitHub Apps scale differently, and a token shared with CI has less than it appears. Any fixed number is wrong for somebody, and the governor is the only party in a position to observe which.

**R16.** The governor must price every request at the published points model's method-level default, and must not pretend to endpoint-level precision the model does not offer. The model prices by method: "Most" GET, HEAD and OPTIONS requests cost 1 point, and "Most" POST, PATCH, PUT and DELETE requests cost 5. Every write R2 names is a POST, a PUT or a DELETE, so all ten carry the same published 5-point default, and DELETE's figure was never anything more than this: an earlier form of this requirement read the table's mention of DELETE as endpoint-specific documentation, and it is not. The precision cap is GitHub's own sentence, "Some REST API endpoints have a different point cost that is not shared publicly", so no endpoint's exact cost is knowable, DELETE included, and none is measurable: measuring means tripping the secondary limit, which PRD risk R4 permanently forbids. That undisclosed variance is part of why R11's cap holds real headroom and why the ramp feels for the wall instead of trusting the arithmetic. See open question 3, resolved, and [docs/research/write-point-costs.md](../../research/write-point-costs.md).

### What is not configurable

**R17.** Deletes-per-second must not be exposed as a setting, by flag, config key or environment variable. The only thing that knob does is let someone raise it and get their account blocked, and the adaptive governor already beats any number a person could pick, because it observes what their account actually tolerates.

**R18.** Requests-per-second, concurrency and backoff parameters must likewise not be exposed. The single permitted user-facing knob is intent: what share of the Budget gh-runs may spend. That is a question answerable from the user's own context, unlike one that requires the points model, their token tier and their repo count.

**R19.** The governor must honour the intent-level Budget share over reads alone, and must never let it throttle a Purge. The Budget is a share of the **primary** limit, which is observable and is what polling spends. A Purge's throughput is bound by the **secondary** limit, which exposes no counter of its own. A share of the primary limit says nothing about the secondary one, so a Purge running under a 25% Budget deletes at the governor's full ramped rate.

**This is independence on the Budget, not independence outright.** Polling and purging do share the secondary pool, which is exactly what R11's dynamic ceiling exists to arbitrate. The two mechanisms answer different questions and must not be collapsed into one: the Budget is a policy the user sets over their primary allowance, while the ceiling is an arithmetic fact about a secondary pool nobody can read. See [ADR-0007](../../adr/0007-adaptive-delete-throttle.md).

### Delivery and seams

**R20.** The governor must pace a Purge of 18,258 Runs to completion without being rate limited and without manual babysitting. Stated against the rates this governor can actually reach: **~2 hours** at R11's 2.5/sec cap (150/min), **~10 hours** at its 0.5/sec floor (30/min), and **~155 minutes** in the normal case, where the Feed is running and R11's dynamic ceiling settles near 1.96/sec. Adaptive lands inside that band and self-tunes.

The published-limit band of ~100 minutes to ~5 hours is a different claim about different numbers, and it appears in this document's Constraints table where it belongs. The governor reaches neither end of it: the points model's ~180/min is above R11's cap, and the prose's ~60/min is above R11's floor.

**R21.** All of the governor's timing (the inter-write interval, the ramp's dwell, every backoff and every reset wait) must come from an injected clock. This is the highest-value target for that seam in the whole project: ramp and backoff logic is pure timing, and a test that sleeps through it is a test nobody runs.

**R22.** Tests of the ramp and backoff must be deterministic and instant. A test of R20's 18,258-Run Purge must complete in milliseconds of real time while virtual time advances across hours, and no test may sleep through a real interval.

**R23.** The governor must be exercisable end-to-end against recorded HTTP fixtures, with no live network, including recorded 403, 429 and `Retry-After` responses and the interleaved 200/304 measurement from [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md). Cassettes replay true payloads, which is what catches the API drifting. A hand-written fake would encode our beliefs about rate limiting and stay green while reality moved. And this API has real surprises.

## Acceptance criteria

**AC1: Cold start rate.** From cold, the first write is issued at ~1/second. The interval is measured on the injected clock and no test sleeps.

**AC2: The AIMD ramp.** Replayed against a cassette and the injected clock, with no test sleeping. 20 consecutive clean responses step the rate from 1.0/sec to 1.25/sec, and 19 do not. A 403 arriving mid-stream halves the current rate on the spot, and the ramp restarts its count from the halved rate. Across the whole replay the rate never exceeds R11's 2.5/sec cap and never falls below its 0.5/sec floor. **No 60s window of virtual time spends more than ~900 points in total, counting reads at 1 point and DELETEs at 5.** That total is the claim worth checking. The old form of this criterion asserted the rate stayed under "the ~180 points ceiling", which was wrong twice over: 180 is writes per minute rather than points, and R11's cap already holds writes at 150/min, so nothing could ever have failed it.

**AC2a: The ceiling tracks the reads.** Replayed with the scheduler polling 26 repositories at 5s (312 points/min), the ramp stops at ~1.96/sec rather than at 2.5/sec, and no 60s window exceeds ~900 total points. Replayed with the same cassette and no reads in flight, the same ramp reaches R11's 2.5/sec cap and stops there. The observed ceiling differs between the two runs, and the governor is given no configuration to tell them apart.

**AC3: Purge at reference scale.** A cassette of 18,258 successful DELETEs completes in milliseconds of real time, advancing virtual time across the run. No 60s window of virtual time spends more than ~900 points in total, and at no instant does the issue rate exceed R11's dynamic ceiling for the reads observed in that window.

**AC4: Backoff on 429.** Given a 429 with `Retry-After: 60`, no write is issued until virtual time has advanced 60s, the affected Run is re-attempted, and the operation's failure count remains 0.

**AC5: Backoff on 403.** Given a 403 carrying rate-limit signals, the issue rate falls, the request is re-attempted, and it is not recorded as a failure.

**AC6: Rate limits are not failures.** Given a sustained sequence of rate-limit responses, none is reported as a failure to any consumer, and R14's classification marks each as a rate-limit response.

**AC7: Header authority.** Given responses whose `x-ratelimit-remaining` falls faster than the governor's own tally predicts (traffic from another consumer of the same token), the published remaining Budget matches the header, not the tally.

**AC8: 304 is free.** Replaying ADR-0004's interleaved measurement, the governor's primary accounting advances by exactly one per round, attributable to the 200. Each 304 advances it by zero.

**AC9: Exhaustion.** Given `x-ratelimit-remaining: 0` and `x-ratelimit-reset` at 14:32, the governor publishes exhaustion with a resumption time of 14:32, and issues nothing until virtual time passes it.

**AC10: Exhaustion without a time.** Given exhaustion with neither `x-ratelimit-reset` nor `Retry-After`, the governor publishes exhaustion with no time. It does not synthesise one.

**AC11: Pressure is a projection, and routine work does not trip it.** Replayed against a cassette and the injected clock, with no test sleeping. Given the reference cold start (~163 discovery probes and ~26 Feed payloads inside ten seconds) against headers reporting 4,811 remaining and a reset 45 minutes out, the governor reports no pressure and no Budget Readout is rendered anywhere. Given a sustained burn the same predicate projects past the reset, it reports pressure, and the Readout renders. The two runs differ in the cassette's headers and in nothing else: no percentage of the limit appears in the test, and the governor is given no configuration to tell them apart (R8a).

**AC11a: `/rate_limit` is never the source.** Across every replay in this suite, the governor issues zero requests to `/rate_limit`, and its published primary accounting is reproducible from the response headers alone. Given a cassette in which `/rate_limit` reports `used: 90` while the headers report `used: 62` climbing to 78, the Readout follows the headers (R5a).

**AC11b: Zero burn is not pressure, and divides by nothing.** Given a five-minute window of the injected clock in which every response was a 304, the governor reports no pressure whatever `remaining` says, and performs no division. Given `remaining: 0`, it reports pressure and exhaustion together, and AC9's resumption time is unaffected (R8a, R7).

**AC12: One global throttle.** A Purge spanning 3 repositories issues writes at the same aggregate rate as a Purge of the same size confined to 1. The rate does not scale with repository count.

**AC13: Nothing to turn up.** No flag, config key or environment variable sets deletes-per-second, requests-per-second, concurrency, or any backoff parameter. Setting the intent-level Budget share changes the observed rate. Nothing sets the rate itself.

**AC14: The Budget governs polling, never a Purge.** A configured Budget share is respected across a read workload. The same share applied to a workload mixing polls and deletes leaves the deletes at the governor's full ramped rate, because a Purge is bound by the secondary limit and the Budget is a share of the primary one.

## Constraints

**GitHub gives two answers for how fast you may write, and they disagree by 3×:**

| Source | Rule | Implied ceiling | An 18,258-Run Purge |
|---|---|---|---|
| Points model | DELETE costs 5 points against ~900 points/min | ~180/min | ~100 minutes |
| Written guidance | wait at least one second between writes | ~60/min | ~5 hours |

**Both rows are documented rather than measured**, and the distinction is load-bearing. Neither number came from an experiment, because establishing a point cost by observation means driving the account into the secondary limit to find its edge, which PRD risk R4 forbids permanently. They are GitHub's published model, re-verified as current: ~900 points/min, DELETE at 5 points, still live. The word "measured" was applied to this table in error and is corrected throughout.

**The band in the last column is the published one, and this governor reaches neither end of it.** R11's cap holds writes at 150/min against the model's 180, and R11's floor sits at 30/min against the prose's 60. R20 states the reachable band. Where a document means these two published limits and their disagreement, it must say so.

That disagreement is the entire reason this component is adaptive rather than a constant ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)). Neither number can be dismissed: the points model is GitHub's own arithmetic, and the prose is GitHub's own advice.

**Reads and writes spend the same ~900 points/min.** GET costs 1 point ([polling-scheduler](../polling-scheduler/requirements.md) R11), DELETE costs 5, and the pool is one pool. A Feed at 312 points/min plus a Purge at R11's 2.5/sec cap (750 points/min) totals 1,062 against ~900, and [purge](../purge/requirements.md) R14 requires the Feed to keep running throughout. That is why R11's ceiling is dynamic. The Budget's independence from a Purge (R19) is a fact about the **primary** limit and has never been a claim about this pool.

**The penalty for being wrong is asymmetric.** Too slow costs the user time. Too fast costs them a block on their account. That asymmetry is why R10 starts at the documented-safe rate and ramps, rather than starting at the ceiling and backing off.

**`x-ratelimit-remaining` and `x-ratelimit-reset` arrive on every response at no cost**, and are currently discarded. The Budget model is free. They are reachable only below go-gh's client, which returns an error and drops the response ([ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md)). R5 is a requirement on a RoundTripper, not on a caller.

**`/rate_limit` disagrees with the headers and reads high.** Measured: `core` frozen at `used: 90` for minutes while the header counter on the same resource climbed 62 to 78, resets near-identical. A frozen number that reads **above** the truth is not a lagging snapshot, and R7 accounts `used`, so seeding from it starts the baseline wrong by 12 to 36. `/rate_limit` is free (three back-to-back calls did not advance it), which is what makes R5a's prohibition worth writing down rather than obvious.

**A 304 costs zero primary allowance. A 200 costs one.** Measured by interleaving: `used` 120 → 121 → 122, the increment belonging entirely to the 200 ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)). We assume conservatively that 304s do count against the **secondary** limit (PRD risk R4).

**Every write carries the same published default of 5 points, and none is exact.** The points model prices by method rather than by endpoint: "Most REST API POST, PATCH, PUT, or DELETE requests" cost 5 points. Cancel, force-cancel, re-run, Dispatch, and Workflow enable and disable are priced by the same table row as the four DELETEs, and so is the caveat: "Some REST API endpoints have a different point cost that is not shared publicly." None of the ten can be measured either, for R16's reason. Full citations in [docs/research/write-point-costs.md](../../research/write-point-costs.md).

**Content creation is a separate secondary dimension with a lower number.** "In general, no more than 80 content-generating requests per minute and no more than 500 content-generating requests per hour are allowed", some endpoints have lower limits still, and the count includes actions taken on the web interface. The docs never define which requests are content-generating. Re-run and Dispatch create Runs, so the conservative reading puts them under it, and 80/min sits well below the ~180/min the points model prices writes at, so wherever this dimension applies it binds first. A Purge is DELETEs, which remove content rather than create it, but that reading is ours and not GitHub's. What keeps this a constraint rather than a crisis is that the governor never trusts arithmetic anyway: R12 backs off on the response, whichever dimension produced it, and "You may also encounter a secondary rate limit for undisclosed reasons" is GitHub's own warning that the ledger cannot be complete.

**Enterprise Cloud has 3× the primary allowance**, GitHub Apps scale differently, and a token shared with CI has less headroom than its limit suggests. There is no fixed number that is right for every account.

**v1 chose `sleep 0.25`**, ~4 writes/sec, faster than *both* published numbers. It works on small repositories and would be blocked on a large one. That is the failure mode this component replaces.

**A Purge cannot be a modal**, because at reference scale this governor runs it for ~155 minutes in the normal case, and for as long as ~10 hours at R11's floor (R20). The governor's pacing is what makes that duration a background fact rather than a hostage situation.

## Open questions

1. **Resolved by body, not by header, and it fails safe toward backoff.** R12 sends a 403 to hard backoff. [repo-discovery](../repo-discovery/requirements.md) R10 and [purge](../purge/requirements.md) R20 send a 403 to an authorization outcome. Both are canon, and they demand opposite handling of the same status code.

    **An authorization 403 is measured.** `GET /repos/cli/cli/actions/permissions` on a token without admin returns HTTP 403 carrying a `documentation_url` that points at **the endpoint's own reference page**, a `message` naming the missing permission, no `retry-after`, and `x-ratelimit-remaining: 4976`.

    **A secondary-limit violation is documented rather than measured**, deliberately, because provoking one is the hazard PRD R4 refuses. GitHub documents 403 **or 429**, "an error message that indicates that you exceeded a secondary rate limit", and an optional `retry-after`. It does **not** publish the exact message text, so we must not match on a string we have never seen.

    **`x-ratelimit-remaining` is not the discriminator, and the temptation to use it is a trap.** Those headers describe the **primary** limit only (PRD). A secondary-limit 403 can arrive with a completely healthy primary remaining, exactly as the authorization 403 above did. The two are indistinguishable on that header.

    **The rule: a 403 or 429 is an authorization outcome only when it matches the measured authorization shape.** Everything else is rate limiting. `retry-after` present means rate limiting outright. This is asymmetric on purpose: misreading a rate limit as authorization keeps issuing and risks the account block this component exists to avoid. Default to backing off. Verify the shape against a live secondary limit only if one is ever hit in the wild, never by provoking one.

    **The rule needs a bound, because without one the safe direction is an infinite loop.** An earlier wording of this question claimed that misreading authorization as a rate limit "costs one backoff before the retry fails the same way and resolves". That was false, and [repo-discovery](../repo-discovery/requirements.md) open question 2 said so correctly: it "stalls a Purge behind a backoff that will never clear". Trace it. R12 backs off on any 403. R13 forbids counting a rate limit as a failure, so [purge](../purge/requirements.md) R21's circuit breaker never advances. R11's floor keeps issuing at 0.5/sec forever. Nothing resolves, because an authorization 403 has nothing to wait for. And [purge](../purge/requirements.md) R13 makes this the **expected** case rather than a corner: a 403 despite `push: true` is what a fine-grained PAT does. The safe-by-default rule, unbounded, hammers GitHub with 403s at 0.5/sec indefinitely, which is the account block this component exists to prevent.

    **The bound: after three consecutive rate-limit classifications on the same Run, reclassify as an authorization failure.** The Run is then [purge](../purge/requirements.md) R20's to skip and record, the Purge continues, and the counter resets on any clean response.

    **Three, because three is what backoff has to give.** R10 halves on each rate-limit response, so three consecutive halvings carry the rate from R11's cap to its floor (2.5 to 1.25 to 0.625, then floored at 0.5). A fourth backoff cannot slow anything further, so past that point the classification is buying nothing at all. A genuine secondary limit clears within the interval GitHub supplies, and a genuine authorization 403 never clears. When three backoffs and their waits have not changed the answer, the remaining explanation is authorization. The asymmetry survives: three wasted backoffs on a real rate limit is a cheap mistake, and it is still the direction we default to.

2. **Resolved: the secondary limit is not observable, and the design already assumed so everywhere else.** This question called it UNKNOWN while three other places in the canon treated it as settled. The [PRD](../../PRD.md) carries it as a constraint that shaped a decision. [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) states the measurement outright: responses carry `x-ratelimit-limit`, `-remaining`, `-used`, `-reset` and `-resource` for the **primary** limit only, and `/rate_limit` exposes `core`, `graphql`, `search`, `code_search`, `audit_log`, `scim` and friends, and **none of them describe the secondary limit**. R19 and open question 7 both rest on that answer. The question was the outlier, not the canon.

    **What follows is exactly what the design already does.** All secondary accounting (R11's ceiling included, and every number in [polling-scheduler](../polling-scheduler/requirements.md) R11) is a projection from our own issued requests against the published points model, computed by a client that cannot see the other consumers of the same token. The only feedback is the 403 or 429 that means we already overshot. That is why R11's headroom must be real, why R12's backoff must be hard, and why open question 5 chose a control law rather than a constant.

    **One narrow thing stays genuinely unverified**, and it is not this question: what a live secondary-limit response actually carries. Open question 1 covers it, resolves the discrimination rule by body shape rather than by header, and declines to provoke one.

3. **Resolved: the published points model prices by method, and one table row settles all six at DELETE's own number.** The model reads "Most REST API GET, HEAD, and OPTIONS requests: 1" point and "Most REST API POST, PATCH, PUT, or DELETE requests: 5" points. Cancel, force-cancel, re-run and Dispatch are POSTs, enable and disable are PUTs, so every write R2 names carries the same published 5-point default, and this question's premise was a misreading: DELETE never had endpoint-specific documentation, its 5 always came from the same method-level row as everything else. Documentation was what would close the question, and the documentation was already there.

    **What remains unknown is symmetric, published as unknowable, and unmeasurable.** "Some REST API endpoints have a different point cost that is not shared publicly", so no endpoint's exact cost is certain, DELETE and the 1-point reads included, and PRD risk R4 permanently forbids measuring any of it. R16 now prices every request at the method default and leaves the undisclosed exceptions to the ramp, which is what open question 5 built the ramp for.

    **The reading surfaced one genuinely new number.** The content-creation dimension caps content-generating requests at 80/min and 500/hour, below the ~180/min the points model allows writes, with no published list of which requests it covers. The Constraints section carries it. Raised by [run-lifecycle](../run-lifecycle/requirements.md) open question 3, resolved for both. Full citations in [docs/research/write-point-costs.md](../../research/write-point-costs.md), retrieved 2026-07-19.

4. **Resolved: the ramp caps at 2.5/sec against a documented ~3/sec, and never drops below 0.5/sec.** R11 required headroom and did not size it. Open question 5's ramp sizes it: 2.5/sec is 150/min against the ~180/min the published points model allows, so ~17% is held back. The floor of 0.5/sec is the other end of the same decision, because a multiplicative decrease with nothing under it converges on a stall. Neither number is load-bearing on its own, and that is the point of choosing a control law over a constant: AIMD approaches the account's real tolerance from below, so 2.5/sec caps the search rather than naming a target to converge on. R10 and R11 carry the numbers. AC2 verifies them.

    Two corrections since. The ~3/sec is **documented, never measured**, and calling it measured was wrong: it comes from the published points model, and measuring it would mean tripping the limit PRD risk R4 forbids. And 2.5/sec is now a **cap on a dynamic ceiling** rather than the ceiling itself, because reads and writes share the secondary pool and a Purge runs while the Feed polls. R11 carries the arithmetic.

5. **Resolved: the ramp is AIMD, additive increase and multiplicative decrease.** Start at 1.0 deletes/sec. Add 0.25/sec after every 20 consecutive clean responses. Halve the current rate on a rate-limit response and re-ramp from there. Ceiling 2.5/sec, floor 0.5/sec.

    **AIMD is the control law TCP uses for this exact problem**: an unobservable ceiling where overshoot is expensive. That is open question 2's situation restated. The secondary limit exposes no counter, so the only feedback is the 403 that means we already overshot, and a governor that cannot see the wall must feel for it.

    **It converges, it climbs slowly, and it retreats fast.** Those three properties are what the asymmetry in the Constraints section asks for. Overshoot costs one halving and a re-ramp. It does not cost a blocked account. Additive increase means the cost of probing is bounded and linear, while multiplicative decrease means the cost of being wrong is paid once and immediately.

    **The endpoints were already canon** (start at 1/sec, ramp toward the ceiling, back off hard) and the shape between them was not. R10 and R11 now carry it, and AC2 pins the behaviour against a cassette and the injected clock rather than against prose.

6. **Resolved: pressure is projected exhaustion, not a fixed percentage.** Consumption is under pressure when the current burn rate would exhaust the remaining allowance before it resets: `remaining / burn < time_to_reset`. R8a carries the predicate, AC11 and AC11b pin it, and [BUILD-ORDER](../../BUILD-ORDER.md) no longer names this as the stage-2 blocker.

    **A compiled constant is what [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) already rejected one floor down**, on grounds that transfer whole: Enterprise Cloud has 3× the primary allowance, GitHub Apps scale differently, and a token shared with CI has less headroom than its limit suggests. A percentage is also wrong in both directions on one account. 75% consumed with 50 minutes to reset is fine and a percentage shouts. 60% consumed and burning fast is doomed and a percentage stays silent.

    **The window is the part that is still a constant, and R8a says so rather than hiding it.** Five minutes, divided by its full width and never by its elapsed part, so a cold-start burst of ~189 requests reads as ~38/min and stays silent where a one-minute window would fire on it. That choice biases toward under-reporting, which is the safe direction when [live-run-feed](../live-run-feed/requirements.md) R29's failure mode is a readout nobody believes.

    **What this left to [polling-scheduler](../polling-scheduler/requirements.md) open question 4 has since been settled there.** Pressure now has an onset, so R15's first demotion has a moment, and [ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md) chose the staging: slow alone at onset, escalating tier by tier with this question's five-minute window as the dwell, and every demotion held until the reset.

7. **Resolved: one Budget share, over the primary limit, and a Purge is not subject to it.** R19 previously applied one share to reads and writes alike, which contradicted [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) outright. The ADR wins, on measurement: the two limits are different currencies, so a share of the primary one has nothing to say about a Purge bound by the secondary one. R19 and AC14 are corrected. The secondary limit gets no share of its own, because it exposes no counter to take a share of, and its control is the ramp rather than a budget. The observation that a workload can be comfortable on one limit while pinned against the other still holds and is exactly why the two are governed differently.

## Related

- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R10, R11, R12, R17, R21.
- [ADR-0012: The transport chain, and what ghclient may expose](../../adr/0012-transport-chain-and-the-client-surface.md). Why R5 is a RoundTripper's requirement and not a caller's, and where this component sits in the chain.
- [ADR-0018: The fan-out's concurrency shape](../../adr/0018-the-fanout-concurrency-shape.md). The concurrency limiter under this governor, and R9's exhaustion-on-classification rule.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). R7's measurement. Why the secondary limit binds.
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R20's duration is survivable because a resume is free.
- [purge](../purge/requirements.md). R2 paces its R17. R13 implements its R19. R3 settles its open question 7.
- [polling-scheduler](../polling-scheduler/requirements.md). Consumes R8's Budget Readout. R4 draws the line between them, and R11 prices its reads into the write ceiling.
- [repo-discovery](../repo-discovery/requirements.md). Its probe burst is accounted here. Shares open question 1.
- [run-lifecycle](../run-lifecycle/requirements.md). Paced by R2. Owns open question 3's origin.
- [workflow-management](../workflow-management/requirements.md). Its R5's enable and disable are two of R2's ten writes, and their point cost is open question 3's.
- [cli-surface](../cli-surface/requirements.md). Its R14 throttle is this component.
- [live-run-feed](../live-run-feed/requirements.md). R8 and R9 supply its R29 and R30.
- [settings](../settings/requirements.md). Owns R19's intent-level share, and owns what R17 and R18 refuse to expose.
