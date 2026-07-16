# Purge

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Status and Conclusion are two different fields.

## Purpose

A Purge deletes every Run matching a filter, across one or more repositories, at a rate the API tolerates, an operation measured in minutes or hours rather than seconds. It is the capability v1 existed for, preserved in full and subordinated to the Feed.

## Requirements

### Sourcing and counting

**R1.** A Purge MUST enumerate candidate Runs by crawling each target repository's Run list **unfiltered** and applying the Purge's filter predicates client-side. It MUST NOT enumerate through a server-side filtered listing.

**R2.** A Purge MUST NOT treat `total_count` from a filtered listing as the number of Runs it will delete, and MUST NOT treat an empty page from a filtered listing as exhaustion. A filtered listing reports 18,258 matches and then returns `[]` past result 1,000 with no error and no flag.

**R3.** While the crawl is running, a Purge MUST label its count as provisional. Once the crawl completes, the count it reports MUST be the exact number of Runs the filter matched, never a capped or estimated figure.

### Selection and confirmation

**R4.** Selection MUST be keyed by the tuple **(host, owner, repo, id)**. No selection, frozen set, or delete request may be derived from a row's index or position, because the Feed is live and mutates underneath the cursor.

**A bare id is not enough, for two reasons.** A DELETE needs `/repos/{owner}/{repo}/actions/runs/{id}`, so a frozen set spanning repositories cannot be resolved back to a request from ids alone, and cross-repo frozen sets are the ordinary case here rather than the exception. And [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) makes every persisted and published key host-qualified, including in 2.0.0, which serves github.com alone.

**R5.** The frozen set MUST freeze at the moment the confirm modal opens. Objects entering or leaving the Feed after that moment MUST NOT change it.

**R4 and R5 are generic over what is being deleted, and MUST be written that way.** [storage-reclamation](../storage-reclamation/requirements.md) R17 reuses R4 to R9 for Caches and Artifacts, [run-lifecycle](../run-lifecycle/requirements.md) R16 reuses them for Runs under four other operations, [log-viewer](../log-viewer/requirements.md) R17 reuses them for logs, and [ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md) mandates one shared implementation across all four. So the `id` in R4's tuple is a Run ID here, a Cache id in Reclamation, and an Artifact id beside it. What must not happen is a selection model that says "Run" in its types and then gets copied.

**R6.** The confirm modal MUST display the frozen set's total count together with a per-repository breakdown whose parts sum to that total.

**R7.** Confirmation friction MUST scale with blast radius. A frozen set confined to one repository and below the type-the-count threshold MUST be confirmable with `y`/`N`, defaulting to no. A frozen set that spans more than one repository, or that reaches the threshold, MUST require the user to type the exact count, and no other input may start it. **The threshold defaults to 50 Runs**, and R8's setting moves it.

**50 is roughly where a frozen set stops being something you can eyeball.** Below it, R6's per-repository breakdown fits on one screen and a `y` confirms a set the operator has actually read. Above it the same keystroke rubber-stamps a number. The cross-repository limb carries no number and R8 leaves it alone: a set spanning repositories types the count at any size, whatever the threshold says.

**R8.** The type-the-count threshold MUST be configurable, and the configured value MUST be clamped to a hard upper bound of **500 Runs**. At or above that bound, and for any cross-repository frozen set, typing the count MUST be required regardless of configuration. Friction has a floor. [settings](../settings/requirements.md) R12 owns the setting and the clamp.

**500 means nobody can configure away protection for a set they could never have verified by hand.** The bound is on the *setting* and not on the frozen set, per the inversion [settings](../settings/requirements.md) R12 already states: raising the threshold lowers protection, so a floor under protection is an upper bound on the value. The reference repository holds ~28,700 Runs, three orders of magnitude past 500, so a real Purge types its count whatever the config says. That is the intended outcome rather than a corner of it.

**R9.** There MUST be no setting, config key or mode that skips confirmation. In the TUI, every path from a selection to the first DELETE MUST pass through R7's graduated confirmation. The non-interactive CLI confirms differently and does not confirm less: its mandatory `--yes` ([cli-surface](../cli-surface/requirements.md) R11) is that surface's confirmation, an explicit act made once per invocation by the person typing the command, and a destructive command without it MUST refuse. That flag is not an exemption from this requirement, it is how this requirement is met where there is no modal to show. The distinction R9 draws is between a per-invocation act and a persistent state: no stored setting may stand in for either surface's confirmation, or waive the flag's requirement. v1 piped `fzf --multi` straight into a delete loop and Enter was immediately destructive. This requirement is the correction.

### Eligibility

**R10.** Deletion MUST be gated per repository on `permissions.push && !archived`, using the permission and `archived` fields that repository discovery already carries at no extra request cost. Runs in repositories failing the gate MUST NOT be attempted.

**R11.** When a frozen set mixes eligible and ineligible Runs, the confirm modal MUST state the split before the Purge starts, in the shape "3 of 47 selected Runs are in read-only repos and will be skipped". An archived repository MUST be distinguished from one merely lacking push, because archived is permanent: its Runs can never be cleaned.

**R12.** A Purge MUST exclude Runs whose Status is not `completed`, record them as skipped, and MUST NOT cancel them to make them deletable. DELETE rejects a Run that is still in progress, and cancelling is a Run lifecycle operation the user invokes deliberately. **R12 decides on crawl-time Status, and nothing revalidates it before the DELETE.** That is deliberate, and the reasoning is below.

**The gap is real and it is hours wide.** The crawl finishes long before the last DELETE: at reference scale a Purge runs ~155 minutes in the normal case and as long as ~10 hours at [rate-governor](../rate-governor/requirements.md) R11's floor. So a Run's Status can move between the page that listed it and the request that deletes it. The question is not whether that happens. It is whether any transition turns a missed deletion into a **wrong** one.

**`completed` is not terminal, and this canon says so out loud.** A re-run reuses the Run ID, resets Status to `queued`, resets Conclusion to null and increments `run_attempt`, which is exactly why [live-run-feed](../live-run-feed/requirements.md) R9 repaints the row in place rather than inserting a new one (its AC4). So a Run recorded `completed` by the crawl can be in flight by the time its DELETE is issued, and any argument that leans on `completed` being an absorbing state is wrong. That is the direction with something to lose, because deleting a Run mid-flight destroys an Attempt nobody selected. It is also the direction the API guards: DELETE rejects a Run that is still in progress, R20 records the rejection as a skip with its reason, and the Purge continues. **That guard is synchronous with the write**, which is the one property no revalidating GET can have.

**The other direction is a missed deletion, not a wrong one.** A Run that was in progress at crawl time and has completed since is skipped by R12, survives, and matches the next crawl. R24 makes that free: re-running the same Purge is the resume, and the whole cost of the miss is one pass.

**R18's 404 absorbs a different thing, and it is not this.** A 404 means the Run is gone: a previous pass deleted it, another person did, or the repository went with it. None of those is a Status transition, and no Status transition produces one. A re-run under the frozen set produces either a clean DELETE or the API's in-progress rejection, and that rejection lands in the summary and in R29's log as a skip carrying its reason. So a Run that changed Status under the frozen set is **distinguishable** from a routine race, and the failure contract is not quietly swallowing one as the other.

**The last case is a Run re-run and completed again, and R5 already decided it.** A Purge over `conclusion=failure` can reach a Run that failed at crawl time, was re-run by somebody during those ~155 minutes, and now reads `success`. Its DELETE succeeds and the Run destroyed no longer matches the filter. R5 says the set freezes when the modal opens and that objects leaving the Feed afterwards do not change it, and a Run that has left the filter is precisely an object leaving the Feed. **The frozen set is the contract. The filter is only how it was resolved.** The operator confirmed a list of Run IDs, and this is one of them. [PRD](../../PRD.md) success criterion 4's wrong Run is a Run the operator never selected, which R4's ID-keying is what prevents. It has never meant a Run whose Conclusion moved after the confirm, and R30 is where an operator gets to see the list before agreeing to it.

**Revalidation was considered and costs more than it buys.** A GET before each DELETE spends one point per Run from the pool reads and writes share, so [rate-governor](../rate-governor/requirements.md) R11's ceiling falls from ~117 deletes/min to ~98 and the normal case stretches from ~155 minutes to ~186. Those reads are 200s and not free 304s, because the crawl listed pages and holds no per-Run ETag to revalidate against. And it does not close the race, it narrows it: from hours to the milliseconds between the GET and the DELETE, a window the API's own rejection already covers at no cost. A Purge 20% slower in exchange for a smaller version of a race somebody else already closes is not a trade worth making.

**R13.** The eligibility gate is advisory, not a guarantee. A Purge MUST handle a permission error at delete time as an expected outcome under R20: the API is always the final authority, and fine-grained PATs expose no scopes, so a 403 can arrive despite `push: true`.

### Execution

**R14.** A Purge MUST NOT be modal. The Feed MUST continue to update and the rest of the tool MUST remain navigable while a Purge runs.

**R15.** A Purge MUST show continuous progress: Runs deleted against the frozen total, skips and failures so far, the current delete rate, elapsed time, and a remaining-time figure explicitly presented as an estimate.

**R16.** A Purge MUST be cancellable at any point while it runs. After cancellation at most one in-flight request may complete, and no further DELETE may be issued. Runs already deleted stay deleted. Cancelling stops a Purge, it does not reverse one.

**R17.** Delete rate MUST be set by the adaptive throttle, which begins at the documented-safe one write per second and ramps toward [rate-governor](../rate-governor/requirements.md) R11's ceiling while responses stay clean. That ceiling is dynamic: reads and writes share one ~900 points/min pool, and R14 requires the Feed to keep polling throughout, so the governor prices the Feed's reads into what a Purge may spend. With the Feed at 312 points/min it is ~1.96 deletes/sec.

Deletes-per-second MUST NOT be exposed as a setting. **Nor may the Budget share throttle a Purge**: the Budget is a share of the *primary* limit, which a Purge's writes do not spend ([rate-governor](../rate-governor/requirements.md) R19). The intent-level question a person may answer is what share of their Budget this tool spends **polling**. Nothing exposed to a user sets a delete rate, directly or indirectly.

### Failure contract

**R18.** A 404 MUST count as a success. The Run is gone, which is what was asked. Under a stateless model, racing a previous pass or another person is routine rather than exceptional.

**R19.** A rate-limit response MUST NOT count as a failure. It MUST feed the throttle's backoff and the Run MUST be re-attempted rather than recorded as failed.

**R19a.** Re-attempts under R19 MUST be bounded. After **three consecutive rate-limit classifications on the same Run**, that Run MUST be reclassified as an authorization failure and skipped under R20, and the Purge MUST continue. Any clean response resets the count.

Without the bound, R19 is an infinite loop rather than a retry. R21's breaker cannot stop it (R19 forbids the backoff from advancing it), and the throttle's floor keeps issuing at 0.5/sec forever, so a Run answering an authorization 403 would be retried until the process is killed. **R13 makes that the expected case rather than a corner**, because a 403 despite `push: true` is what a fine-grained PAT does. Three is the number because three halvings carry the throttle from its cap to its floor, so a fourth backoff buys nothing at all. [rate-governor](../rate-governor/requirements.md) open question 1 owns the reasoning and the classification rule.

**R20.** A permission error or an unexpected error MUST cause that Run to be skipped and recorded with its reason, and the Purge MUST continue.

**R21.** A Purge MUST stop itself after 50 consecutive failures. A single success MUST reset the counter, and a backoff under R19 MUST NOT advance it. Sustained consecutive failure means something systemic (a revoked token, an archived repository), and grinding on for another 90 minutes helps nobody. The summary MUST say the Purge circuit-broke, and why.

**R22.** The end-of-Purge summary MUST group failures by reason with a count for each, and MUST offer a single keystroke that re-attempts only the recorded failures. That retry MUST reuse the same throttle and the same failure contract. It needs no fresh confirmation because its set is a subset of an already-confirmed frozen set and can only shrink.

### Statelessness

**R23.** A Purge MUST NOT write a job record, a resolved ID list, or progress to disk, and MUST NOT read back anything it writes, in this pass or a later one. The filter is the durable state.

**The rule is not that nothing is written. It is that nothing written is ever read back.** That is what statelessness buys here, and the list is finite: no schema to migrate, no reconciliation against IDs that vanished underneath us, no second source of truth to disagree with the API, and no resume prompt. A file no code path opens for reading costs none of those and breaks none of them. R29's deletion log is written under exactly that constraint and is permitted. [ADR-0006](../../adr/0006-stateless-bulk-jobs.md) carries the amendment, and the decision it records is unchanged: the filter is still the job state, and R24 is still the only resume.

**R24.** Re-running the same Purge MUST be the resume path, and MUST converge: Runs deleted by an earlier pass are simply absent from the next crawl. A crash, a quit or a kill MUST require no reconciliation and MUST NOT produce a resume prompt.

**R25.** A summary MUST report only what this pass did. It MUST NOT claim cumulative progress across sessions, because none is recorded.

### Seams

**R26.** A Purge MUST be exercisable end-to-end against recorded HTTP fixtures, with no live network, covering the crawl and every branch of the failure contract. The crawl fixtures MUST include a repository at reference scale whose `Link` header carries `rel="next"` on every populated page and, on a page past the end, carries `prev`, `last` and `first` with no `next` (R1, AC5, open question 5). The delete fixtures MUST include a 404, a permission 403, a rate-limit response, and a DELETE rejected against a Run that is still in progress (R12, R18, R19, R20). [rate-governor](../rate-governor/requirements.md) R23 already records a bulk-delete cassette, and it is not this one: it proves pacing across 18,258 clean DELETEs and asserts nothing about which Run IDs a Purge resolved, or what a Purge did with the ones that answered 404. Pacing and selection are different claims and need different fixtures. Cassettes replay what the API actually said, which is how this project learned that a filtered listing caps at 1,000 and that `rel="last"` lies inside one. A hand-written fake would encode our reading of the `Link` header and stay green while the API changed it.

**R27.** A Purge's timing MUST come from an injected clock: the throttle's interval, R19's backoffs, and R15's elapsed and remaining-time figures. A test of a Purge at reference scale (18,258 Runs, which the governor's reachable rates put between ~2 hours and ~10 hours of wall clock, and at ~155 minutes in the normal case) MUST complete in milliseconds of real time while virtual time advances across the whole run, and no test may sleep through a backoff. AC13's circuit breaker is the case that needs this most. Its sequence of 49 failures, one success and 49 more failures is a counting property, and R19's backoff sits between every pair of them. On a real clock that test either sleeps for hours or never gets written.

**R28.** No test may issue a live DELETE. This tool deletes tens of thousands of Runs irreversibly, it has no undo, and the measurements this document rests on were taken against real third-party repositories. Deletion is exercised against cassettes, never against an account. A test that deletes only a few Runs is a test that deletes somebody's Runs. R26's fixtures are what make that rule affordable rather than a restriction, and they are also the only way R23, R29 and AC11 become checkable: a Purge whose every request is replayed can be run to completion, killed mid-flight, and have its state directory inspected for the files it MUST NOT have written and the one file it MUST.

### The deletion log

**R29.** Every deletion this tool issues MUST be recorded, one line per attempt, in an append-only log under the XDG state directory that nothing ever reads back. The log's writability MUST be a precondition of the first DELETE, and a write it cannot complete MUST stop the operation.

**A Purge is irreversible, has no undo (R28), and kept no record of what it destroyed.** R15's progress and R22's failure groups are in-memory and die with the process, and R25 says outright that nothing cumulative is recorded. After a Purge over the wrong filter there was nothing to tell anyone, not even the IDs. This requirement is the record. It is not a job store: nothing reads it, so it carries no schema, needs no reconciliation, and is not a resume path ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md), amended, and R23).

**Shape.** One line per deletion attempt, tab-separated, six fields in fixed order:

| Field | Value |
|---|---|
| timestamp | RFC 3339 UTC, taken from R27's injected clock and never from the wall clock |
| repo | `host/owner/name`, per [ADR-0009](../../adr/0009-host-qualified-repo-identity.md) and R4's tuple |
| kind | `run`, `log`, `cache` or `artifact` |
| id | the object's id, the same one R4's tuple carries |
| outcome | `deleted`, `gone`, `skipped` or `failed`, and nothing else |
| reason | free text, empty when the outcome is `deleted`. Tab and newline MUST be escaped |

**Tab-separated, because the only moment this file is ever read is the worst possible moment to need a parser.** `grep 4675883901` finds a Run, `cut -f4` lists the IDs to send someone, `grep -c deleted` counts them. JSON Lines answers the same questions through `jq`, which is a second tool to have installed and a syntax to recall while staring at 18,000 deletions you did not mean. It would also invite the thing R23 forbids, because a format built to be parsed eventually is.

**The outcome is a closed vocabulary in a column of its own**, so that a count is a `grep -c` and not a regex. `gone` is the 404, and R18 is unaffected by its presence here: a 404 still counts as a success in the summary. The log holds the two apart because "I deleted it" and "it was already gone" are different facts about the world, and the person reading this file is asking which. `skipped` and `failed` carry R20's recorded reason in the sixth field.

**The id is not unique across kinds, and the kind column is what separates them.** Deleting a Run's logs ([log-viewer](../log-viewer/requirements.md) R17) and later deleting the Run itself writes two lines carrying the same id, one `log` and one `run`. That is exactly the distinction log-viewer R17 exists to draw, and the log would erase it by keying on the id alone.

**Location.** `$XDG_STATE_HOME/gh-runs/deletions.log`, defaulting to `~/.local/state/gh-runs/deletions.log`. [settings](../settings/requirements.md) R2 puts state under the state directory and keeps it out of the config file, and this is state by that rule: nobody wants it on a second machine, and nobody wants it in a dotfiles repository. It sits beside the [local-store](../local-store/requirements.md) and is no part of it. That store is derived and always safe to delete (its R11), while this log is the one thing under that directory recoverable from nowhere else, so local-store R12's discard-on-unknown-schema and R13's discard-on-corrupt MUST never reach this file.

**Bounds.** Rotate at 8 MB, keep 4 generations, ~40 MB in total and no more. A reference-scale Purge writes 18,258 lines at ~100 bytes, which is ~1.8 MB, so 8 MB carries four of them and no Purge rotates in its own middle. Four generations is roughly twenty Purges of history, in a tool that exists to reclaim 10.59 GB. **One rolling log, never a file per Purge and never a file per repository.** A file per Purge is a job record wearing a different name: it is discoverable, it is countable, and the first person to see that directory asks which one to resume from. The reference user has 163 repositories, so a file per repository is a directory that only grows. The rotation size and the generation count MUST NOT be settable, by flag, config key or environment variable. Answering "how many megabytes of deletion log" needs the reference scale and the bytes-per-line figure, and the person has neither, which is [settings](../settings/requirements.md) R13's test for mechanism.

**Failure mode: no record, no deletion.** The log MUST be opened and proved writable before the first DELETE, and an operation that cannot open it MUST refuse to start and MUST name the log as the reason. A write that fails mid-operation MUST stop it exactly as R21's breaker does: no further DELETE, objects already deleted stay deleted, and the summary says the log failed and why. Deleting 18,000 Runs while unable to record one of them is the precise state this requirement exists to abolish, and it is worse than refusing to start, because the operator believes a record is being kept. R24 is what makes refusing cheap: the cost of stopping is one re-crawl, and the cost of continuing is permanent and unbounded. A disk-full does stop a ~155-minute operation. That trade is not close.

**Ordering.** The line MUST be written after the response is classified and before the next DELETE is issued, so the log is never more than one attempt behind reality. A kill inside that window loses one line, and that is accepted, on the same reasoning as R16's one in-flight request. Writing an intent line first and an outcome line after would double the file and give every attempt two records to correlate, which is a job record arriving by the back door.

**It is write-only.** No code path in this tool may open this file for reading, parse it, count it, or offer to resume from it. R24 stays the only resume, and its mechanism is the filter and the API, never this file. The log is for a person, and for one question: what did I just destroy.

**Scope: deletions, and nothing else.** [rate-governor](../rate-governor/requirements.md) R2 paces ten writes. Four destroy something no later action recreates, and those four are logged: Run deletion here, log deletion ([log-viewer](../log-viewer/requirements.md) R17), and Cache and Artifact deletion ([storage-reclamation](../storage-reclamation/requirements.md) R17). The other six MUST NOT be logged. Cancel and force-cancel change a Run's Status. Re-run adds an Attempt. Dispatch creates a Run. Flipping a Workflow's State, which R2 now names as enable and disable and which [ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md) also gives to `ops`, is undone by flipping it back. Every one of those leaves an object standing on GitHub carrying its own record, and that record is the record. Logging them would make this a general activity log, which has other readers, other bounds, and no reason to stop a write when the disk fills.

### Inspecting the frozen set

**R30.** Before a Purge is confirmed, the operator MUST be able to read the Runs in the frozen set, one row each, and reach both ends of it. The view MUST open on a single keystroke from the confirm modal, and the modal MUST name that keystroke. It MUST NOT be a mandatory step, and it MUST NOT be the only way to reach R7's confirmation.

**R6 shows a count and a breakdown, and neither answers the question a Purge is confirmed against.** [PRD](../../PRD.md) success criterion 4 says you cannot delete the wrong Run, and it decomposes into three clauses: selection is ID-keyed (R4), the confirm set is frozen (R5), rows never move while the cursor is in the list ([live-run-feed](../live-run-feed/requirements.md) R10). All three defend against the list moving under the cursor. **None defends against a filter matching more than you meant**, which is the failure mode with teeth at reference scale. A filter-driven Purge of 18,258 Runs is confirmed by typing "18258" (R7), against a number nothing on screen can check. Typing it proves the operator read a number. It proves nothing about whether the number is the right one.

**Available rather than mandatory, and advertised rather than hidden.** Nobody pages 183 screens of 100 rows, so compulsory inspection at reference scale is a keystroke to dismiss, and friction that cannot be completed is friction theatre. R7 already carries the load that scales with blast radius. What R30 changes is that the number stops being unverifiable: the first page is where most of the value sits, because a date filter off by a year, or a Status filter that swept up a Workflow the operator forgot about, is visible in ten rows. Naming the key on the modal is what gets it pressed at the moment it matters, which is while the operator is about to type 18258. Making it compulsory is what gets it skipped.

**Both ends, because the ends are where a filter is wrong.** The view carries the Feed's order ([live-run-feed](../live-run-feed/requirements.md) R8, `run_started_at` descending), so the oldest Runs in the set are its last rows, and "delete Runs older than 90 days" is a filter aimed at exactly those. An operator checking a date boundary needs the row at each end, not the first screen and a scrollbar.

**Per row, the Feed's columns and no new ones.** The owning repository ([live-run-feed](../live-run-feed/requirements.md) R3), the Run ID R4's tuple already carries, Status and Conclusion in separate cells with Conclusion empty on any row not `completed` (its R4, R5), the Workflow name, and `run_started_at`. Same fields, same vocabulary, same rules. An operator who has been reading the Feed all morning must not have to learn a second row to check the one thing that is irreversible.

**It is not a second Feed, and it must not become one.** R5 freezes the set at modal open, so it is stable by construction and there is nothing to update. The view therefore issues no request, spends no Budget, revalidates nothing, polls nothing, and inherits none of the deferred-insertion machinery ([live-run-feed](../live-run-feed/requirements.md) R9 to R12) that is the entire cost of a live list. It is a viewport over held state. It offers no selection, no cursor-driven action and no deletion of its own, and the only way out is back to the modal with the frozen set and the confirmation state unchanged. Nothing in it is a path from a selection to a DELETE that skips R7 (R9).

**The rows must already be held, which is a requirement on the frozen set rather than on the view.** R1's crawl decodes every Run to apply the filter predicates client-side, so the objects pass through memory once, and R3 takes its exact count from that pass. If the frozen set keeps only R4's tuples, R30 is unimplementable without a second crawl, and R5's freeze makes a second crawl a different set by definition. So the frozen set carries the row. The cost is bounded and small: R29 puts a deletion log line at ~100 bytes carrying four of these fields, so 18,258 rows is megabytes, against a crawl that already cost ~287 requests on the reference repository alone. [ADR-0011](../../adr/0011-package-layout-and-dependency-direction.md) has the home for it, and R30 needs no new one. `ops.Plan` freezes the set and resolves eligibility, `tui/confirm` renders a `Plan` and collects the input, and this requirement is the `Plan` carrying rows and `confirm` gaining a viewport over them. Not a package, and not a tab.

**The binding is not this requirement's to name, and it is now named.** [live-run-feed](../live-run-feed/requirements.md) R7 ships exactly two keybinding profiles and owns which key does what in each. Its **R7a** enumerates both and gives this view **`v`** in each, alongside the first-row and last-row motions this requirement's "both ends" depends on (`g`/`G` under Vim, `home`/`end` under Standard). R30 still requires only that the keystroke exists and that the modal says which.

**Same resolved set, two presentations.** [cli-surface](../cli-surface/requirements.md) R10 gives the non-interactive surface a `--dry-run` that resolves the affected set through the same code path as the real operation and reports what would be deleted, and its R20 forbids that from being a second implementation of the resolution. R30 is that same guarantee on the surface that has a screen. One crawl, one frozen set, two ways to look at it: stdout that `grep` and `wc -l` answer questions about, and a viewport that pages. Neither surface resolves the set twice, and neither may disagree with the other about what is in it.

**R30 is a Purge's, not the shared confirmation's.** R4 to R9 are generic over what is being deleted and are reused for Caches, Artifacts, logs and four lifecycle operations. R30 is not, deliberately. Every one of those reuses acts on a selection of rows the operator was looking at when they made it, and the count on the modal is a count of things they picked. A Purge is the one operation whose set is resolved by a predicate over ~28,700 objects the operator never saw, and that gap between "what I asked for" and "what matched" is the whole of what R30 addresses. Reclamation's 83 Caches and 581 Artifacts do not have it.

## Acceptance criteria

**AC1: The breakdown sums to the total.** Given a frozen set of 47 Runs drawn from 3 repositories, the confirm modal shows the number 47 and three per-repository rows whose counts sum to 47.

**AC2: The frozen set ignores arrivals.** Given a confirmed Purge, when a Run matching the same filter is invoked before the first DELETE is issued, exactly the 47 frozen Run IDs are attempted and the new Run is not.

**AC3: Re-sorting does not move the target.** Given a confirmed Purge, when the Feed re-sorts or removes rows mid-flight, the sequence of Run IDs sent to DELETE is unchanged.

**AC4: The count comes from the crawl.** Given a repository whose filtered listing reports `total_count: 18258`, a Purge on that filter reports a matched count derived from the unfiltered crawl, and that count is neither 1,000 nor 18,258 unless the crawl independently produces it. The fixture's own totals are what the assertion reads, never a constant compiled into the test. No pagination loop terminates on the first empty filtered page.

**AC5: The crawl reaches past the cap.** Given a fixture repository of ~28,700 Runs, the crawl issues on the order of 287 list requests and reaches Runs beyond result 1,000. The request count is derived from the fixture's `Link` header, not hardcoded: these totals drift ([PRD](../../PRD.md)).

**AC6: A small set confirms with `y`/`N`.** Given a single-repository frozen set of 49 Runs against the default threshold of 50, `y` starts the Purge. `n`, Esc, and Enter on the default abort it with zero DELETE requests issued. The same set at 50 does not start on `y`: only the exact count string starts it (R7).

**AC7: A cross-repository set requires the count.** Given a frozen set spanning 2 or more repositories, `y` does not start the Purge. Only the exact count string does, and a wrong number leaves the DELETE count at zero.

**AC8: Friction has a floor.** Given a configuration setting the threshold to 5,000, it clamps to 500, and a single-repository frozen set of 500 Runs still requires the count to be typed (R8).

**AC9: No path skips confirmation.** In the TUI, no configuration, flag or key sequence produces a path from a selection to a first DELETE without an intervening confirm interaction. In the CLI, no configuration and no environment variable produces a Purge that issues a DELETE without `--yes` present on the command line.

**AC10: A Purge is not modal.** Given a Purge in flight, the Feed still applies polled updates and still accepts cursor movement and view changes.

**AC11: Cancellation stops promptly and leaves no job state.** Given cancellation after N deletions, no DELETE is issued after at most one in-flight request completes, and the summary reports N. Afterwards the state directory holds no job record, no resolved ID list and no progress file, and re-running the same Purge offers no resume prompt. R29's deletion log is expected to exist and to carry those N deletions, and its presence MUST NOT fail this criterion. The claim under test is that nothing on disk is read back, never that nothing was written (R23).

**AC12: A 404 is success.** Given a DELETE that returns 404, the summary counts it under successes and not under failures.

**AC13: The circuit breaker counts consecutively.** Given 49 consecutive failures followed by one success followed by 49 more failures, the Purge does not stop. Given 50 consecutive failures, it stops, issues no further DELETE, and the summary names the circuit-break.

**AC14: A rate limit is not a failure.** Given a sustained sequence of rate-limit responses, none appears in the summary's failure groups, none advances the consecutive-failure counter, and the throttle's issue rate falls.

**AC14a: The backoff is bounded and the Purge does not stall.** Given a Run in a repository recording `push: true` whose every DELETE answers an authorization-shaped 403, that Run is retried at most three times, is then recorded as a skip with its reason, and the Purge proceeds to the next Run. The test asserts a terminating Purge and a bounded request count for that Run. Unbounded, this case issues 403s at the throttle's floor until the process dies (R19a).

**AC15: Ineligible Runs are skipped, not attempted.** Given a frozen set of 47 Runs of which 3 are in repositories with `push: false` or `archived: true`, the modal states that 3 of 47 will be skipped, 44 DELETE requests are issued, and the 3 are counted as skipped rather than failed.

**AC16: In-progress Runs are skipped, not cancelled.** Given a frozen set containing Runs whose Status is `in_progress`, no DELETE and no cancel request is issued for them, and each is recorded as skipped with a reason.

**AC16a: A Run re-run under the frozen set is skipped, not wrongly deleted.** Given a frozen set whose crawl recorded a Run as `completed`, and a fixture in which that Run's DELETE answers the API's in-progress rejection, the Run is recorded as a skip carrying its reason, no cancel request is issued for it, the summary does not count it as a failure, and the Purge proceeds to the next Run. Across the whole Purge no revalidating GET is issued for any Run in the frozen set, and the set is never re-resolved. R26's fixtures already carry that rejection, so this criterion needs no new one (R12, R20).

**AC17: Re-running is the resume.** Given a Purge that deleted 500 Runs and then quit, re-running the same Purge reports a matched count reduced by roughly those 500, shows no resume prompt, and reports only the deletions performed by the new pass.

**AC18: The summary groups failures and retries only those.** Given failures spanning two distinct reasons, the summary shows two groups with per-reason counts, and the retry keystroke issues exactly as many requests as there are recorded failures.

**AC19: Every attempt is one line, and nothing reads it back.** Given a Purge over R26's fixtures whose outcomes span a deletion, a 404, a skip and a failure, the deletion log carries exactly one line per attempt, each with R29's six fields in order, each repo field host-qualified, and each timestamp taken from R27's injected clock rather than the wall clock. The 404's line reads `gone` while the summary counts it a success (R18). Across this whole suite no code path opens the log for reading, and a Purge run against a populated log behaves identically to one run against an absent log and offers no resume prompt (R23, R24).

**AC20: No record, no deletion.** Given a state directory that cannot be written, a Purge issues zero DELETE requests, refuses to start, and names the log in its diagnostic. Given a log write that fails after N deletions, no DELETE is issued afterwards, the N stay deleted, and the summary names the log as the reason it stopped (R21, R29).

**AC21: The log is bounded.** Given deletions totalling more than 8 MB of lines, the log rotates, at most 4 generations exist, and the whole directory's log footprint stays under the stated bound. No configuration, flag or environment variable changes the rotation size or the generation count (R29).

**AC22: The frozen set is inspectable, and inspecting it costs nothing.** Given a frozen set of 18,258 Runs drawn from several repositories, the confirm modal names the keystroke that opens the inspect view. Opening it, paging to the last row and closing it issues zero HTTP requests and zero DELETEs. Every row carries its owning repository, its Run ID, and Status and Conclusion in separate cells, with Conclusion empty on any row whose Status is not `completed`. The rows are the same tuples `Execute` is handed, the last row is the oldest Run in the set by `run_started_at`, and no key in the view selects, deletes or confirms. Closing it leaves the frozen set and the confirmation state untouched, and the Purge still requires R7's typed count. The fixture's own totals are what the assertion reads, never a constant compiled into the test (R30).

## Constraints

Measured against the live API. Every number below is from the [PRD](../../PRD.md).

| Constraint | Measurement | Effect on a Purge |
|---|---|---|
| Filtered listing silently caps at 1,000 | `total_count` 28,694 unfiltered, 18,258 for `status=success`. Page 11 returns 100 Runs unfiltered and `[]` filtered, with no error | R1, R2. A Purge crawls unfiltered. Filtered pagination cannot enumerate the job |
| Runs sort newest-first, and the cap is per repository | A filtered view reaches only the newest 1,000 matches | "Delete Runs older than 90 days" asks for the oldest, so a Purge cannot reuse the Feed's path |
| Unfiltered crawl cost | ~287 requests for 28,694 Runs, one-off | Counts are exact. The crawl is affordable but not instant, hence R3's provisional label |
| DELETE costs 5 points against ~900/min. **Documented, not measured** | ~180 deletes/min, and only with no reads in flight | An upper bound the governor never reaches. R11 there caps at 150/min |
| GitHub's prose advises ≥1s between writes | ~60 deletes/min | A 3× disagreement with the points model. Hence the adaptive ramp rather than a fixed rate |
| Reads and writes share the ~900 points/min pool | The Feed spends 312 points/min at 26 repos on 5s. A Purge at 2.5/sec spends 750. The sum is 1,062 | R14 keeps the Feed polling during a Purge, so this collision is the normal case. [rate-governor](../rate-governor/requirements.md) R11's ceiling is `(900 - reads) / 5` deletes per minute, which is ~117/min (~1.96/sec) with the Feed running |
| Purge duration at reference scale | 18,258 Runs at the governor's **reachable** rates: ~2 hours at its 2.5/sec cap, ~10 hours at its 0.5/sec floor, ~155 minutes with the Feed running. The published band of ~100 minutes to ~5 hours belongs to the two limits above, and the governor reaches neither end of it | R14, R15, R16: a Purge cannot be a modal, must show progress, must be interruptible |
| Throttling makes cancellation cheap | The governor is idle between writes | Cancellation is naturally prompt (R16) |
| Repo permissions ride along free | `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request | R10 costs nothing |
| Archived repositories are permanently read-only | n/a | Their Runs can never be cleaned. R11 must say so rather than implying a retry would help |
| Fine-grained PATs expose no scopes | `x-oauth-scopes` exists only for classic tokens | Pre-flight permission checks are impossible. A 403 can arrive despite `push: true` (R13) |
| DELETE rejects in-progress Runs | n/a | R12. They must be cancelled first, which a Purge does not do on the user's behalf |
| Conclusion is null until Status reaches `completed` | n/a | A Purge filtered on Conclusion matches only completed Runs, so R12 bites hardest on Status-only and date-only filters |
| Reference scale | 163 repositories, ~26 with Runs. Reference repository 28,694 Runs, point-in-time | Cross-repo frozen sets are the normal case, not the edge case |
| v1's chosen rate | `sleep 0.25` ≈ 4 deletes/sec, faster than both numbers above | Works on small repositories, would be blocked on a large one |

## Open questions

1. **Resolved: the threshold defaults to 50 and clamps at 500.** R7 and R8 carry the numbers, [settings](../settings/requirements.md) R12 carries the setting, and AC6 and AC8 pin both.

    **50 is roughly where a frozen set stops being reviewable by eye**, so below it a `y`/`N` is an honest confirmation rather than a rubber stamp. **500 is a bound on the setting rather than on the frozen set**, per the inversion settings R12 states: raising the threshold lowers protection, so the floor under protection is an upper bound on the value. It means nobody can configure away the typed confirmation for a set they could never have verified by hand.

    **Both numbers are chosen, not measured**, and the reference scale is what makes the choice safe rather than tuned: ~28,700 Runs sit three orders of magnitude above the maximum, so a real Purge types its count under any configuration. R7's cross-repository limb is unchanged and forces typing regardless of count. Neither number is settable past the clamp, which is settings R13's mechanism test applied.
2. **Resolved: a non-interactive Purge confirms with a mandatory `--yes`.** [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) commits to a full non-interactive CLI, and R9 refused to offer a skip. The reconciliation is that `--yes` is not a skip. It is an explicit act, made once per invocation by the person typing the command, and it is what confirmation looks like on a surface with no modal to show. A Purge invoked without it refuses ([cli-surface](../cli-surface/requirements.md) R11). What stays forbidden is the persistent form: any stored setting that waives the flag or pre-answers the modal ([settings](../settings/requirements.md) R13). R9 carries the amended wording.
3. **Resolved: discriminate on the body's shape, and bound the retries.** R19 and R20 send a 403 to opposite outcomes, and the canon established that both occur without saying how they are told apart. [rate-governor](../rate-governor/requirements.md) open question 1 owns the answer and resolves it in two parts.

    **A 403 is an authorization outcome only when it matches the measured authorization shape**, which is a `documentation_url` pointing at the endpoint's own reference page, a `message` naming the missing permission, and no `retry-after`. Everything else is rate limiting, and `retry-after` present means rate limiting outright. The header is not the discriminator: a secondary-limit 403 can arrive with a perfectly healthy `x-ratelimit-remaining`, exactly as the measured authorization 403 did.

    **And the safe direction is bounded at three.** Defaulting to backoff, unbounded, is an infinite loop rather than a cheap mistake: R21's breaker never advances (R19 forbids it), and the governor's floor keeps issuing at 0.5/sec forever. R13 makes that the *expected* path, because a 403 despite `push: true` is what a fine-grained PAT does. So after three consecutive rate-limit classifications on the same Run, the governor reclassifies it as authorization and **R20 skips it**. Three is what backoff has to give: three halvings carry the rate from the governor's cap to its floor, and a fourth cannot slow anything further.
4. **The Status values DELETE rejects are UNKNOWN beyond in-progress.** R12 conservatively excludes every Status other than `completed`. Whether DELETE also rejects `queued`, `waiting`, `requested` and `pending` has not been measured. Verify before narrowing R12.
5. **Resolved: the terminal signal is the disappearance of `rel="next"` from the `Link` header.** Measured against `cli/cli` unfiltered, `total_count` 28,694. Page 287 is the last populated page, and `Link` on page 1 already carries `rel="last"` pointing at it, so an unfiltered crawl knows its own length from the first request rather than discovering it. Requesting page 400 returns **HTTP 200, an empty array, and a `Link` carrying `prev`, `last` and `first` but no `next`**. Unfiltered, `rel="last"` and `total_count` agree (287 pages x 100 = 28,700 against a claimed 28,694), which is exactly what a filtered listing does not do. Crawl on `next`, stop when it is gone, and never compute the end from `total_count`.
6. **50 consecutive failures is asserted, not measured.** It is undecided whether the number is configurable. Too low and a flaky network stops a legitimate Purge. Too high and it stops meaning anything.
7. **Resolved: the throttle is global, not per repository.** The Budget is a property of the token, so a per-repository throttle would multiply its own rate by however many repositories a Purge happens to span. One cross-repo Purge is one throttle. Settled by [rate-governor](../rate-governor/requirements.md) R3, which owns it.
8. **Whether a Purge's crawl yields to the Feed's polling is undecided.** Both draw on the same Budget. The crawl is ~287 requests of 200s that cannot be revalidated away. Belongs to [polling-scheduler](../polling-scheduler/requirements.md).
9. **R15's estimate is weak early.** The adaptive ramp spans a 3× range, so a remaining-time figure computed in the first minute may be off by hours. How honestly to present that is undecided.

## Related

- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md). R1, R2, R3
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R18, R23, R24, R25. Its amendment unbundles R29's log from the job store it was rejected alongside
- [ADR-0011: Package layout and dependency direction](../../adr/0011-package-layout-and-dependency-direction.md). `ops` owns R29's log, and `Execute` is the only thing that writes it
- [log-viewer](../log-viewer/requirements.md) and [storage-reclamation](../storage-reclamation/requirements.md). Their R17s carry R29 for log, Cache and Artifact deletion
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R14, R16, R17, R19
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). Why frozen sets span repositories
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md). R9's non-interactive half, settled in open question 2
- [run-lifecycle](../run-lifecycle/requirements.md) shares R4–R8's frozen set and graduated confirm. It owns the cancel that R12 declines to perform
- [live-run-feed](../live-run-feed/requirements.md). The surface selection is made on, and the reason R4 exists
- [rate-governor](../rate-governor/requirements.md) implements R17 and R19
- [repo-discovery](../repo-discovery/requirements.md) supplies R10's permission and `archived` data
- [cli-surface](../cli-surface/requirements.md). The non-interactive Purge. Its R10 `--dry-run` presents R30's frozen set on the surface with no screen, resolved by the same code path
