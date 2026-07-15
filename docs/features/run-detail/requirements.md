# Run Detail

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). This document assumes [PRD.md](../../PRD.md).

## Purpose

The detail pane shows the Jobs and Steps of the Run selected in the [Feed](../live-run-feed/requirements.md), staying live while that Run is in progress. It presents Attempt as a badge rather than a view, because GitHub does not serve prior Attempts' Jobs and Attempt history therefore cannot be built at all.

## Requirements

### Content

**R1.** The detail pane must show the Jobs of the selected Run and, within each Job, its Steps.

**R2.** The detail pane must render each Job's own Status and Conclusion as two separate fields, and must render an empty Conclusion for any Job whose Status is not `completed`.

**R3.** The detail pane must render each Step's Status. Whether a Step also carries a Conclusion is unresolved (see Open questions), but if it does, R2's separation must hold for it too.

**R4.** The detail pane must render the Run's Attempt number as a badge attached to the Run's identity, whenever the Run has more than one Attempt. The badge must not be placed inside the Jobs list, where it would read as a property of a Job.

**R5.** The detail pane must not offer navigation to a prior Attempt's Jobs, must not present a per-Attempt picker, and must not present Attempt as a view.

**R6.** The detail pane must not request a prior Attempt's Jobs, and must not use `filter=all` as a means of retrieving them.

**R7.** Where the detail pane surfaces `previous_attempt_url`, it must present it as prior Attempt metadata only, and must not imply that a prior Attempt's Jobs, Steps or logs are retrievable.

**R8.** When the selected Run's Workflow is in the `deleted` state (an Orphaned Run), the detail pane must mark the Workflow as deleted, so that a Run with no possible successor is distinguishable from a Run whose Workflow still exists.

**R9.** The detail pane must not present log content as live, streaming or tailing. Job and Step Status may be live. Log content may not.

### Fetching

**R10.** The detail pane must debounce its Job fetch on selection settle, at approximately 150ms, and must not issue a Job request per keystroke while the cursor moves through the Feed.

**R11.** The detail pane must discard any Job response whose Run is no longer the selected Run, and must never render one Run's Jobs under another Run's identity.

**R12.** On selection change, the detail pane must show the new Run's Jobs or an explicit pending state, and must not leave the previous Run's Jobs on screen as though they belonged to the new selection.

**R13.** The detail pane must refresh the selected Run's Jobs at the fast tier, approximately every 3 seconds, for exactly as long as that Run's Status is not `completed`, and must not refresh a Run whose Status is `completed`.

**R14.** The detail pane must stop refreshing a Run's Jobs once that Run is no longer selected.

**R15.** The detail pane must fetch conditionally, so that refreshing an unchanged Run costs nothing against the primary rate limit.

**R16.** The detail pane's refreshing must be governed by the same Budget as the Feed's. Under Budget exhaustion it must pause, state when it resumes, and must not present stale Jobs as live.

### Re-run

**R17.** When the selected Run is re-run, the detail pane must reflect that the Run mutated in place (Status returns to `queued`, Conclusion returns to null, `run_attempt` increments), and must not imply that a new Run was created. A user who expects a new row will not find one, so the Attempt number is the only evidence the re-run happened. R4's badge must be legible at that moment.

**R18.** The detail pane must gate re-run and every other mutating action on `permissions.push && !archived` for the owning repository, consistently with the Feed. The mechanics of the operations themselves belong to [run-lifecycle](../run-lifecycle/requirements.md).

### Seams

**R19.** The detail pane must render to a frame from held state alone, with no live terminal and no network, and that frame must be verified by golden-file tests covering the Jobs and Steps rendering (R1, R2, R3) and R4's Attempt badge. The badge carries the most weight of anything this pane paints: it is the whole of what the API supports for Attempt, and after a re-run it is the only evidence on screen that anything happened (R17). R4 also fixes where it goes, against the Run's identity and not inside the Jobs list, and a position is a fact about the frame and nowhere else.

## Acceptance criteria

**AC1: No fetch per keystroke.** Arrow-keying through 100 Feed rows with less than 150ms between keystrokes issues exactly one Job request: the one for the row where the cursor settles.

**AC2: Stale response discarded.** A Job response for Run A arriving after the selection moved to Run B is not rendered. The pane shows Run B's Jobs or Run B's pending state, never Run A's Jobs.

**AC3: Attempt is a badge.** For a Run with `run_attempt: 3`, the pane renders "Attempt 3" against the Run's identity, offers no control that navigates to Attempt 1 or 2, and issues no request to `/runs/{id}/attempts/1/jobs` or `/runs/{id}/attempts/2/jobs`.

**AC4: `filter=all` is not history.** No code path requests Jobs with `filter=all` in order to obtain prior Attempts.

**AC5: Job Status and Conclusion are separate.** A Job with Status `in_progress` renders an empty Conclusion. No field renders a Status value and a Conclusion value together.

**AC6: Fast tier while live.** With a Run at Status `in_progress` selected, a Job Status change appears within ~3s with no user interaction. When that Run's Status reaches `completed`, refreshing stops and no further Job request is issued for it.

**AC7: Deselection stops polling.** With an in-progress Run selected and refreshing, moving the selection to a completed Run stops all requests for the first Run within one refresh interval.

**AC8: Re-run in place.** Re-running the selected Run leaves the Feed's row count unchanged. The same Run's row repaints to Status `queued` with an empty Conclusion, and the pane's Attempt badge goes from N to N+1. No new row is created.

**AC9: Logs are not live.** While the selected Run is in progress, no surface in the pane presents log content as streaming, tailing or updating.

**AC10: Unchanged refresh costs nothing.** Refreshing a selected in-progress Run whose Jobs have not changed does not advance the primary rate limit's `used` counter.

**AC11: Orphaned Run.** For a Run whose Workflow is `deleted`, the pane marks the Workflow deleted.

**AC12: Budget exhaustion.** At Budget exhaustion, the pane stops refreshing and states that it has paused and when it resumes. Jobs already on screen are not presented as live.

**AC13: Goldens hold the pane's frame.** Rendering a recorded frame from held state, with no terminal and no network, reproduces the stored golden byte for byte. Separate goldens fix a Run's Jobs with their Steps rendered under them, a Job at Status `in_progress` with an empty Conclusion field, and a Run with `run_attempt: 3` rendering "Attempt 3" against the Run's identity and not within the Jobs list. Moving the badge into the Jobs list fails its golden.

## Constraints

**Prior Attempts' Jobs are not served.** `/runs/{id}/attempts/1/jobs` returns `total_count: 0`, and `filter=all` was verified to return only the latest Attempt's Jobs, identical to `filter=latest`. `previous_attempt_url` exposes prior Attempt *metadata* only. Attempt history is therefore not buildable, and the PRD scopes it out on that ground rather than deferring it. R4's badge is not a simplification of a richer view. It is the whole of what the API supports.

**Live log streaming does not exist.** Logs are a zip per Run or plain text per Job, delivered on completion. This is exactly why Job and Step *status* can be live while log *content* cannot, and why R9 exists. In-progress Run log behaviour is unverified (PRD risk R5), which the [log viewer](../log-viewer/requirements.md)'s empty state depends on but this pane does not.

**A re-run never creates a Run. It adds an Attempt to the Run that already exists.** The Run mutates in place: Status returns to `queued`, Conclusion returns to null, `run_attempt` increments. `created_at` and `run_started_at`, identical on 8/8 measured normal Runs, were 3 hours apart on the one measured re-run.

**Conclusion is null until Status reaches `completed`**, for a Job as for a Run. A Job carries its own Status and Conclusion, and conflating the two fields is the defining bug of the tools that came before this one.

**Conditional requests cost nothing against the primary limit.** Measured by interleaving, `used` advanced by exactly one per round (120 → 121 → 122) and that one belonged entirely to the 200. A ~3s refresh of an unchanged Run is therefore ~free against the primary limit, though we assume conservatively that 304s do count against the secondary one (~900 points/min, GET = 1 point, PRD risk R4). go-gh's own client is TTL-only and never revalidates (PRD risk R2, resolved), so those 304s come from a transport of our own ([local-store](../local-store/requirements.md) R19) rather than from the client.

**The ~150ms debounce is chosen, not measured.** Its justification is arithmetic rather than empirical: eager fetching while arrow-keying a 100-row Feed costs one request per keystroke. Per ADR-0007's principle (expose intent, not mechanism), it must not become a user-facing setting.

## Open questions

1. **Do Steps arrive with the Jobs, or need their own request?** **UNKNOWN.** R1 requires Steps to be shown. The canon does not establish whether Step data is embedded in the Jobs payload or fetched per Job. If it is separate, showing Steps for a wide matrix Run multiplies the cost of every 3s refresh, and R10's debounce and R13's tier both need re-costing.

2. **Does a Step carry a Conclusion?** **UNKNOWN.** CONTEXT grants a Job "its own Status and Conclusion" and says nothing of the kind for a Step. The PRD says only that "Job and Step *status* can be live". R3 therefore requires Step Status alone. If Steps do carry a Conclusion, R3 must extend and the Status/Conclusion separation must be enforced there too.

3. **Does the Jobs list paginate?** **UNKNOWN.** A large matrix Run may exceed one page. If it does, a 3s refresh of a wide Run is several requests rather than one, and R13's tier is more expensive than it looks.

4. **What does a Run that has not started serve?** **UNKNOWN** whether a Run at Status `queued`, `waiting`, `requested` or `pending` returns an empty Jobs list, a partial one, or an error. This defines the pane's empty state and decides whether R13's fast tier has anything to render for the Runs most likely to be selected.

5. **Is `run_attempt` on the Feed's list payload, or only on the single-Run payload?** **UNKNOWN.** If the list already carries it, R4's badge is free. If not, rendering the badge costs a request per selected Run and must be folded into R10's debounce rather than issued eagerly.

6. **Should a Run parked at `waiting` refresh at the fast tier?** R13 refreshes whenever Status is not `completed`, but a Run awaiting a pending deployment can sit at `waiting` for days. Refreshing it every 3s for days is defensible only if 304s are as cheap as measured. For the secondary limit that is PRD risk R4, assumed to go the wrong way.

7. **Are an Orphaned Run's Jobs still served?** **UNKNOWN.** CONTEXT says an Orphaned Run's history persists indefinitely, but says that of the Run, not of its Jobs. R8's marking is safe either way. R1's content is not.

8. **Is re-run rejected on an Orphaned Run?** **UNKNOWN.** CONTEXT says nothing remains that could ever produce another Run, which suggests the API refuses, but the canon does not establish it. R18's gate tests permission, not Workflow state, so as written an Orphaned Run in a writable repository still offers re-run.

9. **Does re-running failed Jobs only behave like a full re-run here?** The PRD scopes both operations. **UNKNOWN** whether "re-run failed Jobs" increments `run_attempt` and returns Status to `queued` identically, which is what R17's display assumes.

## Related

- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md)
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md) (for the intent-not-mechanism principle behind the debounce)
- Sibling features: [live-run-feed](../live-run-feed/requirements.md), [log-viewer](../log-viewer/requirements.md), [run-lifecycle](../run-lifecycle/requirements.md), [approvals](../approvals/requirements.md), [polling-scheduler](../polling-scheduler/requirements.md), [rate-governor](../rate-governor/requirements.md)
