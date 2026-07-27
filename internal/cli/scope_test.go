package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
)

// TestUnsupportedHostRejectedOffline pins AC7: an unsupported host is rejected by
// name, offline, whichever of the three routes carries it (-R, GH_REPO, GH_HOST).
// The offline harness fails the test if any request reaches the wire, so this
// proves the rejection precedes the network, never arriving as a 404 or an auth
// error (cli-surface R8, R9).
func TestUnsupportedHostRejectedOffline(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"repo flag", []string{"list", "-R", "ghe.corp/o/r"}, nil},
		{"GH_REPO", []string{"list"}, map[string]string{"GH_REPO": "ghe.corp/o/r"}},
		{"GH_HOST", []string{"list"}, map[string]string{"GH_HOST": "ghe.corp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOffline(t)
			for k, v := range tc.env {
				h.env[k] = v
			}
			code := h.run(tc.args...)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero for host ghe.corp")
			}
			if n := h.counting.count(); n != 0 {
				t.Errorf("wire requests = %d, want 0 (host rejected before the network)", n)
			}
			if !strings.Contains(h.stderr.String(), "ghe.corp") {
				t.Errorf("rejection did not name the host; stderr=%q", h.stderr.String())
			}
		})
	}
}

// TestRepoArgRejectsCraftedSegments pins the security hardening: a -R value whose
// owner or name is outside GitHub's identifier charset is rejected by name, offline,
// before any request, so an attacker-shaped segment can never be interpolated into
// the request URL path (security review, repo-discovery R18). The offline harness
// fails the test if any request reaches the wire, so this proves the crafted value
// never leaves the process. The three forms are a query smuggle, a path traversal,
// and an encoded slash, exactly the values the review verified against net/http.
func TestRepoArgRejectsCraftedSegments(t *testing.T) {
	for _, bad := range []string{
		"foo/bar?actor=x",  // a query smuggled into the name segment
		"foo/..",           // a parent-directory traversal
		"foo/bar%2F..%2Fx", // an encoded slash the "/" split does not catch
	} {
		t.Run(bad, func(t *testing.T) {
			h := newHarnessOffline(t)
			code := h.run("list", "-R", bad)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero for a crafted -R %q", bad)
			}
			if n := h.counting.count(); n != 0 {
				t.Errorf("wire requests = %d, want 0 (crafted -R rejected before the wire)", n)
			}
			if !strings.Contains(h.stderr.String(), "unsupported owner or name") {
				t.Errorf("rejection was not the charset error; stderr=%q", h.stderr.String())
			}
		})
	}
}

// TestRepoArgFromEnvRejectsCraftedSegments pins the same hardening on the GH_REPO
// route: GH_REPO flows through the same parseRepoArg, so a crafted value there is
// rejected offline too (cli-surface R8).
func TestRepoArgFromEnvRejectsCraftedSegments(t *testing.T) {
	h := newHarnessOffline(t)
	h.env["GH_REPO"] = "foo/bar?actor=x"
	if code := h.run("list"); code == 0 {
		t.Fatalf("exit = 0, want non-zero for a crafted GH_REPO")
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("wire requests = %d, want 0 (crafted GH_REPO rejected before the wire)", n)
	}
}

// TestExplicitGitHubHostEqualsBareForm pins AC7's second half: -R
// github.com/cli/cli behaves identically to -R cli/cli. Both resolve to the same
// identity, issue the same request, and print the same output.
func TestExplicitGitHubHostEqualsBareForm(t *testing.T) {
	bare := newHarness(t, "list_clicli")
	if code := bare.run("list", "-R", "cli/cli"); code != 0 {
		t.Fatalf("bare form exit = %d, want 0; stderr=%q", code, bare.stderr.String())
	}

	qualified := newHarness(t, "list_clicli")
	if code := qualified.run("list", "-R", "github.com/cli/cli"); code != 0 {
		t.Fatalf("qualified form exit = %d, want 0; stderr=%q", code, qualified.stderr.String())
	}

	if bare.stdout.String() != qualified.stdout.String() {
		t.Errorf("outputs differ:\nbare:\n%s\nqualified:\n%s", bare.stdout.String(), qualified.stdout.String())
	}
	if !strings.Contains(bare.stdout.String(), "301") {
		t.Errorf("expected Run 301 in output\n%s", bare.stdout.String())
	}
}

// TestExcludedWorkingDirectoryStillScopesToThatRepository pins cli-surface R22's MUST
// against the exclude list: "Inside a repository, no -R MUST mean that repository,
// matching gh, so R2's parity holds." The exclude list governs discovery, the Feed and
// polling (settings R7), and the working directory is none of those.
//
// An earlier revision let an excluded working directory fall through to the fan-out
// limb, which broke R22 and turned a one-request invocation into a full one. The
// deletion consequence is worse and TestExcludedWorkingDirectoryNeverEscalatesADelete
// pins it: the same fall-through silently rescoped `gh runs delete --all --yes` from
// one repository to the whole account.
func TestExcludedWorkingDirectoryStillScopesToThatRepository(t *testing.T) {
	h := newHarness(t, "list_fanout").
		withCurrent(gh("acme", "alpha")).
		withExclude(gh("acme", "alpha")).
		withDiscovered(gh("acme", "alpha"), gh("acme", "beta"), gh("acme", "gamma"))

	if code := h.run("list"); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if n := h.counting.count(); n != 1 {
		t.Fatalf("wire requests = %d, want 1 (this repository alone, R22)", n)
	}
	for _, repo := range []string{"acme/beta", "acme/gamma"} {
		if n := h.counting.countMatching("/repos/" + repo + "/"); n != 0 {
			t.Errorf("%s was requested (%d) but the tool was launched inside alpha", repo, n)
		}
	}
}

// TestExcludedWorkingDirectoryNeverEscalatesADelete is the blocking case, asserted at
// the wire. delete reads resolveScope's repositories and never inspects whether the
// scope was a fan-out, so an excluded working directory falling through to the fan-out
// limb rescoped a one-repository delete to every discovered repository. Nothing
// downstream caught it: ops.FrictionTypedCount accepts NonInteractiveYes, so the --yes
// the operator passed for one repository satisfied the friction for the account.
//
// --all-repos exists precisely so that account-wide deletion is asked for by name
// (cli-surface R22, ADR-0022), and a config line must never supply it implicitly. The
// crawl is where the escalation first becomes visible on the wire, so that is where
// this asserts: the repositories the operator did not name receive nothing.
func TestExcludedWorkingDirectoryNeverEscalatesADelete(t *testing.T) {
	h := newHarness(t, "delete_all").
		withCurrent(gh("o", "r")).
		withExclude(gh("o", "r")).
		withDiscovered(gh("o", "r"), gh("acme", "beta"), gh("acme", "gamma"))

	h.runDriven("delete", "--all", "--yes")

	if n := h.counting.countMatching("/repos/o/r/actions/runs"); n == 0 {
		t.Error("the working-directory repository was not crawled at all")
	}
	for _, repo := range []string{"acme/beta", "acme/gamma"} {
		if n := h.counting.countMatching("/repos/" + repo + "/"); n != 0 {
			t.Errorf("delete reached %s (%d requests), a repository the operator never named", repo, n)
		}
	}
}

// TestExcludedRepositoryStillReachableByName pins settings R4's precedence against R7's
// exclude list: "flags, then environment, then config file, then defaults (highest
// first)." A config list may not refuse an explicit flag.
//
// An earlier revision refused -R and GH_REPO for an excluded repository, which inverted
// that precedence and, with the working-directory limb, left no path in the tool that
// could reach an excluded repository at all. That is the wrong end state twice over:
// the reason to exclude a repository is polling cost, so the excluded set is the
// noisiest repositories, which are exactly the ones a Purge targets. R7 enumerates the
// three surfaces exclusion closes, discovery, the Feed and all polling, and a
// present-tense instruction typed by name is none of them.
func TestExcludedRepositoryStillReachableByName(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"repo flag", []string{"list", "-R", "cli/cli"}, nil},
		{"GH_REPO", []string{"list"}, map[string]string{"GH_REPO": "cli/cli"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "list_clicli").withExclude(gh("cli", "cli"))
			for k, v := range tc.env {
				h.env[k] = v
			}
			if code := h.run(tc.args...); code != 0 {
				t.Fatalf("exit = %d, want 0: an explicit flag outranks the config file (R4); stderr=%q",
					code, h.stderr.String())
			}
			if !strings.Contains(h.stdout.String(), "301") {
				t.Errorf("expected Run 301 in output\n%s", h.stdout.String())
			}
		})
	}
}

// TestExcludedRepositoryDeleteNamesTheRealCause is the diagnostic this whole line of
// review started from. Deleting in an excluded repository fails, because exclusion kept
// it out of discovery and Plan refuses a repository with no recorded capability (purge
// R10). The failure is correct; the message was not, naming the eligibility snapshot
// and leaving the operator to work out that a config line put it there.
//
// The fix is a diagnostic and not a refusal, which is the distinction settings R4 turns
// on: the explicit request proceeds as far as it can, and only the message changes.
func TestExcludedRepositoryDeleteNamesTheRealCause(t *testing.T) {
	h := newHarness(t, "delete_all").
		withExclude(gh("o", "r")).
		withEmptySnapshot()

	if code := h.runDriven("delete", "-R", "o/r", "--all", "--yes"); code == 0 {
		t.Fatal("exit = 0, want non-zero: an unrecorded capability must fail closed")
	}
	msg := h.stderr.String()
	if !strings.Contains(msg, "exclude") || !strings.Contains(msg, "o/r") {
		t.Errorf("the failure does not name the exclude list as the cause; stderr=%q", msg)
	}
	if h.logExists() {
		t.Error("a refused plan wrote a deletion log")
	}
}

// TestUnrecognisedRemoteCarriesTheGHTokenInstruction is repo-discovery R14 on the CLI
// surface. On a machine where gh was never installed, go-gh's resolver fails even though
// git works and the remote is plainly github.com, and setting GH_TOKEN fixes it in one
// step. Scope resolution treats every resolver failure as cli-surface R22's fan-out
// trigger, which is right, but silence is not: the operator sees the command scope itself
// to the whole account and has nothing telling them why it did not scope to the repository
// they are standing in.
//
// It stays a note rather than a failure. Fanning out is a legitimate answer, so the command
// runs; the line only says why the scope is what it is.
func TestUnrecognisedRemoteCarriesTheGHTokenInstruction(t *testing.T) {
	h := newHarnessOffline(t).withCurrentErr(
		fmt.Errorf("%w: probing remotes", ghclient.ErrRemoteHostUnrecognised))

	_ = h.run("list")

	if got := h.stderr.String(); !strings.Contains(got, "GH_TOKEN") {
		t.Errorf("stderr = %q, want R14's GH_TOKEN instruction: the operator has no other way to learn why the scope is not this repository", got)
	}
}

// TestBeingOutsideARepositoryStaysSilent is the other half, and the reason the case above
// needs a condition rather than a blanket report. Running outside a git repository is the
// ordinary way to use the fan-out, and a GH_TOKEN instruction there would name a problem
// the operator does not have.
func TestBeingOutsideARepositoryStaysSilent(t *testing.T) {
	h := newHarnessOffline(t) // its default resolver reports no current repository

	_ = h.run("list")

	if got := h.stderr.String(); strings.Contains(got, "GH_TOKEN") {
		t.Errorf("stderr = %q, want no GH_TOKEN instruction outside a repository", got)
	}
}
