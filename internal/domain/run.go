package domain

import "time"

// Run is one invocation of a Workflow, and the central object of this
// tool. JSON tags are the API's names (ADR-0011): the gh-compatible
// spellings live in cli's projection and nowhere else.
type Run struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	DisplayTitle       string     `json:"display_title"`
	RunNumber          int        `json:"run_number"`
	RunAttempt         int        `json:"run_attempt"`
	Event              string     `json:"event"`
	Status             Status     `json:"status"`
	Conclusion         Conclusion `json:"conclusion"`
	WorkflowID         int64      `json:"workflow_id"`
	HeadBranch         string     `json:"head_branch"`
	HeadSHA            string     `json:"head_sha"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	RunStartedAt       time.Time  `json:"run_started_at"`
	PreviousAttemptURL string     `json:"previous_attempt_url"`
	HTMLURL            string     `json:"html_url"`
	Actor              User       `json:"actor"`
	TriggeringActor    User       `json:"triggering_actor"`

	// Stamped by the decoding caller, never decoded. See below.
	Repo         RepoID `json:"-"`
	WorkflowName string `json:"-"`

	// WorkflowState is the owning Workflow's State, resolved by the same
	// WorkflowID join that resolves WorkflowName and stamped in the same
	// place. A deleted Workflow makes this Run an Orphaned Run, which
	// run-detail R8 marks and R18 excludes from re-run. The empty State
	// is an unresolved join, and reads as not-deleted: a Run is never
	// marked Orphaned on the strength of a join that found nothing.
	WorkflowState State `json:"-"`
}

// User is an actor reduced to the one field a requirement reads.
type User struct {
	Login string `json:"login"`
}

// EffectiveStart is the Feed's sort key: run_started_at, falling back to
// created_at where the API served null (live-run-feed R8).
func (r Run) EffectiveStart() time.Time {
	if r.RunStartedAt.IsZero() {
		return r.CreatedAt
	}
	return r.RunStartedAt
}
