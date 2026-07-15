# Build order

> The [PRD](./PRD.md) groups the sixteen features as Surfaces, Operations and Infrastructure. That is a taxonomy for reading, and it is close to the reverse of a build order. It lists Surfaces first, and the Feed is the single most dependent thing in the set. This file is the order. Where the two disagree, this one is about building.

## The root is a RoundTripper

**Start at [local-store](./features/local-store/requirements.md) R19.** It is our own `http.RoundTripper`, handed to go-gh as `api.ClientOptions.Transport` with go-gh's cache off (`CacheTTL: 0`). Every conditional request in the product goes through it, which means every free 304 in [ADR-0004](./adr/0004-conditional-polling-for-liveness.md) does too, which means the entire affordability argument for a live dashboard does. [live-run-feed](./features/live-run-feed/requirements.md) says so outright: "That is a build requirement on local-store R19."

**[rate-governor](./features/rate-governor/requirements.md) R1 and R2 are the co-root.** The governor is the single authority for Budget accounting, and every request the tool issues must be accounted through it. Nothing that makes a request can precede it, so nothing does.

Those two are the floor. Everything below stands on them.

## The order

| Stage | Build | Because |
|---|---|---|
| **0** | `go.mod`, `main.go`, `domain`, `ghclient`, `config` | The skeleton of [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md). `config` lands here rather than with the settings view, because the governor needs a Budget share before any view exists. |
| **1** | **local-store** | The root. R19's RoundTripper, ETags, payloads. |
| **2** | **rate-governor** | The co-root. Pacing, and published Budget state. Its ramp is still undefined, see below. |
| **3** | **repo-discovery** | Needs local-store's persistence and the governor's accounting. |
| **4** | **polling-scheduler** | Needs discovery's poll set, local-store's ETags, the governor's Budget state. |
| **5** | **cli-surface**, read half | The first surface that proves stages 1 to 4 work, and it needs no terminal. See below. |
| **6** | **live-run-feed** | Needs all four of 1 to 4. The first thing a user sees, and the fifth thing built. |
| **7** | **run-detail**, **log-viewer** | Panes over the Feed's selection. |
| **8** | **purge** | Owns the graduated confirmation and the failure contract that two later features reuse. |
| **9** | **run-lifecycle**, **storage-reclamation** | Both reuse purge's confirmation "unchanged". Building either first means building it twice. |
| **10** | **workflow-management**, **workflow-dispatch** | Independent of the Feed. Parallelisable with 8 and 9. |
| **11** | **approvals** | A predicate and a badge over the Feed. |
| **12** | **settings**, the view | The view is last. The file was stage 0. |
| **13** | **notifications** | Four upstreams: approvals' predicate, the Feed's transitions, local-store's baseline, dispatch's correlation. Genuinely last. |

## Build the CLI before the TUI

Stage 5 is the one place this order departs from pure dependency. [cli-surface](./features/cli-surface/requirements.md) could come much later and nothing would break. Put it early anyway, because after stage 4 there is a store, a governor, a discovery pass and a scheduler, and **no way to run any of it**. A read-only `list` over the finished stack is a surface with no Bubble Tea in it, which makes stages 1 to 4 exercisable, demonstrable and debuggable before a single golden file exists.

The write flags wait for stage 8. R10's `--dry-run` resolves the affected set "by the same code path as the real operation", so it cannot precede the operation.

## What blocks what

Three things are unresolved and sit directly in this path. None of them blocks stage 0.

- **The governor's ramp is undefined** and it is stage 2. The canon fixes the endpoints (start at 1/sec, ramp toward the ceiling, back off hard) and nothing between them. Stage 2 can be built against any ramp that respects the endpoints, and the shape settled before stage 8 turns it on against real deletes.
- **403 discrimination is unmeasured** and [rate-governor](./features/rate-governor/requirements.md) calls it "the most consequential gap here… Must be resolved by measurement before any Purge ships." That is stage 8, not stage 2. It does not hold up the floor.
- **`run_started_at` on an unstarted Run is unknown** and [live-run-feed](./features/live-run-feed/requirements.md) R8's sort spine depends on it. That is stage 6. Measure it before then, and note the answer is free: it needs one API read, no writes, and no risk.

## Parallelism

Stages 1 and 2 are the co-root and can be built at once by two people who agree on the Budget state type first. Stages 8, 9 and 10 fan out cleanly. Stage 10 shares nothing with 8 or 9. Everything from 6 onward is a tab, and [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md) forbids tabs importing each other, which is what keeps that fan-out honest.

Nothing before stage 5 parallelises usefully. It is a chain, it is four links long, and it is the whole foundation.
