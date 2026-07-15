# Liveness via conditional ETag polling

The Feed must show Runs invoked elsewhere without interaction. GitHub offers no push mechanism a local TUI can use, so liveness means polling. What makes it affordable is that **conditional requests returning 304 cost nothing against the primary rate limit**.

Measured directly, interleaving unconditional and conditional requests against the same endpoint:

```text
round1:  uncond 200 used=120  |  cond 304 used=120
round2:  uncond 200 used=121  |  cond 304 used=121
round3:  uncond 200 used=122  |  cond 304 used=122
```

`used` advances by exactly one per round, and that one belongs entirely to the 200. Polling roughly 26 repositories every few seconds is therefore about 3,600 requests an hour consuming approximately zero budget while idle. Only repositories that actually changed cost anything.

## Considered Options

**Webhooks.** They need a publicly reachable endpoint. A non-starter for a local TUI.

**SSE or websockets.** The web UI's live updates run on an internal socket with no public equivalent.

**Unconditional polling.** 26 repos × 12/min = 18,720 requests an hour against a 5,000/hour limit. Impossible.

## Consequences

The binding constraint moves from the primary limit to the **secondary** one (roughly 900 points/min, GET = 1 point), which is why the polling scheduler tiers and auto-scales rather than exposing an interval. We conservatively assume 304s **do** count against the secondary limit (risk R4). The primary exemption does not imply a secondary one, and confirming it would mean deliberately tripping a limit and risking a block.

ETags must **persist across sessions**, or every cold start pays full 200s. That persistence is what makes stale-while-revalidate cold starts both instant and free.

**This ADR rests on unverified risk R2**: whether go-gh's client does real ETag revalidation or merely TTL caching. If it is TTL-only, we need our own `http.RoundTripper`. The economics above are the foundation of the entire live layer, so verify this before building on it.
