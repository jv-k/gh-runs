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

// StatusValues is the six Statuses, the membership list the filter engine
// validates a permissive -s value against (ADR-0016). It lives here beside the
// constants so there is one shelf of truth: filter is the single validation
// point for every consumer, and a second table one package over is the drift
// ADR-0014 defines these types to prevent. It returns a fresh slice so a caller
// cannot mutate the vocabulary.
func StatusValues() []Status {
	return []Status{
		StatusQueued,
		StatusInProgress,
		StatusCompleted,
		StatusWaiting,
		StatusRequested,
		StatusPending,
	}
}

// ConclusionValues is the nine Conclusions the filter engine validates against
// (ADR-0016). ConclusionNone is deliberately absent: "" is the null Conclusion
// carries until a Run completes, never a value a person types, and gh's 15-value
// -s enum is these nine plus the six Statuses with nothing left over (R4).
func ConclusionValues() []Conclusion {
	return []Conclusion{
		ConclusionSuccess,
		ConclusionFailure,
		ConclusionCancelled,
		ConclusionSkipped,
		ConclusionTimedOut,
		ConclusionNeutral,
		ConclusionActionRequired,
		ConclusionStale,
		ConclusionStartupFailure,
	}
}

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
