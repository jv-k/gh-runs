# A full CLI surface, mirroring gh's flags, despite the overlap

`gh` already covers listing and one-shot operations, and the naive bulk-delete pipeline already works:

```sh
gh run list --status failure -L 100 --json databaseId -q '.[].databaseId' | xargs -n1 gh run delete
```

We ship a full non-interactive CLI anyway, as a **drop-in superset of `gh run`'s flags**, with identical names and semantics (`-b/--branch`, `-s/--status`, `-w/--workflow`, `-u/--user`, `-e/--event`, `--created`, `-c/--commit`, `-L/--limit`, `--json`, `-q/--jq`, `-t/--template`, `-R/--repo`), plus what gh lacks.

Two arguments carry this, and neither is "the pipeline is inadequate":

1. **Standalone coherence.** [ADR-0002](./0002-go-gh-with-dual-distribution.md) ships a standalone binary. Telling a Homebrew user to "use `gh run` for one-shots" is not available to someone who does not have gh. The binary must be self-sufficient, or the channel is a lie. **Risk R1 threatened precisely this argument, and it is now resolved in the argument's favour.** go-gh cannot reach a keyring token without gh, so a user without gh supplies `GH_TOKEN`: the binary needs no gh, only a token, and standalone is a product rather than a shim. This argument stands as written.
2. **The filter engine exists anyway.** The Feed must parse and apply branch, status, workflow, actor, event and date filters regardless. Flags are a thin adapter over domain logic that has to exist, not a parallel product.

## Consequences

Compatibility is a **stated requirement, not an accident**. `gh run list` to `gh runs list` must be muscle memory. A cleaner syntax of our own was available and rejected: a second syntax to learn is exactly the cost the overlap objection accuses us of, and mirroring gh is what answers it. Superset, never divergence.

**Correction: `--conclusion` was proposed as the flagship example of that principle, and it does not survive measurement.** During design it was argued that gh's `-s/--status` "conflates" Status and Conclusion, and that we would keep that permissive behaviour for compatibility while adding a precise `--conclusion` flag gh lacks. Measured against the live API:

```text
?conclusion=failure     -> total_count=28710  conclusions=["success"]
?conclusion=bogusvalue  -> total_count=28710  conclusions=["success"]
(no filter)             -> total_count=28710
```

There is **no server-side `conclusion` filter**. The API's `status` param is the single param that matches *either* field, which is why `?status=success` genuinely narrows to 18,258. gh is faithfully mirroring the API, not conflating anything of its own. A `--conclusion` flag could only be client-side post-filtering over the reachable 1,000 ([ADR-0005](./0005-hybrid-filtered-live-unfiltered-purge.md)), filtering *what was reachable* rather than *what matches*, which is the exact dishonesty the PRD forbids.

**The flag is therefore dropped** (`cli-surface` R5). It is not deferred and not an open question: there is no parameter to expose. `-s/--status` already reaches Conclusion server-side, so `--conclusion` only ever bought the ability to name a Status and a Conclusion in one invocation. That is marginal, and it is not worth lying about counts for. Note what this costs the ADR's own thesis: the flagship example of "superset, never divergence" is gone. What the CLI still adds over `gh run` is `--dry-run` and `--yes` (`cli-surface` R10, R11), plus the four behaviours at the foot of this page. The two arguments above are what carry this decision. `--conclusion` never did.

**The API validates a known parameter's value, but silently ignores an unknown parameter.** These are opposite failure modes and an earlier draft of this ADR conflated them, claiming a misspelled value would "silently target everything". Measured, that is false:

```text
(no filter)         -> total_count=28718
?status=failure     -> total_count=1038   (valid value, filters correctly)
?status=faliure     -> total_count=0      (HTTP 200, matches nothing)
```

So an unknown **parameter** returns everything, while an invalid **value** on a known parameter returns nothing. Only the first is dangerous, and we build the query strings, so it is ours to avoid in code rather than a hazard a user can reach. The second is a usability defect: `gh runs list --status faliure` would exit cleanly having matched nothing, which reads as "no failures" rather than "you typed it wrong".

Filter values must still be validated client-side, because the API will not (`cli-surface` R6). The reason is that a typo silently produces an empty result, not that it silently produces a catastrophic one.

Note what v1 was, so nobody re-derives a false premise. v1 piped into `fzf --multi` and was **interactive-only**. It could never be scripted. There is no scripted install base this preserves, and that argument was made during design and was wrong.

What the CLI genuinely adds over the pipeline is narrow and real: throttling ([ADR-0007](./0007-adaptive-delete-throttle.md)), retry, looping past the silent 1,000 cap, and skipping in-progress Runs, which DELETE rejects.
