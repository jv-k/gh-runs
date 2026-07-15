# Reclamation

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Constraints cite [PRD.md](../../PRD.md).

## Purpose

Answer one question (what is consuming this repository's Actions storage) and reclaim it in place. The answer is Caches, which outweigh Artifacts by ~160× on the reference repository, so this is a single bytes-first view led by Caches rather than an Artifact browser.

## Requirements

### Totals and honesty

**R1.** Display the repository's Cache totals from the cache-usage endpoint's `active_caches_size_in_bytes` and `active_caches_count`. Both arrive in one request. Neither may be recomputed by summing an enumerated list.

**R2.** Reconcile the enumerated Cache list against R1's totals. When the enumeration does not account for `active_caches_count` entries or `active_caches_size_in_bytes` bytes, label the list incomplete and continue to present R1's figures as the repository's totals. The free total is this view's truth oracle: it is what makes the list checkable without anyone having established whether a cap applies here.

**R3.** Sum the Artifact total from a complete enumeration, or label it an estimate. There is no Artifact equivalent of R1 in the canon, and the PRD's ~80 MB is extrapolated from 100 of 557 Artifacts rather than measured. Caches are exact to the byte for free. Artifacts are neither.

### The list

**R4.** Present Caches and Artifacts merged into a single list ordered by `size_in_bytes` descending by default. The sort carries the argument: it puts 302 MB Caches above ~145 KB Artifacts without a word of editorial copy.

**R5.** Mark every row's kind. A merged list must never leave it ambiguous whether a row is a Cache or an Artifact.

**R6.** Render each size in units suited to that row's own magnitude. A list spanning 302 MB and ~145 KB must not round the small rows to zero.

**R7.** Show `last_accessed_at` on every Cache row. Staleness is what makes a Cache safe to delete, and it must be readable without opening anything.

**R8.** Offer a filter to Artifacts alone. Under R4's sort all 96 Caches precede nearly all 557 Artifacts, which would otherwise put R13's download out of reach.

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

**R18.** Show enough in the confirmation to distinguish rows whose keys differ only in a version fragment or a hash suffix. `setup-go-macOS-arm64-go-1.26.5-06fc251f3` and `setup-go-macOS-arm64-go-1.26.5-20b85b5b8` differ by nine characters and 21,383 bytes out of 302 million, and R4's sort files them adjacently.

**R19.** Keep rows still while the cursor is in the list. A refresh must not reorder beneath it.

**R20.** Gate deletion on `permissions.push && !archived`, using the fields repository discovery already carries at no extra request. Distinguish an archived repository, which is permanently read-only, from one merely lacking push: an archived repository's 12.8 GB can never be reclaimed, and saying so is not the same as reporting a permission that might change.

**R21.** Treat the API as the final authority. A 403 can arrive despite `permissions.push: true` because fine-grained PATs expose no scopes. Record it and continue rather than treating it as a fault.

**R22.** Count a 404 on a deletion as a success. The Cache or Artifact is gone, which is what was asked.

**R23.** Route multi-row deletion through the rate governor rather than deleting as fast as the list allows.

**R24.** Report bytes reclaimed when a deletion completes, and adjust the displayed totals by exactly the deleted rows' sizes.

### Seams

**R25.** Render to a frame from held state alone, with no live terminal and no network, and verify that frame with golden-file tests covering R6's per-row byte formatting and R9's expired-Artifact tombstones. Both are properties of the painted row and of nothing else. R6's whole point is a list spanning 302,460,229 bytes and 145,212 bytes in which the small rows still render non-zero, and R9's is a row that says on its face that deleting it reclaims nothing. A unit test over the model can assert the bytes. Only a golden asserts what the row says about them.

## Acceptance criteria

**AC1: The totals come from one request.** Opening the view for `cli/cli` displays 12,823,331,523 bytes and 96 Caches, and exactly one request produces both figures.

**AC2: An incomplete list is labelled.** Given a stub whose cache-usage reports 96 Caches while enumeration yields 40, the view labels the list incomplete and still shows 96 as the repository's Cache count.

**AC3: The Artifact total is summed or labelled.** Any Artifact total the view presents is either summed from a complete enumeration or labelled an estimate. No total is extrapolated from a sample and presented plainly.

**AC4: The default sort is bytes descending.** For any two adjacent rows in the default order, the upper row's `size_in_bytes` is greater than or equal to the lower row's, irrespective of kind.

**AC5: Kind and staleness are on every row.** Every row states whether it is a Cache or an Artifact, and every Cache row shows `last_accessed_at`.

**AC6: No row rounds to zero.** A 145,212-byte Artifact and a 302,460,229-byte Cache in the same list both render a non-zero size.

**AC7: Expired Artifacts are tombstones and reclaim nothing.** In a 100-Artifact sample containing 10 with `expired: true`, exactly 10 rows render as tombstones, and the reclaimable total equals the summed `size_in_bytes` of the other 90 plus the Caches, unchanged whether the stub reports the expired Artifacts' size as their original value or as zero.

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

Measured on `cli/cli`:

| | Count | Storage |
|---|---|---|
| Caches | 96 | 12,823,331,523 bytes (12.8 GB), exact, one request |
| Artifacts | 557 | ~80 MB, extrapolated from 14,521,232 bytes per 100 sampled, ~145 KB each |

Caches outweigh Artifacts by ~160×. The storage story is Caches. Artifacts are a rounding error. The three largest Caches are near-duplicates burning ~302 MB apiece:

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
| 10 of 100 sampled Artifacts were already expired | Measured | Tombstones are the common case, not an edge case |
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

**UNKNOWN: is the per-repository cache size limit readable by a non-admin?** Displaying "12.8 GB / limit" would turn a number into a judgement, and R1's total is otherwise context-free. Do not invent the limit: if no endpoint reachable by a non-admin token returns it, the view shows the total alone.

**UNKNOWN: does deleting by key remove every Cache sharing that key?** [CONTEXT.md](../../CONTEXT.md) defines a Cache as scoped to a repository *and a ref*, so one key may correspond to several Caches across refs. R16 targets the id precisely because the blast radius of a key-targeted delete is unmeasured. Measure before offering key-targeted deletion anywhere.

**UNKNOWN: does the 1,000-result cap apply to Cache or Artifact listing?** [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) measured it on Run listing under a filter. At 96 and 557 neither sample came near it. R2 makes the Cache list honest either way, but no equivalent oracle protects R3's Artifact total.

**UNKNOWN: does `active_caches_size_in_bytes` reflect a deletion immediately?** R24 adjusts the total locally by the deleted rows' sizes. Whether a subsequent refresh agrees, or lags, is unverified. A total that jumps back up after a successful reclaim reads as a bug.

**Resolved: an expired Artifact keeps its original `size_in_bytes`.** 30 of 30 sampled expired Artifacts reported a non-zero size and none reported zero: 23 on `kubernetes/kubernetes`, ranging 13,852 to 234,131 bytes, and 7 on `cli/cli` at ~258 to 259 KB apiece. R10 no longer needs to be correct either way, and it now states the measured case. The size is always present, and it is always a lie about what deleting the Artifact reclaims, which is precisely what R9's tombstone and R11's zero-byte confirmation exist to say out loud.

**UNKNOWN: do these endpoints support ETag revalidation?** [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md)'s free 304s were measured against a Run listing. This view is opened and refreshed deliberately rather than polled, so nothing here depends on the answer.

**Resolved: both, and the person chooses. The cross-repo rollup is the default.** "Which of my 163 repositories is hoarding Caches?" is the question this view exists to answer, and the per-repo view cannot answer it at all, so the rollup leads. It is affordable at one request per repository over the fan-out [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md) already builds, and `active_caches_size_in_bytes` is exact and free per repository (PRD). `this-repo` stays available as a scope for the case where you are cleaning one repository deliberately. [settings](../settings/requirements.md) R19 owns the setting and defines what `this-repo` resolves to. Both code paths must exist, and every byte count must stay honest under either scope.

**Undecided: should multi-row deletion reuse a Purge's failure contract wholesale?** [purge](../purge/requirements.md) R18–R22 define 404-as-success, rate-limit-is-not-failure, and a 50-failure circuit breaker. R22 adopts the first. The other two are written for an operation lasting hours. 96 Caches is a minute's work, and whether the machinery is worth its weight here is unasked.

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
