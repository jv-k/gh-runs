# Filtered listing for the Feed, unfiltered crawl for a Purge

**Applying any filter silently caps listing at 1,000 results.** Measured on `cli/cli`:

```text
total_count, unfiltered:        28,707
total_count, status=success:    18,260
page 11 (results 1001–1100) unfiltered: 100 runs
page 11 WITH a filter:                    0 runs
```

The API reports `total_count: 18260`, then returns an empty array past result 1,000 with **no error and no flag**. Naive pagination sees `[]`, concludes it is done, and silently misses 17,260 matching Runs. Unfiltered paging walks to roughly 27,000.

So the Feed uses server-side filters (cheap, ETag-friendly, capped) and **labels the cap honestly**, as "1,000 of ~18,260". A Purge switches to an unfiltered crawl of about 287 requests, where counts are exact and old Runs are directly reachable.

## Considered Options

**Wave deletion.** Always filter, delete the reachable 1,000 or fewer, re-query so the next 1,000 surfaces, repeat. One code path and naturally resumable, but the UI can never show a true total.

**Always crawl unfiltered.** Exact counts everywhere and no cap semantics to explain, but it pays a full crawl per repository and makes ETag revalidation coarse, because any new Run invalidates the whole list.

**Cap the feature honestly.** Operate only on the newest 1,000 and offer no Purge. Never lies, but abandons the exact use case v1 existed for.

## Consequences

Two code paths through the data layer, justified because the two jobs have opposite needs. The Feed wants cheap revalidation of a small recent window. A Purge wants exhaustive reach.

**Never trust `total_count` in a filtered view.** It reports matches, not reachable matches, and the difference is silent. Any code that paginates a filtered list and stops on an empty page is wrong.

The Link header inverts the help you would want, in both directions, and this too is measured. On an unfiltered crawl it keeps advertising `rel="next"` past the end, so an empty page is the only honest terminal signal. On a filtered list `rel="next"` disappears at the 1,000 cap, presenting the cap as indistinguishable from genuine exhaustion. That is precisely why the cap is silent.

Runs sort newest-first, so a filtered view reaches only the *newest* 1,000 matches, while "delete Runs older than 90 days" asks for the oldest. That asymmetry is the whole reason a Purge cannot reuse the Feed's path.
