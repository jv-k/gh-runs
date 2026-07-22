package governor_test

import (
	"net/http"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestHeaderAuthority pins AC7: when x-ratelimit-remaining falls faster than the
// governor's own tally predicts (another consumer of the same token is spending
// it), the published remaining matches the header, not the tally. The governor
// issues three requests but the header drops by hundreds; the Readout follows the
// header (R5, R6).
func TestHeaderAuthority(t *testing.T) {
	ft := respondWith(http.StatusOK, 4811, baseUnix+3600, "{}")
	g := governor.New(ft, baseClock())

	getN(t, g, 1)
	ft.serve(http.StatusOK, 4700, baseUnix+3600, "{}")
	getN(t, g, 1)
	ft.serve(http.StatusOK, 4600, baseUnix+3600, "{}")
	getN(t, g, 1)

	// Three requests would predict a local tally near 4808. The header is
	// authoritative, so the published remaining is its 4600.
	if got := g.Readout().Remaining; got != 4600 {
		t.Errorf("Readout.Remaining = %d, want 4600 from the header, not the local tally (R5, R6, AC7)", got)
	}
	if got := g.Remaining(); got != 4600 {
		t.Errorf("Remaining() = %d, want 4600 (header authority, AC7)", got)
	}
}

// TestNotModifiedIsFree pins AC8 at the governor's own seam, the complement of
// the store's end-to-end cassette: a 304 costs zero primary allowance and a 200
// costs one (R7). After a 200 and three 304s the primary tally is one, and the
// three revalidations are counted separately so a caller can prove they stayed
// free.
func TestNotModifiedIsFree(t *testing.T) {
	ft := respondWith(http.StatusOK, 4811, baseUnix+3600, "{}")
	g := governor.New(ft, baseClock())

	getN(t, g, 1)
	if got := g.PrimaryUsed(); got != 1 {
		t.Fatalf("primaryUsed = %d after one 200, want 1 (R7)", got)
	}
	ft.serve(http.StatusNotModified, 4811, baseUnix+3600, "")
	getN(t, g, 3)

	if got := g.PrimaryUsed(); got != 1 {
		t.Errorf("primaryUsed = %d after three 304s, want still 1; a 304 costs zero primary allowance (R7, AC8)", got)
	}
	if got := g.Revalidations(); got != 3 {
		t.Errorf("Revalidations = %d, want 3 free 304s (R7, AC8)", got)
	}
}
