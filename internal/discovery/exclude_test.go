package discovery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestExcludedRepositoryReceivesNoRequestsAcrossAPollingCycle is settings AC5,
// asserted at the wire rather than over a slice. A repository named in the exclude
// list is removed from discovery, the Feed and all polling, so across a full cycle
// (enumerate, classify, then the conditional re-probe the cadence makes due) not one
// request targets it. The counter sits directly above the cassette, so what it
// reports is what left the process, not what the code believed it sent.
//
// The negative alone would pass on a discovery that issued nothing at all, so the
// test also pins the positive: the repository that was not excluded is enumerated,
// probed, and re-probed conditionally, which is the same cycle running normally
// around the hole the exclusion cut.
func TestExcludedRepositoryReceivesNoRequestsAcrossAPollingCycle(t *testing.T) {
	const fast = 5 * time.Minute
	h := newHarness(t, "reprobe", "", withRefresh(fast), withExclude(gh("jv-k", "eta")))
	ctx := context.Background()

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// The excluded repository is not classified, so it is in no poll set at any tier
	// and the scheduler that reads that set can never reach it (polling-scheduler R2).
	if got := pollSetKeys(h.disc); strings.Join(got, ",") != "github.com/jv-k/zeta" {
		t.Errorf("poll set after the pass = %v, want only github.com/jv-k/zeta", got)
	}
	if _, ok := recordsByID(h.disc)["github.com/jv-k/eta"]; ok {
		t.Error("the excluded repository has a record; exclusion must remove it from discovery, not merely hide it")
	}

	// It is not due for a re-probe either, which is what keeps the exclusion holding
	// past the first pass rather than only at launch.
	h.clk.Advance(fast + time.Second)
	due := h.disc.DueForReprobe(h.clk.Now())
	for _, id := range due {
		if id == gh("jv-k", "eta") {
			t.Error("the excluded repository is due for a re-probe")
		}
	}
	h.disc.Reprobe(ctx, due, nil)

	// AC5 at the wire: across enumeration, classification and the re-probe, zero
	// requests targeted the excluded repository.
	if h.counting.sawPath("/repos/jv-k/eta") {
		t.Errorf("a request reached the excluded repository across the polling cycle: %v", h.counting.urls)
	}
	// The cycle ran: enumeration, zeta's probe, and zeta's conditional re-probe. Three
	// requests, none of them eta's, so the exclusion cut one repository out rather than
	// stopping discovery.
	if n := h.counting.count(); n != 3 {
		t.Errorf("wire requests = %d, want 3 (1 enumeration + zeta probed and re-probed): %v", n, h.counting.urls)
	}
}

// TestExclusionBeatsPin is settings AC14. A repository named in both lists is
// excluded: it appears nowhere, receives zero requests, and its pin has no
// observable effect. The pin is proved live by a second repository that is pinned
// and not excluded, which leads the poll set: without that, "the pin had no effect"
// would be indistinguishable from a pin that does nothing anywhere.
func TestExclusionBeatsPin(t *testing.T) {
	both := gh("jv-k", "alpha")     // excluded and pinned: exclusion wins
	pinned := gh("jv-k", "epsilon") // pinned only: the pin is observable here
	h := newHarness(t, "pass_basic", "",
		withExclude(both),
		withPin(both, pinned),
	)

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// It appears nowhere: not in the poll set the Feed and the scheduler read, and not
	// in the records a consumer gates a destructive action on.
	for _, id := range h.disc.PollSet() {
		if id == both {
			t.Error("the excluded-and-pinned repository is in the poll set; exclusion must win (R7, AC14)")
		}
	}
	if _, ok := recordsByID(h.disc)["github.com/jv-k/alpha"]; ok {
		t.Error("the excluded-and-pinned repository has a record; exclusion must win (R7, AC14)")
	}

	// It receives zero requests, exactly as an excluded-only repository does.
	if h.counting.sawPath("/repos/jv-k/alpha") {
		t.Errorf("a request reached the excluded-and-pinned repository: %v", h.counting.urls)
	}
	// Two enumeration pages and one probe each for the four repositories that were not
	// excluded. The pin bought alpha no request, which is what "no observable effect"
	// means at the wire.
	if n := h.counting.count(); n != 6 {
		t.Errorf("wire requests = %d, want 6 (2 enumeration + 4 probes, alpha excluded): %v", n, h.counting.urls)
	}

	// The pin that was not overridden is observable: epsilon leads the poll set,
	// ahead of the repositories that were not pinned.
	set := h.disc.PollSet()
	if len(set) == 0 || set[0] != pinned {
		t.Errorf("poll set = %v, want the pinned repository %v first", set, pinned)
	}
}

// TestPinOrdersThePollSet pins R7's other half on its own: a pinned repository is
// prioritised, and the pin list's order is the priority. The repositories that are
// not pinned follow in a stable order, so the set a scheduler reads is deterministic
// rather than map-ordered.
func TestPinOrdersThePollSet(t *testing.T) {
	h := newHarness(t, "pass_basic", "", withPin(gh("jv-k", "epsilon"), gh("jv-k", "gamma")))

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	var got []string
	for _, id := range h.disc.PollSet() {
		got = append(got, id.String())
	}
	want := []string{"github.com/jv-k/epsilon", "github.com/jv-k/gamma", "github.com/jv-k/alpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set = %v, want the pin list's order first, then the rest: %v", got, want)
	}
}

// TestExclusionDropsAPersistedRepository pins the reload limb of AC5. A session that
// ran before the exclusion existed persisted the repository's classification, and the
// next session must not re-admit it: an exclusion that only filtered a fresh
// enumeration would let a warm cache put the repository straight back into the poll
// set, and from there into the scheduler's rotation.
func TestExclusionDropsAPersistedRepository(t *testing.T) {
	first := newHarness(t, "pass_basic", "")
	if err := first.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("first Pass: %v", err)
	}
	if _, ok := recordsByID(first.disc)["github.com/jv-k/alpha"]; !ok {
		t.Fatal("the first session did not classify alpha, so the reload has nothing to drop")
	}

	// A second session over the same store, now excluding alpha.
	second := newHarness(t, "pass_basic", first.dir, withExclude(gh("jv-k", "alpha")))
	if n := second.disc.Reload(); n == 0 {
		t.Fatal("the second session reloaded nothing, so this proves nothing about the exclusion")
	}
	if _, ok := recordsByID(second.disc)["github.com/jv-k/alpha"]; ok {
		t.Error("Reload re-admitted an excluded repository from the persisted set")
	}
	for _, id := range second.disc.DueForReprobe(second.clk.Now()) {
		if id == gh("jv-k", "alpha") {
			t.Error("an excluded repository reloaded from the store is due for a re-probe")
		}
	}
	if second.counting.count() != 0 {
		t.Errorf("Reload issued %d requests, want 0", second.counting.count())
	}
}

// TestExcludedLaunchRepositoryGetsNoFastPathRequest pins the cold-start limb of AC5.
// Launching inside an excluded repository must not spend even the fast path's single
// Run-listing request, nor R22's adoption request: "removed from discovery, the Feed
// and all polling" admits no exception for the repository the terminal happens to sit
// in.
func TestExcludedLaunchRepositoryGetsNoFastPathRequest(t *testing.T) {
	local := gh("jv-k", "local")
	h := newHarness(t, "fastpath", "",
		withCurrent(func() (domain.RepoID, error) { return local, nil }),
		withExclude(local),
	)

	if err := h.disc.Discover(context.Background(), nil); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if h.counting.sawPath("/repos/jv-k/local") {
		t.Errorf("a request reached the excluded launch repository: %v", h.counting.urls)
	}
	if err := h.disc.FastPathErr(); err != nil {
		t.Errorf("FastPathErr = %v, want nil: an excluded launch repository is a configured choice, not a failure", err)
	}
	// The account is still discovered around it: enumeration and the two probes ran.
	if got := pollSetKeys(h.disc); strings.Join(got, ",") != "github.com/acme/owned-a" {
		t.Errorf("poll set = %v, want github.com/acme/owned-a (discovery proceeded past the excluded launch repository)", got)
	}
}
