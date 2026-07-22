package approval

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Requester issues a request through the transport chain and returns the response for the
// caller to read and close. It is ghclient.Client's Request surface (ADR-0012: Request,
// never Get or Do, so the response's body survives), narrowed to the one read the pane
// makes. A cassette-backed ghclient fills it in production and in tests, so a fetch replays
// what the API actually said with no live network.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// ClientFetch is the production Fetcher, wired in main.go over the shared ghclient
// (ADR-0015). It reads a Run's pending deployments through the same store-then-governor chain
// every other read uses, so the governor accounts it and the store may revalidate it.
type ClientFetch struct {
	client Requester
}

// NewClientFetch returns a ClientFetch over the shared client.
func NewClientFetch(client Requester) ClientFetch {
	return ClientFetch{client: client}
}

// apiPendingDeployment is the fragment of the pending_deployments listing the pane reads: the
// environment with its id and name, the current_user_can_approve flag, and the reviewers.
// This endpoint serves exactly the environments the Run awaits, so the ids the review targets
// arrive with the names to label them, and it returns an empty array on a completed Run rather
// than something ambiguous (R12's resolved open question).
type apiPendingDeployment struct {
	Environment struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"environment"`
	CurrentUserCanApprove bool `json:"current_user_can_approve"`
	Reviewers             []struct {
		Type     string `json:"type"`
		Reviewer struct {
			Login string `json:"login"`
			Name  string `json:"name"`
			Slug  string `json:"slug"`
		} `json:"reviewer"`
	} `json:"reviewers"`
}

// PendingDeployments reads the environments a Run awaits (R12). A non-200 (a 404, or a
// rate-limit status the governor has already folded into the Readout) yields no environments,
// which the pane renders as none awaiting rather than an error, and the Feed's badge is the
// authority on whether a Run is awaiting at all. The reviewer labels are the author-controlled
// login, team name or slug, which the pane sanitises before painting.
func (c ClientFetch) PendingDeployments(repo domain.RepoID, runID int64) ([]PendingDeployment, error) {
	resp, err := c.client.Request(http.MethodGet, pendingDeploymentsPath(repo, runID), nil)
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
	var payload []apiPendingDeployment
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]PendingDeployment, 0, len(payload))
	for _, p := range payload {
		pd := PendingDeployment{
			EnvironmentID:         p.Environment.ID,
			EnvironmentName:       p.Environment.Name,
			CurrentUserCanApprove: p.CurrentUserCanApprove,
		}
		for _, rv := range p.Reviewers {
			pd.Reviewers = append(pd.Reviewers, reviewerLabel(rv.Type, rv.Reviewer.Login, rv.Reviewer.Name, rv.Reviewer.Slug))
		}
		out = append(out, pd)
	}
	return out, nil
}

// pendingDeploymentsPath is the pending-deployments listing for a Run, the same endpoint the
// review POSTs to (R12), so the ids read here are the ids the review targets.
func pendingDeploymentsPath(repo domain.RepoID, runID int64) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/pending_deployments"
}

// reviewerLabel is the display label for one reviewer: a User's login, else a Team's name or
// slug, else the bare type. It never invents a value, so an unrecognised reviewer shape reads
// as its type rather than blank.
func reviewerLabel(typ, login, name, slug string) string {
	switch {
	case login != "":
		return login
	case name != "":
		return name
	case slug != "":
		return slug
	default:
		return typ
	}
}
