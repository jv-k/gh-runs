package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// hostGitHub is the one host 2.0.0 serves (ADR-0009). Every other host is
// rejected by name, before any request, with a class-neutral message (cli-surface
// R9): it names the host and states that 2.0.0 serves github.com only, claiming
// nothing about whether the host is GHES or Enterprise Cloud, so it cannot be
// false for a tenant.ghe.com user. It reads from domain.HostGitHub so the host
// string has one home.
const hostGitHub = domain.HostGitHub

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
		if excluded(deps, id) {
			return scope{}, excludedRepo(id)
		}
		return scope{repos: []domain.RepoID{id}}, nil
	}
	if ghRepo, ok := deps.Getenv("GH_REPO"); ok && ghRepo != "" {
		id, err := parseRepoArg(ghRepo)
		if err != nil {
			return scope{}, err
		}
		if excluded(deps, id) {
			return scope{}, excludedRepo(id)
		}
		return scope{repos: []domain.RepoID{id}}, nil
	}

	if f.allRepos {
		return fanOutScope(deps)
	}

	if deps.Current != nil {
		if id, err := deps.Current(); err == nil && !excluded(deps, id) {
			return scope{repos: []domain.RepoID{id}}, nil
		}
		// A resolver error means the tool was not launched inside a repository (or
		// its remote is not recognised). That is R22's fan-out trigger, not a
		// failure: no repository means all repositories.
		//
		// An excluded working directory takes the same branch, mirroring discovery's
		// fast path (settings R7): being launched inside a repository is not an
		// explicit request for it, so it is not refused either. The fan-out set has
		// the exclusion applied already, so the account is listed around the hole.
	}
	return fanOutScope(deps)
}

// excluded reports whether id is on settings R7's exclude list, which main.go fills
// from the loaded config. The list is small, roughly ten entries at reference scale,
// so a scan beats carrying a set through Deps.
func excluded(deps Deps, id domain.RepoID) bool {
	return slices.Contains(deps.Exclude, id)
}

// excludedRepo is the refusal for a repository the operator named explicitly and also
// excluded (settings R7). It states which repository and which setting, and how to lift
// it, because the operator has contradicted themselves and the useful answer names both
// halves rather than only the one that lost.
func excludedRepo(id domain.RepoID) error {
	return fmt.Errorf(
		"repository %s/%s is in your config's exclude list; remove it from exclude to operate on it",
		id.Owner, id.Name)
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

// parseRepoArg parses a [HOST/]OWNER/REPO selector into a host-qualified identity
// (cli-surface R8, R9). The bare OWNER/REPO form defaults to github.com, and an
// explicit github.com/OWNER/REPO is accepted and treated identically (AC7).
//
// Both the shape parse and the validation live in domain, so -R, GH_REPO and settings
// R7's exclude list accept exactly the same spellings and reject exactly the same ones.
// Keeping a private copy of the switch here left the charset with one home and the
// shape with two, which is the same class of hole one home closes.
func parseRepoArg(arg string) (domain.RepoID, error) {
	return domain.ParseRepoRef(arg)
}

// unsupportedHost is the class-neutral rejection for the GH_HOST route (cli-surface
// R9, ADR-0009). It returns domain's UnsupportedHostError, the same value -R and
// GH_REPO get from domain.NewRepoID and the same one discovery raises, so the
// phrasing has one home: it names the host and claims nothing about its class, so it
// cannot be false for an Enterprise Cloud or GHES host.
func unsupportedHost(host string) error {
	return &domain.UnsupportedHostError{Host: host}
}
