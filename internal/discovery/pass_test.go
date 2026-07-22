package discovery_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// pollSetKeys renders a discovery's poll set as sorted host-qualified strings, so
// a test asserts membership without depending on map iteration order.
func pollSetKeys(d *discovery.Discovery) []string {
	ids := d.PollSet()
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = id.String()
	}
	sort.Strings(keys)
	return keys
}

func gh(owner, name string) domain.RepoID {
	return domain.RepoID{Host: "github.com", Owner: owner, Name: name}
}

// TestPassClassifiesAndGatesFromOneProbeEach drives a full pass over the reference
// cost model: enumeration paginates to two pages, then every enumerated repository
// is probed exactly once, and capability is read from the enumeration payload at no
// extra request. It pins AC1 (two enumeration requests, no third page), AC2 (the
// poll set is exactly the repositories whose Run list was non-empty), AC3 (one
// probe per repository), AC5 (no probe carries a filter, and total_count is never
// the classifier), AC6 (an archived repository is probed and marked permanently
// read-only) and R7 (capability costs zero requests).
func TestPassClassifiesAndGatesFromOneProbeEach(t *testing.T) {
	h := newHarness(t, "pass_basic", "")

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// AC2: the poll set is exactly the three repositories whose probe returned a
	// non-empty Run list. beta returned total_count 5 with an empty array, so a
	// classifier that trusted total_count would wrongly include it; it is absent,
	// which is AC5's total_count half proved by a body that contradicts its count.
	got := pollSetKeys(h.disc)
	want := []string{"github.com/jv-k/alpha", "github.com/jv-k/epsilon", "github.com/jv-k/gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set = %v, want %v", got, want)
	}

	// AC1 + AC3: two enumeration requests and one probe per repository, seven wire
	// requests in all, and no third enumeration page. The counter sees exactly what
	// left the process.
	if n := h.counting.count(); n != 7 {
		t.Errorf("wire requests = %d, want 7 (2 enumeration + 5 probes)", n)
	}
	enumCount := 0
	for _, u := range h.counting.urls {
		if strings.Contains(u, "/user/repos") {
			enumCount++
		}
	}
	if enumCount != 2 {
		t.Errorf("enumeration requests = %d, want 2 (AC1: a third page is never requested)", enumCount)
	}

	// AC5: no probe carries a filter parameter. A probe URL is the runs endpoint
	// with no query string at all.
	for _, u := range h.counting.urls {
		if strings.Contains(u, "/actions/runs") && strings.Contains(u, "?") {
			t.Errorf("probe carried a query string, so it was filtered: %s", u)
		}
	}

	// AC4: no code-search request is issued by any code path. Discovery classifies
	// from Runs, never from Workflow files, so a repository whose Workflow was
	// deleted but whose Runs survive still classifies from its Run list alone.
	if h.counting.sawPath("/search") {
		t.Error("a code-search request was issued; discovery must never use code search (R6, AC4)")
	}

	// R7 / AC7: capability is recorded for every repository, and it cost zero extra
	// requests because it rode along with enumeration. The tri-state is read from
	// the recorded permissions and archived flag.
	cases := []struct {
		id        domain.RepoID
		want      domain.Capability
		permanent bool
	}{
		{gh("jv-k", "alpha"), domain.CapabilityPermitted, false},
		{gh("jv-k", "beta"), domain.CapabilityPermitted, false},
		{gh("jv-k", "gamma"), domain.CapabilityRefused, true}, // archived: refused and permanent (AC6)
		{gh("jv-k", "delta"), domain.CapabilityPermitted, false},
		{gh("jv-k", "epsilon"), domain.CapabilityRefused, false},
	}
	byID := recordsByID(h.disc)
	for _, c := range cases {
		if got := h.disc.Capability(c.id); got != c.want {
			t.Errorf("%s capability = %v, want %v", c.id, got, c.want)
		}
		rec, ok := byID[c.id.String()]
		if !ok {
			t.Errorf("%s: no record after the pass", c.id)
			continue
		}
		if rec.Permanent() != c.permanent {
			t.Errorf("%s Permanent() = %v, want %v (AC6: archived is permanently read-only)", c.id, rec.Permanent(), c.permanent)
		}
	}
}

// recordsByID indexes a discovery's records by host-qualified key.
func recordsByID(d *discovery.Discovery) map[string]discovery.Record {
	out := make(map[string]discovery.Record)
	for _, r := range d.Records() {
		out[r.ID().String()] = r
	}
	return out
}
