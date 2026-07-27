# The local-store's on-disk contract

[local-store](../features/local-store/requirements.md) carried four open questions into stage 1, the earliest stage with unfinished decisions, and each touches what that stage writes to disk, which is expensive to revise once anything has written to it. This ADR resolves them together: where the store lives, what bounds its growth, which fragment of the Budget Readout survives a restart, and what a filtered listing's 304 is allowed to mean.

## The store moves to the cache directory, and the state directory keeps the log

The store, the persisted discovery results and R21's `store.lock` live under `$XDG_CACHE_HOME/gh-runs/`. `$XDG_STATE_HOME/gh-runs/` holds [purge](../features/purge/requirements.md) R29's `deletions.log` and nothing else.

**R11 already described a cache directory, and the requirement was arguing with its own address.** Derived, never authoritative, always safe to delete: that is the XDG Base Directory spec's definition of `$XDG_CACHE_HOME`'s contents. The deletion log is the opposite in every property (not derived, not reproducible, not safe to delete), and the spec names logs among the state directory's contents by example. Each file now sits where the spec says a file with its properties belongs.

**The split turns R11's footnote from a warning into a fact.** Before it, `rm -rf` over the shared directory was safe for this component and silently destroyed the one unrecoverable file beside it, which the requirement could only document. Now the obvious wipe target, the cache directory, is safe by construction, and a backup policy that skips caches and keeps state does the right thing for both files without knowing this tool exists.

**The lock moves with the store, exactly as R21 anticipated.** It guards this store alone, it lives in the store's own directory, and it never gated a write to the deletion log, so nothing about R21, R22 or R23 changes beyond the address.

## Eviction is LRU by last-revalidated, under one 50 MB bound

R15 required a bound per repository and in total, and named neither. The per-repository half was never eviction's to solve: R16 caps what is written per repository to the window the Feed paints. What eviction owns is the total, and the policy is one number and one ordering: a **50 MB total bound**, a compiled constant, enforced at write time by evicting the oldest last-revalidated entries first until the write fits.

**Recency ordering costs no new bookkeeping.** R3 already records last-revalidated per entry, so the eviction queue is a sort over metadata the store keeps anyway, and the cost of evicting the wrong entry is one full 200, the store's unit of failure everywhere else.

**A repository leaving the poll set triggers nothing, deliberately.** The open question's own worry was that a repository dropped from discovery's classification may reappear at the next re-probe. Under LRU that case needs no rule: an unused repository's entries stop being revalidated, age to the back of the queue, and fall out only if space is actually needed. A repository that returns first finds its ETags intact and re-enters on free 304s.

**50 MB is a backstop, not a target.** The bound is not a knob, by R9's own argument transferred: there is no value of it a user can pick that improves on the mechanism.

**Amended 2026-07-27, on measurement.** Two corrections, both to this section, both inputs to pricing the ceiling below.

The first: this section previously said the reference workload sits an order of magnitude below the bound, so reference use never evicts. Measured against `cli/cli`, a Runs listing at `per_page=100`, the scheduler's own poll shape, is 2,311,840 bytes (~2.20 MiB), and one Run object is ~23 KiB (the [PRD](../PRD.md)'s constraints table is where these live). Base64-in-JSON makes the stored entry about 1.33x that. A poll set of ~26 repositories that busy is past 50 MB on Run listings alone, so eviction is on the reference path rather than in a corner of it, and a quieter poll set never approaches the bound. The policy is unchanged and this is not a defect in it: LRU by last-revalidated is what makes both workloads correct. What was wrong was the claim about which one the reference workload is, and the figure it rested on was never measured.

The second: this section previously said R16 caps what is written per repository "at write time, before eviction is involved". That is true of no code. Nothing in `store` bounds anything per repository, and the phrase is struck above. R16 is a property of what the tool requests, applied before a response exists at all, which is what the table in the amendment below records.

## Amendment: a per-entry ceiling, above both the bounds this ADR named

**The store declines to hold a response body above 8 MiB, and declines to buffer it rather than merely to save it.** local-store R25 carries it, AC21 pins it, and [issue 138](https://github.com/jv-k/gh-runs/issues/138) is where it was decided.

This ADR resolved what bounds the store's growth and named two bounds. It needs a third, because the two it named both assume the response in hand is a resource worth keeping, and nothing in the store tested that assumption. `persist` read whatever it was handed, in full, before the ETag was looked at, and the only thing standing between the store and an arbitrarily large binary was a composition-root convention: name the client `blob` rather than `shared` for anything large. That convention is invisible from inside the store, enforced only by review, and has been got wrong three times: an Artifact download ([issue 85](https://github.com/jv-k/gh-runs/issues/85)), a whole-Run log export ([issue 89](https://github.com/jv-k/gh-runs/issues/89)), and a per-Job log fetch ([issue 153](https://github.com/jv-k/gh-runs/issues/153)), the last of which was live on `main` when this amendment was written.

**The three bounds, and where each acts:**

| Bound | Enforced by | Guards |
|---|---|---|
| R16, per repository | the caller's URL, before a response exists | the store never holds a repository's Run history |
| R25, per entry, 8 MiB | `persist`, at buffer time | one response cannot be a payload |
| R15, 50 MB total, LRU | `enforceBound`, after the write lands | footprint across sessions |

**The rule keys on size and on nothing else.** [Issue 89](https://github.com/jv-k/gh-runs/issues/89) proposed two rules and only one of them survives inspection. A size ceiling fixes the class without knowing anything about the endpoint. A rule declining any response whose request did not address a `/repos/` path does not: `repoOf` returns `""` for any path whose first segment is not `repos`, and repo-discovery's account enumeration is `/user/repos`, so the path rule would decline the one response this store most wants to keep. The signal that separates a signed blob URL from that enumeration is the host rather than the path, and a host rule works at the cost of giving the store a notion of "the API host" it does not have. Size costs nothing the store is not already holding.

**Not read from `Content-Length`.** go-gh sets no `Accept-Encoding`, so `net/http` adds `gzip` itself, decodes transparently, and deletes the header. It is therefore absent for gzipped JSON, which is every response worth keeping, and present for binary downloads, which are what the ceiling refuses. The size is measured by reading at most the ceiling plus one byte.

**8 MiB is priced off the largest legitimate response, not off the total bound.** ~3.6x the measured 2.20 MiB (the [PRD](../PRD.md)'s constraints table holds the figure and the three responses measured beside it), so a repository busier than the reference one does not silently stop caching the Feed's own poll, a failure that costs a full 200 per cycle and shows nowhere. Independent of the 50 MB figure because the two answer different questions: this one asks whether a response is a resource or a payload, R15's asks how much is kept. Not a knob, by the same R9 transfer that this ADR already applies to R15's bound.

**What the ceiling does not close.** It bounds the second of the two costs rather than eliminating it. A blob response *under* 8 MiB is still saved, and if its URL addresses no repository it is still stored with `Repo: ""`, which repository invalidation can never reach, so it sits until LRU evicts it. That residue is a few kilobytes against a 50 MB budget, which is why the size rule is enough on its own and the host rule was not worth its cost. The client-side choice is what closes it, and this is what defence in depth means here.

**The client-side choice stays.** [ADR-0012](./0012-transport-chain-and-the-client-surface.md)'s two entry points are unchanged, and a binary download still enters the chain below the store. A store-side rule and a client-side choice are defence in depth rather than alternatives.

## The Budget Readout persists as its exhaustion, and nothing else

Open question 3 presented a symmetric trade, and the Readout's four fields do not carry it symmetrically. `Remaining` and `Pressure` genuinely perish across a restart and can be stale in either direction, so they are never read back. The exhausted case is different: **within a reset window the primary allowance only ever falls**, other consumers of the token can spend it and nothing can refill it early, so "exhausted, resets at 14:32" observed at 14:10 is still true at 14:20. The record also expires by its own `Reset` field, and once the clock passes it, the record is discarded unread. The staleness objection never applies to the one field acted on.

**What is written: `Exhausted` and `Reset`, keyed by a hash of the token under R20's rule, never the raw token.** A different token ignores the record. On launch inside a still-future `Reset`, the tool paints from the store as ever, states the pause with its resumption time ([live-run-feed](../features/live-run-feed/requirements.md) R30 and [polling-scheduler](../features/polling-scheduler/requirements.md) R16 already carry that rendering), and issues nothing until the injected clock passes `Reset`.

**The case this prevents is the one the project is built around.** A user whose Purge hit exhaustion restarts the tool, and a persistence-free cold start fans ~26 or more requests into a limit at zero. That is issuing requests while limited, the documented path to the account block [PRD](../PRD.md) risk R4 exists to avoid. Re-learning exhaustion by fan-out is the hammering with a cold start as the excuse.

**The record is derived and R11 holds.** Deleting it costs one risky fan-out, once, and nothing is recoverable only from it. It lives in the cache-directory store with everything else derived. `store` may not import `governor` ([ADR-0011](./0011-package-layout-and-dependency-direction.md)), so `main.go` mediates between the two, which is exactly the hook [ADR-0014](./0014-domain-types-and-the-budget-readout.md) reserved for this resolution.

## A filtered listing's 304 is trusted, and the aging-out hazard dissolves by keying

Open question 5 named the hazard: a Run aging out of a `created` window changing what the listing would return without changing the ETag. Walked through R20's keying, it cannot occur. A Run's `created_at` is immutable, so membership in a **fixed** window never changes and nothing ages out of it. A **rolling** window recomputed at request time produces a different URL on each poll, and R20 keys the store over the URL among the rest, so each recomputation is a different entry. That is a hit-rate cost, never a staleness one. There is no URL for which a Run can age out between two requests to the same key.

**The residual unknown is narrower and is measurable within policy.** Whether GitHub's 304 is faithful to the response body on filtered listings is what R6 actually stands on. HTTP semantics say it is, and this canon trusts measurement over recall, so a research ticket runs [ADR-0004](./0004-conditional-polling-for-liveness.md)'s own technique: conditional GETs interleaved with unconditional ones against a single filtered listing on an active public repository, GETs only, no DELETE, no limit tripped. Until that measurement contradicts it, R6 stands as written, and a contradiction would be an API bug worth reporting as well as a design input. The measurement ran on 2026-07-19 and confirmed faithfulness: 13 provable body changes in 75 interleaved rounds, each answered with a 200 and a new ETag, no stale 304 and no spurious 200 ([research note](../research/filtered-listing-etag.md)).

## Considered Options

**Everything stays in the state directory.** One directory, and the R11 footnote stays a live hazard: the obvious wipe destroys the deletion log, and no backup policy is right for both files at once.

**Everything moves to the cache directory.** One directory again, and worse: the log is not derived and not safe to delete, so any cache-cleaning tool, or a user wiping caches on instinct, destroys the only record of every deletion the tool has made.

**Evict when a repository leaves discovery's classification.** Deterministic, and wrong for the case the open question itself raised: a repository that reappears at the next re-probe pays full 200s that LRU would have kept free.

**A freshness TTL sweep** (delete entries not revalidated in N days). It evicts working entries on a machine that was merely switched off, and N is a freshness lifetime by another name, which R9's reasoning resists.

**Count bounds** (N entries per repository, M total). Blind to entry size, and R15 asks for a disk-footprint bound.

**A `/repos/` path rule** for the per-entry ceiling (decline any response whose request did not address a repository). Proposed in [issue 89](https://github.com/jv-k/gh-runs/issues/89) and rejected on inspection: `repoOf` yields `""` for any path whose first segment is not `repos`, and repo-discovery's account enumeration is `/user/repos`, so the rule would decline the one response this store most wants to keep. It fails in the direction that costs a full 200 every poll.

**A host rule** for the same ceiling (decline anything not addressed to the API host). It does separate a signed blob URL from the account enumeration correctly, which the path rule does not, and it costs the store a notion of "the API host" that it does not have and would have to be handed at construction. Size needs nothing the store is not already holding and covers a response shape nobody has thought of yet.

**Declining to save without declining to buffer.** The one-line version of the ceiling, and it reclaims neither cost that matters: the read runs before the ETag is looked at, so the response is already held whole by the time the rule fires.

**Persisting the full Readout.** `Remaining` can be stale in either direction across a restart, and acting on it is the exact hazard the open question warned about. The monotonicity argument covers exhaustion alone.

**Persisting no Readout at all.** Simplest, and it makes the restart-after-exhaustion cold start hammer a limit at zero before it can even say why it is stuck.

**A periodic unconditional refresh** to hedge filtered-listing 304s. It spends primary allowance permanently, breaks AC3's free cold start, and hedges a failure never observed. Measurement first.

## Consequences

**local-store's requirements carry the contract.** R1 names the cache directory, R11's footnote becomes the split's statement, R15 names the bound and the policy, R21 names the lock's home, and a new R24 carries the exhaustion record with acceptance criteria for the paused launch and the token mismatch. Open questions 3 through 6 are closed in place.

**Three neighbouring documents change one line each.** [CONTEXT.md](../CONTEXT.md)'s local-store entry names the cache directory. [settings](../features/settings/requirements.md) R2 names both directories rather than one. [ADR-0012](./0012-transport-chain-and-the-client-surface.md)'s wiring note stops calling the store's `dir` the state directory.

**purge R29 is untouched.** The deletion log's path, preconditions and append-only nature are as ADR-0006's amendment left them. The log simply stops sharing its directory.

**[ADR-0013](./0013-dependency-pins.md)'s pin set is unchanged.** The store resolves `$XDG_CACHE_HOME` the same way the log already resolves `$XDG_STATE_HOME`, one directory over, and no new dependency arrives with the move.

**One measurement is commissioned rather than assumed.** The filtered-listing ETag ticket is the map's, AFK, and bounded by the same rules as every probe: GETs only, no DELETE, no limit deliberately tripped.
