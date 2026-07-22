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
	return resp.Header.Get(rateLimitedHeader) == "true"
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
// carries the endpoint's terminal resource noun (permissions, runs, caches). A
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

// terminalResource is the endpoint's last significant path segment, lowercased:
// the resource being acted on, skipping numeric IDs. It is what a reference
// page's path echoes (runs -> workflow-runs, permissions -> permissions).
// Segments shorter than four characters are dropped so a positional owner or
// repo (o, r, cli) can never stand in for a resource; the shortest real Actions
// resource nouns (runs, jobs, logs) are four characters.
func terminalResource(path string) string {
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.ToLower(segs[i])
		if len(s) < 4 {
			continue
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
