package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Requester issues a request through the transport chain and returns the response for the caller to
// read and close. It is ghclient.Client's Request surface (ADR-0012), narrowed to the two reads the
// form makes: the Workflow YAML at a ref, and the repository's environments. A cassette-backed
// ghclient fills it in production and in tests, so a fetch replays what the API actually said with no
// live network.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// ClientFetch is the production Fetcher, wired in main.go over the shared ghclient (ADR-0015). It
// reads a Workflow's YAML at the target ref through the Contents API (R5), and the repository's
// environments (R7). Each request travels the store-then-governor chain, so the governor accounts it
// and the store may revalidate it.
type ClientFetch struct {
	client Requester
}

// NewClientFetch returns a ClientFetch over the shared client.
func NewClientFetch(client Requester) ClientFetch {
	return ClientFetch{client: client}
}

// DefaultBranch fetches the repository's default branch, which the form defaults the ref picker to
// (R23). It is the only ref where a workflow_dispatch is guaranteed present, and it matches gh
// workflow run. An error yields an empty string, which the pane falls back on.
func (c ClientFetch) DefaultBranch(repo domain.RepoID) (string, error) {
	resp, err := c.client.Request(http.MethodGet, "repos/"+repo.Owner+"/"+repo.Name, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	return payload.DefaultBranch, nil
}

// WorkflowYAML fetches a Workflow's YAML at the target ref through the Contents API, using the
// Workflow's own path so the schema is the one for the ref that will run (R2, R5). The Contents
// response carries the file base64-encoded, which this decodes. A non-2xx (a 404 for a path absent
// at the ref) arrives as an error the RESTClient raises, and a decode failure is an error too; both
// reach the pane, which surfaces an explicit failure naming the ref and the path (R12), never an
// untyped fallback.
func (c ClientFetch) WorkflowYAML(repo domain.RepoID, path, ref string) ([]byte, error) {
	resp, err := c.client.Request(http.MethodGet, contentsPath(repo, path, ref), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Encoding == "base64" {
		// The Contents API wraps the base64 at a fixed width with newlines, which StdEncoding
		// rejects, so they are stripped before decoding.
		return base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	}
	return []byte(payload.Content), nil
}

// Environments fetches the repository's environments for the environment selects (R7). It is called
// at most once per form render, and only when the form declares an environment input (the pane's
// HasEnvironmentInput gate). An error or an empty list yields no environments, so the select shows
// the declared default alone rather than blocking the form.
func (c ClientFetch) Environments(repo domain.RepoID) ([]string, error) {
	resp, err := c.client.Request(http.MethodGet, environmentsPath(repo), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Environments []struct {
			Name string `json:"name"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(payload.Environments))
	for _, e := range payload.Environments {
		names = append(names, e.Name)
	}
	return names, nil
}

// contentsPath is the Contents API endpoint for a file at a ref (R5). The ref is query-escaped so a
// branch or tag carrying a slash resolves as one parameter.
func contentsPath(repo domain.RepoID, path, ref string) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/contents/" + path + "?ref=" + url.QueryEscape(ref)
}

// environmentsPath is the repository environments endpoint (R7).
func environmentsPath(repo domain.RepoID) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/environments"
}
