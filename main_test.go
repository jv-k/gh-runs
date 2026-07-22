package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestCurrentHostSupported pins R35's host gate: a resolver reporting a non-github.com host
// is a rejection the TUI surfaces, while a github.com repository, a session outside any
// repository, and any other resolver error are not, so the Feed falls back to progressive
// reveal rather than refusing to open (R34, AC17). The rejection reuses the domain's typed
// UnsupportedHostError, the same value NewRepoID and discovery raise.
func TestCurrentHostSupported(t *testing.T) {
	local := domain.RepoID{Host: domain.HostGitHub, Owner: "jv-k", Name: "gh-runs"}

	t.Run("foreign host is rejected", func(t *testing.T) {
		err := currentHostSupported(func() (domain.RepoID, error) {
			return domain.RepoID{}, &domain.UnsupportedHostError{Host: "ghe.example.com"}
		})
		if err == nil {
			t.Fatal("a non-github.com host was not rejected (R35)")
		}
		var unsupported *domain.UnsupportedHostError
		if !errors.As(err, &unsupported) || unsupported.Host != "ghe.example.com" {
			t.Fatalf("rejection is not an UnsupportedHostError naming the host: %v", err)
		}
	})

	t.Run("wrapped foreign host is still rejected", func(t *testing.T) {
		err := currentHostSupported(func() (domain.RepoID, error) {
			return domain.RepoID{}, fmt.Errorf("resolve current: %w", &domain.UnsupportedHostError{Host: "tenant.ghe.com"})
		})
		if err == nil {
			t.Fatal("a wrapped foreign-host rejection was not caught by errors.As (R35)")
		}
	})

	t.Run("github.com repository proceeds", func(t *testing.T) {
		if err := currentHostSupported(func() (domain.RepoID, error) { return local, nil }); err != nil {
			t.Fatalf("a github.com repository was rejected: %v", err)
		}
	})

	t.Run("outside a repository proceeds", func(t *testing.T) {
		// R34: an unresolvable remote is not a rejection; the Feed reveals the account.
		if err := currentHostSupported(func() (domain.RepoID, error) {
			return domain.RepoID{}, errors.New("not launched inside a github.com repository")
		}); err != nil {
			t.Fatalf("an unresolvable remote was treated as a rejection: %v", err)
		}
	})
}
