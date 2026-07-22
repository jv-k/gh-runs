package ops_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// TestCrawlCountsFromTheArrayNotTotalCount pins R1, R3 and AC4: the crawl walks the
// unfiltered pages to the end and applies the filter client-side, so its matched
// count comes from the Runs it decoded, never from total_count, which the fixture
// sets to a lie (18258). status=success matches exactly the three completed+success
// Runs; the failure and the in-progress Run drop out.
func TestCrawlCountsFromTheArrayNotTotalCount(t *testing.T) {
	h := newHarness(t, "crawl_pages", 50, 50)
	var flt filter.Filter
	if err := flt.ParseStatus("success"); err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	items, err := h.ops.Crawl(context.Background(), []domain.RepoID{repoID("o", "r")}, flt)
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("Crawl matched %d Runs, want 3 (from the array, not total_count 18258) (R1, R3, AC4)", len(items))
	}
	if got := h.counting.countMethod(http.MethodGet); got != 3 {
		t.Errorf("crawl issued %d GETs, want 3 (one per page, derived by following Link) (AC5)", got)
	}
	if h.counting.deletes() != 0 {
		t.Errorf("the crawl issued %d DELETEs; a crawl is read-only (R1)", h.counting.deletes())
	}
}

// TestCrawlZeroFilterMatchesEveryRun pins that the zero-value Filter matches every
// Run (ADR-0016), which is the match-all Purge's resolution (cli-surface R26): all
// five Runs across the three pages, none dropped.
func TestCrawlZeroFilterMatchesEveryRun(t *testing.T) {
	h := newHarness(t, "crawl_pages", 50, 50)
	items, err := h.ops.Crawl(context.Background(), []domain.RepoID{repoID("o", "r")}, filter.Filter{})
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if len(items) != 5 {
		t.Errorf("zero-filter Crawl matched %d Runs, want 5 (the whole crawl) (ADR-0016)", len(items))
	}
	for _, it := range items {
		if it.Repo != repoID("o", "r") {
			t.Errorf("crawled Item carries repo %v, want o/r stamped by the crawl", it.Repo)
		}
	}
}
