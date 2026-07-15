# A full CLI surface, mirroring gh's flags, despite the overlap

`gh` already covers listing and one-shot operations, and the naive bulk-delete pipeline already works:

```sh
gh run list --status failure -L 100 --json databaseId -q '.[].databaseId' | xargs -n1 gh run delete
```

We ship a full non-interactive CLI anyway, as a **drop-in superset of `gh run`'s flags**, with identical names and semantics (`-b/--branch`, `-s/--status`, `-w/--workflow`, `-u/--user`, `-e/--event`, `--created`, `-c/--commit`, `-L/--limit`, `--json`, `-q/--jq`, `-t/--template`, `-R/--repo`), plus what gh lacks.

Two arguments carry this, and neither is "the pipeline is inadequate":

1. **Standalone coherence.** [ADR-0002](./0002-go-gh-with-dual-distribution.md) ships a standalone binary. Telling a Homebrew user to "use `gh run` for one-shots" is not available to someone who does not have gh. The binary must be self-sufficient, or the channel is a lie.
2. **The filter engine exists anyway.** The Feed must parse and apply branch, status, workflow, actor, event and date filters regardless. Flags are a thin adapter over domain logic that has to exist, not a parallel product.

## Considered Options

**TUI only.** Smallest surface. Point people at `gh run` and `gh api`, and say so in the README rather than competing badly. Rejected on argument 1.

**Own syntax.** Cleanest internal model, but it creates a genuinely second syntax to learn, at which point the overlap objection stands.

## Consequences

Compatibility is a **stated requirement, not an accident**. `gh run list` to `gh runs list` must be muscle memory. Superset, never divergence.

**Correction: `--conclusion` was proposed as the flagship example of that principle, and it does not survive measurement.** During design it was argued that gh's `-s/--status` "conflates" Status and Conclusion, and that we would keep that permissive behaviour for compatibility while adding a precise `--conclusion` flag gh lacks. Measured against the live API:

```text
?conclusion=failure     -> total_count=28710  conclusions=["success"]
?conclusion=bogusvalue  -> total_count=28710  conclusions=["success"]
(no filter)             -> total_count=28710
```

There is **no server-side `conclusion` filter**. The API's `status` param is the single param that matches *either* field, which is why `?status=success` genuinely narrows to 18,260. gh is faithfully mirroring the API, not conflating anything of its own. A `--conclusion` flag could only be client-side post-filtering over the reachable 1,000 ([ADR-0005](./0005-hybrid-filtered-live-unfiltered-purge.md)), filtering *what was reachable* rather than *what matches*, which is the exact dishonesty the PRD forbids. It is therefore an open question owned by `cli-surface`, not a settled requirement, and viable only on unfiltered-crawl paths where we hold the complete set.

**The API silently ignores unknown query parameters.** `conclusion=bogusvalue` returned all 28,710 Runs and no error. For a destructive non-interactive command this is a safety hazard rather than an ergonomic one: `--status faliure` would silently target *everything*. Filter values must be validated client-side, because the API will not.

Note what v1 was, so nobody re-derives a false premise. v1 piped into `fzf --multi` and was **interactive-only**. It could never be scripted. There is no scripted install base this preserves, and that argument was made during design and was wrong.

What the CLI genuinely adds over the pipeline is narrow and real: throttling ([ADR-0007](./0007-adaptive-delete-throttle.md)), retry, looping past the silent 1,000 cap, and skipping in-progress Runs, which DELETE rejects.
