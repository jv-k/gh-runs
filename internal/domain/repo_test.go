package domain_test

import (
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestRepoCapability pins live-run-feed R17's gate: a Repo is Permitted only with
// push and not archived, and Refused otherwise. The gate fails closed, so every
// case that is not exactly "push and live" is Refused, which is what keeps a
// destructive action off a repository the token cannot write or that is frozen.
func TestRepoCapability(t *testing.T) {
	cases := []struct {
		name     string
		push     bool
		archived bool
		want     domain.Capability
	}{
		{"push and live is permitted", true, false, domain.CapabilityPermitted},
		{"no push is refused", false, false, domain.CapabilityRefused},
		{"archived is refused even with push", true, true, domain.CapabilityRefused},
		{"no push and archived is refused", false, true, domain.CapabilityRefused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := domain.Repo{
				Permissions: domain.Permissions{Push: c.push},
				Archived:    c.archived,
			}
			if got := r.Capability(); got != c.want {
				t.Fatalf("Capability() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRepoCapabilityNeverUnknown pins ADR-0014's decision that the method returns
// only the two known values: holding a Repo is proof it was enumerated, so
// CapabilityUnknown (the zero value that fails destructive actions closed) is
// never a return of this method, only a not-yet-enumerated state elsewhere.
func TestRepoCapabilityNeverUnknown(t *testing.T) {
	var empty domain.Repo // the zero Repo: no permissions, not archived
	if got := empty.Capability(); got == domain.CapabilityUnknown {
		t.Fatalf("Capability() returned CapabilityUnknown; it must resolve to a known value")
	}
}
