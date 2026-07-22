// Package ghclient constructs the go-gh REST client and exposes Request.
//
// It exposes Request and never Get or Do. go-gh's Get(path, &resp) and
// Do(method, path, body, &resp) return an error alone: the *http.Response is
// consumed, unmarshalled and closed inside go-gh, so its headers are unreachable.
// Two requirements die there. rate-governor R5 reads x-ratelimit-* from every
// response, and ADR-0005's crawl terminates on the disappearance of Link. Both
// need headers Get and Do have already thrown away. Request returns the response,
// so callers own the body, the close and the decode (ADR-0012).
//
// ghclient imports domain alone and never store or governor. Its transport is
// injected, so main.go stays the only place that knows the whole chain (ADR-0011).
package ghclient

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Options configures the client. Transport is the head of the transport chain
// main.go assembles: the store's RoundTripper over the governor's over the base.
type Options struct {
	// Host is the API host. Empty defaults to github.com, which is all 2.0.0
	// serves (ADR-0009).
	Host string
	// AuthToken is the token go-gh sends as "token <tok>". Empty lets go-gh
	// resolve it from the environment or the gh keyring (ADR-0002).
	AuthToken string
	// Transport is our RoundTripper chain. go-gh installs it as opts.Transport,
	// which replaces http.DefaultTransport rather than wrapping it.
	Transport http.RoundTripper
}

// Client wraps go-gh over the injected transport. It exposes two request surfaces
// over the one chain: Request, go-gh's RESTClient, which raises non-2xx as an error
// (the read half's exit-code taxonomy rests on that, cli-surface R17); and
// RequestWithContext, a raw client that returns the response for any status so a
// Purge can classify a 404, a 409 and a 403 itself (purge R18, R20). Both install
// opts.Transport, so both dial through the same store-then-governor-then-limiter
// chain and the governor accounts and paces every request either issues.
type Client struct {
	rest       *api.RESTClient
	raw        *http.Client
	restPrefix string
}

// New builds the client with go-gh's own cache disabled. Only CacheTTL: 0
// disables it: EnableCache: false merely defaults the TTL to 24h, and the cache
// RoundTripper is installed unconditionally. Our transport does the revalidating
// instead, and must never run underneath a cache that would store a bare 304 as
// a response (local-store R19). The raw http.Client is built from the same options,
// so it carries the same auth and dials the same chain, but without the RESTClient's
// non-2xx-to-error conversion, which is what a Purge needs to read a rejection.
func New(opts Options) (*Client, error) {
	clientOpts := api.ClientOptions{
		Host:      opts.Host,
		AuthToken: opts.AuthToken,
		Transport: opts.Transport,
		CacheTTL:  0,
	}
	rest, err := api.NewRESTClient(clientOpts)
	if err != nil {
		return nil, err
	}
	raw, err := api.NewHTTPClient(clientOpts)
	if err != nil {
		return nil, err
	}
	return &Client{rest: rest, raw: raw, restPrefix: restPrefix(opts.Host)}, nil
}

// Request issues method against path and returns the response. The caller owns
// the body and must close it. A conditional request that the store answered from
// a 304 arrives here as an ordinary 200, because the store reconstitutes it below
// this surface (local-store R19b, ADR-0012). Non-2xx raises an *api.HTTPError, which
// the read half maps to gh's exit codes (cli-surface R17).
func (c *Client) Request(method, path string, body io.Reader) (*http.Response, error) {
	return c.rest.Request(method, path, body)
}

// RequestWithContext issues method against path with a context and returns the RAW
// response for any status, 2xx or not: a Purge must read a 404 as gone (R18), a 409
// as an in-progress rejection (R12), and a 403 as a failure or a governor-classified
// rate limit (R19, R20), which the RESTClient's non-2xx-to-error conversion would
// hide. The context is load-bearing: it reaches the governor's pacing and the
// limiter's wire through req.Context(), so a cancelled Purge stops waiting rather
// than parking to the deadline (purge R16, ADR-0018). The caller owns the body and
// must close it. A full URL path (a crawl's Link next) is used as-is; a bare path is
// prefixed with the REST host.
func (c *Client) RequestWithContext(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := path
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = c.restPrefix + strings.TrimPrefix(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	return c.raw.Do(req)
}

// restPrefix is the REST API base for a host. 2.0.0 serves github.com only
// (ADR-0009), whose REST host is api.github.com; an empty host defaults to it. The
// full form mirrors go-gh's own restPrefix so a bare path resolves identically to
// the RESTClient's.
func restPrefix(host string) string {
	if host == "" || host == "github.com" {
		return "https://api.github.com/"
	}
	return "https://" + host + "/api/v3/"
}
