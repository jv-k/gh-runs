package discovery_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestConditionalReprobeIsFreeAndRevealsNewRuns is AC10. After a first pass
// persists ETags, a re-probe carries each ETag as If-None-Match. An unchanged
// repository answers 304, which the store reconstitutes to a 200 and the governor
// counts as zero primary allowance, so its poll-set membership is unchanged for
// free. A repository that acquired its first Run breaks its own ETag, answers 200,
// and enters the poll set. The re-probe spends exactly the one 200 that repository
// cost.
func TestConditionalReprobeIsFreeAndRevealsNewRuns(t *testing.T) {
	h := newHarness(t, "reprobe", "")
	ctx := context.Background()

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	// After the first pass zeta has Runs and eta does not.
	if got := pollSetKeys(h.disc); strings.Join(got, ",") != "github.com/jv-k/zeta" {
		t.Fatalf("poll set after pass = %v, want [github.com/jv-k/zeta]", got)
	}
	usedBefore := h.gov.PrimaryUsed()
	revalBefore := h.gov.Revalidations()

	// Re-probe both repositories conditionally.
	h.disc.Reprobe(ctx, []domain.RepoID{gh("jv-k", "zeta"), gh("jv-k", "eta")}, nil)

	// zeta's 304 cost no primary allowance; eta's 200 cost exactly one. So the used
	// counter advanced by one across a two-repository re-probe.
	if got := h.gov.PrimaryUsed() - usedBefore; got != 1 {
		t.Errorf("primary used advanced by %d across the re-probe, want 1 (the 304 is free, only the flipped 200 counts)", got)
	}
	// zeta's re-probe was a real conditional round trip that the governor saw as a
	// 304, below the store's reconstitution.
	if got := h.gov.Revalidations() - revalBefore; got != 1 {
		t.Errorf("revalidations advanced by %d, want 1 (zeta's free 304)", got)
	}

	// eta revealed its first Run and joined the poll set; zeta is unchanged.
	got := pollSetKeys(h.disc)
	want := []string{"github.com/jv-k/eta", "github.com/jv-k/zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set after re-probe = %v, want %v", got, want)
	}
}

// TestTwoTierRefreshCadence is AC15 and R11/R12's two-tier split, proved
// deterministically against the injected clock with no sleeping. A repository
// whose probe carried an ETag revalidates on the fast interval; one whose probe
// carried none re-probes on the fixed hourly constant. Advancing virtual time past
// the fast interval but short of the hour makes only the ETag-holding repository
// due, and advancing past the hour makes both due.
func TestTwoTierRefreshCadence(t *testing.T) {
	const fast = 5 * time.Minute
	h := newHarness(t, "tiers", "", withRefresh(fast))
	ctx := context.Background()

	start := h.clk.Now()
	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	// Immediately after the pass, nothing is due: both were just probed.
	if due := dueKeys(h, start.Add(time.Second)); len(due) != 0 {
		t.Errorf("due one second after the pass = %v, want none", due)
	}

	// Past the fast interval but short of the hour: only the ETag-holding
	// repository is due. The hourly-tier repository is not.
	dueFast := dueKeys(h, start.Add(fast+time.Second))
	if strings.Join(dueFast, ",") != "github.com/jv-k/withetag" {
		t.Errorf("due after the fast interval = %v, want only github.com/jv-k/withetag", dueFast)
	}

	// Past the hour, both tiers are due.
	dueHour := dueKeys(h, start.Add(time.Hour+time.Second))
	want := []string{"github.com/jv-k/noetag", "github.com/jv-k/withetag"}
	if strings.Join(dueHour, ",") != strings.Join(want, ",") {
		t.Errorf("due after an hour = %v, want %v", dueHour, want)
	}
}

// dueKeys renders DueForReprobe at now as sorted host-qualified strings.
func dueKeys(h *harness, now time.Time) []string {
	ids := h.disc.DueForReprobe(now)
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = id.String()
	}
	sort.Strings(keys)
	return keys
}
