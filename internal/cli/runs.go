package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// apiRunsPage is the fragment of an actions/runs listing the CLI reads. It
// decodes workflow_runs and ignores total_count for anything the operator sees: a
// filtered listing inflates total_count past the silent 1,000 cap while the array
// stops there (cli-surface R16, ADR-0005), so the CLI reports the rows it holds
// and never total_count as a reachable number.
type apiRunsPage struct {
	TotalCount   int          `json:"total_count"`
	WorkflowRuns []domain.Run `json:"workflow_runs"`
}

// listRuns lists across every repository in scope, merges the results into one
// list newest-first by CreatedAt, and caps it at limit (cli-surface R23). Under
// fan-out the cap bounds the merged total, never L per repository (AC16): each
// repository yields at most its newest limit matches, so the global newest limit
// is always a subset of their union, and one sort-and-truncate gives the answer.
// The sort key is CreatedAt, the field the merge is defined on (R23), not the
// Feed's run_started_at.
func listRuns(client Requester, repos []domain.RepoID, flt filter.Filter, limit int) ([]domain.Run, error) {
	query := flt.Query()
	var merged []domain.Run
	for _, repo := range repos {
		runs, err := listRepo(client, repo, query, flt, limit)
		if err != nil {
			return nil, err
		}
		merged = append(merged, runs...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].CreatedAt.After(merged[j].CreatedAt)
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// listRepo lists one repository's newest matching Runs, up to limit. It pushes the
// filter's server-side half as query parameters (filter.Query) and applies the
// rest client-side (filter.Match), which is the same split every consumer of the
// engine makes (ADR-0016). It follows the Link header's rel="next" only as far as
// it needs to fill limit matches, so a client-side-only axis that drops rows from
// a page (a workflow selected by name, cli-surface says filter.Match owns it)
// still returns limit matches rather than one short page of them.
func listRepo(client Requester, repo domain.RepoID, query url.Values, flt filter.Filter, limit int) ([]domain.Run, error) {
	path := firstPath(repo, query, limit)
	var collected []domain.Run
	for path != "" && len(collected) < limit {
		page, next, err := getRunsPage(client, path)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			r := page[i]
			r.Repo = repo
			if flt.Match(r) {
				collected = append(collected, r)
				if len(collected) >= limit {
					break
				}
			}
		}
		path = next
	}
	return collected, nil
}

// firstPath builds the first page's path: the runs endpoint, the filter's
// server-side query parameters, and per_page. per_page is limit capped at the
// API's 100 maximum, so the common default of 20 is one page and the merge sees a
// repository's newest matches without over-fetching (cli-surface R23). The keys
// are url.Values-encoded, so the path is deterministic and a cassette can match it
// exactly (R19).
func firstPath(repo domain.RepoID, query url.Values, limit int) string {
	q := url.Values{}
	for k, vs := range query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	perPage := limit
	if perPage > 100 {
		perPage = 100
	}
	q.Set("per_page", strconv.Itoa(perPage))
	return fmt.Sprintf("repos/%s/%s/actions/runs?%s", repo.Owner, repo.Name, q.Encode())
}

// getRunsPage issues one listing request through the chain and returns its Runs
// and the next page's URL, empty when the listing is exhausted (cli-surface R19).
// The caller owns the body and this closes it. A conditional request the store
// answered from a 304 arrives here as an ordinary 200, reconstituted below this
// surface (local-store R19b, ADR-0012).
func getRunsPage(client Requester, path string) ([]domain.Run, string, error) {
	resp, err := client.Request(http.MethodGet, path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("list %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("list %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("list %s: read body: %w", path, err)
	}
	var page apiRunsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("list %s: decode: %w", path, err)
	}
	return page.WorkflowRuns, nextLink(resp.Header.Get("Link")), nil
}

// nextLink extracts the rel="next" URL from a Link header, or "" when none is
// present. The scan walks angle-bracket pairs rather than splitting on commas,
// because a GitHub Actions listing URL can carry commas of its own in a query, so
// a naive split would tear the URL apart. It mirrors discovery's own Link parse,
// kept local because a package may not reach into another's unexported helpers
// (ADR-0011).
func nextLink(header string) string {
	for header != "" {
		lt := strings.IndexByte(header, '<')
		if lt < 0 {
			return ""
		}
		gt := strings.IndexByte(header[lt:], '>')
		if gt < 0 {
			return ""
		}
		gt += lt
		link := header[lt+1 : gt]

		rest := header[gt+1:]
		params := rest
		if nextLT := strings.IndexByte(rest, '<'); nextLT >= 0 {
			params = rest[:nextLT]
			header = rest[nextLT:]
		} else {
			header = ""
		}
		if relIsNext(params) {
			return link
		}
	}
	return ""
}

// relIsNext reports whether a link's parameter list declares rel="next",
// tolerating GitHub's quoting and spacing.
func relIsNext(params string) bool {
	for _, attr := range strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ',' }) {
		attr = strings.ReplaceAll(attr, "\"", "")
		attr = strings.ReplaceAll(attr, " ", "")
		if attr == "rel=next" {
			return true
		}
	}
	return false
}
