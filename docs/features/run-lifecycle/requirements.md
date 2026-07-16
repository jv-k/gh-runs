# Run lifecycle

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Status and Conclusion are two different fields.

## Purpose

Cancel, force-cancel, re-run, and re-run failed Jobs are the four operations that act on a Run's execution rather than its existence, invoked from the Feed, from Run detail, or across a multi-selection. A re-run never creates a Run. It adds an Attempt to the Run that already exists, and the Feed row mutates in place.

## Requirements

### The operations

**R1.** The tool MUST offer exactly four lifecycle operations on a Run: cancel, force-cancel, re-run, and re-run failed Jobs.

**R2.** All four MUST be gated per repository on `permissions.push && !archived`, using the permission and `archived` fields repository discovery already carries. Where the gate fails, each operation MUST be visibly unavailable with its reason shown, and MUST issue no request. An archived repository MUST be distinguished from one merely lacking push, because archived is permanent.

**R3.** The gate is advisory, not a guarantee. A 403 arriving despite `push: true` MUST be handled as an expected outcome and MUST NOT be presented as a defect: fine-grained PATs expose no scopes, so the API is always the final authority.

### Cancel and force-cancel

**R4.** Cancel is asynchronous. A 202 means the request was accepted, not that the Run was cancelled. The tool MUST show that cancellation was requested and MUST NOT optimistically display a `cancelled` Conclusion. Only a subsequent poll observing the Run's actual transition may do that.

**R5.** A 409 from cancel means the Run is not cancelable. The tool MUST present this as a fact about the Run's Status rather than as an error, and MUST offer force-cancel where the gate in R2 permits it.

**R6.** Force-cancel MUST be a distinct operation against a distinct endpoint, offered as the escalation for when plain cancel does not take effect. It MUST NOT be the default, and MUST NOT be silently substituted for cancel.

**R7.** The tool MUST NOT render `cancelled` as a Status. A cancelled Run has Status `completed` and Conclusion `cancelled`, and every surface MUST show them in their own fields.

### Re-run and the Attempt model

**R8.** A re-run MUST NOT be presented as creating a Run. It adds an Attempt to the existing Run: `run_attempt` increments, Status returns to `queued`, and Conclusion returns to null. This is the single most confusable behaviour in the product, and every surface that shows the result of a re-run MUST agree with this model.

**R9.** The Feed row for a re-run Run MUST mutate in place. No row may be added, and the Feed's row count MUST NOT change as a result of a re-run.

**R10.** A re-run row MUST clear its previous Conclusion when Status returns to `queued`. A row that mutates in place while still displaying the prior Attempt's `failure` is the exact conflation this product exists to avoid.

**R11.** A re-run Run MUST surface to the top of the Feed, which follows from sorting on `run_started_at`. The row MUST remain identifiable as the same Run (same Run ID, incremented Attempt badge) rather than reading as a new arrival.

**R12.** Attempt MUST be displayed as a badge and never as a view. The tool MUST NOT offer navigation into a prior Attempt's Jobs or Steps, because prior Attempts' Jobs are not served.

**R13.** Re-run failed Jobs MUST be a distinct operation from re-run, offered only where the Run has Jobs that failed. Both are re-runs and both MUST obey R8 through R12.

**R14.** Re-run MUST offer a debug-logging option at the point of invocation, defaulting to off.

**R15.** The tool MUST NOT hide, disable, or pre-emptively reject a re-run based on the Run's age. If the API rejects a re-run, the tool MUST surface the API's own reason. This follows R3: the API is the authority, and the age limit described in open question 1 is unverified.

### Multi-selection

**R16.** All four operations MUST be invocable on a multi-selection, using the Purge's Run-ID-keyed selection and frozen set: the set freezes when the confirm modal opens, and Feed activity after that moment MUST NOT change it.

**R17.** Every multi-selection lifecycle operation MUST open a confirm modal showing the frozen count and a per-repository breakdown summing to it, and MUST apply the Purge's graduated friction unchanged: `y`/`N` for a small single-repository set, typing the exact count when the set is large or spans repositories. Cancelled work cannot be recovered, an Attempt cannot be un-added, and every re-run spends Actions minutes.

**R18.** Single-Run cancel and force-cancel MUST take a `y`/`N` confirmation. Single-Run re-run and re-run failed Jobs MUST NOT, since neither destroys a Run and correcting a failed Run is the Feed's most common action.

**R19.** The confirm modal MUST report Runs in the frozen set that are ineligible for the chosen operation (by repository permission under R2, and by Status for the operation at hand) in the shape "3 of 47 selected Runs are in read-only repos and will be skipped". Ineligible Runs MUST be skipped, not attempted.

**R20.** Status observed at freeze time is a snapshot of a live Feed. A Run may complete between freeze and request, so a 409 from cancel MUST be recorded as a skip rather than a failure, and MUST NOT advance the consecutive-failure counter.

### Failure contract for bulk lifecycle

**R21.** A bulk lifecycle operation MUST reuse the Purge's failure contract: rate-limit responses feed the throttle's backoff and are not failures. Permission and unexpected errors skip the Run, record the reason, and continue. 50 consecutive failures circuit-break. The summary groups failures by reason and offers a one-key retry of the recorded failures only.

**R22.** A 404 MUST NOT be interpreted uniformly across operations. For cancel and force-cancel, a 404 means the Run no longer exists and therefore is not running, so the requested end state holds and it MUST be recorded as a skip rather than a failure. For re-run and re-run failed Jobs, a 404 means the Run cannot gain an Attempt, and it MUST be recorded as a failure. The Purge's "404 counts as success" rule reasons from the requested end state. Only deletion has "gone" as its goal.

**R23.** Bulk lifecycle operations are writes and MUST be paced by the same adaptive throttle as a Purge. Rate MUST NOT be exposed as a setting.

**R24.** Bulk lifecycle operations MUST be stateless in the same sense as a Purge: no job record, no progress file, and re-invoking the same selection is the only resume. That sense is a rule about reading, not about writing ([ADR-0006](../../adr/0006-stateless-bulk-jobs.md), amended, and [purge](../purge/requirements.md) R23): what is forbidden is anything this tool reads back on a later pass.

**None of the four operations here is a deletion, so [purge](../purge/requirements.md) R29's deletion log MUST NOT record them.** R29 logs what no later action recreates. Cancel and force-cancel change a Run's Status, and the Run, its logs and its metadata all survive. Re-run adds an Attempt. Each leaves an object standing on GitHub that carries its own record, and that record is better than ours.

**Re-run is the closest call, and it still falls outside.** R12's constraint means a new Attempt makes the prior Attempt's Jobs permanently unreachable, which open question 7 already flags as a one-way door. It is not logged, for two reasons. The Run survives carrying `run_attempt`, which is GitHub's own record that the door was opened. And there is no id for the thing that was lost, so R29's line has nothing to put in its id column. A record that cannot name what it lost is not the record R29 is.

### Seams

**R25.** All four operations MUST be exercisable end-to-end against recorded HTTP fixtures, with no live network. The fixtures MUST include cancel's 202 and its 409, force-cancel against its own endpoint, a 403 arriving on a repository whose recorded permission is `push: true` (R3), a 404 under both readings R22 draws, and a re-run followed by a poll showing `run_attempt` incremented, Status back to `queued` and Conclusion back to null (R8). They MUST also include `/runs/{id}/attempts/1/jobs` returning `total_count: 0`, because R12's whole case rests on that one response and a fake would return whatever we expected to see. Cassettes replay what the API actually said. Every row of the constraints table above was learned that way, including that a re-run's `created_at` and `run_started_at` disagree by 3 hours.

**R26.** A bulk lifecycle operation's timing MUST come from the same injected clock the throttle uses, so that R21's backoffs, R23's pacing and a run across a large frozen set are deterministic and instant. AC5 depends on the clock for a second reason: proving that a 202 shows no Conclusion until a poll observes the transition means advancing to that poll, and a test that waits for a real one is slow, then flaky, then deleted.

**R27.** The Feed row a re-run mutates MUST render to a frame from held state alone, with no live terminal and no network, and that frame MUST be verified by golden-file tests covering AC1, AC2 and AC3. R8 calls the Attempt model the most confusable behaviour in the product, and its three observable consequences are each a property of the painted frame: a row count that does not change, a Conclusion cell that empties, and an Attempt badge reading 2 against the Run ID it read 1 against. A test over the model can assert Conclusion is null. Only a golden proves the row stopped saying `failure`. See [live-run-feed](../live-run-feed/requirements.md) R36, which owns the Feed's goldens, and [run-detail](../run-detail/requirements.md) R19, which owns the badge's.

**R28.** No test may issue a live DELETE. This tool deletes irreversibly at a scale of tens of thousands, and the reference measurements were taken against real third-party repositories. Deletion is exercised against cassettes, never against an account. The four operations here inherit that rule and extend it: no test may issue a live cancel, force-cancel or re-run either. A live cancel kills work somebody is waiting on, a live re-run spends their Actions minutes, and neither can be undone. Every one of AC1 to AC17 is assertable against R25's fixtures, so no test here needs an account.

## Acceptance criteria

**AC1: A re-run adds no row.** Given a Run with `run_attempt: 1`, Status `completed` and Conclusion `failure`, when it is re-run, the Feed's row count is unchanged, no row is added, and the row bearing that Run ID shows Attempt 2.

**AC2: A re-run clears the prior Conclusion.** Given the same re-run, that row shows Status `queued`, shows no Conclusion, and specifically does not still show `failure`.

**AC3: A re-run rises to the top as the same Run.** Given the same re-run, that row moves to the top of the Feed's default ordering while retaining its original Run ID.

**AC4: `cancelled` is never a Status.** Given a Run with Status `completed` and Conclusion `cancelled`, no surface renders `cancelled` in a Status field, and no surface renders `completed` in a Conclusion field.

**AC5: A 202 is a request, not an outcome.** Given a cancel returning 202, the row does not display Conclusion `cancelled` before a poll has observed the transition. It displays a cancellation-requested indicator.

**AC6: A 409 offers force-cancel.** Given a cancel returning 409, no error dialog is raised, the message states the Run is not cancelable, and force-cancel is offered.

**AC7: The gate states its reason.** Given a Run in a repository with `push: false`, all four operations are unavailable, a reason is shown, and no request is issued. The same holds with `archived: true`, and the reason distinguishes the two.

**AC8: The breakdown sums and the skips are stated.** Given a multi-selection of 47 Runs across 3 repositories, of which 3 are in read-only repositories, the modal states that 3 of 47 will be skipped and shows three per-repository rows summing to 47, and 44 requests are issued.

**AC9: A cross-repository set requires the count.** Given a bulk cancel over a frozen set spanning 2 repositories, `y` does not start it and only the exact count string does.

**AC10: Bulk re-run still confirms.** Given a bulk re-run over a small single-repository frozen set, the confirm modal still opens and `y` starts it. Given the same set cross-repository, the count must be typed.

**AC11: The single-Run asymmetry.** Given a single-Run cancel, a `y`/`N` prompt appears before any request. Given a single-Run re-run, none does.

**AC12: A raced 409 is a skip.** Given a frozen set for cancel in which one Run completes between freeze and request, the resulting 409 appears under skips, not under failures, and the consecutive-failure counter does not advance.

**AC13: A 404 reads by the requested end state.** Given a re-run against a Run that has been deleted, the 404 is recorded as a failure. Given a cancel against the same Run, the 404 is recorded as a skip.

**AC14: Debug logging is opt-in.** Given a re-run invoked with the debug-logging option enabled, the issued request carries it. Given the default path, it does not.

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
| GitHub's prose advises ≥1s between writes. DELETE measures 5 points against ~900/min | ~60/min versus ~180/min, a 3× disagreement | R23. Bulk lifecycle is a write stream and needs the same adaptive throttle. The point cost of cancel and re-run is not measured (see open question 3) |
| Live log streaming does not exist | Logs are a zip per Run or plain text per Job, delivered on completion | After a re-run, Job and Step Status can be watched live. Log content cannot |
| Reference scale | 163 repositories, ~26 with Runs | Cross-repository multi-selections are ordinary, so R17's escalation fires often |

## Open questions

1. **The re-run age limit is UNKNOWN.** Research suggested re-runs are only possible within roughly 30 days of the original Run, but this was **not verified directly**. R15 deliberately declines to gate on it. Verify against the live API. If real, the limit changes what the Feed should offer on old Runs.
2. **What a re-run of failed Jobs produces is UNKNOWN.** Whether the new Attempt contains only the re-run Jobs, or all Jobs with prior successes carried across, determines what Run detail shows afterwards. Not established by the canon.
3. **The point cost of cancel, force-cancel and re-run is UNKNOWN.** Only DELETE has a cost at all, at 5 points, and that one is **documented rather than measured**: establishing a point cost by experiment means tripping the secondary limit, which PRD risk R4 forbids permanently. R23's budget maths for bulk lifecycle assumes the three are comparable to DELETE, which is an assumption on top of a citation. Belongs to [rate-governor](../rate-governor/requirements.md), whose open question 3 owns it and carries three more operations besides.
4. **Whether the debug-logging option applies to re-run failed Jobs as well as re-run is UNKNOWN.** R14 claims it only for re-run.
5. **The set of cancelable Status values is UNKNOWN.** R5 and R20 rely on 409 meaning "not cancelable", but which of `queued`, `in_progress`, `waiting`, `requested` and `pending` are cancelable has not been measured, so R19's pre-filter by Status cannot yet be written precisely.
6. **When to offer force-cancel is undecided.** R6 offers it when cancel "does not take effect", but cancel is asynchronous, so there is no immediate failure to detect. The observable signal that separates "cancel accepted and still working" from "cancel accepted and stuck" (and how long to wait before escalating) is undecided, and a plain 409 (R5) is the only unambiguous trigger identified so far.
7. **Whether to warn before a re-run that discards inspectable Jobs is undecided.** R12's constraint means re-running a Run whose Jobs are open makes those Jobs permanently unreachable. That is a one-way door with no warning attached.
8. **R18's asymmetry is a judgement call.** Single-Run re-run takes no confirmation, though it spends Actions minutes and cannot be undone. If that proves wrong in use, it is a one-line change.

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
- [cli-surface](../cli-surface/requirements.md). The non-interactive form of these four operations
