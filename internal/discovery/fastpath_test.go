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
