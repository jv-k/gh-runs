package scheduler

import (
	"sync"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// runsListings counts the Run listings that have reached the wire, answered or not. It is
// the request AC16 counts, and it is deliberately narrower than the whole wire: the
// Workflow listing the fan-out joins against (ADR-0014, workflowStates) is a different
// resource, and a repository's opening poll issues one of each. AC16 bounds the Run
// listings at one and says nothing about any other resource.
func (c *countingRT) runsListings() int { return c.countIssued("/actions/runs") }

// TestFastPathIsPolledAloneUntilItLands is R32 and AC16 at the scheduler's seam. With a
// warm poll set of three repositories and the fast path naming one of them, the engine
// issues exactly one Run listing, for that repository, and holds the other two back until
// it lands. Before this the cold start made every repository due at once, so the warm-cache
// launch fanned out to the whole set and the repository the operator is sitting in was one
// response among many.
//
// The proof is at the wire and by ordering, never by elapsed real time (R21). The fast
// path's poll is held in the base transport, and a whole idle interval of virtual time is
// spent with it in flight: the loop re-evaluates and declines to schedule anything else.
// Releasing it opens the gate and the rest follow.
func TestFastPathIsPolledAloneUntilItLands(t *testing.T) {
	first, b, c := gh("acme", "a"), gh("acme", "b"), gh("acme", "c")
	base := &gatedBaseRT{
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	releaseAll := sync.OnceFunc(func() { close(base.release) })

	h := newHarness(t, harnessConfig{
		base:    base,
		pollSet: []domain.RepoID{first, b, c},
		first:   first,
	})
	h.start(t)
	// Registered after start, so it runs before the Stop start registered: cleanups unwind
	// last-registered first, and Stop waits on every in-flight poll, including one still held
	// in the base. Without this order a failed assertion below would deadlock the run instead
	// of reporting.
	t.Cleanup(releaseAll)

	<-base.entered // one request has reached the wire and is held there

	// Spend a whole slow interval of virtual time with the fast path's poll in flight. The
	// loop wakes, re-evaluates, and schedules nothing, because the fast path has not
	// landed. This is the ordering AC16 asserts, proved by what is on the wire rather than
	// by how long anything took (R21: no test sleeps through an interval).
	//
	// The barrier after the advance is the settle probe, not a second block on the clock.
	// Blocking on the clock would return at once on the timer armed before the advance and
	// prove nothing about the decision taken after it. Settling on defaultIdle is that
	// decision: it is the wait the loop chooses when nothing is schedulable, which here means
	// it looked at the gated set and found only a repository already in flight.
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitSettle(t, defaultIdle)

	if got := h.counting.runsListings(); got != 1 {
		t.Fatalf("Run listings on the wire while the fast path is still in flight = %d, want exactly 1 (AC16): %v", got, h.counting.issued)
	}
	if got := h.counting.countIssued("/repos/acme/a/actions/runs"); got != 1 {
		t.Fatalf("the one Run listing was not the fast path's: acme/a listed %d times, want 1", got)
	}

	// Release it. The gate opens on the fast path's poll finishing, so the rest of the poll
	// set follows with no user interaction (R33). Four polls unwind: the fast path's, and
	// the three the loop schedules at the reopened decision (the fast path is due again by
	// now, having been polled a slow interval ago).
	releaseAll()
	h.waitPolls(t, 4)

	if got := h.counting.countPath("/repos/acme/b/actions/runs"); got != 1 {
		t.Errorf("acme/b listed %d times after the fast path landed, want 1 (R33: the rest reveal progressively)", got)
	}
	if got := h.counting.countPath("/repos/acme/c/actions/runs"); got != 1 {
		t.Errorf("acme/c listed %d times after the fast path landed, want 1 (R33)", got)
	}
}

// TestNoFastPathFansOutAtOnce is R34. Launched outside a git repository there is no fast
// path, so nothing is gated and every discovered repository is due at the cold start,
// exactly as it was before R32's gate existed. This is the regression guard on the gate
// defaulting closed: a zero First must open it at construction, not leave the engine
// waiting for a poll it will never schedule.
func TestNoFastPathFansOutAtOnce(t *testing.T) {
	a, b := gh("acme", "a"), gh("acme", "b")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a, b}})
	h.start(t)

	h.waitPolls(t, 2)
	if got := h.counting.runsListings(); got != 2 {
		t.Errorf("Run listings at a cold start with no fast path = %d, want 2 (R34: progressive reveal across the whole set)", got)
	}
}

// TestPollSetChangedRevealsWithoutWaitingOutTheInterval is R33's other half. Reading the
// poll set every scheduling decision (R3) is not enough on its own once discovery runs
// behind the fast path: the loop sleeps until the next repository it already knows about is
// due, so a repository classified a moment after it went to sleep would wait out that whole
// interval, up to the slow tier's 30s, before its first Run reached the screen. "Reveal
// their Runs progressively as they arrive" is not satisfied by revealing them a slow
// interval after they arrive.
//
// The proof advances no virtual time at all. The repository is added, the engine is told,
// and its poll goes out.
func TestPollSetChangedRevealsWithoutWaitingOutTheInterval(t *testing.T) {
	a, b := gh("acme", "a"), gh("acme", "b")
	h := newHarness(t, harnessConfig{base: stubRT{}, pollSet: []domain.RepoID{a}})
	h.start(t)

	h.waitPolls(t, 1)
	h.waitSettle(t, slowTarget) // the loop is asleep for a full slow interval

	h.ps.set(a, b)
	h.s.PollSetChanged()
	h.waitPolls(t, 1)

	if got := h.counting.countPath("/repos/acme/b/"); got != 1 {
		t.Errorf("the repository discovery added was polled %d times with no time advanced, want 1 (R33)", got)
	}
}

// TestFastPathDefersToTheClassification is polling-scheduler R2. The fast path carries the
// launch repository into the schedule before discovery has said anything about it, which is
// R32, but it must not carry it there for ever: R2 says the scheduler polls **only** the
// repositories discovery classified as having Runs, and repo-discovery R22 admits the
// launch repository to the poll set only "if it has Runs".
//
// The failing case is the common one. Most repositories have no Actions at all, so a launch
// inside one leaves discovery classifying it HasRuns=false and keeping it out of the poll
// set, and an unconditional union would poll it every slow interval for the whole session
// against a requirement that says not to.
func TestFastPathDefersToTheClassification(t *testing.T) {
	first, withRuns := gh("acme", "a"), gh("acme", "b")
	classified := &fakeClassified{}
	h := newHarness(t, harnessConfig{base: stubRT{}, first: first, classified: classified})
	h.start(t)

	// R32: before discovery has spoken, the launch repository is the schedule.
	h.waitPolls(t, 1)
	h.waitPrimed(t)
	if got := h.counting.countPath("/repos/acme/a/"); got != 1 {
		t.Fatalf("the launch repository was polled %d times before discovery classified it, want 1 (R32)", got)
	}

	// Discovery classifies both: the launch repository has no Runs and stays out of the poll
	// set, the other has Runs and joins it.
	classified.set(first, withRuns)
	h.ps.set(withRuns)
	h.s.PollSetChanged()
	h.waitPolls(t, 1)

	before := h.counting.countPath("/repos/acme/a/")
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 1) // the classified repository, and only it
	h.waitSettle(t, slowTarget)

	if got := h.counting.countPath("/repos/acme/a/"); got != before {
		t.Errorf("the launch repository was polled %d times after being classified as having no Runs, want it held at %d (R2)", got, before)
	}
	if got := h.counting.countPath("/repos/acme/b/"); got < 1 {
		t.Errorf("the classified repository was polled %d times, want it polling: the control has stopped controlling", got)
	}
}

// TestFastPathIsPolledWhenDiscoveryIsCold is R32's cold-cache half. The poll set is empty,
// because discovery has not run and nothing is persisted, and the fast path is still
// painted from its single request. Then discovery lands and the rest of the set joins the
// rotation with no restart (R3), the fast-path repository still among them.
func TestFastPathIsPolledWhenDiscoveryIsCold(t *testing.T) {
	first, b := gh("acme", "a"), gh("acme", "b")
	h := newHarness(t, harnessConfig{base: stubRT{}, first: first})
	h.start(t)

	h.waitPolls(t, 1)
	h.waitPrimed(t)
	if got := h.counting.runsListings(); got != 1 {
		t.Fatalf("Run listings from an empty poll set with a fast path = %d, want exactly 1 (R32): %v", got, h.counting.urls)
	}
	if got := h.counting.countPath("/repos/acme/a/actions/runs"); got != 1 {
		t.Fatalf("the fast path's repository listed %d times, want 1", got)
	}

	// Discovery lands behind the fast path. The repository it adds enters the rotation, and
	// the launch repository stays in it because nothing has classified it: no Classified is
	// wired here, which is the "discovery has said nothing" case, the one repo-discovery R22
	// answers by adopting a repository enumeration never returned (issue #100). It is not an
	// exemption from the classification, which TestFastPathDefersToTheClassification pins.
	h.ps.set(b)
	h.blockUntil(1)
	h.clk.Advance(slowTarget)
	h.waitPolls(t, 2)

	if got := h.counting.countPath("/repos/acme/b/actions/runs"); got < 1 {
		t.Errorf("the repository discovery added listed %d times, want it polling (R33)", got)
	}
	if got := h.counting.countPath("/repos/acme/a/actions/runs"); got < 2 {
		t.Errorf("the fast path's repository listed %d times, want it still polling after discovery landed", got)
	}
}
