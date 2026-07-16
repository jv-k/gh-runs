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
	"io"
	"net/http"

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

// Client is a thin wrapper over go-gh's RESTClient that exposes only Request.
type Client struct {
	rest *api.RESTClient
}

// New builds the client with go-gh's own cache disabled. Only CacheTTL: 0
// disables it: EnableCache: false merely defaults the TTL to 24h, and the cache
// RoundTripper is installed unconditionally. Our transport does the revalidating
// instead, and must never run underneath a cache that would store a bare 304 as
// a response (local-store R19).
func New(opts Options) (*Client, error) {
	rest, err := api.NewRESTClient(api.ClientOptions{
		Host:      opts.Host,
		AuthToken: opts.AuthToken,
		Transport: opts.Transport,
		CacheTTL:  0,
	})
	if err != nil {
		return nil, err
	}
	return &Client{rest: rest}, nil
}

// Request issues method against path and returns the response. The caller owns
// the body and must close it. A conditional request that the store answered from
// a 304 arrives here as an ordinary 200, because the store reconstitutes it below
// this surface (local-store R19b, ADR-0012).
func (c *Client) Request(method, path string, body io.Reader) (*http.Response, error) {
	return c.rest.Request(method, path, body)
}
