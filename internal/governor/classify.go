package governor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// rateLimitedHeader is stamped onto every observed response so a consumer reads
// the governor's verdict off one header rather than re-deriving open question 1's
// discrimination (R14). It is a synthetic, internal header: GitHub never sends
// it, and no request ever carries it.
const rateLimitedHeader = "X-Ghruns-Ratelimited"

// maxClassifyBody caps the bytes classify reads from a 403 body before restoring
// the stream. GitHub's 403 bodies are tiny; the cap only stops a pathological or
// misdirected host (a proxy, a stray GH_HOST, GHES) from ballooning memory. The
// full body is always handed back to the consumer, never a truncated one.
const maxClassifyBody = 64 << 10 // 64 KiB

// RateLimited reports whether the governor classified resp as a rate-limit
// response. Consumers apply purge R13 (never count it as a failure) and open
// question 1's per-Run bound from this verdict, without re-classifying (R14).
func RateLimited(resp *http.Response) bool {
	return RateLimitedHeaders(resp.Header)
}

// RateLimitedHeaders is RateLimited for a consumer that holds the headers without the
// response. go-gh's RESTClient converts every non-2xx into an *api.HTTPError carrying
// a copy of the response headers and returns a nil response, so a consumer above it
// has the verdict but not the thing it was stamped on.
//
// It exists so such a consumer still reads the verdict rather than re-deriving open
// question 1's discrimination from a status code (R14). A 403 is the case that
// matters: it is rate limiting or authorization depending on its body, only the
// governor has looked, and the two want opposite handling.
//
// Headers with no stamp report false, which is the safe direction for a consumer
// deciding whether to surface a failure: an unclassified response is surfaced rather
// than silently dropped.
func RateLimitedHeaders(h http.Header) bool {
	return h.Get(rateLimitedHeader) == "true"
}

// Classified reports whether the governor stamped a verdict on these headers at
// all, which is a different question from what the verdict was.
//
// RateLimitedHeaders collapses the two: an unclassified response and one classified
// as authorization both read false. That is the safe direction for a consumer asking
// "should I surface this failure", because it surfaces either way. It is the unsafe
// direction for repo-discovery R23, which counts a 403 as definitive evidence a
// repository is gone precisely when the verdict is a positive not-rate-limited. There
// an unstamped response read as authorization would retire a repository on a 403
// nobody classified, and ADR-0020 fixes that outcome the other way: unclassified is
// not definitive, so it retires nothing.
//
// A response that travelled the transport chain is always stamped, so this separates
// a real verdict from one that never reached the governor rather than from one it
// declined to give.
func Classified(h http.Header) bool {
	return h.Get(rateLimitedHeader) != ""
}

// stampRateLimited records the classification on the response the consumer will
// see. Go canonicalises the key, so RateLimited reads back what stamp wrote.
func stampRateLimited(resp *http.Response, limited bool) {
	if limited {
		resp.Header.Set(rateLimitedHeader, "true")
	} else {
		resp.Header.Set(rateLimitedHeader, "false")
	}
}

// classify decides whether resp is a rate-limit response, resolving open question
// 1 by body shape rather than by header. A 429 is always rate limiting. A 403 is
// rate limiting unless it positively matches the measured authorization shape,
// because a secondary-limit 403 can arrive with a healthy x-ratelimit-remaining
// and the header cannot tell the two apart. The default is the safe direction:
// misreading a rate limit as authorization keeps issuing and risks the account
// block this component exists to avoid. A Retry-After means rate limiting
// outright. classify may read the 403 body, and always restores it in full.
func classify(req *http.Request, resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
		return !isAuthorizationShape(req, resp)
	default:
		return false
	}
}

// isAuthorizationShape reports whether a 403 positively matches the measured
// authorization shape: a documentation_url that points at the reference page for
// the very endpoint req targets (open question 1's measurement,
// GET /repos/cli/cli/actions/permissions returning a doc URL at that endpoint's
// own reference). That correspondence is the whole discriminator. A secondary
// limit carries a documentation_url too, but it points at the rate-limits page,
// so it fails the correspondence and defaults to rate limiting. We never match on
// the body's message text, because GitHub does not publish the secondary-limit
// wording ("we must not match on a string we have never seen"), and
// x-ratelimit-remaining is not the discriminator either (both shapes can arrive
// with a healthy primary remaining). It reads the body and restores it, because
// the consumer (repo-discovery R10, purge R20) still needs it.
func isAuthorizationShape(req *http.Request, resp *http.Response) bool {
	if resp.Body == nil {
		return false
	}
	body := readCappedBody(resp)
	var payload struct {
		DocumentationURL string `json:"documentation_url"`
	}
	if json.Unmarshal(body, &payload) != nil {
		// A body over the cap or malformed JSON does not parse, so it is not the
		// measured authorization shape and falls to the safe rate-limit direction.
		return false
	}
	return docURLPointsAtEndpoint(payload.DocumentationURL, req.URL)
}

// docURLPointsAtEndpoint reports whether docURL is the docs.github.com REST reference
// page for the resource endpoint targets. The measured authorization documentation_url
// points at the endpoint's own reference page, so one of that page's path segments is
// the endpoint's resource noun (permissions, runs, artifacts), which is what
// terminalResource resolves, stepping over the trailing segments GitHub documents on a
// parent's page. Some of GitHub's pages name the resource in the other number (a Cache
// is documented at /rest/actions/cache), so both are tried. A secondary-limit doc URL
// points at a rate-limit page, whose segments name no resource of ours, so it fails this
// test and defaults to rate limiting. The check is a positive match on purpose: anything
// short of a confident correspondence is treated as a limit, which is the safe direction.
//
// Two rules keep that safety from resting on how GitHub happens to word its URLs, which
// is the only thing an earlier substring test rested on. The noun must be one we know is
// a REST resource, so the repository names and file paths that reach the terminal
// position of some endpoints cannot be compared at all. And the comparison is against
// whole path segments, so a page merely containing a noun's letters is not a match.
// TestNoResourceNounMatchesARateLimitPage checks the result over every known noun.
func docURLPointsAtEndpoint(docURL string, endpoint *url.URL) bool {
	if docURL == "" || endpoint == nil {
		return false
	}
	doc, err := url.Parse(docURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(doc.Host, "docs.github.com") {
		return false
	}
	docPath := strings.ToLower(doc.Path)
	// The REST reference lives under /rest/. A secondary-limit doc URL does too,
	// so the section is not the discriminator; the resource noun is.
	if !strings.Contains(docPath, "/rest") {
		return false
	}
	resource := terminalResource(endpoint.Path)
	// Only a noun we know is a REST resource may correspond. The terminal segment can be
	// a repository name or a file path the user chose, and neither may be compared
	// against the doc URL at all.
	if !knownResources[resource] {
		return false
	}
	for _, seg := range docSegments(docPath) {
		if segmentNames(seg, resource) {
			return true
		}
	}
	return false
}

// docSegments splits a documentation path into the segments a resource may correspond
// to, dropping the empty ones and the "rest" section marker. The marker is dropped
// because every REST doc URL carries it, the rate-limit pages included, so matching on
// it would make every 403 under /rest an authorization outcome. A locale or version
// prefix (en, free-pro-team@latest) is left in place and simply never names a resource.
func docSegments(docPath string) []string {
	var out []string
	for _, seg := range strings.Split(docPath, "/") {
		if seg == "" || seg == "rest" {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// segmentNames reports whether one documentation path segment names the resource. The
// reference page's own segment is the resource (workflows, artifacts, permissions), the
// resource behind a qualifier (workflow-runs names runs, workflow-jobs names jobs), or
// the resource in the other number (cache names caches, users names user).
//
// It compares whole segments rather than substrings, which is what keeps a resource from
// matching a page that merely contains its letters: "rate-limits-for-the-rest-api" is one
// segment, and it names no resource. The old substring test made every noun's safety a
// property of GitHub's page wording rather than of this code.
func segmentNames(segment, resource string) bool {
	if segment == resource || segment == singular(resource) || singular(segment) == resource {
		return true
	}
	return strings.HasSuffix(segment, "-"+resource) || strings.HasSuffix(segment, "-"+singular(resource))
}

// singular drops one trailing s, for the pages that name a resource in the singular
// while its endpoint says the plural. It is a spelling adjustment and not a pluraliser:
// nothing here needs one, and a real one would invent correspondences this discriminator
// has no measurement for.
//
// The length guard is hygiene and nothing more. It stops a short word being reduced to a
// stem no resource is spelled with, and it is NOT what makes the shortened form safe to
// compare: "rest", "rate", "team" and "best" are all four characters and all appear in
// real rate-limit doc paths. What makes it safe is that only a known resource reaches
// this function, and that its result is compared against whole segments.
func singular(noun string) string {
	if len(noun) > 4 && strings.HasSuffix(noun, "s") {
		return noun[:len(noun)-1]
	}
	return noun
}

// knownResources are the REST resource nouns this product's own endpoints resolve to,
// and the only nouns allowed to satisfy the correspondence test.
//
// It exists because the terminal path segment is not always ours. GET /repos/{owner}/{repo}
// has no sub-path, so its terminal segment is the repository's NAME, and
// GET /repos/{owner}/{repo}/contents/{path} puts a user-chosen file path there. Both are
// user data, and both would otherwise be compared against the documentation_url. A user
// owning a repository called "rest", "overview" or "resources" would then turn a genuine
// secondary limit into an authorization reading, and that error runs in the direction
// open question 1 exists to prevent: no backoff, no hold, and the caller keeps issuing
// into the limit. Only a noun on this list can match, so a repository may be named
// anything at all.
//
// It is also what makes the safety checkable rather than incidental. The property the
// discriminator rests on is that no resource noun appears in a rate-limit page's path,
// and with an open set that could only ever be argued. With a closed one it is a test
// (TestNoResourceNounMatchesARateLimitPage), and every future entry is checked by it.
//
// A noun missing from the list fails the safe way: it matches nothing and the 403
// defaults to rate limiting.
var knownResources = map[string]bool{
	"runs":         true, // the crawl, the scheduler's polls, the CLI's listing, every Run write
	"jobs":         true, // the Run-detail pane's job list, the log viewer's job logs
	"workflows":    true, // the Workflows tab's listing, enable, disable and Dispatch
	"caches":       true, // the Storage tab's listing and Reclamation's Cache deletion
	"cache":        true, // GET .../actions/cache/usage, which GitHub documents on the same page
	"artifacts":    true, // the Storage tab's listing and Reclamation's Artifact deletion
	"permissions":  true, // open question 1's measured authorization 403
	"repos":        true, // repo-discovery's user/repos enumeration
	"environments": true, // the Dispatch form's environment list
	"contents":     true, // the Dispatch form's Workflow file read
}

// parentDocumented are the trailing Actions path segments that GitHub documents on the
// PARENT resource's reference page rather than on one of their own. Two kinds sit here:
// a segment naming an action (cancel, enable) and a sub-resource of a Run, a Job or the
// Cache (logs, usage, pending_deployments). Either way an authorization
// documentation_url carries the parent's noun and never the segment the request path
// ends in, so stepping over them is what lets the correspondence be tested against the
// noun behind them.
//
// Without this, a fine-grained PAT's 403 on any of them cannot match the authorization
// shape and falls to the rate-limit direction: safe, and bounded by purge R19a, but it
// spends three backoffs and their waits before the reclassification, halving the write
// ramp toward its floor on each one (run-lifecycle R3, workflow-management R7, purge R13).
//
// This is every trailing segment this product's own requests carry, checked against the
// endpoints in ops, discovery, scheduler, cli and the tabs, and it is not a claim about
// GitHub's whole API. A segment missing from it fails the safe way: it stays the
// terminal resource, matches nothing, and defaults to rate limiting exactly as before.
var parentDocumented = map[string]bool{
	"cancel":              true, // POST .../runs/{id}/cancel (run-lifecycle R4)
	"force-cancel":        true, // POST .../runs/{id}/force-cancel (run-lifecycle R6)
	"rerun":               true, // POST .../runs/{id}/rerun (run-lifecycle R8)
	"rerun-failed-jobs":   true, // POST .../runs/{id}/rerun-failed-jobs (run-lifecycle R13)
	"approve":             true, // POST .../runs/{id}/approve (approvals R11)
	"pending_deployments": true, // GET and POST .../runs/{id}/pending_deployments (approvals R12)
	"logs":                true, // DELETE .../runs/{id}/logs (log-viewer R17), GET .../jobs/{id}/logs
	"enable":              true, // PUT .../workflows/{id}/enable (workflow-management R5)
	"disable":             true, // PUT .../workflows/{id}/disable (workflow-management R5)
	"dispatches":          true, // POST .../workflows/{id}/dispatches (workflow-dispatch R14)
	"usage":               true, // GET .../cache/usage (storage-reclamation R2)
}

// terminalResource is the endpoint's last significant path segment, lowercased: the
// resource being acted on, skipping numeric IDs and the segments GitHub documents on a
// parent's page. It is what a reference page's path echoes (runs -> workflow-runs,
// permissions -> permissions, runs/{id}/cancel -> workflow-runs, jobs/{id}/logs ->
// workflow-jobs). Segments shorter than four characters are dropped so a positional
// owner or repo (o, r, cli) can never stand in for a resource; the shortest real
// Actions resource nouns (runs, jobs, logs) are four characters.
func terminalResource(path string) string {
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.ToLower(segs[i])
		if len(s) < 4 {
			continue
		}
		if parentDocumented[s] {
			continue // documented on the parent's page: the noun behind it is the resource
		}
		// Parsed at a fixed 64 bits, never at the platform's int width: strconv.Atoi
		// returns ErrRange above 2^31-1 on a 32-bit build, and GitHub's Run, Cache and
		// Artifact ids passed that long ago. An id that failed this test would be
		// returned as the resource noun, match nothing, and quietly cost every
		// parent-documented endpoint three backoffs on the 386 and arm builds
		// gh-extension-precompile ships.
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			continue // a numeric ID is never the resource noun
		}
		return s
	}
	return ""
}

// readCappedBody reads at most maxClassifyBody bytes of resp.Body for
// classification and restores resp.Body so the consumer still reads it in full:
// the bounded prefix is stitched back in front of the untouched remainder with
// io.MultiReader, and the original Closer is preserved. It never hands downstream
// a truncated body, even one larger than the cap.
func readCappedBody(resp *http.Response) []byte {
	original := resp.Body
	prefix, _ := io.ReadAll(io.LimitReader(original, maxClassifyBody))
	resp.Body = &multiReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), original),
		closer: original,
	}
	return prefix
}

// multiReadCloser re-serves a body's bounded prefix ahead of its untouched
// remainder while closing the original stream.
type multiReadCloser struct {
	io.Reader
	closer io.Closer
}

func (m *multiReadCloser) Close() error { return m.closer.Close() }
