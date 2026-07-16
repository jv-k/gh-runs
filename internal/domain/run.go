package domain

import "time"

// Run is a GitHub Actions workflow run, decoded from the API's own field names.
// ADR-0011 keeps this shape distinct from the gh-compatible projection: domain.Run
// decodes what the API sends (id, status, run_started_at), and cli holds the
// databaseId/displayTitle vocabulary gh's --json flag requires. Letting the gh
// names in here would put a presentation contract at the root of the tree.
type Run struct {
	ID           int64     `json:"id"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunStartedAt time.Time `json:"run_started_at"`
	CreatedAt    time.Time `json:"created_at"`
}
