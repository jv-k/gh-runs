// Package domain holds the core types the tool reasons about. It performs no
// I/O and imports nothing internal, which is the one rule ADR-0011 fixes at the
// root of the tree. Every type here is transcribed from ADR-0014, measured
// against the live API: the vocabulary is fixed once so later stages do not
// each invent it.
package domain

// Status is where a Run, Job or Step is in its lifecycle.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusWaiting    Status = "waiting"
	StatusRequested  Status = "requested"
	StatusPending    Status = "pending"
)

// Conclusion is how a Run, Job or Step ended. Its zero value means
// "not yet concluded": the API serves null until Status reaches completed.
type Conclusion string

const (
	ConclusionNone           Conclusion = ""
	ConclusionSuccess        Conclusion = "success"
	ConclusionFailure        Conclusion = "failure"
	ConclusionCancelled      Conclusion = "cancelled"
	ConclusionSkipped        Conclusion = "skipped"
	ConclusionTimedOut       Conclusion = "timed_out"
	ConclusionNeutral        Conclusion = "neutral"
	ConclusionActionRequired Conclusion = "action_required"
	ConclusionStale          Conclusion = "stale"
	ConclusionStartupFailure Conclusion = "startup_failure"
)

// State is a Workflow's lifecycle value. A Workflow has a State and
// never a Status or a Conclusion.
type State string

const (
	StateActive             State = "active"
	StateDisabledManually   State = "disabled_manually"
	StateDisabledInactivity State = "disabled_inactivity"
	StateDisabledFork       State = "disabled_fork"
	StateDeleted            State = "deleted"
)
