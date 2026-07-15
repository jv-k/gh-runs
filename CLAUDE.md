# CLAUDE.md: gh-runs

**A live GitHub Actions dashboard across your repositories, where deletion is one operation.**

v2 is a ground-up Go rewrite. **There is no Go code yet.** v1 was a bash script and now lives only at the `v1.0.7` tag. If you are looking for `delete-workflow-runs.sh`, it is not missing, it left main deliberately.

## Read these first, in this order

| Doc | What it is |
|---|---|
| [docs/PRD.md](docs/PRD.md) | What we are building, who for, and the measured constraints that shaped it. The constraints table is the most valuable thing in the repo. |
| [docs/CONTEXT.md](docs/CONTEXT.md) | The glossary. It is binding, see below. |
| [docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) | What to build first. **Not** the PRD's feature grouping, which is a taxonomy and points roughly backwards. |
| [docs/adr/](docs/adr/) | Thirteen decisions and the options they beat. |
| [docs/features/](docs/features/) | Sixteen requirement sets, one per capability. Numbered `R*` requirements and `AC*` acceptance criteria. |

## The glossary is binding

**Cache** means a GitHub Actions Cache, the thing Reclamation deletes. It never means our on-disk store, which is called **local-store** for exactly this reason. **Purge** is the capability's name, never "bulk-delete". **Run**, **Workflow**, **Job**, **Step**, **Artifact** and **Attempt** all carry their GitHub Actions meanings from [CONTEXT.md](docs/CONTEXT.md) and no other.

Directory names follow the glossary. If a name feels wrong, read CONTEXT.md before renaming it.

## Facts that are settled, and expensive to rediscover

- **Module path is `github.com/jv-k/gh-runs/v2`.** The `/v2` suffix is mandatory at any v2 tag, prereleases included. [ADR-0010](docs/adr/0010-module-path-carries-the-v2-suffix.md).
- **`main.go` lives at the repository root.** Not `cmd/`. This is what makes `go install …/v2@latest` yield a binary called `gh-runs`, and it is what `cli/gh-extension-precompile` builds by default. [ADR-0011](docs/adr/0011-package-layout-and-dependency-direction.md).
- **Everything else is `internal/`.** Nothing here is a library. [ADR-0011](docs/adr/0011-package-layout-and-dependency-direction.md) fixes the tree and the direction every import points.
- **`store` and `ghclient` must not import each other.** `store` exports an `http.RoundTripper`, `ghclient` takes one, `main.go` is the only place that knows both. This is the load-bearing seam.
- **Every pin in `go.mod` is [ADR-0013](docs/adr/0013-dependency-pins.md), and three of them are load-bearing.** Bubble Tea v2 is `charm.land/bubbletea/v2`, never `github.com/charmbracelet/bubbletea`, which is a live v1 that compiles. Cassettes need **go-vcr v4**: v3's matcher ignores headers, so local-store AC5 passes vacuously. `go get -u` is not a routine chore here.
- **go-gh's cache is TTL-only and never revalidates.** `EnableCache: false` does not disable it. Only `CacheTTL: 0` does. Our RoundTripper does the revalidating.
- **The Budget is a share of the primary limit and never throttles a Purge.** The two limits are different currencies. [ADR-0007](docs/adr/0007-adaptive-delete-throttle.md).
- **There is no server-side `conclusion` parameter.** Measured: `?conclusion=failure` returns every Run, because the API ignores it. Never send it.
- **Filtered Run listing silently caps at 1,000** while `total_count` still claims 18,260. Never trust `total_count` in a filtered view.
- **`GH_TOKEN` is required for users without gh.** go-gh reaches the keyring only by shelling out to the gh binary. [ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md).

## Stack

Go, **Bubble Tea** and **Lipgloss** for the TUI, **go-gh** for the client. gh-dash is the reference implementation for this exact domain. The stack rationale is in the PRD's Stack section, and the client choice is [ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md).

## Testing

Three seams, from the PRD, designed in rather than retrofitted:

| Seam | Where | For |
|---|---|---|
| **Recorded HTTP cassettes** | `store`, `discovery`, `scheduler`, `governor`, `ops` | Replay what the API actually said. Hand-written fakes encode what we believe and stay green while reality moves. |
| **Injected clock** | `scheduler`, `governor` | Timing-dependent, must be deterministic and instant. Never sleep through a real interval. |
| **Golden files** | `tui/*` | A live Feed's correctness is mostly what it puts on screen. |

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

## Ground truth beats recall

Every number in the PRD's constraints table was measured, and several contradict what the API's documentation implies. Where a doc here disagrees with the GitHub API, **the API is right and the doc is a bug worth reporting**. Where a doc disagrees with another doc, say so rather than picking one. That has happened, and it is how the Budget contradiction survived as long as it did.
