# Build order

> The [PRD](./PRD.md) groups the sixteen features as Surfaces, Operations and Infrastructure. That is a taxonomy for reading, and it is close to the reverse of a build order. It lists Surfaces first, and the Feed is the single most dependent thing in the set. This file is the order. Where the two disagree, this one is about building.

## The root is a RoundTripper

**Start at [local-store](./features/local-store/requirements.md) R19.** It is our own `http.RoundTripper`, handed to go-gh as `api.ClientOptions.Transport` with go-gh's cache off (`CacheTTL: 0`). Every conditional request in the product goes through it, which means every free 304 in [ADR-0004](./adr/0004-conditional-polling-for-liveness.md) does too, which means the entire affordability argument for a live dashboard does. [live-run-feed](./features/live-run-feed/requirements.md) says so outright: "That is a build requirement on local-store R19."

**[rate-governor](./features/rate-governor/requirements.md) R1 and R2 are the co-root**, and a RoundTripper too. The governor is the single authority for Budget accounting, and every request the tool issues must be accounted through it. Nothing that makes a request can precede it, so nothing does.

**Both are RoundTrippers, and that is the shape of the floor.** `main.go` nests them, `store.NewTransport(governor.New(http.DefaultTransport))`, so the governor observes real network exchanges and only those, and neither package imports the other ([ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md)). That ADR is stage 0 reading, because it also fixes what `ghclient` may expose: `Request`, never `Get` or `Do`, since those return an error and discard the response, and R5's headers and ADR-0005's `Link` are both unreachable through them.

Those two are the floor. Everything below stands on them.

## The order

| Stage | Build | Because |
|---|---|---|
| **0** | `go.mod`, `main.go`, `domain`, `clock`, `config`, `ghclient` | The skeleton of [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md). `config` lands here rather than with the settings view, because the governor needs a Budget share before any view exists. `clock` is here because five later packages inject it and it imports nothing. |
| **1** | **local-store** | The root. R19's RoundTripper, R19a's injected base, R19b's 304-to-200 reconstitution, ETags, payloads. [ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md). |
| **2** | **rate-governor** | The co-root, and a RoundTripper nested inside stage 1's ([ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md)). Pacing, and the published **Budget Readout**. Its pressure threshold is still open, see below. |
| **3** | **repo-discovery** | Needs local-store's persistence and the governor's accounting. |
| **4** | **polling-scheduler** | Needs discovery's poll set, local-store's ETags, the governor's Budget Readout. |
| **5** | **filter** | The engine both later consumers adapt. Over `domain` alone, so it could sit at stage 0. It sits here because nothing needs it sooner and stage 6 is where it gets used. |
| **6** | **cli-surface**, read half | The first surface that exercises stages 1 to 3, and it needs no terminal. See below. |
| **7** | **live-run-feed** | Needs 1 to 5. The first thing a user sees, and the sixth thing built. |
| **8** | **run-detail** | A pane over the Feed's selection. |
| **9** | **purge** | Owns the frozen set, the graduated confirmation and the failure contract that **three** later features reuse. |
| **10** | **run-lifecycle**, **storage-reclamation**, **log-viewer** | All three reuse purge's confirmation "unchanged". Building any of them first means building it twice. |
| **11** | **workflow-management**, **workflow-dispatch** | Independent of the Feed. Parallelisable with 9 and 10. |
| **12** | **approvals** | A predicate and a badge over the Feed. |
| **13** | **settings**, the view | The view is last. The file was stage 0. |
| **14** | **notifications** | Four upstreams: approvals' predicate, the Feed's transitions, local-store's baseline, dispatch's correlation. Genuinely last. |

**The filter engine is a package, and that is what makes stage 6 possible.** [cli-surface](./features/cli-surface/requirements.md) says the Feed's "filter engine has to exist regardless. Flags are a thin adapter over it", and names [live-run-feed](./features/live-run-feed/requirements.md) as its owner. Read literally, that puts the CLI after the Feed and has `cli` importing `tui/feed`, which [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md) forbids outright: nothing imports `tui`. The engine is `internal/filter` over `domain`, it precedes both consumers, and the ordering above is only coherent because of it.

**log-viewer moved from before purge to after it.** [log-viewer](./features/log-viewer/requirements.md) R17 routes log deletion through purge R4 to R9's graduated confirmation, exactly as run-lifecycle R17 and storage-reclamation R17 do. It was scheduled a stage *ahead* of the thing it depends on, against this file's own stage-10 rationale that "building either first means building it twice". Three features reuse that contract, not two.

## Build the CLI before the TUI

Stage 6 is the one place this order departs from pure dependency. [cli-surface](./features/cli-surface/requirements.md) could come much later and nothing would break. Put it early anyway, because after stage 4 there is a store, a governor, a discovery pass and a scheduler, and **no way to run any of it**. A read-only `list` over the finished stack is a surface with no Bubble Tea in it, which makes the stack exercisable, demonstrable and debuggable before a single golden file exists.

**It does not prove stage 4.** An earlier wording claimed this stage "proves stages 1 to 4 work", and a one-shot `list` does not touch the scheduler at all: it issues its requests and exits, while [polling-scheduler](./features/polling-scheduler/requirements.md) is tiers, intervals and revalidation over time, and nothing in a `list` has a second poll to schedule. What stage 6 exercises is stages 1 to 3, plus the transport chain end to end. The scheduler's first real consumer is the Feed at stage 7, and until then it is proved by its own cassettes and injected clock (R20 to R22) rather than by a surface.

The write flags wait for stage 9. R10's `--dry-run` resolves the affected set "by the same code path as the real operation", so it cannot precede the operation.

## What blocks what

**All three things this section used to list are now resolved**, and it named them long after they were. The ramp is settled ([rate-governor](./features/rate-governor/requirements.md) open questions 4 and 5: AIMD, start 1.0/sec, +0.25 per 20 clean, halve on a rate limit, floor 0.5/sec, cap 2.5/sec). 403 discrimination is settled (that document's open question 1: discriminate on the body's shape, and bound the safe direction at three consecutive classifications). `run_started_at` is settled ([live-run-feed](./features/live-run-feed/requirements.md) open question 3: populated on every Status a sample exists for, and `requested` has no sample anywhere, so R8's fallback costs one line).

**Worse, it quoted a sentence that no longer exists.** "The most consequential gap here… Must be resolved by measurement before any Purge ships" was deleted from rate-governor when its open question 1 resolved, and this file was written afterwards, quoting it. A build order that quotes the canon rather than citing it will say that again. Cite requirement numbers, and let a reader who follows one find out whether it moved.

**One real blocker sits at stage 2, and it was never listed.** [rate-governor](./features/rate-governor/requirements.md) **open question 6 is genuinely open**: the pressure threshold at which the Budget Readout appears. R8 requires the Readout to carry a pressure flag, [live-run-feed](./features/live-run-feed/requirements.md) R29 requires silence when consumption is nominal and a readout under pressure, and AC11 pins the behaviour. No number separates the two. Stage 2 can publish the Readout with the flag stubbed, and everything downstream reads a boolean either way, so this does not hold up the floor. It holds up AC11 and the Feed's R29 golden, which is stage 6.

None of this blocks stage 0.

## Parallelism

Stages 1 and 2 are the co-root and can be built at once by two people who agree on two types first: the **Budget Readout** ([CONTEXT.md](./CONTEXT.md)) and the `base http.RoundTripper` each takes. Neither imports the other, and `main.go` nests them ([ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md)), so the seam between them is two standard-library interfaces and one struct.

Stages 9, 10 and 11 fan out cleanly. Stage 11 shares nothing with 9 or 10. Everything from 7 onward is a tab, and [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md) forbids tabs importing each other, which is what keeps that fan-out honest. Stage 5 is parallelisable with anything, since `filter` imports `domain` alone.

Nothing before stage 6 parallelises usefully beyond that. It is a chain, it is four links long, and it is the whole foundation.
