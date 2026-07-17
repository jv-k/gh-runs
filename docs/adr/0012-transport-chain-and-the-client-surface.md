# The transport chain, and what ghclient may expose

[ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed the package tree and left one seam undescribed: what `ghclient` hands its callers, and where the governor sits relative to the store's RoundTripper. Both answers follow from go-gh's actual shape rather than from taste. Everything below was verified against **go-gh v2.13.0**, the current release. The canon's earlier readings were taken against v2.9.0, and none of this behaviour moved between them.

## ghclient exposes Request, never Get or Do

**go-gh's ergonomic surface throws the response away.** `RESTClient.Get(path string, response any) error` and `RESTClient.Do(method, path string, body io.Reader, response any) error` return an error and nothing else. The `*http.Response` is consumed inside `DoWithContext`, unmarshalled into `response`, and closed. **Response headers are unreachable through them.**

Two requirements die there:

- **[rate-governor](../features/rate-governor/requirements.md) R5** reads `x-ratelimit-remaining` and `x-ratelimit-reset` "from every response it sees". Above `Get` and `Do` it sees no responses, only errors and decoded bodies.
- **[ADR-0005](./0005-hybrid-filtered-live-unfiltered-purge.md)'s crawl** terminates on the disappearance of `rel="next"` from the `Link` header. `Get` and `Do` drop `Link` with the rest of the headers.

`RESTClient.Request(method, path string, body io.Reader) (*http.Response, error)` returns the response. So **ghclient's surface is `Request`**, and callers own the body, the close and the decode. That cost is what the two requirements above are worth, and it is not optional.

## The store's RoundTripper is innermost and takes a base

**`opts.Transport` replaces `http.DefaultTransport`. It does not wrap it.** `NewHTTPClient` reads:

```go
transport := http.DefaultTransport
if opts.UnixDomainSocket != "" { transport = newUnixDomainSocketRoundTripper(...) }
if opts.Transport != nil { transport = opts.Transport }   // replaces, never wraps
transport = newSanitizerRoundTripper(transport)
c := cache{dir: opts.CacheDir, ttl: opts.CacheTTL}
transport = c.RoundTripper(transport)
```

Ours therefore sits at the bottom of go-gh's stack with nothing underneath it, and **it must dial the network itself**. It cannot assume a base exists, so `store.NewTransport` takes a `base http.RoundTripper` as a constructor parameter. `http.DefaultTransport` is what main.go passes in production. A cassette is what a test passes, and that parameter is the only injection point cassettes have anywhere in the tree.

## The store reconstitutes a 200 from a 304

**A bare 304 travelling upward becomes an error.** `RequestWithContext` treats any non-2xx as a failure:

```go
success := resp.StatusCode >= 200 && resp.StatusCode < 300
if !success { defer resp.Body.Close(); return nil, HandleHTTPError(resp) }
```

`DoWithContext` carries the identical check. A 304 reaching go-gh therefore surfaces to the caller as `*api.HTTPError` reading "HTTP 304", on **every** surface the client offers, `Request` included. A conditional request that worked would read as a failed one.

**So the 304 must never leave the store.** [local-store](../features/local-store/requirements.md) R19's transport sends `If-None-Match`, receives the 304, and hands go-gh a synthetic 200 carrying the persisted payload. Nothing above the store learns that a 304 happened, which is what local-store R8 already asks for: the 304-versus-200 distinction is drawn **below** the reconstitution, for the store and the governor, and never above it.

### Which headers win

A reconstituted 200 is a splice of two responses, so the precedence is stated here rather than left to whoever writes it:

| Taken from the fresh 304 | Taken from the store |
|---|---|
| `x-ratelimit-limit`, `-remaining`, `-used`, `-reset`, `-resource`. They describe the exchange just made, and a persisted copy would be a stale reading of a live counter | The body, `ETag`, `Content-Type`, `Link`, and every other entity header |

The rule: **anything describing the exchange comes from the 304, anything describing the entity comes from the store.** A 304 is a statement about the request, not about the payload.

**A GitHub 304 carries no `Link`, measured.** A conditional GET against `/repos/cli/cli/actions/runs?per_page=2` returned `HTTP/2 304` carrying `etag`, `x-ratelimit-remaining: 4947` and `x-ratelimit-used: 53`, and **no `link` header**. The unconditional control against the same URL returned 200 with `link` carrying `rel="next"` and `rel="last"`. So the store's persisted `Link` is not a fallback, it is the only source, and a reconstituted 200 that omits it is a bug the caller cannot see. RFC 7232 does not require `Link` on a 304 and GitHub does not send it.

The rate-limit half is confirmed by the same measurement: a 304 does carry the `x-ratelimit-*` family, which is what lets [rate-governor](../features/rate-governor/requirements.md) R5 observe a free revalidation at all. Had it not, R7's "a 304 costs zero" would have been unobservable from inside the transport.

ADR-0005's crawl is unaffected either way, because a crawl walks fresh pages that answer 200.

## Where the governor sits

**Both are RoundTrippers, and main.go nests them.**

```go
base := http.DefaultTransport                     // a cassette, in a test
clk := clock.Real()                               // injected clock (local-store R3, R17)
dir := stateDir                                   // ETag directory main.go supplies (local-store R1)
transport := store.NewTransport(governor.New(base, clk), dir, clk)
opts := api.ClientOptions{Transport: transport, CacheTTL: 0}
```

`clk` and `dir` are not decoration. Both constructors take the injected clock, the store takes the state directory, and a wiring that drops them fails to compile against the real signatures (`governor.New(base, clk)` and `store.NewTransport(base, dir, clk)`, both proven in the stage 0 floor). They appear here because an earlier draft of this snippet omitted them, and a reader who copied that draft would have missed two injections the rest of the canon requires.

The governor sits **under** the store, so it observes real network exchanges and only those. A store hit that never reaches the network costs no rate limit, and the governor never sees it. A 304 reaches the governor **as a 304**, before the store rewrites it, so rate-governor R7's "a 304 costs zero, a 200 costs one" is read straight off the wire rather than inferred.

This is what makes rate-governor R5 possible at all. **The governor observes at the transport layer, not above the client.** Above the client there are no headers left to read.

Neither package imports the other. `store` takes an `http.RoundTripper`, `governor.New` returns one, and `http.RoundTripper` belongs to the standard library rather than to us. **main.go stays the only place that knows both exist**, which is the composition-root argument [ADR-0011](./0011-package-layout-and-dependency-direction.md) already makes for `store` and `ghclient`, applied to the same seam one layer down.

## Considered Options

**The store imports a `governor.Observer`.** The store sees every response and hands each to an interface the governor implements. It works, and it draws an arrow ADR-0011's table does not have: `store` would import `governor`. The moment the governor needs anything back from the store, the arrow reverses and the cycle arrives.

**An observer interface in `domain`.** Both packages import it and neither imports the other, which is the textbook dodge. It fails ADR-0011's own rule that `domain` holds no I/O: an interface whose entire vocabulary is `*http.Response` is an I/O type wearing a domain hat. It also buys nothing that nesting two RoundTrippers does not already buy for free.

**The governor above the client, wrapping `ghclient`.** This is what the canon assumed before go-gh was read closely, and it cannot work. R5 needs headers, and above the client there are none.

## Consequences

**Callers of `ghclient` handle `*http.Response` by hand.** No decode helper, no `Get`. That is more code at every call site, and it is the only shape that reaches the headers.

**The governor cannot see a request's purpose.** At the transport layer a poll and a delete are both `*http.Request`. Where the governor must pace writes without scheduling reads (rate-governor R2 and R4), it discriminates on method, which is all a RoundTripper has. That suffices here: every write this tool issues is a non-GET, and every read is a GET.

**Everything above the store believes conditional requests do not exist.** That is the design, and it is also the trap. A future caller expecting to observe a 304 will never see one. local-store R8 and this ADR are where that is written down.

**go-gh's own cache stays off** (`CacheTTL: 0`, local-store R19). It sits above our transport, short-circuits before we run, and would store a bare empty-bodied 304 as though it were a response, because `isCacheableResponse` is `StatusCode < 500 && StatusCode != 403`.

**A `newSanitizerRoundTripper` sits between go-gh's cache and ours** and is not ours to remove. It rewrites response bodies to strip ASCII control sequences. Nothing here depends on its absence, and it is recorded so that a reader tracing the chain does not mistake it for something we installed.
