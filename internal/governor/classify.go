package governor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// rateLimitedHeader is stamped onto every observed response so a consumer reads
// the governor's verdict off one header rather than re-deriving open question 1's
// discrimination (R14). It is a synthetic, internal header: GitHub never sends
// it, and no request ever carries it.
const rateLimitedHeader = "X-Ghruns-Ratelimited"

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
// rate limiting unless it matches the measured authorization shape, because a
// secondary-limit 403 can arrive with a healthy x-ratelimit-remaining and the
// header cannot tell the two apart. Defaulting the ambiguous 403 to rate limiting
// is the safe direction: misreading a rate limit as authorization keeps issuing
// and risks the account block this component exists to avoid. A Retry-After means
// rate limiting outright. classify may read the 403 body, and always restores it
// for the consumer.
func classify(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
		return !isAuthorizationShape(resp)
	default:
		return false
	}
}

// isAuthorizationShape reports whether a 403's body carries the measured
// authorization shape: both a message naming the missing permission and a
// documentation_url pointing at the endpoint's reference page. It reads the body
// and restores it, because the consumer (repo-discovery R10, purge R20) still
// needs it. GitHub's secondary-limit body text is not published, so we match on
// the authorization shape we have measured and treat everything else as a limit.
func isAuthorizationShape(resp *http.Response) bool {
	if resp.Body == nil {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	var payload struct {
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return payload.Message != "" && payload.DocumentationURL != ""
}
