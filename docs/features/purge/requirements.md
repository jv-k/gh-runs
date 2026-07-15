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

**R7.** Confirmation friction MUST scale with blast radius. A frozen set confined to one repository and below the type-the-count threshold MUST be confirmable with `y`/`N`, defaulting to no. A frozen set that spans more than one repository, or that reaches the threshold, MUST require the user to type the exact count, and no other input may start it.

**R8.** The type-the-count threshold MUST be configurable, and the configured value MUST be clamped to a hard upper bound. At or above that bound, and for any cross-repository frozen set, typing the count MUST be required regardless of configuration. Friction has a floor.

**R9.** There MUST be no setting, config key or mode that skips confirmation. In the TUI, every path from a selection to the first DELETE MUST pass through R7's graduated confirmation. The non-interactive CLI confirms differently and does not confirm less: its mandatory `--yes` ([cli-surface](../cli-surface/requirements.md) R11) is that surface's confirmation, an explicit act made once per invocation by the person typing the command, and a destructive command without it MUST refuse. That flag is not an exemption from this requirement, it is how this requirement is met where there is no modal to show. The distinction R9 draws is between a per-invocation act and a persistent state: no stored setting may stand in for either surface's confirmation, or waive the flag's requirement. v1 piped `fzf --multi` straight into a delete loop and Enter was immediately destructive. This requirement is the correction.

### Eligibility

**R10.** Deletion MUST be gated per repository on `permissions.push && !archived`, using the permission and `archived` fields that repository discovery already carries at no extra request cost. Runs in repositories failing the gate MUST NOT be attempted.

**R11.** When a frozen set mixes eligible and ineligible Runs, the confirm modal MUST state the split before the Purge starts, in the shape "3 of 47 selected Runs are in read-only repos and will be skipped". An archived repository MUST be distinguished from one merely lacking push, because archived is permanent: its Runs can never be cleaned.

**R12.** A Purge MUST exclude Runs whose Status is not `completed`, record them as skipped, and MUST NOT cancel them to make them deletable. DELETE rejects a Run that is still in progress, and cancelling is a Run lifecycle operation the user invokes deliberately.

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

**R23.** A Purge MUST NOT write a job record, a resolved ID list, or progress to disk. The filter is the durable state.

**R24.** Re-running the same Purge MUST be the resume path, and MUST converge: Runs deleted by an earlier pass are simply absent from the next crawl. A crash, a quit or a kill MUST require no reconciliation and MUST NOT produce a resume prompt.

**R25.** A summary MUST report only what this pass did. It MUST NOT claim cumulative progress across sessions, because none is recorded.

### Seams

**R26.** A Purge MUST be exercisable end-to-end against recorded HTTP fixtures, with no live network, covering the crawl and every branch of the failure contract. The crawl fixtures MUST include a repository at reference scale whose `Link` header carries `rel="next"` on every populated page and, on a page past the end, carries `prev`, `last` and `first` with no `next` (R1, AC5, open question 5). The delete fixtures MUST include a 404, a permission 403, a rate-limit response, and a DELETE rejected against a Run that is still in progress (R12, R18, R19, R20). [rate-governor](../rate-governor/requirements.md) R23 already records a bulk-delete cassette, and it is not this one: it proves pacing across 18,258 clean DELETEs and asserts nothing about which Run IDs a Purge resolved, or what a Purge did with the ones that answered 404. Pacing and selection are different claims and need different fixtures. Cassettes replay what the API actually said, which is how this project learned that a filtered listing caps at 1,000 and that `rel="last"` lies inside one. A hand-written fake would encode our reading of the `Link` header and stay green while the API changed it.

**R27.** A Purge's timing MUST come from an injected clock: the throttle's interval, R19's backoffs, and R15's elapsed and remaining-time figures. A test of a Purge at reference scale (18,258 Runs, which the governor's reachable rates put between ~2 hours and ~10 hours of wall clock, and at ~155 minutes in the normal case) MUST complete in milliseconds of real time while virtual time advances across the whole run, and no test may sleep through a backoff. AC13's circuit breaker is the case that needs this most. Its sequence of 49 failures, one success and 49 more failures is a counting property, and R19's backoff sits between every pair of them. On a real clock that test either sleeps for hours or never gets written.

**R28.** No test may issue a live DELETE. This tool deletes tens of thousands of Runs irreversibly, it has no undo, and the measurements this document rests on were taken against real third-party repositories. Deletion is exercised against cassettes, never against an account. A test that deletes only a few Runs is a test that deletes somebody's Runs. R26's fixtures are what make that rule affordable rather than a restriction, and they are also the only way R23 and AC11 become checkable: a Purge whose every request is replayed can be run to completion, killed mid-flight, and have its state directory inspected for the files it MUST NOT have written.

## Acceptance criteria

**AC1: The breakdown sums to the total.** Given a frozen set of 47 Runs drawn from 3 repositories, the confirm modal shows the number 47 and three per-repository rows whose counts sum to 47.

**AC2: The frozen set ignores arrivals.** Given a confirmed Purge, when a Run matching the same filter is invoked before the first DELETE is issued, exactly the 47 frozen Run IDs are attempted and the new Run is not.

**AC3: Re-sorting does not move the target.** Given a confirmed Purge, when the Feed re-sorts or removes rows mid-flight, the sequence of Run IDs sent to DELETE is unchanged.

**AC4: The count comes from the crawl.** Given a repository whose filtered listing reports `total_count: 18258`, a Purge on that filter reports a matched count derived from the unfiltered crawl, and that count is neither 1,000 nor 18,258 unless the crawl independently produces it. The fixture's own totals are what the assertion reads, never a constant compiled into the test. No pagination loop terminates on the first empty filtered page.

**AC5: The crawl reaches past the cap.** Given a fixture repository of ~28,700 Runs, the crawl issues on the order of 287 list requests and reaches Runs beyond result 1,000. The request count is derived from the fixture's `Link` header, not hardcoded: these totals drift ([PRD](../../PRD.md)).

**AC6: A small set confirms with `y`/`N`.** Given a single-repository frozen set below the threshold, `y` starts the Purge. `n`, Esc, and Enter on the default abort it with zero DELETE requests issued.

**AC7: A cross-repository set requires the count.** Given a frozen set spanning 2 or more repositories, `y` does not start the Purge. Only the exact count string does, and a wrong number leaves the DELETE count at zero.

**AC8: Friction has a floor.** Given a configuration setting the threshold above its hard upper bound, a frozen set at the bound still requires the count to be typed.

**AC9: No path skips confirmation.** In the TUI, no configuration, flag or key sequence produces a path from a selection to a first DELETE without an intervening confirm interaction. In the CLI, no configuration and no environment variable produces a Purge that issues a DELETE without `--yes` present on the command line.

**AC10: A Purge is not modal.** Given a Purge in flight, the Feed still applies polled updates and still accepts cursor movement and view changes.

**AC11: Cancellation stops promptly and writes nothing.** Given cancellation after N deletions, no DELETE is issued after at most one in-flight request completes, the summary reports N, and no file is created on disk.

**AC12: A 404 is success.** Given a DELETE that returns 404, the summary counts it under successes and not under failures.

**AC13: The circuit breaker counts consecutively.** Given 49 consecutive failures followed by one success followed by 49 more failures, the Purge does not stop. Given 50 consecutive failures, it stops, issues no further DELETE, and the summary names the circuit-break.

**AC14: A rate limit is not a failure.** Given a sustained sequence of rate-limit responses, none appears in the summary's failure groups, none advances the consecutive-failure counter, and the throttle's issue rate falls.

**AC14a: The backoff is bounded and the Purge does not stall.** Given a Run in a repository recording `push: true` whose every DELETE answers an authorization-shaped 403, that Run is retried at most three times, is then recorded as a skip with its reason, and the Purge proceeds to the next Run. The test asserts a terminating Purge and a bounded request count for that Run. Unbounded, this case issues 403s at the throttle's floor until the process dies (R19a).

**AC15: Ineligible Runs are skipped, not attempted.** Given a frozen set of 47 Runs of which 3 are in repositories with `push: false` or `archived: true`, the modal states that 3 of 47 will be skipped, 44 DELETE requests are issued, and the 3 are counted as skipped rather than failed.

**AC16: In-progress Runs are skipped, not cancelled.** Given a frozen set containing Runs whose Status is `in_progress`, no DELETE and no cancel request is issued for them, and each is recorded as skipped with a reason.

**AC17: Re-running is the resume.** Given a Purge that deleted 500 Runs and then quit, re-running the same Purge reports a matched count reduced by roughly those 500, shows no resume prompt, and reports only the deletions performed by the new pass.

**AC18: The summary groups failures and retries only those.** Given failures spanning two distinct reasons, the summary shows two groups with per-reason counts, and the retry keystroke issues exactly as many requests as there are recorded failures.

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

1. **The threshold numbers are UNKNOWN.** R7's default type-the-count threshold and R8's hard upper bound both need values. Nothing in the canon implies either.
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
- [ADR-0006: Purges are stateless, the filter is the job state](../../adr/0006-stateless-bulk-jobs.md). R18, R23, R24, R25
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). R14, R16, R17, R19
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). Why frozen sets span repositories
- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md). R9's non-interactive half, settled in open question 2
- [run-lifecycle](../run-lifecycle/requirements.md) shares R4–R8's frozen set and graduated confirm. It owns the cancel that R12 declines to perform
- [live-run-feed](../live-run-feed/requirements.md). The surface selection is made on, and the reason R4 exists
- [rate-governor](../rate-governor/requirements.md) implements R17 and R19
- [repo-discovery](../repo-discovery/requirements.md) supplies R10's permission and `archived` data
- [cli-surface](../cli-surface/requirements.md). The non-interactive Purge
