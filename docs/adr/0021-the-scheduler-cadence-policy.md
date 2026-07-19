# The scheduler's cadence policy

[ADR-0004](./0004-conditional-polling-for-liveness.md) made liveness a matter of conditional polling, and [ADR-0018](./0018-the-fanout-concurrency-shape.md) fixed how the fan-out executes while deliberately leaving cadence policy above it open. [rate-governor](../features/rate-governor/requirements.md) R8a then gave pressure an onset, which is the input that policy was waiting on. This ADR closes [polling-scheduler](../features/polling-scheduler/requirements.md)'s remaining open questions with six decisions: what the medium tier watches, what the fast tier tracks, how demotion stages, what a demotion does, when it releases, and where the scheduler's remit stops.

## The medium tier is the viewport

A repository is on screen while it has at least one row in the Feed's current viewport, and for exactly that long it qualifies for the medium tier. Three properties pay for the rule:

**It is self-capping.** The tier's cost is bounded by terminal height, roughly 40 rows and usually fewer distinct repositories, rather than by poll-set size. At 26 repositories the whole set at 5s was affordable (312 points/min) and at 100 it was not, so the widest reading of "on screen" failed exactly when scale arrived. The viewport reading never meets that cliff.

**The sort does the targeting.** Ordered by `run_started_at`, the viewport holds the most recently active repositories, so the medium tier tracks recency without tracking it explicitly.

**The alternatives collapse into it.** An active filter changes which rows are visible, so filter scoping is subsumed. The cursor is already served twice over: a live Run under the cursor is fast-tier work (R7), and the cursor's detail fetch is R9's debounced path. A cursor-scoped medium tier would let a new Run appear at up to 30s on a repository the user is looking straight at.

The consequence is that scrolling changes tier assignments, so the Feed publishes its viewport's repositories to the scheduler. [ADR-0015](./0015-the-async-model.md)'s routing already carries size and data everywhere, and R3 already obliges the scheduler to adopt set changes live, so the mechanism exists.

## The fast tier is progress, not incompleteness

A repository qualifies for the fast tier when its recent Runs include at least one whose Status is `queued` or `in_progress`. The parked statuses, `waiting`, `requested` and `pending`, qualify it for nothing, and the repository polls at whatever tier it otherwise holds.

R5 previously read "any Status short of `completed`", and `waiting` is the case that breaks it: a Run parked on a deployment approval can hold for days, and ~20 polls/min for days buys nothing, because a parked Run's next event is a human acting. Under the pessimistic assumption that 304s count against the secondary limit (open question 1), that spend is real. The ~3s figure was chosen to match `gh run watch` on a Run that is moving. A parked Run is not moving.

The cost of the narrowing is one interval of latency, once: an approval flips the Run to `queued`, the change surfaces within the ambient tier's interval (5s if visible, and a recent `waiting` Run usually is, 30s otherwise), and the fast tier takes over from there. That is R8's general liveness contract, not a breach of it. An approval made from inside gh-runs is [approvals](../features/approvals/requirements.md)' business, and it can act on its own knowledge of the moment rather than on polling.

[run-detail](../features/run-detail/requirements.md) R13 narrows identically: the pane refreshes Jobs at ~3s only while the selected Run's Status is `queued` or `in_progress`.

## Demotion is staged, five minutes apart

At pressure onset, the slow tier alone demotes. If a later Readout still reports pressure after a full five minutes, the medium tier joins. Five more, and the fast tier joins. R15's "least visible first" thereby becomes a sequence rather than a statement about degree: the medium and fast tiers are untouched at stage one, not merely stretched less.

The dwell is R8a's own five-minute burn window, reused rather than invented. Burn is measured over that window, so five minutes is how long the measurement takes to fully reflect the previous stage's effect. Escalating faster than the measurement can respond is chasing noise, and a separate dwell constant would be one more number to defend.

The slow tier goes first because that is where the recoverable burn is: everything not visible and not live, which at reference scale is most of the poll set.

## The arithmetic is compounding doubles

At each escalation step, every already-demoted tier's interval doubles again, and the next tier in R15's order joins at twice its target. Step one: slow at 60s. Step two: slow at 120s, medium at 10s. Step three: slow at 240s, medium at 20s, fast at 6s. Further steps keep doubling all three.

One rule, two constants, both already in canon (the factor mirrors the governor's halving in reverse, the dwell is R8a's window). Every step strictly decreases requests per minute, which is what AC10 asserts. The relief compounds where the burn is: by step three the slow tier has stretched eightfold while the fast tier has conceded 3s to 6s, which is R15's priority expressed as arithmetic.

There is no cap, deliberately. The release rule below bounds every episode at one rate-limit window, and thirty minutes of sustained pressure leaving the slow tier near-parked is the correct posture on a trajectory the projection says is doomed, with R16's explicit stop as the terminal state.

## A demotion holds until the reset

No demoted tier is promoted inside the rate-limit window. At the reset instant the schedule recomputes from scratch, from undemoted tiers and a fresh `remaining`, and if pressure immediately re-fires, escalation restarts at stage one.

This is the answer to the loop the projection introduces: demoting cuts the read rate, which cuts burn, which clears R8a's predicate, which would promote the tiers back into the same pressure. A hysteresis band changes that oscillation's amplitude and a promotion dwell changes its period, and both leave an oscillator in the design with a new constant to defend. Holding until reset removes the oscillator. The predicate literally asks whether the allowance survives until the reset, so the reset is the condition's natural end, and it is never more than 60 minutes away.

The bias is the one R8a already chose: under-serving is the safe direction. The bad case, a transient burst leaving the Feed demoted for the remainder of a window that turned out to have headroom, degrades cadence visibly, because the Readout renders for as long as pressure holds ([live-run-feed](../features/live-run-feed/requirements.md) R29). It never degrades correctness, and it never trains the operator to ignore a flapping banner.

## The crawl and the Feed do not yield to each other

A Purge's crawl and the Feed's polling share the account with no explicit priority in either direction, because the machinery already in place arbitrates every channel they collide on:

- **Primary burn.** R8a's window was sized so the reference crawl amortises (~287 requests reading as ~57/min, projecting exhaustion ~82 minutes out) and stays silent. If a crawl on a shared, low-headroom token genuinely trips the projection, staged demotion answers it, and demoting slow-first is the right sacrifice whoever caused the burn. The scheduler never learns a crawl exists, which is R13 and R14's division held.
- **Secondary points.** The reference crawl's ~287 points and the Feed's 312 points/min fit under ~900 together, and the governor's dynamic write ceiling ([rate-governor](../features/rate-governor/requirements.md) R11) subtracts observed reads, crawl included, so a concurrent Purge's deletes slow automatically for the crawl's duration.
- **Wire slots.** The crawl's requests pass through the shared 10-slot limiter in about six seconds, during which a poll may queue behind them and a fast-tier target may slip once by a few seconds. Adding priority to the limiter to erase a one-off, seconds-scale slip would reopen [ADR-0018](./0018-the-fanout-concurrency-shape.md) for nothing.

One residual is recorded rather than solved. An account far larger than the reference could carry a crawl big enough to exceed ~900 points/min on reads alone at full tilt, drawing a secondary 403 that rides ADR-0018's exhaustion path: the governor classifies, the Readout goes exhausted, everything pauses and says so. Whether a crawl at that scale deserves pacing of its own is [purge](../features/purge/requirements.md)'s question to raise from its own numbers, not this scheduler's to preempt.

This closes purge open question 8.

## The Dispatch correlation poll is not a tier

If [workflow-dispatch](../features/workflow-dispatch/requirements.md)'s verification of `return_run_details` shows the correlation race still exists, the dispatch path owns a bounded, purpose-built fetch loop, following the pane-owned shape [ADR-0015](./0015-the-async-model.md) gave the detail pane. It rides the same transport chain, so ADR-0018's limiter bounds it and the governor accounts it with no new rules, and R10's attributability applies: a correlation response whose dispatch context has passed is discardable.

"Fits no tier in R5" was the answer, not the problem. Tiers are steady-state, repository-level policy computed from Status and visibility (R4, R6). An expectant poll is event-driven, short-lived, and bound to one user action, and forcing it into the tier model means entry and exit rules keyed to something that is not Status, for a case the verification may delete outright. Whether the loop is needed at all, and its bounds, stay with workflow-dispatch.

## Considered Options

**A cursor-scoped medium tier.** Cheapest possible, and it slows every other visible repository to 30s while the user looks at them. The cursor's needs are already met by R7 and R9.

**A filter-scoped or whole-set medium tier.** Unfiltered, both are the whole poll set at 5s, which is 312 points/min at 26 repositories and unaffordable at 100. The viewport reading is filter-aware for free and never meets the cliff.

**All tiers demote at onset.** Makes R15's "before" vacuous and sacrifices a live Run's tracking at the first sign of pressure, when demoting slow alone may clear the projection.

**Computed arithmetic in place of stages.** Solve for the burn reduction the projection needs and stretch tiers in R15 order until it is covered. The most ADR-0007-flavoured option, but burn includes traffic the scheduler did not issue, so the equation needs an attribution R14 keeps the scheduler out of, and the answer slides continuously with the window, leaving no discrete states for tests to pin.

**A hysteresis band, or a promotion dwell.** Both keep mid-window recovery and both keep the oscillator, one at a band edge and one at a slower period, each with a fresh constant to defend.

**One doubling per tier, without compounding.** Bounded worst case, but total relief maxes out at half the schedule's burn, and if that is not enough the design just waits for exhaustion with no further move.

**Fixed demoted intervals.** A table of three new constants, and no answer for pressure persisting past the last row.

**`waiting` keeps the fast tier.** Days of ~3s polling on a Run whose next event is a human acting.

**`waiting` gets the fast tier briefly, then decays.** Serves the about-to-approve case at the cost of a timer, a constant and per-Run state, for a case the visible-row medium tier already covers at 5s.

**Pacing the crawl to protect the tiers.** Someone must start scheduling reads, which R4 deliberately keeps out of the governor, and the crawl is not the scheduler's to pace either. A third pacing authority for a collision the window already absorbs.

**Demoting the Feed for the crawl's duration.** Spends visible liveness on a one-off burst R8a was explicitly sized to absorb silently.

**A fourth, expectant tier for Dispatch.** Extends the tier model beyond Status for a mechanism that `return_run_details` may make unnecessary.

## Consequences

**R5's table changes in both rows that were ambiguous.** Fast reads `queued` or `in_progress`. Medium reads at least one visible row in the Feed's current viewport. R15 gains the staging, the arithmetic and the release rule, and new acceptance criteria pin all three against the injected clock.

**Scroll reaches the scheduler.** The Feed publishes its viewport's repositories, and a scroll is now a scheduling input alongside Status and the poll set.

**Under sustained pressure the degradation is visible and monotonic.** Requests per minute strictly decrease at every escalation step, the Readout renders throughout, and nothing is promoted until the reset instant. Tests assert discrete states at known virtual times rather than margins.

**run-detail R13 narrows and its open question 6 closes.** The pane's ~3s refresh runs only while the selected Run is `queued` or `in_progress`.

**purge open question 8 closes** with no yielding in either direction, and the giant-crawl residual is purge's to raise if its numbers ever demand it.

**polling-scheduler open question 3 is re-homed.** The Budget share's mapping belongs to [settings](../features/settings/requirements.md) jointly with [rate-governor](../features/rate-governor/requirements.md), carrying one constraint from here: the share reaches the scheduler only through the Readout, never as a second input.

**Two documents are amended in place.** [ADR-0018](./0018-the-fanout-concurrency-shape.md)'s closing paragraph pointed its two open cadence questions here, and rate-governor's open question 6 said the staging "stays open". Both now point at this ADR.
