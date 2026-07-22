package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/ghlink"
)

// apiRunsPage decodes an actions/runs listing. total_count is deliberately not read:
// a filtered listing inflates it past the silent 1,000 cap while the array stops
// there, so it is never a number to stand behind (R2, R3, ADR-0005). The crawl is
// unfiltered, so it never stops on an empty page either; it stops when rel="next"
// disappears from the Link header, which is the true end (ADR-0005).
type apiRunsPage struct {
	WorkflowRuns []domain.Run `json:"workflow_runs"`
}

// Crawl resolves the affected set: it walks each repository's Run list UNFILTERED,
// following rel="next" to the end past the 1,000 cap, and applies flt's predicates
// client-side, building a RunItem for every match (R1, R2, R3, R15). The count is
// exact because it comes from the crawl and never from total_count (R3, AC4).
//
// This is the one resolution both surfaces share (cli-surface R10, R20, purge R30):
// cli's --dry-run stops after Plan over these Items and prints them, and the real
// Purge Confirms and Executes them, so --dry-run cannot diverge from or precede the
// real resolution. The crawl honours ctx so a cancelled Purge stops mid-crawl.
func (o *Ops) Crawl(ctx context.Context, repos []domain.RepoID, flt filter.Filter) ([]Item, error) {
	var items []Item
	for _, repo := range repos {
		path := fmt.Sprintf("repos/%s/%s/actions/runs?per_page=100", repo.Owner, repo.Name)
		for path != "" {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, next, err := o.crawlPage(ctx, path)
			if err != nil {
				return nil, err
			}
			for i := range page {
				r := page[i]
				r.Repo = repo // stamp the owning repository the payload does not carry
				if flt.Match(r) {
					items = append(items, RunItem(r))
				}
			}
			path = next
		}
	}
	return items, nil
}

// crawlPage issues one unfiltered listing request through the chain and returns its
// Runs and the next page's URL, empty when rel="next" is gone (R1, ADR-0005). The
// caller owns the body and this closes it.
func (o *Ops) crawlPage(ctx context.Context, path string) ([]domain.Run, string, error) {
	resp, err := o.client.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("crawl %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("crawl %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("crawl %s: read body: %w", path, err)
	}
	var page apiRunsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("crawl %s: decode: %w", path, err)
	}
	return page.WorkflowRuns, ghlink.Next(resp.Header.Get("Link")), nil
}
