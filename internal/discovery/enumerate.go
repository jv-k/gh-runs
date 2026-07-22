package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// apiRepo is the fragment of a /user/repos entry discovery reads. The permissions
// object, archived and disabled all arrive here at no extra request (R7, AC7), so
// gating costs nothing. No field of this object says whether the repository has
// Runs (R2): has_issues and its siblings describe other features, and Actions has
// no such flag, which is the whole reason a probe exists.
type apiRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions domain.Permissions `json:"permissions"`
	Archived    bool               `json:"archived"`
	Disabled    bool               `json:"disabled"`
}

// enumerated pairs a host-qualified identity with the capability data that rode
// along with it, so classification can build a Record without re-reading the
// enumeration payload.
type enumerated struct {
	id   domain.RepoID
	repo apiRepo
}

// enumerate walks the account's repository list, following the Link header's
// rel="next" until it disappears (R1). The cost is the page count and no more: at
// reference scale two pages of 100 and 63 cost exactly two requests, and a third
// page is never requested because rel="next" is absent on the second (AC1). It
// rejects any repository resolving to a host other than github.com (R18, AC14),
// which for a /user/repos enumeration cannot happen but is enforced at the one
// place every identity is built so the rule has no hole.
//
// It trusts rel="next" rather than total_count. total_count is honest on an
// unfiltered listing, but the loop needs no count: it stops when the server stops
// offering a next page, which is the unfiltered Link's documented behaviour
// (ADR-0005).
func (d *Discovery) enumerate(ctx context.Context) ([]enumerated, error) {
	var out []enumerated
	path := enumeratePath
	for path != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		repos, next, err := d.enumeratePage(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
			owner := r.Owner.Login
			name := r.Name
			if owner == "" || name == "" {
				owner, name = splitFullName(r.FullName, owner, name)
			}
			id, err := newRepoID(githubHost, owner, name)
			if err != nil {
				// A /user/repos entry is always github.com, so this cannot fire
				// here; enforcing it anyway keeps AC14's rule single-sourced.
				return nil, err
			}
			out = append(out, enumerated{id: id, repo: r})
		}
		path = next
	}
	return out, nil
}

// enumeratePage issues one enumeration request and returns its repositories and
// the next page's URL, empty when the listing is exhausted. The request goes
// through ghclient.Request and therefore through the governor (R17) and the store
// (R12): a re-enumeration whose pages have not changed answers 304 and costs no
// primary allowance. The caller owns the body and this closes it.
func (d *Discovery) enumeratePage(ctx context.Context, path string) ([]apiRepo, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	resp, err := d.opts.Client.Request(http.MethodGet, path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("enumerate %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("enumerate %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("enumerate %s: read body: %w", path, err)
	}
	var repos []apiRepo
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, "", fmt.Errorf("enumerate %s: decode: %w", path, err)
	}
	return repos, nextLink(resp.Header.Get("Link")), nil
}

// splitFullName recovers owner and name from full_name ("owner/repo") when the
// nested owner.login or name were absent, so an enumeration payload that omits
// one still yields a qualified identity. It preserves any component already known.
func splitFullName(fullName, owner, name string) (string, string) {
	if fullName == "" {
		return owner, name
	}
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return owner, name
	}
	if owner == "" {
		owner = parts[0]
	}
	if name == "" {
		name = parts[1]
	}
	return owner, name
}

// nextLink extracts the rel="next" URL from a Link header, or "" when none is
// present (R1). An unfiltered listing's Link is honest: rel="next" disappears at
// the true end (ADR-0005), so its absence is the loop's stop condition.
//
// The parse is bracket-aware rather than a comma split, because the enumeration
// URL carries commas of its own: affiliation=owner,collaborator,organization_member
// (R1). A naive split on the Link header's entry separator would tear that query
// apart and never find the next page. Each link is a <URL> followed by its
// parameters up to the next '<', so the scan walks angle-bracket pairs and reads
// the parameters between them.
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
		url := header[lt+1 : gt]

		rest := header[gt+1:]
		params := rest
		if next := strings.IndexByte(rest, '<'); next >= 0 {
			params = rest[:next]
			header = rest[next:]
		} else {
			header = ""
		}
		if relIsNext(params) {
			return url
		}
	}
	return ""
}

// relIsNext reports whether a link's parameter list declares rel="next",
// tolerating the quoting and spacing GitHub uses. It splits on both attribute
// separators so a stray comma before the next entry does not fold two attributes
// into one.
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
