package cli

import (
	"fmt"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// hostGitHub is the one host 2.0.0 serves (ADR-0009). Every other host is
// rejected by name, before any request, with a class-neutral message (cli-surface
// R9): it names the host and states that 2.0.0 serves github.com only, claiming
// nothing about whether the host is GHES or Enterprise Cloud, so it cannot be
// false for a tenant.ghe.com user.
const hostGitHub = "github.com"

// scope is the resolved set of repositories a list runs over, and whether the
// invocation fanned out (cli-surface R22). fanout drives the repository column in
// the human table, because rows from different repositories are otherwise
// indistinguishable (R24).
type scope struct {
	repos  []domain.RepoID
	fanout bool
}

// resolveScope decides which repositories a list covers, host-checking every
// input path before any request (cli-surface R8, R9). The precedence is gh's,
// extended where gh has none (R22, ADR-0022):
//
//  1. GH_HOST, if set to anything but github.com, is rejected up front: it would
//     retarget the whole session, so the rejection is offline and names the host
//     (AC7).
//  2. -R, then GH_REPO, each a [HOST/]OWNER/REPO selecting one repository (R8).
//  3. --all-repos forces a fan-out from anywhere, including inside a repository
//     (R22).
//  4. Otherwise the working-directory repository, if the tool was launched inside
//     one (gh's rule, so R2 parity holds).
//  5. Otherwise a fan-out across the discovered set, rather than gh's dead end
//     (R22): no repository means all repositories, not an error.
func resolveScope(deps Deps, f *listFlags) (scope, error) {
	if host, ok := deps.Getenv("GH_HOST"); ok && host != "" {
		if !strings.EqualFold(host, hostGitHub) {
			return scope{}, unsupportedHost(host)
		}
	}

	if f.repo != "" {
		id, err := parseRepoArg(f.repo)
		if err != nil {
			return scope{}, err
		}
		return scope{repos: []domain.RepoID{id}}, nil
	}
	if ghRepo, ok := deps.Getenv("GH_REPO"); ok && ghRepo != "" {
		id, err := parseRepoArg(ghRepo)
		if err != nil {
			return scope{}, err
		}
		return scope{repos: []domain.RepoID{id}}, nil
	}

	if f.allRepos {
		return fanOutScope(deps)
	}

	if deps.Current != nil {
		if id, err := deps.Current(); err == nil {
			return scope{repos: []domain.RepoID{id}}, nil
		}
		// A resolver error means the tool was not launched inside a repository (or
		// its remote is not recognised). That is R22's fan-out trigger, not a
		// failure: no repository means all repositories.
	}
	return fanOutScope(deps)
}

// fanOutScope reads the discovered set through the injected function (cli-surface
// R22). Its error is discovery's, wrapped so an auth failure during enumeration
// reaches the exit-code taxonomy (R17, AC14). An empty set is not an error: a
// fan-out over no repositories lists nothing and exits 0.
func fanOutScope(deps Deps) (scope, error) {
	if deps.Discovered == nil {
		return scope{fanout: true}, nil
	}
	repos, err := deps.Discovered()
	if err != nil {
		return scope{}, fmt.Errorf("discover repositories: %w", err)
	}
	return scope{repos: repos, fanout: true}, nil
}

// parseRepoArg parses a [HOST/]OWNER/REPO selector into a host-qualified identity,
// rejecting any host but github.com by name (cli-surface R8, R9). The bare
// OWNER/REPO form defaults to github.com, and an explicit github.com/OWNER/REPO
// is accepted and treated identically (AC7). A host segment carrying a dot is how
// the three-part and two-part forms are told apart, matching gh's own parse.
func parseRepoArg(arg string) (domain.RepoID, error) {
	parts := strings.Split(arg, "/")
	var host, owner, name string
	switch len(parts) {
	case 2:
		host, owner, name = hostGitHub, parts[0], parts[1]
	case 3:
		host, owner, name = parts[0], parts[1], parts[2]
	default:
		return domain.RepoID{}, fmt.Errorf(
			"invalid repository %q: expected the [HOST/]OWNER/REPO format", arg)
	}
	if owner == "" || name == "" {
		return domain.RepoID{}, fmt.Errorf(
			"invalid repository %q: expected the [HOST/]OWNER/REPO format", arg)
	}
	if !strings.EqualFold(host, hostGitHub) {
		return domain.RepoID{}, unsupportedHost(host)
	}
	return domain.RepoID{Host: hostGitHub, Owner: owner, Name: name}, nil
}

// unsupportedHost is the class-neutral rejection (cli-surface R9, ADR-0009). It
// names the host and claims nothing about its class, so it cannot be false for an
// Enterprise Cloud or GHES host, and it matches the phrasing discovery and
// ghclient already use for the same rejection.
func unsupportedHost(host string) error {
	return fmt.Errorf("repository host %q is not supported; gh-runs 2.0.0 serves github.com only", host)
}
