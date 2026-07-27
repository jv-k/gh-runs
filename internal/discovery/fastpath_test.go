package discovery_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestFastPathYieldsLocalWithCapabilityNotYetKnown is AC8 and AC11. The repository
// the tool launched inside is resolved and painted from a single Run-listing
// request, before enumeration or any other probe. Until enumeration or adoption
// records its capability, that capability reads not-yet-known, and it is never
// inferred from the fact that the repository's Runs listed.
func TestFastPathYieldsLocalWithCapabilityNotYetKnown(t *testing.T) {
	local := gh("jv-k", "local")
	h := newHarness(t, "fastpath", "", withCurrent(func() (domain.RepoID, error) {
		return local, nil
	}))

	var emitted []discovery.Record
	id, resolved, err := h.disc.FastPath(context.Background(), func(r discovery.Record) {
		emitted = append(emitted, r)
	})
	if err != nil {
		t.Fatalf("FastPath: %v", err)
	}
	if !resolved || id != local {
		t.Fatalf("FastPath resolved=%v id=%v, want true %v", resolved, id, local)
	}

	// AC11: painted after exactly one Run-listing request, and yielded to the
	// consumer.
	if n := h.counting.count(); n != 1 {
		t.Errorf("fast path issued %d requests, want 1", n)
	}
	if len(emitted) != 1 || emitted[0].ID() != local {
		t.Errorf("fast path emitted %v, want one record for %v", emitted, local)
	}
	// It has Runs, so it is in the poll set even before enumeration.
	if got := pollSetKeys(h.disc); strings.Join(got, ",") != "github.com/jv-k/local" {
		t.Errorf("poll set after fast path = %v, want [github.com/jv-k/local]", got)
	}

	// AC8: its capability reads not-yet-known. It is not permitted, not refused, and
	// not inferred from its Runs having listed.
	if got := h.disc.Capability(local); got != domain.CapabilityUnknown {
		t.Errorf("fast-path capability = %v, want not-yet-known (AC8)", got)
	}
}

// TestAdoptsFastPathRepoNotEnumerated is R22. When enumeration does not return the
// fast-path repository (a clone the account does not own), discovery spends exactly
// one GET /repos/{owner}/{repo} to learn its capability and admits it for the
// session. Its capability becomes known, it is marked adopted, and a session
// launched elsewhere never sees it: a reload over the same store admits the
// enumerated members but not the adopted clone.
func TestAdoptsFastPathRepoNotEnumerated(t *testing.T) {
	local := gh("jv-k", "local")
	h := newHarness(t, "fastpath", "", withCurrent(func() (domain.RepoID, error) {
		return local, nil
	}))

	if err := h.disc.Discover(context.Background(), nil); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// R22: adoption spent exactly one GET /repos/jv-k/local, distinct from the probe
	// at /repos/jv-k/local/actions/runs.
	if n := h.counting.countExact("https://api.github.com/repos/jv-k/local"); n != 1 {
		t.Errorf("adoption requests to GET /repos/jv-k/local = %d, want 1", n)
	}

	// Capability is now known and gated from the adopted repository's permissions.
	if got := h.disc.Capability(local); got != domain.CapabilityPermitted {
		t.Errorf("adopted capability = %v, want permitted", got)
	}
	byID := recordsByID(h.disc)
	if rec, ok := byID[local.String()]; !ok || !rec.Adopted {
		t.Errorf("local record adopted flag = %v (present=%v), want adopted", ok && rec.Adopted, ok)
	}

	// The session sees the adopted repository plus the enumerated members with Runs.
	got := pollSetKeys(h.disc)
	want := []string{"github.com/acme/owned-a", "github.com/jv-k/local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set = %v, want %v", got, want)
	}

	// A session launched elsewhere reloads the persisted results and never sees the
	// adopted clone: only the enumerated members return. This is R22's membership
	// rule, the Feed never accreting past clones.
	second := newHarness(t, "fastpath", h.dir)
	second.disc.Reload()
	if got := pollSetKeys(second.disc); strings.Join(got, ",") != "github.com/acme/owned-a" {
		t.Errorf("reloaded poll set = %v, want only github.com/acme/owned-a (the adopted clone is not re-admitted)", got)
	}
}

// TestAdoptLaunchAdmitsARepositoryEnumerationDidNotReturn is R22 reached the way the
// composition root reaches it, from an identity that is already resolved.
//
// main.go resolves the launch repository once, before the engine exists, because the
// resolver shells out to git and one launch needs the answer twice (as the scheduler's
// Options.First and as R35's host gate). Driving adoption through Discover would resolve
// it a second time and re-probe its Run listing, so the entrypoint the root calls takes
// the identity rather than the resolver.
//
// No resolver is configured here for exactly that reason: the seam under test must work
// with none, or the root is obliged to hand discovery a resolver it should never call.
func TestAdoptLaunchAdmitsARepositoryEnumerationDidNotReturn(t *testing.T) {
	local := gh("jv-k", "local")
	h := newHarness(t, "fastpath", "")

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	// The precondition is the defect this closes: enumeration does not return the
	// repository the operator is sitting in, so its capability is not-yet-known and every
	// destructive action stays disabled for the session (R8, AC8).
	if got := h.disc.Capability(local); got != domain.CapabilityUnknown {
		t.Fatalf("precondition: capability = %v, want not-yet-known", got)
	}

	if err := h.disc.AdoptLaunch(context.Background(), local, nil); err != nil {
		t.Fatalf("AdoptLaunch: %v", err)
	}

	if got := h.disc.Capability(local); got != domain.CapabilityPermitted {
		t.Errorf("capability after adoption = %v, want permitted (R22)", got)
	}
	// R22 admits it "to the poll set if it has Runs", so the classification has to be
	// established as well as the capability. A record carrying capability alone would read
	// as having no Runs, and the launch repository would leave the poll set at the moment
	// the scheduler stopped treating it as a special case: the Feed would go quiet for the
	// one repository the operator is certainly watching.
	got := pollSetKeys(h.disc)
	want := []string{"github.com/acme/owned-a", "github.com/jv-k/local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("poll set after adoption = %v, want %v", got, want)
	}
}

// TestAdoptLaunchSpendsNothingOnAnEnumeratedMember pins R22's condition, not just its
// effect. Adoption is for a repository enumeration did not return. A member it did return
// is already Known from its enumeration payload, so adoption must cost no request at all,
// or every launch inside an ordinary repository pays for a capability it already has.
func TestAdoptLaunchSpendsNothingOnAnEnumeratedMember(t *testing.T) {
	member := gh("acme", "owned-a")
	h := newHarness(t, "fastpath", "")

	if err := h.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	before := h.counting.count()

	if err := h.disc.AdoptLaunch(context.Background(), member, nil); err != nil {
		t.Fatalf("AdoptLaunch: %v", err)
	}

	if got := h.counting.count() - before; got != 0 {
		t.Errorf("adoption of an enumerated member issued %d requests, want 0 (R22 adopts only what enumeration did not return)", got)
	}
}

// TestAdoptLaunchIsANoOpOutsideAnyRepository pins the zero identity. A launch outside a
// git repository, or one whose remote did not resolve, has no launch repository, and the
// root passes the zero value rather than branching. Spending a request on it would ask
// the API for a repository named "/".
func TestAdoptLaunchIsANoOpOutsideAnyRepository(t *testing.T) {
	h := newHarness(t, "fastpath", "")

	if err := h.disc.AdoptLaunch(context.Background(), domain.RepoID{}, nil); err != nil {
		t.Fatalf("AdoptLaunch for the zero repository: %v", err)
	}
	if got := h.counting.count(); got != 0 {
		t.Errorf("adoption outside any repository issued %d requests, want 0", got)
	}
}

// TestAdoptLaunchRefusesAnExcludedRepository is settings R7 and AC5 at the new entrypoint.
// An excluded repository receives zero requests, and the terminal happening to sit inside
// it is not an exception. FastPath already refuses one, so this keeps the two entrypoints
// agreeing rather than leaving the rule true of only the path Discover drives.
func TestAdoptLaunchRefusesAnExcludedRepository(t *testing.T) {
	local := gh("jv-k", "local")
	h := newHarness(t, "fastpath", "", withExclude(local))

	if err := h.disc.AdoptLaunch(context.Background(), local, nil); err != nil {
		t.Fatalf("AdoptLaunch for an excluded repository: %v", err)
	}
	if got := h.counting.count(); got != 0 {
		t.Errorf("adoption of an excluded repository issued %d requests, want 0 (settings R7, AC5)", got)
	}
	if got := h.disc.Capability(local); got != domain.CapabilityUnknown {
		t.Errorf("excluded repository capability = %v, want not-yet-known: it must be in no record at all", got)
	}
}

// TestDiscoverSurfacesFastPathError pins the orchestration path's contract: Discover
// records the fast-path resolver's error (R14's actionable GH_TOKEN instruction)
// through FastPathErr rather than discarding it, and still completes the enumerate
// and classify pass. A session launched where the fast path fails discovers the
// account and can surface the instruction alongside the results.
func TestDiscoverSurfacesFastPathError(t *testing.T) {
	wantErr := errors.New("set GH_TOKEN to discover the current repository")
	h := newHarness(t, "fastpath", "", withCurrent(func() (domain.RepoID, error) {
		return domain.RepoID{}, wantErr
	}))

	if err := h.disc.Discover(context.Background(), nil); err != nil {
		t.Fatalf("Discover returned a fatal error for a non-fatal fast-path failure: %v", err)
	}
	if err := h.disc.FastPathErr(); !errors.Is(err, wantErr) {
		t.Errorf("FastPathErr = %v, want the resolver's error surfaced rather than discarded", err)
	}

	// The pass still ran: the account's one enumerated member with Runs is in the
	// poll set, and the unresolved fast-path repository is not.
	if got := pollSetKeys(h.disc); strings.Join(got, ",") != "github.com/acme/owned-a" {
		t.Errorf("poll set = %v, want github.com/acme/owned-a (discovery proceeded past the fast-path failure)", got)
	}
}

// TestFastPathResolverErrorIsSurfacedNotFatal pins R14's failure contract at the
// engine. A resolver that cannot determine the current repository (the KnownHosts
// trap main.go translates into the GH_TOKEN instruction, or an unsupported host)
// surfaces its error from FastPath and stops no pass: Discover proceeds to
// enumerate the account, so a session launched outside any repository still works.
func TestFastPathResolverErrorIsSurfacedNotFatal(t *testing.T) {
	wantErr := errors.New("set GH_TOKEN to discover the current repository")
	h := newHarness(t, "fastpath", "", withCurrent(func() (domain.RepoID, error) {
		return domain.RepoID{}, wantErr
	}))

	_, resolved, err := h.disc.FastPath(context.Background(), nil)
	if resolved {
		t.Error("FastPath reported a resolved repository despite the resolver error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("FastPath error = %v, want the resolver's own error surfaced", err)
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("fast path issued %d requests after a resolver error, want 0", n)
	}
}
