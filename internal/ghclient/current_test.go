package ghclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/repository"
)

// TestResolveCurrentTranslatesTheKnownHostsTrap pins repo-discovery R14's failure
// contract. go-gh's repository.Current fails on the KnownHosts trap with a message
// that names the wrong problem; the resolver replaces it with the GH_TOKEN
// instruction that actually fixes it, and preserves the underlying error for a
// reader who wants it.
func TestResolveCurrentTranslatesTheKnownHostsTrap(t *testing.T) {
	trap := errors.New("unable to determine current repository, none of the git remotes configured for this repository point to a known GitHub host")

	_, err := resolveCurrent(repository.Repository{}, trap)
	if err == nil {
		t.Fatal("resolveCurrent returned no error for the KnownHosts trap")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("error %q does not carry the GH_TOKEN instruction R14 requires", err)
	}
	if !errors.Is(err, trap) {
		t.Error("resolveCurrent discarded the underlying error; it should wrap it")
	}
}

// TestResolveCurrentRejectsNonGitHubHost pins R18 at the resolver: a repository on
// any host but github.com is rejected explicitly, and the message names the host.
func TestResolveCurrentRejectsNonGitHubHost(t *testing.T) {
	_, err := resolveCurrent(repository.Repository{Host: "ghe.example.com", Owner: "corp", Name: "app"}, nil)
	if err == nil {
		t.Fatal("resolveCurrent admitted a non-github.com host")
	}
	if !strings.Contains(err.Error(), "ghe.example.com") || !strings.Contains(err.Error(), "github.com") {
		t.Errorf("rejection %q must name the host and state github.com support", err)
	}
}

// TestResolveCurrentHostQualifies pins the success path: a github.com repository
// yields a host-qualified RepoID (R14, R18).
func TestResolveCurrentHostQualifies(t *testing.T) {
	id, err := resolveCurrent(repository.Repository{Host: "github.com", Owner: "jv-k", Name: "gh-runs"}, nil)
	if err != nil {
		t.Fatalf("resolveCurrent: %v", err)
	}
	if id.String() != "github.com/jv-k/gh-runs" {
		t.Errorf("resolved id = %q, want github.com/jv-k/gh-runs", id.String())
	}
}
