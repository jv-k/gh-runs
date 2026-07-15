# Filtered listing for the Feed, unfiltered crawl for a Purge

**Applying any filter silently caps listing at 1,000 results.** Measured on `cli/cli`:

```text
total_count, unfiltered:        28,694
total_count, status=success:    18,258
page 11 (results 1001–1100) unfiltered: 100 runs
page 11 WITH a filter:                    0 runs
```

The API reports `total_count: 18258`, then returns an empty array past result 1,000 with **no error and no flag**. Naive pagination sees `[]`, concludes it is done, and silently misses ~17,250 matching Runs. Unfiltered paging walks the whole 28,694.

So the Feed uses server-side filters (cheap, ETag-friendly, capped) and **labels the cap honestly**, as "1,000 of ~18,258". A Purge switches to an unfiltered crawl of about 287 requests, where counts are exact and old Runs are directly reachable.

Those totals are point-in-time. They drift with every Run `cli/cli` invokes, and the [PRD](../PRD.md) says what that means for a test.

## Considered Options

Both alternatives collapse the design to one code path, and each fails for a different reason.

**Wave deletion.** Always filter, delete the reachable 1,000 or fewer, re-query so the next 1,000 appears, repeat. One code path and naturally resumable, but the UI can never show a true total.

**Always crawl unfiltered.** Exact counts everywhere and no cap semantics to explain, but it pays a full crawl per repository and makes ETag revalidation coarse, because any new Run invalidates the whole list.

## Consequences

Maintaining two code paths through the data layer is the price. The two jobs have opposite needs: the Feed wants cheap revalidation of a small recent window, a Purge wants exhaustive reach.

**Never trust `total_count` in a filtered view.** It reports matches, not reachable matches, and the difference is silent. Any code that paginates a filtered list and stops on an empty page is wrong.

**The `Link` header is honest exactly where the crawl needs it and dishonest exactly where the Feed must not trust it.** Measured on `cli/cli`, both directions.

Unfiltered, `Link` can be trusted. `rel="next"` disappears at the true end: page 400 returns HTTP 200, `[]`, `total_count` preserved at 28,694, and a `Link` carrying `prev`, `last` and `first` but **no `next`**. `rel="last"` agrees with `total_count` (page 287, and 287 x 100 = 28,700 against a claimed 28,694), so a crawl knows its own length from request one. Crawl on `next`, stop when it is gone, and never compute the end from `total_count`.

Filtered, `Link` lies in the same way `total_count` does, because it is derived from it. Page 1 of `status=success` claims `rel="last"` is **page 183** while only 10 pages are ever served, and past the cap `total_count` collapses to **0** rather than holding at 18,258. Neither number may seed a crawl, and the 0 is the cap rather than exhaustion.

**Note what this rules out.** `rel="next"` does **not** vanish at the 1,000 cap. Were it to, the cap would announce itself on the very header a paginating client already reads, and this ADR's premise that the cap is silent would be false. The cap is silent precisely because the filtered `Link` keeps offering a `next` that leads to an empty page.

Runs sort newest-first, so a filtered view reaches only the *newest* 1,000 matches, while "delete Runs older than 90 days" asks for the oldest. That asymmetry is the whole reason a Purge cannot reuse the Feed's path.
