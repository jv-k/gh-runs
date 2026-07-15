# Live Run Feed

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

The Feed is gh-runs' default view and primary surface: one live list of Runs spanning every repository on the account, updating as Runs are invoked anywhere, by anyone. It is where Runs are observed, filtered and selected, and it is the entry point to every operation performed on a Run.

## Requirements

### Layout and navigation

**R1.** The Feed must be the view presented on launch, with no intervening menu, splash or repository picker.

**R2.** The application must present exactly three top-level tabs (Runs, Workflows, Storage), with the Feed occupying Runs. Settings must be reachable from any tab and must not appear as a fourth peer tab.

**R3.** The Feed must present the owning repository as a column on every row and as a filter. It must not present repositories as tabs, sections or any other per-repository navigation: the reference account has 163 repositories and ~26 with Runs, and ~26 tabs is not navigable.

**R4.** The Feed must render Status and Conclusion as two distinct, separately labelled columns, and must never merge them into one column.

**R5.** The Feed must render an empty Conclusion cell for every Run whose Status is not `completed`, and must not substitute a Conclusion-like value in its place. Conclusion is null until Status reaches `completed`.

**R6.** The Feed must render each Status value (`queued`, `in_progress`, `completed`, `waiting`, `requested`, `pending`) distinguishably from the others, and must render any Status or Conclusion value it does not recognise verbatim rather than discarding it or collapsing it to "unknown".

**R7.** The Feed must ship exactly two keybinding profiles, Vim and Standard, and must offer no others. No binding in either profile may use Cmd, which terminals do not send, and neither profile may bind Ctrl+C to anything but quitting, because the terminal sends it as SIGINT.

### Ordering and stability

**R8.** The Feed must sort Runs by `run_started_at` descending. It must not sort by `created_at`: the two were identical on 8/8 measured normal Runs and diverged only on a re-run, which is precisely the event that should resurface.

**R9.** When a Run's Status or Conclusion changes, the Feed must repaint that Run's row in place, leaving its position and every other row's position unchanged.

**R10.** While the cursor is in the list, the Feed must defer every change that would move a row: insertion of a newly matching Run, eviction of a Run that no longer matches or no longer exists, and reordering caused by a re-run advancing `run_started_at`. Deferred changes must be applied only on idle or on explicit refresh.

**R11.** While changes are deferred, the Feed must display an affordance stating how many are pending ("3 new runs") and the key that applies them.

**R12.** When deferred changes are applied, the Feed must leave the cursor on the same Run ID it rested on beforehand, not on the same row index.

### Selection and safety

**R13.** The Feed must key selection by Run ID and must never key it by row index.

**R14.** The Feed must freeze the selected set at the moment the user confirms a destructive action, and must act on exactly that frozen set regardless of what arrives afterwards.

**R15.** The confirmation step for a destructive action must account for the entire frozen set, attributing each Run to its repository, including Runs not currently visible under the active filter or scroll position.

**R16.** When a selection spans repositories where the account lacks `push` or the repository is `archived`, the Feed must state the split before issuing any request ("3 of 47 selected Runs are in read-only repos and will be skipped"), and must then skip exactly those Runs.

**R17.** The Feed must gate every destructive action on `permissions.push && !archived` for the owning repository. Both fields arrive with the repository list at no additional request cost.

**R18.** The Feed must keep destructive actions disabled for any repository whose permission data has not yet arrived, and must not infer permission from the ability to list that repository's Runs.

**R19.** The Feed must treat the API as the final authority on permission: a 403 on a destructive request must be surfaced as a per-Run failure of that request, not as a gate defect. A 403 can arrive despite `push: true`.

**R20.** The Feed must display Runs from repositories the account cannot write to, and must keep them updating live at the same tiers as writable repositories. Watching CI on a repository you do not administer is a primary use case.

**R21.** The Feed must mark an archived repository as permanently read-only rather than as temporarily ungated. Its Runs can never be cleaned.

### Filtering and honest counts

**R22.** The Feed must apply its filters server-side, over branch, Status, Workflow, actor, event, creation date and commit (the set gh exposes as `-b/--branch`, `-s/--status`, `-w/--workflow`, `-u/--user`, `-e/--event`, `--created` and `-c/--commit`), with identical names and semantics. R23's Conclusion filter is the single exception, for the measured reason recorded there.

**R23.** The Feed's Status filter must remain permissive, accepting both Status and Conclusion values in one input, because gh's `-s/--status` does and compatibility is a stated requirement. The Feed must additionally offer a Conclusion filter, which gh has no equivalent for. **That filter is a client-side predicate over Runs the Feed already holds, and must never be sent as a query parameter.** No server-side conclusion parameter exists: measured, `?conclusion=failure` and `?conclusion=bogusvalue` each returned all 28,710 Runs, exactly as no filter did, because the API ignores the parameter rather than honouring or rejecting it. The predicate spends no Budget and is bounded by a window R24 already labels honestly. What the measurement forbids is a *count* derived from post-filtering a capped set. See [approvals](../approvals/requirements.md) R6, which states the same prohibition. Permissiveness must be confined to parsing that one input: every column, count and label the Feed renders must keep Status and Conclusion separate.

**R24.** When a filtered view's `total_count` exceeds the number of results the API will return, the Feed must label the view with the reachable count first and the claimed count second and marked approximate ("1,000 of ~18,260"), and must never present `total_count` alone.

**R25.** The Feed must not treat an empty page from a filtered listing as evidence that all matches were retrieved. Measured: page 11 of a filtered listing returns `[]`, with no error and no flag, while `total_count` reports 18,260.

**R26.** When a destructive action is invoked over a capped filtered view, the Feed must state that the action reaches only the newest 1,000 matches and must offer the Purge path, which crawls unfiltered. A filtered view reaches the newest matches, and "delete Runs older than 90 days" asks for the oldest.

### Liveness and Budget

**R27.** The Feed must surface a Run invoked elsewhere with no user interaction, within ~30s while nothing is in progress and within ~3s while something is.

**R28.** The Feed must consume ~0 primary rate limit while idle, by revalidating conditionally.

**R29.** The Feed must not display a Budget readout while consumption is nominal. It must display one only under pressure.

**R30.** On Budget exhaustion the Feed must pause live updates, state that it has paused, and state when updates resume ("resumes 14:32"). Where no resumption time is available it must still state that updates are paused. It must never continue presenting rows as live once revalidation has stopped.

### Approvals

**R31.** The Feed must expose Runs awaiting approval as a badge plus a saved filter over the Feed itself, and must not present them as a separate view. The badge must cover both cases, and they are carried by different fields: a fork-PR Run awaiting approval is Conclusion `action_required` with Status `completed`, and a Run awaiting a pending deployment is Status `waiting`. The predicate must read both fields and must never read Status alone ([approvals](../approvals/requirements.md) R2, R4).

### Cold start

**R32.** When launched inside a git repository whose remote resolves to github.com, the Feed must paint that repository's Runs first, from a single Run-listing request, without waiting on repository discovery or on any other repository's response.

**R33.** Once the local repository has painted, the Feed must probe the remaining repositories in the background and reveal their Runs progressively as they arrive, without blocking interaction and subject to R10.

**R34.** When launched outside a git repository, the Feed must fall back to progressive reveal across all discovered repositories, painting Runs as each repository responds.

**R35.** When launched inside a git repository whose remote resolves to a host other than github.com, the Feed must reject the host explicitly. It must not silently fall back to github.com or attribute Runs to the wrong host. Repository identity is host-qualified (`host/owner/name`) throughout.

### Seams

**R36.** The Feed must render to a frame from held state alone, with no live terminal and no network, and that frame must be verified by golden-file tests. The goldens must cover four things at minimum. Row rendering, with Status and Conclusion in their own cells and an empty Conclusion below `completed` (R4, R5, R6). R24's honest cap label ("1,000 of ~18,260"). The action states R17, R18 and R21 distinguish, which are offered, read-only, not yet known, and permanently read-only. And R11's deferred-insertion affordance ("3 new runs"). Those four are the claims this document argues hardest for, and each is a property of the painted frame rather than of the model behind it: a test over held state can prove Conclusion is null, and only a golden proves the cell is empty. They are also the four a refactor can undo without failing anything else here.

## Acceptance criteria

**AC1: Row stability.** With the cursor in the list, the Run ID at a given row index is identical between two consecutive frames unless the user moved the cursor, refreshed explicitly, changed the filter, or applied deferred changes. A poll result alone never changes it.

**AC2: Repaint in place.** With the cursor on row 5, a poll that transitions the Run at row 12 from `queued` to `in_progress` leaves every row index unchanged, updates row 12's Status cell to `in_progress`, and leaves its Conclusion cell empty.

**AC3: Deferred insertion.** With the cursor in the list, three Runs matching the active filter are invoked elsewhere. No row index changes and an affordance reads "3 new runs". After an explicit refresh the three Runs occupy the top rows in `run_started_at` descending order, and the cursor rests on the same Run ID as before.

**AC4: A re-run repaints now and moves later.** With the cursor in the list, a visible Run is re-run elsewhere: its Status returns to `queued`, its Conclusion returns to null, its `run_attempt` increments. Its row repaints in place immediately and does not move. The reorder is applied only on idle or explicit refresh.

**AC5: Selection survives repaint.** With 47 Runs selected, a poll repaints 12 of them with new Status values and defers 3 insertions. The selected set is still exactly the same 47 Run IDs.

**AC6: Frozen confirm set.** Runs A, B and C are selected and deletion is confirmed. Run D arrives and matches the filter before the first request is issued. The executed set is exactly {A, B, C}.

**AC7: Mixed-permission warning.** 47 Runs are selected, 3 of which belong to repositories with `push: false` or `archived: true`. Before any request is issued the confirmation states "3 of 47 selected Runs are in read-only repos and will be skipped". Exactly 44 delete requests are issued.

**AC8: Read-only repositories stay live.** A repository with `push: false` lists its Runs, and a Run invoked in it repaints on Status change within R27's timings. Its destructive actions are unavailable.

**AC9: Permission not yet known.** Before the repository list has returned, no destructive action is offered for any repository, including one already painted by R32's fast path.

**AC10: Honest cap.** A filtered view whose `total_count` is 18260 and whose reachable results number 1,000 renders "1,000 of ~18,260". No surface renders 18,260 as the count of what is shown or actionable.

**AC11: An empty page is not completeness.** A filtered listing returning `[]` at result 1,001 while reporting `total_count: 18260` leaves the view labelled capped, not complete.

**AC12: Status and Conclusion never merged.** No column, label, badge or count renders a Status value and a Conclusion value in the same field. A Run with Status `in_progress` renders no Conclusion. The sole place both are accepted is R23's permissive filter input.

**AC13: Idle costs nothing.** Across a steady-state interval in which no Run changes, the primary rate limit's `used` counter does not advance while the Feed polls.

**AC14: Budget silence.** With consumption nominal, no Budget readout is rendered on any surface.

**AC15: Exhaustion is explicit.** At Budget exhaustion the Feed stops updating and states both that it has paused and when it resumes ("resumes 14:32"). No row is presented as live after that point.

**AC16: Cold start inside a repository.** Launched inside a git repository, the Feed paints that repository's Runs within 1s, having issued exactly one Run-listing request. The other repositories' Runs appear afterwards with no user interaction.

**AC17: Unsupported host.** Launched inside a git repository whose remote is a GHES host, the Feed states the host is unsupported and lists no Runs under that repository's name.

**AC18: Keybindings.** Exactly two profiles are selectable. No binding in either uses Cmd. Ctrl+C quits in both.

**AC19: Goldens hold the Feed's frame.** Rendering a recorded frame from held state, with no terminal and no network, reproduces the stored golden byte for byte. Four separate goldens fix the cases R36 names. A row at Status `in_progress` with an empty Conclusion cell, alongside one at `completed` carrying its Conclusion. A capped view labelled "1,000 of ~18,260", nowhere carrying a bare 18,260. The same row rendered with its destructive action offered, read-only, not yet known, and permanently read-only, each visibly different from the others. A deferred state whose affordance reads "3 new runs". Changing any of them fails its golden.

## Constraints

**No cross-repository Run query exists.** Not in REST. Not in GraphQL, where `WorkflowRun` is reachable only via `CheckSuite` and lacks Status and Conclusion entirely. Not in `search`, which does not cover Runs. The Feed is therefore one request per repository, merged, sorted and filtered client-side, across the ~26 repositories with Runs out of the reference account's 163. gh-dash's "section = search query" model is unavailable: a section here can only be a set of repositories plus client-side filters.

**Conditional requests are what make liveness affordable.** Measured by interleaving unconditional and conditional requests against one endpoint, `used` advanced by exactly one per round (120 → 121 → 122) and that one belonged entirely to the 200. The 304s cost nothing. Polling ~26 repositories every few seconds is ~3,600 requests/hour at ~0 primary budget, so the binding constraint is the secondary limit (~900 points/min, GET = 1 point) rather than the primary one. We assume conservatively that 304s do count against the secondary limit (PRD risk R4). go-gh's own client turned out to be TTL-only and never revalidates (PRD risk R2, resolved), so the 304s above are ours to send from a transport of our own rather than the client's to provide. That is a build requirement on [local-store](../local-store/requirements.md) R19, and it changes nothing in this feature.

**Any filter caps listing at 1,000 results, silently.** Measured on `cli/cli`: `total_count` unfiltered 28,707. `total_count` at `status=success` 18,260. Page 11 unfiltered returns 100 Runs. Page 11 filtered returns 0, with no error and no flag. `total_count` reports matches, not reachable matches, and the gap is 17,260. The cap is per repository, which is what makes "all Runs" a fuzzy notion in a merged Feed. Runs sort newest-first, so a filtered view reaches only the newest 1,000 matches, the opposite end from the one a Purge is usually asked for.

**`created_at` and `run_started_at` were identical on 8/8 measured normal Runs and 3 hours apart on the one measured re-run.** That is the entire basis for R8: `run_started_at` is as stable as `created_at` in every ordinary case, and diverges exactly when a Run should resurface.

**Repository permissions ride along free.** `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request, so R17's gating costs nothing. But fine-grained PATs expose no scopes (`x-oauth-scopes` exists only for classic tokens), so pre-flight permission checks are impossible for them and R19 must hold.

**Conclusion is null until Status reaches `completed`.** Status and Conclusion are separate fields, and conflating them is the defining bug of the tools that came before this one.

## Open questions

1. **Resolved: Conclusion carries it. A fork-PR Run awaiting approval is `status=completed`, `conclusion=action_required`.** Measured on `cli/cli` and `home-assistant/core`. The premise that broke the reasoning was "a Run awaiting fork-PR approval has not completed". It has: the Run stopped, and its outcome is "needs action", which is precisely why it carries a Conclusion at all. All three canon statements hold together, and `action_required` is a Conclusion ([CONTEXT.md](../../CONTEXT.md)). R31's badge must therefore match on [approvals](../approvals/requirements.md) R2's two-field predicate, because a Status-only predicate misses every fork-PR case. On the server-side half: each kind is independently reachable through the permissive `status` parameter, which matches either field, so neither is stranded. The predicate spans both kinds and the parameter takes one value at a time, so a server-side form would cost a request per kind. Approvals R5 evaluates it client-side instead, because the Feed already holds both fields and the badge should cost no Budget.

2. **Resolved: a Conclusion filter is not server-side. There is no such parameter, and the CLI's `--conclusion` flag is dropped.** Measured: `?conclusion=failure` and `?conclusion=bogusvalue` both returned all 28,710 Runs, exactly as no filter did. The API ignores the parameter rather than honouring or rejecting it. [cli-surface](../cli-surface/requirements.md) R5 drops the flag on that basis, and [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) records the correction. R23's Conclusion filter is unaffected and stays: it is a client-side predicate over Runs the Feed already holds, spends no Budget, and is bounded by a window R24 already labels honestly. What the measurement forbids is a *count* derived from post-filtering a capped set, which is the CLI's failure mode, not the Feed's.

3. **Resolved for `queued`: `run_started_at` is populated, and R8's sort spine holds.** Measured against `home-assistant/core`, three Runs at Status `queued`, all three carrying a non-null `run_started_at`. R8 needs no fallback for the common case and the newest Runs sort correctly. Two details worth keeping. On two of the three, `run_started_at` equalled `created_at` exactly. On the third it was **37 minutes later than `created_at` while the Run was still `queued`**, so the field is not "when it began running" and must not be read as a start time. It is a sort key, which is all R8 asks of it. **Still unmeasured: `waiting`, `requested` and `pending`**, none of which were present at measurement time. R8 should carry a null-safe fallback to `created_at` for those three rather than assume, at the cost of one line.

4. **Does a re-run always advance `run_started_at`, and does re-running failed Jobs only behave the same way?** n = 1. R8's rationale requires the divergence to be *forward*. A re-run whose `run_started_at` stayed put would sink rather than surface. UNKNOWN whether this generalises, and the PRD scopes "re-run failed Jobs" as a separate operation whose effect on `run_started_at` has not been observed.

5. **What counts as "idle" for applying deferred changes?** R10 defers while the cursor is in the list and applies "on idle". Undecided whether idle means a quiet period since the last keystroke, the cursor leaving the list, the cursor returning to the top, or focus moving to another pane. Each gives a different answer to "the user is reading row 40 and has not typed for five seconds".

6. **Does the affordance count anything but insertions, and does the user's own action defer?** R11's copy says "3 new runs", but a deferred re-run reorder (AC4) is not a new Run and an eviction is not either. Separately, when the user re-runs a Run from the Feed the reorder is expected rather than surprising. Yet it still moves a row under their cursor. Both undecided.

7. **Does changing the filter clear the selection?** Selection is ID-keyed (R13), so it survives a filter change invisibly, and R15 requires the confirmation to account for Runs that are not visible. Undecided whether that is the right contract or whether a filter change should clear selection outright. Success criterion 4 can be argued either way.

8. **How is the cap labelled in a merged view?** "1,000 of ~18,260" is a per-repository label and the cap is per repository. Undecided what a Feed spanning ~26 repositories, an arbitrary subset of them capped, says in one line without lying.

9. **Is a concrete resumption time always derivable?** R30 requires "resumes 14:32". ADR-0007 cites `Retry-After` on secondary-limit responses. The canon does not establish that a reset timestamp is available for primary exhaustion. **UNKNOWN**, which is why R30 carries a fallback.

10. **What happens when the local repository is not in the discovered set?** R32's fast path resolves the repository from the git remote and needs no discovery, but its permissions come from `/user/repos`, where a clone the account does not own may never appear. Undecided whether such a repository joins the Feed ad-hoc, and what learning its permissions costs.

## Related

- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md)
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md)
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Repo identity is host-qualified](../../adr/0009-host-qualified-repo-identity.md)
- Sibling features: [run-detail](../run-detail/requirements.md), [purge](../purge/requirements.md), [approvals](../approvals/requirements.md), [run-lifecycle](../run-lifecycle/requirements.md), [repo-discovery](../repo-discovery/requirements.md), [polling-scheduler](../polling-scheduler/requirements.md), [rate-governor](../rate-governor/requirements.md), [cli-surface](../cli-surface/requirements.md), [settings](../settings/requirements.md)
