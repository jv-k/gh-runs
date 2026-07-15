# Liveness via conditional ETag polling

The Feed must show Runs invoked elsewhere without interaction. GitHub offers no push mechanism a local TUI can use, so liveness means polling. What makes it affordable is that **conditional requests returning 304 cost nothing against the primary rate limit**.

Measured directly, interleaving unconditional and conditional requests against the same endpoint:

```text
round1:  uncond 200 used=120  |  cond 304 used=120
round2:  uncond 200 used=121  |  cond 304 used=121
round3:  uncond 200 used=122  |  cond 304 used=122
```

`used` advances by exactly one per round, and that one belongs entirely to the 200. Polling roughly 26 repositories every few seconds is therefore about 3,600 requests an hour consuming approximately zero budget while idle. Only repositories that actually changed cost anything.

## Considered Options

**Webhooks.** They need a publicly reachable endpoint. A non-starter for a local TUI.

**SSE or websockets.** The web UI's live updates run on an internal socket with no public equivalent.

**Unconditional polling.** 26 repos × 12/min = 18,720 requests an hour against a 5,000/hour limit. Impossible.

## Consequences

The binding constraint moves from the primary limit to the **secondary** one (roughly 900 points/min, GET = 1 point), which is why the polling scheduler tiers and auto-scales rather than exposing an interval. We conservatively assume 304s **do** count against the secondary limit (risk R4). The primary exemption does not imply a secondary one, and confirming it would mean deliberately tripping a limit and risking a block.

ETags must **persist across sessions**, or every cold start pays full 200s. That persistence is what makes stale-while-revalidate cold starts both instant and free.

**Risk R2 is resolved: go-gh's cache is TTL-only and never revalidates.** The economics above are unchanged and still hold, but they are not go-gh's to give. From `pkg/api/cache.go`, verified byte-identical at tag v2.9.0:

```go
// RoundTrip: on a cache hit it RETURNS. rt.RoundTrip is never called.
if res, err := crt.fs.read(key); err == nil {
    res.Request = req
    return res, nil          // no network, no revalidation
}
// read(): freshness is file mtime vs TTL. That is the entire policy.
age := time.Since(stat.ModTime())
if age > fs.ttl { return nil, errors.New("cache expired") }
```

A grep across the whole v2 module for `etag`, `if-none-match`, `if-modified-since`, `StatusNotModified` and `revalidat` returns zero matches, tests included, and go.sum carries no caching library. This is bespoke, not RFC-9111. Empirically, real go-gh v2.9.0 with `EnableCache: true, CacheTTL: 30s` against a counting server serving an ETag: two identical GETs produced **1 network hit, and 0 requests carrying `If-None-Match`**. A revalidating cache would show 2.

**So we supply the transport ourselves, and that is verified working.** `api.ClientOptions` has a `Transport http.RoundTripper` field. `NewHTTPClient` chains, outermost to innermost, `header → logger → cache → sanitizer → opts.Transport`, so ours is the innermost: the last stop before the wire, seeing requests after auth and Accept headers are applied. End to end, `If-None-Match` reached the wire, the server returned 304, the body was reconstituted from our store, and the caller saw a normal 200. Setting `Transport` does not disturb gh's token or host resolution. This is a build requirement now, not a risk.

**`EnableCache: false` does not disable go-gh's cache.** The cache RoundTripper is installed unconditionally, and `EnableCache` only defaults `CacheTTL` to 24h. `EnableCache: false, CacheTTL: 5 * time.Second` still caches. The real off-switch is **`CacheTTL: 0`**, and the `X-GH-CACHE-TTL` and `X-GH-CACHE-DIR` request headers can arm it per request.

**Our transport must never run underneath go-gh's cache.** Theirs sits above ours and short-circuits before we run, so our `If-None-Match` would never reach the wire. Worse, its `isCacheableResponse` is `StatusCode < 500 && StatusCode != 403`, which makes a 304 cacheable: it would store a bare empty-bodied 304 as the response. `Transport` also takes precedence over `UnixDomainSocket`, so a user's `http_unix_socket` gh config is silently ignored. That is irrelevant against api.github.com, but it is a real override.

The ETag was on disk the whole time, incidentally. `store` writes responses with `res.Write(f)`, which serialises headers. `read` just never looks at it.
