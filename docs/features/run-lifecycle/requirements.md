# Run lifecycle

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Status and Conclusion are two different fields.

## Purpose

Cancel, force-cancel, re-run, re-run failed Jobs and re-run one Job are the five operations that act on execution rather than existence, invoked from the Feed, from Run detail, or across a multi-selection. Four address a Run and the fifth addresses a Job, and all five land on the Run: a re-run never creates a Run. It adds an Attempt to the Run that already exists, and the Feed row mutates in place.

## Requirements

### The operations

**R1.** The tool MUST offer exactly five lifecycle operations: cancel, force-cancel, re-run, re-run failed Jobs, and re-run one Job. The first four address a Run. The fifth addresses a Job and is specified in R14a.

**R2.** All five MUST be gated per repository on `permissions.push && !archived`, using the permission and `archived` fields repository discovery already carries. Where the gate fails, each operation MUST be visibly unavailable with its reason shown, and MUST issue no request. An archived repository MUST be distinguished from one merely lacking push, because archived is permanent. For re-run one Job the gate is keyed on the Job's own repository, which R14a requires the Job to carry.

**R3.** The gate is advisory, not a guarantee. A 403 arriving despite `push: true` MUST be handled as an expected outcome and MUST NOT be presented as a defect: fine-grained PATs expose no scopes, so the API is always the final authority.

### Cancel and force-cancel

**R4.** Cancel is asynchronous. A 202 means the request was accepted, not that the Run was cancelled. The tool MUST show that cancellation was requested and MUST NOT optimistically display a `cancelled` Conclusion. Only a subsequent poll observing the Run's actual transition may do that.

**R4a.** The indicator MUST occupy [live-run-feed](../live-run-feed/requirements.md) R4a's gutter, and MUST NOT be written into the Status or the Conclusion cell. R7 holds both of those to the values the API served, and a cancellation this process requested is a fact about the request rather than about either field: the Run is still running, so its Conclusion is legitimately empty and its Status is legitimately whatever it was. The indicator MUST clear when a poll observes the Run reach Status `completed`, which is R4's own authority and the only one, and MUST clear whether or not the Run's row is on screen at that moment, because a filter can hide the Run whose transition landed and a mark that outlives its request is worse than none.

**R4b.** Naming the Runs a cancel was accepted for is a property of the pass, not of a Run. The mark MUST be derived from what the operation reported acting on, and MUST NOT be stored on a Run, persisted, or inferred from Status alone: no Run the API describes carries it, no later session may resurrect it, and `in_progress` is true of Runs nobody has cancelled. [purge](../purge/requirements.md) R23's statelessness is untouched, because nothing here is written or read back.

**R5.** A 409 from cancel means the Run is not cancelable. The tool MUST present this as a fact about the Run's Status rather than as an error, and MUST offer force-cancel where the gate in R2 permits it.

**R6.** Force-cancel MUST be a distinct operation against a distinct endpoint, offered as the escalation for when plain cancel does not take effect. It MUST NOT be the default, and MUST NOT be silently substituted for cancel. The tool MUST offer it on a 409 from plain cancel (R5), and otherwise on demand as an escalation the user chooses. It MUST NOT infer from a timer that an accepted cancel is stuck, because an asynchronous cancel has no reliable stuck-signal.

**R6a.** The cancel confirmation MUST carry R6's offer at the point of decision: the modal names force-cancel as the escalation and names the key that reaches it, and that key MUST be inert on every other modal, so it cannot pick up a second meaning in front of a Purge. Choosing it MUST confirm nothing. The opener re-prices the same frozen Items as a force-cancel and the graduated confirmation runs again, so the escalation carries the friction the harder verb is owed rather than inheriting the one priced for cancel. The set MUST be the Items the cancel Plan already holds and never a fresh read of the selection, because R16 froze it when the modal opened and a poll landing while the modal was up must not be swept into the harder verb.

**R7.** The tool MUST NOT render `cancelled` as a Status. A cancelled Run has Status `completed` and Conclusion `cancelled`, and every surface MUST show them in their own fields.

### Re-run and the Attempt model

**R8.** A re-run MUST NOT be presented as creating a Run. It adds an Attempt to the existing Run: `run_attempt` increments, Status returns to `queued`, and Conclusion returns to null. This is the single most confusable behaviour in the product, and every surface that shows the result of a re-run MUST agree with this model.

**R9.** The Feed row for a re-run Run MUST mutate in place. No row may be added, and the Feed's row count MUST NOT change as a result of a re-run.

**R10.** A re-run row MUST clear its previous Conclusion when Status returns to `queued`. A row that mutates in place while still displaying the prior Attempt's `failure` is the exact conflation this product exists to avoid.

**R11.** A re-run Run MUST surface to the top of the Feed, which follows from sorting on `run_started_at`. The row MUST remain identifiable as the same Run (same Run ID, incremented Attempt badge) rather than reading as a new arrival.

**R12.** Attempt MUST be displayed as a badge and never as a view. The tool MUST NOT offer navigation into a prior Attempt's Jobs or Steps, because prior Attempts' Jobs are not served.

**R13.** Re-run failed Jobs MUST be a distinct operation from re-run, offered only where the Run has Jobs that failed. Both are re-runs and both MUST obey R8 through R12.

**R14.** Re-run and re-run failed Jobs MUST each offer a debug-logging option at the point of invocation, defaulting to off. Both endpoints accept `enable_debug_logging`, and `gh run rerun` exposes `--debug` alongside `--failed`.

**R14a.** Re-run one Job MUST be a distinct operation against `POST /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun`, the fifth operation R1 names. It MUST obey R8 through R12 as both other re-runs do, and MUST offer R14's debug-logging option, which that endpoint accepts. It MUST NOT be silently widened to the whole Run: re-running a Run when the operator named one Job spends Actions minutes they did not ask to spend.

**The endpoint re-runs the named Job and its dependents, and the canon records that rather than meeting it at the wire.** GitHub documents this endpoint as "Re-run a job and its dependent jobs in a workflow run", so what re-runs is the Job the operator named plus whatever the Workflow declares downstream of it. That is GitHub's documentation and not our measurement, because R28 bars the write that would measure it, and R3 leaves the API the authority either way. It is still not the whole Run, which is the distinction R1 admits the operation for, and R14b is where the operator is told.

A frozen set for this operation MUST hold at most one Job per Run, and a set holding two Jobs of one Run MUST be refused before any request is issued. A re-run adds an Attempt, and R12's measured constraint is that prior Attempts' Jobs are not served, so the second Job id in such a set was read from the Attempt the first request has just superseded. Whether that id still addresses anything is unverified, and R28 bars the live write that would settle it. Across Runs the interference does not arise, because each Run gains its own Attempt independently.

The multi-Run form MUST select the Job by name rather than by id, because a Job id names one Job of one Run and cannot express "this Job in each of these Runs". A name matching no Job in a given Run MUST be recorded as a skip with its reason stated, never as a failure, which is R19's shape for an ineligible member of a frozen set.

**R14b.** Where the Run detail pane offers this operation, it MUST render a one-line non-blocking note stating that the other Jobs of the current Attempt lose their Steps and their logs, and that the Jobs declared downstream of this one re-run with it (R14a). Resolved open question 7 permits such a note for the other two re-runs and does not require it, on the reasoning that "those are the failed Jobs you are re-running". That reasoning does not hold here: the operator names one Job and the rest of the Attempt goes anyway. The note MUST NOT block, MUST NOT confirm, and MUST NOT be read as reopening R18.

**R15.** The tool MUST NOT hide, disable, or pre-emptively reject a re-run based on the Run's age. If the API rejects a re-run, the tool MUST surface the API's own reason. This follows R3: the API is the authority, and the age limit described in open question 1 is unverified.

### Multi-selection

**R16.** All five operations MUST be invocable on a multi-selection, using the Purge's frozen set: the set freezes when the confirm modal opens, and Feed activity after that moment MUST NOT change it. The four Run operations use the Purge's Run-ID-keyed selection. Re-run one Job is Job-ID-keyed, bounded by R14a to one Job per Run, and the multi-Run form is reached by naming the Job rather than by selecting Job rows.

**R17.** Every multi-selection lifecycle operation MUST open a confirm modal showing the frozen count and a per-repository breakdown summing to it, and MUST apply the Purge's graduated friction unchanged: `y`/`N` for a small single-repository set, typing the exact count when the set is large or spans repositories. Cancelled work cannot be recovered, an Attempt cannot be un-added, and every re-run spends Actions minutes.

**R17a.** A by-name per-Job re-run over a multi-selection MUST resolve the name against each selected Run before R17's count is frozen, which is the only lifecycle operation that issues a request ahead of its own confirmation. Where that resolution does not reach every selected Run, the frozen set MUST be what it did resolve, so the count the operator confirms stays the count that is attempted. A Run the resolution never reached MUST NOT be counted as a Run holding no Job of that name, because those are a missing answer and a definite one. The confirm surface MUST render a one-line non-blocking note naming how many selected Runs the resolution did not reach and why, on R14b's terms: it does not block and it does not confirm. Without the note the operator confirms a number smaller than the set they named with nothing saying so, which is [cli-surface](../cli-surface/requirements.md) R16's rule against a count the output cannot stand behind. [ADR-0019](../../adr/0019-ops-plan-and-confirmed.md), amended, carries the reasoning and the two options it beat.

**R18.** Single-Run cancel and force-cancel MUST take a `y`/`N` confirmation. Single-Run re-run and re-run failed Jobs MUST NOT, since neither destroys a Run and correcting a failed Run is the Feed's most common action. A single-Job re-run MUST NOT either, on the same reasoning applied one level down: one Job is smaller than one Run, and R18 already exempts one Run. R14b's note is what that operation carries instead, and a note is not a confirmation.

**R19.** The confirm modal MUST report Runs in the frozen set that are ineligible for the chosen operation (by repository permission under R2, and by Status for the operation at hand) in the shape "3 of 47 selected Runs are in read-only repos and will be skipped". Ineligible Runs MUST be skipped, not attempted. For cancel and force-cancel, whose cancelable-Status set is unmeasured (open question 5), no Status pre-filter is applied and a request-time 409 skips the Run (R20). The permission pre-filter is exact and always applied.

**R20.** Status observed at freeze time is a snapshot of a live Feed. A Run may complete between freeze and request, so a 409 from cancel MUST be recorded as a skip rather than a failure, and MUST NOT advance the consecutive-failure counter.

### Failure contract for bulk lifecycle

**R21.** A bulk lifecycle operation MUST reuse the Purge's failure contract: rate-limit responses feed the throttle's backoff and are not failures. Permission and unexpected errors skip the Run, record the reason, and continue. 50 consecutive failures circuit-break. The summary groups failures by reason and offers a one-key retry of the recorded failures only.

**R22.** A 404 MUST NOT be interpreted uniformly across operations. For cancel and force-cancel, a 404 means the Run no longer exists and therefore is not running, so the requested end state holds and it MUST be recorded as a skip rather than a failure. For all three re-runs, a 404 means the target cannot gain an Attempt, and it MUST be recorded as a failure. The Purge's "404 counts as success" rule reasons from the requested end state. Only deletion has "gone" as its goal.

**R23.** Bulk lifecycle operations are writes and MUST be paced by the same adaptive throttle as a Purge. Rate MUST NOT be exposed as a setting.

**R24.** Bulk lifecycle operations MUST be stateless in the same sense as a Purge: no job record, no progress file, and re-invoking the same selection is the only resume. That sense is a rule about reading, not about writing ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md), amended, and [purge](../purge/requirements.md) R23): what is forbidden is anything this tool reads back on a later pass.

**None of the five operations here is a deletion, so [purge](../purge/requirements.md) R29's deletion log MUST NOT record them.** R29 logs what no later action recreates. Cancel and force-cancel change a Run's Status, and the Run, its logs and its metadata all survive. Re-run adds an Attempt. Each leaves an object standing on GitHub that carries its own record, and that record is better than ours.

**Re-run is the closest call, and it still falls outside.** R12's constraint means a new Attempt makes the prior Attempt's Jobs permanently unreachable, which open question 7 already flags as a one-way door. It is not logged, for two reasons. The Run survives carrying `run_attempt`, which is GitHub's own record that the door was opened. And there is no id for the thing that was lost, so R29's line has nothing to put in its id column. A record that cannot name what it lost is not the record R29 is.

### Seams

**R25.** All five operations MUST be exercisable end-to-end against recorded HTTP fixtures, with no live network. The fixtures MUST include cancel's 202 and its 409, force-cancel against its own endpoint, a 403 arriving on a repository whose recorded permission is `push: true` (R3), a 404 under both readings R22 draws, a re-run followed by a poll showing `run_attempt` incremented, Status back to `queued` and Conclusion back to null (R8), and the Job endpoint R14a addresses under both a success and a 404. They MUST also include `/runs/{id}/attempts/1/jobs` returning `total_count: 0`, because R12's whole case rests on that one response and a fake would return whatever we expected to see. Cassettes replay what the API actually said. Every row of the constraints table above was learned that way, including that a re-run's `created_at` and `run_started_at` disagree by 3 hours.

**R26.** A bulk lifecycle operation's timing MUST come from the same injected clock the throttle uses, so that R21's backoffs, R23's pacing and a run across a large frozen set are deterministic and instant. AC5 depends on the clock for a second reason: proving that a 202 shows no Conclusion until a poll observes the transition means advancing to that poll, and a test that waits for a real one is slow, then flaky, then deleted.

**R27.** The Feed row a re-run mutates MUST render to a frame from held state alone, with no live terminal and no network, and that frame MUST be verified by golden-file tests covering AC1, AC2 and AC3. **AC5's frame MUST be goldened on the same terms**, because R4a's whole claim is about what the row does and does not say: a test over the model can assert that the mark is held, and only a golden proves the Status cell still reads the API's value and the Conclusion cell is still empty beside it. R8 calls the Attempt model the most confusable behaviour in the product, and its three observable consequences are each a property of the painted frame: a row count that does not change, a Conclusion cell that empties, and an Attempt badge reading 2 against the Run ID it read 1 against. A test over the model can assert Conclusion is null. Only a golden proves the row stopped saying `failure`. See [live-run-feed](../live-run-feed/requirements.md) R36, which owns the Feed's goldens, and [run-detail](../run-detail/requirements.md) R19, which owns the badge's.

**R28.** No test may issue a live DELETE. This tool deletes irreversibly at a scale of tens of thousands, and the reference measurements were taken against real third-party repositories. Deletion is exercised against cassettes, never against an account. The five operations here inherit that rule and extend it: no test may issue a live cancel, force-cancel or re-run of any kind either. A live cancel kills work somebody is waiting on, a live re-run spends their Actions minutes, and neither can be undone. Every one of AC1 to AC17 is assertable against R25's fixtures, so no test here needs an account. R14a's refusal of two Jobs from one Run exists partly because the write that would justify the alternative is one this rule forbids.

## Acceptance criteria

**AC1: A re-run adds no row.** Given a Run with `run_attempt: 1`, Status `completed` and Conclusion `failure`, when it is re-run, the Feed's row count is unchanged, no row is added, and the row bearing that Run ID shows Attempt 2.

**AC2: A re-run clears the prior Conclusion.** Given the same re-run, that row shows Status `queued`, shows no Conclusion, and specifically does not still show `failure`.

**AC3: A re-run rises to the top as the same Run.** Given the same re-run, that row moves to the top of the Feed's default ordering while retaining its original Run ID.

**AC4: `cancelled` is never a Status.** Given a Run with Status `completed` and Conclusion `cancelled`, no surface renders `cancelled` in a Status field, and no surface renders `completed` in a Conclusion field.

**AC5: A 202 is a request, not an outcome.** Given a cancel returning 202, the row does not display Conclusion `cancelled` before a poll has observed the transition. It displays a cancellation-requested indicator.

**AC6: A 409 offers force-cancel, and so does the modal.** Given a cancel returning 409, no error dialog is raised, the message states the Run is not cancelable, and force-cancel is offered. Given a cancel confirmation over a frozen set, the modal names force-cancel and the key that reaches it, and pressing that key leaves the modal open on a force-cancel Plan over the same Items, having issued nothing. The same key on a Purge, a re-run or a force-cancel modal does nothing.

**AC7: The gate states its reason.** Given a Run in a repository with `push: false`, all five operations are unavailable, a reason is shown, and no request is issued. The same holds with `archived: true`, and the reason distinguishes the two.

**AC8: The breakdown sums and the skips are stated.** Given a multi-selection of 47 Runs across 3 repositories, of which 3 are in read-only repositories, the modal states that 3 of 47 will be skipped and shows three per-repository rows summing to 47, and 44 requests are issued.

**AC9: A cross-repository set requires the count.** Given a bulk cancel over a frozen set spanning 2 repositories, `y` does not start it and only the exact count string does.

**AC10: Bulk re-run still confirms.** Given a bulk re-run over a small single-repository frozen set, the confirm modal still opens and `y` starts it. Given the same set cross-repository, the count must be typed.

**AC11: The single-Run asymmetry.** Given a single-Run cancel, a `y`/`N` prompt appears before any request. Given a single-Run re-run, none does.

**AC12: A raced 409 is a skip.** Given a frozen set for cancel in which one Run completes between freeze and request, the resulting 409 appears under skips, not under failures, and the consecutive-failure counter does not advance.

**AC13: A 404 reads by the requested end state.** Given a re-run against a Run that has been deleted, the 404 is recorded as a failure. Given a cancel against the same Run, the 404 is recorded as a skip.

**AC14: Debug logging is opt-in.** Given any of the three re-runs invoked with the debug-logging option enabled, the issued request carries `enable_debug_logging`. Given the default path, it does not.

**AC14a: One request, against the Job the operator named.** Given a single-Job re-run, exactly one request is issued, it addresses the Job endpoint with that Job's id, and no request addresses the Run endpoint. What GitHub then re-runs is that Job and its dependents (R14a), which is the endpoint's documented behaviour and not a count this tool asserts. No confirmation prompt appears (R18), and where the Run detail pane offered the operation, R14b's note was on screen at the point of invocation.

**AC14b: Two Jobs of one Run are refused before the wire.** Given a frozen set holding two Jobs that share a `run_id`, the operation is refused, the message names the Run, and zero requests are issued. Given two Jobs belonging to two different Runs, the set is accepted.

**AC14c: An unmatched name is a skip.** Given a by-name re-run across 12 Runs of which 9 have a Job of that name, 9 requests are issued, the other 3 appear under skips with a reason naming the absent Job, none appears under failures, and the command exits 0. The frozen count is 12 and not 9, the 3 group under one reason rather than three, the per-repository breakdown sums to 12, and the summary's accepted, skipped and failed figures sum to the same 12.

**AC14d: A resolution that stopped early prices what it resolved, and says so.** Given a by-name re-run over 40 selected Runs whose resolution is rate-limited after 12, the confirm surface shows a frozen count of 12, a non-blocking note names the 28 it did not reach and why, and typing 12 starts it. Exactly 12 requests follow. The 28 appear in no count and under no skip reason, because they were never answered. On the CLI the same invocation exits 1.

**AC15: Age does not pre-gate a re-run.** Given a Run old enough to fall outside any suspected age limit, re-run is still offered, a request is still issued, and any rejection is reported using the API's stated reason.

**AC16: Attempt is a badge, never a view.** No surface exposes a control that navigates to a prior Attempt's Jobs or Steps.

**AC17: Bulk lifecycle is not modal.** Given a bulk lifecycle operation in flight, the Feed continues to update and the operation is cancellable, matching the Purge's behaviour.

## Constraints

Measured against the live API. Numbers are from the [PRD](../../PRD.md) unless marked otherwise.

| Constraint | Measurement | Effect on run lifecycle |
|---|---|---|
| Prior Attempts' Jobs are not served | `/runs/{id}/attempts/1/jobs` returns `total_count: 0` | R12. Attempt history is not buildable, so Attempt is a badge. A re-run therefore replaces the Jobs you were diagnosing |
| `created_at` ≠ `run_started_at` on re-runs | Identical on 8/8 normal Runs. 3 hours apart on a re-run | R11. Sorting on `run_started_at` surfaces a re-run. `created_at` would have buried it |
| Conclusion is null until Status reaches `completed` | n/a | R8, R10. A re-run's null Conclusion is the model working, not missing data |
| Cancel is asynchronous and returns 202. 409 when the Run is not cancelable. Force-cancel is a distinct endpoint, which gh surfaces as `gh run cancel --force` | Stated in the v2 design brief, not in the PRD's measured table | R4, R5, R6. Both responses are expected, not exceptional |
| Repo permissions ride along free | `/user/repos` returns `{admin, maintain, push, triage, pull}` and `archived` with no extra request | R2 costs nothing |
| Archived repositories are permanently read-only | n/a | Neither cancel nor re-run will ever be available on their Runs |
| Fine-grained PATs expose no scopes | `x-oauth-scopes` exists only for classic tokens | R3. Pre-flight permission checks are impossible. A 403 can arrive despite `push: true` |
| GitHub's prose advises ≥1s between writes. The points model prices most writes, DELETE included, at 5 points against ~900/min | ~60/min versus ~180/min, a 3× disagreement | R23. Bulk lifecycle is a write stream and needs the same adaptive throttle. Cancel and re-run carry the same published 5-point default as DELETE (open question 3, resolved) |
| Live log streaming does not exist | Logs are a zip per Run or plain text per Job, delivered on completion | After a re-run, Job and Step Status can be watched live. Log content cannot |
| Reference scale | 163 repositories, ~26 with Runs | Cross-repository multi-selections are ordinary, so R17's escalation fires often |

## Open questions

1. **Resolved: no age gate (R15 stands), and the limit stays unverified by policy.** Confirming a roughly 30-day re-run limit means issuing a live re-run of an old Run, which spends Actions minutes and cannot be undone, the class R28 bars from tests. R15 declines to gate on age and surfaces the API's own reason if it rejects, which R3 makes the authority regardless. The limit stays a possibility, not a canon fact.
2. **Resolved: Run detail renders the new Attempt's Jobs as served, assuming no carry-forward model ([run-detail](../run-detail/requirements.md) R1).** Whether GitHub's re-run-failed-Jobs Attempt contains only the re-run Jobs or carries prior successes across is its behaviour to reflect, not ours to assume: the pane shows whatever the Attempt's Jobs endpoint returns and is correct either way. A live re-run could measure it, but that is a write and not required for the display to be right.
3. **Resolved: cancel, force-cancel and re-run carry the same published 5-point default as DELETE.** The points model prices by method, "Most REST API POST, PATCH, PUT, or DELETE requests: 5" points, so R23's budget maths stops being an assumption on top of a citation and becomes the citation itself. DELETE never had endpoint-specific documentation to be the exception. The published caveat is symmetric ("Some REST API endpoints have a different point cost that is not shared publicly") and unmeasurable (PRD risk R4, permanently). One new caution for R23 specifically: the separate content-creation dimension caps content-generating requests at 80/min and 500/hour with no published request list, and re-run creates Runs, so a bulk re-run stream may be bound at 80/min rather than the points model's ~180/min. [rate-governor](../rate-governor/requirements.md) open question 3 owns the resolution and its Constraints carry the content-creation numbers. Full citations in [docs/research/write-point-costs.md](../../research/write-point-costs.md).
4. **Resolved: yes, debug-logging applies to both (R14, AC14).** Both `POST /runs/{id}/rerun` and `POST /runs/{id}/rerun-failed-jobs` accept `enable_debug_logging`, and `gh run rerun` exposes `--debug` alongside `--failed`. R14 now offers the option on both operations.
5. **Resolved: rely on the 409, do not pre-filter cancel by Status (R5, R19, R20).** Which of `queued`, `in_progress`, `waiting`, `requested` and `pending` are cancelable is unmeasured, and probing means live cancels (R28). So R19 pre-filters cancel on permission only, and a request-time 409 is the authoritative "not cancelable" that R20 records as a skip. The unknown does not block, because the 409 draws the line the measurement would have.
6. **Resolved: on a 409, and otherwise on demand (R6).** Force-cancel is offered the moment a plain cancel returns 409 (R5, the one unambiguous trigger), and is otherwise available as an escalation the user chooses. The tool does not infer from a timer that an accepted cancel is stuck, because an asynchronous cancel has no reliable stuck-signal and any timeout would be arbitrary. gh models the same shape as an explicit `gh run cancel --force`.
7. **Resolved: no blocking warning (R18 stands); a passive note is permitted where the Jobs are on screen.** Re-running replaces the Jobs you were inspecting, but those are the failed Jobs you are re-running, the Attempt badge (R8) records that the door was opened, and a confirmation on the Feed's most common corrective action is exactly the friction R18 removes. The detail pane may show a one-line, non-blocking note that the current Jobs will be replaced, but no surface may block or confirm a single re-run on this ground.
8. **Resolved: R18 stands.** Single-Run re-run and re-run failed Jobs take no confirmation, though they spend Actions minutes, because correcting a failed Run is the Feed's most common action and bulk re-run still confirms (R17). The asymmetry is deliberate and, as this question noted, a one-line change if usage proves it wrong.

## Related

- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R21, R22, R24
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R23
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). Why R2's data is free and why R17 escalates often
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md). `gh run cancel`, `gh run cancel --force` and `gh run rerun` compatibility
- [purge](../purge/requirements.md) owns the frozen set, graduated confirm and failure contract this feature reuses. Its R12 defers cancelling in-progress Runs to R1 here
- [live-run-feed](../live-run-feed/requirements.md) owns the `run_started_at` sort R11 depends on, and the in-place row mutation R9 requires
- [run-detail](../run-detail/requirements.md) owns the Attempt badge and the Jobs view a re-run replaces
- [log-viewer](../log-viewer/requirements.md) consumes R14's debug logging
- [rate-governor](../rate-governor/requirements.md) paces R23
- [cli-surface](../cli-surface/requirements.md). The non-interactive form of these five operations
