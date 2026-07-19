# Repo identity is host-qualified, though 2.0.0 ships github.com only

A repository is identified by `host/owner/name` everywhere, in cache keys, config, persisted state and the API base URL, even though 2.0.0 discovers and displays github.com alone and rejects other hosts explicitly.

The asymmetry is the point. Host is **one struct field now** versus **a migration of every persisted key later**. GHES becomes an additive change rather than a refactor.

## Considered Options

**Full multi-host in 2.0.0**, which a host field that nothing yet reads openly invites. Real value for anyone living in both github.com and a corporate GHES. Rejected because it means building per-host auth, separate rate Budgets and cross-host discovery against exactly one host we can actually test, while GHES API versions lag github.com by months.

## Consequences

[ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md) commits us to gh flag compatibility, and gh's own `-R` takes `[HOST/]OWNER/REPO`. **The syntax arrives whether we support it or not**, so the only choice is between rejecting a host explicitly and misbehaving silently.

A host can also arrive via the `GH_HOST` and `GH_REPO` environment variables, not only via `-R`. All three paths need the same check, or the explicit rejection has a hole in it.

The rejection message must be accurate, and gh distinguishes github.com, `*.ghe.com` (Enterprise Cloud with data residency) and GHES. Telling someone their `tenant.ghe.com` host is "GHES, not supported yet" is wrong, and since this ADR's whole point is explicit rejection over silent misbehaviour, an inaccurate message is a weak form of the thing it set out to avoid. **Amended, with [ADR-0022](./0022-the-cli-beyond-gh-precedent.md)'s session: accuracy is had by neutrality, not by taxonomy.** The message names the host and states that 2.0.0 supports github.com only, claiming nothing about what the host is. A message that makes no class claim cannot make a false one, and the alternative, classifying hosts just to phrase an error, would put a copy of gh's host taxonomy in the binary with no other job. [cli-surface](../features/cli-surface/requirements.md) R9 fixes the wording.

go-gh authenticates per host, so the plumbing is already host-shaped. Adding GHES later should mean lifting the single-host guard and testing, not rekeying state.
