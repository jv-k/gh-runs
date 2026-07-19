# CLI surface

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Decisions are recorded in [adr/](../../adr/).

## Purpose

`gh runs` with no arguments opens the TUI. The full non-interactive CLI exists alongside it as a **drop-in superset of `gh run`'s flags**, so that `gh run list` → `gh runs list` is muscle memory. It exists because the standalone binary must be self-sufficient for someone who does not have `gh` installed ([ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md)), and because the Feed's filter engine has to exist regardless. Flags are a thin adapter over it. Where gh has no behaviour to mirror, the surface's shape is fixed by [ADR-0022](../../adr/0022-the-cli-beyond-gh-precedent.md): cross-repository fan-out, the bare `delete`, and the match-all spelling (R22 through R27).

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

**R9.** A host other than `github.com` MUST be rejected explicitly, naming the host, before any network request is made, never with a 404, an auth error, or a confusing failure. The message MUST be class-neutral: it names the host and states that 2.0.0 supports github.com only, and it MUST NOT claim the host is GHES or any other class. gh distinguishes github.com, `*.ghe.com` (Enterprise Cloud with data residency) and GHES, and a "GHES not supported" message shown to a `tenant.ghe.com` user is false. A message that makes no class claim cannot make a false one, and the CLI carries no host-class taxonomy whose only job is phrasing an error ([ADR-0009](../../adr/0009-host-qualified-repo-identity.md), amended). An explicit `github.com` host (`-R github.com/o/r`) MUST be accepted and treated identically to the bare `-R o/r` form.

**R10.** Every non-interactive destructive command MUST support `--dry-run`, which resolves the affected set of Runs by the same code path as the real operation, reports exactly what would be deleted, deletes nothing, and exits 0. Deleting nothing, it MUST write no line to [purge](../purge/requirements.md) R29's deletion log, and it MUST NOT require that log to be writable. A line records an attempt, `--dry-run` makes none, and a command that issues no DELETE has nothing R29 protects.

**"Exactly what would be deleted" means the Runs, not their count.** `--dry-run` MUST emit one row per Run in the resolved set, each naming its repository and its Run ID at minimum. A count on its own is a number the operator has no way to check, and this is the surface where checking is cheapest: the output is a file, a `grep` and a `wc -l`. [purge](../purge/requirements.md) R30 closes the same gap on the TUI, where the operator pages a viewport instead. One resolved set, two presentations, and R20 is what keeps them one set rather than two implementations that agree by luck.

**R11.** Every non-interactive destructive command MUST require `--yes`, and MUST refuse to delete without it. `--yes` is gh's established spelling on `gh repo delete`. Passing it **is** the confirmation on this surface: an explicit act, made once per invocation, by the person typing the command. That is what a persistent setting can never be, which is why the flag is confirmation and a config key that waived it would be a skip. The flag is always required. No configuration setting, environment variable or mode may waive it ([settings](../settings/requirements.md) R13), and none may supply it on the operator's behalf.

**R12.** A Purge MUST skip Runs whose Status is not `completed`, report them as **skipped** rather than failed, and count them separately in the summary. DELETE rejects in-progress Runs. Skipping them is one of the four things this CLI adds over the shell pipeline.

**The conservative skip is deliberate, and the narrower set is not worth knowing.** Whether DELETE also rejects `queued`, `waiting`, `requested` and `pending` is unmeasured, and measuring it means issuing live DELETEs, which R21 forbids absolutely. The question is moot as well as unknowable: every skipped Status is transient, a skipped Run becomes deletable once it completes, and R17's resume rail picks it up on the next invocation for free. The one persistent case, a Run parked in `waiting` on an approval, is a cancel-then-delete flow that belongs to [run-lifecycle](../run-lifecycle/requirements.md). Attempting the DELETE and classifying the response was rejected because an unexpected success would destroy a Run this requirement promised to skip. Do not re-open this with a live probe.

**R13.** A Purge MUST treat a 404 on DELETE as **success**. Under the stateless model ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md)) racing a previous pass or another person is routine, and "the Run is gone" is exactly what was asked for.

**R14.** A Purge MUST use the shared adaptive throttle ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)) and retry on transient failure. It MUST start at 1 delete/sec, ramp toward [rate-governor](../rate-governor/requirements.md) R11's ceiling while responses stay clean, and back off on a 403, a 429 or a `Retry-After`. That ceiling is dynamic, because reads and writes share one ~900 points/min pool. The rate MUST NOT be settable by flag or config.

**R15.** A Purge MUST reach Runs beyond the 1,000-result cap by crawling unfiltered and filtering client-side ([ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)). It MUST NOT stop on the first empty page of a filtered listing.

**R16.** Output MUST NOT report a count it cannot stand behind. Where a listing is capped, the CLI MUST label it as capped rather than present the cap as a total, and MUST NOT present a filtered `total_count` as the reachable number.

**R17.** Exit codes MUST follow gh's documented taxonomy (verified, `gh help exit-codes`): **0** on success, **1** on failure, **2** when a running command is cancelled, **4** when authentication is required. A Purge interrupted by the user MUST exit 2, leave already-deleted Runs deleted, and state that re-running the same filter resumes it.

**A partially failed Purge MUST exit 1, with no distinct partial code.** The exit code carries one bit, not everything happened, and the summary and deletion log carry the detail. Minting a code would be the first departure from gh's documented set, and a script would still have to parse the summary to act.

**A Purge whose filter matches zero Runs MUST exit 0.** Zero matches is the terminal state of every completed and resumed Purge, so a non-zero exit would make resume-to-completion end in failure. gh's contrary precedent (`gh cache delete --all` exits 1 on no caches, with `--succeed-on-no-caches` to opt out) is deliberately not followed, and no such opt flag exists here. A Purge that deletes nothing because every match was skipped under R12 also exits 0 (AC10).

**R18.** Interrupting a Purge MUST be safe at any point. No job record, no resolved ID list and no progress file is written, and none is needed: the filter is the job state, and resuming means running the same command again. [purge](../purge/requirements.md) R29's append-only deletion log is none of those three and is written: nothing reads it back, so it is not what R17's resume runs on, and an interrupted Purge's log MUST hold every deletion issued before the interrupt. Statelessness here is a rule about reading, not about writing ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md), amended).

### Seams

**R19.** Every request the CLI issues MUST pass through an injected transport, and the non-interactive paths MUST be exercisable end-to-end against recorded HTTP fixtures with no live network. AC5 and AC7 each assert that a command issues **zero** HTTP requests, and zero is a claim about a request that was never made. Only a transport that counts what passes through it can carry that claim. A test that watches for an error message passes just as happily while `-s faliure` reaches the wire and comes back with all 28,710 Runs, which is the exact failure R6 exists to prevent, and the same holds for the `ghe.corp` host R9 rejects. The fixtures MUST cover a crawl past the 1,000 cap (R15, AC12), a 404 on DELETE (R13, AC11), an in-progress Run's rejection (R12, AC10), and the auth failure R17 exits 4 on (AC14).

**R20.** `--dry-run` MUST resolve its set through the same code path as the real operation and MUST NOT be a second implementation of it. R10 makes it a production rail, and AC9 pins the equivalence: removing `--dry-run` and adding `--yes` deletes exactly the N it reported. That equivalence is what makes it usable in a test as well as at a keyboard. It is also why `--dry-run` is not the seam that makes deletion safe to test. A bug living in its own branch is precisely what a test would be hunting, so it cannot be trusted to prove itself. Deletion is proven against R19's fixtures. `--dry-run` is the rail an operator gets, not the harness.

**R21.** No test may issue a live DELETE. This tool deletes tens of thousands of Runs irreversibly and cannot undo one, and the measurements throughout this document (~28,700 Runs, `?status=success` narrowing to ~18,258, page 11 returning `[]`) were taken against real third-party repositories that other people depend on. Deletion is exercised against R19's fixtures, never against an account. This binds `--dry-run` as well: a flag an operator can forget is not a substitute for a test that never had a live endpoint to reach.

### Where gh has no precedent ([ADR-0022](../../adr/0022-the-cli-beyond-gh-precedent.md))

**R22.** `gh runs list`, invoked outside a repository with no `-R` and no `GH_REPO`, MUST fan out across every discovered repository ([repo-discovery](../repo-discovery/requirements.md), [ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)) rather than fail as gh does. Inside a repository, no `-R` MUST mean that repository, matching gh, so R2's parity holds. `--all-repos` (long form only, no shorthand, `-a` being taken) MUST force fan-out from anywhere, and MUST be accepted redundantly where fan-out is already the behaviour. The spelling is [settings](../settings/requirements.md) R19's own scope vocabulary, and the TUI's rule is the same: no repository means all repositories, not an error.

**R23.** Under fan-out, `-L/--limit` MUST bound the **merged total**: one list, newest-first by `CreatedAt`, at most `L` rows, defaulting to 20. It MUST NOT mean `L` per repository. How many Runs each per-repository request fetches before the merge is implementation.

**R24.** `--json` MUST offer a `repository` field, an object of `{name, nameWithOwner}`, requestable on any invocation including single-repo. The shape is gh's own on `gh search prs --json repository` (see Constraints), and it extends additively if multi-host ever adds a `host` key ([ADR-0009](../../adr/0009-host-qualified-repo-identity.md)). Under fan-out, the human table output MUST carry a repository column, because rows from different repositories are otherwise indistinguishable. R7's field set is unchanged: `repository` is a superset addition, never emitted unless requested.

**R25.** `gh runs delete`, invoked with no Run ID and no filter flags, MUST open the TUI, plain, identically to bare `gh runs`, including R1's non-TTY refusal. The command is an intent-synonym: the person typed "delete", and the TUI is where deletion is one operation. This deliberately diverges from gh's bare `gh run delete`, which prompts an interactive single-Run picker (see Constraints). No interactive selector exists outside the TUI.

**R26.** A match-all Purge MUST be asked for by name: `--all`. `gh runs delete --all --yes` deletes every Run in scope. `gh runs delete --yes` with no Run ID, no filter and no `--all` MUST fail with a usage diagnostic and delete nothing. [ADR-0016](../../adr/0016-the-filter-representation.md)'s zero-value Filter matches every Run, and this rule makes that zero filter unreachable by omission. The spelling is gh's own on `gh cache delete --all`, given no `-a` shorthand so it stays visually distinct from `list`'s unrelated `-a`.

**R27.** `--all-repos` MUST work on `delete` with the same semantics as on `list`, so a cross-repository Purge is expressible non-interactively. The rails are the existing ones: `--yes` (R11), `--all` for match-all (R26), `--dry-run`'s per-repository rows (R10), and the deletion log ([purge](../purge/requirements.md) R29). The widest expressible command, `gh runs delete --all-repos --all --yes`, is three explicit opt-ins, and no default or working directory reaches it by accident.

## Acceptance criteria

**AC1: The TUI needs a TTY.** `gh runs` with no arguments, on a TTY, opens the TUI. The same command with stdout redirected to a file exits non-zero with a diagnostic and writes no escape sequences.

**AC2: Flag parity with `gh run list`.** For each flag in R2, `gh runs list <flag>` and `gh run list <flag>` accept the same argument forms. Neither rejects an input the other accepts. `gh runs list` with no `-L` fetches at most 20 Runs.

**AC3: `-s` spans both fields.** `gh runs list -s failure` returns Runs whose Conclusion is `failure` (Status-field mismatch notwithstanding). `gh runs list -s in_progress` returns Runs whose Status is `in_progress`. Both succeed.

**AC4: `--conclusion` does not exist.** `gh runs list --conclusion failure` is rejected as an unknown flag. No command issues a request carrying a `conclusion` query parameter.

**AC5: A typo is caught before the wire.** `gh runs list -s faliure` is rejected by name, client-side, and issues zero HTTP requests. It does not list every Run in the repository.

**AC6: `--json` and `-q` match gh.** `gh runs list --json databaseId,status,conclusion -q '.[].databaseId'` emits a bare list of IDs, byte-identical in shape to the same invocation against `gh run list`.

**AC7: An unsupported host is rejected offline.** `gh runs list -R ghe.corp/o/r`, `GH_REPO=ghe.corp/o/r gh runs list`, and `GH_HOST=ghe.corp gh runs list` each print a message naming `ghe.corp` and stating the host is unsupported, exit non-zero, and issue zero HTTP requests. `gh runs list -R github.com/cli/cli` behaves identically to `gh runs list -R cli/cli`.

**AC8: `--yes` is never waivable.** A Purge invoked without `--yes` deletes nothing and exits non-zero, whatever the config file contains. No config key makes `--yes` optional, and none supplies it.

**AC9: `--dry-run` resolves the same set.** `--dry-run` over a filter matching N Runs emits those N Runs, one row each, each row naming its repository and Run ID, issues no DELETE, and exits 0. It writes no line to the deletion log, and it still exits 0 with the state directory unwritable (R10). Removing `--dry-run` and adding `--yes` deletes exactly the same N and writes exactly N lines.

**AC10: An in-progress Run is skipped, not failed.** A Purge over a set containing an `in_progress` Run reports it as skipped, not failed, and the command still exits 0 if all completed Runs were deleted.

**AC11: A 404 on DELETE is success.** A DELETE returning 404 does not increment the failure count and does not change the exit code.

**AC12: A Purge reaches past the cap.** A Purge against a repository with more than 1,000 matching Runs deletes matches beyond the first 1,000. It does not halt at the first empty filtered page.

**AC13: Interruption exits 2 and resumes.** Sending SIGINT mid-Purge exits 2 and prints how to resume. Re-running the identical command deletes only the Runs that remain.

**AC14: Auth failure exits 4.** With no credentials resolvable, any command requiring auth exits 4, not 1.

**AC15: Scope follows the working directory, and the flag overrides it.** Against R19's fixtures, `gh runs list` outside a repository issues one listing request per discovered repository and emits a merged list. The same command inside a repository issues requests against that repository alone. `gh runs list --all-repos` inside a repository fans out anyway.

**AC16: The merged limit.** Under fan-out with repositories holding more than 20 Runs between them, `gh runs list` emits at most 20 rows, newest-first by creation time, and `-L 5` emits at most 5. No invocation emits `L` rows per repository.

**AC17: The repository field.** `gh runs list --json repository,databaseId` emits `repository` as `{"name": ..., "nameWithOwner": ...}` for every row, under fan-out and single-repo alike. Without `repository` in the field list, no repository key appears.

**AC18: Bare delete is the TUI.** `gh runs delete` with no arguments, on a TTY, opens the TUI exactly as bare `gh runs` does. With stdout redirected it exits non-zero with a diagnostic and writes no escape sequences.

**AC19: Match-all needs its name.** `gh runs delete --yes` with no Run ID, no filter and no `--all` exits non-zero, deletes nothing, and issues zero DELETE requests. `gh runs delete --all --dry-run` resolves every Run in scope.

**AC20: Purge exit codes.** Against R19's fixtures, a Purge whose deletes all succeed exits 0. A Purge with any real failure (a 403 among the deletes) exits 1. A Purge whose filter matches zero Runs exits 0 and prints a summary saying so.

## Constraints

The flag names, shorthands, `-s` enum, `--json` field set, `-L` default of 20, the `ls` alias, and the exit-code taxonomy in this document were verified directly against **gh 2.92.0** (`gh run list --help`, `gh run delete --help`, `gh help exit-codes`, `gh help environment`). They are gh's contract, not ours, and they move when gh moves.

`gh run delete` takes exactly one Run ID and, invoked bare, interactively selects a single Run. It has **no `--dry-run` and no confirmation flag**, only inherited flags. R10 and R11 therefore have no gh flag on `gh run` to mirror. `--yes` ("Confirm deletion without prompting") is gh's established spelling on `gh repo delete`, which is why R11 adopts that spelling rather than inventing one. `--all` is gh's established spelling for "everything" on `gh cache delete --all`, which is why R26 adopts it.

**R25 is this surface's one deliberate divergence from a gh behaviour.** gh's bare `gh run delete` prompts an interactive picker, and ours opens the TUI instead ([ADR-0022](../../adr/0022-the-cli-beyond-gh-precedent.md)). Everywhere else the surface diverges only where gh has nothing, never against something gh does.

**The `repository` object shape was verified against gh 2.92.0**: `gh search prs --json repository` emits `{"name": ..., "nameWithOwner": ...}`, which is the precedent R24 adopts.

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

- **Resolved: outside a repository, `list` fans out.** The product is cross-repository first, and the TUI's own scope rule (settings R19) answers "no repository" with `all-repos` rather than an error. Inside a repository gh's rule holds, and `--all-repos` forces fan-out from anywhere. R22 and R23 carry it, [ADR-0022](../../adr/0022-the-cli-beyond-gh-precedent.md) records the options it beat, including failing with a diagnostic that names the flag.
- **Resolved: `--json` gains a `repository` object.** `{name, nameWithOwner}`, gh's own shape on `gh search prs`, requestable on any invocation and extensible with a `host` key later. R24.
- **Resolved: `--conclusion` is neither. No such parameter exists, and the flag is dropped.** It was asked whether a Conclusion filter was server-side or client-side. Measurement answered a third way: the API has no `conclusion` parameter to be either, and ignores one silently (see Constraints). Client-side was the only remaining option, it filters what was reachable rather than what matches, and R5 drops the flag instead. `-s/--status` already reaches Conclusion server-side, so nothing is lost but the Status-and-Conclusion combination.
- **Resolved: `startup_failure` is a Conclusion.** Measured on real Runs across `cli/cli`, `home-assistant/core` and `microsoft/vscode`: `status=completed conclusion=startup_failure`. CONTEXT.md is corrected and now carries nine Conclusions. The arithmetic closes exactly: gh's 15-value `-s` enum is CONTEXT.md's six Statuses plus its nine Conclusions, with nothing unaccounted on either side (R4). It sat on the Status/Conclusion boundary the project treats as its defining distinction, and it fell on the Conclusion side.
- **Resolved: the rejected-Status set is unknowable within policy, and moot.** Measuring it means live DELETEs, which R21 forbids, and the answer would not change R12: every skipped Status is transient, so a skipped Run becomes deletable on completion and the resume rail collects it. R12 stands, carries the rationale, and forbids re-measuring with a live probe.
- **Resolved: the confirmation flag is `--yes`.** gh's spelling on `gh repo delete`, adopted by R11. `gh run delete` set no precedent, so the nearest one in gh's own surface wins.
- **Resolved: bare `gh runs delete` opens the TUI.** An intent-synonym for bare `gh runs`, plain, with R1's non-TTY refusal. No second interactive picker exists beside the TUI, and the divergence from gh's bare-delete prompt is recorded in Constraints. R25, with the match-all guard in R26: `--yes` alone fails, and only `--all --yes` spells "everything". [ADR-0022](../../adr/0022-the-cli-beyond-gh-precedent.md).
- **Resolved: a partially failed Purge exits 1, and a zero-match Purge exits 0.** One failure bit, no minted code, and the summary carries the detail. Zero matches is the terminal state of every resumed Purge, so it is success, and gh's `--succeed-on-no-caches` machinery is not carried. R17.
- **Resolved: the rejection message is class-neutral.** It names the host and states that 2.0.0 supports github.com only, claiming nothing about what the host is, so it cannot be false for `tenant.ghe.com` and the CLI carries no host-class taxonomy. R9, and [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) is amended in place.
- **Resolved: risk R1 does not weaken this feature's scope.** Standalone coherence is one of ADR-0008's two arguments for a full CLI surface, and it depended on go-gh resolving a token with **no gh binary installed**. It does not: go-gh has no keyring code, its only keyring path is shelling out to `gh auth token`, and with gh off PATH the reference keyring token resolves empty. The product owner's decision is to **require `GH_TOKEN` for users without gh, and document it** ([ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md)). `GH_TOKEN`/`GITHUB_TOKEN` are therefore the contract rather than an escape hatch, which is the normal contract for a CLI and what CI already does. The binary needs no gh, only a token, so ADR-0008's argument stands and this feature's scope is unchanged.

## Related

- [ADR-0022: The CLI beyond gh's precedent: fan-out, the bare delete, and the match-all spelling](../../adr/0022-the-cli-beyond-gh-precedent.md)
- [ADR-0008: A full CLI surface, mirroring gh's flags, despite the overlap](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Repo identity is host-qualified, though 2.0.0 ships github.com only](../../adr/0009-host-qualified-repo-identity.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md)
- [ADR-0002: Build on go-gh, ship as both a gh extension and a standalone binary](../../adr/0002-go-gh-with-dual-distribution.md)
- [ADR-0001: Rename to gh-runs](../../adr/0001-rename-to-gh-runs.md)
- Siblings: [purge](../purge/requirements.md) (owns the Purge this CLI drives), [live-run-feed](../live-run-feed/requirements.md) (owns the filter engine these flags adapt), [rate-governor](../rate-governor/requirements.md) (owns R14's throttle), [settings](../settings/requirements.md) (owns what is deliberately not configurable), [repo-discovery](../repo-discovery/requirements.md)
