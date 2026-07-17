package domain

import "time"

// Workflow is an automation definition. Its Runs outlive it (CONTEXT.md).
type Workflow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Repo RepoID `json:"-"`
}

// Job is a unit of work within an Attempt, with its own Status and
// Conclusion (CONTEXT.md). Steps arrive inline at no extra request
// (run-detail, resolved open question 1).
type Job struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	RunAttempt  int        `json:"run_attempt"`
	Name        string     `json:"name"`
	Status      Status     `json:"status"`
	Conclusion  Conclusion `json:"conclusion"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
	Steps       []Step     `json:"steps"`
}

// Step is one command or Action invocation within a Job, in the
// measured shape: number, name, status, conclusion, started_at,
// completed_at.
type Step struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      Status     `json:"status"`
	Conclusion  Conclusion `json:"conclusion"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
}
