# Multi-repo Feed via client-side fan-out

The Feed spans every repository with Runs, discovered automatically from the account. **There is no cross-repository Run query anywhere in GitHub's API**, so we fan out one request per repository and merge, sort and filter client-side.

## Considered Options

This is not a preference. It was verified by introspection, and there is no alternative.

**GraphQL.** `Repository` exposes no workflow or actions field. The only near-match, `interactionAbility`, is a false positive. The `WorkflowRun` type exists but is reachable only via `CheckSuite`, and it lacks `status` and `conclusion` entirely, which are the two fields the Feed is built on.

**Search.** The `search` root covers repositories, code, commits, issues, users, topics and labels. Runs are not searchable, in REST or in GraphQL.

**Copying gh-dash.** gh-dash's multi-repo dashboard is cheap because one `search` call returns cross-repo results. That mechanism does not exist for Actions. A "section" here can only be *a set of repositories plus client-side filters*, never a query.

## Consequences

The cost is **latency and complexity, not rate limit**, because [ADR-0004](./0004-conditional-polling-for-liveness.md) makes steady-state polling free. Fan-out needs bounded concurrency, since roughly 26 serial round-trips would take about 5 seconds per refresh, and it needs client-side merge, sort and filter across N lists.

The 1,000-result cap is **per repository**, which makes "all Runs" a fuzzy notion across a Feed. See [ADR-0005](./0005-hybrid-filtered-live-unfiltered-purge.md).

If anyone proposes GraphQL for this again: the introspection above is why it was rejected, and nothing about it has changed.
