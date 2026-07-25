package scheduler

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// filteredRunsBody is a listing whose total_count (the claimed match count) far
// exceeds the two Runs it returns, the shape a capped filtered view has: the API
// reports 18,258 matches but a filtered listing reaches only the newest 1,000
// (ADR-0005). R24's cap label is derived from exactly this gap.
const filteredRunsBody = `{"total_count":18258,"workflow_runs":[{"id":1,"status":"completed","name":"CI"},{"id":2,"status":"completed","name":"CI"}]}`

// filteredStubRT answers every GET with the filtered body above and a per-path ETag,
// so a poll carrying a server-side filter lands a 200 whose total_count the scheduler
// must carry back for the cap label.
type filteredStubRT struct{}

func (filteredStubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	h := http.Header{
		"Content-Type":          {"application/json"},
		"Etag":                  {`"` + req.URL.Path + `-v1"`},
		"X-Ratelimit-Remaining": {"5000"},
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(filteredRunsBody)),
		ContentLength: int64(len(filteredRunsBody)),
	}, nil
}

// TestActiveFilterIsPushedToTheWire is R22 at the transport seam: once a filter is
// set, every poll's request carries the filter's Query() parameters, so the server
// returns the newest matches rather than the newest Runs the Feed then filters over
// its ~30-Run held window. It is asserted at the counting transport, the same seam
// cli-surface AC4 uses, so a dropped or misspelled parameter is caught on the wire.
func TestActiveFilterIsPushedToTheWire(t *testing.T) {
	a := gh("acme", "a")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}})
	h.s.SetFilter(filter.Filter{Branch: "main"})
	h.start(t)

	h.waitPolls(t, 1)
	if n := h.counting.countPath("branch=main"); n != 1 {
		t.Fatalf("polls carrying branch=main = %d, want 1 (R22: the filter is pushed server-side)", n)
	}
}

// TestFilterChangeRepollsPromptly is R22's promptness: a filter set while the loop is
// asleep on the slow interval re-polls at once rather than waiting out the up-to-30s
// interval, so the operator's filter takes effect immediately. It is symmetric with
// the viewport change that already wakes the loop (TestViewportChangeWakesTheLoop).
func TestFilterChangeRepollsPromptly(t *testing.T) {
	a := gh("acme", "a")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}})
	h.start(t)

	// Cold start: one unfiltered poll, then the loop sleeps on the 30s slow interval.
	h.waitPolls(t, 1)
	h.waitSettle(t, slowTarget)
	if got := h.counting.count(); got != 1 {
		t.Fatalf("cold-start wire requests = %d, want 1", got)
	}

	// Set a filter. Without a wake-and-repoll this would wait out the 30s interval; with
	// it the repository is polled again at once, though no virtual time has passed.
	h.s.SetFilter(filter.Filter{Branch: "main"})
	h.waitPolls(t, 1)
	if n := h.counting.countPath("branch=main"); n != 1 {
		t.Errorf("polls carrying the new filter = %d, want 1 (a filter change re-polls promptly)", n)
	}
}

// TestFilteredUpdateCarriesTheClaimedTotal is R24 at the scheduler seam: a poll that
// pushed a server-side filter carries the response's total_count back on its Update,
// because the claimed count exists only in the response the engine consumed
// (ADR-0015, ADR-0016). The Feed derives "1,000 of ~18,258" from it.
func TestFilteredUpdateCarriesTheClaimedTotal(t *testing.T) {
	a := gh("acme", "a")
	h := newHarness(t, harnessConfig{base: filteredStubRT{}, pollSet: []domain.RepoID{a}})
	h.s.SetFilter(filter.Filter{Branch: "main"})
	h.start(t)

	h.waitUpdates(t, 1)
	u := h.updates()[0]
	if !u.Filtered {
		t.Errorf("Update.Filtered = false, want true (the poll pushed a server-side filter)")
	}
	if u.ClaimedTotal != 18258 {
		t.Errorf("Update.ClaimedTotal = %d, want 18258 (R24: the response's total_count)", u.ClaimedTotal)
	}
}

// TestUnfilteredUpdateCarriesNoClaimedTotal is R24's negative: an unfiltered poll's
// total_count is the repository's whole count, not a claimed match count, so it is
// never carried as a cap. The Feed shows no cap label for it. A filter whose Query()
// is empty (a client-side-only axis) is unfiltered on the wire and behaves the same.
func TestUnfilteredUpdateCarriesNoClaimedTotal(t *testing.T) {
	a := gh("acme", "a")
	h := newHarness(t, harnessConfig{base: filteredStubRT{}, pollSet: []domain.RepoID{a}})
	h.start(t) // no filter set: the listing is unfiltered

	h.waitUpdates(t, 1)
	u := h.updates()[0]
	if u.Filtered {
		t.Errorf("Update.Filtered = true, want false (an unfiltered poll carries no cap)")
	}
	if u.ClaimedTotal != 0 {
		t.Errorf("Update.ClaimedTotal = %d, want 0 (unfiltered listing)", u.ClaimedTotal)
	}
}
