# The async model: one channel in, two routes out

[ADR-0014](./0014-domain-types-and-the-budget-readout.md) closed with the `tea.Msg` catalog named as the widest gap between the canon and a buildable TUI. [ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed the routes (size and data reach every tab, keys reach exactly one) without saying what travels them, and [ADR-0012](./0012-transport-chain-and-the-client-surface.md) fixed the transport without saying how a response becomes a repaint. This ADR fills the gap: what messages exist, who emits each one, who consumes it, and where live state lives. It adds no product decisions, and it deliberately does not choose the fan-out's concurrency bound, which remains its own decision.

## The seam: the engine emits domain events, the root adapts them

The scheduler, composing discovery per [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s import table, is an engine. It runs its own goroutines, owns every polling cadence ([polling-scheduler](../features/polling-scheduler/requirements.md) R1), and emits typed events on **one channel**. The events are declared in `scheduler` over `domain` vocabulary, with no Bubble Tea type anywhere in them.

The `tui` root turns that channel into messages with the canonical receive-one-then-reschedule `tea.Cmd`: the command blocks on the channel, returns the received event as a `tea.Msg`, and `Update` re-issues the command. `main.go` constructs the engine, hands the channel to the root, and wires nothing else, exactly as it already mediates between `store` and `ghclient`.

Two alternatives compile and both lose. `main.go` draining the channel into `Program.Send` moves logic into the one file [ADR-0011](./0011-package-layout-and-dependency-direction.md) reserves for wiring, and puts message delivery outside the tea runtime where the golden-file seam cannot reach it. Driving the polling from `tea.Tick` inside the root makes the scheduler a passive library and moves cadence into the view, which [polling-scheduler](../features/polling-scheduler/requirements.md) R1 assigns to the scheduler.

**The seam is what makes the goldens cheap.** A test constructs the root model, feeds it fabricated events, and paints. No `tea.Program`, no live channel, no engine. The engine is tested on its own side with cassettes and the injected clock, and neither side's tests ever touch the other.

## The read catalog: four events, all broadcast

The engine learns four kinds of things, and the channel's catalog mirrors them exactly.

| Event | Payload | Emitted when | Consumed by |
|---|---|---|---|
| `ReposDiscovered` | `[]domain.Repo` | discovery yields a batch, incrementally | every tab. The Feed's capability gate reads the permissions ([live-run-feed](../features/live-run-feed/requirements.md) R17) |
| `WorkflowsFetched` | `RepoID`, `[]domain.Workflow` | a repository's Workflow list lands | `workflows` renders it. The engine has already used it for the name join |
| `RunsFetched` | `RepoID`, `[]domain.Run`, and a filtered listing's `total_count` (see below) | a repository's Run page lands with a 200 | every tab. The Feed's reveal, verbatim |
| `RepoPollFailed` | `RepoID`, error | a repository's poll fails | the Feed, so a failed repository is distinguishable from one that has not answered yet |

**The unit is one repository's response, never a Run and never a cycle.** [live-run-feed](../features/live-run-feed/requirements.md) R33's progressive reveal is per repository, so a per-repository snapshot is the reveal with no translation. Per-Run deltas would make the engine diff old against new, holding state it otherwise never needs, and a whole-cycle barrier would let the slowest repository gate every repaint, which R33 exists to forbid.

**A 304 emits nothing.** [polling-scheduler](../features/polling-scheduler/requirements.md) R19 forbids re-render work on a 304, and the cheapest work is none: nothing crosses the channel, so the TUI never learns the poll happened. The Feed's ~30s liveness (R27) is carried by the engine's cadence, not by heartbeat events.

**Runs arrive stamped.** [ADR-0014](./0014-domain-types-and-the-budget-readout.md) has the fan-out stamp `Repo` and `WorkflowName` where it holds both sides of the join, so every `Run` in a `RunsFetched` event is already whole. No consumer joins anything.

**A filtered listing's event carries the claimed total.** [live-run-feed](../features/live-run-feed/requirements.md) R24 labels a capped view with the reachable count first and the claimed count second ("1,000 of ~18,258"), and the claimed count exists only in the `total_count` of the response the engine consumed. When the active Filter makes a poll a filtered listing ([ADR-0016](./0016-the-filter-representation.md)), `RunsFetched` carries that reported total alongside the page and the Feed derives the label. An unfiltered poll carries none, and no consumer misses it. This is the catalog's one amendment, made when ADR-0016 fixed the filter representation, which is a decision opening the catalog exactly as the Consequences section requires.

## Live state: each tab accumulates its own projection

**The Feed owns the accumulated Runs.** It keeps the map from `RepoID` to that repository's Runs and the interleaved view sorted by `EffectiveStart`, replacing a repository's slice wholesale when its `RunsFetched` arrives. `workflows` keeps the Workflow list, `storage` keeps Caches and Artifacts, and nobody holds state for somebody else.

The broadcast is the synchronisation. Every data event reaches every tab under [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s routing table, so an unfocused Feed keeps accumulating, which is exactly what R33's background reveal and R27's liveness require. There is no canonical model in the root and no shared store wired in `main.go`: the first would need a state package below every tab plus a second broadcast hop, and the second would put locks inside `View()`. The message loop exists so that neither is necessary. If a second consumer of Runs ever appears, the same events already reach it, and the cost is a second projection rather than a redesign.

## The detail pane fetches for itself

[ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed that `feed` hands `rundetail` the selected Run and that the pane owns its five-requirement state machine ([run-detail](../features/run-detail/requirements.md) R10 through R14). This ADR fixes how its requests happen: **pane-owned `tea.Cmd`s over an injected fetch function**, constructed in `main.go`, backed by `ghclient`, and handed down through the root.

The debounce is a `tea.Tick`. A response returns as a pane-private message tagged with the Run ID, and R11's discard rule is one comparison in the pane. The engine never learns the detail pane exists, so deselection is a discarded message rather than a cross-goroutine cancellation. Routing a subscription through the scheduler would split the debounce from the fetch across the exact package boundary [ADR-0011](./0011-package-layout-and-dependency-direction.md) refused to split. Budget safety is unaffected either way: the governor is a RoundTripper under every request ([ADR-0012](./0012-transport-chain-and-the-client-surface.md)), and the pane pauses on the broadcast Readout ([run-detail](../features/run-detail/requirements.md) R16).

## The Readout: the root pulls, and broadcasts on change

[ADR-0014](./0014-domain-types-and-the-budget-readout.md) fixed delivery as a pull: `governor.Readout()`, computed under the governor's lock. The root is the one puller. It calls the getter at two moments: whenever an engine event arrives through the adapter loop, and on a coarse tick of roughly a second, so exhaustion and the reset countdown stay live while the channel is quiet ([live-run-feed](../features/live-run-feed/requirements.md) R30 must not wait for traffic to notice recovery). When the value differs from the last one sent, the root broadcasts the `governor.Readout` struct itself, by value. Four comparable fields make change detection one `==`.

Tabs and panes consume the message and never touch the getter. The alternative, every component pulling for itself, is legal under the import table and wrong in practice: lock acquisitions on every repaint, three tabs seeing three different snapshots in one frame, and every golden needing a live governor where a fabricated `Readout{Exhausted: true}` now suffices.

## Writes: Cmd-initiated, with a progress channel for the long shape

A tab or `confirm` calls `ops` inside a `tea.Cmd`, and the two shapes of write return differently.

**A one-shot write** (cancel, re-run, enable, disable, a single deletion) returns a single completion message. Only the issuer renders the outcome, such as [live-run-feed](../features/live-run-feed/requirements.md) R19's per-Run failure row, and the message type's visibility does the targeting, per the routing rule below.

**A long write** (a Purge above all) cannot be one message at the end, because [purge](../features/purge/requirements.md) paints progress and its summary must record failures for retry (R22). `Execute` returns promptly with a channel of `ops`-typed progress events. The initiating Cmd's first message hands that channel to the root, which adapts it with the same receive-one loop as the engine's channel, and progress is **broadcast**: a Purge outlives the operator's attention, must keep painting its indicator whichever tab is focused, and its summary must land regardless of focus. Riding progress on the engine channel instead would make the scheduler courier for `ops`, coupling the two packages [ADR-0011](./0011-package-layout-and-dependency-direction.md) keeps apart.

`ops` never imports Bubble Tea. It emits its own progress type on a plain channel and does not know a UI exists, which is what keeps its cassette tests headless.

## Message types are the emitters' own

There is no `msgs` package. `tea.Msg` is `any`, so the emitting package's types are the messages: the engine's four events are `scheduler` structs, Purge progress is an `ops` type, and the Readout broadcast is literally `governor.Readout`. Pane-private and tab-private messages (the debounce tick, a tagged Jobs response) are unexported types inside their own package. Tabs and panes may already import `scheduler`, `ops` and `governor` under [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s table, so this adds no edge, and the root translates nothing.

A parallel TUI vocabulary would drift from the emitters' exactly as [ADR-0014](./0014-domain-types-and-the-budget-readout.md) predicted sixteen implementers' structs would, and its only virtue would be insulating tabs from an import the table explicitly permits.

## Routing is two routes, and privacy does the targeting

[ADR-0011](./0011-package-layout-and-dependency-direction.md)'s four-row table collapses, mechanically, to exactly two routes in the root's `Update`:

> **A `tea.KeyPressMsg` goes to the focused tab only. Every other message goes to every tab and pane.**

Focus resolution stays recursive, per [ADR-0011](./0011-package-layout-and-dependency-direction.md): the root picks the tab, a tab forwards to its open pane.

Fine targeting inside the broadcast route is done by **type visibility, not by the router**. A `rundetail` response reaches every tab, and no other package can act on it because no other package can name its unexported type. The root neither exports pane vocabularies nor tracks which component awaits what, and the stale case is already the pane's own discard rule (R11). The root's routing code stays a dozen lines, and grows by zero when a message type is added.

## Considered Options

**`Program.Send` from a `main.go` goroutine.** Delivery from outside the tea runtime, logic in the wiring file, and a golden seam that needs a live program. Rejected above.

**The TUI drives polling via `tea.Tick`.** Cadence moves into the view, against [polling-scheduler](../features/polling-scheduler/requirements.md) R1, and the scheduler's clock seam stops covering the thing it exists to test.

**Per-Run delta events.** The engine grows a diffing memory it otherwise never needs, and the wire grows a delta protocol where a snapshot already carries the whole truth.

**A canonical model in the root, or a shared store.** A new package below every tab plus a second hop for every event, or locks in the render path. Both solve a sharing problem no requirement has.

**Detail fetches as scheduler subscriptions.** Splits R10 from R11 across a package boundary, and turns deselection into cross-goroutine cancellation.

**A central `msgs` package.** A second vocabulary tracking the first, bought at the price of translating every event, to avoid an import the table permits.

**Selective routing of responses by the root.** Requires exporting pane types or root-side bookkeeping of who awaits what, against "each level knows only its own children".

## Consequences

**An exported event type is seam contract.** The engine's four events, `ops`'s progress type and `governor.Readout` are compiled against by their consumers, so changing one is a cross-package contract change, exactly like retyping a `domain` field. Pane-private types can change freely, which is the reward for keeping them unexported.

**The catalog is closed until a decision opens it.** Four read events, one Readout broadcast, one progress stream, per-issuer completions, and the components' private messages. A new broadcast event earns its place the way these did: something the engine learns, or something every tab must not miss.

**The golden seam holds with no live parts.** Every frame the goldens assert is reachable by fabricating messages: a reveal is a `RunsFetched`, exhaustion is a `Readout`, a mid-Purge indicator is a progress event. [live-run-feed](../features/live-run-feed/requirements.md) R36's "held state alone" is satisfied by construction.

**Liveness is the engine's alone.** Nothing in the TUI schedules a poll, retries a failure, or knows a 304 happened. If the Feed goes stale, the defect is in the engine's cadence, not in a repaint path, which is where [polling-scheduler](../features/polling-scheduler/requirements.md)'s clock-driven tests can catch it.

**What this ADR deliberately does not fix.** The fan-out's concurrency bound is its own decision, informed by the verified 100-concurrent cap (polling-scheduler open question 2, resolved). `ops`'s `Plan` and `Confirmed` field lists remain stage 9's, owed before that stage begins. This ADR fixes only how their results travel. Channel buffer sizes and shutdown ordering are implementation detail below decision grade, with one exception the seam already implies: the engine owns closing its channel, and the root treats a closed channel as quit.
