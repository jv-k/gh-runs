# Log viewer

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Constraints cite [PRD.md](../../PRD.md).

## Purpose

Read a Job's log inside the tool, rendered the way the web UI renders it (folded, de-noised, legible in a terminal) without leaving for a browser. Logs are fetched one Job at a time, on open, so nothing is downloaded that is not read.

**This line used to say "legible at 80 columns" and it was the only 80 in the canon.** It contradicted the Feed's own mandated row, which [live-run-feed](../live-run-feed/requirements.md) R4a costs at 100 columns minimum and 80 cannot hold. A log is one column of text and stays legible at any width, so the number was doing no work here while quietly disagreeing with the surface this pane opens over.

## Requirements

**R1.** Open the log for a single Job, selected from Run detail, and fetch that Job's plain-text log at the moment it is opened. Logs for Jobs that are not opened must never be fetched.

**R2.** Never fetch the whole-Run archive to satisfy a viewing request. The archive exists for the export in R11 and for nothing else. The per-Job plain text is the only viewing path.

**R3.** Strip the UTF-8 BOM carried on the first line. It must never reach the display, and it must never be counted as part of the first line's content.

**R4.** Strip the 29-character ISO timestamp prefix from every line by default, and provide a toggle that restores it. When the toggle is on, the restored prefix must be byte-identical to the one the API sent. The toggle is the view's session state, not a [settings](../settings/requirements.md) key: showing timestamps is mechanism, and settings hold intent (settings R2).

**R5.** Present each `##[group]` … `##[endgroup]` span as one collapsible fold, labelled with the text following the `##[group]` marker and collapsed by default. Folds must be expandable and re-collapsible.

**R6.** Derive Step boundaries from `##[group]`/`##[endgroup]` markers alone. These boundaries are approximate. A Job author is free to group by something other than Steps, and Steps that emit no group produce no fold. That approximation is accepted rather than corrected by downloading the archive.

**R7.** Style `##[error]`, `##[warning]` and `##[notice]` lines distinctly from ordinary log lines and from each other.

**R8.** Recognise the whole marker family: `group`, `endgroup`, `error`, `warning`, `notice`, `command`, `debug`. Never render a recognised marker's literal `##[…]` syntax as log text. A marker outside that family must render verbatim, because swallowing it would destroy content the tool does not understand.

**R9.** Carry every distinction in text as well as colour, never in colour alone. v1's `SKIP`/`GOOD`/`FAIL` labels were the accessible choice by accident. That principle is now deliberate.

**R10.** Suppress colour when `NO_COLOR` is set, and remain fully legible without it, which R9 guarantees.

**R11.** Provide an explicit "download whole Run" export that fetches the run-level archive and writes it to disk as received. This is the one place where fetching everything is the user's actual intent. The archive is written as the single file received, not unpacked, because unpacking assumes a directory the export was not asked for.

**R12.** Never derive a Job name, a Step name, or any other domain value from a filename inside the archive. Archive filenames are lossily sanitised and cannot be reversed. Correlate by index if correlation is ever needed.

**R13.** Never persist, cache or reuse a signed log URL. Each fetch re-requests from the API endpoint and follows the redirect it returns.

**R14.** Never display a determinate progress percentage or a predicted size while a log or archive downloads. The size cannot be known before the download starts.

**R15.** Never present log content as live and never offer to tail it. Provide an explicit refetch instead. A Job's or Step's Status may update while its log is open. The content behind it does not.

**R16.** Offer logs only for the Jobs of a Run's latest Attempt. A prior Attempt's log must not be offered, because a prior Attempt's Jobs are not served at all.

**R17.** Provide log deletion as an operation distinct from deleting the Run, reachable by a different keystroke, separately confirmed, and gated on `permissions.push && !archived` for the repository. The confirmation must name both what is destroyed (the logs) and what survives (the Run and its metadata). Gating is advisory only: the API is the final authority, and a 403 must be reported as a permission failure rather than treated as a bug. Log deletion is a write, and it gets no dispensation for being small: it must route through the Purge's graduated confirmation, [purge](../purge/requirements.md) R4 to R9, exactly as [run-lifecycle](../run-lifecycle/requirements.md) R17 and [storage-reclamation](../storage-reclamation/requirements.md) R17 do. That means selection keyed by id, the set frozen when the modal opens, a displayed count, friction scaled to blast radius, and no path from a selection to a first DELETE that skips confirmation. It must also be paced by [rate-governor](../rate-governor/requirements.md) R2, which names it among the writes it owns. What this requirement adds on top is the wording, because "the logs die and the Run lives" is a distinction no generic confirmation draws.

**Log deletion is a deletion, so every attempt must be recorded in [purge](../purge/requirements.md) R29's deletion log**, with `log` as the kind and the Run's id as the id, and R29's failure mode binds here too: a log that cannot be written stops the deletion. Deleting a Run's logs is irreversible and reclaims something nothing regenerates, which is R29's whole test. **R29 does not ride in on the route above, and that is why this paragraph exists.** This requirement routes through purge R4 to R9, which are selection and confirmation. R29 is execution, it sits outside that range, and an implementer reading only the citation would ship log deletion that keeps no record. R29's kind column is what keeps this distinct from deleting the Run itself: both write a line carrying the same id, and only the kind separates them, which is the same distinction this requirement's wording draws for the operator.

**R18.** Render an empty state (not a blank pane and not an error) when a Job's log has no content, whatever the cause: an in-progress Job, a Job that emitted no log content, or logs already deleted.

**R19.** Strip terminal control sequences (ANSI escapes) from log content rather than interpreting them, and never emit a raw control sequence to the terminal. R7's marker styling and R9's text-first distinctions carry the meaning, so nothing legible is lost by stripping, while interpreting arbitrary sequences in a pane the tool does not fully control risks corrupting the display.

**R20.** Render to a frame from held log content alone, with no live terminal and no network, and verify that frame with golden-file tests. The goldens must cover at minimum: the BOM stripped from the first line (R3), the 29-character timestamp prefix absent by default and byte-identically restored when toggled (R4), `##[group]`/`##[endgroup]` spans folded and labelled (R5), and `##[error]` and `##[warning]` lines styled apart from ordinary lines and from each other (R7). Every one of those is a byte-level transformation of text the API sent, applied to content the tool did not author and cannot predict. A golden compares exactly that, and it is the only check here that would notice a fold quietly swallowing a line or a prefix returning one character short.

**R21.** Provide a search within the open log: a case-insensitive find that marks matches and moves between them. A Job log has no upper bound on size, and where the folds (R5) do not structure it, search is the only navigation a long log offers. The search runs over the already-fetched content in memory and issues no request.

**R22.** Soft-wrap long lines by default, wrapping at the pane width rather than scrolling horizontally or truncating. A log is one column of text, and stripping the 29-character timestamp prefix (R4) already returns 29% of the width, so wrapping keeps every character visible without a horizontal scroll surface.

## Acceptance criteria

**AC1: One request per Job opened.** Opening a Job issues exactly one request for that Job's log and zero requests for the run-level archive. Opening a Run detail without opening a Job issues no log request at all.

**AC2: The measured log renders clean.** Rendering the measured 4,153-byte trivial Job log produces a view in which no line contains U+FEFF at any offset, 12 folds are present, 2 lines are styled as warnings, and the literal strings `##[group]`, `##[endgroup]` and `##[warning]` appear nowhere in the rendered view.

**AC3: Timestamps strip by default and restore exactly.** With the default timestamp setting, no rendered line begins with an ISO-8601 timestamp. Toggling timestamps on and diffing against the raw response shows the prefix restored character-for-character. Toggling twice returns to the stripped view.

**AC4: Legible without colour.** With `NO_COLOR` set, a snapshot of the same log still distinguishes the 2 warning lines from ordinary lines by text content alone.

**AC5: The archive is export-only.** The whole-Run export produces a zip on disk. No code path that renders a log requests the run-level archive, and no rendered Job or Step name is a substring of an archive filename.

**AC6: Deleting logs is not deleting the Run.** Deleting a Run's logs leaves that Run listable in the Feed. The confirmation text for deleting logs and the confirmation text for deleting a Run are different, and no single keystroke performs both.

**AC7: Empty state, indeterminate progress, no URL reuse.** A Job with no log content renders the empty state. A download in progress shows an indeterminate indicator and no percentage. Two successive fetches of the same Job's log issue two requests to the API endpoint and reuse no URL between them.

**AC8: Only the latest Attempt's logs.** A Run with more than one Attempt offers logs for the latest Attempt's Jobs only, and offers no path to a prior Attempt's logs.

**AC9: Goldens hold the rendered log.** Rendering the measured 4,153-byte Job log from held content, with no terminal and no network, reproduces the stored golden byte for byte. Separate goldens fix the default view (no U+FEFF, no timestamp prefix, 12 folds collapsed and labelled), the timestamps-on view, a fold expanded, and a log carrying `##[error]` and `##[warning]` lines styled apart from ordinary lines and from each other. Changing any of those transformations fails its golden.

**AC10: In-log search finds and moves between matches.** Opening a log and searching for a substring present on several lines marks every match and moves between them, and issues no request while doing so.

**AC11: Long lines wrap, not truncate.** A line wider than the pane wraps to the next row rather than being cut off or requiring horizontal scroll, and no character of it is hidden.

## Constraints

Every fact below was measured against the live API. Each one removed an option.

| Fact | Measured | Consequence |
|---|---|---|
| Per-Job log | `GET /repos/{o}/{r}/actions/jobs/{id}/logs` → 303 → `200 text/plain`. **4,153 bytes** for a trivial Job | One small request per Job opened → R1 |
| Byte-order mark | The first line carries a UTF-8 BOM | R3 |
| Timestamp prefix | **Every** line prefixed with a 29-char ISO timestamp (`2026-07-15T03:11:52.0835958Z `). **29% of the 100-column minimum** ([live-run-feed](../live-run-feed/requirements.md) R4a) | R4 |
| Marker density | One small Job's log: **12** `##[group]`, **12** `##[endgroup]`, **2** `##[warning]` | Markers are the norm, not an edge case → R5–R8 |
| Marker family | `group`, `endgroup`, `error`, `warning`, `notice`, `command`, `debug` | R8 |
| Run-level logs | `GET /repos/{o}/{r}/actions/runs/{id}/logs` → 303 → a **zip** | Export only → R2, R11 |
| Signed URL lifetime | ~1 minute | R13 |
| `HEAD` on the signed blob | **405** | Size is unknowable before download → R14 |
| Streaming | **No endpoint exists.** Content arrives on completion | Live tailing is out of scope, permanently → R15, and PRD *Out* |
| Prior Attempts | `/runs/{id}/attempts/1/jobs` → `total_count: 0` | Attempt is a badge, never a view → R16 |
| Repo permissions | `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request | Gating R17 costs nothing |
| Fine-grained PATs | Expose no `x-oauth-scopes` | No pre-flight check. A 403 can arrive despite `push: true` → R17 |
| Archived repos | Permanently read-only | Their logs can never be deleted |

The run-level archive contains both a flat whole-Job log and per-Step files:

```
0_no-response _ noResponse.txt              ← whole Job log, flat
no-response _ noResponse/system.txt
no-response _ noResponse/1_Set up job.txt   ← per-STEP files
no-response _ noResponse/3_Complete job.txt
```

Those per-Step files are exact Step boundaries, and the tool declines them anyway: getting them means downloading every Job's log to read one. R6's approximate folds are the accepted price.

Note also that archive filenames are **lossily sanitised**: a space-slash-space sequence in a Job name is replaced by space-underscore-space, which is how the `no-response _ noResponse` entries above are spelled. The replacement cannot be undone (a Job whose name genuinely contains the underscore form is indistinguishable from one that contained a slash), so R12 forbids deriving a Job name from a filename, and correlates by index instead.

## Open questions

**Resolved: strip them (R19).** Whether a given log carries ANSI is moot: R19 now strips terminal control sequences rather than interpreting them, so the display is safe whether they are present or not. A sampled real Job log was ANSI-clean, consistent with GitHub emitting `##[…]` workflow commands rather than raw ANSI, and R7 and R9 carry the styling in colour and in text of our own.

**Resolved by design: R18's empty state covers it, and opening is allowed (R15, R18).** Whether the per-Job endpoint returns partial text, an empty body or a 404 for an incomplete Job, R18 renders the same empty state and R15's explicit refetch updates it, so opening an in-progress Job's log is permitted and shows whatever is served. The exact behaviour stays unmeasured, since catching a Job mid-run is timing-dependent, and the pane is correct without it. PRD risk R5 closes on this basis, resolved by design rather than by measurement, and the PRD risk table is updated to match.

**Resolved: R18 handles both, and they are not distinguished (R18).** Re-requesting a log after `DELETE` yields either a 404 or an empty 200, both of which mean no content and both of which land in R18's empty state. Measuring which means a live log DELETE, which policy forbids, and the pane behaves identically either way.

**Resolved: yes, a minimal in-log search (R21).** A single Job log has no upper bound, and where the folds do not structure a long one, search is the main way to navigate it. R21 provides a case-insensitive find over the fetched content in memory, issuing no request.

**Resolved: soft-wrap by default (R22).** Long lines wrap at the pane width rather than scrolling horizontally or truncating, so no character is hidden. A log is one column of text, and stripping the 29-char prefix (R4, [live-run-feed](../live-run-feed/requirements.md) R4a) already returns 29% of the 100-column minimum, which keeps most lines within the pane before wrapping applies.

**Resolved: view session state, not a settings key (R4).** "Show timestamps" is mechanism, and settings hold intent (settings R2, R13's test), so the toggle lives in the view's session state rather than the config file. R4 now says so. Persisting it across sessions in the local-store, as window state is, stays a later option and needs no settings key either way.

**Resolved: one archive, as received (R11).** The export writes the single zip the API returns and does not unpack it, because unpacking assumes a directory the user did not ask for and they can unpack it themselves. R11 now says so.

## Related

- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). Why Status can be live while content cannot.
- [ADR-0009: Host-qualified repo identity](../../adr/0009-host-qualified-repo-identity.md). Log URLs are host-derived. 2.0.0 is github.com only.
- [run-detail](../run-detail/requirements.md) is the entry point. It owns Jobs, Steps and the Attempt badge.
- [purge](../purge/requirements.md) covers deleting a Run, which R17's log deletion is deliberately not. Its R4 to R9 graduated confirmation is what R17 routes through anyway.
- [rate-governor](../rate-governor/requirements.md). Its R2 paces R17's log deletion, and names it among the writes it owns.
- [run-lifecycle](../run-lifecycle/requirements.md) and [storage-reclamation](../storage-reclamation/requirements.md). Their R17s route through the same confirmation R17 does, for the same reason.
- [repo-discovery](../repo-discovery/requirements.md) supplies the free `permissions` and `archived` that gate R17.
- [settings](../settings/requirements.md). `NO_COLOR` and the intent-level settings boundary.
