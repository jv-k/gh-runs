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

// docURLPointsAtEndpoint reports whether docURL is the docs.github.com REST
// reference page for the resource endpoint targets. The measured authorization
// documentation_url points at the endpoint's own reference page, so its path
// carries the endpoint's resource noun (permissions, runs, caches), which is what
// terminalResource resolves, stepping over a trailing sub-action to reach it. A
// secondary-limit doc URL points at the rate-limits page, which carries none of
// them, so it fails this test and defaults to rate limiting. The check is a
// positive match on purpose: anything short of a confident correspondence is
// treated as a limit, which is the safe direction.
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
	if resource == "" {
		return false
	}
	return strings.Contains(docPath, resource)
}

// subActions are the Actions endpoint segments that name an action rather than the
// resource it acts on. GitHub documents every one of them on the parent resource's
// reference page (cancel and re-run on workflow-runs, enable and disable on
// workflows), so an authorization documentation_url carries the parent's noun and
// never the segment the request path ends in. Stepping over them is what lets the
// correspondence be tested against the noun behind the action.
//
// Without this, a fine-grained PAT's 403 on any of them cannot match the
// authorization shape and falls to the rate-limit direction: safe, and bounded by
// purge R19a, but it spends three backoffs and their waits before the reclassification
// (run-lifecycle R3, workflow-management R7, purge R13).
//
// The list is the sub-actions this product writes to, and a segment missing from it
// fails the safe way: it stays the terminal resource, matches nothing, and defaults to
// rate limiting exactly as before.
var subActions = map[string]bool{
	"cancel":            true, // POST .../runs/{id}/cancel (run-lifecycle R4)
	"force-cancel":      true, // POST .../runs/{id}/force-cancel (R6)
	"rerun":             true, // POST .../runs/{id}/rerun (R8)
	"rerun-failed-jobs": true, // POST .../runs/{id}/rerun-failed-jobs (R13)
	"approve":           true, // POST .../runs/{id}/approve
	"enable":            true, // PUT .../workflows/{id}/enable (workflow-management R5)
	"disable":           true, // PUT .../workflows/{id}/disable (R5)
	"dispatches":        true, // POST .../workflows/{id}/dispatches (workflow-dispatch R16)
}

// terminalResource is the endpoint's last significant path segment, lowercased:
// the resource being acted on, skipping numeric IDs and the sub-action segments that
// name an action instead of a resource. It is what a reference page's path echoes
// (runs -> workflow-runs, permissions -> permissions, runs/{id}/cancel ->
// workflow-runs). Segments shorter than four characters are dropped so a positional
// owner or repo (o, r, cli) can never stand in for a resource; the shortest real
// Actions resource nouns (runs, jobs, logs) are four characters.
func terminalResource(path string) string {
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.ToLower(segs[i])
		if len(s) < 4 {
			continue
		}
		if subActions[s] {
			continue // the action, not the resource: the doc URL names the resource behind it
		}
		if _, err := strconv.Atoi(s); err == nil {
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
