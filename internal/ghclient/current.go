package ghclient

import (
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// hostGitHub is the one host 2.0.0 serves (ADR-0009). The fast-path resolver
// rejects any other explicitly.
const hostGitHub = "github.com"

// CurrentRepo resolves the repository the tool was launched inside, host-qualified
// (repo-discovery R14). main.go passes it to discovery as the fast-path resolver,
// so discovery can paint the local repository's Runs from one request without
// waiting on enumeration.
//
// It wraps go-gh's repository.Current, which carries a trap: Current gates its
// answer on auth.KnownHosts, and KnownHosts reads only environment variables and
// hosts.yml, never the keyring. On a machine where gh was never installed, Current
// fails with "none of the git remotes point to a known GitHub host" even though
// git works and the remote is plainly github.com. The GH_TOKEN contract populates
// KnownHosts and clears the trap (ADR-0002), so the resolver surfaces that
// instruction rather than the library's message, which names the wrong problem.
func CurrentRepo() (domain.RepoID, error) {
	repo, err := repository.Current()
	return resolveCurrent(repo, err)
}

// resolveCurrent is CurrentRepo's pure core: it translates go-gh's result into a
// host-qualified RepoID or an actionable error, so the translation is testable
// without a git remote. A KnownHosts failure becomes the GH_TOKEN instruction
// (R14), and a host other than github.com is rejected with the domain's
// UnsupportedHostError, which names the host and claims nothing about its class
// (ADR-0009). Returning that typed error rather than a bespoke string is what lets
// the TUI reject a foreign host explicitly through errors.As (live-run-feed R35),
// the same value discovery and the CLI already raise from domain.NewRepoID.
func resolveCurrent(repo repository.Repository, err error) (domain.RepoID, error) {
	if err != nil {
		if strings.Contains(err.Error(), "known GitHub host") {
			return domain.RepoID{}, fmt.Errorf(
				"could not recognise this repository's git remote as GitHub: %w; "+
					"if gh is not installed, set GH_TOKEN so the remote's host is known", err)
		}
		return domain.RepoID{}, fmt.Errorf("not launched inside a github.com repository: %w", err)
	}
	if !strings.EqualFold(repo.Host, hostGitHub) {
		return domain.RepoID{}, &domain.UnsupportedHostError{Host: repo.Host}
	}
	return domain.RepoID{Host: hostGitHub, Owner: repo.Owner, Name: repo.Name}, nil
}
