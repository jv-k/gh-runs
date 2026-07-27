package main

import (
	"errors"
	"fmt"
	"sync/atomic"
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
		_, err := fastPathRepo(func() (domain.RepoID, error) {
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
		_, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, fmt.Errorf("resolve current: %w", &domain.UnsupportedHostError{Host: "tenant.ghe.com"})
		}, nil)
		if err == nil {
			t.Fatal("a wrapped foreign-host rejection was not caught by errors.As (R35)")
		}
	})

	t.Run("github.com repository is the fast path", func(t *testing.T) {
		got, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, nil)
		if err != nil {
			t.Fatalf("a github.com repository was rejected: %v", err)
		}
		if got.repo != local {
			t.Fatalf("fast path = %v, want %v: this is the repository the engine polls first (R32)", got.repo, local)
		}
	})

	t.Run("an excluded repository is not the fast path", func(t *testing.T) {
		// settings R7: exclusion removes a repository from discovery, the Feed and all
		// polling. Options.First is a polling path that bypasses discovery, so it has to
		// honour the list too, on the same ground discovery.FastPath already refuses one.
		got, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, []domain.RepoID{local})
		if err != nil {
			t.Fatalf("an excluded repository was treated as a rejection: %v", err)
		}
		if got.repo != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none: an excluded repository is removed from all polling (settings R7)", got.repo)
		}
	})

	t.Run("exclusion does not catch a different repository", func(t *testing.T) {
		other := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "deslopper"}
		got, err := fastPathRepo(func() (domain.RepoID, error) { return local, nil }, []domain.RepoID{other})
		if err != nil {
			t.Fatalf("unexpected rejection: %v", err)
		}
		if got.repo != local {
			t.Fatalf("fast path = %v, want %v: only the excluded identity is removed", got.repo, local)
		}
	})

	t.Run("outside a repository there is no fast path", func(t *testing.T) {
		// R34: an unresolvable remote is not a rejection; the Feed reveals the account.
		got, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, errors.New("not launched inside a github.com repository")
		}, nil)
		if err != nil {
			t.Fatalf("an unresolvable remote was treated as a rejection: %v", err)
		}
		if got.advice != nil {
			t.Errorf("advice = %v, want none: a launch outside any repository is an ordinary session, and an instruction here would name a problem the operator does not have", got.advice)
		}
		if got.repo != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none (R34: progressive reveal across the account)", got.repo)
		}
	})

	t.Run("an unrecognised remote yields R14's instruction, not a rejection", func(t *testing.T) {
		// repo-discovery R14. On a machine where gh was never installed, go-gh's resolver
		// fails on the KnownHosts trap even though git works and the remote is plainly
		// github.com. Setting GH_TOKEN fixes it in one step, so the instruction is worth
		// showing. It is not a rejection: the dashboard still opens and reveals whatever the
		// token can see, which is R34's fallback.
		got, err := fastPathRepo(func() (domain.RepoID, error) {
			return domain.RepoID{}, fmt.Errorf("%w: probing remotes", ghclient.ErrRemoteHostUnrecognised)
		}, nil)
		if err != nil {
			t.Fatalf("an unrecognised remote was treated as a rejection: %v", err)
		}
		if got.advice == nil {
			t.Fatal("no advice returned: R14's GH_TOKEN instruction never reaches the user")
		}
		if !errors.Is(got.advice, ghclient.ErrRemoteHostUnrecognised) {
			t.Errorf("advice = %v, want the resolver's own condition surfaced", got.advice)
		}
		if got.repo != (domain.RepoID{}) {
			t.Fatalf("fast path = %v, want none: the remote did not resolve", got.repo)
		}
	})
}

// TestFullRefreshRunsOnePassAtATime pins the guard on repo-discovery R11's on-demand full
// refresh. It is not politeness: fanOut spawns a goroutine per repository and relies on the
// transport limiter for its wire bound, so two concurrent passes put double the requests in
// flight against a limiter sized for one, and both then write the same document. A held key
// repeats, so this is reachable by leaning on u.
//
// A press arriving while a pass runs is dropped rather than queued, because the pass already
// in flight is about to produce exactly the state the second press was asking for.
func TestFullRefreshRunsOnePassAtATime(t *testing.T) {
	var passes atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan struct{})

	// A stand-in for the pass, holding the first caller inside until the test releases it.
	refresh := guardOnePass(func() {
		if passes.Add(1) == 1 {
			close(entered)
		}
		<-release
	})

	go func() { refresh(); close(done) }()
	<-entered // the first pass is inside and has not returned

	// Every press while it runs is dropped. None may block, and none may start a pass.
	for range 5 {
		refresh()
	}
	if got := passes.Load(); got != 1 {
		t.Errorf("%d passes ran concurrently, want the guard to admit one", got)
	}

	close(release)
	<-done

	// A mutual exclusion, not a sync.Once: once the first has returned, a later press
	// still refreshes. Without this the guard would silently make u work exactly once.
	refresh()
	if got := passes.Load(); got != 2 {
		t.Errorf("the guard admitted %d passes in total, want it to reopen after the first returned", got)
	}
}
