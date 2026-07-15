# CLI surface

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Decisions are recorded in [adr/](../../adr/).

## Purpose

`gh runs` with no arguments opens the TUI. The full non-interactive CLI exists alongside it as a **drop-in superset of `gh run`'s flags**, so that `gh run list` → `gh runs list` is muscle memory. It exists because the standalone binary must be self-sufficient for someone who does not have `gh` installed ([ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md)), and because the Feed's filter engine has to exist regardless. Flags are a thin adapter over it.

## Requirements

**R1.** `gh runs`, invoked with no subcommand and no arguments, MUST open the TUI. When standard output is not a terminal, it MUST NOT attempt to open the TUI, and MUST fail with a diagnostic rather than emit control sequences into a pipe.

**R2.** `gh runs list` MUST accept every flag `gh run list` accepts, with identical names, shorthands, argument types and semantics. No flag in this table may be renamed, dropped, or given a different meaning.

| Flag | Semantics (verified, gh 2.92.0) |
|---|---|
| `-a, --all` | Include disabled workflows |
| `-b, --branch string` | Filter Runs by branch |
| `-c, --commit SHA` | Filter Runs by the SHA of the commit |
| `--created date` | Filter Runs by the date it was created |
| `-e, --event event` | Filter Runs by which event triggered the Run |
| `-q, --jq expression` | Filter JSON output using a jq expression |
| `--json fields` | Output JSON with the specified fields |
| `-L, --limit int` | Maximum number of Runs to fetch (default 20) |
| `-s, --status string` | Filter Runs by status |
| `-t, --template string` | Format JSON output using a Go template |
| `-u, --user string` | Filter Runs by user who triggered the Run |
| `-w, --workflow string` | Filter Runs by Workflow |
| `-R, --repo [HOST/]OWNER/REPO` | Inherited. Select another repository |

**R3.** `-L/--limit` MUST default to 20, matching gh. `gh runs ls` MUST work as an alias of `gh runs list`, matching gh's own `gh run ls`.

**R4.** `-s/--status` MUST accept gh's full enum (`queued`, `completed`, `in_progress`, `requested`, `waiting`, `pending`, `action_required`, `cancelled`, `failure`, `neutral`, `skipped`, `stale`, `startup_failure`, `success`, `timed_out`) and MUST remain **permissive**, matching a Run whose Status *or* Conclusion equals the value. This conflates two fields that CONTEXT.md holds distinct, and it is retained deliberately: compatibility is a stated requirement, not an accident.

**R5.** `--conclusion` MUST be added, accepting only Conclusion values (`success`, `failure`, `cancelled`, `skipped`, `timed_out`, `neutral`, `action_required`, `stale`) and matching only against a Run's Conclusion. It is the precise counterpart to R4's permissive flag, and it is a pure addition (gh has no such flag).

**R6.** `-s/--status` and `--conclusion` MUST be combinable, conjunctively (`-s completed --conclusion failure` matches Runs that are both). A combination that cannot match any Run MUST return an empty result, not an error.

**R7.** `--json` MUST accept at least gh's field set: `attempt`, `conclusion`, `createdAt`, `databaseId`, `displayTitle`, `event`, `headBranch`, `headSha`, `name`, `number`, `startedAt`, `status`, `updatedAt`, `url`, `workflowDatabaseId`, `workflowName`. `-q/--jq` and `-t/--template` MUST operate over that output exactly as they do in gh. Additional fields MAY be offered. No listed field may be omitted or renamed.

**R8.** Repository identity MUST be host-qualified on every input path. A host may arrive by three routes, all verified as gh behaviour: `-R/--repo [HOST/]OWNER/REPO`, `GH_REPO` (same format), and `GH_HOST`. Each MUST be host-checked.

**R9.** A host other than `github.com` MUST be rejected explicitly, naming the host, before any network request is made. `-R ghe.corp/o/r` MUST report that the host is not supported yet, never a 404, an auth error, or a confusing failure. An explicit `github.com` host (`-R github.com/o/r`) MUST be accepted and treated identically to the bare `-R o/r` form.

**R10.** Every non-interactive destructive command MUST support `--dry-run`, which resolves the affected set of Runs by the same code path as the real operation, reports exactly what would be deleted, deletes nothing, and exits 0.

**R11.** Every non-interactive destructive command MUST require an explicit confirmation flag, and MUST refuse to delete without it. The flag is always required: no configuration setting may waive it, and there is no way to skip confirmation entirely ([settings](../settings/requirements.md)).

**R12.** A Purge MUST skip Runs whose Status is not `completed`, report them as **skipped** rather than failed, and count them separately in the summary. DELETE rejects in-progress Runs. Skipping them is one of the four things this CLI adds over the shell pipeline.

**R13.** A Purge MUST treat a 404 on DELETE as **success**. Under the stateless model ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md)) racing a previous pass or another person is routine, and "the Run is gone" is exactly what was asked for.

**R14.** A Purge MUST use the shared adaptive throttle ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)) and retry on transient failure. It MUST start at 1 delete/sec, ramp toward the points ceiling while responses stay clean, and back off on a 403, a 429 or a `Retry-After`. The rate MUST NOT be settable by flag or config.

**R15.** A Purge MUST reach Runs beyond the 1,000-result cap by crawling unfiltered and filtering client-side ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)). It MUST NOT stop on the first empty page of a filtered listing.

**R16.** Output MUST NOT report a count it cannot stand behind. Where a listing is capped, the CLI MUST label it as capped rather than present the cap as a total, and MUST NOT present a filtered `total_count` as the reachable number.

**R17.** Exit codes MUST follow gh's documented taxonomy (verified, `gh help exit-codes`): **0** on success, **1** on failure, **2** when a running command is cancelled, **4** when authentication is required. A Purge interrupted by the user MUST exit 2, leave already-deleted Runs deleted, and state that re-running the same filter resumes it.

**R18.** Interrupting a Purge MUST be safe at any point. No state file is written and none is needed: the filter is the job state, and resuming means running the same command again.

## Acceptance criteria

- `gh runs` with no arguments, on a TTY, opens the TUI. The same command with stdout redirected to a file exits non-zero with a diagnostic and writes no escape sequences.
- For each flag in R2, `gh runs list <flag>` and `gh run list <flag>` accept the same argument forms. Neither rejects an input the other accepts. `gh runs list` with no `-L` fetches at most 20 Runs.
- `gh runs list -s failure` returns Runs whose Conclusion is `failure` (Status-field mismatch notwithstanding). `gh runs list -s in_progress` returns Runs whose Status is `in_progress`. Both succeed.
- `gh runs list --conclusion in_progress` is rejected as an invalid value, because `in_progress` is a Status and not a Conclusion.
- `gh runs list -s completed --conclusion failure` returns only Runs that are both completed and failed. `gh runs list -s success --conclusion failure` returns an empty result and exits 0.
- `gh runs list --json databaseId,status,conclusion -q '.[].databaseId'` emits a bare list of IDs, byte-identical in shape to the same invocation against `gh run list`.
- `gh runs list -R ghe.corp/o/r`, `GH_REPO=ghe.corp/o/r gh runs list`, and `GH_HOST=ghe.corp gh runs list` each print a message naming `ghe.corp` and stating the host is unsupported, exit non-zero, and issue zero HTTP requests. `gh runs list -R github.com/cli/cli` behaves identically to `gh runs list -R cli/cli`.
- A Purge invoked without the confirmation flag deletes nothing and exits non-zero, whatever the config file contains.
- `--dry-run` over a filter matching N Runs reports N, issues no DELETE, and exits 0. Removing `--dry-run` and adding the confirmation flag deletes exactly the same N.
- A Purge over a set containing an `in_progress` Run reports it as skipped, not failed, and the command still exits 0 if all completed Runs were deleted.
- A DELETE returning 404 does not increment the failure count and does not change the exit code.
- A Purge against a repository with more than 1,000 matching Runs deletes matches beyond the first 1,000. It does not halt at the first empty filtered page.
- Sending SIGINT mid-Purge exits 2 and prints how to resume. Re-running the identical command deletes only the Runs that remain.
- With no credentials resolvable, any command requiring auth exits 4, not 1.

## Constraints

The flag names, shorthands, `-s` enum, `--json` field set, `-L` default of 20, the `ls` alias, and the exit-code taxonomy in this document were verified directly against **gh 2.92.0** (`gh run list --help`, `gh run delete --help`, `gh help exit-codes`, `gh help environment`). They are gh's contract, not ours, and they move when gh moves.

`gh run delete` takes exactly one Run ID and, invoked bare, interactively selects a single Run. It has **no `--dry-run` and no confirmation flag**, only inherited flags. R10 and R11 therefore have no gh flag to mirror. `--yes` ("Confirm deletion without prompting") is gh's established spelling on `gh repo delete`, and is the idiom to match rather than invent.

`-a/--all` is already bound to "include disabled workflows" and interacts specifically with `-w`: gh documents that a `workflow_name` passed to `-w` will not fetch disabled Workflows unless `-a` is also passed. **`-a` is therefore unavailable to mean "all repositories"**, which constrains any future cross-repo flag name.

gh documents, and the underlying API imposes, that **Runs created by organization and enterprise ruleset Workflows do not carry a Workflow name**. `--json workflowName` and `-w` filtering are both affected. Neither can be assumed populated.

**Filtered listing silently caps at 1,000.** Measured on `cli/cli`: `total_count` reports 18,260 for `status=success` while page 11 of a filtered listing returns `[]` with no error and no flag. Unfiltered paging walks to 28,707. Any code that paginates a filtered list and stops on an empty page is wrong (R15, R16).

**DELETE costs 5 points against a ~900 points/min secondary limit (~180/min), while GitHub's prose advises at least one second between writes (~60/min)**, a 3× disagreement. Purging 18,260 Runs takes ~100 minutes at best and ~5 hours at worst, which is why R14's throttle is adaptive and why a Purge cannot be a modal. For scale, v1 used `sleep 0.25` (~4/sec), faster than *both* numbers.

**Runs sort newest-first**, so a filtered listing reaches only the newest 1,000 matches while "delete Runs older than 90 days" asks for the oldest. This asymmetry is why a Purge cannot reuse the Feed's read path.

**There is no cross-repository Run query** in REST or GraphQL ([ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)). Any cross-repo CLI behaviour costs one request per repository: 163 repositories discovered, ~26 with Runs for the reference user.

**Fine-grained PATs expose no scopes** (`x-oauth-scopes` exists only for classic tokens), so no pre-flight permission check is possible. The API is the final authority: a 403 can arrive despite `push: true`, and destructive commands must report that cleanly rather than pre-empt it.

Historical note, recorded so nobody re-derives a false premise: **v1 was interactive-only.** It piped `gh api --paginate` through `jq` into `fzf --multi` and could never be scripted. There is no scripted install base being preserved by any of the above. That argument was made during design and was wrong.

## Open questions

- **Does `gh runs list` outside a repository, or without `-R`, fan out across all discovered repositories, or fail as gh does?** Drop-in compatibility fixes the in-repo case (no `-R` inside a repo means that repo) but says nothing about the out-of-repo case, where gh errors and a cross-repo tool plausibly should not. Undecided. Note `-a/--all` is not available as the opt-in flag name (see Constraints).
- **If the CLI can list across repositories, `--json` has no field identifying which repository a Run came from.** gh's 16 fields are all single-repo, so cross-repo JSON output would be ambiguous. Adding a repository field is a superset addition and safe, but is contingent on the question above.
- **Is `--conclusion` server-side or client-side?** Canon states the API's status filter matches either a Status or a Conclusion value, which implies a single permissive parameter and **no separate server-side `conclusion` parameter**. But this is UNKNOWN and MUST be verified rather than assumed. If it is client-side, `--conclusion` filters after fetching, so `-L 100 --conclusion failure` may return far fewer than 100. gh's own wording for `-L` is "maximum number of Runs **to fetch**", which is consistent with that behaviour, but the interaction must be documented and honestly labelled (R16) rather than left to surprise.
- **Which field does `startup_failure` belong to?** UNKNOWN, and it matters here more than anywhere. gh's verified `-s` enum holds 15 values. CONTEXT.md accounts for 14 of them (6 Statuses and 8 Conclusions), and `startup_failure` appears in **neither list**. R5 enumerates `--conclusion` from CONTEXT.md, so as written it rejects `startup_failure`. If the field is in fact a Conclusion, R5's enum is wrong *and* CONTEXT.md's Conclusion list is incomplete. This must be measured against the API and the canon corrected, not guessed. It sits exactly on the Status/Conclusion boundary the project treats as its defining distinction.
- **The exact set of Statuses for which DELETE is rejected is UNKNOWN.** Canon establishes that DELETE fails on in-progress Runs. Whether `queued`, `waiting`, `requested` and `pending` are also rejected has not been measured. R12 conservatively skips everything that is not `completed`. If the true rejected set is narrower, R12 skips Runs it could have deleted.
- **What is the confirmation flag named?** `--yes` is gh's spelling on `gh repo delete` and is the strong default, but `gh run delete` sets no precedent. Undecided.
- **What does `gh runs delete` with no Run ID do?** gh's bare `gh run delete` interactively selects one Run, while R1 gives bare `gh runs` to the TUI. The two conventions meet here and the resolution is undecided.
- **What is the exit code when a Purge partially fails, say 900 deleted, 100 failed with 403?** gh's taxonomy has one failure code and notes that a command may define more. Also undecided: whether a Purge matching zero Runs exits 0 or 1. gh has precedent in both directions: `gh cache delete --all` exits 1 on no caches, with `--succeed-on-no-caches` to opt into 0.
- **Does `*.ghe.com` reject correctly?** gh distinguishes three host classes: `github.com`, subdomains of `ghe.com` (Enterprise Cloud with data residency, authenticated by `GH_TOKEN`), and GHES hosts (authenticated by `GH_ENTERPRISE_TOKEN`). [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) mandates an explicit "GHES not supported yet", but that message is **inaccurate for a `tenant.ghe.com` host**, which is not GHES. Since the ADR's whole point is choosing explicit rejection over silent misbehaviour, an inaccurate rejection message is a weak form of the thing it set out to avoid. The message needs to distinguish the classes, or the ADR needs to say why it need not.
- **Risk R1 bears directly on this feature.** Standalone coherence is one of ADR-0008's two arguments for a full CLI surface, and it depends on go-gh resolving a token with **no gh binary installed**. The reference token lives in the OS keyring, not `hosts.yml`. `GH_TOKEN`/`GITHUB_TOKEN` are a documented escape hatch but do not help the reference user. If R1 fails, the justification for this feature's scope weakens and ADR-0008 needs revisiting.

## Related

- [ADR-0008: A full CLI surface, mirroring gh's flags, despite the overlap](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Repo identity is host-qualified, though 2.0.0 ships github.com only](../../adr/0009-host-qualified-repo-identity.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md)
- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md)
- [ADR-0001: Rename to gh-runs](../../adr/0001-rename-to-gh-runs.md)
- Siblings: [purge](../purge/requirements.md) (owns the Purge this CLI drives), [live-run-feed](../live-run-feed/requirements.md) (owns the filter engine these flags adapt), [rate-governor](../rate-governor/requirements.md) (owns R14's throttle), [settings](../settings/requirements.md) (owns what is deliberately not configurable), [repo-discovery](../repo-discovery/requirements.md)
