# Reclamation

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Constraints cite [PRD.md](../../PRD.md).

## Purpose

Answer one question (what is consuming this repository's Actions storage) and reclaim it in place. The answer is Caches, and the reason is not the one it first looks like.

**Artifacts are the bigger number and the smaller problem.** Measured on `cli/cli` by summing all 581 Artifacts rather than sampling: Artifacts hold 15.55 GB against the Caches' 10.59 GB, so on raw bytes Artifacts **win**. But 279 of the 581 (48%) are expired, and they hold 15.50 GB of that 15.55 GB. Deleting an expired Artifact reclaims nothing. Live Artifacts total **47.68 MB**, which 10.59 GB of Caches genuinely dwarfs by ~220×.

So this is a single bytes-first view led by Caches rather than an Artifact browser. Caches lead because **an expired Artifact's bytes are already gone**, not because Artifacts are small. A view that sorted on raw `size_in_bytes` and stopped thinking would put 15.5 GB of tombstones at the top and offer to reclaim nothing at all, which is exactly what R9, R10 and R11 exist to prevent.

## Requirements

### Totals and honesty

**R0.** This view MUST operate under **either scope**, `all-repos` or `this-repo`, and MUST default to `all-repos` ([settings](../settings/requirements.md) R19). Both code paths must exist and both must be correct. Under `all-repos` the view fans out one cache-usage request per repository over [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)'s machinery and leads with a per-repository rollup, because "which of my 163 repositories is hoarding Caches?" is the question this view exists to answer and a single-repository view cannot answer it at all. Under `this-repo` it presents one repository. Every requirement below reads "the repository" as **each repository in scope**, and every byte count must stay honest under either.

**R1.** Display each in-scope repository's Cache totals from the cache-usage endpoint's `active_caches_size_in_bytes` and `active_caches_count`. Both arrive in one request per repository. Neither may be recomputed by summing an enumerated list. Under `all-repos`, the rollup's total MUST be the sum of those per-repository figures and MUST NOT be derived any other way.

**R2.** Reconcile the enumerated Cache list against R1's totals. When the enumeration does not account for `active_caches_count` entries or `active_caches_size_in_bytes` bytes, label the list incomplete and continue to present R1's figures as the repository's totals. The free total is this view's truth oracle: it is what makes the list checkable without anyone having established whether a cap applies here.

**R3.** Sum the Artifact total from a complete enumeration, or label it an estimate. There is no Artifact equivalent of R1. Caches are exact to the byte for free. Artifacts are neither.

**This requirement has now been vindicated by the exact failure it warns about.** An earlier ~80 MB figure in the canon was extrapolated from 100 of 557 Artifacts. Enumerating all 581 produced **15.55 GB**. The sample was wrong by ~194×, and it was wrong because expired Artifacts are not evenly distributed and the first page is the newest. Extrapolation is not a cheap approximation here. It is a different answer.

### The list

**R4.** Present Caches and Artifacts merged into a single list ordered by `size_in_bytes` descending by default. The sort carries the argument: it puts 302 MB Caches above ~145 KB Artifacts without a word of editorial copy.

**R5.** Mark every row's kind. A merged list must never leave it ambiguous whether a row is a Cache or an Artifact.

**R6.** Render each size in units suited to that row's own magnitude. A list spanning 302 MB and ~145 KB must not round the small rows to zero.

**R7.** Show `last_accessed_at` on every Cache row. Staleness is what makes a Cache safe to delete, and it must be readable without opening anything.

**R8.** Offer a filter to Artifacts alone. Under R4's sort the Caches precede nearly all the Artifacts (83 against 581, measured), which would otherwise put R13's download out of reach. Expired Artifacts make this worse rather than better: 279 tombstones still report their original size, so they sort high on bytes they no longer hold.

### Expired Artifacts

**R9.** Mark every Artifact whose `expired` field is true as a tombstone, and state on the row that deleting it reclaims nothing. The bytes are already gone. Only the record remains.

**R10.** Exclude expired Artifacts from every figure the view presents as reclaimable. An expired Artifact keeps reporting its original non-zero `size_in_bytes` (measured, 30 of 30), so the number is always sitting there to be added up by mistake, and it must never contribute to a reclaimable figure.

**R11.** State a reclaim figure of zero bytes when confirming deletion of an expired Artifact.

**R12.** Show `expires_at` on live Artifact rows, so that an Artifact about to expire on its own is visibly not worth deleting. Artifacts expire on a retention policy without anyone acting.

### Actions

**R13.** Offer download of an Artifact from its row. Downloading is a genuine non-storage use case and must remain available independent of whether the Artifact is worth deleting.

**R14.** Offer no download on an expired Artifact, whose download answers 410 Gone. If one is attempted anyway, report the bytes as gone rather than as a transient failure.

**R15.** Offer deletion of a Cache and of an Artifact from its row, and support selecting several rows for one deletion.

**R16.** Target a Cache's id when deleting from a row, never its key. A Cache is scoped to a repository and a ref, so a key may name more than one Cache. See Open questions.

### Confirmation and eligibility

**R17.** Route every deletion through the Purge's confirmation, [purge](../purge/requirements.md) R4–R9: selection keyed by id, the set frozen when the modal opens, a displayed count, friction scaled to blast radius, and no path from a selection to a first DELETE that skips confirmation.

**Record every Cache and Artifact deletion in [purge](../purge/requirements.md) R29's deletion log**, one line per attempt, with `cache` or `artifact` as the kind and R16's id as the id. R29's failure mode binds here: a log that cannot be written stops the deletion. An expired Artifact is logged like any other, even though R11 confirms it at zero bytes, because R29 records what was destroyed rather than what was reclaimed. **R29 does not arrive with R4 to R9 and has to be stated here.** Those six are selection and confirmation, R29 is execution, and this feature is where "which of my 163 repositories did I just take 10.59 GB out of" gets asked.

**R18.** Show enough in the confirmation to distinguish rows whose keys differ only in a version fragment or a hash suffix. `setup-go-macOS-arm64-go-1.26.5-06fc251f3` and `setup-go-macOS-arm64-go-1.26.5-20b85b5b8` differ by nine characters and 21,383 bytes out of 302 million, and R4's sort files them adjacently.

**R19.** Keep rows still while the cursor is in the list. A refresh must not reorder beneath it.

**R20.** Gate deletion on `permissions.push && !archived`, using the fields repository discovery already carries at no extra request. Distinguish an archived repository, which is permanently read-only, from one merely lacking push: an archived repository's 10.59 GB can never be reclaimed, and saying so is not the same as reporting a permission that might change.

**R21.** Treat the API as the final authority. A 403 can arrive despite `permissions.push: true` because fine-grained PATs expose no scopes. Record it and continue rather than treating it as a fault.

**R22.** Count a 404 on a deletion as a success. The Cache or Artifact is gone, which is what was asked.

**R23.** Route multi-row deletion through the rate governor rather than deleting as fast as the list allows.

**R24.** Report bytes reclaimed when a deletion completes, and adjust the displayed totals by exactly the deleted rows' sizes.

### Seams

**R25.** Render to a frame from held state alone, with no live terminal and no network, and verify that frame with golden-file tests covering R6's per-row byte formatting and R9's expired-Artifact tombstones. Both are properties of the painted row and of nothing else. R6's whole point is a list spanning 302,460,229 bytes and 145,212 bytes in which the small rows still render non-zero, and R9's is a row that says on its face that deleting it reclaims nothing. A unit test over the model can assert the bytes. Only a golden asserts what the row says about them.

## Acceptance criteria

**AC1: The totals come from one request.** Given a cache-usage fixture reporting `active_caches_size_in_bytes` and `active_caches_count`, the view displays exactly those two values, and exactly one request produces both. **The assertion reads the fixture, never a constant.** An earlier form of this criterion hardcoded 12,823,331,523 bytes and 96 Caches as a golden, which live measurement has since moved to 10,587,236,096 and 83, and which would move again next week: `cli/cli`'s storage is not ours and does not hold still. What this criterion pins is that the view reports the endpoint's figures unaltered and computes neither ([PRD](../../PRD.md) on point-in-time counts).

**AC2: An incomplete list is labelled.** Given a stub whose cache-usage reports N Caches while enumeration yields fewer, the view labels the list incomplete and still shows N as the repository's Cache count. N comes from the fixture.

**AC3: The Artifact total is summed or labelled.** Any Artifact total the view presents is either summed from a complete enumeration or labelled an estimate. No total is extrapolated from a sample and presented plainly.

**AC4: The default sort is bytes descending.** For any two adjacent rows in the default order, the upper row's `size_in_bytes` is greater than or equal to the lower row's, irrespective of kind.

**AC5: Kind and staleness are on every row.** Every row states whether it is a Cache or an Artifact, and every Cache row shows `last_accessed_at`.

**AC6: No row rounds to zero.** A 145,212-byte Artifact and a 302,460,229-byte Cache in the same list both render a non-zero size.

**AC7: Expired Artifacts are tombstones and reclaim nothing.** Given a fixture of 581 Artifacts of which 279 carry `expired: true`, exactly 279 rows render as tombstones, and the reclaimable total equals the summed `size_in_bytes` of the other 302 plus the Caches, unchanged whether the stub reports the expired Artifacts' size as their original value or as zero. On the measured data that total is 47.68 MB of Artifacts against 10.59 GB of Caches, and a naive sum would report 15.55 GB of Artifacts, over 300× the truth. This is the criterion that catches it.

**AC8: An expired Artifact confirms at zero bytes.** Confirming deletion of an expired Artifact shows a reclaim figure of zero bytes.

**AC9: 410 Gone reads as gone.** Download is unavailable on every row with `expired: true`. Given a stub returning 410 Gone, the outcome reads as the bytes being gone, not as a network failure.

**AC10: Deletion targets the id, not the key.** Given the three near-duplicate `setup-go-macOS-arm64` Caches, selecting the 302,442,412-byte row and confirming issues a delete carrying that Cache's id. The other two remain listed afterwards.

**AC11: The frozen set holds.** Selecting rows, opening the confirmation, then mutating the underlying data deletes exactly the frozen set.

**AC12: The total adjusts by the deleted bytes.** Deleting a Cache of 302,460,229 bytes decreases the displayed total by exactly 302,460,229.

**AC13: The gate distinguishes archived from unpermitted.** With `permissions.push: false`, or `archived: true`, no delete action is offered and no delete request is issued. The archived case is worded differently from the unpermitted one.

**AC14: A 403 is an outcome, not a fault.** Given a stub returning 403 for a delete in a repository whose `permissions.push` is true, the outcome is recorded as a permission result, the deletion is not retried, and the view continues.

**AC15: A 404 is success.** Given a stub returning 404 for a delete, the outcome is counted as a success.

**AC16: Multi-row deletion goes through the governor.** Deleting N rows issues no delete faster than the governor permits.

**AC17: Goldens hold the list's frame.** Rendering a recorded frame from held state, with no terminal and no network, reproduces the stored golden byte for byte. Separate goldens fix a list holding both the 302,460,229-byte Cache and the 145,212-byte Artifact, each size rendered in units suited to its own magnitude and neither shown as zero, and a row for an Artifact with `expired: true` rendering as a tombstone that states deleting it reclaims nothing.

## Constraints

Measured on `cli/cli`. These are point-in-time and drift ([PRD](../../PRD.md)). No test may hardcode one.

| | Count | Storage |
|---|---|---|
| Caches | 83 | 10,587,236,096 bytes (10.59 GB), exact, one request |
| Artifacts, all | 581 | 15,548,177,058 bytes (15.55 GB), summed across 6 pages |
| Artifacts, expired | 279 (48%) | 15.50 GB, reclaimable: **zero** |
| Artifacts, live | 302 | **47.68 MB** |

**Read that table in the right order.** Artifacts hold more bytes than Caches. They also hold almost nothing worth deleting: 48% of them are tombstones, and those tombstones are 99.7% of the Artifact bytes. Against the 47.68 MB a person can actually reclaim from Artifacts, 10.59 GB of Caches is ~220×. The storage story is Caches, and the argument runs through **expiry**, never through size.

Note what the earlier canon got right by luck. It claimed Caches beat Artifacts ~160× on the strength of a ~80 MB Artifact estimate that was wrong by ~194× and had the sign of the raw comparison backwards. The conclusion survived. The reasoning did not, and R3 is what caught it.

The three largest Caches are near-duplicates burning ~302 MB apiece:

```
setup-go-macOS-arm64-go-1.26.4-d79741d50   302,460,229
setup-go-macOS-arm64-go-1.26.5-06fc251f3   302,442,412
setup-go-macOS-arm64-go-1.26.5-20b85b5b8   302,421,029
```

| Fact | Source | Consequence |
|---|---|---|
| `GET /repos/{o}/{r}/actions/cache/usage` returns `active_caches_size_in_bytes` and `active_caches_count` | Measured | R1, R2. A repo total in one request, with no arithmetic and nothing to get wrong |
| Caches list sortable by `size_in_bytes`, exposing `last_accessed_at` | Measured | R4, R7. Staleness is directly visible |
| Caches are deletable by key or by id | Measured | R16 chooses id |
| A Cache is scoped to a repository **and a ref** | [CONTEXT.md](../../CONTEXT.md) | A key is not obviously unique. R16 and Open questions |
| Artifacts expose `size_in_bytes`, `expired`, `expires_at`, `created_at` | Measured | R9, R10, R12 |
| **279 of 581 Artifacts are expired (48%)** | Measured across a full enumeration, not sampled | Tombstones are not the common case, they are nearly half the list, and they hold 15.50 GB of its 15.55 GB. An earlier 10-of-100 sample read this as 10% |
| **Deleting an expired Artifact reclaims nothing** | Measured | R9–R11. The bytes are already gone and the record is a tombstone |
| **An expired Artifact still reports its original `size_in_bytes`** | Measured, 30 of 30, none zero | R10. The number outlives the bytes, so a reclaimable total that sums naively is wrong by exactly the tombstones |
| Artifacts auto-expire on a retention policy without user action | Measured | R12. The artifact half of this view is largely busywork. The cache half is where a real problem lives |
| An expired Artifact's download returns 410 Gone | Measured | R14 |
| Repo permissions and `archived` ride along free on `/user/repos` | PRD | R20 costs nothing |
| Archived repositories are permanently read-only | PRD | Their storage can never be reclaimed. R20 must say so rather than imply a retry would help |
| Fine-grained PATs expose no `x-oauth-scopes` | PRD | Pre-flight checks are impossible. R21 |
| DELETE costs 5 points against ~900/min. GitHub's prose advises ≥1s between writes | PRD, [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) | R23. The two numbers disagree 3×, so the rate is the governor's to choose, not this view's |
| The tool never lies about counts | PRD success criterion 6 | R2, R3 |
| 2.0.0 serves github.com only | [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) | |

## Open questions

**UNKNOWN: is the per-repository cache size limit readable by a non-admin?** Displaying "10.59 GB / limit" would turn a number into a judgement, and R1's total is otherwise context-free. Do not invent the limit: if no endpoint reachable by a non-admin token returns it, the view shows the total alone.

**UNKNOWN: does deleting by key remove every Cache sharing that key?** [CONTEXT.md](../../CONTEXT.md) defines a Cache as scoped to a repository *and a ref*, so one key may correspond to several Caches across refs. R16 targets the id precisely because the blast radius of a key-targeted delete is unmeasured. Measure before offering key-targeted deletion anywhere.

**UNKNOWN: does the 1,000-result cap apply to Cache or Artifact listing?** [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) measured it on Run listing under a filter. At 83 and 581 neither came near it, and the Artifact enumeration paginated to 6 pages without truncating. R2 makes the Cache list honest either way, but no equivalent oracle protects R3's Artifact total.

**UNKNOWN: does `active_caches_size_in_bytes` reflect a deletion immediately?** R24 adjusts the total locally by the deleted rows' sizes. Whether a subsequent refresh agrees, or lags, is unverified. A total that jumps back up after a successful reclaim reads as a bug.

**Resolved: an expired Artifact keeps its original `size_in_bytes`.** 30 of 30 sampled expired Artifacts reported a non-zero size and none reported zero: 23 on `kubernetes/kubernetes`, ranging 13,852 to 234,131 bytes, and 7 on `cli/cli` at ~258 to 259 KB apiece. R10 no longer needs to be correct either way, and it now states the measured case. The size is always present, and it is always a lie about what deleting the Artifact reclaims, which is precisely what R9's tombstone and R11's zero-byte confirmation exist to say out loud.

**UNKNOWN: do these endpoints support ETag revalidation?** [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s free 304s were measured against a Run listing. This view is opened and refreshed deliberately rather than polled, so nothing here depends on the answer.

**Resolved: both, and the person chooses. The cross-repo rollup is the default.** "Which of my 163 repositories is hoarding Caches?" is the question this view exists to answer, and the per-repo view cannot answer it at all, so the rollup leads. It is affordable at one request per repository over the fan-out [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md) already builds, and `active_caches_size_in_bytes` is exact and free per repository (PRD). `this-repo` stays available as a scope for the case where you are cleaning one repository deliberately. [settings](../settings/requirements.md) R19 owns the setting and defines what `this-repo` resolves to.

**R0 now carries this into the requirements, which is where it was missing.** This resolution said "both code paths must exist" while R1 and the Purpose line specified one, describing a single repository's totals throughout. A decision recorded only in an open question is a decision an implementer reads last, and this one changes the shape of the view's top-level state. R0 is the normative form. Rows 5 and 20 below still read "the repository" and now mean each repository in scope.

**Undecided: should multi-row deletion reuse a Purge's failure contract wholesale?** [purge](../purge/requirements.md) R18–R22 define 404-as-success, rate-limit-is-not-failure, and a 50-failure circuit breaker. R22 adopts the first. The other two are written for an operation lasting hours. 83 Caches is a minute's work, and whether the machinery is worth its weight here is unasked.

## Related

- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R22's 404-as-success.
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R23.
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). The cap whose reach over these endpoints is unmeasured, and the rule that a reported total is not a reachable one.
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). Why this view is refreshed rather than polled.
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). The fan-out a cross-repo rollup would ride on.
- [ADR-0009: Host-qualified repo identity](../../adr/0009-host-qualified-repo-identity.md). How a repository is keyed here.
- [purge](../purge/requirements.md) owns R17's confirmation machinery and R23's governor contract.
- [repo-discovery](../repo-discovery/requirements.md) supplies R20's free `permissions` and `archived`.
- [rate-governor](../rate-governor/requirements.md) implements R23.
- [live-run-feed](../live-run-feed/requirements.md): the surface this one is reached from, and the reason R19 exists.
- [cli-surface](../cli-surface/requirements.md): a non-interactive Reclamation, if there is one.
