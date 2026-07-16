# Notifications

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Status and Conclusion are two different fields: a Run **completes** on Status and **fails** on Conclusion.

## Purpose

Interrupt someone, through their operating system, when something has happened that is worth being interrupted for. Notifications derive from Feed state transitions and need no additional polling, so the entire design problem is which transitions qualify.

## Requirements

### What fires

**R1.** Derive every notification from a Feed state transition. Notifications must issue no request of their own, spend no Budget, and never fetch to enrich a notification's contents.

**R2.** Establish a baseline at session start. The first observation of a repository's Runs in a session (whether from persisted state, a revalidation or a full fetch) must produce no notifications, and only transitions observed after that baseline may fire. [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) requires ETags to persist across sessions, so a cold start routinely reveals everything that changed while the tool was closed. Without a baseline, launching in the morning produces a night's worth of toasts from a repository holding ~28,700 Runs.

**R3.** Fire at most one notification per transition, however many enabled events match it. A Run you triggered failing in a repository you can push to matches two events and is one thing that happened.

**R4.** Ship the event matrix with exactly these defaults:

| Event | Fires when | Default |
|---|---|---|
| An approval is blocking someone | A Run enters the approvals predicate (Conclusion becomes `action_required`, or Status becomes `waiting`) | **On** |
| A Run you triggered has failed | Conclusion becomes `failure` on a Run whose actor is the authenticated account | **On** |
| A Dispatch completes | Status reaches `completed` on a Run correlation has resolved to a Dispatch you made, whatever the Conclusion | Off |
| Any failure in a repository you can push to | Conclusion becomes `failure` in a repository whose `permissions.push` is true | Off |

**R5.** Use the approvals predicate for the approval event rather than a copy of it. The badge and the notification must be incapable of disagreeing, and the predicate spans two fields. See [approvals](../approvals/requirements.md) R2.

**R6.** Fire the Dispatch event only where correlation resolved to exactly one Run. [workflow-dispatch](../workflow-dispatch/requirements.md) R17 labels a correlated Run probable and never certain, and R18 requires ambiguity to be stated rather than resolved by picking. A notification naming the wrong Run is worse than no notification, and a system toast has nowhere to put a hedge.

**R7.** Keep every event other than R4's two defaults available and off.

**R8.** Silence the whole subsystem with one control, independent of the matrix.

**R9.** Admit no default that fails the question "would I want to be interrupted for this?". The reference user has push access to 159 repositories and ~26 with live CI, so "any failure in a repository you can push to" is a dozen interruptions a day for other people's branches. A notification feature that gets muted in week one has shipped nothing, and every subsequent event added to R4's table must clear the same bar.

**R10.** Treat the matrix as intent-level settings and expose no mechanism. "Do I want to know when my Dispatch finishes?" is answerable from a person's own context. A threshold, a coalescing window or a per-hour cap is not. This is [ADR-0007](../../adr/0007-adaptive-delete-throttle.md)'s settings philosophy applied rather than an exception to it. The same reasoning that keeps deletes-per-second and the poll interval out of Settings is what puts these in.

### Delivery

**R11.** Deliver through the operating system's own notification facility on macOS, Linux and Windows.

**R12.** Degrade silently when the channel is unavailable (no notification daemon, a headless or SSH session, permission refused by the OS). An unavailable channel must never surface an error, block the Feed, or retry.

**R13.** Report the channel's unavailability in Settings rather than offering switches that do nothing.

**R14.** Carry enough in the notification itself to be understood without acting on it: the repository, the Workflow, and what changed. Do not depend on a click-through to convey the point.

## Acceptance criteria

**AC1: A cold start is silent.** With the default matrix, replaying a cold start against a stub already holding Runs with Conclusion `failure` and Runs already matching the approvals predicate produces zero notifications.

**AC2: Notifications cost no requests.** The number of requests issued over a replayed sequence of transitions is identical with notifications enabled and disabled.

**AC3: Your own failure fires.** Given a Run whose actor is the authenticated account transitioning to Conclusion `failure`, exactly one notification fires under the default matrix.

**AC4: Someone else's failure does not.** Given the same transition on a Run whose actor is another account, no notification fires under the default matrix.

**AC5: The badge and the toast cannot disagree.** Given a Run entering the approvals predicate, one notification fires and [approvals](../approvals/requirements.md)' badge increments. A stub Run with Status `completed` and Conclusion `action_required` triggers both or neither, never one alone.

**AC6: One transition, one notification.** With "any failure in a repository you can push to" also enabled, a Run you triggered failing in a pushable repository produces exactly one notification, not two.

**AC7: The master control silences everything.** With the master control off, no transition produces a notification under any matrix.

**AC8: Ambiguous correlation stays quiet.** Given a Dispatch whose correlation matched two candidate Runs, no notification fires when either completes.

**AC9: A failed correlation is not a failed Dispatch.** Given a Dispatch whose correlation resolved to no Run, no notification fires, and the Dispatch is still reported as accepted.

**AC10: A repeated observation is not a new transition.** Given the same transition observed on two consecutive polls, one notification fires.

**AC11: An unavailable channel degrades silently.** Given a stub where the OS channel is unavailable, the Feed continues to update, no error is surfaced, and Settings reports the channel unavailable.

**AC12: `cancelled` and `skipped` are not failures.** Given a Run reaching Conclusion `cancelled` or `skipped`, no notification fires under any enabled failure event.

## Constraints

| Fact | Source | Consequence |
|---|---|---|
| A Run **fails** on Conclusion and **completes** on Status | [CONTEXT.md](../../CONTEXT.md) | R4's predicates: "failed" is Conclusion `failure`, "completes" is Status `completed`. The two events read different fields |
| Conclusion is null until Status reaches `completed` | [CONTEXT.md](../../CONTEXT.md) | Every failure event implies an already-completed Run. There is no earlier "failing" transition to catch |
| Status `completed` does **not** mean the Run finished its work: a fork-PR Run awaiting approval is `completed` with Conclusion `action_required` | Measured on `cli/cli`, `home-assistant/core` | R4's Dispatch event reads Status alone, so a Run that stopped for a human would read as finished. See Open questions |
| ETags persist across sessions | [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) | R2. A cold start is precisely when the largest batch of unseen transitions arrives |
| A 304 costs zero primary rate limit, and the Feed already polls ~26 repositories | [ADR-0004](../../adr/0004-conditional-polling-for-liveness.md) | R1 is free. Notifications add no polling and no Budget |
| Dispatch returns 204 with no Run ID. Correlation is best-effort and racy by construction | PRD, [workflow-dispatch](../workflow-dispatch/requirements.md) R16–R19 | R6. The Dispatch event is only as reliable as a correlation the canon labels *probable* |
| Reference scale: 163 repositories, ~26 with Runs, push access to 159 | PRD, decided design | R9. "Any failure you can push to" is a dozen a day for branches that are not yours |
| The reference repository holds ~28,700 Runs | PRD | R2's baseline is not a theoretical concern |
| Repo permissions ride along free on `/user/repos` | PRD | R4's push-gated event costs no request to evaluate |
| Settings are intent-level only | PRD scope, [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) | R10 |
| Three delivery backends (macOS, Linux, Windows) | | R11–R13 are a real subsystem. The platform surface is this feature's whole cost, and it buys nothing on the API side |

**This feature is an override twice over.** The product owner was recommended no notifications in 2.0.0 at all, deferred to 2.1 on the argument that cross-platform delivery is a real subsystem (the last row above is that argument), and required OS-native notifications gated in settings instead. The owner was separately recommended a narrow fixed default event set, and chose R4's configurable matrix with a conservative default. Both are recorded so that a later reader knows the scope was contested rather than assumed.

**The mechanism has now been measured, and the recommendation to defer stands on different ground.** [ADR-0013](../../adr/0013-dependency-pins.md) carries it in full. The short of it, because it changes what R11 to R13 are asking for:

| Measured | Effect here |
|---|---|
| `gen2brain/beeep v0.11.2` is pure Go and cross-compiles under `CGO_ENABLED=0` to all five release targets | **R11 is affordable.** The subsystem argument the first deferral rested on is retired. It is one require line |
| Releases are bundle-less precompiled binaries ([ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md)), so `UNUserNotificationCenter` is unreachable and macOS delivery is `osascript` | The toast is attributed to the AppleScript host rather than to gh-runs. R14's content still lands. The badge names somebody else |
| `osascript` exits 0 whether or not the toast rendered | **R13 cannot be satisfied on macOS.** There is no availability signal to report, so Settings could only claim one it cannot know |
| beeep prefers `terminal-notifier` when `exec.LookPath` finds it, and it was present on the reference machine | Delivery and attribution vary with an unrelated Homebrew package |
| beeep's Linux path is D-Bus, falling back to `notify-send` | **R12's degrade path is exactly [ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md)'s rejected case.** That ADR turned down go-keyring partly because headless Linux often has no D-Bus session bus. Same bus, same absence, and the two documents had never met |

**The recommendation is to cut this feature to 2.1.** The first deferral was argued on cost and measurement has answered it. This one is argued on correctness: on macOS the feature would attribute its toasts to another application, report an availability it cannot observe (R13), and degrade silently in the case where it was meant to work rather than the case R12 reserves. **R12's silence is a virtue when the channel is absent and a defect when the channel merely failed, and macOS cannot tell those apart.** This canon spends R24, R29 and R30 on refusing to state what it cannot know, and a Settings row reading "Notifications: available" would be the one place it does.

**The requirements below stand unchanged, and the decision is the owner's.** This scope was overridden once already and it is not being cut from underneath that. If it stays, `beeep v0.11.2` is the pin, R13 needs rewording to what a subprocess can support, and the two caveats ship with it.

## Open questions

**Resolved: `GET /user` returns `.login`.** One request, cacheable, and it resolves the account R4's "a Run you triggered" compares a Run's actor against. The question was whether the canon recorded a source for the login, not whether one was hard to find, and the answer is trivial. It belongs to [repo-discovery](../repo-discovery/requirements.md), which already reads the authenticated account's repositories and is where the account's identity is established for everything else.

**Undecided: does the approval event's default-On survive R9's test?** [approvals](../approvals/requirements.md) records that the badge "counts Runs the account may have no standing to act on", and that its count is "wrong in both directions for two independent reasons": a lower bound on what exists (their R10's bounded window) and an overcount of what is actionable, because reviewer designation is not pre-flightable. R5 binds this event to that same predicate deliberately, so the notification inherits both errors rather than escaping them. An OS toast for a Run someone else must approve is an interruption the reader can do nothing with, which is what R9's "would I want to be interrupted for this?" asks about. The event stays On until someone measures how often the overcount fires against a real account. R9 requires every event in R4's table to clear that bar, and this one has not been shown to.

**UNKNOWN: does a re-run re-attribute a Run's actor?** Re-running adds an Attempt to the Run that already exists ([CONTEXT.md](../../CONTEXT.md)), so a Run you triggered and someone else re-ran is still one Run. Whether its actor still names you decides whether their failure interrupts you.

**Undecided: do `timed_out` and `stale` count as failure?** R4 reads "failed" strictly as Conclusion `failure`. Someone asking to be told when their Run fails probably means a timeout too, while `cancelled` plainly must not fire. They cancelled it. The Conclusion enum holds **nine** values ([CONTEXT.md](../../CONTEXT.md)) and R4 currently wires one. The count was eight here until `startup_failure` was measured onto the Conclusion side of the boundary and CONTEXT.md was corrected, which [cli-surface](../cli-surface/requirements.md) records: gh's 15-value `-s` enum is exactly six Statuses plus nine Conclusions, and the arithmetic closes with nothing left over. `startup_failure` is a ninth candidate for this question, and a strong one.

**Undecided: should the Dispatch event exclude Runs blocked on a human?** Status reaching `completed` is the only signal the event has, and a `completed` Run can be one that stopped awaiting approval rather than one that finished. Reading Conclusion `action_required` as "not actually done" would fix it, at the cost of the Dispatch event knowing about approvals.

**Undecided: should bursts coalesce?** A repository-wide break can transition thirty Runs at once. Under R4's defaults a burst is unlikely (another argument for the narrow default), but enabling the push-gated event makes it reachable. R10 forbids exposing a coalescing window as a setting, which leaves coalescing ours to decide or to omit.

**Undecided: should notifications fire while the TUI is focused?** Interrupting someone about a row they are looking at fails R9's test, but terminal focus is not reliably detectable.

**Undecided: is a click-through to the Run offered?** Support varies across the three platforms, which is why R14 requires the notification to stand alone. Whether activating one navigates anywhere is unasked.

**Resolved, and the answer deletes the question: the prompt is not ours to spend.** The question assumed gh-runs owns a notification permission. It does not. Releases are bundle-less precompiled binaries ([ADR-0002](../../adr/0002-go-gh-with-dual-distribution.md)), macOS delivery is therefore `osascript`, and the permission belongs to the AppleScript host. There is no in-app prompt to offer before it and no one-shot to husband, because the prompt is another application's and may already have been answered years ago. **The honest version of this question is R13's**, which asks the tool to report the channel's availability, and `osascript` exits 0 whether the toast rendered or not. See [ADR-0013](../../adr/0013-dependency-pins.md) and the Constraints table above.

## Related

- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md). R1's free transitions, and the cross-session ETag persistence that makes R2's baseline necessary.
- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md). The intent-versus-mechanism argument R10 applies.
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md). Why every repository is already being watched.
- [approvals](../approvals/requirements.md) owns R5's predicate. The badge is the ambient state, a notification is the transition.
- [workflow-dispatch](../workflow-dispatch/requirements.md) owns R6's correlation, and labels it probable rather than certain.
- [live-run-feed](../live-run-feed/requirements.md): the transitions every event is derived from, and R2's baseline.
- [local-store](../local-store/requirements.md): the cross-session state R2 must not mistake for news.
- [polling-scheduler](../polling-scheduler/requirements.md) sets how quickly a transition is noticed. Notifications add no polling of their own.
- [settings](../settings/requirements.md) hosts R4's matrix, R8's control and R13's unavailability report.
- [ADR-0013: The dependency pins](../../adr/0013-dependency-pins.md) measured R11's three backends and pins none of them. It carries the recommendation to defer this feature to 2.1, and the pin to reach for if it stays.
- [ADR-0002: Build on go-gh with dual distribution](../../adr/0002-go-gh-with-dual-distribution.md). Bundle-less precompiled binaries are why macOS delivery is a subprocess, and its rejected go-keyring option already reasoned about the missing D-Bus session bus that R12 degrades on.
- [repo-discovery](../repo-discovery/requirements.md) supplies R4's free `permissions.push`, and the account identity question above.
