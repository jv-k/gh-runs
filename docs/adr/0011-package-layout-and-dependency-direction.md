# Package layout, and which way dependencies point

The canon says what to build and never says where the code goes. Sixteen feature documents draw component boundaries as responsibility ("the governor owns pacing, the scheduler consumes Budget state") and never as packages, so sixteen agents would agree on the boundaries and each invent a different tree. This ADR fixes the tree. It adds no product decisions.

## The layout

```
.
├── main.go                  package main. Wiring only, no logic.
├── go.mod                   module github.com/jv-k/gh-runs/v2
└── internal/
    ├── domain/              Run, Workflow, Cache, Artifact, RepoID. No I/O.
    ├── ghclient/            go-gh construction, host qualification, the GH_TOKEN contract.
    ├── store/               local-store. The RoundTripper, ETags, payloads.
    ├── governor/            rate-governor. Paces writes, publishes Budget state.
    ├── discovery/           repo-discovery.
    ├── scheduler/           polling-scheduler.
    ├── ops/                 purge, run-lifecycle, reclamation. Every write.
    ├── notify/              notifications. The three platform backends.
    ├── config/              settings, as a file and as precedence. Not the view.
    ├── cli/                 cli-surface. Flags, and the non-interactive paths.
    └── tui/                 Bubble Tea. Root model and tabs.
        ├── confirm/         The graduated-friction confirmation. Shared, see below.
        ├── feed/  rundetail/  logview/  workflows/  storage/  settings/
```

**`main.go` sits at the repository root**, which is not a style preference. `gh extension create --precompiled=go` scaffolds it there, `cli/gh-extension-precompile` builds from there, and it is what makes `go install github.com/jv-k/gh-runs/v2@latest` produce a binary called `gh-runs` rather than something with a path element bolted on. A `cmd/gh-runs/` tree would cost us the clean install line for nothing. gh-dash, our reference implementation, does the same.

**Everything is `internal/`, and nothing is `pkg/`.** No part of this is a library. `internal/` makes that a compiler error rather than a convention, and it keeps us free to change any seam here without owning somebody's import.

## Dependency direction

One rule, in one direction:

> **`domain` imports nothing of ours. `tui` may import anything. Nothing imports `tui`.**

Expanded, the edges that matter:

| Package | May import | Must never import |
|---|---|---|
| `domain` | nothing internal | anything |
| `ghclient` | `domain` | `store`, `governor` |
| `store` | `domain` | `ghclient`, `scheduler` |
| `governor` | `domain` | `scheduler`, `ops` |
| `discovery` | `domain`, `ghclient`, `store`, `governor` | `scheduler`, `tui` |
| `scheduler` | `domain`, `store`, `governor`, `discovery` | `ops`, `tui` |
| `ops` | `domain`, `ghclient`, `governor` | `scheduler`, `tui` |
| `tui/*` | anything below | another tab's package |

**`store` and `ghclient` do not know about each other, and that is deliberate.** local-store R19 requires our own `http.RoundTripper` passed as `api.ClientOptions.Transport` with go-gh's own cache off. The obvious reading is that `store` imports `ghclient` to install itself, or that `ghclient` imports `store` to fetch one. Both are wrong and both create a cycle the moment the other side needs a type back. `store` exports a `http.RoundTripper`. `ghclient` takes one. **`main.go` is the only place that knows both exist.** That is what a composition root is for, and it is the single most load-bearing wiring decision in the tree.

**`scheduler` reads Budget state and never computes it.** polling-scheduler R14 already says the scheduler must not parse rate-limit headers, keep its own accounting, or throttle writes, and rate-governor R4 says the governor must not schedule reads. The import table makes both enforceable rather than aspirational: `governor` cannot import `scheduler`, so it cannot start reaching into scheduling, and the scheduler's only route to Budget state is the governor's published value.

**The confirmation is one shared component, not three copies.** purge R4 to R9 define the graduated friction. run-lifecycle R17 requires it "unchanged", and storage-reclamation R17 routes every deletion through it. The canon never said whether that is a component, an interface, or a copy, so it would have become a copy. It lives at `internal/tui/confirm`, and `ops` carries the policy (thresholds, eligibility) while `confirm` carries the interaction. Three call sites, one implementation, one golden-file suite.

## Consequences

**Tabs do not import each other.** A tab needing another tab's data takes it from the model above, not sideways. Without this rule `feed` and `rundetail` grow a cycle within a week, because a Run detail pane wants the Feed's row and the Feed wants the detail's selection.

**Test material sits in each package's `testdata/`**, per Go's convention, which the tooling already ignores. The three seams from the PRD land in the obvious places: cassettes under `store`, `discovery`, `scheduler`, `governor` and `ops`; the injected clock in `scheduler` and `governor`; golden files under `tui/*`.

**`config` is not `tui/settings`.** The file, its precedence and its defaults are needed by the governor before any view exists, and settings R2 forbids state from entering the config file. Splitting them keeps a settings view out of the dependency path of everything that merely reads a setting.

**This is a floor and not a cage.** A package here earns its place by being a boundary the canon already draws. Adding one is cheap and needs no ADR. Reversing an arrow in the table above is not, and does.
