package governor_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// baseUnix is a fixed instant the timing tests build their fake clock from. It is
// arbitrary and far from the Unix epoch, so time.Unix(baseUnix,0).IsZero() is
// false and no test accidentally leans on the epoch guard the Readout keeps.
const baseUnix = 1_800_000_000 // 2027-01-15T08:00:00Z

func baseClock() *clockwork.FakeClock {
	return clockwork.NewFakeClockAt(time.Unix(baseUnix, 0).UTC())
}

// fakeTransport is a programmable http.RoundTripper. It stands in for the network
// where a test asserts the governor's ARITHMETIC given a known response shape (a
// ramp step, the dynamic ceiling, the pressure quotient) rather than the API's
// behaviour. It encodes no rate-limit policy, only a canned reply; the real
// 403/429/Retry-After/DELETE exchanges are recorded on cassettes (R23).
type fakeTransport struct {
	reply func(*http.Request) *http.Response
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.reply(req), nil
}

// serve makes every subsequent round trip return status, body and the two
// rate-limit headers R5 reads. Tests swap it between calls to script a sequence.
func (f *fakeTransport) serve(status int, remaining, reset int64, body string) {
	f.reply = func(_ *http.Request) *http.Response {
		h := make(http.Header)
		h.Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
}

func respondWith(status int, remaining, reset int64, body string) *fakeTransport {
	f := &fakeTransport{}
	f.serve(status, remaining, reset, body)
	return f
}

// respondNoReset returns a transport whose responses carry x-ratelimit-remaining
// but neither x-ratelimit-reset nor Retry-After, so the governor has no instant to
// resume from (AC10).
func respondNoReset(status int, remaining int64, body string) *fakeTransport {
	return &fakeTransport{reply: func(_ *http.Request) *http.Response {
		h := make(http.Header)
		h.Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}}
}

// getN issues n GET reads through the governor, closing each body. Reads are
// never paced (R4), so the fake clock does not advance and no test sleeps.
func getN(t *testing.T, g *governor.Governor, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		resp := getResp(t, g)
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}
}

// getResp issues one GET read and returns the response for the caller to inspect
// and close.
func getResp(t *testing.T, g *governor.Governor) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/o/r/actions/runs", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	return resp
}

// TestRampAdditiveIncrease pins R10's additive increase and AC2's exact count: 20
// consecutive clean responses step the rate from 1.0 to 1.25, and 19 do not.
func TestRampAdditiveIncrease(t *testing.T) {
	g := governor.New(respondWith(http.StatusOK, 4811, baseUnix+3600, "{}"), baseClock())

	if got := g.WriteRate(); got != 1.0 {
		t.Fatalf("cold-start write rate = %v, want 1.0 (R10)", got)
	}
	getN(t, g, 19)
	if got := g.WriteRate(); got != 1.0 {
		t.Errorf("after 19 clean responses write rate = %v, want still 1.0 (AC2)", got)
	}
	getN(t, g, 1)
	if got := g.WriteRate(); got != 1.25 {
		t.Errorf("after 20 clean responses write rate = %v, want 1.25 (AC2, R10)", got)
	}
}

// TestRampMultiplicativeDecrease pins R10's halving and the streak reset: a
// rate-limit response halves the current rate on the spot, and the ramp restarts
// its count from the halved rate (AC2).
func TestRampMultiplicativeDecrease(t *testing.T) {
	ft := respondWith(http.StatusOK, 4811, baseUnix+3600, "{}")
	g := governor.New(ft, baseClock())

	getN(t, g, 40) // two ramp steps: 1.0 -> 1.25 -> 1.5
	if got := g.WriteRate(); got != 1.5 {
		t.Fatalf("after 40 clean responses rate = %v, want 1.5", got)
	}

	ft.serve(http.StatusTooManyRequests, 4811, baseUnix+3600, "")
	getN(t, g, 1)
	if got := g.WriteRate(); got != 0.75 {
		t.Errorf("after a rate-limit response rate = %v, want 0.75 (halved, R10)", got)
	}

	ft.serve(http.StatusOK, 4811, baseUnix+3600, "{}")
	getN(t, g, 19)
	if got := g.WriteRate(); got != 0.75 {
		t.Errorf("19 cleans after backoff rate = %v, want still 0.75 (streak reset, AC2)", got)
	}
	getN(t, g, 1)
	if got := g.WriteRate(); got != 1.0 {
		t.Errorf("20 cleans after backoff rate = %v, want 1.0 (re-ramp from 0.75, R10)", got)
	}
}

// TestBackoffFloorsAtHalf pins R11's floor: a run of backoffs slows the writes
// rather than stalling them. Multiplicative decrease converges on 0.5/sec.
func TestBackoffFloorsAtHalf(t *testing.T) {
	g := governor.New(respondWith(http.StatusTooManyRequests, 4811, baseUnix+3600, ""), baseClock())

	getN(t, g, 10) // ten rate-limit responses
	if got := g.WriteRate(); got != 0.5 {
		t.Errorf("after sustained backoff rate = %v, want floored at 0.5 (R11)", got)
	}
}

// TestRateNeverExceedsCap pins the upper half of R11 and AC2: the observed write
// rate never rises above 2.5/sec however long responses stay clean.
func TestRateNeverExceedsCap(t *testing.T) {
	g := governor.New(respondWith(http.StatusOK, 4811, baseUnix+3600, "{}"), baseClock())

	getN(t, g, 120) // six ramp steps' worth: 1.0 -> 2.5
	if got := g.WriteRate(); got > 2.5 {
		t.Errorf("after 120 clean responses rate = %v, want no more than the 2.5 cap (R11, AC2)", got)
	}
	if got := g.WriteRate(); got < 0.5 {
		t.Errorf("write rate = %v fell below the 0.5 floor (R11)", got)
	}
}
