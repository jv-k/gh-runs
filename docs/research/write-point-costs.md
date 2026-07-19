# Secondary-limit point costs: every write, not just DELETE

Research note. Retrieved 2026-07-19. Documentation reading only. No API call of any kind was issued, per PRD risk R4 and the no-live-DELETE rule.

## Short answer

The published points model prices by HTTP method, not by endpoint. The table on GitHub's rate-limits page reads, verbatim: "Most REST API `GET`, `HEAD`, and `OPTIONS` requests: 1" point, and "Most REST API `POST`, `PATCH`, `PUT`, or `DELETE` requests: 5" points. Cancel, force-cancel, re-run and Dispatch are POSTs, and Workflow enable and disable are PUTs, so all six carry the same published 5-point default as the four DELETEs. DELETE was never separately documented. The canon's "only DELETE has a documented cost" misread the table's mention of DELETE as endpoint-specific documentation, when DELETE's 5 comes from the same one-row-per-method-class table that prices POST, PATCH and PUT.

The model does not distinguish these endpoints, and it says so itself. Directly under the table: "Some REST API endpoints have a different point cost that is not shared publicly." So no endpoint's exact cost is knowable from documentation, DELETE included. The uncertainty the canon attributed to the six non-DELETE writes is real, but it is symmetric: it covers every request the tool issues, reads included.

## Evidence

### docs.github.com: Rate limits for the REST API

URL: https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api

The "Calculating points for the secondary rate limit" section opens: "Some secondary rate limits are determined by the point values of requests. For GraphQL requests, these point values are separate from the point value calculations for the primary rate limit." The table in full:

| Request | Points |
|---|---|
| GraphQL requests without mutations | 1 |
| GraphQL requests with mutations | 5 |
| Most REST API `GET`, `HEAD`, and `OPTIONS` requests | 1 |
| Most REST API `POST`, `PATCH`, `PUT`, or `DELETE` requests | 5 |

Immediately after the table: "Some REST API endpoints have a different point cost that is not shared publicly."

The pool the points spend against is unchanged: "No more than 900 points per minute are allowed for REST API endpoints, and no more than 2,000 points per minute are allowed for the GraphQL API endpoint." The framing sentence for that figure is "Make too many requests to a single endpoint per minute", and endpoint granularity remains undefined, exactly as docs/research/secondary-limit-concurrency.md recorded.

Two closing caveats on the same page: "These secondary rate limits are subject to change without notice." And one the earlier research note did not capture: "You may also encounter a secondary rate limit for undisclosed reasons."

### The content-creation dimension, quoted in full

Same page, one of the listed ways to trigger a secondary limit: "Create too much content on GitHub in a short amount of time. In general, no more than 80 content-generating requests per minute and no more than 500 content-generating requests per hour are allowed. Some endpoints have lower content creation limits. Content creation limits include actions taken on the GitHub web interface as well as via the REST API and GraphQL API."

The docs never define which requests are content-generating. The arithmetic that makes this worth recording: 80 per minute is well below the ~180 per minute that 5-point writes fit inside 900 points, so for any write this dimension covers, content creation binds before points do. Re-run and Dispatch create Runs, so a conservative reading puts them under it. Deletion removes content rather than creating it, and cancel, enable and disable mutate state without creating anything, but that is our taxonomy, not GitHub's, and the docs offer no list to check it against.

### docs.github.com: the endpoint reference pages

URLs: https://docs.github.com/en/rest/actions/workflow-runs and https://docs.github.com/en/rest/actions/workflows

Neither page mentions rate limits, points, or any request-cost figure for any endpoint. Checked specifically: Cancel a workflow run, Force cancel a workflow run, Re-run a workflow, Re-run failed jobs from a workflow run, Delete a workflow run, Create a workflow dispatch event, Enable a workflow, Disable a workflow. These pages are generated from GitHub's OpenAPI descriptions, so the OpenAPI descriptions carry no per-endpoint cost either. There is no per-endpoint documentation to prefer over the method-level table, in either direction.

### docs.github.com: Best practices for using the REST API

URL: https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api

Unchanged from the earlier retrieval and still live: "If you are making a large number of `POST`, `PATCH`, `PUT`, or `DELETE` requests, wait at least one second between each request." Note the prose advice was always method-shaped too. The 3x disagreement the rate-governor Constraints table records is between two method-level publications, and neither ever singled out DELETE.

## Implications, facts only

- All ten writes rate-governor R2 names carry one published default: 5 points. The four DELETEs (Run, log, Cache, Artifact) and the six others (cancel, force-cancel, re-run, Dispatch, enable, disable) are priced by the same table row.
- No write's cost is exact, DELETE included: "Some REST API endpoints have a different point cost that is not shared publicly." The same sentence covers reads at their 1-point default.
- Nothing here can ever be measured. A point cost is establishable by observation only by tripping the secondary limit, which PRD risk R4 forbids permanently.
- The content-creation dimension (80 per minute, 500 per hour, some endpoints lower, web interface actions included) is a separate published cap with no published request list. If it covers re-run or Dispatch, it binds before the points model does.
- "You may also encounter a secondary rate limit for undisclosed reasons": a clean points ledger is not a guarantee of no 403, which is one more reason backoff reacts to responses rather than trusting arithmetic.

## Sources checked

All retrieved 2026-07-19.

- https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api (the points table, the not-shared-publicly caveat, the content-creation dimension, the undisclosed-reasons sentence)
- https://docs.github.com/en/rest/actions/workflow-runs (no cost figures anywhere on the page)
- https://docs.github.com/en/rest/actions/workflows (no cost figures anywhere on the page)
- https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api (the one-second-between-writes advice, method-shaped)
