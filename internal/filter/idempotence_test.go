package filter_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestQueryMatchIdempotence pins ADR-0016's load-bearing contract: for every axis
// Query pushes, the client predicate reads the same field the server filters on,
// so applying Match to a page the server already filtered changes nothing. Each
// row asserts three things on one axis: Query pushes it, Match keeps a Run the
// server would return (the idempotence claim), and Match excludes one it would
// not (so the agreement is on the field, not by accident). This is what lets one
// representation serve the CLI, the Feed and a Purge with no second matcher.
func TestQueryMatchIdempotence(t *testing.T) {
	created, err := filter.ParseCreated("2024-01-01..2024-12-31")
	if err != nil {
		t.Fatalf("ParseCreated returned error %v", err)
	}
	cases := []struct {
		name     string
		f        filter.Filter
		queryKey string
		kept     domain.Run // a Run the server's parameter would return
		excluded domain.Run // a Run it would not
	}{
		{
			name:     "branch",
			f:        filter.Filter{Branch: "main"},
			queryKey: "branch",
			kept:     domain.Run{HeadBranch: "main"},
			excluded: domain.Run{HeadBranch: "dev"},
		},
		{
			name:     "commit rides head_sha",
			f:        filter.Filter{Commit: "abc123"},
			queryKey: "head_sha",
			kept:     domain.Run{HeadSHA: "abc123"},
			excluded: domain.Run{HeadSHA: "def456"},
		},
		{
			name:     "actor rides actor",
			f:        filter.Filter{Actor: "octocat"},
			queryKey: "actor",
			kept:     domain.Run{Actor: domain.User{Login: "octocat"}},
			excluded: domain.Run{Actor: domain.User{Login: "hubot"}},
		},
		{
			name:     "event",
			f:        filter.Filter{Event: "push"},
			queryKey: "event",
			kept:     domain.Run{Event: "push"},
			excluded: domain.Run{Event: "schedule"},
		},
		{
			name:     "created reads created_at",
			f:        filter.Filter{Created: created},
			queryKey: "created",
			kept:     domain.Run{CreatedAt: at("2024-06-15T00:00:00Z")},
			excluded: domain.Run{CreatedAt: at("2025-06-15T00:00:00Z")},
		},
		{
			name:     "status singleton is a Status",
			f:        filter.Filter{Statuses: []domain.Status{domain.StatusInProgress}},
			queryKey: "status",
			kept:     domain.Run{Status: domain.StatusInProgress},
			excluded: domain.Run{Status: domain.StatusQueued},
		},
		{
			name:     "status singleton is a Conclusion pushed as status",
			f:        filter.Filter{Conclusions: []domain.Conclusion{domain.ConclusionFailure}},
			queryKey: "status",
			kept:     domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionFailure},
			excluded: domain.Run{Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, pushed := c.f.Query()[c.queryKey]; !pushed {
				t.Fatalf("Query() did not push the %q parameter; the contract only holds for pushed axes", c.queryKey)
			}
			if !c.f.Match(c.kept) {
				t.Errorf("Match evicted a Run the server would return: idempotence broken on %q", c.queryKey)
			}
			if c.f.Match(c.excluded) {
				t.Errorf("Match kept a Run the server would not return: Match disagrees with the server field on %q", c.queryKey)
			}
		})
	}
}
