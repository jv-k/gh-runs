# Local Store

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).
>
> The **local store** is gh-runs' own on-disk state: ETags, last-seen payloads and discovery results. It is deliberately not called a cache, because CONTEXT.md reserves **Cache** for a GitHub Actions Cache, a keyed blob in a repository, which [storage-reclamation](../storage-reclamation/requirements.md) deletes. The two have nothing to do with each other.

## Purpose

The local store persists ETags, last-seen payloads and discovery results across sessions, so a cold start paints the Feed from disk immediately and then revalidates for free. It is what converts [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s free 304s from a steady-state property into a launch-time one.

## Requirements

### What is persisted

**R1.** The local store must persist the ETag and the last-seen payload for every resource the tool revalidates, under the XDG cache directory ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)). An earlier form of this requirement said the state directory, and the store lived there while [purge](../purge/requirements.md) R29's deletion log shared it. The split is ADR-0017's: everything derived, this store included, lives under `$XDG_CACHE_HOME/gh-runs/`, and `$XDG_STATE_HOME/gh-runs/` keeps the log alone.

**R2.** The local store must persist [repo-discovery](../repo-discovery/requirements.md)'s results (the classification of which repositories have Runs, and the recorded capability for each), so a cold start does not re-probe 163 repositories before it can paint.

**R3.** The local store must record, per entry, when it was last revalidated, taken from the injected clock.

**R4.** The local store must not persist a Purge's crawl: not the resolved Run IDs, not the pages walked, not the progress. [ADR-0006](../../adr/0006-stateless-bulk-jobs.md) makes the filter the durable state precisely to avoid a job store, and persisting a ~287-page unfiltered crawl would rebuild that job store (with its schema, its reconciliation, and its capacity to disagree with reality) under a different name.

**R4 forbids a job store, and is not a claim that a Purge writes nothing to disk.** [purge](../purge/requirements.md) R29 requires an append-only deletion log under the XDG state directory, which this store no longer shares ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)), which nothing ever reads back and which therefore has none of the properties R4 is guarding against. It is not this store's, and this store must neither own it nor manage it: R11's safe-to-delete, R12's discard-on-unknown-schema and R13's discard-on-corrupt are claims about the local store, and none of them may reach that file. ADR-0006's amendment draws the line at reading rather than at the filesystem, and purge R23 carries it.

### Cold start

**R5.** On launch, the tool must paint the Feed from the local store before any HTTP response has arrived, and then revalidate. Stale-while-revalidate is the right shape for a dashboard that must feel immediate: the cached Feed is nearly always right, and where it is wrong the revalidation is seconds behind.

**R6.** Revalidation on cold start must be conditional, carrying each persisted ETag. In the ordinary case (nothing changed since the last session), every response is a 304 and the entire cold start costs zero primary allowance.

**R7.** The local store must expose each entry's last-revalidated time to its consumers, so a Feed whose revalidation has paused can say what it is showing and as of when, rather than presenting cached rows as live. [live-run-feed](../live-run-feed/requirements.md) R30 and [polling-scheduler](../polling-scheduler/requirements.md) R16 depend on the distinction between painted and revalidated.

### Correctness

**R8.** The HTTP layer must perform true ETag revalidation: send `If-None-Match`, and distinguish a 304 from a 200 to the local store and to the [rate-governor](../rate-governor/requirements.md). A time-based cache that serves an entry without revalidating it does not satisfy this requirement, no matter how fresh the entry is. go-gh's client is exactly such a cache (open question 1, resolved), so R8 is met only by the transport R19 requires. The distinction R8 demands is drawn **below** R19b's reconstitution, where both this component and the governor still see the real status code. Above it, nothing does, and nothing should.

**R9.** A freshness lifetime must not be exposed as a user setting, by flag or config key. With ETag revalidation being both free and correct, a TTL could only ever make data staler than revalidating it would. There is no value of the knob that improves on asking.

**R10.** A successful write must invalidate the affected repository's cached entries immediately, rather than waiting for the next revalidation to discover what the tool itself just did.

**R11.** The local store must be derived state and must always be safe to delete. Nothing may be recoverable only from it, and deleting the whole store must cost a cold start its speed and nothing else. **The store has the cache directory to itself, so deleting that directory is safe outright ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)).** [purge](../purge/requirements.md) R29's deletion log lives in the state directory, is the one file this tool writes that is recoverable from nowhere else, and shares no directory with anything this requirement declares deletable. An earlier layout put the two side by side, and this requirement had to warn that the obvious wipe destroyed the log. The split retires the warning, and R4 keeps the two apart.

**R12.** The local store must carry a schema version, and a version it does not recognise must be discarded and rebuilt rather than migrated in place or read optimistically. R11 is what makes discarding always available. A migration is only worth writing when discarding costs the user something, and here it costs one slow launch.

**R13.** An unreadable, truncated or corrupt local store must never fail a launch. It must be discarded and rebuilt, and the tool must start.

### Identity

**R14.** Every persisted key must be host-qualified as `host/owner/name` per [ADR-0009](../../adr/0009-host-qualified-repo-identity.md), including in 2.0.0, which serves github.com alone. Host is one field now versus rekeying every persisted entry later, and there is no legacy unqualified key to migrate from because this cache ships with 2.0.0.

### Growth

**R15.** The local store's disk footprint must be bounded, per repository and in total, and must not grow without limit across sessions.

**The total bound is 50 MB, and eviction is LRU by last-revalidated time ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)).** Enforced at write time: when a write would carry the store past the bound, the oldest last-revalidated entries are evicted until it fits. R3's timestamp supplies the ordering, so eviction keeps no bookkeeping of its own, and the cost of evicting the wrong entry is one full 200, this store's unit of failure everywhere else. A repository leaving the poll set triggers no eviction, because it may reappear at the next re-probe: under LRU its entries age out only if the space is needed, so a returning repository finds its ETags intact and re-enters on free 304s. The bound is a compiled constant and not a knob, by R9's own argument, because there is no value of it a user can pick that improves on the mechanism. It is a backstop rather than a target: the reference workload sits an order of magnitude below it and never evicts. The per-repository bound is R16's, applied at write time before eviction is ever involved.

**R16.** The local store must bound what it stores per repository to the window the Feed actually paints, not to the repository's Run history. The reference repository holds ~28,700 Runs. Nothing about painting a Feed requires holding them.

### Seams

**R17.** The local store's freshness bookkeeping must take its timing from an injected clock, so that tests of expiry, revalidation and last-revalidated reporting advance time explicitly rather than sleeping.

**R18.** The local store must be exercisable end-to-end against recorded HTTP fixtures, with no live network, and the fixtures must include a 200-with-ETag followed by a 304 for the same resource. This is the seam that proves R8 rather than assuming it: a hand-written fake would return whatever we believed a conditional request does, and would keep returning it long after the API changed. Cassettes replay what the API actually said. That is how this project learned that a filtered listing silently caps at 1,000 and that DELETE rejects a Run still in progress.

### The transport

**R19.** The tool must supply its own `http.RoundTripper` as `api.ClientOptions.Transport`, and must leave go-gh's own cache off by setting `CacheTTL: 0`. `EnableCache: false` does **not** disable it: the cache RoundTripper is installed unconditionally, and `EnableCache` only defaults `CacheTTL` to 24h, so `EnableCache: false, CacheTTL: 5 * time.Second` still caches. Our transport must never run underneath go-gh's cache, which sits above it and short-circuits before it runs, and which treats a 304 as cacheable (`isCacheableResponse` is `StatusCode < 500 && StatusCode != 403`) and would store a bare empty-bodied 304 as the response.

**R19a.** The transport must take a `base http.RoundTripper` as a constructor parameter and must dial through it. `api.ClientOptions.Transport` **replaces** `http.DefaultTransport` rather than wrapping it, so ours is the innermost RoundTripper in go-gh's stack and has nothing beneath it unless it is given one. `http.DefaultTransport` is what production passes. A cassette is what a test passes, and that parameter is R18's only injection point. See [ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md), which also fixes where the [rate-governor](../rate-governor/requirements.md) nests inside it.

**R19b.** The transport must **reconstitute a 200** from a 304 before returning it to go-gh: the persisted payload as the body, the fresh response's rate-limit headers and `Link`, and the stored `ETag` and entity headers. A bare 304 must never leave this component. go-gh treats every non-2xx as an error on **every** surface it offers, `Request` included (`success := resp.StatusCode >= 200 && resp.StatusCode < 300`), so a 304 handed upward surfaces to the caller as `*api.HTTPError` reading "HTTP 304". A conditional request that worked would read as a failed one. [ADR-0012](../../adr/0012-transport-chain-and-the-client-surface.md) fixes which headers win.

**R20.** Every ETag store key must be a **cryptographic hash** over the request method, the request URL, the `Accept` header, the `Authorization` header and the request body. **The key must never contain the raw `Authorization` header, or any other recoverable form of the token.** The store writes its keys to disk, and a key composed by concatenation would persist the user's token in a filename. Hash, never compose.

Keying on the URL alone is the failure this guards against in the other direction: it leaks across tokens and across Accept headers, so a second account, or a request for a different media type, would revalidate against an entry that was never its own.

go-gh's own `cacheKey` is the reference, and it does exactly this: SHA256 over `Method`, `URL`, `Accept`, `Authorization` and the body, rendered as hex. Note that it folds in **`Method` and the body**, which an earlier wording of this requirement omitted while claiming to mirror it. Both belong. A GET and a DELETE of one URL are different requests, and a POST body is part of what identifies one.

### Concurrency

**R21.** The local store must permit **exactly one writer at a time**, held by an **advisory file lock** at `store.lock` in the store's own directory, taken **non-blocking** at startup. A process that acquires it reads and writes for the rest of its lifetime. A process that does not **still reads the store and must never write to it**. A failed acquisition must degrade on the spot: it must not wait, must not poll the lock, and must not delay a launch.

**The lock is per store directory, and it guards this store alone.** Open question 6 resolved toward exactly the move it named: the store lives in the XDG cache directory and the lock lives with it ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)). [purge](../purge/requirements.md) R29's deletion log keeps the state directory and is no part of this: it is append-only, nothing reads it back, and two Purges appending to it need no arbitration from here. A lock on this store must never gate a DELETE.

**R22.** The lock must never wedge the store. Acquisition is the OS advisory lock (`flock(2)`), which the kernel releases when the holding process exits **by any means, SIGKILL included**, so a dead holder leaves no lock held and there is no stale-lock case to detect. The lock file's contents must be **empty and must stay empty**. An acquisition that fails for any reason other than contention, such as a filesystem that does not honour the lock, must degrade to reading exactly as contention does. R11 is unaffected: the lock file is derived, and deleting it costs nothing.

**The lock is mutual exclusion and not state, and [ADR-0006](../../adr/0006-stateless-bulk-jobs.md)'s own test is what says so.** No schema, because nothing parses it. No reconciliation, because nothing resumes from it. No second source of truth to disagree with the API, because it claims nothing about the world beyond "a process holds this". That ADR's amendment draws its boundary at reading rather than at the filesystem, and an empty file nobody opens for reading sits well inside it. R4 is untouched: a lock is not a crawl, an ID list, or progress.

**A PID in that file would be the design going wrong.** A lock file carrying a PID is a file something must read back and interpret, which needs staleness rules, which needs reconciling against a process that died holding it. `flock(2)` needs none of that, because the kernel drops the lock for us. Choose it because staleness is the kernel's problem, and keep the file empty so it stays the kernel's problem. `flock(2)` is reachable from the standard library on unix, so [ADR-0013](../../adr/0013-dependency-pins.md)'s pin set is unchanged on that limb. Windows has no `flock` and wants `LockFileEx`, and 2.0.0 ships Windows ([PRD](../../PRD.md), Scope, ruled 2026-07-16), so the limb is required: the same semantics, non-blocking acquisition, released by the kernel when the holding process exits by any means. Whatever spells both, wrapper or hand-rolled syscalls, is a `go.mod` question and therefore [ADR-0013](../../adr/0013-dependency-pins.md)'s to record when stage 1 lands.

**R23.** A degraded reader must apply R10's invalidation to its own in-memory view, and must not reach the persisted entry. An entry it could not invalidate can never be served stale regardless, because R8 forbids serving any entry without revalidating it, and a resource that changed answers 200. The cost of degrading is that one 200, which is exactly what R10 exists to save.

**The honest price, stated once.** While a Purge holds the lock, a Feed in another terminal reads every persisted ETag, collects its free 304s, and persists none of its own. Its newly-observed ETags die with the process, so the next cold start is slightly colder. R11 is what keeps that the whole cost: the store is derived, so a session that never writes loses speed and nothing else.

### The exhaustion record

**R24.** The local store must persist the Budget Readout's exhausted case, and only that case: the `Exhausted` flag and the `Reset` instant, keyed by a cryptographic hash of the token under R20's rule, never the raw token. `Remaining` and `Pressure` must never be read back, because both can be stale in either direction across a restart, and the first live response re-learns them. On launch, a persisted exhaustion whose `Reset` is still ahead of the injected clock must start the tool in its paused state: painting from the store as ever, stating the resumption time per [live-run-feed](../live-run-feed/requirements.md) R30, and issuing nothing until the clock passes `Reset`. A record whose `Reset` has passed, or whose token hash does not match, must be discarded unread.

**Within a reset window, exhaustion cannot go stale, and that is what makes it the one persistable field.** Other consumers of the token can only spend allowance, never refill it early, so an exhausted observation holds until its own `Reset` and expires by it. The check against the clock runs before the record is trusted, so the refusal open question 3 feared, declining to work on the strength of a stale number, cannot arise. The alternative was worse than slow: a persistence-free cold start after exhaustion fans ~26 or more requests into a limit at zero, which is issuing requests while limited, the documented path to the account block [PRD](../../PRD.md) risk R4 exists to avoid.

**The record is derived, and R11 holds.** Deleting it costs one risky fan-out, once, and nothing is recoverable only from it. It lives in the cache-directory store beside everything else derived. `store` may not import `governor` ([ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md)), so `main.go` mediates between the two, which is the hook [ADR-0014](../../adr/0014-domain-types-and-the-budget-readout.md) reserved for exactly this resolution.

## Acceptance criteria

**AC1: Paint before the network.** Given a populated local store and a cassette whose responses are held, the Feed renders rows before any response is delivered.

**AC2: Cold start is conditional.** Given persisted ETags, every revalidation request on cold start carries `If-None-Match`. None is unconditional.

**AC3: Cold start is free.** Given persisted ETags and a cassette returning 304 for every entry, the primary allowance's `used` counter does not advance across the whole cold start, and the painted Feed does not change.

**AC4: The baseline persistence removes.** Given an empty local store, every repository in the poll set costs a full 200 and the discovery pass re-probes. This is the cost R1 and R2 exist to avoid, and it is what an ETag-blind implementation would pay on every launch.

**AC5: Revalidation is real, not TTL.** Given an entry that a TTL implementation would consider fresh, a request is still issued and it carries `If-None-Match`. No entry is ever served without revalidation on the strength of its age alone.

**AC6: 304 versus 200 is visible.** The cache and the rate-governor can both distinguish a 304 from a 200 for the same resource. The 304 leaves the payload untouched and the 200 replaces it.

**AC7: Discovery survives restart.** Given persisted discovery results, a cold start yields the ~26-repository poll set with zero probe requests issued, and the capability recorded for each repository is the capability that was persisted.

**AC8: Last-revalidated is reportable.** With revalidation paused at Budget exhaustion, each painted entry's last-revalidated time is available and is the time the injected clock held at its last 200 or 304.

**AC9: A Purge leaves no crawl in the store.** Given a Purge crawling ~287 unfiltered pages and deleting Runs, the local store gains no entry for the crawl and its size does not grow with it. After a kill mid-Purge, no resume state exists on disk. [purge](../purge/requirements.md) R29's deletion log is not this store's, and its presence and its growth are expected rather than a failure of this criterion (R4).

**AC10: A write invalidates.** Given a successful deletion of Run X, no subsequent cold start serves X from the local store.

**AC11: Safe to delete.** With the store's entire cache directory removed, the tool launches, paints, and reaches the same Feed. The only observable difference is that the launch was slower. The deletion log's state directory is untouched by that wipe and appears nowhere in this criterion.

**AC12: Unknown schema version.** Given a cache file whose schema version the binary does not recognise, the launch succeeds, the cache is rebuilt, and no data loss is reported to the user because none occurred.

**AC13: Corrupt file.** Given a truncated or malformed cache file, the launch succeeds and the cache is rebuilt. No launch path fails on a bad cache.

**AC14: Host-qualified keys.** Every persisted key carries a host component, and no key can be constructed without one. A fixture asserting the key shape fails if `owner/name` is ever persisted bare.

**AC14a: No token reaches the disk.** Given a request carrying `Authorization: Bearer <token>`, the token's characters appear in no key, no filename and no file the store writes. Grepping the whole state directory for the token's value after a full session returns nothing. Two requests differing only in their `Authorization` header produce different keys, and two differing only in method (GET against DELETE of one URL) do too (R20).

**AC15: Bounded growth.** Across repeated sessions over the reference account's ~26 repositories with Runs, the on-disk footprint stays under R15's 50 MB bound and does not grow monotonically with session count. With the store at its bound, a new write succeeds by evicting the oldest last-revalidated entries first, and no newer entry is ever evicted while an older one remains.

**AC16: No TTL knob.** No flag or config key sets a cache TTL or a freshness lifetime.

**AC17: Deterministic.** Every test in this feature advances the injected clock rather than sleeping, and completes without a network.

**AC18: Two processes, one store directory.** Given two processes over one store directory against cassettes and the injected clock, one crawling and deleting while the other polls ~26 repositories, the store carries no corrupt entry and no partially-written one, and every read either process issues is served. The second process to start acquires no lock and writes nothing, while the first writes throughout. Its own reads still come from the store and still carry `If-None-Match`, so its 304s stay free (R21, R23). SIGKILLing the lock holder releases the lock, and a third process started afterwards acquires it and writes (R22). The lock file is empty at every point in this criterion, and deleting it mid-run costs nothing (R11).

**AC19: Exhaustion survives a restart.** Given a persisted exhaustion record whose `Reset` is ahead of the injected clock, a launch paints from the store, states the pause with its resumption time, and issues zero requests until virtual time passes `Reset`, after which revalidation proceeds normally. Given the same record with `Reset` in the past, the record is discarded, no pause is rendered, and the cold start is AC2's (R24).

**AC20: The exhaustion record is token-scoped and token-safe.** Given an exhaustion record persisted under one token and a launch under another, the record is ignored and the cold start proceeds. The token's characters appear in no part of the record or its key, per AC14a's grep (R24, R20).

## Constraints

**Conditional requests are free against the primary limit, and persistence is what lets a cold start collect that.** Measured by interleaving unconditional and conditional requests against one endpoint: `used` advanced by exactly one per round (120 → 121 → 122), attributable entirely to the 200 ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)). ETags must persist across sessions or every cold start pays full 200s: ~26 repositories of full payload for the Feed, plus ~163 probes if discovery is not cached either.

**Cold start must paint in under a second** when launched inside a repository (PRD success criterion 1), and **idle polling must consume ~0 primary allowance** (success criterion 3). R5 meets the first from disk. R6 keeps the second true at launch and not merely in the steady state.

**The Feed's per-repository window is bounded by the 1,000-result cap, not by the repository's history.** Any filter caps a listing at 1,000 silently ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)), which is a lie the Feed must label honestly. It is also, incidentally, a ceiling on what R16 could ever be asked to hold.

**A Purge is stateless by decision, not by omission** ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md)). Deletion is idempotent, so re-running the same Purge is the resume path and the filter is the durable state. The alternative (a persisted job queue) was considered and rejected for the cost of a job store, schema versioning, and reconciling IDs that vanished underneath it. R4 keeps that decision from being quietly undone here, which is the natural place for it to be undone.

**That rejection also swept up an audit trail, and the ADR is amended on exactly that point.** The three costs above are the job store's alone. A log nothing reads back pays none of them, so [purge](../purge/requirements.md) R29 requires one and R23 permits it. The decision this component enforces is unchanged, and its boundary is now stated as reading rather than as writing: R4 still forbids a crawl, an ID list and progress, and it never forbade a file.

**Repository identity is host-qualified from day one** ([ADR-0009](../../adr/0009-host-qualified-repo-identity.md)), because host is one field now against a migration of every persisted key later. This feature is the "later" that ADR is protecting against.

**go-gh's client never revalidates (PRD risk R2, resolved).** Free 304s are the economic basis of polling, of cold start and of the re-probe, and go-gh's cache is TTL-only: a hit returns without touching the network, and freshness is file mtime against a TTL. Measured against real go-gh v2.9.0, two identical GETs produced 1 network hit and 0 requests carrying `If-None-Match`. The economics in this document, [polling-scheduler](../polling-scheduler/requirements.md) and [rate-governor](../rate-governor/requirements.md) all still hold, because the 304s themselves are real and free. They are simply ours to send, which is what R19 exists for ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)).

## Open questions

1. **Resolved: go-gh's client is TTL-only and never revalidates.** The check was the one this question named: issue a request, issue it again, and look at whether the second carried `If-None-Match`. It did not. Verified three ways. `pkg/api/cache.go` returns on a cache hit without ever calling the inner RoundTripper, and freshness is file mtime against a TTL, which is the entire policy. A grep across the whole v2 module for `etag`, `if-none-match`, `if-modified-since`, `StatusNotModified` and `revalidat` returns zero matches, tests included, and go.sum carries no caching library. Empirically, real go-gh v2.9.0 with `EnableCache: true, CacheTTL: 30s` against a counting server serving an ETag: two identical GETs produced 1 network hit and 0 requests carrying `If-None-Match`, where a revalidating cache would show 2. R8 stands and is unaffected. What changed is that it is met by R19's transport of our own rather than by the client, which is verified end to end: `If-None-Match` reached the wire, the server returned 304, the body was reconstituted from the local store, and the caller saw a normal 200. The remedy is contained but not small, and it is now a build requirement rather than an open question ([ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)).

2. **Resolved: one writer at a time, held by an advisory lock, and a process that cannot take it still reads.** R21, R22 and R23 carry it and AC18 pins it. The case this question named has an answer: whichever process starts first writes, and the other reads every persisted ETag, collects its free 304s, and persists none of its own. No interleaved writes, no thrash, no corruption.

    **Last-write-wins was the thing to avoid rather than the thing to bound.** R13 does discard a corrupt entry and rebuild it, and the rebuild costs exactly the requests this store exists to avoid. Bounding damage is not avoiding it, and "your Feed got slower because your Purge trampled its ETags" was the outcome, not the mitigation.

    **Partitioning by process was rejected.** It removes contention by removing sharing, so every process cold-starts and N processes hold N copies of ~26 repositories of ETags.

    **The cost is R23's and it is small.** A Feed that loses the lock persists no new ETags, so the next cold start is slightly colder. R11 is why that is the whole of it.

3. **Resolved: the exhausted case persists, and nothing else does.** The trade this question stated was symmetric, and the Readout's fields do not carry it symmetrically. `Remaining` and `Pressure` can be stale in either direction across a restart, so they are never read back. Exhaustion cannot go stale within its own reset window, because other consumers of the token can only spend allowance and nothing refills it early, and the record expires by its own `Reset` field, checked against the clock before it is trusted. So the refusal this question feared cannot arise: the one field acted on is the one that holds until it expires. R24 carries the requirement, AC19 and AC20 pin it, and [ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md) records the decision with its options.

4. **Resolved: LRU by last-revalidated time, against a 50 MB total bound, enforced at write time.** R15 carries the policy and [ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md) the reasoning. The dropped-repository case this question raised is exactly why poll-set exit is not a trigger: a repository that reappears at the next re-probe finds its ETags intact and re-enters on free 304s, while one that never returns simply ages out under the bound when the space is needed. The per-repository bound was never eviction's to solve, because R16 already caps it at write time.

5. **Resolved: the 304 is trusted, and the aging-out hazard cannot occur.** A Run's `created_at` is immutable, so membership in a fixed `--created` window never changes and nothing ages out of it. A rolling window recomputed at request time produces a different URL on each poll, and R20 keys the store over the URL among the rest, so each recomputation is a different entry: a hit-rate cost, never a staleness one. There is no URL for which a Run can age out between two requests to the same key. The residual unknown is narrower than this question stated: whether GitHub's 304 is faithful to the response body on a filtered listing at all. That was commissioned as a measurement rather than assumed, using [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s technique (conditional GETs interleaved with unconditional ones against one filtered listing, GETs only, no limit tripped), and the measurement ran on 2026-07-19: 13 provable body changes in 75 interleaved rounds, every one answered with a 200 and a new ETag, no stale 304 and no spurious 200 ([research note](../../research/filtered-listing-etag.md)). R6 stands as written ([ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md)).

6. **Resolved: the store moves to a cache directory of its own, and the state directory keeps the log.** The half this question had already settled (the directory could not be reclassified wholesale, because the deletion log is genuinely state) is why the split is the shape. `$XDG_CACHE_HOME/gh-runs/` holds everything derived, `store.lock` included, and `$XDG_STATE_HOME/gh-runs/` holds the one file that is recoverable from nowhere else, which is also where the XDG spec says logs belong. Deleting the cache directory becomes safe by construction rather than by a footnote, and a backup policy that skips caches and keeps state is right for both files without knowing this tool exists. R1, R11 and R21 carry the addresses, and [ADR-0017](../../adr/0017-the-local-store-on-disk-contract.md) records the decision with its options.

## Related

- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md) (R1, R6, R8, and the ETags-must-persist consequence this feature implements)
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md) (R4, and R21's lock, which is mutual exclusion rather than state, and which the ADR's read-boundary admits)
- [ADR-0009: Repo identity is host-qualified](../../adr/0009-host-qualified-repo-identity.md) (R14)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) (R16's window, open question 5)
- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md) (the client open question 1 interrogated, and which R19 now wraps)
- [ADR-0012: The transport chain, and what ghclient may expose](../../adr/0012-transport-chain-and-the-client-surface.md) (R19a's base, R19b's reconstitution, and where the governor nests)
- [ADR-0017: The local-store's on-disk contract](../../adr/0017-the-local-store-on-disk-contract.md) (R1's home, R15's bound, R24's exhaustion record, and open question 5's measurement)
- [polling-scheduler](../polling-scheduler/requirements.md) (consumes R1's ETags, shares open question 1's fallout)
- [rate-governor](../rate-governor/requirements.md) (consumes R8's 304/200 distinction for R7 of that document)
- [repo-discovery](../repo-discovery/requirements.md) (R2 persists its results, R1 persists the ETags its conditional re-probe needs)
- [live-run-feed](../live-run-feed/requirements.md) (R5 paints its cold start, R7 keeps its R30 honest)
- [purge](../purge/requirements.md) (R4 is the enforcement of its R23, and R29's deletion log is the neighbour R4 and R11 keep this store apart from)
- [settings](../settings/requirements.md) (owns what R9 refuses to expose)
