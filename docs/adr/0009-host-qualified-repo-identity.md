# Repo identity is host-qualified, though 2.0.0 ships github.com only

A repository is identified by `host/owner/name` everywhere, in cache keys, config, persisted state and the API base URL, even though 2.0.0 discovers and displays github.com alone and rejects other hosts explicitly.

The asymmetry is the point. Host is **one struct field now** versus **a migration of every persisted key later**. GHES becomes an additive change rather than a refactor.

## Considered Options

**Full multi-host in 2.0.0.** Real value for anyone living in both github.com and a corporate GHES. Rejected because it means building per-host auth, separate rate Budgets and cross-host discovery against exactly one host we can actually test, while GHES API versions lag github.com by months.

**Ignore host entirely.** Simplest model and no dead abstraction. Rejected because it makes GHES a migration rather than a feature, and because of the flag surface below.

## Consequences

[ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md) commits us to gh flag compatibility, and gh's own `-R` takes `[HOST/]OWNER/REPO`. **The syntax arrives whether we support it or not**, so the only choice is between rejecting a host explicitly and misbehaving silently.

A host can also arrive via the `GH_HOST` and `GH_REPO` environment variables, not only via `-R`. All three paths need the same check, or the explicit rejection has a hole in it.

The rejection message must be accurate about *which* host class it is refusing. gh distinguishes github.com, `*.ghe.com` (Enterprise Cloud) and GHES. Telling someone their `tenant.ghe.com` host is "GHES, not supported yet" is wrong, and since this ADR's whole point is explicit rejection over silent misbehaviour, an inaccurate message is a weak form of the thing it set out to avoid.

go-gh authenticates per host, so the plumbing is already host-shaped. Adding GHES later should mean lifting the single-host guard and testing, not rekeying state.
