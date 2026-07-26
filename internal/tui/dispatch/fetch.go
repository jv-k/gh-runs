package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghlink"
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
//
// This is the fallback path, not the ordinary one. default_branch rides the /user/repos payload
// discovery already reads, so the Workflows tab hands the pane the branch it discovered and R23
// costs no request (repo-discovery R7a, AC7). This call remains for the case discovery cannot
// answer: a record persisted before discovery carried the field. Nothing refreshes such a record on
// a warm start, so that session keeps paying this request per form open until the local-store is
// rebuilt (issue #100). Answering with a guess instead would put the form on the wrong ref, which R4
// says must never be ambiguous, so one request is the right price.
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

// Refs lists the repository's branches and then its tags, which is the picker set R24 requires:
// gh's --ref accepts either, so a picker offering branches alone would make the interactive surface
// a strict subset of the CLI. The pane calls it lazily, on the picker's first use and at most once
// per picker session, so a Dispatch at the default branch never pays for it.
//
// A tags read that fails or returns nothing yields the branches alone, which is R24's
// no-tags case and is also the right answer when a token can list branches but not tags: withholding
// the branches too would make a repository undispatchable at any ref but the default over a list the
// form only needed for convenience. A branches failure is an error, because a picker with no
// branches is not a picker.
//
// Both listings follow the Link header's rel="next" to exhaustion, the same walk discovery's
// enumeration takes. A single page would silently truncate a large repository's branches, and the
// truncation is not harmless: the ref the picker opened on could be absent from the set it lists.
func (c ClientFetch) Refs(repo domain.RepoID) ([]Ref, error) {
	branches, err := c.refNames(refsPath(repo, "branches"))
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(branches))
	for _, name := range branches {
		out = append(out, Ref{Name: name})
	}
	tags, err := c.refNames(refsPath(repo, "tags"))
	if err != nil {
		return out, nil
	}
	for _, name := range tags {
		out = append(out, Ref{Name: name, IsTag: true})
	}
	return out, nil
}

// refNames reads a branch or tag listing to exhaustion, both of which are arrays of objects carrying
// a name. The two endpoints differ in what else they carry and in nothing this reads. It trusts
// rel="next" rather than a count, exactly as discovery's enumeration does (ADR-0005), and stops when
// the server stops offering a next page.
func (c ClientFetch) refNames(path string) ([]string, error) {
	var names []string
	for path != "" {
		page, next, err := c.refPage(path)
		if err != nil {
			return nil, err
		}
		names = append(names, page...)
		path = next
	}
	return names, nil
}

// refPage reads one page of a branch or tag listing and returns its names and the next page's URL,
// empty when the listing is exhausted. The caller owns the loop and this owns the body.
func (c ClientFetch) refPage(path string) ([]string, string, error) {
	resp, err := c.client.Request(http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	var payload []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(payload))
	for _, p := range payload {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	return names, ghlink.Next(resp.Header.Get("Link")), nil
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

// refsPath is the branches or tags listing endpoint for the picker (R24), at the API's page ceiling.
func refsPath(repo domain.RepoID, kind string) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/" + kind + "?per_page=100"
}
