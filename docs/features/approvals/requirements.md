# Approvals

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). **Status and Conclusion are two different fields, and this is the feature where conflating them does the most damage: the field that matches decides which request is sent.**

## Purpose

Tell someone that a Run is blocked on a human decision, and let that decision be made without leaving the Feed. The value is in being told, not in browsing. The Feed already polls every repository and already holds both fields this depends on, so approvals are a badge, a saved filter and two actions rather than a surface of their own.

## Requirements

**R1.** Add no view. Approvals are reachable through a count badge and a saved filter over the Feed. A dedicated inbox would render data the Feed already holds and stand empty almost every time it was opened.

**R2.** Match a blocked Run with a two-field predicate, and classify it by *which* field matched:

| Kind | Matches when | Action |
|---|---|---|
| Fork-PR Run awaiting approval | **Conclusion** is `action_required` (its Status is `completed`) | Approve the Run |
| Pending deployment | **Status** is `waiting` | Review the Run's pending deployments |

**R3.** Route each kind to its own action by that classification. The two take different requests (`/approve` and `/pending_deployments`), so a predicate that knows a Run is blocked but not which field said so cannot choose between them. Conflating Status and Conclusion here does not merely mislabel a row. It sends the wrong request to the API.

**R4.** Never write a predicate against Status alone. There is no Status meaning "awaiting approval": a fork-PR Run awaiting approval has Status `completed`, and a Status-only predicate silently misses every one of them.

**R5.** Evaluate the predicate client-side against Feed data already held. The badge and the filter must issue no request of their own and spend no Budget.

**R6.** Never send `conclusion` as a query parameter. The API has no such parameter and silently ignores one, returning unfiltered results. That is a wrong answer with no error and no flag. Every Conclusion filter is applied client-side.

**R7.** Count the Runs actually held that match the predicate. Never derive the badge's count from `total_count`, which in a filtered view reports matches rather than reachable matches.

**R8.** Show the badge whenever the count exceeds zero, and remove it when the count returns to zero. A badge that is always on screen is chrome, and chrome is what people stop seeing.

**R9.** Jump to the Feed with the saved filter applied when the badge is activated, and fetch nothing to do it.

**R10.** Present the count as the number of matching Runs in the Feed's current contents, and never as a repository-wide or account-wide total. The Feed reaches a bounded recent window, so the count is a lower bound on what exists.

**R11.** Offer approval of a fork-PR Run inline from its row.

**R12.** Offer review of a Run's pending deployments inline from its row: approve or reject, targeting environment ids, carrying a comment.

**R13.** Refuse to submit a deployment review with an empty comment, and issue no request while refusing.

**R14.** Treat a 403 from a deployment review as an expected outcome (the account is not a designated reviewer), and never as an error, a retry or a fault. The same holds for a 403 arriving despite `permissions.push: true`, because fine-grained PATs expose no scopes and the API is the final authority.

**R15.** Clear the badge and the filter through the Feed's ordinary update path. A successful approval changes the Run's fields, and nothing here needs a bespoke refresh.

## Acceptance criteria

**AC1: The badge counts both kinds.** Given Feed contents holding one Run with Status `completed` and Conclusion `action_required`, and one Run with Status `waiting` and Conclusion null, the badge shows 2.

**AC2: Each kind routes to its own action.** The first of those Runs is classified as a fork-PR approval and offers only the approve action. The second is classified as a pending deployment and offers only the review action. Neither offers the other's.

**AC3: A Status-only predicate misses the fork-PR case.** A predicate reading Status alone matches the second Run and not the first. This case must have a test. It is the one the prior generation of tools got wrong.

**AC4: The badge and the filter cost nothing.** Computing the count and applying the filter issue zero requests.

**AC5: No `conclusion` parameter is ever sent.** No request issued anywhere by this feature carries a `conclusion` query parameter.

**AC6: The count is what is held, not `total_count`.** Given Feed contents where a filtered listing reported `total_count: 40` while holding 2 matching Runs, the badge shows 2.

**AC7: The badge disappears at zero.** When the last matching Run leaves the predicate, the badge is not rendered.

**AC8: The badge opens no view.** Activating the badge leaves the Feed as the focused surface with the filter applied, and opens no new destination.

**AC9: An empty review comment is refused.** Submitting a deployment review with an empty comment is refused and issues no request.

**AC10: A 403 on review is an outcome, not an error.** Given a stub returning 403 for a deployment review, the outcome reads as not-a-designated-reviewer, no retry is issued, and the Run remains in the filter.

**AC11: The badge clears through the Feed's poll.** Given a stub where an approved Run's fields change on the next poll, the badge decrements with no request beyond the Feed's ordinary poll.

## Constraints

| Fact | Source | Consequence |
|---|---|---|
| A fork-PR Run awaiting approval has **`status=completed`, `conclusion=action_required`** | Measured on `cli/cli` and `home-assistant/core` | R2, R4. Counter-intuitive but decisive: the Run has stopped, and its outcome is "needs action" |
| `waiting` is a **Status** (pending deployment) | [CONTEXT.md](../../CONTEXT.md), measured | R2's second branch |
| Conclusion is null until Status reaches `completed` | [CONTEXT.md](../../CONTEXT.md) | Self-consistent with the above. These Runs carry a Conclusion precisely *because* they are completed |
| The API's `status` query parameter matches **either** Status or Conclusion | Measured | Both kinds are reachable through one parameter. [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md)'s permissive `-s/--status` mirrors the API, not merely gh's flag |
| **There is no `conclusion` query parameter**. The API silently ignores one and returns unfiltered results | Measured | R6. The failure shape is [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md)'s: silently wrong, no error, no flag |
| Presence in `gh run list --status`'s enum says nothing about field membership | [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) | The flag accepts Status *and* Conclusion values in one flag because the API's parameter does. CONTEXT.md is the authority on which field a value lives on |
| Both fields already ride on every Feed Run | [ADR-0003](../../adr/0003-multi-repo-via-client-side-fanout.md)'s fan-out | No new data source. R5 costs nothing |
| A 304 costs zero primary rate limit | [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) | The Feed already polls ~26 repositories. Approvals ride along at zero Budget |
| Filtered listing caps at 1,000, reaching the **newest** matches | [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) | R10. The count is bounded by the Feed's window |
| `total_count` reports matches, not reachable matches | [ADR-0005](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) | R7 |
| Reviewing pending deployments requires being a designated reviewer | Decided design | A 403 is an expected outcome, not an error state (R14) |
| Fine-grained PATs expose no `x-oauth-scopes` | PRD | Reviewer designation cannot be pre-flighted. R14 discovers it by acting |
| Reference scale: 163 repositories, ~26 with Runs | PRD | The fan-out already covers everything the badge counts |

## Open questions

**Resolved: `/actions/runs/{id}/pending_deployments` returns them.** The question was where the ids come from, given that a repository's environments are not the subset a given Run awaits. This endpoint serves exactly that subset: `[{environment: {id, node_id, name, url, html_url}, wait_timer, wait_timer_started_at, current_user_can_approve, reviewers}]`. R12's ids arrive with the names to label them, and `current_user_can_approve` and `reviewers` ride along. It discriminates cleanly, returning `[]` on a completed Run rather than something ambiguous. Real sample: `pytorch/pytorch` run 29350572503, environment id 3734916060, named `scribe-protected`. **R12 can be built.** One caveat, recorded rather than glossed: every `waiting` Run observed carried exactly one environment, so R12's plural is unproven rather than disproven.

**UNKNOWN: can one Run be blocked on several environments at once, and may one submission carry several ids?** R12 is written in the plural because the action targets ids. All 9 `waiting` Runs observed carried exactly one environment, which fixes the common case and says nothing about the bound. The plural is the safe way to write it either way.

**UNKNOWN: what Status and Conclusion does a Run reach after a rejected deployment review?** R15 assumes rejection moves the Run out of the predicate. If a rejected Run stays `waiting`, the badge never clears and R8 never fires.

**UNKNOWN: does approving a fork-PR Run require `permissions.push`, or something narrower?** R11 does not pre-gate, so R14 carries it either way.

**Resolved: there is no `--conclusion` flag. It is dropped.** [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) once added a `--conclusion` flag gh does not have. The measurement in the table above killed it: no such parameter exists, sending one returns unfiltered results silently, and a client-side flag would post-filter a set the server has already capped at 1,000, reporting what was reachable rather than what matches. [cli-surface](../cli-surface/requirements.md) R5 drops it and ADR-0008 records the correction. Nothing here depended on it: R5 already evaluates this feature's predicate client-side against Feed data, and R6 already forbids sending `conclusion` as a query parameter. The flag was never this predicate's route to a Conclusion, only a second consumer of the same fact.

**Undecided: should the badge say "need you"?** `action_required` and `waiting` mean a Run needs *someone*. Reviewer designation is not pre-flightable, so the badge counts Runs the account may have no standing to act on. The count is therefore wrong in both directions for two independent reasons: a lower bound on what exists (R10), and a possible overcount of what is actionable.

**Undecided: should blocked Runs be backfilled past the Feed's window?** R10 makes the count honest but not complete. A Run blocked for days in a busy repository ages out of a newest-first window while still blocking someone. The longest-blocked Runs are exactly the ones this feature exists to surface.

## Related

- [ADR-0008: A full CLI surface, mirroring gh's flags](../../adr/0008-full-cli-surface-despite-gh-overlap.md) (why `-s/--status` is permissive, and the `--conclusion` flag this feature's measurement helped drop)
- [ADR-0005: Filtered listing for the Feed, unfiltered crawl for a Purge](../../adr/0005-hybrid-filtered-live-unfiltered-purge.md) (R7 and R10, and the silent-wrong-answer failure shape R6 guards against)
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md) (why the badge is free)
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md) (why both fields are already local, and why no new data source is needed)
- [live-run-feed](../live-run-feed/requirements.md) owns the data, the window bounding R10, and the update path R15 relies on.
- [notifications](../notifications/requirements.md) reuses R2's predicate for its one default approval event, and is deferred to 2.1 ([PRD](../../PRD.md) Scope). When it ships, the badge and the toast must be incapable of disagreeing. In 2.0.0 the badge stands alone, so that coupling is a 2.1 obligation.
- [run-lifecycle](../run-lifecycle/requirements.md) covers the other Run actions taken from a Feed row, and owns cancel and re-run, not approve.
- [run-detail](../run-detail/requirements.md) is where a blocked Run's Jobs are inspected before deciding.
- [repo-discovery](../repo-discovery/requirements.md) supplies the `permissions` R14 declines to trust.
- [cli-surface](../cli-surface/requirements.md) owns the flag spelling. Its R5 drops `--conclusion` on the measurement in this feature's table, and its R6 carries the client-side validation that same measurement demands.
