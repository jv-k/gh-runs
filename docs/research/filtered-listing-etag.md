# Filtered listing 304s are body-faithful

Research note. Measured 2026-07-19, 07:25:42Z to 07:56:18Z. GET requests only, no DELETE, no limit approached, per [PRD](../PRD.md) risk R4's rules. Commissioned by [ADR-0017](../adr/0017-the-local-store-on-disk-contract.md) and tracked as [issue #19](https://github.com/jv-k/gh-runs/issues/19).

## Short answer

On a filtered Run listing, GitHub's 304 tracked the response body exactly. Across 75 interleaved rounds against one filtered listing URL, the listing's contents changed 13 provable times. Every one of those changes coincided with a conditional 200 carrying a new ETag. No 304 ever accompanied a body its unconditional twins showed changed, no 200 ever carried an unchanged body, and no 200 ever repeated the stored ETag against a new body. [local-store](../features/local-store/requirements.md) R6 stands as written, now on measurement rather than on HTTP semantics alone.

## Method

[ADR-0004](../adr/0004-conditional-polling-for-liveness.md)'s interleaving technique, extended with bracketing so a mid-round change cannot masquerade as a violation.

- **Target**: `GET https://api.github.com/repos/home-assistant/core/actions/runs?status=completed&per_page=30&page=1`. The ticket's example was cli/cli, but home-assistant/core was chosen for pace: its filtered listing changed roughly every 2.5 minutes during the window, so a half-hour run observes many transitions.
- **Constant headers**: `Authorization: Bearer` (one OAuth token throughout), `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`.
- **Seed**: one plain GET establishing the stored ETag and the stored body it was minted for.
- **Each round, about 20 seconds apart**: an unconditional GET (u1), then a conditional GET carrying `If-None-Match: <stored ETag>`, then a second unconditional GET (u2). On a conditional 200, the stored ETag and stored body advance to the fresh response. 226 GETs in total.
- **Comparison**: SHA-256 over the raw decompressed body bytes. A round counts as a provable change only when u1 and u2 agree on a body that differs from the stored one. Rounds where u1 and u2 disagree are racy (the listing moved mid-round) and are excluded from verdicts rather than counted either way.
- **Safety**: the probe reads `x-ratelimit-remaining` every round and aborts below 500. It never got close: the whole run spent 166 primary requests from a 5,000 allowance.

## Evidence

| Measure | Count |
|---|---|
| Rounds | 75 |
| Conditional 200s | 15 |
| Conditional 304s | 60 |
| Provable changes (u1 = u2, both differ from stored) | 13 |
| Provable changes answered 200 with a new ETag | 13 |
| Provable changes answered 304 (violations) | 0 |
| Provable non-changes answered 304 (correct 304s) | 59 |
| Spurious 200s (200 with a byte-identical body) | 0 |
| ETag churn (200, new body, ETag unchanged) | 0 |
| Racy rounds, excluded | 3 |

The listing provably moved throughout the window: 13 distinct page-one bodies, `total_count` rising 146,786 to 146,795, and 6 distinct newest Run ids.

A change round versus a quiet round, hashes truncated:

| Round | Stored body | u1 | Conditional | u2 |
|---|---|---|---|---|
| 1 | `a80c3d8b` | `8090ed6d` | 200, new ETag, `8090ed6d` | `8090ed6d` |
| 2 | `8090ed6d` | `8090ed6d` | 304 | `8090ed6d` |

The one racy 304, round 59, is the sharpest promptness evidence in the run. At u1 the body still equalled the stored body and the 304 was correct at that instant. The body changed in the roughly two seconds before u2 (`total_count` 146,795 to 146,796). The very next round's conditional immediately returned 200 with the new body:

| Round | Stored body | u1 | Conditional | u2 |
|---|---|---|---|---|
| 59 | `dce0a577` | `dce0a577` (146,795) | 304 | `587b9e37` (146,796) |
| 60 | `dce0a577` | `587b9e37` | 200, new ETag, `587b9e37` | `587b9e37` |

The other two racy rounds (14 and 31) both answered 200, consistent with a body already moving.

## Incidental observations

**The primary-limit exemption holds on a filtered listing.** 57 of the 60 conditional 304s left `x-ratelimit-used` exactly where the preceding unconditional 200 put it, while every 200 in the run advanced it by exactly one. The three exceptions moved by +25, +4 and +1, and each is explained by traffic outside the probe: the token is gh's own OAuth token and other processes on the machine spend from it, and one exception straddles the hourly window reset (`used` fell from the sixties to 5 mid-run). Aggregate arithmetic over quiet stretches matches the 200 count, not the request count. This reconfirms [ADR-0004](../adr/0004-conditional-polling-for-liveness.md)'s measurement on the exact endpoint shape the Feed will poll, and matches the docs: "Making a conditional request does not count against your primary rate limit if a `304` response is returned and the request was made while correctly authorized with an `Authorization` header."

**A 304 echoes the ETag without its weak prefix.** Every 200 returned `ETag: W/"<hex>"`. Every one of the 60 304s echoed the same value as `ETag: "<hex>"`, the weak prefix stripped, all 60 of 60. Implication for the transport [local-store](../features/local-store/requirements.md) R19 requires: overwrite the stored ETag from a 304's headers and the stored form silently changes from weak to strong. This run only ever sent the weak form and it always matched. Whether GitHub's comparison also matches the strong form was not tested. The safe rule is to keep the ETag from the 200 that minted the payload and never rewrite it from a 304. RFC 9110 section 13.1.2 requires servers to use weak comparison for `If-None-Match`, under which the two forms match, but the measurement did not lean on that.

## Consequences for the canon

- **R6 stands as written.** No amendment to [local-store](../features/local-store/requirements.md) is needed. Open question 5's residual unknown is now measured rather than assumed, and this note is its record.
- **[ADR-0017](../adr/0017-the-local-store-on-disk-contract.md)'s trust in filtered-listing 304s is now backed.** The rejected hedge, a periodic unconditional refresh, stays rejected: it would spend primary allowance against a failure that 75 rounds could not produce.
- **No API bug to report.** The measurement was commissioned with an upstream report as the contradiction path. There is nothing to report.
- **Scope of the claim.** One endpoint shape (`actions/runs` with a `status` filter), one repository, one half-hour window, page one only. It is a measurement, not a proof, and GitHub can change behaviour without notice. The 13-for-13 record plus zero spurious 200s is as strong as a window this size can say.

## Sources

- The measurement itself: 226 GETs against `https://api.github.com/repos/home-assistant/core/actions/runs?status=completed&per_page=30&page=1`, 2026-07-19 07:25:42Z to 07:56:18Z, recorded as one JSONL row per round (statuses, ETags, SHA-256 body hashes, `total_count`, newest Run id, `x-ratelimit-used` and `-remaining` per request).
- https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api (retrieved 2026-07-19): "Most endpoints return an `etag` header", "You can use the values of these headers to make conditional `GET` requests", and the primary-limit quote above.
- RFC 9110, sections 8.8.3 (ETag, weak validators) and 13.1.2 (`If-None-Match`, weak comparison).
- [ADR-0004](../adr/0004-conditional-polling-for-liveness.md) (the interleaving technique and the original `used` measurement).
