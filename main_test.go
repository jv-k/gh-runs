package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
)

// TestFastPathRepo pins R35's host gate and the value it yields the engine. A resolver
// reporting a non-github.com host is a rejection the TUI surfaces, while a github.com
// repository, a session outside any repository, and any other resolver error are not, so the
// Feed falls back to progressive reveal rather than refusing to open (R34, AC17). The
// rejection reuses the domain's typed UnsupportedHostError, the same value NewRepoID and
// discovery raise.
//
// The repository it returns is the engine's Options.First, the one polled alone at the cold
// start (R32), so the two non-rejection cases are distinguished by what comes back rather
// than by the absent error alone: a resolved repository is a fast path, an unresolved one is
// none at all.
func TestFastPathRepo(t *testing.T) {
	local := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"}

	t.Run("foreign host is rejected", func(t *testing.T) {
		_, _, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, &domain.UnsupportedHostError{Host: "ghe.example.com"}
		}, nil)
		if err == nil {
			t.Fatal("a non-github.com host was not rejected (R35)")
		}
		var unsupported *domain.UnsupportedHostError
		if !errors.As(err, &unsupported) || unsupported.Host != "ghe.example.com" {
			t.Fatalf("rejection is not an UnsupportedHostError naming the host: %v", err)
		}
	})

	t.Run("wrapped foreign host is still rejected", func(t *testing.T) {
		_, _, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, fmt.Errorf("resolve current: %w", &domain.UnsupportedHostError{Host: "tenant.ghe.com"})
		}, nil)
		if err == nil {
			t.Fatal("a wrapped foreign-host rejection was not caught by errors.As (R35)")
		}
	})

	t.Run("github.com repository is the fast path", func(t *testing.T) {
		id, _, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, nil)
		if err != nil {
			t.Fatalf("a github.com repository was rejected: %v", err)
		}
		if id != local {
			t.Fatalf("fast path = %v, want %v: this is the repository the engine polls first (R32)", id, local)
		}
	})

	t.Run("an excluded repository is not the fast path", func(t *testing.T) {
		// settings R7: exclusion removes a repository from discovery, the Feed and all
		// polling. Options.First is a polling path that bypasses discovery, so it has to
		// honour the list too, on the same ground discovery.FastPath already refuses one.
		id, _, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, []domain.RepoID{local})
		if err != nil {
			t.Fatalf("an excluded repository was treated as a rejection: %v", err)
		}
		if id != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none: an excluded repository is removed from all polling (settings R7)", id)
		}
	})

	t.Run("exclusion does not catch a different repository", func(t *testing.T) {
		other := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "deslopper"}
		id, _, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, []domain.RepoID{other})
		if err != nil {
			t.Fatalf("unexpected rejection: %v", err)
		}
		if id != local {
			t.Fatalf("fast path = %v, want %v: only the excluded identity is removed", id, local)
		}
	})

	t.Run("outside a repository there is no fast path", func(t *testing.T) {
		// R34: an unresolvable remote is not a rejection; the Feed reveals the account.
		id, advice, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, errors.New("not launched inside a github.com repository")
		}, nil)
		if err != nil {
			t.Fatalf("an unresolvable remote was treated as a rejection: %v", err)
		}
		if advice != nil {
			t.Errorf("advice = %v, want none: a launch outside any repository is an ordinary session, and an instruction here would name a problem the operator does not have", advice)
		}
		if id != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none (R34: progressive reveal across the account)", id)
		}
	})

	t.Run("an unrecognised remote yields R14's instruction, not a rejection", func(t *testing.T) {
		// repo-discovery R14. On a machine where gh was never installed, go-gh's resolver
		// fails on the KnownHosts trap even though git works and the remote is plainly
		// github.com. Setting GH_TOKEN fixes it in one step, so the instruction is worth
		// showing. It is not a rejection: the dashboard still opens and reveals whatever the
		// token can see, which is R34's fallback.
		id, advice, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, fmt.Errorf("%w: probing remotes", ghclient.ErrRemoteHostUnrecognised)
		}, nil)
		if err != nil {
			t.Fatalf("an unrecognised remote was treated as a rejection: %v", err)
		}
		if advice == nil {
			t.Fatal("no advice returned: R14's GH_TOKEN instruction never reaches the user")
		}
		if !errors.Is(advice, ghclient.ErrRemoteHostUnrecognised) {
			t.Errorf("advice = %v, want the resolver's own condition surfaced", advice)
		}
		if id != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none: the remote did not resolve", id)
		}
	})
}
