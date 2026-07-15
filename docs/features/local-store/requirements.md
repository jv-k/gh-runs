# Local Store

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).
>
> The **local store** is gh-runs' own on-disk state: ETags, last-seen payloads and discovery results. It is deliberately not called a cache, because CONTEXT.md reserves **Cache** for a GitHub Actions Cache, a keyed blob in a repository, which [storage-reclamation](../storage-reclamation/requirements.md) deletes. The two have nothing to do with each other.

## Purpose

The local store persists ETags, last-seen payloads and discovery results across sessions, so a cold start paints the Feed from disk immediately and then revalidates for free. It is what converts [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s free 304s from a steady-state property into a launch-time one.

## Requirements

### What is persisted

**R1.** The local store must persist the ETag and the last-seen payload for every resource the tool revalidates, under the XDG state directory.

**R2.** The local store must persist [repo-discovery](../repo-discovery/requirements.md)'s results (the classification of which repositories have Runs, and the recorded capability for each), so a cold start does not re-probe 163 repositories before it can paint.

**R3.** The local store must record, per entry, when it was last revalidated, taken from the injected clock.

**R4.** The local store must not persist a Purge's crawl: not the resolved Run IDs, not the pages walked, not the progress. [ADR-0006](../../adr/0006-stateless-bulk-jobs.md) makes the filter the durable state precisely to avoid a job store, and persisting a ~287-page unfiltered crawl would rebuild that job store (with its schema, its reconciliation, and its capacity to disagree with reality) under a different name.

### Cold start

**R5.** On launch, the tool must paint the Feed from the local store before any HTTP response has arrived, and then revalidate. Stale-while-revalidate is the right shape for a dashboard that must feel immediate: the cached Feed is nearly always right, and where it is wrong the revalidation is seconds behind.

**R6.** Revalidation on cold start must be conditional, carrying each persisted ETag. In the ordinary case (nothing changed since the last session), every response is a 304 and the entire cold start costs zero primary allowance.

**R7.** The local store must expose each entry's last-revalidated time to its consumers, so a Feed whose revalidation has paused can say what it is showing and as of when, rather than presenting cached rows as live. [live-run-feed](../live-run-feed/requirements.md) R30 and [polling-scheduler](../polling-scheduler/requirements.md) R16 depend on the distinction between painted and revalidated.

### Correctness

**R8.** The HTTP layer must perform true ETag revalidation: send `If-None-Match`, and distinguish a 304 from a 200 to the local store and to the [rate-governor](../rate-governor/requirements.md). A time-based cache that serves an entry without revalidating it does not satisfy this requirement, no matter how fresh the entry is. See open question 1. This is the highest risk in the project.

**R9.** A freshness lifetime must not be exposed as a user setting, by flag or config key. With ETag revalidation being both free and correct, a TTL could only ever make data staler than revalidating it would. There is no value of the knob that improves on asking.

**R10.** A successful write must invalidate the affected repository's cached entries immediately, rather than waiting for the next revalidation to discover what the tool itself just did.

**R11.** The local store must be derived state and must always be safe to delete. Nothing may be recoverable only from it, and deleting the whole directory must cost a cold start its speed and nothing else.

**R12.** The local store must carry a schema version, and a version it does not recognise must be discarded and rebuilt rather than migrated in place or read optimistically. R11 is what makes discarding always available. A migration is only worth writing when discarding costs the user something, and here it costs one slow launch.

**R13.** An unreadable, truncated or corrupt local store must never fail a launch. It must be discarded and rebuilt, and the tool must start.

### Identity

**R14.** Every persisted key must be host-qualified as `host/owner/name` per [ADR-0009](../../adr/0009-host-qualified-repo-identity.md), including in 2.0.0, which serves github.com alone. Host is one field now versus rekeying every persisted entry later, and there is no legacy unqualified key to migrate from because this cache ships with 2.0.0.

### Growth

**R15.** The local store's disk footprint must be bounded, per repository and in total, and must not grow without limit across sessions.

**R16.** The local store must bound what it stores per repository to the window the Feed actually paints, not to the repository's Run history. The reference repository has 28,707 Runs. Nothing about painting a Feed requires holding them.

### Seams

**R17.** The local store's freshness bookkeeping must take its timing from an injected clock, so that tests of expiry, revalidation and last-revalidated reporting advance time explicitly rather than sleeping.

**R18.** The local store must be exercisable end-to-end against recorded HTTP fixtures, with no live network, and the fixtures must include a 200-with-ETag followed by a 304 for the same resource. This is the seam that proves R8 rather than assuming it: a hand-written fake would return whatever we believed a conditional request does, and would keep returning it long after the API changed. Cassettes replay what the API actually said. That is how this project learned that a filtered listing silently caps at 1,000 and that DELETE rejects a Run still in progress.

## Acceptance criteria

**AC1: Paint before the network.** Given a populated local store and a cassette whose responses are held, the Feed renders rows before any response is delivered.

**AC2: Cold start is conditional.** Given persisted ETags, every revalidation request on cold start carries `If-None-Match`. None is unconditional.

**AC3: Cold start is free.** Given persisted ETags and a cassette returning 304 for every entry, the primary allowance's `used` counter does not advance across the whole cold start, and the painted Feed does not change.

**AC4: The baseline persistence removes.** Given an empty local store, every repository in the poll set costs a full 200 and the discovery pass re-probes. This is the cost R1 and R2 exist to avoid, and it is what an ETag-blind implementation would pay on every launch.

**AC5: Revalidation is real, not TTL.** Given an entry that a TTL implementation would consider fresh, a request is still issued and it carries `If-None-Match`. No entry is ever served without revalidation on the strength of its age alone.

**AC6: 304 versus 200 is visible.** The cache and the rate-governor can both distinguish a 304 from a 200 for the same resource. The 304 leaves the payload untouched and the 200 replaces it.

**AC7: Discovery survives restart.** Given persisted discovery results, a cold start yields the ~26-repository poll set with zero probe requests issued, and the capability recorded for each repository is the capability that was persisted.

**AC8: Last-revalidated is reportable.** With revalidation paused at Budget exhaustion, each painted entry's last-revalidated time is available and is the time the injected clock held at its last 200 or 304.

**AC9: A Purge writes nothing.** Given a Purge crawling ~287 unfiltered pages and deleting Runs, no file is created and the local store's size does not grow with the crawl. After a kill mid-Purge, no resume state exists on disk.

**AC10: A write invalidates.** Given a successful deletion of Run X, no subsequent cold start serves X from the local store.

**AC11: Safe to delete.** With the entire state directory removed, the tool launches, paints, and reaches the same Feed. The only observable difference is that the launch was slower.

**AC12: Unknown schema version.** Given a cache file whose schema version the binary does not recognise, the launch succeeds, the cache is rebuilt, and no data loss is reported to the user because none occurred.

**AC13: Corrupt file.** Given a truncated or malformed cache file, the launch succeeds and the cache is rebuilt. No launch path fails on a bad cache.

**AC14: Host-qualified keys.** Every persisted key carries a host component, and no key can be constructed without one. A fixture asserting the key shape fails if `owner/name` is ever persisted bare.

**AC15: Bounded growth.** Across repeated sessions over the reference account's ~26 repositories with Runs, the on-disk footprint stays under the stated bound and does not grow monotonically with session count.

**AC16: No TTL knob.** No flag or config key sets a cache TTL or a freshness lifetime.

**AC17: Deterministic.** Every test in this feature advances the injected clock rather than sleeping, and completes without a network.

## Constraints

**Conditional requests are free against the primary limit, and persistence is what lets a cold start collect that.** Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), attributable entirely to the 200 ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)). ETags must persist across sessions or every cold start pays full 200s: ~26 repositories of full payload for the Feed, plus ~163 probes if discovery is not cached either.

**Cold start must paint in under a second** when launched inside a repository (PRD success criterion 1), and **idle polling must consume ~0 primary allowance** (success criterion 3). R5 meets the first from disk. R6 keeps the second true at launch and not merely in the steady state.

**The Feed's per-repository window is bounded by the 1,000-result cap, not by the repository's history.** Any filter caps a listing at 1,000 silently ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)), which is a lie the Feed must label honestly. It is also, incidentally, a ceiling on what R16 could ever be asked to hold.

**A Purge is stateless by decision, not by omission** ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md)). Deletion is idempotent, so re-running the same Purge is the resume path and the filter is the durable state. The alternative (a persisted job queue) was considered and rejected for the cost of a job store, schema versioning, and reconciling IDs that vanished underneath it. R4 keeps that decision from being quietly undone here, which is the natural place for it to be undone.

**Repository identity is host-qualified from day one** ([ADR-0009](../../adr/0009-host-qualified-repo-identity.md)), because host is one field now against a migration of every persisted key later. This feature is the "later" that ADR is protecting against.

**The whole live layer rests on PRD risk R2.** Free 304s are the economic basis of polling, of cold start, and of the re-probe. If the client we build on does not revalidate, none of the economics in this document, [polling-scheduler](../polling-scheduler/requirements.md) or [rate-governor](../rate-governor/requirements.md) hold.

## Open questions

1. **Whether go-gh's client does real ETag revalidation or only TTL caching is UNKNOWN, and it is the most important open question in this document, arguably in the project.** PRD risk R2 names it "the one that forces a redesign". R8 states the behaviour the tool requires. Nothing states that the client we have chosen provides it. If it is TTL-only, then a cached entry is served on age rather than on asking, 304s never happen, every poll is a full 200, and the arithmetic underneath [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) (~26 repositories polled every few seconds at ~zero cost) collapses into 18,720 requests/hour against a 5,000/hour allowance. The remedy is a transport of our own, which is contained but is not small, and it must be discovered before the live layer is built on top of it rather than after. **Verify first.** This is a build-time check, not a judgement call: issue a request, issue it again, and look at whether the second one carried `If-None-Match` and what the server said.

2. **Concurrent access is undecided.** Nothing prevents two gh-runs processes from running at once, and [ADR-0006](../../adr/0006-stateless-bulk-jobs.md)'s stateless Purge actively invites it. Running a 100-minute Purge in one terminal while browsing the Feed in another is a normal thing to do, not an abuse. Both processes revalidate, and both write. Whether the local store is single-writer, locked, partitioned by process, or simply last-write-wins has not been decided. R13 bounds the damage (a corrupt cache is discarded and rebuilt), but bounding the damage is not the same as not causing it, and "your Feed got slower because your Purge trampled its ETags" is a real outcome.

3. **Whether last-known Budget state should be persisted is undecided.** Without it, a cold start immediately after exhaustion fans out into a limit it already knew about and learns nothing it had not already been told. With it, the tool refuses to make requests on the strength of a possibly-stale number it can only confirm by making the request the number exists to prevent. The trade is real in both directions and the canon does not settle it.

4. **The eviction policy is UNKNOWN.** R15 requires a bound and does not name one, nor whether entries fall out by age, by count, by bytes, or when a repository leaves the poll set. A repository dropped from discovery's classification is the obvious candidate and is not obviously correct: it may reappear at the next re-probe.

5. **What a 304 leaves us holding is UNKNOWN in one respect.** R6 assumes a 304 means the persisted payload is still current, which is what a conditional request means. But the Feed's requests are filtered listings, and it is not established whether an ETag on a filtered listing is stable in the way this depends on: whether, for instance, a Run aging out of a `--created` window changes the ETag without any Run changing. If it does not, a filtered entry can be stale while revalidating clean.

6. **Whether the XDG state directory is the right home is worth one more look.** The decided design says state. R11 says the contents are derived and always safe to delete, which is the classic description of a cache directory rather than a state directory. The distinction matters to anyone whose backup policy treats the two differently, and to a user looking for the thing to delete when they suspect it.

## Related

- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md) (R1, R6, R8, and the ETags-must-persist consequence this feature implements)
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md) (R4)
- [ADR-0009: Repo identity is host-qualified](../../adr/0009-host-qualified-repo-identity.md) (R14)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) (R16's window, open question 5)
- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md) (the client open question 1 interrogates)
- [polling-scheduler](../polling-scheduler/requirements.md) (consumes R1's ETags, shares open question 1's fallout)
- [rate-governor](../rate-governor/requirements.md) (consumes R8's 304/200 distinction for R7 of that document)
- [repo-discovery](../repo-discovery/requirements.md) (R2 persists its results, R1 persists the ETags its conditional re-probe needs)
- [live-run-feed](../live-run-feed/requirements.md) (R5 paints its cold start, R7 keeps its R30 honest)
- [purge](../purge/requirements.md) (R4 is the enforcement of its R23)
- [settings](../settings/requirements.md) (owns what R9 refuses to expose)
