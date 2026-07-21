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
| **0** | `go.mod`, `main.go`, `domain`, `clock`, `config`, `ghclient` | The skeleton of [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md). **Every line of `go.mod` is [ADR-0013](./adr/0013-dependency-pins.md)**, including the Go floor that CI and the released binaries both read from it. **`domain`'s structs and enums are [ADR-0014](./adr/0014-domain-types-and-the-budget-readout.md)**, measured against the live API. `config` lands here rather than with the settings view, because the governor needs a Budget share before any view exists. `clock` is here because five later packages inject it and it imports nothing. |
| **1** | **local-store** | The root. R19's RoundTripper, R19a's injected base, R19b's 304-to-200 reconstitution, ETags, payloads. [ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md). |
| **2** | **rate-governor** | The co-root, and a RoundTripper nested inside stage 1's ([ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md)). Pacing, and the published **Budget Readout**, whose pressure flag is R8a's projection and needs no stub. |
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
| **14 (2.1)** | **notifications**, deferred | Out of 2.0.0 scope, deferred to 2.1 ([ADR-0013](./adr/0013-dependency-pins.md), [PRD](./PRD.md) Scope). Four upstreams when 2.1 builds it: approvals' predicate, the Feed's transitions, local-store's baseline, and dispatch's returned Run ID (correlation is superseded, #27). Last then, and last now. |

**Stages 0 to 13 are 2.0.0. Stage 14 is 2.1.** notifications defers to 2.1 because delivery cannot be confirmed on macOS from a precompiled binary ([ADR-0013](./adr/0013-dependency-pins.md)): `osascript` exits 0 whether or not a toast rendered. [settings](./features/settings/requirements.md) R11's notification options defer with it, so the last thing 2.0.0 builds is stage 13, the Settings view. When 2.1 builds notifications it is still last, standing on the same four upstreams.

**The filter engine is a package, and that is what makes stage 6 possible.** [cli-surface](./features/cli-surface/requirements.md) says the Feed's "filter engine has to exist regardless. Flags are a thin adapter over it", and names [live-run-feed](./features/live-run-feed/requirements.md) as its owner. Read literally, that puts the CLI after the Feed and has `cli` importing `tui/feed`, which [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md) forbids outright: nothing imports `tui`. The engine is `internal/filter` over `domain`, it precedes both consumers, and the ordering above is only coherent because of it.

**log-viewer moved from before purge to after it.** [log-viewer](./features/log-viewer/requirements.md) R17 routes log deletion through purge R4 to R9's graduated confirmation, exactly as run-lifecycle R17 and storage-reclamation R17 do. It was scheduled a stage *ahead* of the thing it depends on, against this file's own stage-10 rationale that "building either first means building it twice". Three features reuse that contract, not two.

## Build the CLI before the TUI

Stage 6 is the one place this order departs from pure dependency. [cli-surface](./features/cli-surface/requirements.md) could come much later and nothing would break. Put it early anyway, because after stage 4 there is a store, a governor, a discovery pass and a scheduler, and **no way to run any of it**. A read-only `list` over the finished stack is a surface with no Bubble Tea in it, which makes the stack exercisable, demonstrable and debuggable before a single golden file exists.

**It does not prove stage 4.** An earlier wording claimed this stage "proves stages 1 to 4 work", and a one-shot `list` does not touch the scheduler at all: it issues its requests and exits, while [polling-scheduler](./features/polling-scheduler/requirements.md) is tiers, intervals and revalidation over time, and nothing in a `list` has a second poll to schedule. What stage 6 exercises is stages 1 to 3, plus the transport chain end to end. The scheduler's first real consumer is the Feed at stage 7, and until then it is proved by its own cassettes and injected clock (R20 to R22) rather than by a surface.

The write flags wait for stage 9. R10's `--dry-run` resolves the affected set "by the same code path as the real operation", so it cannot precede the operation.

## What blocks what

**All three things this section used to list are now resolved**, and it named them long after they were. The ramp is settled ([rate-governor](./features/rate-governor/requirements.md) open questions 4 and 5: AIMD, start 1.0/sec, +0.25 per 20 clean, halve on a rate limit, floor 0.5/sec, cap 2.5/sec). 403 discrimination is settled (that document's open question 1: discriminate on the body's shape, and bound the safe direction at three consecutive classifications). `run_started_at` is settled ([live-run-feed](./features/live-run-feed/requirements.md) open question 3: populated on every Status a sample exists for, and `requested` has no sample anywhere, so R8's fallback costs one line).

**Worse, it quoted a sentence that no longer exists.** "The most consequential gap here… Must be resolved by measurement before any Purge ships" was deleted from rate-governor when its open question 1 resolved, and this file was written afterwards, quoting it. A build order that quotes the canon rather than citing it will say that again. Cite requirement numbers, and let a reader who follows one find out whether it moved.

**The one real blocker sat at stage 2, and it is resolved.** [rate-governor](./features/rate-governor/requirements.md) open question 6 asked what pressure threshold makes the Budget Readout appear. There is no threshold. **R8a** answers it with a projection: consumption is under pressure when the current burn rate would exhaust the remaining allowance before it resets, `remaining / burn < time_to_reset`, over a five-minute window. Stage 2 builds the flag rather than stubbing it, because the predicate needs only R5's headers and the injected clock, both of which stage 2 already holds. AC11 and AC11b are checkable there, against a cassette.

**That paragraph also put the Feed's R29 golden at stage 6, and stage 6 is the CLI.** The Feed is stage 7 and has no golden before it exists. Nothing downstream of stage 2 now waits on a number.

**What stays open touches stage 4 and does not block it.** [polling-scheduler](./features/polling-scheduler/requirements.md) open question 4 asks when each tier demotes. R8a fixes the onset, so R15's first demotion has a moment, and the staging between its three tiers does not. The scheduler builds and its cassettes run either way.

None of this blocks stage 0.

## Parallelism

Stages 1 and 2 are the co-root and can be built at once by two people who agree on two types first: the **Budget Readout** ([CONTEXT.md](./CONTEXT.md), its struct fixed by [ADR-0014](./adr/0014-domain-types-and-the-budget-readout.md)) and the `base http.RoundTripper` each takes. Neither imports the other, and `main.go` nests them ([ADR-0012](./adr/0012-transport-chain-and-the-client-surface.md)), so the seam between them is two standard-library interfaces and one struct.

Stages 9, 10 and 11 fan out cleanly. Stage 11 shares nothing with 9 or 10. Stage 5 is parallelisable with anything, since `filter` imports `domain` alone.

**What keeps that fan-out honest is [ADR-0011](./adr/0011-package-layout-and-dependency-direction.md)'s tab contract, and this paragraph used to cite the wrong half of it.** It said "everything from 7 onward is a tab", which is false and was the flat tree line talking: only three of these stages build a tab (7 is Runs, 10's storage-reclamation is Storage, 11 is Workflows). Stages 8, 10's log-viewer and 13 build **panes**, and a pane is exactly the thing the old rule was silent about. The contract that makes the fan-out safe is the whole of it: a tab may import a pane, a tab may not import another tab, and a pane may not import a tab or whatever opened it. So `storage` and `workflows` cannot reach into `feed`, and `logview` cannot reach back up to `rundetail`.

**Stages 7, 8 and 10's log-viewer are a chain rather than a fan-out**, and the table already implies it without saying so. Runs is `feed` opening `rundetail` opening `logview`, three packages in one tab, which is why stage 8 follows 7 and why [log-viewer](./features/log-viewer/requirements.md) R1 can only open "from Run detail".

**`internal/keys` lands at stage 7**, with the Feed that requires it ([live-run-feed](./features/live-run-feed/requirements.md) R7a). It imports nothing of ours, so it could sit at stage 0 beside `clock`, and it does not because nothing before stage 7 has a key to press. Every stage from 7 on draws its bindings from it, and its AC18 test needs no terminal.

Nothing before stage 6 parallelises usefully beyond that. It is a chain, it is four links long, and it is the whole foundation.
