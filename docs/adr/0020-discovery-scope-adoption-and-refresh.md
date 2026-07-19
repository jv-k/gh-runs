# Discovery's scope, ad-hoc adoption, and refresh cadence

[ADR-0003](./0003-multi-repo-via-client-side-fanout.md) made the Feed a client-side fan-out and [repo-discovery](../features/repo-discovery/requirements.md) made discovery the thing that bounds it, but that document carried six open questions, several of which waited on the fan-out's concurrency shape. [ADR-0018](./0018-the-fanout-concurrency-shape.md) has landed, so this ADR closes all six: which repositories enumeration covers, what happens when the local repository is not among them, the re-probe cadence and its settings surface, and the disposition of three unknowns (the empty-list ETag, the meaning of `disabled`, and the fine-grained PAT header).

## Enumeration keeps the API default affiliations

`GET /user/repos` defaults to `affiliation=owner,collaborator,organization_member` with `type=all`, forks included. Enumeration keeps that default, and repo-discovery R1 now names the set explicitly rather than inheriting whatever the default happens to return.

Three reasons. It is what was measured: the reference 163, its two-page cost, and the ~26-with-Runs ratio were all observed under this default, so keeping it keeps the constraints table honest. It is everything the token can see, which is the spirit of the auto-discovery override the product owner chose knowingly (repo-discovery Constraints). And narrowing it fails silently: an organization repository the user works in daily would simply never appear in the Feed, with nothing to say why.

The cost of the choice scales with the account. A user in large organizations pays a larger one-time probe pass, revalidated for ~free thereafter (R12), and the two-tier cadence below bounds the recurring cost by construction.

## The local repository is adopted for the session

R14's fast path resolves a repository from the git remote without waiting on enumeration. When enumeration completes and that repository is not in the returned set (a clone the account does not own may never appear in `/user/repos`), its capability would stay not-yet-known indefinitely under R8, permanently disabling its destructive actions. The escape costs exactly one request: `GET /repos/{owner}/{repo}` returns the same `permissions` object, plus `archived` and `disabled`.

So discovery spends that request and adopts the repository **for the session**. It joins the Feed, and the poll set if it has Runs. Its classification, capability and ETags persist in the local-store like any other repository's, so revalidation stays free across sessions. But membership is not persistent: only launching inside the repository re-admits it. The Feed never accretes every clone the user has ever stood in, and a session launched elsewhere is unaffected.

This resolves repo-discovery open question 7 and [live-run-feed](../features/live-run-feed/requirements.md) open question 10 jointly, and repo-discovery now carries it as R22.

## Refresh is two-tier: a settable revalidation interval, an hourly floor for the rest

R11 requires a re-probe schedule and R12 makes it conditional, but no cadence was decided, and the economics hinged on an unmeasured unknown: whether a Run-list response for a repository with no Runs carries an ETag. If it does, a full pass revalidates for ~free. If it does not, the ~137 empty repositories are full-cost 200s on every pass, and a single aggressive cadence would spend up to ~39% of the primary allowance per hour at a 5-minute interval.

The cadence is therefore split by what each repository holds:

- **Repositories with a persisted ETag** re-probe on the **revalidation interval**: user-settable, default **5 minutes**, floor **1 minute**. A value below the floor is clamped with a diagnostic, the same shape as settings R12's clamp.
- **Repositories without a persisted ETag** re-probe on a fixed **hourly** constant, not settable.

The design is self-adapting: whatever the ETag measurement finds, the tier a repository lands in follows from the response it last gave, and the expensive case is bounded at ~163 unconditional requests per hour (~3% of the primary allowance) regardless of the knob. The burst itself runs under [ADR-0018](./0018-the-fanout-concurrency-shape.md)'s bound of 10 and completes in seconds. On-demand refresh exists independently (R11), and a manual refresh runs the full pass.

**The knob survives settings R13's test, and the wording there is amended to say so precisely.** R13 rejects "poll interval in seconds" because choosing it needs the token tier, the repo count and the points model. That reasoning binds the scheduler's poll interval and does not transfer here: two-tier makes the settable tier cheap by construction, so "how quickly should a newly active repository appear?" is answerable from the person's own context, in minutes, with no cost model in hand. That is the same intent test settings R12 and R19 already apply. Settings R13, AC6 and AC12 are amended in place to name the **scheduler's** poll interval as the rejected key, and settings gains the `discovery_refresh_minutes` key as R20 there.

## Three unknowns, dispositioned

**The empty-list ETag (open question 4) is no longer load-bearing, and its measurement folds into an existing ticket.** Two-tier means the answer changes cost, not design. The observation (does a Run-list 200 with zero Runs carry an ETag?) joins [Measure whether a filtered listing's ETag is body-faithful](https://github.com/jv-k/gh-runs/issues/19), which already exists to record listing ETag behaviour with the same instrumentation. The requirements doc records the observed answer when it lands.

**`disabled` (open question 5) is resolved from documentation and stays inert.** The repository object's `disabled` field means the repository has been disabled **by GitHub**: DMCA takedown, billing failure, terms-of-service action. It is not "Actions disabled", which lives at a separate endpoint (`GET /repos/{owner}/{repo}/actions/permissions`, an `enabled` boolean), and it is not a synonym for `archived`. It is also effectively unmeasurable by us, because only GitHub can produce a disabled repository, so documentation is the only source this answer will ever have. R7 keeps recording it because it arrives free, nothing acts on it in 2.0.0, and destructive gating continues to key on `archived` and `permissions` alone.

**The fine-grained PAT header (open question 1) is moot by rule.** The canon's claim that `x-oauth-scopes` exists only for classic tokens was observed with a classic token and never verified against a fine-grained one. Rather than verify it, no code path reads the header at all. R10 already makes capability advisory and keeps every consumer's error handling. Ruling the header unread turns "verify before code depends on the distinction" into "no code may depend on the distinction", and the verification task evaporates.

## Considered Options

**Narrower affiliations: owner plus collaborator, or owner alone.** Bounds the probe pass for organization-heavy accounts, but an organization repository the user works in silently never appears, the reference measurements no longer describe the shipped behaviour, and the auto-discovery override loses its point.

**Permanent adoption of the local repository.** One request either way, but once seen it stays in every future session's Feed, so the Feed accretes every clone ever visited. Session scope keeps the invariant that the steady-state Feed is the enumerated account plus, at most, where you are standing now.

**A read-only guest instead of adoption.** Zero extra requests, capability stays not-yet-known, destructive actions stay disabled. Rejected because the repository you are standing in is the single most likely subject of the tool, and one request is the cheapest capability lookup this tool ever makes.

**A single cadence robust to the unknown: hourly.** The original recommendation, and it holds up on cost. But discovery lag of up to an hour for a newly active repository was judged too slow for a live dashboard, and two-tier buys the 5-minute default without gambling the allowance on the measurement.

**A 5-minute cadence regardless, single-tier.** Up to ~1,956 requests per hour (~39% of the primary allowance) if empty lists carry no ETag, spent detecting an event that happens a few times a year.

**Gating the default on the measurement.** Ship 5 minutes if the ETag measurement confirms, hourly if not. Couples a shipped default to an unlanded measurement and puts a contingency in an ADR where a self-adapting mechanism could simply not care.

**One knob scaling both tiers, or two knobs.** Scaling drags the expensive tier along with the cheap one (a 1-minute setting makes the ETag-less tier ~700 requests per hour). Two knobs is a second setting explaining a distinction users should never need to know exists.

**A named tier for the knob (`eager | normal | relaxed`), following the budget precedent.** Defensible, and the closest call in this ADR. Rejected because the budget tier hides a number no user can choose well, while this interval is answerable in minutes from intent alone. Hiding an answerable number behind three names is indirection without protection.

**Dropping the knob: a fixed 5-minute constant.** Cleanest against the settings canon, but the interval is genuinely a preference (a laptop on battery has a different answer than a wall-powered dashboard), and it passes the intent test that justifies R12 and R19 being settable.

**Verifying the fine-grained PAT header anyway.** A human mints a token, one request records the headers, and the fact is consumed by no code path, forever. Ceremony for a moot point.

**Acting on `disabled`, gating it like `archived`.** Acts on a field no test can exercise with a real fixture, on a documented meaning this project cannot observe. Recording without acting keeps the claim falsifiable later at zero present cost.

## Consequences

**repo-discovery is amended.** R1 names the affiliation set. R11 carries the two-tier cadence and the knob. R22 is new and carries session adoption. Open questions 1, 3, 4, 5, 6 and 7 become resolved notes pointing here. AC10's economics now describe the revalidation tier.

**settings is amended.** R13's rejected "poll interval" row, AC6 and AC12 now name the scheduler's poll interval precisely, so the discovery refresh row does not trip them. R20 is new there: `discovery_refresh_minutes`, default 5, floor 1, clamped with a diagnostic below the floor. R14's specific-diagnostic contract is unchanged.

**live-run-feed's open question 10 closes**, pointing at R22.

**Ticket #19 widens by one observation**: whether an empty Run list's 200 carries an ETag.

**The measurement that never happens.** No fine-grained PAT verification, no live `disabled` fixture. Both are recorded as deliberate, so a future reader does not rediscover them as gaps.
