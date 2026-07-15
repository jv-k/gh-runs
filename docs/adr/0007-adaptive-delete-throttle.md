# Adaptive delete throttle, not a fixed rate

GitHub gives two answers for how fast you may delete, and they disagree by 3×:

- The **points model**: DELETE costs 5 points against roughly 900/min, giving about **180/min**.
- The **written guidance** for write requests: *"wait at least one second between each request"*, giving about **60/min**.

Both are **documented rather than measured**, and neither can be measured: establishing a point cost by experiment means driving the account into the secondary limit to find its edge, which the PRD's risk R4 forbids permanently. They are GitHub's published model, re-verified as current.

On an 18,258-Run Purge that published band is 100 minutes versus 5 hours. We start at the documented-safe 1/sec, ramp toward the ceiling the points model leaves us while responses stay clean, and back off hard the moment a 403, a 429 or a `Retry-After` appears. Starting at the ceiling instead would treat the points model as the whole truth while GitHub's prose says otherwise, and the penalty for being wrong is a block on the user's account, not a retry.

The governor reaches neither end of that band. [rate-governor](../features/rate-governor/requirements.md) R11 caps the ramp at 150/min and floors it at 30/min, so a real Purge at this scale runs ~2 hours to ~10 hours, and ~155 minutes in the normal case where the Feed is polling. Where this document says 100 minutes or 5 hours, it means the two published limits and their disagreement, which is what the design has to survive.

## Consequences

**The secondary limit is not observable, which is why this must be adaptive rather than merely tuned.** Measured: responses carry `x-ratelimit-limit`, `-remaining`, `-used`, `-reset` and `-resource` for the **primary** limit only, and `/rate_limit` exposes buckets for `core`, `graphql`, `search`, `code_search`, `audit_log`, `scim` and friends. **None of them describe the secondary limit.** There is no counter to read. The ramp is open-loop, and the only feedback that we exceeded roughly 900 points/min is a 403 that arrives after we already did. Headroom and backoff are therefore not optimisations. They are the entire control mechanism.

This also settles the relationship with the Budget setting, on measurement rather than taste. The Budget is a share of the **primary** limit, which is observable and is what background polling spends. A Purge's write throughput is bound by the **secondary** limit, which is not observable. So a Budget of "25% of my allowance" governs polling and never throttles a Purge.

**The conclusion stands and the old reasoning for it was wrong.** This ADR used to dismiss the apparent conflict (180 deletes/min x 5 points = the whole 900 points/min secondary allowance) by saying the two limits "are not the same currency". The Budget and a Purge are indeed different currencies, primary against secondary. But **polling and purging share the secondary pool**, which is a separate fact and the one that was missed. [polling-scheduler](../features/polling-scheduler/requirements.md) says so outright: "the binding constraint is therefore the secondary limit, not the primary one: ~900 points/min, GET = 1 point", and its R11 budgets 26 repositories at 5s as 312 points/min against that same 900.

Do the arithmetic the old wording skipped. A Purge at the ramp's 2.5/sec cap spends 750 points/min. The Feed spends 312. **1,062 against ~900**, and [purge](../features/purge/requirements.md) R14 *requires* the Feed to keep updating during a Purge, so that is the normal case rather than a corner. R10's additive increase crosses the wall about four steps in, at 2.0/sec.

**So the write ceiling is dynamic** ([rate-governor](../features/rate-governor/requirements.md) R11): `(900 - observed read points per minute) / 5` deletes per minute, floored at 0.5/sec and capped at 2.5/sec. With the Feed at 312 points/min that is ~117/min, or **~1.96/sec**, which is exactly where the wall sits. The governor already holds the number it needs, because R1 makes it account every request the tool issues, scheduler polls included. The AIMD shape from R10 is unchanged. What changed is what the ramp is climbing toward.

Adaptive is the only approach that is right for more than one token tier. Enterprise Cloud has 3× the primary budget, GitHub Apps scale differently, and a token shared with CI has less headroom than the limit suggests. Any fixed number is wrong for somebody.

**Deletes-per-second is deliberately not a setting.** The only thing that knob does is let someone raise it and get their account blocked, and the adaptive governor already beats any number a person could pick, because it observes what their account actually tolerates. Exposing intent, "what share of my Budget may this spend?", is a question a user can answer. Exposing mechanism, "how many deletes per second?", requires the points model, their token tier and their repo count.

For context, v1 chose `sleep 0.25`, about 4/sec, faster than *both* numbers above. That is why it works on small repositories and would get blocked on a large one.

This is the highest-value target for the fake-clock test seam. Ramp and backoff must be testable without real sleeps.
