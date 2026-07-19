# The fan-out's concurrency shape

[ADR-0003](./0003-multi-repo-via-client-side-fanout.md) made the Feed a client-side fan-out and named bounded concurrency as part of its price. [ADR-0015](./0015-the-async-model.md) fixed how a fan-out's results travel and deliberately left the bound its own decision. The verification that decision waited on has landed: GitHub publishes a cap of 100 concurrent requests, shared across the REST and GraphQL APIs, counting unit unpublished, subject to change without notice ([research note](../research/secondary-limit-concurrency.md)). This ADR closes the gap with five decisions: the bound, where it lives, how a fan-out is cancelled, what executes the polls, and how the governor's signals touch all of it.

## The bound is 10, and it is a constant

At most 10 requests are on the wire at once, process-wide. The figure is octokit's own: GitHub's plugin-throttling bounds its global pool at `maxConcurrent: 10`, the closest thing to an operationalised reading of GitHub's guidance that exists. It sits a tenth of the way to the published cap, so it survives the cap shrinking without notice, a second instance of the tool sharing a pool whose counting unit the docs never scoped, and any concurrent GraphQL use. And it is fast enough by a factor of five: at the measured ~190ms per round-trip, 26 repositories complete in three waves, roughly 0.6s, against the fast tier's ~3s target ([polling-scheduler](../features/polling-scheduler/requirements.md) R8).

Nothing configures it (polling-scheduler AC15), nothing scales it, and no state changes it. polling-scheduler R18 now carries the number.

## One limiter, innermost in the transport chain

The bound is not the scheduler's. It is a concurrency-limiting RoundTripper in [ADR-0012](./0012-transport-chain-and-the-client-surface.md)'s chain, below the store and below the governor, directly above the network. Every request the tool issues passes through it: scheduler polls, discovery's ~163-probe burst, the detail pane's fetches, a Purge's crawl, and every write. The published pool is per principal rather than per feature, so the bound must be too. With one pool there is nothing outside it, and no composition of components can breach the cap.

Innermost, because a slot must measure what the cap measures. GitHub's 100 counts requests in flight on the wire. A governor-paced DELETE waiting out its inter-write interval is invisible to GitHub, and above the governor it would hold one of 10 slots while waiting, letting a slow Purge starve the Feed's fan-out of slots nobody was using. Below the governor, a slot is held exactly while a request is on the wire and released when the response lands.

## Cancellation: single-flight per repository, one context for quit

**A superseded poll is skipped, never cancelled.** At most one poll per repository is in flight. A tick that comes due while its repository's previous poll is still out is dropped, and the next tick schedules normally. By the time a cancel could act the spend has already happened server-side, so cancel-and-reissue pays two requests for one answer, and under sustained slowness it can livelock a repository into never completing a poll. The late response is still the newest data held, and it emits normally. This is the read fan-out's form of the discard rule [ADR-0015](./0015-the-async-model.md) already gave the detail pane.

**Quit is one context.** The engine is constructed over a root context. `Stop()` cancels it, every in-flight request aborts through its per-request context, the workers unwind through a `WaitGroup`, and the engine closes its channel, which [ADR-0015](./0015-the-async-model.md) already made the root's quit signal. Nothing drains, because reads are side-effect free and their responses have no consumer after quit.

## Execution: a goroutine per due poll

Each due tick spawns a goroutine that issues the request, decodes, stamps `Repo` and `WorkflowName` ([ADR-0014](./0014-domain-types-and-the-budget-readout.md)), and sends the event. Single-flight already bounds live goroutines at the poll-set size, ~26 at reference, and the transport limiter bounds the wire at 10, so nothing else needs bounding. Decode and stamp happen in the worker, so nothing heavier than a channel receive ever sits in the render path, and a send blocked on a busy TUI stalls only that repository's goroutine, a case the skip rule already covers.

## Pressure moves intervals, never the bound

Spend rate and wire concurrency are different physics. Points per minute is how many requests are issued per minute, which the tiers' intervals control. The bound controls only how many of those requests overlap, and shrinking it under pressure would slow every cycle while saving zero points, because the same requests still go out, just later. So [polling-scheduler](../features/polling-scheduler/requirements.md) R15's demotion stretches intervals, R16's exhaustion stops scheduling ticks, and the bound holds in every state.

At exhaustion, in-flight polls complete and emit. Their cost is already paid, and their responses are the freshest data the paused Feed can show under its "resumes 14:32" banner. The stop is purely the scheduler declining to schedule until the reset instant.

## A rate-limited read rides the exhaustion path

A secondary-limit 403 or 429 can arrive on a poll. It is an account-scoped condition rather than a fact about the repository that happened to be polling, so it must not become a `RepoPollFailed`, and the other repositories must not keep firing into a limit that names the whole token, which is the ban risk [rate-governor](../features/rate-governor/requirements.md)'s Constraints describe. The governor already classifies the response (rate-governor R14) and already derives resumption from `Retry-After` (R9). The decision is that the classification reaches the scheduler as the Readout's exhaustion: the governor publishes `Exhausted` with the derived resume time, the scheduler stops scheduling exactly as it does at primary exhaustion, in-flight polls complete, and the Feed states the pause. No new mechanism, and the pause is visible rather than a silent breach of the ~30s liveness contract. A poll's re-attempt is its next scheduled tick after the resume, which keeps rate-governor R13 honoured without a retry queue.

The classification is account-scoped whichever request tripped it, so a write's rate-limit response publishes the same exhaustion, alongside the R12 backoff the write path already takes.

This widens the Readout's remit from the primary limit alone to the account's rate limiting, and [ADR-0014](./0014-domain-types-and-the-budget-readout.md) and [CONTEXT.md](../CONTEXT.md) are amended in place to say so.

## Considered Options

**A bound scaled to the poll set.** Something like a third of N, clamped. It buys cycle speed exactly where the ~900 points/min ceiling already forbids polling a large set quickly, so the speed is unusable, and it adds a formula where AC15 wants a constant.

**A higher constant, one wave of 26.** Finishes ~0.4s sooner than three waves, which no tier target needs, and spends a quarter of a shared cap whose counting unit is unpublished, against explicit prose advice to make requests serially.

**An adaptive bound, AIMD-style.** The governor already backs off on 403 and 429 underneath every request (rate-governor R12). A second controller acting on the same signal one layer up is two hands on one wheel, the shape of argument [ADR-0007](./0007-adaptive-delete-throttle.md) already deploys against redundant controls.

**A scheduler-owned semaphore.** Leaves discovery's burst, the detail pane and the crawl each bounding themselves or not at all, and makes the real in-flight total the sum of every component's private bound. The cap is per principal, so a per-component bound is the wrong scope by construction.

**Both a scheduler semaphore and a transport backstop.** Two mechanisms controlling one quantity. The inner one masks the outer in every test, and a regression has no single home.

**The limiter outermost, above the store.** Bounds cache handling and governor waits that GitHub never sees, so slots go missing while nothing is on the wire. The simple mental model costs exactly the concurrency the Feed needs back.

**Cancel-and-reissue on supersede.** Pays twice for one answer and can livelock under sustained slowness. Rejected above.

**Queueing the superseded tick.** Guarantees back-to-back polls precisely when responses are slow, pressure in the wrong direction, and buys nothing over skipping, because the completing response already carries the latest state.

**Draining on quit, bounded or not.** The responses have no consumer after quit. A bounded drain makes every quit slower for nothing, and an unbounded one holds the terminal hostage to the slowest request.

**A fixed worker pool with a queue.** A second concurrency number, a queue and an ordering policy, to bound goroutines single-flight already bounds at ~26.

**A long-lived goroutine per repository.** Turns tier changes and poll-set changes (polling-scheduler R3) into cross-goroutine coordination, and ~26 sleeping timers where one clock-driven schedule suffices.

**Shrinking the bound under pressure, or closing the limiter at exhaustion.** The first saves zero points while slowing every cycle. The second expresses a scheduler policy inside the transport layer and strangles non-scheduler traffic the policy was never about.

**Cancelling in-flight polls at exhaustion.** Discards responses already paid for and makes the paused Feed staler than it needed to be, for zero saved allowance.

**`RepoPollFailed` on a rate-limited read, or a scheduler-private pause.** The first mislabels an account condition as a repository fact and keeps the other repositories firing into the limit. The second sends the Feed silent past its ~30s liveness with no banner, the looks-live-but-is-not failure polling-scheduler R16 exists to prevent.

## Consequences

**The chain gains a resident, and main.go stays the only place that knows it.** The production nesting becomes store over governor over limiter over the network. The limiter imports nothing of ours and holds no state beyond its semaphore, so the cassette seam is untouched: tests still inject at the base parameter, now one layer further down. [ADR-0012](./0012-transport-chain-and-the-client-surface.md) carries a note, and its wiring snippet's signatures remain the stage 0 floor's.

**AC15 asserts a constant.** At no instant of virtual time are more than 10 requests in flight, in any state, under any pressure. The number appears in exactly one place.

**The Readout's exhaustion covers both limits.** Consumers already treat exhaustion as stop-and-say-when, so none changes. But anyone reasoning about the Readout must know `Exhausted` can mean a secondary-limit backoff with a `Retry-After` resume as well as a spent primary allowance. [ADR-0014](./0014-domain-types-and-the-budget-readout.md) carries the amended comment and [CONTEXT.md](../CONTEXT.md) the amended entry.

**Slow responses degrade per repository, and that is the design.** A repository whose responses take longer than its tier interval polls at its response rate under single-flight, and no queue builds anywhere. It is not a defect to fix with cancellation.

**What this ADR deliberately does not fix.** Whether demotion under pressure is staged across tiers, and its hysteresis, stays [polling-scheduler](../features/polling-scheduler/requirements.md) open question 4's. The crawl-versus-Feed priority stays its open question 5's. Both are cadence policy above this shape, and both consume it unchanged.
