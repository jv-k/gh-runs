package storage

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghlink"
)

// Requester issues a request through the transport chain and returns the response for the
// caller to read and close. It is ghclient.Client's surface narrowed to what the storage
// fetch uses (ADR-0012: Request, never Get or Do, so the response's headers and body
// survive, which is how R3's pagination is detected from the Link header). A cassette-backed
// ghclient.Client fills it in production; a gated fake fills it in a path-and-decode test.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Fetch reads one repository's storage: the cache-usage totals, the enumerated Cache list,
// and the enumerated Artifact list (storage-reclamation R1, R7, R9, R12). It returns a
// RepoStorage carrying any error rather than a Go error, because a 403 on a fine-grained PAT
// is an outcome the view records and continues past, never a fault that fails the fan-out
// (R21). The tab issues one per in-scope repository (R0).
type Fetch func(domain.RepoID) RepoStorage

// apiCacheUsage is the cache-usage endpoint's two exact figures, both in one request (R1).
type apiCacheUsage struct {
	ActiveCachesSizeInBytes int64 `json:"active_caches_size_in_bytes"`
	ActiveCachesCount       int   `json:"active_caches_count"`
}

// apiCacheList is the Cache listing's envelope. The Caches decode straight into the domain
// type, which carries the API's field names (ADR-0011), so last_accessed_at rides along for
// R7 at no extra mapping.
type apiCacheList struct {
	ActionsCaches []domain.Cache `json:"actions_caches"`
}

// apiArtifactList is the Artifact listing's envelope. expired and expires_at ride along on
// the domain type for R9 and R12.
type apiArtifactList struct {
	Artifacts []domain.Artifact `json:"artifacts"`
}

// ClientFetch is the production Fetch, wired in main.go over the ghclient the whole tool
// shares (ADR-0015). Each request travels the store-then-governor chain, so the governor
// accounts it and the store may revalidate it. The two Cache figures come from one request
// and are never recomputed from the enumerated list (R1); the Cache list's completeness is
// the tab's to reconcile against active_caches_count (R2); the Artifact list is labelled a
// complete enumeration only when the Link header carries no rel="next", else an estimate
// (R3). A non-2xx on any request is recorded on the RepoStorage and the fan-out continues
// (R21).
func ClientFetch(client Requester) Fetch {
	return func(repo domain.RepoID) RepoStorage {
		rs := RepoStorage{Repo: repo}

		var usage apiCacheUsage
		if err := getJSON(client, usagePath(repo), &usage, nil); err != nil {
			rs.Err = err
			return rs
		}
		rs.ActiveCachesSizeInBytes = usage.ActiveCachesSizeInBytes
		rs.ActiveCachesCount = usage.ActiveCachesCount

		var caches apiCacheList
		if err := getJSON(client, cachesPath(repo), &caches, nil); err != nil {
			rs.Err = err
			return rs
		}
		rs.Caches = stampRepo(caches.ActionsCaches, repo)

		var arts apiArtifactList
		var artifactsComplete bool
		if err := getJSON(client, artifactsPath(repo), &arts, func(h http.Header) {
			artifactsComplete = ghlink.Next(h.Get("Link")) == "" // R3: no next page means the enumeration is complete
		}); err != nil {
			rs.Err = err
			return rs
		}
		rs.Artifacts = stampArtifactRepo(arts.Artifacts, repo)
		rs.ArtifactsComplete = artifactsComplete
		return rs
	}
}

// getJSON issues one GET, decodes the body into v, and hands the response header to onHeader
// before it closes so the caller can read the Link header (R3). A non-2xx is an error the
// caller records under R21.
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

// stampRepo sets each Cache's repository, which the API's payload does not carry (it is
// json:"-" on the domain type), so a merged list across repositories keeps each row's owner
// (R0, R4).
func stampRepo(caches []domain.Cache, repo domain.RepoID) []domain.Cache {
	for i := range caches {
		caches[i].Repo = repo
	}
	return caches
}

func stampArtifactRepo(arts []domain.Artifact, repo domain.RepoID) []domain.Artifact {
	for i := range arts {
		arts[i].Repo = repo
	}
	return arts
}

// statusError reports a non-2xx storage response, so a 403 on a fine-grained PAT is recorded
// as an outcome rather than swallowed (R21).
type statusError struct {
	path string
	code int
}

func (e *statusError) Error() string {
	return "storage: " + e.path + ": HTTP " + http.StatusText(e.code)
}

// usagePath is the cache-usage endpoint, which returns active_caches_size_in_bytes and
// active_caches_count in one request with no arithmetic to get wrong (R1).
func usagePath(repo domain.RepoID) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/cache/usage"
}

// cachesPath is the Cache listing, sortable by size and exposing last_accessed_at (R7). The
// query asks the API's page ceiling; the tab reconciles the returned list against R1's count
// (R2).
func cachesPath(repo domain.RepoID) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/caches?per_page=100&sort=size_in_bytes"
}

// artifactsPath is the Artifact listing, exposing expired and expires_at (R9, R12). One page
// at the ceiling; completeness is read from the Link header (R3).
func artifactsPath(repo domain.RepoID) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/artifacts?per_page=100"
}
