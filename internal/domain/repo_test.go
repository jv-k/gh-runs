package domain_test

import (
	"errors"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestNewRepoID pins the one validation home (ADR-0009, repo-discovery R18). A
// github.com owner/name is built; a foreign host is an UnsupportedHostError; and an
// owner or name outside GitHub's charset is an InvalidRepoError, so no path-unsafe
// segment can be built into an identity that later reaches a request URL path or a
// filesystem key. discovery and the CLI's -R both route here, so this is where the
// invariant is proven.
func TestNewRepoID(t *testing.T) {
	t.Run("valid github.com repository is built", func(t *testing.T) {
		id, err := domain.NewRepoID("github.com", "cli", "cli")
		if err != nil {
			t.Fatalf("NewRepoID returned error %v, want nil", err)
		}
		if id != (domain.RepoID{Host: "github.com", Owner: "cli", Name: "cli"}) {
			t.Errorf("NewRepoID = %+v, want github.com/cli/cli", id)
		}
	})

	t.Run("foreign host is an UnsupportedHostError", func(t *testing.T) {
		_, err := domain.NewRepoID("ghe.corp", "o", "r")
		var uhe *domain.UnsupportedHostError
		if !errors.As(err, &uhe) {
			t.Fatalf("NewRepoID error = %v, want an UnsupportedHostError", err)
		}
	})

	t.Run("path-unsafe owner and name are an InvalidRepoError", func(t *testing.T) {
		for _, c := range []struct{ owner, name string }{
			{"foo", "bar?actor=x"},  // a query in the name
			{"foo", ".."},           // parent-directory traversal
			{"foo", "bar%2F..%2Fx"}, // encoded slash
			{"foo/bar", "baz"},      // a slash smuggled into the owner
			{"", "repo"},            // empty owner
			{"owner", ""},           // empty name
		} {
			_, err := domain.NewRepoID("github.com", c.owner, c.name)
			var ire *domain.InvalidRepoError
			if !errors.As(err, &ire) {
				t.Errorf("NewRepoID(%q, %q) error = %v, want an InvalidRepoError", c.owner, c.name, err)
			}
		}
	})
}

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
