# Adaptive delete throttle, not a fixed rate

GitHub gives two answers for how fast you may delete, and they disagree by 3×:

- The **points model**: DELETE costs 5 points against roughly 900/min, giving about **180/min**.
- The **written guidance** for write requests: *"wait at least one second between each request"*, giving about **60/min**.

On an 18,260-Run Purge that is 100 minutes versus 5 hours. We start at the documented-safe 1/sec, ramp toward the points ceiling while responses stay clean, and back off hard the moment a 403, a 429 or a `Retry-After` appears. Starting at the ceiling instead would treat the points model as the whole truth while GitHub's prose says otherwise, and the penalty for being wrong is a block on the user's account, not a retry.

## Consequences

**The secondary limit is not observable, which is why this must be adaptive rather than merely tuned.** Measured: responses carry `x-ratelimit-limit`, `-remaining`, `-used`, `-reset` and `-resource` for the **primary** limit only, and `/rate_limit` exposes buckets for `core`, `graphql`, `search`, `code_search`, `audit_log`, `scim` and friends. **None of them describe the secondary limit.** There is no counter to read. The ramp is open-loop, and the only feedback that we exceeded roughly 900 points/min is a 403 that arrives after we already did. Headroom and backoff are therefore not optimisations. They are the entire control mechanism.

This also settles the relationship with the Budget setting, on measurement rather than taste. The Budget is a share of the **primary** limit, which is observable and is what background polling spends. A Purge's write throughput is bound by the **secondary** limit, which is not observable and is a different pool. So a Budget of "25% of my allowance" governs polling and never throttles a Purge. The apparent conflict, that 180 deletes/min × 5 points = 900 points/min and therefore the whole secondary allowance, is not a conflict at all. The two limits are not the same currency.

Adaptive is the only approach that is right for more than one token tier. Enterprise Cloud has 3× the primary budget, GitHub Apps scale differently, and a token shared with CI has less headroom than the limit suggests. Any fixed number is wrong for somebody.

**Deletes-per-second is deliberately not a setting.** The only thing that knob does is let someone raise it and get their account blocked, and the adaptive governor already beats any number a person could pick, because it observes what their account actually tolerates. Exposing intent, "what share of my Budget may this spend?", is a question a user can answer. Exposing mechanism, "how many deletes per second?", requires the points model, their token tier and their repo count.

For context, v1 chose `sleep 0.25`, about 4/sec, faster than *both* numbers above. That is why it works on small repositories and would get blocked on a large one.

This is the highest-value target for the fake-clock test seam. Ramp and backoff must be testable without real sleeps.
