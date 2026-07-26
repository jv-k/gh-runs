package domain

import (
	"strconv"
	"strings"
)

// HostGitHub is the one host gh-runs 2.0.0 serves (ADR-0009). Repository identity is
// host-qualified everywhere, and every construction site rejects any other host by
// name rather than silently attributing it to github.com. discovery and cli read it
// from here, so the host string has one home.
const HostGitHub = "github.com"

// NewRepoID host-qualifies and validates a repository at the one construction site
// every identity passes through (ADR-0009, repo-discovery R18, cli-surface R8). It
// rejects any host but github.com, and rejects an owner or name outside GitHub's
// identifier charset, so a value that could be interpolated into a request URL path
// or a filesystem key is refused before the identity is built rather than after.
//
// It is the single validation home the security review asked for: discovery's
// enumeration, its persisted-record reload, its fast path, and the CLI's -R and
// GH_REPO all route here, so a path-unsafe segment has no second door. The host must
// be stated; an empty host is not defaulted here, because the callers that accept a
// bare OWNER/REPO know their own default and pass github.com explicitly (discovery
// defaults a host-less persisted record before it calls in).
func NewRepoID(host, owner, name string) (RepoID, error) {
	host = strings.TrimSpace(host)
	if !strings.EqualFold(host, HostGitHub) {
		return RepoID{}, &UnsupportedHostError{Host: host}
	}
	if !validOwner(owner) || !validRepoName(name) {
		return RepoID{}, &InvalidRepoError{Owner: owner, Name: name}
	}
	return RepoID{Host: HostGitHub, Owner: owner, Name: name}, nil
}

// ParseRepoRef parses a [HOST/]OWNER/REPO selector into a host-qualified identity. The
// bare OWNER/REPO form defaults to github.com, and an explicit github.com/OWNER/REPO is
// accepted and means the same repository (ADR-0009, cli-surface R8, AC7). A host segment
// is told from an owner segment by the part count, matching gh's own parse.
//
// It is the shape parse, and it lives beside NewRepoID because those two together are
// what every textual repository selector needs: the CLI's -R and GH_REPO, and settings
// R7's exclude list. Two copies of this switch could drift into accepting different
// spellings on the flag and in the config file, which is the same class of hole the
// single validation home closes for the charset.
//
// Surrounding whitespace is trimmed, so `-R " o/r"` and a config entry indented past
// its dash both resolve. A YAML list is the reason: an entry's leading space is the
// file's formatting rather than the operator's intent, and a flag that rejected what
// the config file accepted would be the drift this function exists to prevent. Nothing
// inside the value is trimmed, so a segment carrying a space still fails the charset
// check in NewRepoID.
func ParseRepoRef(ref string) (RepoID, error) {
	parts := strings.Split(strings.TrimSpace(ref), "/")
	var host, owner, name string
	switch len(parts) {
	case 2:
		host, owner, name = HostGitHub, parts[0], parts[1]
	case 3:
		host, owner, name = parts[0], parts[1], parts[2]
	default:
		return RepoID{}, &RepoRefFormatError{Ref: ref}
	}
	if owner == "" || name == "" {
		return RepoID{}, &RepoRefFormatError{Ref: ref}
	}
	return NewRepoID(host, owner, name)
}

// RepoRefFormatError reports a selector that is not [HOST/]OWNER/REPO shaped at all, as
// distinct from one that is well shaped and names an unsupported host or an out-of-charset
// segment. It names what it rejected and claims nothing more, the same rule the other two
// identity errors follow.
type RepoRefFormatError struct {
	Ref string
}

func (e *RepoRefFormatError) Error() string {
	return "invalid repository " + strconv.Quote(e.Ref) + ": expected the [HOST/]OWNER/REPO format"
}

// validOwner reports whether owner is a syntactically valid GitHub account name:
// non-empty and composed of ASCII letters, digits and hyphens. GitHub's own rule is
// stricter (no leading or trailing hyphen, at most 39 characters), but the charset is
// what closes the finding: an owner reaches request URL paths and any filesystem key,
// and a value outside this set is neither a real GitHub account nor safe to
// interpolate. NewRepoID is the one place every identity is built, so validating here
// has no hole (repo-discovery R18).
func validOwner(owner string) bool {
	if owner == "" {
		return false
	}
	for _, r := range owner {
		if !isAlnum(r) && r != '-' {
			return false
		}
	}
	return true
}

// validRepoName reports whether name is a syntactically valid GitHub repository name:
// non-empty, neither "." nor "..", and composed of ASCII letters, digits, hyphen,
// underscore and dot. Rejecting the dot-dot component and any separator keeps the name
// safe as a URL path segment and as a filesystem key, the same reason the store hashes
// its own keys.
func validRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !isAlnum(r) && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// isAlnum reports whether r is an ASCII letter or digit.
func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// UnsupportedHostError reports a repository resolving to a host 2.0.0 does not serve
// (ADR-0009, repo-discovery R18). It names the host and claims nothing about what the
// host is, the neutral rejection ADR-0009 settled on: a message that makes no class
// claim cannot make a false one for a tenant.ghe.com user.
type UnsupportedHostError struct {
	Host string
}

func (e *UnsupportedHostError) Error() string {
	return "repository host " + e.Host + " is not supported; gh-runs 2.0.0 serves github.com only"
}

// InvalidRepoError reports a repository whose owner or name is outside GitHub's
// identifier charset, so it never reaches a request URL path or a filesystem key
// (security hardening). Like UnsupportedHostError it names what it rejected and claims
// nothing more.
type InvalidRepoError struct {
	Owner string
	Name  string
}

func (e *InvalidRepoError) Error() string {
	return "repository " + e.Owner + "/" + e.Name +
		" has an unsupported owner or name; gh-runs accepts GitHub owner and repository names only"
}
