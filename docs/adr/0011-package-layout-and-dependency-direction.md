# Package layout, and which way dependencies point

The canon says what to build and never says where the code goes. Sixteen feature documents draw component boundaries as responsibility ("the governor owns pacing, the scheduler consumes the Budget Readout") and never as packages, so sixteen agents would agree on the boundaries and each invent a different tree. This ADR fixes the tree. It adds no product decisions.

## The layout

```
.
├── main.go                  package main. Wiring only, no logic.
├── go.mod                   module github.com/jv-k/gh-runs/v2
└── internal/
    ├── domain/              Run, Workflow, Cache, Artifact, RepoID. No I/O.
    ├── clock/               The injected clock. Imports nothing, see below.
    ├── filter/              The filter engine. Predicates over Runs, see below.
    ├── config/              settings, as a file and as precedence. Not the view.
    ├── ghclient/            go-gh construction, host qualification, the GH_TOKEN contract.
    ├── store/               local-store. The RoundTripper, ETags, payloads.
    ├── governor/            rate-governor. Paces writes, publishes the Budget Readout.
    ├── discovery/           repo-discovery.
    ├── scheduler/           polling-scheduler.
    ├── ops/                 Every write in the product, see below.
    ├── notify/              notifications. The three platform backends.
    ├── cli/                 cli-surface. Flags, and the non-interactive paths.
    └── tui/                 Bubble Tea. Root model and tabs.
        ├── confirm/         The graduated-friction confirmation. Shared, see below.
        ├── feed/  rundetail/  logview/  workflows/  storage/  settings/
```

**[ADR-0012](./0012-transport-chain-and-the-client-surface.md) owns the transport chain.** What `ghclient` exposes, where `store`'s RoundTripper sits, and where `governor` nests inside it are that ADR's decisions, not this one's. This ADR fixes the tree. That one fixes the wiring through it.

**`main.go` sits at the repository root**, which is not a style preference. `gh extension create --precompiled=go` scaffolds it there, `cli/gh-extension-precompile` builds from there, and it is what makes `go install github.com/jv-k/gh-runs/v2@latest` produce a binary called `gh-runs` rather than something with a path element bolted on. A `cmd/gh-runs/` tree would cost us the clean install line for nothing. gh-dash, our reference implementation, does the same.

**Everything is `internal/`, and nothing is `pkg/`.** No part of this is a library. `internal/` makes that a compiler error rather than a convention, and it keeps us free to change any seam here without owning somebody's import.

## Dependency direction

One rule, in one direction:

> **`domain` imports nothing of ours. `tui` may import anything. Nothing imports `tui`.**

Expanded, the edges that matter:

| Package | May import | Must never import |
|---|---|---|
| `domain` | nothing internal | anything |
| `clock` | nothing internal | anything |
| `config` | `domain` | everything but `domain` |
| `filter` | `domain` | everything but `domain` |
| `notify` | `domain` | everything but `domain` |
| `ghclient` | `domain` | `store`, `governor` |
| `store` | `domain`, `clock` | `ghclient`, `governor`, `scheduler` |
| `governor` | `domain`, `clock`, `config` | `store`, `scheduler`, `ops` |
| `discovery` | `domain`, `clock`, `ghclient`, `store`, `governor` | `scheduler`, `tui` |
| `scheduler` | `domain`, `clock`, `config`, `store`, `governor`, `discovery` | `ops`, `tui` |
| `ops` | `domain`, `clock`, `config`, `filter`, `ghclient`, `governor` | `scheduler`, `tui` |
| `cli` | anything except `tui` | `tui` |
| `tui/*` | anything below | another tab's package |

**`clock` imports nothing, and anything may import it.** The injected clock is needed by `store` (local-store R17), `governor` (rate-governor R21), `scheduler` (polling-scheduler R20), `discovery` (repo-discovery R21) and `ops` (purge R27, run-lifecycle R26). `domain` is the only other package that imports nothing, and it is the wrong home: this ADR declares `domain` free of I/O, and reading the wall clock is I/O wearing a very small hat. A package of its own costs one directory and keeps that declaration true.

**`filter` is a package, not a tab's private code.** [cli-surface](../features/cli-surface/requirements.md) says the Feed's "filter engine has to exist regardless. Flags are a thin adapter over it", and names [live-run-feed](../features/live-run-feed/requirements.md) as its owner. That cannot mean `tui/feed` owns it, because `cli` would have to import a tab and nothing imports `tui`. So the engine is `internal/filter`, over `domain` alone, and `cli`, `ops` and `tui/feed` are three consumers of one implementation. It precedes all three.

**`cli` never imports `tui`.** cli-surface R1 gives bare `gh runs` to the TUI, which reads like a dependency and is not one. `main.go` picks the surface: no subcommand opens `tui`, anything else runs `cli`. The composition root already knows both, and that is where the choice belongs.

**`domain.Run` carries the API's field names. The gh-compatible shape is `cli`'s.** The API serves `id`, `display_title` and `workflow_id`. cli-surface R7 must emit `databaseId`, `displayTitle` and `workflowDatabaseId`, because those are gh's `--json` field names and R7 forbids renaming one. Two serialisations, and they get different homes: `domain.Run` decodes what the API sends, and `cli` holds a projection that marshals what gh's users type. The gh vocabulary exists to satisfy one flag on one surface, and letting it into `domain` would put a presentation contract at the root of the tree where every package would inherit it.

**`store` and `ghclient` do not know about each other, and that is deliberate.** local-store R19 requires our own `http.RoundTripper` passed as `api.ClientOptions.Transport` with go-gh's own cache off. The obvious reading is that `store` imports `ghclient` to install itself, or that `ghclient` imports `store` to fetch one. Both are wrong and both create a cycle the moment the other side needs a type back. `store` exports a `http.RoundTripper`. `ghclient` takes one. **`main.go` is the only place that knows both exist.** That is what a composition root is for, and it is the single most load-bearing wiring decision in the tree. [ADR-0012](./0012-transport-chain-and-the-client-surface.md) extends the same argument to `governor`, which nests inside `store`'s transport by the same rule and for the same reason.

**`scheduler` reads the Budget Readout and never computes it.** polling-scheduler R14 already says the scheduler must not parse rate-limit headers, keep its own accounting, or throttle writes, and rate-governor R4 says the governor must not schedule reads. The import table makes both enforceable rather than aspirational: `governor` cannot import `scheduler`, so it cannot start reaching into scheduling, and the scheduler's only route to the Readout is the governor's published value.

**`ops` holds every write, and that list is longer than it first looked.** rate-governor R2 names the writes it paces: Run deletion, log deletion, cancel, force-cancel, re-run, Dispatch, and Cache and Artifact deletion. Three of those (log deletion, Dispatch, Workflow enable and disable) are invoked from `tui/logview` and `tui/workflows`, and `tui` may import anything below it, so a tab could issue them directly and bypass R2 without breaking a single rule in the table above. `ops` therefore owns all of them. A tab renders and calls. It never issues a write of its own.

That is the policy half. The structural half is [ADR-0012](./0012-transport-chain-and-the-client-surface.md): the governor is a RoundTripper under every request the client makes, so a write that skipped `ops` would still be paced. Pacing is enforced by the transport. `ops` exists to enforce the things a transport cannot see, which are confirmation and eligibility.

**The confirmation is one shared component, not four copies.** purge R4 to R9 define the graduated friction. run-lifecycle R17 requires it "unchanged", storage-reclamation R17 routes every deletion through it, and log-viewer R17 routes log deletion through it too. The canon never said whether that is a component, an interface, or a copy, so it would have become a copy. It lives at `internal/tui/confirm`, and `ops` carries the policy (thresholds, eligibility, the frozen set) while `confirm` carries the interaction. **Four call sites, one implementation, one golden-file suite.**

**`ops` must never import `tui`, so the confirmation is a type and not a convention.** purge R9 requires that no path from a selection to a first DELETE skips confirmation. As long as `ops` exposes a plain `Execute`, that is a promise the tabs make, enforced by nobody, which is the exact class of claim this ADR exists to convert into a tree property. So `ops` exposes three calls and not one:

- `Plan(selection) (Plan, error)` freezes the set, resolves eligibility, and computes the friction R7 demands of that blast radius.
- `Confirm(Plan, input) (Confirmed, error)` validates the operator's answer against the plan (`y`, or the exact typed count) and returns a `Confirmed` whose fields are unexported.
- `Execute(Confirmed)` is the only thing that issues a request.

`tui/confirm` renders a `Plan` and collects the input. It cannot construct a `Confirmed`, because outside `ops` there is no way to populate an unexported field, and it cannot reach `Execute` without one. The arrow still runs `tui` to `ops` and never back. R9 stops being a convention in the tabs and becomes a thing the compiler refuses.

## Consequences

**Tabs do not import each other.** A tab needing another tab's data takes it from the model above, not sideways. Without this rule `feed` and `rundetail` grow a cycle within a week, because a Run detail pane wants the Feed's row and the Feed wants the detail's selection.

**Test material sits in each package's `testdata/`**, per Go's convention, which the tooling already ignores. The three seams from the PRD land where the canon actually asks for them:

| Seam | Packages | Required by |
|---|---|---|
| Cassettes | `store`, `discovery`, `scheduler`, `governor`, `ops`, `cli` | local-store R18, repo-discovery R20, polling-scheduler R22, rate-governor R23, purge R26, run-lifecycle R25, cli-surface R19 |
| Injected clock | `store`, `governor`, `scheduler`, `discovery`, `ops` | local-store R17, rate-governor R21, polling-scheduler R20, repo-discovery R21, purge R27, run-lifecycle R26 |
| Golden files | `tui/*` | live-run-feed R36, log-viewer R20, storage-reclamation R25, workflow-dispatch R21, settings R18, run-lifecycle R27 |

`cli` carries cassettes because cli-surface R19 requires every request it issues to pass through an injected transport, and AC5 and AC7 each assert a command issued **zero** requests. Only a transport that counts what passes through it can carry a claim about a request that was never made. The clock reaches `store`, `discovery` and `ops` for the same reason it reaches the other two: each has timing the canon names and requires to be tested without sleeping.

**`config` is not `tui/settings`.** The file, its precedence and its defaults are needed by the governor before any view exists, and settings R2 forbids state from entering the config file. Splitting them keeps a settings view out of the dependency path of everything that merely reads a setting.

**This is a floor and not a cage.** A package here earns its place by being a boundary the canon already draws. Adding one is cheap and needs no ADR. Reversing an arrow in the table above is not, and does.
