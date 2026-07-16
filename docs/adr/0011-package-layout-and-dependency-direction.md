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
    ├── keys/                Both keybinding profiles, as data. Imports nothing, see below.
    ├── filter/              The filter engine. Predicates over Runs, see below.
    ├── config/              settings, as a file and as precedence. Not the view.
    ├── ghclient/            go-gh construction, host qualification, the GH_TOKEN contract.
    ├── store/               local-store. The RoundTripper, ETags, payloads.
    ├── governor/            rate-governor. Paces writes, publishes the Budget Readout.
    ├── discovery/           repo-discovery.
    ├── scheduler/           polling-scheduler.
    ├── ops/                 Every write in the product, see below.
    ├── notify/              notifications, a 2.1 package. Three platform backends, if the feature ships. See below.
    ├── cli/                 cli-surface. Flags, and the non-interactive paths.
    └── tui/                 Bubble Tea. The root model, three tabs, four panes.
        ├── feed/            Tab: Runs.
        ├── workflows/       Tab: Workflows.
        ├── storage/         Tab: Storage.
        ├── rundetail/       Pane, opened by feed over its selection.
        ├── logview/         Pane, opened by rundetail over a Job.
        ├── settings/        Pane, owned by the root. Reachable from every tab.
        └── confirm/         Pane. The graduated-friction confirmation. Shared, see below.
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
| `keys` | nothing internal | anything |
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
| `tui` (root) | anything below, every tab, `settings` | nothing |
| `tui/<tab>` | anything below, a pane | another tab, the root |
| `tui/<pane>` | anything below, a pane | a tab, the root |

**`clock` imports nothing, and anything may import it.** The injected clock is needed by `store` (local-store R17), `governor` (rate-governor R21), `scheduler` (polling-scheduler R20), `discovery` (repo-discovery R21) and `ops` (purge R27, run-lifecycle R26). `domain` is the only other package that imports nothing, and it is the wrong home: this ADR declares `domain` free of I/O, and reading the wall clock is I/O wearing a very small hat. A package of its own costs one directory and keeps that declaration true.

**`keys` is a package for the same reason `clock` is one, and it is a package rather than a tab's private code for the same reason `filter` is.** [live-run-feed](../features/live-run-feed/requirements.md) R7a declares both profiles as one registry, and its AC18 asserts a property over the whole of it ("no binding in either uses Cmd"). That assertion is over a data table and needs no terminal, so filing it under `tui` would put a pure enumeration in the one subtree whose tests exist to paint frames. It imports nothing of ours and everything above it may read it, which is `clock`'s shape exactly. `key.Binding` comes from `charm.land/bubbles/v2/key`, so the package depends on bubbles and on nothing else.

**`filter` is a package, not a tab's private code.** [cli-surface](../features/cli-surface/requirements.md) says the Feed's "filter engine has to exist regardless. Flags are a thin adapter over it", and names [live-run-feed](../features/live-run-feed/requirements.md) as its owner. That cannot mean `tui/feed` owns it, because `cli` would have to import a tab and nothing imports `tui`. So the engine is `internal/filter`, over `domain` alone, and `cli`, `ops` and `tui/feed` are three consumers of one implementation. It precedes all three.

**`cli` never imports `tui`.** cli-surface R1 gives bare `gh runs` to the TUI, which reads like a dependency and is not one. `main.go` picks the surface: no subcommand opens `tui`, anything else runs `cli`. The composition root already knows both, and that is where the choice belongs.

**`domain.Run` carries the API's field names. The gh-compatible shape is `cli`'s.** The API serves `id`, `display_title` and `workflow_id`. cli-surface R7 must emit `databaseId`, `displayTitle` and `workflowDatabaseId`, because those are gh's `--json` field names and R7 forbids renaming one. Two serialisations, and they get different homes: `domain.Run` decodes what the API sends, and `cli` holds a projection that marshals what gh's users type. The gh vocabulary exists to satisfy one flag on one surface, and letting it into `domain` would put a presentation contract at the root of the tree where every package would inherit it.

**`store` and `ghclient` do not know about each other, and that is deliberate.** local-store R19 requires our own `http.RoundTripper` passed as `api.ClientOptions.Transport` with go-gh's own cache off. The obvious reading is that `store` imports `ghclient` to install itself, or that `ghclient` imports `store` to fetch one. Both are wrong and both create a cycle the moment the other side needs a type back. `store` exports a `http.RoundTripper`. `ghclient` takes one. **`main.go` is the only place that knows both exist.** That is what a composition root is for, and it is the single most load-bearing wiring decision in the tree. [ADR-0012](./0012-transport-chain-and-the-client-surface.md) extends the same argument to `governor`, which nests inside `store`'s transport by the same rule and for the same reason.

**`scheduler` reads the Budget Readout and never computes it.** polling-scheduler R14 already says the scheduler must not parse rate-limit headers, keep its own accounting, or throttle writes, and rate-governor R4 says the governor must not schedule reads. The import table makes both enforceable rather than aspirational: `governor` cannot import `scheduler`, so it cannot start reaching into scheduling, and the scheduler's only route to the Readout is the governor's published value.

**`ops` holds every write, and that list is longer than it first looked.** rate-governor R2 names the writes it paces: Run deletion, log deletion, cancel, force-cancel, re-run, Dispatch, Workflow enable and disable, and Cache and Artifact deletion. Four of those (log deletion, Dispatch, Workflow enable and Workflow disable) are invoked from `tui/logview` and `tui/workflows`, and `tui` may import anything below it, so a tab could issue them directly and bypass R2 without breaking a single rule in the table above. `ops` therefore owns all of them. A tab renders and calls. It never issues a write of its own.

**Workflow enable and disable are the two this paragraph found.** It counted them among the writes a tab issues while [rate-governor](../features/rate-governor/requirements.md) R2's list did not carry them, and [purge](../features/purge/requirements.md) R29 had already counted flipping a Workflow's State among the writes R2 paces. Two documents worked from a list the list itself did not have. R2 now names them, at ten writes rather than eight, and the count here is four.

That is the policy half. The structural half is [ADR-0012](./0012-transport-chain-and-the-client-surface.md): the governor is a RoundTripper under every request the client makes, so a write that skipped `ops` would still be paced. Pacing is enforced by the transport. `ops` exists to enforce the things a transport cannot see, which are confirmation and eligibility.

**The confirmation is one shared component, not four copies.** purge R4 to R9 define the graduated friction. run-lifecycle R17 requires it "unchanged", storage-reclamation R17 routes every deletion through it, and log-viewer R17 routes log deletion through it too. The canon never said whether that is a component, an interface, or a copy, so it would have become a copy. It lives at `internal/tui/confirm`, and `ops` carries the policy (thresholds, eligibility, the frozen set) while `confirm` carries the interaction. **Four call sites, one implementation, one golden-file suite.**

**`ops` must never import `tui`, so the confirmation is a type and not a convention.** purge R9 requires that no path from a selection to a first DELETE skips confirmation. As long as `ops` exposes a plain `Execute`, that is a promise the tabs make, enforced by nobody, which is the exact class of claim this ADR exists to convert into a tree property. So `ops` exposes three calls and not one:

- `Plan(selection) (Plan, error)` freezes the set, resolves eligibility, and computes the friction R7 demands of that blast radius.
- `Confirm(Plan, input) (Confirmed, error)` validates the operator's answer against the plan (`y`, or the exact typed count) and returns a `Confirmed` whose fields are unexported.
- `Execute(Confirmed)` is the only thing that issues a request.

`tui/confirm` renders a `Plan` and collects the input. It cannot construct a `Confirmed`, because outside `ops` there is no way to populate an unexported field, and it cannot reach `Execute` without one. The arrow still runs `tui` to `ops` and never back. R9 stops being a convention in the tabs and becomes a thing the compiler refuses.

**`ops` owns the deletion log, and `Execute` is the only thing that writes it.** [purge](../features/purge/requirements.md) R29 requires an append-only record of every deletion under the XDG state directory. It is a write, so `ops` holds it by the rule above, and it needs no new edge in the table: the timestamp comes from `clock`, the identity from `domain`, and the **path arrives from `main.go`**, exactly as `store`'s RoundTripper reaches `ghclient`. That keeps the state directory out of `ops`'s imports and hands R29 a test seam for free, which purge R28 needs, because a replayed Purge has to be pointed at a directory a test can inspect.

The property worth having is that **the only call that issues a DELETE is the only call that writes the line**, and they are the same call. A tab cannot reach one without the other, for the same reason it cannot reach `Execute` without a `Confirmed`. R29's rule that an unwritable log stops the operation is therefore a precondition inside `Execute` rather than a promise four call sites make, which is this ADR's usual move: purge R9 became a tree property, and R29 becomes one on the same lever. The log is not its own package. It is not a boundary the canon draws, it is part of what executing a deletion means, and a package would only give a tab somewhere else to import.

## The tab contract

**Ten of [BUILD-ORDER](../BUILD-ORDER.md)'s fourteen stages are TUI, and this ADR gave them one tree line, one table row and one consequence.** Everything an implementer needs to write the root model was missing, and the gap is not that the answer is hard. It is that **both answers compile**. A tab can be a `tea.Model` or it can be a view, nothing here preferred one, and the two produce different trees. This section fixes the interface, the routing and the hierarchy. It adds no product decisions.

### Only the root implements `tea.Model`

**A tab is not a `tea.Model`. It exposes `Update(tea.Msg) (Tab, tea.Cmd)` and `View() string`.**

The evidence is the component library the [PRD](../PRD.md) already bought the stack for. Every `charm.land/bubbles/v2` component returns a plain string: `viewport`, `list`, `table`, `textinput` and `spinner` each declare `View() string`, and `help` declares `View(KeyMap) string`. Measured at v2.1.1. Not one of them implements `tea.Model`, and a Feed is a list, a viewport and a help stacked together. **The library's idiom is that the thing at the top returns a `tea.View` and everything beneath it returns a string.** A tab is beneath the top.

The alternative loses on arithmetic. `tea.View` at v2.0.8 carries **twelve fields, and eleven of them are terminal-wide**: `Cursor`, `BackgroundColor`, `ForegroundColor`, `WindowTitle`, `ProgressBar`, `AltScreen`, `ReportFocus`, `DisableBracketedPasteMode`, `MouseMode`, `KeyboardEnhancements` and `OnMouse`. Only `Content` composes. A terminal has one title and one cursor, so three tabs returning three `tea.View`s is three claims on eleven singletons, and the root needs a merge policy for every one. There is no merge policy worth writing, because the answer for all eleven is "the focused tab wins", and that is the same as never asking the other two.

**Measured, and the failure is silent.** A root built the other way compiles and runs. The inactive tab's `View()` returned `WindowTitle: "gh-runs: Storage"` and `MouseMode: MouseModeCellMotion`, the root returned the focused tab's `tea.View`, and both fields vanished with no error, no warning, and nothing to fail. A tab that returns a string cannot express the claim, so it cannot have it dropped.

So `tui.Model` is the only `tea.Model` in this tree, and those eleven fields are its alone to set.

### Routing is per message class, and never per tab

**"Broadcast to every tab" and "the focused tab only" are both wrong, and the canon forbids each.** The rule is per class:

| Message class | Reaches | Because |
|---|---|---|
| `tea.WindowSizeMsg` | every tab and pane | A tab must already be laid out when it is focused. One that learns its width on focus paints its first frame wrong, and [live-run-feed](../features/live-run-feed/requirements.md) R4a makes width a correctness property rather than a cosmetic one |
| `tea.KeyPressMsg` | exactly one | Two tabs acting on one keystroke is the bug this row exists to prevent |
| Poll and data messages | every tab | [live-run-feed](../features/live-run-feed/requirements.md) R33 reveals repositories progressively **in the background**, and R27 requires a Run invoked elsewhere to surface within ~30s. Neither holds if the Feed stops receiving whenever the operator is reading the Storage tab |
| The Budget Readout | every tab and pane | R30 there pauses the Feed on exhaustion and [run-detail](../features/run-detail/requirements.md) R16 pauses the detail pane on the same Budget. Neither may miss it for being unfocused, and R30 forbids presenting rows as live once revalidation has stopped |

**In one line: size and data reach everyone, and keys reach exactly one.**

**Focus resolution is recursive, and each level knows only its own children.** The root picks the focused tab and sends the `tea.KeyPressMsg` there. A tab holding an open pane passes it onward. The root does not know `confirm` exists, and `confirm` knows nothing of the root. The one exception is `settings`, below.

### Three tabs, four panes, and the tree listed six of them on one line

**[live-run-feed](../features/live-run-feed/requirements.md) R2 mandates exactly three tabs.** The tree listed six directories on a line labelled tabs, so three of them were miscategorised, and one of those three is contradicted by R2 in the same sentence: "Settings must be reachable from any tab and must not appear as a fourth peer tab."

> **The tabs are `feed`, `workflows` and `storage`. The panes are `rundetail`, `logview`, `settings` and `confirm`.**

**The import rule named tabs and said nothing about panes**, which left the one import an implementer actually reaches for ("may `feed` import `rundetail`?") unaddressed. Stated:

> **A tab MAY import a pane. A tab MUST NOT import another tab. A pane MUST NOT import a tab, and MUST NOT import whatever opened it.**

That last clause is what the Consequences section's cycle warning names. `rundetail` never imports `feed`. It is handed the Run.

**The Runs tab is three directories, and the flat listing hid the hierarchy.** [BUILD-ORDER](../BUILD-ORDER.md) stage 8 calls `rundetail` "a pane over the Feed's selection", and [log-viewer](../features/log-viewer/requirements.md) R1 opens a log "selected from Run detail". So Runs is `feed` opening `rundetail` opening `logview`, three deep, and the tab count is still three. `confirm` is the other shape: [purge](../features/purge/requirements.md) R4 to R9 are reused by four call sites, so it is a pane several tabs import rather than one tab's child.

**`settings` is the root's, and R2 is the reason.** A pane reachable from *any* tab cannot belong to *a* tab, because three tabs importing it is three copies of its state and three places for R17's file write to disagree. The root owns it and opens it over whichever tab is focused. That is what "reachable from any tab, and not a fourth peer tab" means as a tree.

### The Feed holds the selection, and `rundetail` owns the debounce

**The selection is `feed`'s.** [live-run-feed](../features/live-run-feed/requirements.md) R13 keys it by Run ID and it is that tab's cursor. The Consequences rule below sends a tab to the model above for **another tab's** data, and no other tab wants the Feed's selection: `workflows` and `storage` never read it, and [purge](../features/purge/requirements.md) R4's frozen set goes from `feed` straight to `ops.Plan`. Hoisting it to the root would buy nothing and would put a Feed concern one level above the Feed.

**`rundetail` owns the 150ms timer, the in-flight request and the discard rule.** [run-detail](../features/run-detail/requirements.md) R10 debounces on selection settle, R11 discards a response whose Run is no longer selected, R12 shows a pending state instead of the previous Run's Jobs, R13 refreshes at the fast tier while the Run is live, and R14 stops on deselection. **Those five are one state machine**, and every one of them is written as the pane's. `feed` reports where its cursor is. It does not schedule, and it does not know 150ms exists. Splitting the timer from the fetch would split R10 from R11 across a package boundary and leave neither package able to state the contract.

## Consequences

**Tabs do not import each other, and a pane never imports its opener.** A tab needing another tab's data takes it from the model above, not sideways. Without the second clause `feed` and `rundetail` grow a cycle within a week, because a Run detail pane wants the Feed's row and the Feed wants the detail's selection. The tab contract resolves that pair in one direction rather than by mediation: `feed` imports `rundetail` and hands it the Run, and `rundetail` never reaches back.

**Test material sits in each package's `testdata/`**, per Go's convention, which the tooling already ignores. The three seams from the PRD land where the canon actually asks for them:

| Seam | Packages | Required by |
|---|---|---|
| Cassettes | `store`, `discovery`, `scheduler`, `governor`, `ops`, `cli` | local-store R18, repo-discovery R20, polling-scheduler R22, rate-governor R23, purge R26, run-lifecycle R25, cli-surface R19 |
| Injected clock | `store`, `governor`, `scheduler`, `discovery`, `ops` | local-store R17, rate-governor R21, polling-scheduler R20, repo-discovery R21, purge R27, run-lifecycle R26 |
| Golden files | `tui/*` | live-run-feed R36, log-viewer R20, storage-reclamation R25, workflow-dispatch R21, settings R18, run-lifecycle R27 |

`cli` carries cassettes because cli-surface R19 requires every request it issues to pass through an injected transport, and AC5 and AC7 each assert a command issued **zero** requests. Only a transport that counts what passes through it can carry a claim about a request that was never made. The clock reaches `store`, `discovery` and `ops` for the same reason it reaches the other two: each has timing the canon names and requires to be tested without sleeping.

**`config` is not `tui/settings`.** The file, its precedence and its defaults are needed by the governor before any view exists, and settings R2 forbids state from entering the config file. Splitting them keeps a settings view out of the dependency path of everything that merely reads a setting.

**`notify` is a 2.1 package, and the tree keeps its row anyway.** [ADR-0013](./0013-dependency-pins.md) deferred [notifications](../features/notifications/requirements.md) to 2.1 and pins no backend, on measurement rather than on cost, and the product owner ruled for that on 2026-07-16. This row is what the package looks like when 2.1 builds it: over `domain` alone, importing no other internal package, called from wherever a Feed transition is observed. Nothing else in the tree moves either way, which is the point of it importing so little.

**This is a floor and not a cage.** A package here earns its place by being a boundary the canon already draws. Adding one is cheap and needs no ADR. Reversing an arrow in the table above is not, and does.
