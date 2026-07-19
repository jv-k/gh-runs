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

**R4a.** The Feed's minimum width is **100 columns**. Below it the Feed must state that the terminal is too narrow, name the width it needs, and paint no rows. Four columns are fixed and must never shrink, and two flex above their floors:

| Column | Width | Fixed? | Basis |
|---|---|---|---|
| Status | 11 | Fixed | `in_progress`, the longest of [CONTEXT.md](../../CONTEXT.md)'s six |
| Conclusion | 15 | Fixed | `action_required` and `startup_failure`, the longest of its nine |
| Run ID | 11 | Fixed | Measured. `cli/cli` serves 11-digit ids |
| `run_started_at` | 20 | Fixed | Measured, `2026-07-15T16:39:00Z`. R10 in [settings](../settings/requirements.md) makes relative narrower, never wider |
| Repository | 20 floor | Flexes | `home-assistant/core` is 19, `kubernetes/kubernetes` is 21 |
| Workflow name | 18 floor | Flexes | Chosen. It is what is left |

**The arithmetic is where the number comes from, and it refutes 80 without needing the two floors at all.** The four fixed columns sum to **57**, and five single-space separators make **62**. An 80-column terminal has **18 characters left for the repository and the Workflow name together**, and `home-assistant/core` is 19. So 80 cannot hold the repository column alone, before a character of Workflow name. The floors then give 57 + 5 + 20 + 18 = **100**, and that is the whole derivation. **The only "80 columns" in this canon is [log-viewer](../log-viewer/requirements.md)'s Purpose line, and it contradicts this Feed's own mandated row.** That line is about a Job log, which is one text column, and it does not reach here.

**The row is mandated rather than chosen, which is why nothing can be dropped to fit.** [purge](../purge/requirements.md) R30 fixes all six for the inspect view ("the Feed's columns and no new ones") and forbids new ones. Dropping Conclusion or Status to fit a narrow pane would breach R4 outright, in a tool whose stated defining bug is conflating those two fields. There is no honest subset, so the Feed says so and waits. Widening a pane is one action, and it is a better outcome than a Feed that quietly stops showing the column this product exists to separate.

**20 and 18 are chosen, not measured**, exactly as [purge](../purge/requirements.md) R7's 50 and R8's 500 are. What is measured is the 57, and the 57 is what settles 80.

**R5.** The Feed must render an empty Conclusion cell for every Run whose Status is not `completed`, and must not substitute a Conclusion-like value in its place. Conclusion is null until Status reaches `completed`.

**R6.** The Feed must render each Status value (`queued`, `in_progress`, `completed`, `waiting`, `requested`, `pending`) distinguishably from the others, and must render any Status or Conclusion value it does not recognise verbatim rather than discarding it or collapsing it to "unknown".

**R6a.** R6's "verbatim" governs the value, never the column. **No value in either enum can ever be truncated**, because R4a sizes both columns to the longest member each enum has. Only a value this tool does not recognise can exceed its column, and only because the API added one. Such a value must be rendered as far as the column reaches, marked as truncated, and must never be replaced, discarded, or collapsed to "unknown". The columns must not resize to fit it.

**Truncation is not what R6 forbids.** R6 names two failure modes and both are substitution: discarding a value, and collapsing it to a word the tool made up. A truncated value is still that value, still distinct from every value the tool knows, and still visibly not "unknown". What R6 protects is the tool's refusal to pretend it understands the API better than the API does, and a truncation marker says the opposite of that pretence.

**The columns must not resize, and R9 and R10 are the reason.** A column sized to the widest value present makes every row's layout a function of every other row's content, so one arriving Run carrying an unrecognised 30-character Conclusion reflows the entire table. R9 requires a Status change to repaint one row and leave every other row alone. R10 defers anything that would move a row while the cursor is in the list. A reflow moves all of them, and it arrives from a poll, which is exactly the event AC1 says may never change what the cursor is resting on. **Fixed columns are what make R9 and R10 implementable**, and truncating a value the API invented after we shipped is the price. It is a good price: the alternative is a Feed whose layout an unknown third party controls.

**R7.** The Feed must ship exactly two keybinding profiles, Vim and Standard, and must offer no others. No binding in either profile may use Cmd, which terminals do not send, and neither profile may bind Ctrl+C to anything but quitting, because the terminal sends it as SIGINT.

**R7a.** Both profiles must be **declared as one registry**, in `internal/keys` ([ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md)), as `key.Binding` values. Every binding in the product must come from it, and no view may match a key literal of its own. **AC18 is why this is a requirement rather than a layout preference.** "No binding in either profile uses Cmd" can only be asserted over a declared set: a test cannot enumerate the keys a `switch` statement scattered across nine packages happens to match, so without a registry AC18 is a sentence nothing can check, and R7's second clause is a promise rather than a property.

**The two profiles differ on motion, and nowhere else.** Vim and Standard disagree about how to move a cursor. They have no disagreement about deleting a Run, because vim has no opinion on that. Every binding below that is not a motion is therefore identical in both profiles, deliberately, and a profile that forked them would be inventing a distinction for symmetry.

| Motion | Vim | Standard | Required by |
|---|---|---|---|
| Row up, row down | `k`, `j` | `up`, `down` | run-detail AC1 arrow-keys 100 rows |
| Page up, page down | `ctrl+b`, `ctrl+f` | `pgup`, `pgdown` | R30 in [purge](../purge/requirements.md) |
| **First row, last row** | `g`, `G` | `home`, `end` | **[purge](../purge/requirements.md) R30 depends on this outright.** "Reach both ends" is how an operator checks a date filter, and the oldest Run in a frozen set is its last row |

| Everything else | Both profiles | Required by |
|---|---|---|
| Next tab, previous tab | `tab`, `shift+tab` | R2's three tabs |
| Tab by position | `1`, `2`, `3` | R2. Three tabs is few enough to address directly |
| Settings | `,` | R2. Reachable from any tab, and never a fourth tab |
| Toggle row selection | `space` | R4 in [purge](../purge/requirements.md) |
| Apply deferred changes, refresh | `r` | R10 and R11. R11's affordance must name this key |
| Open Run detail | `enter` | [BUILD-ORDER](../../BUILD-ORDER.md) stage 8 |
| Filter | `/` | R22, R23 |
| Help | `?` | `bubbles/help` renders the registry |
| Quit | `q`, `ctrl+c` | R7. Ctrl+C quits in both and binds to nothing else |

| Confirm modal | Both profiles | Required by |
|---|---|---|
| Confirm below the threshold | `y` | R7 and AC6 in [purge](../purge/requirements.md) |
| Abort | `n`, `esc` | AC6 there |
| Abort on the default | `enter` | AC6 there. The default is no |
| At or above the threshold | the exact count, typed | R7 there. **`y` must not start it** |
| Inspect the frozen set | `v` | R30 there. The modal must name this key on its face |

**Feature-local bindings join this registry rather than escaping it.** Log deletion needs a key distinct from Run deletion ([log-viewer](../log-viewer/requirements.md) R17), a Purge summary needs one that retries only the recorded failures ([purge](../purge/requirements.md) R22), and the log view needs its fold and timestamp toggles ([log-viewer](../log-viewer/requirements.md) R4, R5). Each is its own feature's to name and every one of them is declared here. A binding that lives anywhere else is outside AC18's reach, which is the only thing this requirement is protecting.

**Two spellings are measured, and both are traps.** A space press arrives as `tea.KeyPressMsg` whose `String()` is **`"space"`**, so `key.WithKeys(" ")` matches nothing: verified at bubbletea v2.0.8, where the v1 idiom `case " "` is now `case "space"`. And `tea.KeyMsg` is an interface in v2 rather than a struct, so a press is a `tea.KeyPressMsg` and a type switch on `tea.KeyMsg` catches releases too. `key.Matches[Key fmt.Stringer](k Key, b ...Binding) bool` takes the press directly, which `tea.KeyPressMsg` satisfies.

### Ordering and stability

**R8.** The Feed must sort Runs by `run_started_at` descending. It must not sort by `created_at`: the two were identical on 8/8 measured normal Runs and diverged only on a re-run, which is precisely the event that should resurface. Where `run_started_at` is null the Feed must fall back to `created_at`. Only Status `requested` may ever need that fallback, and no instance of one exists to check against (open question 3).

**R9.** When a Run's Status or Conclusion changes, the Feed must repaint that Run's row in place, leaving its position and every other row's position unchanged.

**R10.** While the cursor is in the list, the Feed must defer every change that would move a row: insertion of a newly matching Run, eviction of a Run that no longer matches or no longer exists, and reordering caused by a re-run advancing `run_started_at`. Deferred changes must be applied only on idle or on explicit refresh. **Idle means focus leaving the list** (switching tab, opening a pane, or entering the filter input) and nothing else. A timer must never apply them: a user who has stopped typing is reading, not idle. Changes the user's own action caused defer uniformly, so a re-run invoked from the Feed repaints its row in place at once (AC4) and its reorder waits with the rest.

**R11.** While changes are deferred, the Feed must display an affordance stating what is pending and the key that applies it. The affordance must count every kind of deferred change, each under its own word: insertions as new ("3 new runs"), evictions as removed, reorders as moved. The kinds must never fold into one number, and the insertion-only case must read exactly "3 new runs", the copy R36's golden fixes. Silence about an eviction would leave the view stale in a way the user cannot see, in a tool whose brand is honest counts (R24).

**R12.** When deferred changes are applied, the Feed must leave the cursor on the same Run ID it rested on beforehand, not on the same row index.

### Selection and safety

**R13.** The Feed must key selection by Run ID and must never key it by row index.

**R13a.** The selection must survive a filter change. Scroll already hides selected Runs without clearing them, and a boundary that cleared on filter but not on scroll would sit in an arbitrary place. Cross-filter accumulation is a supported workflow: filter to one repository's failures, select, filter to another's, select more, delete once. Whenever the selection is non-empty the Feed must render a persistent selected-count ("47 selected"), independent of the active filter and scroll position, so an off-filter selection is never invisible. R15's attributed confirmation remains the final accounting.

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

**R23.** The Feed's Status filter must remain permissive, accepting both Status and Conclusion values in one input, because gh's `-s/--status` does and compatibility is a stated requirement. The Feed must additionally offer a Conclusion filter, which gh has no equivalent for. **That filter is a client-side predicate over Runs the Feed already holds, and must never be sent as a query parameter.** No server-side conclusion parameter exists: measured, `?conclusion=failure` and `?conclusion=bogusvalue` each returned every Run in the repository, exactly as no filter did, because the API ignores the parameter rather than honouring or rejecting it. The predicate spends no Budget and is bounded by a window R24 already labels honestly. What the measurement forbids is a *count* derived from post-filtering a capped set. See [approvals](../approvals/requirements.md) R6, which states the same prohibition. Permissiveness must be confined to parsing that one input: every column, count and label the Feed renders must keep Status and Conclusion separate.

**R24.** When a filtered view's `total_count` exceeds the number of results the API will return, the Feed must label the view with the reachable count first and the claimed count second and marked approximate ("1,000 of ~18,258"), and must never present `total_count` alone.

**In a merged view the label sums, and names where the gap lives.** The cap is per repository, so a merged filtered view must carry the summed reachable count first, the summed claimed count second and marked approximate, and the number of capped repositories ("5,320 of ~24,180, 3 repos capped"). Every number is real: the reachable sum is what the Feed holds, the claimed sum is what the responses reported (each filtered `RunsFetched` carries its repository's `total_count`, [ADR-0015](../../adr/0015-the-async-model.md)), and a repository is capped exactly when its claimed count exceeds its reachable one. The label degenerates to the single-repository form under a one-repository filter, and to no label when nothing is capped. An unfiltered view has no cap and never carries the label.

**R25.** The Feed must not treat an empty page from a filtered listing as evidence that all matches were retrieved. Measured: page 11 of a filtered listing returns `[]`, with no error and no flag, while `total_count` reports 18,258.

**R26.** When a destructive action is invoked over a capped filtered view, the Feed must state that the action reaches only the newest 1,000 matches and must offer the Purge path, which crawls unfiltered. A filtered view reaches the newest matches, and "delete Runs older than 90 days" asks for the oldest.

### Liveness and Budget

**R27.** The Feed must surface a Run invoked elsewhere with no user interaction, within ~30s while nothing is in progress and within ~3s while something is.

**R28.** The Feed must consume ~0 primary rate limit while idle, by revalidating conditionally.

**R29.** The Feed must not display a Budget readout while consumption is nominal. It must display one only under pressure. **Pressure is the [rate-governor](../rate-governor/requirements.md)'s to decide and never this Feed's**, and its R8a defines it as projected exhaustion (`remaining / burn < time_to_reset`) rather than as a percentage of the limit. The Feed reads the flag off the Budget Readout. It must not re-derive the condition from `remaining`, which R1 there already forbids by making the governor the single authority, and which would put a second pressure rule in the one place a user would see both.

**R30.** On Budget exhaustion the Feed must pause live updates, state that it has paused, and state when updates resume ("resumes 14:32"). Where no resumption time is available it must still state that updates are paused. It must never continue presenting rows as live once revalidation has stopped.

### Approvals

**R31.** The Feed must expose Runs awaiting approval as a badge plus a saved filter over the Feed itself, and must not present them as a separate view. The badge must cover both cases, and they are carried by different fields: a fork-PR Run awaiting approval is Conclusion `action_required` with Status `completed`, and a Run awaiting a pending deployment is Status `waiting`. The predicate must read both fields and must never read Status alone ([approvals](../approvals/requirements.md) R2, R4).

### Cold start

**R32.** When launched inside a git repository whose remote resolves to github.com, the Feed must paint that repository's Runs first, from a single Run-listing request, without waiting on repository discovery or on any other repository's response.

**R33.** Once the local repository has painted, the Feed must probe the remaining repositories in the background and reveal their Runs progressively as they arrive, without blocking interaction and subject to R10.

**R34.** When launched outside a git repository, the Feed must fall back to progressive reveal across all discovered repositories, painting Runs as each repository responds.

**R35.** When launched inside a git repository whose remote resolves to a host other than github.com, the Feed must reject the host explicitly. It must not silently fall back to github.com or attribute Runs to the wrong host. Repository identity is host-qualified (`host/owner/name`) throughout.

### Seams

**R36.** The Feed must render to a frame from held state alone, with no live terminal and no network, and that frame must be verified by golden-file tests. The goldens must cover four things at minimum. Row rendering, with Status and Conclusion in their own cells and an empty Conclusion below `completed` (R4, R5, R6). R24's honest cap label ("1,000 of ~18,258"). The action states R17, R18 and R21 distinguish, which are offered, read-only, not yet known, and permanently read-only. And R11's deferred-insertion affordance ("3 new runs"). Those four are the claims this document argues hardest for, and each is a property of the painted frame rather than of the model behind it: a test over held state can prove Conclusion is null, and only a golden proves the cell is empty. They are also the four a refactor can undo without failing anything else here.

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

**AC10: Honest cap.** A filtered view whose `total_count` is 18258 and whose reachable results number 1,000 renders "1,000 of ~18,258". No surface renders 18,258 as the count of what is shown or actionable.

**AC11: An empty page is not completeness.** A filtered listing returning `[]` at result 1,001 while reporting `total_count: 18258` leaves the view labelled capped, not complete.

**AC12: Status and Conclusion never merged.** No column, label, badge or count renders a Status value and a Conclusion value in the same field. A Run with Status `in_progress` renders no Conclusion. The sole place both are accepted is R23's permissive filter input.

**AC13: Idle costs nothing.** Across a steady-state interval in which no Run changes, the primary rate limit's `used` counter does not advance while the Feed polls.

**AC14: Budget silence.** With consumption nominal, no Budget readout is rendered on any surface.

**AC15: Exhaustion is explicit.** At Budget exhaustion the Feed stops updating and states both that it has paused and when it resumes ("resumes 14:32"). No row is presented as live after that point.

**AC16: Cold start inside a repository.** Launched inside a git repository, the Feed paints that repository's Runs within 1s, having issued exactly one Run-listing request. The other repositories' Runs appear afterwards with no user interaction.

**AC17: Unsupported host.** Launched inside a git repository whose remote is a GHES host, the Feed states the host is unsupported and lists no Runs under that repository's name.

**AC18: Keybindings.** Exactly two profiles are selectable. **Enumerating R7a's registry**, no binding in either profile carries a key naming Cmd (`cmd+`, `super+`, `meta+`), and `ctrl+c` appears in both profiles bound to quitting and to nothing else. R7a's motion rows resolve to `k`/`j` under Vim and `up`/`down` under Standard, and both profiles reach the first and last row (`g`/`G`, `home`/`end`), which [purge](../purge/requirements.md) R30 requires. Every binding the product matches on is drawn from the registry, and a view matching a key literal of its own fails this criterion.

**The enumeration is the criterion, and an earlier form of it had none.** "No binding in either uses Cmd" is a claim about a set, and this criterion named no set. Against nine packages each free to `switch` on a key string, it asserted a property of code nothing could enumerate, so it would have passed by being unwritten. R7a's registry is what turns it into a test that can fail.

**AC19: Goldens hold the Feed's frame.** Rendering a recorded frame from held state, with no terminal and no network, reproduces the stored golden byte for byte. Four separate goldens fix the cases R36 names. A row at Status `in_progress` with an empty Conclusion cell, alongside one at `completed` carrying its Conclusion. A capped view labelled "1,000 of ~18,258", nowhere carrying a bare 18,258. The same row rendered with its destructive action offered, read-only, not yet known, and permanently read-only, each visibly different from the others. A deferred state whose affordance reads "3 new runs". Changing any of them fails its golden. Every golden here is rendered at R4a's 100 columns ([ADR-0013](../../adr/0013-dependency-pins.md) fixes the pipeline).

**AC20: The mandated row fits, and a narrow terminal is refused rather than abridged.** At 100 columns a row carrying `action_required` and `in_progress` renders both in full, in their own cells, alongside its repository, Run ID, Workflow name and `run_started_at`. At 99 columns the Feed paints no rows and states the width it needs. No width causes a Status or Conclusion value in either enum to truncate, and no width merges the two columns or drops one (R4, R4a).

**AC21: An unrecognised value truncates and is never renamed.** Given a Run whose Conclusion is a 30-character value absent from [CONTEXT.md](../../CONTEXT.md)'s nine, the cell renders the value's leading characters with a truncation marker, renders neither "unknown" nor an empty cell, and **every other row's layout is unchanged**. A poll delivering that Run does not reflow the table (R6, R6a, R9, R10).

## Constraints

**No cross-repository Run query exists.** Not in REST. Not in GraphQL, where `WorkflowRun` is reachable only via `CheckSuite` and lacks Status and Conclusion entirely. Not in `search`, which does not cover Runs. The Feed is therefore one request per repository, merged, sorted and filtered client-side, across the ~26 repositories with Runs out of the reference account's 163. gh-dash's "section = search query" model is unavailable: a section here can only be a set of repositories plus client-side filters.

**Conditional requests are what make liveness affordable.** Measured by interleaving unconditional and conditional requests against one endpoint, `used` advanced by exactly one per round (120 → 121 → 122) and that one belonged entirely to the 200. The 304s cost nothing. Polling ~26 repositories every few seconds is ~3,600 requests/hour at ~0 primary budget, so the binding constraint is the secondary limit (~900 points/min, GET = 1 point) rather than the primary one. We assume conservatively that 304s do count against the secondary limit (PRD risk R4). go-gh's own client turned out to be TTL-only and never revalidates (PRD risk R2, resolved), so the 304s above are ours to send from a transport of our own rather than the client's to provide. That is a build requirement on [local-store](../local-store/requirements.md) R19, and it changes nothing in this feature.

**Any filter caps listing at 1,000 results, silently.** Measured on `cli/cli`: `total_count` unfiltered 28,694. `total_count` at `status=success` 18,258. Page 11 unfiltered returns 100 Runs. Page 11 filtered returns 0, with no error and no flag. `total_count` reports matches, not reachable matches, and the gap is 17,258. Those totals are point-in-time and drift ([PRD](../../PRD.md)), so no golden may hardcode one. The cap is per repository, which is what makes "all Runs" a fuzzy notion in a merged Feed. Runs sort newest-first, so a filtered view reaches only the newest 1,000 matches, the opposite end from the one a Purge is usually asked for.

**`created_at` and `run_started_at` were identical on 8/8 measured normal Runs and 3 hours apart on the one measured re-run.** That is the entire basis for R8: `run_started_at` is as stable as `created_at` in every ordinary case, and diverges exactly when a Run should resurface.

**Repository permissions ride along free.** `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request, so R17's gating costs nothing. But fine-grained PATs expose no scopes (`x-oauth-scopes` exists only for classic tokens), so pre-flight permission checks are impossible for them and R19 must hold.

**Conclusion is null until Status reaches `completed`.** Status and Conclusion are separate fields, and conflating them is the defining bug of the tools that came before this one.

## Open questions

1. **Resolved: Conclusion carries it. A fork-PR Run awaiting approval is `status=completed`, `conclusion=action_required`.** Measured on `cli/cli` and `home-assistant/core`. The premise that broke the reasoning was "a Run awaiting fork-PR approval has not completed". It has: the Run stopped, and its outcome is "needs action", which is precisely why it carries a Conclusion at all. All three canon statements hold together, and `action_required` is a Conclusion ([CONTEXT.md](../../CONTEXT.md)). R31's badge must therefore match on [approvals](../approvals/requirements.md) R2's two-field predicate, because a Status-only predicate misses every fork-PR case. On the server-side half: each kind is independently reachable through the permissive `status` parameter, which matches either field, so neither is stranded. The predicate spans both kinds and the parameter takes one value at a time, so a server-side form would cost a request per kind. Approvals R5 evaluates it client-side instead, because the Feed already holds both fields and the badge should cost no Budget.

2. **Resolved: a Conclusion filter is not server-side. There is no such parameter, and the CLI's `--conclusion` flag is dropped.** Measured: `?conclusion=failure` and `?conclusion=bogusvalue` both returned every Run in the repository (28,710 at that measurement), exactly as no filter did. The API ignores the parameter rather than honouring or rejecting it. [cli-surface](../cli-surface/requirements.md) R5 drops the flag on that basis, and [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) records the correction. R23's Conclusion filter is unaffected and stays: it is a client-side predicate over Runs the Feed already holds, spends no Budget, and is bounded by a window R24 already labels honestly. What the measurement forbids is a *count* derived from post-filtering a capped set, which is the CLI's failure mode, not the Feed's.

3. **Resolved: `run_started_at` is populated on every Status a sample exists for, and R8's sort spine holds.** Measured across three Statuses. `queued`, on `home-assistant/core`: 3 of 3 non-null. `waiting`, on `pytorch/pytorch`: **9 of 9** non-null. `pending`, on `grafana/grafana`: **5 of 5** non-null. On every `waiting` and `pending` Run the field equalled `created_at` exactly.

    **`requested` has no sample anywhere: zero instances across ~88 repositories.** It is unmeasurable rather than merely unmeasured, and that zero is genuine absence rather than a filter the API quietly dropped. `status=bogusvalue` returns `total_count: 0` instead of everything, which proves the `status` parameter is honoured. Contrast `conclusion`, which is ignored and returns every Run in the repository (open question 2). A 0 from a parameter the server honours means the Runs are not there.

    So R8's null-safe fallback to `created_at` is needed for `requested` alone, and it costs one line. R8 now carries it.

    Two details worth keeping. On two of the three `queued` Runs, `run_started_at` equalled `created_at`. On the third it was **37 minutes later than `created_at` while the Run was still `queued`**, so the field is not "when it began running" and must not be read as a start time. It is a sort key, which is all R8 asks of it.

4. **Resolved: a re-run advances `run_started_at` forward, and re-running failed Jobs only does too.** Measured on three further re-runs (two on `home-assistant/core`, one on `microsoft/vscode`), all at `run_attempt: 2`. On every one, attempt 1's `run_started_at` equalled `created_at`, and the re-run moved `run_started_at` forward, by 17 minutes to over an hour. The attempt-jobs listing distinguishes the kinds: two of the three carried most of attempt 1's successful Jobs over unchanged (31 of 38 on one, 17 of 18 on the other, each started before attempt 2 began) and re-executed only the rest, so both were failed-Jobs-only re-runs, and the field advanced anyway. n is now 4 of 4 forward. R8's rationale holds, and the PRD's separately scoped "re-run failed Jobs" operation resurfaces a Run exactly as a full re-run does.

5. **Resolved: idle means focus leaving the list, and nothing else.** R10 now carries the definition. A quiet-period timer applies a reorder while the user is reading row 40, which is precisely the surprise R10 exists to prevent: a user who has stopped typing is reading, not idle. The cursor returning to the top fails the same way, because insertions above the cursor's Run still move it while the cursor is in the list. Network quiescence became unusable when [ADR-0015](../../adr/0015-the-async-model.md) made arrivals per-repository events with no natural quiet moment. What remains is the one signal that says the user is done with the list: focus moving off it, by switching tab, opening a pane, or entering the filter input. No timer, so goldens stay deterministic.

6. **Resolved: the affordance counts every deferred change by kind, and the user's own action defers uniformly.** R11 carries the counting rule: insertions as new, evictions as removed, reorders as moved, never folded into one number. Silence about an eviction would leave the view stale in a way the user cannot see. And a re-run invoked from the Feed gets no exception: its row repaints in place at once (AC4), which already confirms the action landed, and its reorder waits with the rest. One uniform rule needs no provenance tracking and keeps the deferral golden-testable.

7. **Resolved: the selection survives a filter change, behind an always-visible count.** R13a carries it. Scroll already hides selected Runs without clearing them, so clearing on filter while surviving scroll would put the boundary in an arbitrary place, and cross-filter accumulation is a workflow a purge tool wants. The persistent selected-count makes an off-filter selection visible long before the confirmation, and R15's attributed confirmation remains the final accounting.

8. **Resolved: the merged label sums, and names where the gap lives.** R24 carries the form: summed reachable count first, summed claimed count second and marked approximate, and the number of capped repositories ("5,320 of ~24,180, 3 repos capped"). Each filtered `RunsFetched` already carries its repository's `total_count` ([ADR-0015](../../adr/0015-the-async-model.md)), so every number is held data rather than a new request. The label degenerates to the single-repository form under a one-repository filter and disappears when nothing is capped.

9. **Resolved: derivable for the primary limit always, for the secondary only when `Retry-After` is supplied, so R30's fallback is load-bearing.** `x-ratelimit-reset` arrives on every response ([rate-governor](../rate-governor/requirements.md) R5), so primary exhaustion always has a resumption instant. A secondary-limit response carries `Retry-After` optionally, so none may be available, and the governor's R9 forbids inventing one. [ADR-0014](../../adr/0014-domain-types-and-the-budget-readout.md)'s Readout carries `Reset`, zero when none is derivable, and names `Reset.IsZero()` as this question's case in code. AC10 there pins the behaviour. R30 is unchanged.

10. **Resolved: adopted for the session, at a cost of one request ([ADR-0020](../../adr/0020-discovery-scope-adoption-and-refresh.md)).** When enumeration completes without it, one `GET /repos/{owner}/{repo}` learns its permissions, `archived` and `disabled`, and the repository joins the Feed (and the poll set if it has Runs) for this session only: its record and ETags persist for revalidation economy, but only a launch inside it re-admits it. [repo-discovery](../repo-discovery/requirements.md) R22 carries the mechanism.

## Related

- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md)
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md)
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- [ADR-0009: Repo identity is host-qualified](../../adr/0009-host-qualified-repo-identity.md)
- Sibling features: [run-detail](../run-detail/requirements.md), [purge](../purge/requirements.md), [approvals](../approvals/requirements.md), [run-lifecycle](../run-lifecycle/requirements.md), [repo-discovery](../repo-discovery/requirements.md), [polling-scheduler](../polling-scheduler/requirements.md), [rate-governor](../rate-governor/requirements.md), [cli-surface](../cli-surface/requirements.md), [settings](../settings/requirements.md)
