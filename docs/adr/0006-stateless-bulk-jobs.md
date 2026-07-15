# Purges are stateless, and the filter is the job state

A Purge of roughly 18,000 Runs takes about 100 minutes at the delete ceiling, so it cannot be a modal you wait on. Rather than build a job store, we exploit the fact that **deletion is naturally idempotent**. Re-run the same Purge and the already-deleted Runs are simply no longer in the result set. The filter *is* the durable state.

Resuming means running the same Purge again. It is self-correcting after a crash, a quit, or a kill.

## Considered Options

**Persisted job queue.** Write the resolved run-ID list and progress to disk, then offer to resume exactly. Gives accurate cross-session progress and an audit trail, at the cost of a job store, schema versioning, and reconciliation for IDs that vanished underneath you. That rebuilds, badly, what the filter provides free.

## Consequences

The costs are real and accepted. Resuming pays a re-crawl, and cumulative progress across sessions is not shown, so a resumed Purge reports only what *this* pass did.

This composes with the failure contract, where a **404 counts as success**. Under a stateless model, racing against a previous pass or another person is routine rather than exceptional, and "the Run is gone" is exactly what was asked for. Note that the 404 rule does not generalise beyond deletion: on re-run a 404 is a failure, because a deleted Run cannot gain an Attempt, and on cancel it is a skip, because gone means not running.
