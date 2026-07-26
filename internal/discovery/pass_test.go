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

// TestDefaultBranchRidesEnumerationAtNoExtraRequest pins the free ride workflow-dispatch R23 needs:
// default_branch arrives on the same /user/repos payload the permissions and archived flags arrive
// on (R7), so a consumer that needs a repository's default branch spends nothing to learn it.
//
// It is proved at the wire rather than by reading a populated field. A struct field can be filled
// by an extra request just as easily as by a free one, and the claim here is about a request that
// was never made: after a full pass, the counter has seen the two enumeration pages and one probe
// each, and not one GET /repos/{owner}/{repo}, which is the request the dispatch form used to spend
// per form open.
func TestDefaultBranchRidesEnumerationAtNoExtraRequest(t *testing.T) {
	h := newHarness(t, "pass_basic", "")

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// The value is read per repository from the payload, not defaulted: alpha's is trunk and the
	// rest are main, so a constant would fail here.
	want := map[string]string{
		"github.com/jv-k/alpha":   "trunk",
		"github.com/jv-k/beta":    "main",
		"github.com/jv-k/gamma":   "main",
		"github.com/jv-k/delta":   "main",
		"github.com/jv-k/epsilon": "main",
	}
	for key, rec := range recordsByID(h.disc) {
		if got := rec.Repo().DefaultBranch; got != want[key] {
			t.Errorf("%s default branch = %q, want %q (R23 rides the enumeration)", key, got, want[key])
		}
	}

	// The cost is unchanged: two enumeration requests and one probe each, exactly what the pass cost
	// before default_branch was carried. Learning it added nothing to the wire.
	if n := h.counting.count(); n != 7 {
		t.Errorf("wire requests = %d, want 7 (2 enumeration + 5 probes); default_branch must add none", n)
	}
	// And no repository read was issued for any of them. GET /repos/{owner}/{repo} is the request
	// the dispatch form used to spend on every form open, and its absence here is what makes the
	// free ride a Budget win rather than a tidier struct.
	for key := range want {
		name := strings.TrimPrefix(key, "github.com/")
		if n := h.counting.countExact("https://api.github.com/repos/" + name); n != 0 {
			t.Errorf("GET /repos/%s was issued %d times; the default branch must cost no request (R23)", name, n)
		}
	}
	for _, u := range h.counting.urls {
		if strings.HasPrefix(u, "https://api.github.com/repos/") && !strings.HasSuffix(u, "/actions/runs") {
			t.Errorf("an unexpected repository request left the process: %s", u)
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
