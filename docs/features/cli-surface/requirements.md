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

**R4.** `-s/--status` MUST accept gh's full enum (`queued`, `completed`, `in_progress`, `requested`, `waiting`, `pending`, `action_required`, `cancelled`, `failure`, `neutral`, `skipped`, `stale`, `startup_failure`, `success`, `timed_out`) and MUST remain **permissive**, matching a Run whose Status *or* Conclusion equals the value. Those 15 values are exactly CONTEXT.md's six Statuses plus its nine Conclusions, with nothing left over on either side, so the flag spans two fields the canon holds distinct. It is retained deliberately: compatibility is a stated requirement, not an accident, and gh is mirroring the API's own parameter rather than inventing the breadth.

**R5.** `--conclusion` MUST NOT be provided. There is no server-side `conclusion` parameter: `?conclusion=failure` and `?conclusion=bogusvalue` each returned every Run in the repository, identical to no filter at all. A `--conclusion` flag could therefore only post-filter client-side, over the newest 1,000 Runs a filtered listing reaches ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)), reporting *what was reachable* rather than *what matches*. That is the dishonesty R16 forbids. R4's `-s/--status` already filters on Conclusion server-side (`?status=success` narrows 28,694 to 18,258), so the flag only ever bought naming a Status and a Conclusion in one invocation, which is marginal and not worth lying about counts for ([ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md)).

**R6.** Every filter value MUST be validated client-side, against its own enum, before any request is issued, and an unrecognised value MUST be rejected by name. The API will not do it, and its two failure modes are opposites. An **unknown parameter** is ignored and answered with a full unfiltered result set (`?conclusion=bogusvalue` returned all 28,710 Runs, which was the repository's full unfiltered total at that measurement). An **invalid value on a known parameter** is answered with HTTP 200 and `total_count: 0` (`?status=faliure` matched nothing, against 1,038 for `?status=failure`). We build the query strings, so the first is ours to avoid in code. The second is what R6 exists for: unchecked, `-s faliure` exits cleanly having matched nothing, which reads as "no failures" rather than "you typed it wrong".

**R7.** `--json` MUST accept at least gh's field set: `attempt`, `conclusion`, `createdAt`, `databaseId`, `displayTitle`, `event`, `headBranch`, `headSha`, `name`, `number`, `startedAt`, `status`, `updatedAt`, `url`, `workflowDatabaseId`, `workflowName`. `-q/--jq` and `-t/--template` MUST operate over that output exactly as they do in gh. Additional fields MAY be offered. No listed field may be omitted or renamed.

**These are gh's names, not the API's, so a Run needs two serialisations.** The API serves `id`, `display_title` and `workflow_id` where this flag must emit `databaseId`, `displayTitle` and `workflowDatabaseId`. [ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md) gives each a home: `domain.Run` decodes the API's shape, and the gh-compatible projection lives **here, in `cli`**, because it exists to satisfy this one flag on this one surface and nothing else in the tree renders it.

**R8.** Repository identity MUST be host-qualified on every input path. A host may arrive by three routes, all verified as gh behaviour: `-R/--repo [HOST/]OWNER/REPO`, `GH_REPO` (same format), and `GH_HOST`. Each MUST be host-checked.

**R9.** A host other than `github.com` MUST be rejected explicitly, naming the host, before any network request is made. `-R ghe.corp/o/r` MUST report that the host is not supported yet, never a 404, an auth error, or a confusing failure. An explicit `github.com` host (`-R github.com/o/r`) MUST be accepted and treated identically to the bare `-R o/r` form.

**R10.** Every non-interactive destructive command MUST support `--dry-run`, which resolves the affected set of Runs by the same code path as the real operation, reports exactly what would be deleted, deletes nothing, and exits 0.

**R11.** Every non-interactive destructive command MUST require `--yes`, and MUST refuse to delete without it. `--yes` is gh's established spelling on `gh repo delete`. Passing it **is** the confirmation on this surface: an explicit act, made once per invocation, by the person typing the command. That is what a persistent setting can never be, which is why the flag is confirmation and a config key that waived it would be a skip. The flag is always required. No configuration setting, environment variable or mode may waive it ([settings](../settings/requirements.md) R13), and none may supply it on the operator's behalf.

**R12.** A Purge MUST skip Runs whose Status is not `completed`, report them as **skipped** rather than failed, and count them separately in the summary. DELETE rejects in-progress Runs. Skipping them is one of the four things this CLI adds over the shell pipeline.

**R13.** A Purge MUST treat a 404 on DELETE as **success**. Under the stateless model ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md)) racing a previous pass or another person is routine, and "the Run is gone" is exactly what was asked for.

**R14.** A Purge MUST use the shared adaptive throttle ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)) and retry on transient failure. It MUST start at 1 delete/sec, ramp toward [rate-governor](../rate-governor/requirements.md) R11's ceiling while responses stay clean, and back off on a 403, a 429 or a `Retry-After`. That ceiling is dynamic, because reads and writes share one ~900 points/min pool. The rate MUST NOT be settable by flag or config.

**R15.** A Purge MUST reach Runs beyond the 1,000-result cap by crawling unfiltered and filtering client-side ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)). It MUST NOT stop on the first empty page of a filtered listing.

**R16.** Output MUST NOT report a count it cannot stand behind. Where a listing is capped, the CLI MUST label it as capped rather than present the cap as a total, and MUST NOT present a filtered `total_count` as the reachable number.

**R17.** Exit codes MUST follow gh's documented taxonomy (verified, `gh help exit-codes`): **0** on success, **1** on failure, **2** when a running command is cancelled, **4** when authentication is required. A Purge interrupted by the user MUST exit 2, leave already-deleted Runs deleted, and state that re-running the same filter resumes it.

**R18.** Interrupting a Purge MUST be safe at any point. No state file is written and none is needed: the filter is the job state, and resuming means running the same command again.

### Seams

**R19.** Every request the CLI issues MUST pass through an injected transport, and the non-interactive paths MUST be exercisable end-to-end against recorded HTTP fixtures with no live network. AC5 and AC7 each assert that a command issues **zero** HTTP requests, and zero is a claim about a request that was never made. Only a transport that counts what passes through it can carry that claim. A test that watches for an error message passes just as happily while `-s faliure` reaches the wire and comes back with all 28,710 Runs, which is the exact failure R6 exists to prevent, and the same holds for the `ghe.corp` host R9 rejects. The fixtures MUST cover a crawl past the 1,000 cap (R15, AC12), a 404 on DELETE (R13, AC11), an in-progress Run's rejection (R12, AC10), and the auth failure R17 exits 4 on (AC14).

**R20.** `--dry-run` MUST resolve its set through the same code path as the real operation and MUST NOT be a second implementation of it. R10 makes it a production rail, and AC9 pins the equivalence: removing `--dry-run` and adding `--yes` deletes exactly the N it reported. That equivalence is what makes it usable in a test as well as at a keyboard. It is also why `--dry-run` is not the seam that makes deletion safe to test. A bug living in its own branch is precisely what a test would be hunting, so it cannot be trusted to prove itself. Deletion is proven against R19's fixtures. `--dry-run` is the rail an operator gets, not the harness.

**R21.** No test may issue a live DELETE. This tool deletes tens of thousands of Runs irreversibly and cannot undo one, and the measurements throughout this document (~28,700 Runs, `?status=success` narrowing to ~18,258, page 11 returning `[]`) were taken against real third-party repositories that other people depend on. Deletion is exercised against R19's fixtures, never against an account. This binds `--dry-run` as well: a flag an operator can forget is not a substitute for a test that never had a live endpoint to reach.

## Acceptance criteria

**AC1: The TUI needs a TTY.** `gh runs` with no arguments, on a TTY, opens the TUI. The same command with stdout redirected to a file exits non-zero with a diagnostic and writes no escape sequences.

**AC2: Flag parity with `gh run list`.** For each flag in R2, `gh runs list <flag>` and `gh run list <flag>` accept the same argument forms. Neither rejects an input the other accepts. `gh runs list` with no `-L` fetches at most 20 Runs.

**AC3: `-s` spans both fields.** `gh runs list -s failure` returns Runs whose Conclusion is `failure` (Status-field mismatch notwithstanding). `gh runs list -s in_progress` returns Runs whose Status is `in_progress`. Both succeed.

**AC4: `--conclusion` does not exist.** `gh runs list --conclusion failure` is rejected as an unknown flag. No command issues a request carrying a `conclusion` query parameter.

**AC5: A typo is caught before the wire.** `gh runs list -s faliure` is rejected by name, client-side, and issues zero HTTP requests. It does not list every Run in the repository.

**AC6: `--json` and `-q` match gh.** `gh runs list --json databaseId,status,conclusion -q '.[].databaseId'` emits a bare list of IDs, byte-identical in shape to the same invocation against `gh run list`.

**AC7: An unsupported host is rejected offline.** `gh runs list -R ghe.corp/o/r`, `GH_REPO=ghe.corp/o/r gh runs list`, and `GH_HOST=ghe.corp gh runs list` each print a message naming `ghe.corp` and stating the host is unsupported, exit non-zero, and issue zero HTTP requests. `gh runs list -R github.com/cli/cli` behaves identically to `gh runs list -R cli/cli`.

**AC8: `--yes` is never waivable.** A Purge invoked without `--yes` deletes nothing and exits non-zero, whatever the config file contains. No config key makes `--yes` optional, and none supplies it.

**AC9: `--dry-run` resolves the same set.** `--dry-run` over a filter matching N Runs reports N, issues no DELETE, and exits 0. Removing `--dry-run` and adding `--yes` deletes exactly the same N.

**AC10: An in-progress Run is skipped, not failed.** A Purge over a set containing an `in_progress` Run reports it as skipped, not failed, and the command still exits 0 if all completed Runs were deleted.

**AC11: A 404 on DELETE is success.** A DELETE returning 404 does not increment the failure count and does not change the exit code.

**AC12: A Purge reaches past the cap.** A Purge against a repository with more than 1,000 matching Runs deletes matches beyond the first 1,000. It does not halt at the first empty filtered page.

**AC13: Interruption exits 2 and resumes.** Sending SIGINT mid-Purge exits 2 and prints how to resume. Re-running the identical command deletes only the Runs that remain.

**AC14: Auth failure exits 4.** With no credentials resolvable, any command requiring auth exits 4, not 1.

## Constraints

The flag names, shorthands, `-s` enum, `--json` field set, `-L` default of 20, the `ls` alias, and the exit-code taxonomy in this document were verified directly against **gh 2.92.0** (`gh run list --help`, `gh run delete --help`, `gh help exit-codes`, `gh help environment`). They are gh's contract, not ours, and they move when gh moves.

`gh run delete` takes exactly one Run ID and, invoked bare, interactively selects a single Run. It has **no `--dry-run` and no confirmation flag**, only inherited flags. R10 and R11 therefore have no gh flag on `gh run` to mirror. `--yes` ("Confirm deletion without prompting") is gh's established spelling on `gh repo delete`, which is why R11 adopts that spelling rather than inventing one.

**There is no server-side `conclusion` parameter, and the API ignores an unknown query parameter silently.** Measured against the live API:

```text
?conclusion=failure     -> total_count=28710  conclusions=["success"]
?conclusion=bogusvalue  -> total_count=28710  conclusions=["success"]
(no filter)             -> total_count=28710
```

A garbage value returned every Run in the repository, with no error and no flag, and `conclusions=["success"]` shows the returned page was never filtered at all. `?status=success` by contrast narrows 28,694 to 18,258, so `status` is the one parameter that matches either field and gh's permissive `-s` mirrors the API rather than conflating anything of its own. R5 drops `--conclusion` on this measurement. R6 rests on it too, and reads it as the more general hazard: an API that answers unrecognised input with a full result set will not catch a typo for us.

`-a/--all` is already bound to "include disabled workflows" and interacts specifically with `-w`: gh documents that a `workflow_name` passed to `-w` will not fetch disabled Workflows unless `-a` is also passed. **`-a` is therefore unavailable to mean "all repositories"**, which constrains any future cross-repo flag name.

gh documents, and the underlying API imposes, that **Runs created by organization and enterprise ruleset Workflows do not carry a Workflow name**. `--json workflowName` and `-w` filtering are both affected. Neither can be assumed populated.

**Filtered listing silently caps at 1,000.** Measured on `cli/cli`: `total_count` reports 18,258 for `status=success` while page 11 of a filtered listing returns `[]` with no error and no flag. Unfiltered paging walks to 28,694. These totals are point-in-time and drift ([PRD](../../PRD.md)), so no fixture may hardcode one. Any code that paginates a filtered list and stops on an empty page is wrong (R15, R16).

**DELETE costs 5 points against a ~900 points/min secondary limit (~180/min), while GitHub's prose advises at least one second between writes (~60/min)**, a 3× disagreement. Both are documented rather than measured ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)). That published band puts 18,258 Runs at ~100 minutes to ~5 hours, and the governor reaches neither end of it: [rate-governor](../rate-governor/requirements.md) R11 caps at 150/min and floors at 30/min, so a real Purge runs ~2 hours to ~10 hours, and ~155 minutes with the Feed polling. That is why R14's throttle is adaptive and why a Purge cannot be a modal. For scale, v1 used `sleep 0.25` (~4/sec), faster than *both* numbers.

**Runs sort newest-first**, so a filtered listing reaches only the newest 1,000 matches while "delete Runs older than 90 days" asks for the oldest. This asymmetry is why a Purge cannot reuse the Feed's read path.

**There is no cross-repository Run query** in REST or GraphQL ([ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)). Any cross-repo CLI behaviour costs one request per repository: 163 repositories discovered, ~26 with Runs for the reference user.

**Fine-grained PATs expose no scopes** (`x-oauth-scopes` exists only for classic tokens), so no pre-flight permission check is possible. The API is the final authority: a 403 can arrive despite `push: true`, and destructive commands must report that cleanly rather than pre-empt it.

Historical note, recorded so nobody re-derives a false premise: **v1 was interactive-only.** It piped `gh api --paginate` through `jq` into `fzf --multi` and could never be scripted. There is no scripted install base being preserved by any of the above. That argument was made during design and was wrong.

## Open questions

- **Does `gh runs list` outside a repository, or without `-R`, fan out across all discovered repositories, or fail as gh does?** Drop-in compatibility fixes the in-repo case (no `-R` inside a repo means that repo) but says nothing about the out-of-repo case, where gh errors and a cross-repo tool plausibly should not. Undecided. Note `-a/--all` is not available as the opt-in flag name (see Constraints).
- **If the CLI can list across repositories, `--json` has no field identifying which repository a Run came from.** gh's 16 fields are all single-repo, so cross-repo JSON output would be ambiguous. Adding a repository field is a superset addition and safe, but is contingent on the question above.
- **Resolved: `--conclusion` is neither. No such parameter exists, and the flag is dropped.** It was asked whether a Conclusion filter was server-side or client-side. Measurement answered a third way: the API has no `conclusion` parameter to be either, and ignores one silently (see Constraints). Client-side was the only remaining option, it filters what was reachable rather than what matches, and R5 drops the flag instead. `-s/--status` already reaches Conclusion server-side, so nothing is lost but the Status-and-Conclusion combination.
- **Resolved: `startup_failure` is a Conclusion.** Measured on real Runs across `cli/cli`, `home-assistant/core` and `microsoft/vscode`: `status=completed conclusion=startup_failure`. CONTEXT.md is corrected and now carries nine Conclusions. The arithmetic closes exactly: gh's 15-value `-s` enum is CONTEXT.md's six Statuses plus its nine Conclusions, with nothing unaccounted on either side (R4). It sat on the Status/Conclusion boundary the project treats as its defining distinction, and it fell on the Conclusion side.
- **The exact set of Statuses for which DELETE is rejected is UNKNOWN.** Canon establishes that DELETE fails on in-progress Runs. Whether `queued`, `waiting`, `requested` and `pending` are also rejected has not been measured. R12 conservatively skips everything that is not `completed`. If the true rejected set is narrower, R12 skips Runs it could have deleted.
- **Resolved: the confirmation flag is `--yes`.** gh's spelling on `gh repo delete`, adopted by R11. `gh run delete` set no precedent, so the nearest one in gh's own surface wins.
- **What does `gh runs delete` with no Run ID do?** gh's bare `gh run delete` interactively selects one Run, while R1 gives bare `gh runs` to the TUI. The two conventions meet here and the resolution is undecided.
- **What is the exit code when a Purge partially fails, say 900 deleted, 100 failed with 403?** gh's taxonomy has one failure code and notes that a command may define more. Also undecided: whether a Purge matching zero Runs exits 0 or 1. gh has precedent in both directions: `gh cache delete --all` exits 1 on no caches, with `--succeed-on-no-caches` to opt into 0.
- **Does `*.ghe.com` reject correctly?** gh distinguishes three host classes: `github.com`, subdomains of `ghe.com` (Enterprise Cloud with data residency, authenticated by `GH_TOKEN`), and GHES hosts (authenticated by `GH_ENTERPRISE_TOKEN`). [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) mandates an explicit "GHES not supported yet", but that message is **inaccurate for a `tenant.ghe.com` host**, which is not GHES. Since the ADR's whole point is choosing explicit rejection over silent misbehaviour, an inaccurate rejection message is a weak form of the thing it set out to avoid. The message needs to distinguish the classes, or the ADR needs to say why it need not.
- **Resolved: risk R1 does not weaken this feature's scope.** Standalone coherence is one of ADR-0008's two arguments for a full CLI surface, and it depended on go-gh resolving a token with **no gh binary installed**. It does not: go-gh has no keyring code, its only keyring path is shelling out to `gh auth token`, and with gh off PATH the reference keyring token resolves empty. The product owner's decision is to **require `GH_TOKEN` for users without gh, and document it** ([ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md)). `GH_TOKEN`/`GITHUB_TOKEN` are therefore the contract rather than an escape hatch, which is the normal contract for a CLI and what CI already does. The binary needs no gh, only a token, so ADR-0008's argument stands and this feature's scope is unchanged.

## Related

- [ADR-0008: A full CLI surface, mirroring gh's flags, despite the overlap](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Repo identity is host-qualified, though 2.0.0 ships github.com only](../../adr/0009-host-qualified-repo-identity.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md)
- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md)
- [ADR-0001: Rename to gh-runs](../../adr/0001-rename-to-gh-runs.md)
- Siblings: [purge](../purge/requirements.md) (owns the Purge this CLI drives), [live-run-feed](../live-run-feed/requirements.md) (owns the filter engine these flags adapt), [rate-governor](../rate-governor/requirements.md) (owns R14's throttle), [settings](../settings/requirements.md) (owns what is deliberately not configurable), [repo-discovery](../repo-discovery/requirements.md)
