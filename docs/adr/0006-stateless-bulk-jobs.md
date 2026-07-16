# Purges are stateless, and the filter is the job state

A Purge of roughly 18,000 Runs takes about **155 minutes** in the normal case, and as long as ~10 hours if the throttle spends the run at its floor ([rate-governor](../features/rate-governor/requirements.md) R20). It cannot be a modal you wait on. Rather than build a job store, we exploit the fact that **deletion is naturally idempotent**. Re-run the same Purge and the already-deleted Runs are simply no longer in the result set. The filter *is* the durable state.

Resuming means running the same Purge again. It is self-correcting after a crash, a quit, or a kill.

## Considered Options

**Persisted job queue.** Write the resolved run-ID list and progress to disk, then offer to resume exactly. Gives accurate cross-session progress and an audit trail, at the cost of a job store, schema versioning, and reconciliation for IDs that vanished underneath you. That rebuilds, badly, what the filter provides free. **The audit trail does not belong in that sentence.** See the amendment below.

## Amendment: the audit trail was bundled into the rejected option, and that was the error

**The decision above stands unchanged.** A Purge writes no job record, no resolved ID list and no progress. The filter is still the durable state, re-running the same Purge is still the resume path, and a crash still needs no reconciliation. Nothing here reopens any of that.

What is corrected is the Considered Options entry. It priced two separable things as one. Accurate cross-session progress **and** an audit trail were listed as a single benefit of the persisted job queue, and the queue's three costs (a job store, schema versioning, reconciliation for IDs that vanished underneath you) were charged against both. Only the progress half incurs them.

**An append-only log that nothing ever reads costs none of the three.** It needs no schema version, because no code parses it back. It needs no reconciliation, because no code resumes from it. It cannot disagree with reality, because it makes no claim about the present, only about what one process attempted and what the API answered. The rejected option's real defect was the resume-exactly promise. The audit trail was standing next to it when the verdict came down.

**So the boundary is not disk. It is reading.** A tool that writes a record and never opens it again is stateless in every sense this decision cares about: no schema to migrate, no reconciliation, no resume prompt, and no second source of truth to drift from the API. [purge](../features/purge/requirements.md) R23 now draws the line there rather than at the filesystem, and its R29 specifies the log.

**The gap this leaves open is the reason to close it.** A Purge deletes irreversibly at roughly 18,000 Runs per repository and has no undo ([purge](../features/purge/requirements.md) R28). Under the unamended reading, the end-of-Purge summary was the whole record, and it dies with the process: R15's progress is in-memory, R22's failure groups are in-memory, and R25 says outright that nothing cumulative is recorded. After a Purge over the wrong filter there was nothing left to tell anyone, not even the IDs. That was never a decision anybody made. It was a rider on one.

## Consequences

The costs are real and accepted. Resuming pays a re-crawl, and cumulative progress across sessions is not shown, so a resumed Purge reports only what *this* pass did.

This composes with the failure contract, where a **404 counts as success**. Under a stateless model, racing against a previous pass or another person is routine rather than exceptional, and "the Run is gone" is exactly what was asked for. Note that the 404 rule does not generalise beyond deletion: on re-run a 404 is a failure, because a deleted Run cannot gain an Attempt, and on cancel it is a skip, because gone means not running.
