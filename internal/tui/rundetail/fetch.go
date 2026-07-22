package rundetail

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Requester issues a request through the transport chain and returns the response for the
// caller to read and close. It is ghclient.Client's surface narrowed to what the Jobs fetch
// uses (ADR-0012: Request, never Get or Do, so the response's headers and body survive). A
// cassette-backed ghclient.Client fills it in production; a gated fake fills it in a
// path-and-decode test, where the property under test is the path and the decode rather
// than the wire.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// apiJobsPage is the fragment of an actions/runs/{id}/jobs listing the pane reads: the
// Jobs, with their Steps inline (resolved open question 1). total_count is deliberately
// unread, because the pane renders whatever the latest Attempt served and builds no history
// (R5, R6), and a filtered listing's total_count is the Feed's concern, not this one's.
type apiJobsPage struct {
	Jobs []domain.Job `json:"jobs"`
}

// ClientFetch is the production Fetch, wired in main.go over the ghclient the whole tool
// shares (ADR-0015). It issues one conditional GET for a Run's Jobs and decodes the latest
// Attempt's Jobs with their Steps inline. The store's RoundTripper under it makes the
// request conditional and the governor accounts it, so an unchanged refresh is roughly free
// against the primary limit and the pane pauses on the same Budget as the Feed (R15, R16,
// AC10): the pane re-implements neither. A non-200 (a 404, a 5xx, or a rate-limit status
// the governor has already folded into the Readout) yields an empty result, which R12a
// renders as "no Jobs yet" and the fast tier retries (AC14).
func ClientFetch(client Requester) Fetch {
	return func(repo domain.RepoID, runID int64) ([]domain.Job, error) {
		resp, err := client.Request(http.MethodGet, jobsPath(repo, runID), nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, nil
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var page apiJobsPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		return page.Jobs, nil
	}
}

// jobsPath is a Run's Jobs listing, the latest Attempt's. Its only query is per_page=100, the
// API's page ceiling: resolved open question 3 measured a 38-job Run served 30 with a Link
// rel=next, and the fast tier is "one request per Run for any Run up to 100 Jobs", which holds
// only at the full page (R1). The pane follows no Link header, so anything under 100 defaults
// to 30 and silently drops the rest. The query carries neither filter=all, which was measured
// to return only the latest Attempt anyway, nor an attempts/N/jobs segment, which serves
// total_count: 0. Both are the history the API does not build, and never sending them is R6,
// AC3 and AC4 as a property of the path itself.
func jobsPath(repo domain.RepoID, runID int64) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/jobs?per_page=100"
}
