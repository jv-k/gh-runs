package discovery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
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

// TestExclusionRemovesARepositoryFromAFullAccount is settings AC5 over the reference
// pass shape rather than the two-repository one: five repositories enumerate across two
// pages, one is excluded, and the pass spends exactly one probe on each of the other
// four. It is the cost model AC3 fixes with a hole cut in it, so a regression that
// probed an excluded repository anyway would show up as a request count as well as a
// URL.
func TestExclusionRemovesARepositoryFromAFullAccount(t *testing.T) {
	excluded := gh("jv-k", "alpha")
	h := newHarness(t, "pass_basic", "", withExclude(excluded))

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// It appears nowhere: not in the poll set the Feed and the scheduler read, and not
	// in the records a consumer gates a destructive action on.
	for _, id := range h.disc.PollSet() {
		if id == excluded {
			t.Error("the excluded repository is in the poll set")
		}
	}
	if _, ok := recordsByID(h.disc)["github.com/jv-k/alpha"]; ok {
		t.Error("the excluded repository has a record")
	}
	if h.counting.sawPath("/repos/jv-k/alpha") {
		t.Errorf("a request reached the excluded repository: %v", h.counting.urls)
	}
	if n := h.counting.count(); n != 6 {
		t.Errorf("wire requests = %d, want 6 (2 enumeration + 4 probes, alpha excluded): %v", n, h.counting.urls)
	}
}

// TestPollSetOrderIsDeterministic pins that the published set is stable rather than
// map-ordered, so a failing consumer test is reproducible. The order is not a priority
// and no consumer reads it as one: R7's pin half, which would have been a priority, is
// deferred to issue #97 because cadence belongs to the scheduler's tier policy
// (ADR-0021) and nothing discovery publishes is consumed for order.
func TestPollSetOrderIsDeterministic(t *testing.T) {
	h := newHarness(t, "pass_basic", "")

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	var got []string
	for _, id := range h.disc.PollSet() {
		got = append(got, id.String())
	}
	want := []string{"github.com/jv-k/alpha", "github.com/jv-k/epsilon", "github.com/jv-k/gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set = %v, want %v", got, want)
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

	// A second session over the same store, now excluding alpha. The exclude list has
	// changed, so the persisted set is stale and Reload reports a cold start; either way
	// the excluded repository is not admitted, which is the property that matters.
	second := newHarness(t, "pass_basic", first.dir, withExclude(gh("jv-k", "alpha")))
	second.disc.Reload()
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

// TestExclusionIsReversibleOnAWarmCache pins that an exclusion can be taken back. A
// Pass run while an exclusion is active persists a document without that repository,
// and every caller reloads with "if Reload() == 0 then Pass", so with any other
// repository present a warm cache would answer non-zero forever and the repository
// would never be re-enumerated. That would make a config line a one-way door: delete
// it and nothing comes back until the cache directory is cleared.
//
// The fix is that the persisted set records which exclude list shaped it. A change to
// the list makes the document stale, Reload reports a cold start, and the next Pass
// rebuilds from the account.
func TestExclusionIsReversibleOnAWarmCache(t *testing.T) {
	ctx := context.Background()
	excluded := gh("jv-k", "alpha")
	h := newHarness(t, "exclude_reversal", "")

	// Launch one: no exclusions. The whole account is classified and persisted.
	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("first Pass: %v", err)
	}

	// Launch two: alpha is excluded. The list changed, so the persisted set is stale and
	// the caller spends a pass, which rewrites the document without alpha.
	second := h.relaunch(excluded)
	if n := second.Reload(); n != 0 {
		t.Errorf("Reload with a newly changed exclude list = %d, want 0 so the caller re-enumerates", n)
	}
	if err := second.Pass(ctx, nil); err != nil {
		t.Fatalf("second Pass: %v", err)
	}
	if _, ok := recordsByID(second)["github.com/jv-k/alpha"]; ok {
		t.Fatal("the excluded repository survived its own session")
	}

	// Launch three: the exclusion is taken back. Reload must report a cold start, which
	// is what makes the caller spend a pass and bring the repository back. Without the
	// staleness check the document still holds beta and gamma, Reload would answer two,
	// and alpha would be gone until the cache directory was cleared.
	// The launch is driven exactly as every caller drives it, "if Reload reports nothing
	// then Pass", so a Reload that wrongly answered warm would skip the pass and leave
	// the final assertion to catch it. Calling Pass unconditionally would make that
	// assertion unfailable.
	third := h.relaunch()
	if n := third.Reload(); n == 0 {
		if err := third.Pass(ctx, nil); err != nil {
			t.Fatalf("third Pass: %v", err)
		}
	}
	if _, ok := recordsByID(third)["github.com/jv-k/alpha"]; !ok {
		t.Error("removing the exclusion did not bring the repository back; the setting is a one-way door")
	}
}

// TestAbsentMarkerReloadsWarmForAnExistingStore pins the upgrade path. A store written
// before the exclude marker existed carries no marker, and treating that as a mismatch
// would make every existing user pay a re-enumeration on first launch of this version.
// An absent marker matches an empty exclude list, which is what an existing user has.
func TestAbsentMarkerReloadsWarmForAnExistingStore(t *testing.T) {
	dir := t.TempDir()

	// A store as an older version left it: the results document alone, no marker.
	older := newDocStore(t, dir)
	older.SaveDoc("discovery", []discovery.Record{{
		Host: "github.com", Owner: "jv-k", Name: "alpha", HasRuns: true, Known: true,
	}})

	h := newHarness(t, "exclude_reversal", dir)
	if n := h.disc.Reload(); n != 1 {
		t.Fatalf("Reload over a marker-less store = %d, want 1: an upgrade must not go cold", n)
	}
	if h.counting.count() != 0 {
		t.Errorf("the upgrade reload issued %d requests, want 0", h.counting.count())
	}
}

// TestUnchangedExclusionStillReloadsWarm is the other half of reversibility, and the
// one that keeps it affordable. Going cold on every launch would cost the reference
// account ~165 requests each time, so the staleness check must fire on a change to the
// exclude list and on nothing else.
func TestUnchangedExclusionStillReloadsWarm(t *testing.T) {
	ctx := context.Background()
	excluded := gh("jv-k", "alpha")
	h := newHarness(t, "exclude_reversal", "", withExclude(excluded))

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("first Pass: %v", err)
	}
	spentOnTheFirstPass := h.counting.count()

	second := h.relaunch(excluded)
	if n := second.Reload(); n == 0 {
		t.Fatal("Reload with an unchanged exclude list reported a cold start; every launch would re-enumerate")
	}
	if n := h.counting.count() - spentOnTheFirstPass; n != 0 {
		t.Errorf("a warm Reload issued %d requests, want 0", n)
	}
	if _, ok := recordsByID(second)["github.com/jv-k/alpha"]; ok {
		t.Error("the warm reload re-admitted the excluded repository")
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
