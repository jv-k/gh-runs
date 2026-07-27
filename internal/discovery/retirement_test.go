package discovery_test

import (
	"context"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// R23 retires a repository whose last two consecutive probes returned a definitive
// failure, and retires it on no other signal. These tests are AC16's four sequences
// and AC17's two survivals. Every one of them drives real probes through the real
// transport chain against a cassette, so the governor's verdict on a 403 is the one
// the fixture's body earns rather than one the test asserts into being.

// alphaID and betaID are the two repositories the retirement cassettes tape.
func alphaID(t *testing.T) domain.RepoID { return mustRepoID(t, "jv-k", "alpha") }
func betaID(t *testing.T) domain.RepoID  { return mustRepoID(t, "jv-k", "beta") }

func mustRepoID(t *testing.T, owner, name string) domain.RepoID {
	t.Helper()
	id, err := domain.NewRepoID(domain.HostGitHub, owner, name)
	if err != nil {
		t.Fatalf("build repo id %s/%s: %v", owner, name, err)
	}
	return id
}

// holds reports whether discovery still has a record for id. Retirement is defined
// as the record leaving the set, so this is the assertion every sequence makes.
func holds(d *discovery.Discovery, id domain.RepoID) bool {
	for _, r := range d.Records() {
		if r.ID() == id {
			return true
		}
	}
	return false
}

// TestRetirementTakesTwoConsecutiveDefinitiveFailures is AC16's first sequence. A
// repository answering 404 twice leaves the set, and the repository probed beside it
// that never failed stays, so the retirement is scoped to the record that earned it.
func TestRetirementTakesTwoConsecutiveDefinitiveFailures(t *testing.T) {
	h := newHarness(t, "retire_two_definitive", "", withSequentialReplay())
	ctx := context.Background()
	alpha, beta := alphaID(t), betaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if !holds(h.disc, alpha) || !holds(h.disc, beta) {
		t.Fatalf("after the pass both repositories should be held, got alpha=%v beta=%v",
			holds(h.disc, alpha), holds(h.disc, beta))
	}

	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	if !holds(h.disc, alpha) {
		t.Fatal("one definitive failure retired alpha, want it held until the second")
	}

	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	if holds(h.disc, alpha) {
		t.Error("two consecutive definitive failures left alpha in the set, want it retired")
	}
	if !holds(h.disc, beta) {
		t.Error("retiring alpha also dropped beta, want retirement scoped to the failing record")
	}
}

// TestRetirementLeavesThePersistedDocument is the other half of AC16's first
// sequence: the next persist must write a document without the retired repository,
// so a later launch does not carry it back in. Reprobe persists, so the retiring
// re-probe is itself the persist under test.
func TestRetirementLeavesThePersistedDocument(t *testing.T) {
	h := newHarness(t, "retire_two_definitive", "", withSequentialReplay())
	ctx := context.Background()
	alpha := alphaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)

	// A second Discovery over the same store is the next launch reading what this one
	// wrote. It must not re-admit the retired repository.
	next := h.relaunch()
	if n := next.Reload(); n == 0 {
		t.Fatal("the reload read nothing, so this asserts a cold start rather than the document")
	}
	if holds(next, alpha) {
		t.Error("a retired repository came back on reload, want it absent from the document")
	}
	if !holds(next, betaID(t)) {
		t.Error("the reload dropped beta, so the document lost more than the retirement")
	}
}

// TestASuccessfulProbeResetsTheCount is AC16's second sequence. 404 then 200 leaves
// the repository present, and a third 404 does not retire it, which is the count
// having reset rather than merely not having reached two.
func TestASuccessfulProbeResetsTheCount(t *testing.T) {
	h := newHarness(t, "retire_reset_on_success", "", withSequentialReplay())
	ctx := context.Background()
	alpha := alphaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 404, count one
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 200, count resets
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 404, count one again

	if !holds(h.disc, alpha) {
		t.Error("alpha retired after 404, 200, 404, want the success to have reset the count")
	}
}

// TestATransientAnswerNeitherRetiresNorPostpones is AC16's third sequence. 404, then
// a 5xx, then 404 retires: the transient answer left the count untouched in both
// directions. Resetting on a transient reads as the safer choice and is not, because
// a flaky connection would then make a deleted repository immortal.
func TestATransientAnswerNeitherRetiresNorPostpones(t *testing.T) {
	h := newHarness(t, "retire_transient_between", "", withSequentialReplay())
	ctx := context.Background()
	alpha := alphaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 404, count one
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 502, untouched
	if !holds(h.disc, alpha) {
		t.Fatal("the 5xx retired alpha, want a transient answer to retire nothing")
	}

	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 404, count two
	if holds(h.disc, alpha) {
		t.Error("alpha survived 404, 5xx, 404, want the transient answer not to have postponed it")
	}
}

// TestARateLimitedForbiddenRetiresNothing is AC16's fourth sequence. The fixture's
// body names a rate-limit page, so the governor classifies it as rate limiting, and
// R23 requires that outcome to be counted in neither direction however many arrive.
// The body is the load-bearing part: a bare 403 classifies as rate limiting by
// default and would pass this test for the wrong reason.
func TestARateLimitedForbiddenRetiresNothing(t *testing.T) {
	h := newHarness(t, "retire_ratelimited_403", "", withSequentialReplay())
	ctx := context.Background()
	alpha := alphaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	for i := range 3 {
		h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
		if !holds(h.disc, alpha) {
			t.Fatalf("a rate-limited 403 retired alpha after %d of them, want none of them to count", i+1)
		}
	}
}

// TestAnAuthorizationForbiddenIsDefinitive proves 404 is not R23's only definitive
// signal. The fixture's documentation_url points at the reference page for the very
// endpoint the probe requested, which is the correspondence the governor's classifier
// measures, so the verdict reads not-rate-limited and the failure counts.
func TestAnAuthorizationForbiddenIsDefinitive(t *testing.T) {
	h := newHarness(t, "retire_authorization_403", "", withSequentialReplay())
	ctx := context.Background()
	alpha := alphaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	if !holds(h.disc, alpha) {
		t.Fatal("one authorization 403 retired alpha, want it held until the second")
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	if holds(h.disc, alpha) {
		t.Error("two authorization 403s left alpha in the set, want it retired")
	}
}

// TestACountOfOneSurvivesALaunch is AC17's second survival. A repository carrying one
// definitive failure at persist time reloads with that count and retires on its next
// definitive failure rather than starting over. Without a persisted count the policy
// is inert for short sessions, because under one revalidation interval a repository
// is probed exactly once and two failures never happen in one launch.
func TestACountOfOneSurvivesALaunch(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	alpha := alphaID(t)

	h := newHarness(t, "retire_two_definitive", dir, withSequentialReplay())
	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil) // 404, count one, and persisted
	if !holds(h.disc, alpha) {
		t.Fatal("one definitive failure retired alpha, want it held")
	}

	// The next launch reads the same document through the same store, so it inherits
	// the count of one. Its first definitive failure is the second consecutive one.
	next := h.relaunch()
	if n := next.Reload(); n == 0 {
		t.Fatal("the reload read nothing, so this asserts a cold start rather than the count")
	}
	if !holds(next, alpha) {
		t.Fatal("the reload dropped alpha before it had failed twice")
	}
	next.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	if holds(next, alpha) {
		t.Error("the reloaded count restarted at zero, want the persisted one to have carried")
	}
}

// TestADocumentWithoutTheCountFieldReloadsAtZero is AC17's third survival. A record
// persisted before the field existed reloads with the count at zero, which delays a
// retirement and never causes one. pass_basic.yaml's document is written by a pass
// that never failed, so every count in it is the zero value, which is exactly the
// shape an older document has.
func TestADocumentWithoutTheCountFieldReloadsAtZero(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	h := newHarness(t, "pass_basic", dir)
	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}

	next := h.relaunch()
	n := next.Reload()
	if n == 0 {
		t.Fatal("the reload read nothing, so there is no document to assert about")
	}
	if got := len(next.Records()); got != n {
		t.Errorf("reload admitted %d records but reported %d, want them equal", got, n)
	}
	for _, r := range next.Records() {
		if r.DefinitiveFailures != 0 {
			t.Errorf("%s reloaded carrying %d definitive failures, want zero on a document that recorded none",
				r.ID(), r.DefinitiveFailures)
		}
	}
}

// TestMembershipCarriesEveryRecordWhateverItsCapability is live-run-feed R37's reader.
// The Feed must learn that a repository has left discovery's set from a set whose
// meaning is membership, never from absence in the capability set R18 gates on, so
// this must return a repository whose capability is not yet Known.
func TestMembershipCarriesEveryRecordWhateverItsCapability(t *testing.T) {
	h := newHarness(t, "retire_two_definitive", "", withSequentialReplay())
	ctx := context.Background()
	alpha, beta := alphaID(t), betaID(t)

	if err := h.disc.Pass(ctx, nil); err != nil {
		t.Fatalf("pass: %v", err)
	}
	members := h.disc.Membership()
	if len(members) != len(h.disc.Records()) {
		t.Errorf("membership carries %d ids for %d records, want one per record",
			len(members), len(h.disc.Records()))
	}
	if !contains(members, alpha) || !contains(members, beta) {
		t.Fatalf("membership %v does not carry both enumerated repositories", members)
	}

	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)
	h.disc.Reprobe(ctx, []domain.RepoID{alpha}, nil)

	members = h.disc.Membership()
	if contains(members, alpha) {
		t.Error("membership still carries a retired repository, so the Feed's prunes could never fire")
	}
	if !contains(members, beta) {
		t.Error("membership dropped a repository that was never retired")
	}
}

func contains(ids []domain.RepoID, want domain.RepoID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
