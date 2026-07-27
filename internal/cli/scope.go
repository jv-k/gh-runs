package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
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
//
// Settings R7's exclude list deliberately reaches none of these branches. R7 closes
// three surfaces by name, discovery, the Feed and all polling, and every branch here is
// an operator naming a repository in the present tense: -R and GH_REPO spell it, and
// the working directory is what gh's precedence and R22's MUST both fix as "that
// repository". Settings R4 puts flags and environment above the config file, so a
// config list refusing a flag would invert the precedence that document fixes.
//
// Two earlier attempts here were wrong in opposite directions, and tests now pin both.
// Refusing an excluded -R inverted R4 and, together with the working-directory branch,
// left no path in the tool that could reach an excluded repository at all, which is the
// wrong end state: the reason to exclude is polling cost, so the excluded set is the
// noisiest repositories, which are precisely a Purge's targets. Falling an excluded
// working directory through to the fan-out broke R22's MUST and, because delete uses
// this scope's repositories without inspecting whether it fanned out, silently rescoped
// `gh runs delete --all --yes` from one repository to the whole account.
//
// The exclusion is still visible here, in the fan-out set alone: Discovered is
// discovery's poll set, which the exclusion has already been applied to. That is R7's
// third surface and the only one this function owns.
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
		id, err := deps.Current()
		if err == nil {
			return scope{repos: []domain.RepoID{id}}, nil
		}
		// A resolver error means the tool was not launched inside a repository (or
		// its remote is not recognised). That is R22's fan-out trigger, not a
		// failure: no repository means all repositories.
		//
		// One of those two is worth a word, though (repo-discovery R14). A remote git
		// resolves but go-gh does not recognise is one step from working, and setting
		// GH_TOKEN is that step. Fanning out silently leaves the operator watching a
		// command scope itself to the whole account with nothing saying why it did not
		// scope to the repository they are standing in. Being outside a repository is
		// the ordinary way to reach the fan-out and says nothing, because an
		// instruction there would name a problem they do not have.
		if errors.Is(err, ghclient.ErrRemoteHostUnrecognised) {
			_, _ = fmt.Fprintln(deps.Stderr, "gh-runs:", err)
		}
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
