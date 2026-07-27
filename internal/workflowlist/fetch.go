// Package workflowlist reads one repository's Workflow listing, the name-to-id map three
// surfaces need and none of them may own. The Workflows tab renders it
// (workflow-management R1), the scheduler joins it against each Run's workflow_id to stamp
// Run.WorkflowState (run-detail R8, ADR-0014), and cli resolves -w NAME and the workflowName
// column through it (cli-surface). cli may never import tui and nothing may import a tab, so
// a reader wanted by a tab, an engine and the CLI has nowhere to live but its own package.
// That is the argument ADR-0011 already made for filter, applied to the same shape of fact.
//
// The package holds the reader and no view: no model, no message, no key binding. The tab
// keeps those.
package workflowlist

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghlink"
)

// Requester issues a request through the transport chain and returns the response for the
// caller to read and close. It is ghclient.Client's surface narrowed to what the Workflow
// list fetch uses (ADR-0012: Request, never Get or Do, so the response's headers survive and
// R1's completeness can be read from the Link header). A cassette-backed ghclient fills it in
// production; a gated fake fills it in a path-and-decode test.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Fetch reads one repository's Workflows: the list carrying each Workflow's id, name, path
// and state (workflow-management R1). It returns a RepoWorkflows carrying any error rather
// than a Go error, because a 403 on a fine-grained PAT is an outcome a view records and
// continues past, never a fault that fails the fan-out (repo-discovery R8's spirit, mirrored
// from storage). The Workflows tab issues one per in-scope repository (R0). A consumer with
// no view to state a failure in unwraps Err into a Go error at its own seam, which is what
// main.go does for the scheduler's WorkflowLister.
type Fetch func(domain.RepoID) RepoWorkflows

// RepoWorkflows is one repository's Workflows: the enumerated list, whether the enumeration
// completed, and any fetch error. This list is the name-to-id resolution the run-detail
// deleted-Workflow marker (issue #57) and a numeric-only -w both need.
type RepoWorkflows struct {
	Repo      domain.RepoID
	Workflows []domain.Workflow
	Complete  bool // the Link header carried no rel="next", so the enumeration is the whole list
	Err       error
}

// apiWorkflowList is the Workflow listing's envelope. The Workflows decode straight into the
// domain type, which carries the API's field names (ADR-0011), so id, name, path and state
// ride along at no extra mapping. total_count is deliberately ignored: R1 lists what the page
// carries, and a Run count (which the Workflow object does not carry) is not derived here.
type apiWorkflowList struct {
	Workflows []domain.Workflow `json:"workflows"`
}

// ClientFetch is the production Fetch, wired in main.go over the ghclient the whole tool
// shares (ADR-0015). Each request travels the store-then-governor chain, so the governor
// accounts it and the store may revalidate it. The list is one page at the API's ceiling;
// its completeness is read from the Link header (R1: an incomplete list is a partial map). A
// non-2xx is recorded on the RepoWorkflows and the fan-out continues (R7: a 403 despite push
// is an outcome, not a fault).
//
// The single page is the known gap, and it is now one gap rather than one per consumer. A
// repository with more than 100 Workflows resolves nothing past the first page: the tab says
// so through Complete, and the scheduler memoises the partial list, so its Runs read as
// not-deleted. Re-reading an incomplete list fetches the same first page forever. Paginating
// here closes it for every consumer at once, and is issue #95's follow-on rather than its
// move.
func ClientFetch(client Requester) Fetch {
	return func(repo domain.RepoID) RepoWorkflows {
		rw := RepoWorkflows{Repo: repo}
		var list apiWorkflowList
		var complete bool
		if err := getJSON(client, workflowsPath(repo), &list, func(h http.Header) {
			complete = ghlink.Next(h.Get("Link")) == ""
		}); err != nil {
			rw.Err = err
			return rw
		}
		rw.Workflows = stampRepo(list.Workflows, repo)
		rw.Complete = complete
		return rw
	}
}

// getJSON issues one GET, decodes the body into v, and hands the response header to onHeader
// before it closes so the caller can read the Link header. A non-2xx is an error the caller
// records rather than a fault.
func getJSON(client Requester, path string, v any, onHeader func(http.Header)) error {
	resp, err := client.Request(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if onHeader != nil {
		onHeader(resp.Header)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{path: path, code: resp.StatusCode}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// statusError reports a non-2xx Workflow-list response, so a 403 on a fine-grained PAT is
// recorded as an outcome rather than swallowed (R7). ghclient.Client raises an *api.HTTPError
// before this is reached, so it is the answer for a Requester that is not go-gh's REST
// client. The prefix names the endpoint the read failed on, which is what a reader of the
// message wants, and it is unchanged by the reader's move out of the Workflows tab.
type statusError struct {
	path string
	code int
}

func (e *statusError) Error() string {
	return "workflows: " + e.path + ": HTTP " + http.StatusText(e.code)
}

// stampRepo sets each Workflow's owning repository, which the API's payload does not carry
// (it is json:"-" on the domain type), so a cross-repo list keeps each row's owner (R0, R1)
// and a frozen toggle resolves to the right repository (R5).
func stampRepo(ws []domain.Workflow, repo domain.RepoID) []domain.Workflow {
	for i := range ws {
		ws[i].Repo = repo
	}
	return ws
}

// workflowsPath is the Workflow listing, carrying each Workflow's id, name, path and state
// (R1). One page at the ceiling; completeness is read from the Link header.
func workflowsPath(repo domain.RepoID) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/workflows?per_page=100"
}
