// Package limiter bounds the number of HTTP requests in flight on the wire at
// once, process-wide, to a constant (ADR-0018). It is a concurrency-limiting
// http.RoundTripper and the innermost resident of the transport chain
// (ADR-0012): store over governor over limiter over the network. Every request the
// tool issues passes through it, so no composition of components can breach the cap
// GitHub publishes (100 concurrent, shared across REST and GraphQL). The bound sits
// a tenth of the way to that cap, so it survives the cap shrinking without notice,
// a second instance of the tool sharing the pool, and any concurrent GraphQL use.
//
// It is innermost because a slot must measure what the cap measures: requests on
// the wire. A slot is held exactly while base.RoundTrip runs and released the
// instant the response lands, so a governor-paced write waiting out its inter-write
// interval above the limiter holds no slot and cannot starve a read fan-out of
// slots nobody is using (ADR-0018).
//
// It imports nothing of ours and holds no state beyond its semaphore, so the
// cassette seam is untouched: a test injects at the base parameter, one layer below
// where it did before the limiter existed (ADR-0018 Consequences, ADR-0012).
package limiter

import "net/http"

// Bound is the process-wide wire-concurrency limit (ADR-0018, polling-scheduler
// R18). It is octokit's own plugin-throttling maxConcurrent, a tenth of GitHub's
// published cap of 100. Nothing configures it, nothing scales it, and no state
// changes it (polling-scheduler AC15). The number appears in exactly this one place
// (ADR-0018 Consequences).
const Bound = 10

// Transport is the concurrency-limiting RoundTripper. The semaphore is a buffered
// channel: a send acquires a slot and a receive releases it, so at most cap(sem)
// RoundTrip calls delegate to base at once.
type Transport struct {
	base http.RoundTripper
	sem  chan struct{}
}

// New returns a Transport admitting at most n concurrent RoundTrip calls to base.
// main.go passes Bound; n is a parameter so the number has one home (Bound) while
// the component stays a pure semaphore over any base. base is http.DefaultTransport
// in production and a cassette in a test, the same base parameter the chain injected
// at before this layer existed (ADR-0012).
func New(base http.RoundTripper, n int) *Transport {
	return &Transport{
		base: base,
		sem:  make(chan struct{}, n),
	}
}

// RoundTrip acquires a slot, delegates to base, and releases the slot when base
// returns. The slot is held exactly while the request is on the wire, so at no
// instant are more than n requests in flight through the limiter (AC15). While it
// waits for a slot it honours the request's context, so a cancelled request stops
// waiting rather than parking behind a saturated pool: the engine's Stop() aborts
// queued and in-flight requests alike (ADR-0018's quit context). A request whose
// context is already done loses no slot, because it never reaches the acquire.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case t.sem <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	defer func() { <-t.sem }()
	return t.base.RoundTrip(req)
}
