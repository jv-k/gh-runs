# Polling Scheduler

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

The scheduler decides which repositories are revalidated and how often, so that a Run invoked anywhere appears in the Feed without interaction while idle polling costs ~nothing. It is the component that turns [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s free 304s into liveness without tripping the secondary limit.

## Requirements

### What is polled

**R1.** Every scheduled poll must be a conditional request carrying the persisted ETag for that resource. An unconditional poll of the steady-state poll set is a defect: 26 repositories at 12 polls/min is ~18,720 requests/hour against a 5,000/hour primary allowance.

**R2.** The scheduler must poll only the repositories [repo-discovery](../repo-discovery/requirements.md) classified as having Runs (~26 for the reference account). It must never adopt the ~163-repository discovery probe set as a poll set.

**R3.** The scheduler must adopt changes to the poll set without a restart. A repository newly classified as having Runs must enter the rotation at the slow tier. One that disappears must leave it.

**R4.** The scheduler must schedule by Status and visibility alone, never by capability. A repository the account cannot write to must poll at the same tiers as one it can. Watching CI on a repository you do not administer is a primary use case ([live-run-feed](../live-run-feed/requirements.md) R20).

### Tiers

**R5.** The scheduler must operate three tiers:

| Tier | A repository qualifies when | Target interval |
|---|---|---|
| Fast | its most recent Runs include at least one whose Status is `queued` or `in_progress` | ~3s |
| Medium | it is on screen: it has at least one row in the Feed's current viewport | ~5s |
| Slow | it is in the poll set and qualifies for neither of the above | ~30s |

**Both qualifying rules are [ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)'s, and each replaces an ambiguity.** Fast previously read "any Status short of `completed`", which handed days of ~3s polling to a Run parked at `waiting` on a deployment approval. The parked statuses (`waiting`, `requested`, `pending`) now qualify a repository for nothing: its next event is a human acting, the approval's effect surfaces within the ambient tier's interval, and the fast tier resumes on `queued`. Medium previously read "the repository or section the user is currently looking at", whose widest reading was the whole poll set at 5s. The viewport reading is self-capping at terminal height, recency-targeted by the `run_started_at` sort, and filter-aware for free. It makes a scroll a scheduling input, which the Feed publishes and R3's live set-change machinery already accommodates.

**R6.** Tier selection must read Status and must never read Conclusion. Conclusion is null until Status reaches `completed`, so a Conclusion-driven tier would be null exactly when liveness matters and populated exactly when it does not. Status and Conclusion are two different fields.

**R7.** A repository qualifying for more than one tier must be polled at the fastest tier it qualifies for. Being on screen must never slow a repository that has a live Run.

**R8.** The scheduler must meet the Feed's liveness contract: a Run invoked elsewhere must surface within ~30s with no user interaction, and a repository already containing a Run whose Status is `queued` or `in_progress` must reflect changes within ~3s. The ~3s figure matches `gh run watch`'s default.

**R9.** The scheduler must provide a debounce of ~150ms on selection settle for selection-driven fetches, so that moving the cursor through the Feed issues one request where the cursor lands rather than one request per keystroke. [run-detail](../run-detail/requirements.md) R10 is the consumer. The timing seam is the scheduler's.

**R10.** The scheduler must make a response attributable to the request that asked for it, so that a response whose reason has passed (a Job fetch for a Run the cursor has already left) can be discarded rather than rendered.

### Budget

**R11.** The scheduler must compute its own intervals from the size of the poll set and the points model, and must reduce frequency so that projected consumption stays under the secondary limit. The maths, assuming 304s do count against the secondary limit (~900 points/min, GET = 1 point):

| Poll set | Interval | Points/min | Verdict |
|---|---|---|---|
| 26 repos | 5s | 312 | fine |
| 26 repos | 2s | 780 | at the edge |
| 26 repos | 1s | 1,560 | over |
| 100 repos | 5s | 1,200 | over |

**R12.** Poll interval must not be exposed as a user setting, by flag, config key or keybinding. Choosing it correctly requires the token tier, the repo count and the points model. The scheduler has all three and the user has none. The only knob that may exist is intent (what share of the Budget gh-runs may spend), which is a question answerable from the user's own context.

**R13.** The scheduler must scale against the allowance actually remaining, including consumption it did not itself issue, taking its figure from the [rate-governor](../rate-governor/requirements.md)'s Budget Readout rather than from a tally of its own requests. A Purge's crawl and any other consumer of the same token spend the same **primary** allowance this scheduler does.

**A Purge's deletes are the exception, and the distinction is not a quibble.** They spend the **secondary** pool, and the Budget is a share of the primary limit ([rate-governor](../rate-governor/requirements.md) R19), so a Purge's writes never draw down the allowance R15 demotes against. Reads and writes do still share the secondary pool, which is what rate-governor R11's dynamic ceiling arbitrates, and it arbitrates it by slowing the **Purge** against this scheduler's observed read rate rather than the reverse. So R13 is a requirement about primary consumption. This scheduler never demotes because a Purge is deleting.

**R14.** The scheduler must not parse rate-limit headers, maintain its own Budget accounting, or throttle writes. It consumes the **Budget Readout** ([CONTEXT.md](../../CONTEXT.md)) from the rate-governor, which owns all three. The Readout is a reading of what is left. The Budget is the share the user permitted. The scheduler consumes the first and honours the second, and must not confuse them.

**R15.** Under Budget pressure the scheduler must demote tiers automatically, before anything breaks, rather than continuing until it is refused. Demotion must sacrifice the least visible work first: the slow background tier degrades before the on-screen medium tier, which degrades before the fast tier tracking a live Run.

**Demotion is staged, compounding, and held until the reset ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** At [rate-governor](../rate-governor/requirements.md) R8a's onset the slow tier alone demotes. Pressure persisting a full five-minute burn window demotes the medium tier, and five more the fast, the dwell being R8a's own window because that is how long the measurement takes to reflect the previous stage. Each escalation step doubles every already-demoted tier's interval again and joins the next tier at twice its target: slow 60s, then slow 120s and medium 10s, then slow 240s, medium 20s and fast 6s, doubling on while pressure persists, with no cap. No demoted tier is promoted inside the rate-limit window. At the reset instant the schedule recomputes from undemoted tiers, and escalation restarts at stage one if pressure re-fires. The hold is what closes the projection's own loop (demote, burn falls, predicate clears, promote, repeat): the reset is the condition's natural end and is never more than 60 minutes away, so nothing oscillates and no band or dwell constant exists to tune.

**R16.** At Budget exhaustion the scheduler must stop polling and state when it resumes ("resumes 14:32"), taking the time from the rate-governor. It must never allow the Feed to go quietly stale. A paused Feed that says so is correct. One that looks live and is not is the failure this requirement exists to prevent.

**A secondary-limit backoff rides this same path ([ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md)).** A poll answered with a rate-limit 403 or 429 is an account condition, not a repository failure, so it never becomes a `RepoPollFailed`. The governor publishes the Readout as exhausted with the `Retry-After` derived resume, and this requirement's behaviour covers it unchanged: scheduling stops, in-flight polls complete and emit, and the Feed states the pause. A poll's re-attempt is its next scheduled tick after the resume.

### Fan-out

**R17.** The scheduler must fan out with bounded concurrency. Serially, ~26 round-trips is ~5s per refresh, which exceeds the fast tier's ~3s target before a single repository has been considered.

**R18.** The concurrency bound must be chosen by the tool and must stay well below the published cap of 100 concurrent requests, a pool shared across the REST and GraphQL APIs. See open question 2 for the verification.

**The bound is chosen: 10, fixed, process-global, and innermost in the transport chain ([ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md)).** It is octokit's own figure and a tenth of the published cap. It is not this scheduler's to enforce: the limiter is a RoundTripper under the governor, so every request the tool issues shares the one pool, discovery's burst and a Purge's crawl included. No state changes it. Pressure moves R15's intervals and never the bound, because shrinking wire concurrency saves zero points. Within the fan-out, polls are single-flight per repository: a tick that comes due while its repository's previous poll is still in flight is skipped, and cancellation exists only for quit.

**R19.** The scheduler must distinguish a 304 from a 200 and must do no re-render work for a 304. Both count against the secondary limit under R11's pessimistic assumption. Only the 200 carries a change or costs primary allowance.

### Seams

**R20.** All of the scheduler's timing must come from an injected clock. Every interval, every tier decision, every debounce and every backoff must be driven by it, with no reliance on wall-clock time.

**R21.** Tests of the scheduler must be deterministic and instant: a test asserting the ~30s slow tier must advance virtual time and complete in microseconds, and no test may sleep through a real interval. This is a design constraint from day one, not a retrofit. A scheduler that reads the wall clock directly cannot be made testable later without rewriting its control flow.

**R22.** The scheduler must be exercisable end-to-end against recorded HTTP fixtures, with no live network, including a 200-with-ETag followed by a 304 for the same resource. Cassettes replay true payloads, which is what catches the API drifting. A hand-written fake encodes today's beliefs and stays green while reality moves.

## Acceptance criteria

**AC1: Deterministic intervals.** A test advancing 30s of virtual time observes exactly one poll of each slow-tier repository and completes without sleeping. Real elapsed time is not a factor in any assertion.

**AC2: Conditional always.** Every poll issued in the steady state carries an `If-None-Match` header. No steady-state poll is unconditional.

**AC3: Tier by Status.** A repository whose Run list contains a Run at Status `in_progress` is polled at ~3s. When that Run's Status reaches `completed`, the repository falls back to medium or slow at the next scheduling decision. A Conclusion appearing does not by itself change any tier.

**AC4: Fastest tier wins.** A repository that is both on screen and holds a Run at Status `queued` is polled at ~3s, not ~5s.

**AC5: Capability is not a tier input.** A repository with `push: false` at the same Status and visibility as one with `push: true` is polled at the same interval, and their poll counts over an interval of virtual time are equal.

**AC6: The poll set is discovery's.** Over any interval, the set of repositories polled is a subset of the ~26 classified as having Runs. None of the other 137 is ever polled.

**AC7: Poll set changes live.** A repository added by a discovery re-probe begins polling at the slow tier with no restart, and one removed stops being polled within one interval.

**AC8: Budget maths.** With a poll set of 26 at 5s, projected consumption is 312 points/min. With a poll set of 100, no schedule is produced that polls all of them at 5s, because 1,200 points/min exceeds the ~900 ceiling. The interval auto-scales instead.

**AC9: No interval knob.** No flag, config key or keybinding sets a poll interval. Setting the intent-level Budget share changes observed request rate. Nothing sets seconds.

**AC10: Demotion.** With the rate-governor reporting Budget below the configured share, the requests issued per minute of virtual time strictly decrease, and the slow tier's rate falls before the fast tier's.

**AC11: Exhaustion is explicit.** With the rate-governor reporting exhaustion and a reset at 14:32, the scheduler issues zero polls until virtual time reaches 14:32, and the resumption time it publishes is 14:32.

**AC12: Foreign consumption counts.** With the rate-governor reporting remaining Budget consumed by traffic the scheduler did not issue, the scheduler demotes exactly as it would for its own consumption.

**AC13: Debounce.** Twenty selection changes within 150ms of virtual time produce exactly one selection-driven fetch: the one for the final selection.

**AC14: Stale response discarded.** A response for a request whose selection has since moved is discardable and is attributable to the superseded request.

**AC15: Concurrency bound.** At no instant of virtual time are more than the configured number of requests in flight. No user-facing setting alters the bound.

**AC16: 304 does no work.** A 304 triggers no re-render and decrements the primary allowance by zero. A 200 with changed content is delivered to the Feed.

**AC17: The viewport is the medium tier.** With repositories A and B in the poll set, A holding a visible row and B scrolled out of view, and neither holding a Run at `queued` or `in_progress`, A is polled at ~5s and B at ~30s. Scrolling B into view moves it to ~5s at the next scheduling decision, with no restart.

**AC18: A parked Run rates no tier.** A repository whose only non-completed Run sits at `waiting` is polled at ~5s while visible and ~30s otherwise, over any interval of virtual time. When that Run's Status reaches `queued`, the repository is polled at ~3s at the next scheduling decision.

**AC19: Demotion stages on the injected clock.** With the Readout reporting pressure continuously from t=0: the slow tier polls at 60s intervals from onset while medium and fast are unchanged, the medium tier polls at 10s and slow at 120s from t=5min, and fast polls at 6s, medium at 20s and slow at 240s from t=10min. At t=4min59s the medium tier still polls at ~5s.

**AC20: A demotion holds until the reset.** With pressure reported at t=0, clearing at t=6min, and the reset at t=40min, no tier is promoted before virtual time reaches t=40min. At t=40min the schedule recomputes at undemoted intervals, and a fresh onset restarts escalation at the slow tier alone.

## Constraints

**Conditional requests are free against the primary limit, and that is the entire economic basis of this feature.** Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), and that one belonged entirely to the 200. Polling ~26 repositories every few seconds is ~3,600 requests/hour at ~0 primary cost while idle.

**The binding constraint is therefore the secondary limit, not the primary one**: ~900 points/min, GET = 1 point. This is why the scheduler tiers and auto-scales rather than exposing an interval, and it is why R11's table is the shape of this feature.

**That pool is shared with deletion, and this scheduler wins.** A Purge spends 5 points per DELETE from the same ~900, so R11's 312 points/min is not free of a Purge's throughput, it is subtracted from it. [rate-governor](../rate-governor/requirements.md) R11 makes the write ceiling a function of the reads this scheduler is observed to issue, `(900 - reads) / 5` deletes per minute, which is ~117/min (~1.96/sec) at 312. The Feed's liveness is not negotiated down to let a Purge finish faster.

**We assume 304s do count against the secondary limit** (PRD risk R4). The primary exemption does not imply a secondary one, and confirming it would mean deliberately tripping a limit and risking a block on the user's account. Every number in R11 is computed on the pessimistic assumption.

**There is no cross-repository Run query.** Not in REST, not in GraphQL, not in `search` ([ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)). Liveness across N repositories is N requests per round, which is why the poll set's size is an input to the interval.

**~26 serial round-trips are ~5s per refresh**, which is why R17's concurrency is required rather than an optimisation.

**Conclusion is null until Status reaches `completed`.** Conflating Status and Conclusion is the defining bug of the tools that came before this one, and in a scheduler it would produce a tier that is wrong in exactly the live case.

**What the Feed polls is a filtered listing, capped at 1,000 silently** ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)). The cap is the Feed's to label honestly. It is not the scheduler's to work around, and no polling frequency affects it.

**go-gh's client is TTL-only and never revalidates (PRD risk R2, resolved).** Measured against real go-gh v2.9.0, two identical GETs produced 1 network hit and 0 requests carrying `If-None-Match`. R1 above is therefore implementable only over a transport of our own, passed as `api.ClientOptions.Transport` with go-gh's cache left off (`CacheTTL: 0`), which is verified working end to end. The economics above are untouched: the 304s are still free, and they are now ours to send. See [local-store](../local-store/requirements.md) R8, R19 and R20.

## Open questions

1. **Do 304s count against the secondary limit? UNKNOWN (PRD risk R4).** Assumed yes, pessimistically. If they do not, every interval in R11 could tighten and a 100-repository poll set becomes affordable at 5s. Untestable without deliberately tripping a limit, so it is likely to stay unknown, which is why the assumption is the conservative one.

2. **The concurrent-request cap is published: 100, shared across REST and GraphQL. RESOLVED.** GitHub documents it verbatim: "No more than 100 concurrent requests are allowed. This limit is shared across the REST API and GraphQL API." The counting unit (user, token or app) is not published, the best-practices page still advises making requests serially, and every secondary limit is subject to change without notice. Citations and the full readout live in [docs/research/secondary-limit-concurrency.md](../../research/secondary-limit-concurrency.md). R18's bound is now chosen against a known 100 rather than a rumour, and [ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md) chose it: 10, fixed, process-global, innermost in the transport chain.

3. **Re-homed: the mapping belongs to [settings](../settings/requirements.md) and [rate-governor](../rate-governor/requirements.md) jointly, and the scheduler's half is already fixed ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** Since this question was written, rate-governor open question 7 answered the largest piece: one share, over the primary limit, never throttling a Purge. What remains is what a share means as observed behaviour, and that waits on settings defining the knob itself, not least because a share of the primary limit barely constrains steady-state polling at all: a 304 costs zero primary allowance (rate-governor R7), so the share bites on 200s, cold-start payloads and detail fetches. The constraint this document contributes is binding: the share may reach the scheduler only as the Budget Readout's pressure and exhaustion (R13, R14), never as a second scheduler input, so whatever mapping is chosen requires zero scheduler changes.

4. **Resolved: staged in R15's order with R8a's window as the dwell, compounding doubles, and a hold until the reset ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** R15 carries the mechanism. The slow tier demotes alone at onset, so "least visible first" is a sequence rather than a statement about degree, and the dwell reuses R8a's five-minute constant because escalating faster than the burn measurement can respond is chasing noise.

    **The loop this question owned is closed by the hold, not damped by a constant.** No demoted tier is promoted inside the rate-limit window, so nothing oscillates. The predicate asks whether the allowance survives until the reset, the reset is therefore the condition's natural end, and it is never more than 60 minutes away. A hysteresis band and a promotion dwell were both rejected as tuned oscillators, each with a fresh constant to defend. The bias matches R8a's own: under-serving cadence is the safe direction, and the degradation stays visible because the Readout renders for as long as pressure holds.

5. **Resolved: neither yields, because the machinery already arbitrates every channel ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** On the primary limit, R8a's window was sized so the crawl's burst amortises (~57/min, projecting exhaustion ~82 minutes out) and stays silent, and if a shared, low-headroom token genuinely trips the projection, R15's staged demotion answers it whoever caused the burn. On the secondary pool, ~287 crawl points and the Feed's 312 points/min fit under ~900 together, and [rate-governor](../rate-governor/requirements.md) R11's dynamic ceiling subtracts observed reads, crawl included, so a concurrent Purge's deletes slow automatically. On the wire, the crawl passes the shared 10-slot limiter in about six seconds, a one-off queueing slip not worth reopening [ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md) over. The recorded residual: a far larger account's crawl could exceed ~900 points/min on reads alone and draw a secondary 403, which rides ADR-0018's exhaustion path, and pacing at that scale is [purge](../purge/requirements.md)'s question to raise from its own numbers. Purge open question 8 closes with this.

6. **Resolved: the fast tier is `queued` or `in_progress`, and a parked Run rates nothing ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** R5 carries the narrowed rule. A `waiting` Run's next event is a human acting, so ~3s polling buys nothing for as long as it is parked, and the approval's effect surfaces within the ambient tier's interval (usually the medium tier's ~5s, since a recent `waiting` Run tends to be visible) before the fast tier resumes on `queued`. A brief fast-then-decay window for the about-to-approve case was rejected as a timer, a constant and per-Run state for a case the medium tier already covers. [run-detail](../run-detail/requirements.md) open question 6 closes identically, narrowing its R13.

7. **Resolved: the scheduler does not own it, and there is nothing to own ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** "Fits no tier in R5" was the answer rather than the problem: tiers are steady-state, repository-level policy computed from Status and visibility (R4, R6), and an expectant poll would have been event-driven, short-lived and bound to one user action. [workflow-dispatch](../workflow-dispatch/requirements.md)'s `return_run_details` verification (#27) then removed the race entirely: the Dispatch response carries the Run ID, so no correlation poll exists and no dispatch fetch loop is needed. Had the race survived, the dispatch path would have owned a bounded, pane-owned fetch loop per [ADR-0015](../../adr/0015-the-async-model.md)'s shape, riding the same transport chain (limited by [ADR-0018](../../adr/0018-the-fanout-concurrency-shape.md), accounted by the governor). It did not, so the loop is not built.

8. **Resolved: on screen means the viewport ([ADR-0021](../../adr/0021-the-scheduler-cadence-policy.md)).** A repository is medium-tier while it has at least one row in the Feed's current viewport, and R5 carries the rule. The tier's cost is thereby capped by terminal height rather than poll-set size, so the 100-repository case that broke the widest reading never arises, the `run_started_at` sort targets recency for free, and an active filter's scoping is subsumed because filtering changes which rows are visible. A cursor-scoped tier was rejected because the cursor's needs are already met twice (R7's fastest-tier-wins and R9's debounced detail fetch), while every other visible repository would fall to ~30s under the user's eyes.

## Related

- [ADR-0021: The scheduler's cadence policy](../../adr/0021-the-scheduler-cadence-policy.md). R5's two qualifying rules, R8's narrowed ~3s clause, R15's staging, arithmetic and hold, and open questions 3 through 8.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). R1, R11, and the reason this feature exists.
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). R17's fan-out.
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). What is being polled.
- [ADR-0018: The fan-out's concurrency shape](../../adr/0018-the-fanout-concurrency-shape.md). R18's bound, R17's execution, single-flight and quit, and the secondary-limit path R16 carries.
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R12's intent-not-mechanism principle.
- [rate-governor](../rate-governor/requirements.md) owns the Budget Readout R13–R16 consume, and its R11 prices this scheduler's reads into the Purge's write ceiling.
- [repo-discovery](../repo-discovery/requirements.md) supplies R2's poll set.
- [local-store](../local-store/requirements.md) supplies R1's ETags, and its R19 owns the transport PRD risk R2 resolved into.
- [live-run-feed](../live-run-feed/requirements.md): R8 meets its R27 and R16 meets its R30.
- [run-detail](../run-detail/requirements.md) consumes R9's debounce and R5's fast tier.
- [purge](../purge/requirements.md): its open question 8, which this feature's open question 5 owns. Purge's open question 5 is the `rel="next"` terminal signal and is a different question entirely.
- [settings](../settings/requirements.md) owns the intent-level share R12 permits.
