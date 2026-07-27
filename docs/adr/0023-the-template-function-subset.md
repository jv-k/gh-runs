# The -t template function subset: gh's pure funcs in-repo, the four that need a color or table library dropped

`gh runs list -t/--template` renders Go text/templates. [cli-surface](../features/cli-surface/requirements.md) R7 pinned it to operate "exactly as they do in gh", which means gh's own template function set: eleven named funcs (`autocolor`, `color`, `hyperlink`, `join`, `pluck`, `tablerender`, `tablerow`, `timeago`, `timefmt`, `truncate`) plus a curated five from sprig (`contains`, `hasPrefix`, `hasSuffix`, `regexMatch`, `replace`). Stage 6 (PR #50) shipped the flag over the standard library's `text/template` with none of those funcs, because the package that carries them cannot be imported here. This ADR settles what the func set is: gh's pure, deterministic funcs, reimplemented in this repository, with four excluded by name.

## Why parity is not free

go-gh's `pkg/template` is where gh's funcs live, and importing it fails this module's build. It pulls classic lipgloss and `charmbracelet/x/cellbuf`, whose ansi version conflicts with the `charm.land` Bubble Tea and Lipgloss v2 fork the TUI pins ([ADR-0013](./0013-dependency-pins.md)). The conflict is load-bearing, not incidental: the v2 pin is the whole TUI stack. So "use gh's package" is off the table, and any func that needs what that package needs is off the table with it.

Two capabilities need it. `color` and `autocolor` render ANSI through a color library, and `tablerow` and `tablerender` build output through go-gh's `tableprinter`, which pulls the same cellbuf line. The other funcs need none of it.

## The subset

Ship gh's pure funcs, reimplemented over the standard library's `text/template` as a `FuncMap`: `join`, `pluck`, `timefmt`, `truncate`, `hyperlink`, `timeago`, and the five sprig string funcs (`contains`, `hasPrefix`, `hasSuffix`, `regexMatch`, `replace`). Reimplementing the five costs less than adding the `sprig` dependency, and it keeps the graph as [ADR-0013](./0013-dependency-pins.md) fixed it. None of these funcs reads the environment, and each is deterministic.

`timeago` matches gh's wording exactly: `just now`, then `N minutes ago`, `N hours ago`, `N days ago`, `N months ago`, `N years ago`, on gh's own bucket boundaries. It runs off the injected clock (`deps.Clock`) rather than gh's Parse-time `time.Now()`, so it is deterministic under a golden. The table's terse `age()` renderer (`5m`, `3h`, `2d`) is a separate function. One clock, two renderers. See the amendment below for where the terse one now lives.

`truncate` matches gh's display-width behaviour, measured with `charm.land/lipgloss/v2`'s `Width` (already pinned, already owned), with gh's `"..."` ellipsis and its `minWidthForEllipsis` of five. Width awareness is the point of the func. A rune-count fallback would mis-truncate the CJK and emoji run titles this tool lists against real repositories.

`-t` output stays raw, unsanitised, matching gh and matching `-q` and `--json`. The human table is the one path that strips control bytes, because its content is untrusted and its format is not the operator's. A `-t` template is the operator's own. `hyperlink` therefore emits its OSC 8 escape unconditionally, as gh's does.

## The four dropped funcs error by name

`color`, `autocolor`, `tablerow` and `tablerender` are registered as stubs that accept any arguments and return a clear error naming the func and pointing at this deviation. A template that uses one parses, matching gh's parse, and fails at execution with a message an operator can act on, rather than the standard library's bare `function "color" not defined`. A dropped func inside an untaken conditional branch never fires, so a template that only conditionally colours still runs.

## Considered Options

**Accept the gap: ship `-t` with no funcs.** What stage 6 left in place. Rejected because it is a trap: a template ported from gh parses, then errors on the first `{{timeago .createdAt}}`, which reads as a bug in this tool rather than a scope line. `-q` already covers the byte-identical scripting path (AC6), so `-t`'s value is the formatted path, and a formatted path with no formatting funcs is the weakest of the three surfaces.

**Full parity via dependency surgery.** Fork or split go-gh's `pkg/template` off its classic-lipgloss import, or vendor it. Rejected because it reintroduces exactly what [ADR-0013](./0013-dependency-pins.md) pins away. `color` still needs a color library to render ANSI, and `tablerender` still needs a cellbuf-backed printer, so the conflict returns through the funcs that motivated the surgery. It also commits the project to maintaining that split on someone else's release cadence, for two capabilities.

**Full parity, funcs reimplemented in-repo including `color` and the table pair.** `color` is reimplementable without a library, as a color-name to ANSI map that dodges lipgloss, if a TTY check is accepted. Rejected for 2.0.0 because the TTY read fights this repo's "no environment at render time" discipline, the table pair is stateful and awkward over a one-shot list, and the pair is the least-reached corner of gh's `-t` surface. The color-name map can be added later behind the same `FuncMap` without moving this decision.

**Reimplement the pure funcs, drop the four.** Chosen. It is the subset that makes `-t` genuinely useful for shaping output, at no new dependency and no environment read, with the two capabilities that cost a library dropped by name behind a clear error.

## Consequences

**cli-surface R7's `-t` clause is amended in this ADR's commit**, and a new acceptance criterion (AC21) pins the subset: a pure func matches gh, a dropped func errors by name. R7's `-q` clause is unchanged, because `-q` is genuinely byte-identical to gh through go-gh's own jq.

**The funcs live in `cli`, beside the projection they render over** ([ADR-0011](./0011-package-layout-and-dependency-direction.md)). Nothing else in the tree renders a template, so the `FuncMap` has one home and one caller, `renderTemplate`.

**The color and table capabilities are a later decision, not a debt.** Adding a library-free `color` behind a TTY check, or the table pair if a cellbuf-free printer ever exists, extends the same `FuncMap`. This ADR records why they are out, so a future addition starts from a recorded decision rather than a rediscovery.

## Amendment: the terse `age()` renderer moved out of `cli`, and it now has two consumers

The section above says the table's terse `age()` renderer "is a separate function and stays as it is". It is still a separate function, and it did not stay where it was. a1f4be0 moved it out of `internal/cli` into `internal/timefmt` and gave it a second consumer, so the sentence needed correcting rather than defending.

[settings](../features/settings/requirements.md) R10 added a timestamp format to the Feed, and its relative member is the same rendering the table's age column already served: `just now`, then `5m`, `3h`, `2d`, `4mo`, `1y`, each bucket truncating. The table and the Feed both paint it. `cli` may never import `tui`, and nothing may import a tab ([ADR-0011](./0011-package-layout-and-dependency-direction.md)), so a shared renderer has nowhere to live but a package of its own. `internal/timefmt` is that package, it imports nothing of ours, and it takes `now` as an argument for the reason `timeago` takes the injected clock.

**"One clock, two renderers" survives, and it is what the move preserves.** The two renderings are `timeago`'s prose (`3 hours ago`) and `timefmt.Age`'s terse column (`3h`). They answer the same question at different densities and this ADR keeps them apart on purpose, because `-t` output must match gh's wording exactly and a table column must fit. What changed is that the terse one is no longer `cli`'s private function. Its buckets are now asserted once, against an injected instant, rather than through whichever surface happened to call it.

**The consequence above still holds for the funcs and no longer for `age()`.** The `FuncMap` and `timeago` live in `cli` beside the projection they render over, and that is unchanged: nothing else in the tree renders a template. `age()` was never a template func, so moving it moves nothing about that.
