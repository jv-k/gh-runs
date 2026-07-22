package cli_test

import (
	"strings"
	"testing"
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
