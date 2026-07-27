# CLAUDE.md: gh-runs

**A live GitHub Actions dashboard across your repositories, where deletion is one operation.**

v2 is a ground-up Go rewrite. **The 2.0.0 stages are built in Go**: the stage 0 floor (`main.go`, `domain`, `clock`, `config`, `ghclient`) and the `store` and `governor` transport chain, up through the tabs, the panes and the Settings view ([docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) stage 13). The tree compiles. notifications (stage 14) stays deferred to 2.1. The wiring this paragraph tracked is done, and what remains for 2.0.0 is per-Job re-run's `--job-name` half (#148), which waits on a decision rather than on code (#161). The config-to-consumer gaps #73 tracked are closed, R7's pin among them: `config.Pin` reaches `scheduler.SetPinned` from `main.go` and prices against the ceiling. The poll set now shrinks. `discovery` retires a repository after two consecutive definitive probe failures, a 404 or a 403 the governor reports as not rate limiting, deletes the record and its ETag and cadence entries, and the next persist writes a document without it (repo-discovery R23, #145). `Discovery.Membership` is the set the Feed's two prunes read, which is what made absence mean a departure rather than a capability that has not arrived yet (live-run-feed R37). Retirement's only door back is repo-discovery R11's on-demand full refresh, on `u`, because a warm start skips its pass: that is why the two shipped together rather than in sequence. Repo-discovery R22's session adoption and R14's `GH_TOKEN` instruction are reached (#100). `main.go` calls `discovery.AdoptLaunchRepo` behind the scheduler's `Primed()` gate, with the launch repository it already resolved, and the TUI and the CLI both print the instruction, the TUI before it enters the alt screen and the CLI when scope resolution falls back to the fan-out. The instruction travels on `ghclient.ErrRemoteHostUnrecognised` itself, so a caller cannot print the condition without the remedy. `discovery.Discover`, `FastPath` and `FastPathErr` still have no caller outside the package and its tests, because the composition root holds a resolved identity and those three are for a caller that does not. v1 was a bash script and now lives only at the `v1.0.7` tag. If you are looking for `delete-workflow-runs.sh`, it is not missing, it left main deliberately.

## Read these first, in this order

| Doc | What it is |
|---|---|
| [docs/PRD.md](docs/PRD.md) | What we are building, who for, and the measured constraints that shaped it. The constraints table is the most valuable thing in the repo. |
| [docs/CONTEXT.md](docs/CONTEXT.md) | The glossary. It is binding, see below. |
| [docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) | What to build first. **Not** the PRD's feature grouping, which is a taxonomy and points roughly backwards. |
| [docs/adr/](docs/adr/) | Twenty-five decisions and the options they beat. |
| [docs/features/](docs/features/) | Sixteen requirement sets, one per capability. Fifteen are 2.0.0 scope, and notifications is deferred to 2.1 ([ADR-0013](docs/adr/0013-dependency-pins.md)). Numbered `R*` requirements and `AC*` acceptance criteria. |

## The glossary is binding

**Cache** means a GitHub Actions Cache, the thing Reclamation deletes. It never means our on-disk store, which is called **local-store** for exactly this reason. **Purge** is the capability's name, never "bulk-delete". **Run**, **Workflow**, **Job**, **Step**, **Artifact** and **Attempt** all carry their GitHub Actions meanings from [CONTEXT.md](docs/CONTEXT.md) and no other.

Directory names follow the glossary. If a name feels wrong, read CONTEXT.md before renaming it.

## Facts that are settled, and expensive to rediscover

- **Module path is `github.com/jv-k/gh-runs/v2`.** The `/v2` suffix is mandatory at any v2 tag, prereleases included. [ADR-0010](docs/adr/0010-module-path-carries-the-v2-suffix.md).
- **`main.go` lives at the repository root.** Not `cmd/`. This is what makes `go install …/v2@latest` yield a binary called `gh-runs`, and it is what `cli/gh-extension-precompile` builds by default. [ADR-0011](docs/adr/0011-package-layout-and-dependency-direction.md).
- **Everything else is `internal/`.** Nothing here is a library. [ADR-0011](docs/adr/0011-package-layout-and-dependency-direction.md) fixes the tree and the direction every import points.
- **Only the root implements `tea.Model`. A tab exposes `View() string`.** Every `bubbles/v2` component does the same, and `tea.View` carries eleven terminal-wide fields a second tab could only fight over. There are **three tabs** (`feed`, `workflows`, `storage`) and seven **panes** (`rundetail`, `logview`, `approval`, `dispatch`, `settings`, `confirm`, `running`). The three tabs are what a requirement fixes (live-run-feed R2). The pane count is not fixed by anything and grows with the capabilities. A tab may import a pane, never another tab. `settings` and `running` are the root's, because neither belongs to a tab: one is reachable from every tab, and the other must keep painting while a Purge outlives the operator's attention. [ADR-0011](docs/adr/0011-package-layout-and-dependency-direction.md)'s tab contract.
- **Routing is per message class.** Size and data reach every tab, keys reach exactly one. An inactive tab that stops receiving breaks the Feed's background reveal ([live-run-feed](docs/features/live-run-feed/requirements.md) R33) and its ~30s liveness (R27).
- **`store` and `ghclient` must not import each other.** `store` exports an `http.RoundTripper`, `ghclient` takes one, `main.go` is the only place that knows both. This is the load-bearing seam.
- **Every pin in `go.mod` is [ADR-0013](docs/adr/0013-dependency-pins.md), and three of them are load-bearing.** Bubble Tea v2 is `charm.land/bubbletea/v2`, never `github.com/charmbracelet/bubbletea`, which is a live v1 that compiles. Cassettes need **go-vcr v4**: v3's matcher ignores headers, so local-store AC5 passes vacuously. `go get -u` is not a routine chore here.
- **go-gh's cache is TTL-only and never revalidates.** `EnableCache: false` does not disable it. Only `CacheTTL: 0` does. Our RoundTripper does the revalidating.
- **The Budget is a share of the primary limit and never throttles a Purge.** The two limits are different currencies. [ADR-0007](docs/adr/0007-adaptive-delete-throttle.md).
- **Statelessness means nothing written is read back. It never meant nothing is written.** A job record, a resolved ID list and a progress file stay forbidden ([purge](docs/features/purge/requirements.md) R23). The append-only deletion log is required, because a Purge is irreversible and nothing else records what it destroyed ([purge](docs/features/purge/requirements.md) R29, `$XDG_STATE_HOME/gh-runs/deletions.log`). Nothing may read it, and if it cannot be written, the deletion does not happen. [ADR-0006](docs/adr/0006-stateless-bulk-jobs.md) carries the amendment and its decision is unchanged.
- **There is no server-side `conclusion` parameter.** Measured: `?conclusion=failure` returns every Run, because the API ignores it. Never send it.
- **Filtered Run listing silently caps at 1,000** while `total_count` keeps reporting the full match count. Never trust `total_count` in a filtered view. This file names no figure for it deliberately: the counts are point-in-time reference measurements that drift, they live in the [PRD](docs/PRD.md)'s constraints table, and a copy here is one more place for them to disagree. This one already had.
- **`GH_TOKEN` is required for users without gh.** go-gh reaches the keyring only by shelling out to the gh binary. [ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md).
- **Notifications are deferred to 2.1, on correctness not cost.** beeep is cgo-free and cross-compiles, but `osascript` exits 0 whether or not a toast rendered, so [notifications](docs/features/notifications/requirements.md) R13 (report the channel's availability) is unsatisfiable on macOS from a bundle-less precompiled binary. The requirements are preserved as 2.1's starting point, and [settings](docs/features/settings/requirements.md) R11's notification options defer with the feature. [ADR-0013](docs/adr/0013-dependency-pins.md).

## Stack

Go, **Bubble Tea** and **Lipgloss** for the TUI, **go-gh** for the client. gh-dash is the reference implementation for this exact domain. The stack rationale is in the PRD's Stack section, and the client choice is [ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md).

## Testing

Three seams, from the PRD, designed in rather than retrofitted:

| Seam | Where | For |
|---|---|---|
| **Recorded HTTP cassettes** | `store`, `discovery`, `scheduler`, `governor`, `ops` | Replay what the API actually said. Hand-written fakes encode what we believe and stay green while reality moves. |
| **Injected clock** | `scheduler`, `governor` | Timing-dependent, must be deterministic and instant. Never sleep through a real interval. |
| **Golden files** | `tui/*` | A live Feed's correctness is mostly what it puts on screen. |

**Golden `[]byte(m.View().Content)`, at 100 columns, at a named colour profile.** `View()` returns a `tea.View` **struct** in Bubble Tea v2, not a string. The width comes from a fabricated `tea.WindowSizeMsg`, because goldie means there is no `tea.Program` and so no `tea.WithWindowSize`. lipgloss renders truecolour regardless of `TERM` or `NO_COLOR`, so a golden is stable on any machine and a `NO_COLOR` golden over `View().Content` would prove nothing. [ADR-0013](docs/adr/0013-dependency-pins.md) has the pipeline and the measurements.

Test material goes in each package's `testdata/`.

**No test may issue a live DELETE.** This tool irreversibly deletes Runs, Caches and Artifacts at a scale of tens of thousands. The reference measurements were taken against real third-party repositories. Deletion is exercised against cassettes, never against an account.

**Do not deliberately trip a rate limit to test one.** The PRD refuses this for risk R4, on the grounds that it risks blocking the user's account. The same rule applies to tests and to any probe.

## Prose is linted, and it blocks

Markdown here is gated by [deslopper](https://github.com/jv-k/deslopper), pinned in `.deslopper-version`. A `PostToolUse` hook lints every file you write or edit and **blocks on error-tier findings**. CI runs the same pinned version on PRs and on pushes to main.

Error tier, which blocks: **em dashes**, the section sign, middle dots. Warn tier, which annotates: semicolons in prose, filler verbs, word lists, emoji in body text.

Write short declarative sentences. Use a colon, a comma, parentheses, or two sentences where an em dash wants to go. Run it yourself:

```sh
uvx --from "git+https://github.com/jv-k/deslopper@$(cat .deslopper-version)" deslopper lint
```

## When to write an ADR

The ADRs record decisions a reader would otherwise have to reverse-engineer, along with the options they beat. The PRD's Stack section names the three-part test and puts Bubble Tea below the bar deliberately. A decision earns an ADR when reversing it is expensive, when a reasonable person would pick differently, and when the reason is not visible from the code.

Amending an ADR to close a risk is established practice here. R1, R2 and R3 were all resolved in place rather than by superseding. If you resolve one, **update the PRD's risk table in the same commit**, because those two have already drifted once.

## Conventions

- Conventional Commits with a scope: `feat(feed):`, `fix(governor):`, `docs:`, `ci:`, `chore:`.
- The commit body says why, not what. The diff says what.
- Branch from `main`. `main` is the default branch, `master` has never existed.

## Working alongside other agents

This clone is often driven by more than one agent session at once, for example a feature session and a GitHub-issue triage session, all sharing one working tree and one HEAD. `checkout`, `commit`, `reset` and `pull` mutate the whole tree, so one session's branch switch or a broad `git add` can sweep another session's in-flight files into the wrong commit. That has happened, and it put a mislabeled commit on `main`.

Work in your own git worktree. Isolate at the start of substantive work, with `EnterWorktree`, or `git worktree add .claude/worktrees/<task> <branch>` and then enter it. Do every edit, commit and branch switch there, and leave the top-level checkout on `main` as the clean integration point no session commits to directly. Before any git operation, re-read `git rev-parse origin/main`, because HEAD may have moved between commands. Stage explicit paths only, never `git add -A`, `git add .` or `git commit -am`.

## Ground truth beats recall

Every number in the PRD's constraints table was measured, and several contradict what the API's documentation implies. Where a doc here disagrees with the GitHub API, **the API is right and the doc is a bug worth reporting**. Where a doc disagrees with another doc, say so rather than picking one. That has happened, and it is how the Budget contradiction survived as long as it did.

## Agent skills

### Issue tracker

Issues live in GitHub Issues on `jv-k/gh-runs`, operated via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical triage labels are used as-is: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: the binding glossary is `docs/CONTEXT.md` and decisions live in `docs/adr/`. See `docs/agents/domain.md`.
