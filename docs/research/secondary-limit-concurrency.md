# Secondary rate limits: the concurrent-request cap

Research note. Retrieved 2026-07-19. Documentation reading only, no API calls were made.

## Short answer

GitHub publishes the figure. The official REST rate-limits page states: "No more than 100 concurrent requests are allowed. This limit is shared across the REST API and GraphQL API." The figure is one of six published secondary rate limit dimensions, it applies to REST and GraphQL combined, and GitHub warns that all secondary limits "are subject to change without notice." The docs do not state whether the concurrency cap is counted per user, per app, or per token. Exceeding any secondary limit returns a 403 or 429 with an error message naming the secondary limit, sometimes with a `retry-after` header. Separately, GitHub's best-practices page advises making requests "serially instead of concurrently" regardless of the numeric cap.

## Evidence

### docs.github.com: Rate limits for the REST API

URL: https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api

The "About secondary rate limits" section opens: "In addition to primary rate limits, GitHub enforces secondary rate limits in order to prevent abuse and keep the API available for all users." It adds: "These secondary rate limits are subject to change without notice."

The page lists six ways to trigger a secondary limit. Quoted per dimension:

1. Concurrency: "Make too many concurrent requests. No more than 100 concurrent requests are allowed. This limit is shared across the REST API and GraphQL API."
2. Single endpoint per minute: "Make too many requests to a single endpoint per minute. No more than 900 points per minute are allowed for REST API endpoints, and no more than 2,000 points per minute are allowed for the GraphQL API endpoint." The page does not define what counts as one endpoint (route template versus full path).
3. Requests per minute (points). Point values quoted from the page: "GraphQL requests without mutations: 1", "GraphQL requests with mutations: 5", "Most REST API `GET`, `HEAD`, and `OPTIONS` requests: 1", "Most REST API `POST`, `PATCH`, `PUT`, or `DELETE` requests: 5". The page notes: "For GraphQL requests, these point values are separate from the point value calculations for the primary rate limit."
4. CPU time: "No more than 90 seconds of CPU time per 60 seconds of real time is allowed. No more than 60 seconds of this CPU time may be for the GraphQL API."
5. Content creation: "In general, no more than 80 content-generating requests per minute and no more than 500 content-generating requests per hour are allowed."
6. OAuth token requests: "Make too many OAuth access token requests in a short period of time."

On exceeding a limit: "If you exceed a secondary rate limit, you will receive a `403` or `429` response and an error message that indicates that you exceeded a secondary rate limit." And: "If the `retry-after` response header is present, you should not retry your request until after that many seconds has elapsed."

Scoping: the secondary-limit section never says per user, per app, or per token. The primary-limit sections do attribute limits to users and apps (for example "Requests made on your behalf by a GitHub App that is owned by a GitHub Enterprise Cloud organization have a higher rate limit"). No equivalent sentence exists for the secondary limits, so the unit of the 100-concurrent cap is not published.

### docs.github.com: Best practices for using the REST API

URL: https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api

The full "Avoid concurrent requests" section reads: "To avoid exceeding secondary rate limits, you should make requests serially instead of concurrently. To achieve this, you can implement a queue system for requests." No numeric cap appears on this page.

On mutative requests: "If you are making a large number of `POST`, `PATCH`, `PUT`, or `DELETE` requests, wait at least one second between each request."

On handling a secondary-limit error, in order: honor `retry-after` if present, otherwise "If the `x-ratelimit-remaining` header is `0`, you should not make another request until after the time specified by the `x-ratelimit-reset` header", otherwise "wait for at least one minute before retrying" with exponential backoff on repeated failures. It warns: "Continuing to make requests while you are rate limited may result in the banning of your integration."

### docs.github.com: GraphQL resource limitations

URL: https://docs.github.com/en/graphql/overview/resource-limitations

The GraphQL page repeats the same figure verbatim: "No more than 100 concurrent requests are allowed. This limit is shared across the REST API and GraphQL API." This confirms the cap is one shared pool, not 100 per API. GraphQL primary limits are separate ("The REST API also has a separate primary rate limit"), but the secondary concurrency pool is combined.

### octokit/plugin-throttling (GitHub's own SDK tooling, labelled as such)

URLs: https://github.com/octokit/plugin-throttling.js (README and `src/index.ts`)

This is not documentation of the limit. It is evidence of how GitHub's own Octokit organization operationalizes the guidance. The README states the plugin "Implements all recommended best practices to prevent hitting secondary rate limits", linking to the GitHub docs. Its Bottleneck configuration uses `maxConcurrent: 10` for the global request pool, and `maxConcurrent: 1` for writes with `minTime: 1000` (one second between mutative requests, matching the best-practices page), plus `minTime: 2000` for search and `minTime: 3000` for notifications. GitHub's own client therefore bounds concurrency at 10, far below the published 100 cap. The best-practices docs page itself does not mention Octokit or this plugin.

## Implications for a ~26-repository fan-out

Facts only, the product decision is out of scope here.

- 26 concurrent GET requests sit under the published cap of 100 shared concurrent requests. The headroom is roughly 4x, not unbounded.
- The pool is shared with GraphQL. Any concurrent GraphQL use by the same principal draws from the same 100.
- The cap's counting unit (user, token, app) is not published. If two instances of the tool run under one account, the docs give no basis for assuming separate pools.
- The numeric cap coexists with explicit prose guidance to make requests "serially instead of concurrently". An unbounded fan-out is under the number but against the stated best practice. GitHub's own Octokit throttling plugin bounds reads at 10 concurrent.
- Points: 26 GETs cost 26 points against the 900-points-per-minute figure for REST endpoints. A refresh loop multiplies this (for example, a 10 second cycle spends 156 points per minute). The docs frame the 900 figure as "requests to a single endpoint per minute" without defining endpoint granularity, so whether 26 different repos count as one endpoint is not published.
- Mutative requests are different: a DELETE costs 5 points, best practices ask for at least one second between mutative requests, and content-creation limits (80 per minute, 500 per hour) exist as a separate dimension.
- All secondary limits are "subject to change without notice", so any hard-coded assumption about the 100 figure can silently rot.
- On breach the response is a 403 or 429 with a secondary-limit error message. Recovery order is `retry-after` if present, then `x-ratelimit-reset` when `x-ratelimit-remaining` is 0, then a minimum one minute wait with exponential backoff. Continued traffic while limited risks an integration ban.

## Sources checked

All retrieved 2026-07-19.

- https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api (publishes the 100 figure)
- https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api (serial-requests guidance, error handling, no numeric cap)
- https://docs.github.com/en/graphql/overview/resource-limitations (repeats the 100 figure, confirms the shared pool)
- https://github.com/octokit/plugin-throttling.js README and src/index.ts (GitHub's SDK-org implementation, maxConcurrent 10)
- github.blog changelog search for an announcement of the documented secondary-limit values: no changelog entry stating the 100 figure was found. The docs pages above are the only primary source located for the number.
