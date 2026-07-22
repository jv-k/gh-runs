package cli_test

import "testing"

// TestAuthFailureExitsFour pins AC14: a command requiring auth exits 4 when the
// credential does not resolve, not the generic 1. go-gh turns the 401 into an
// *api.HTTPError, and the command's classifier reaches it through the wrapping
// with errors.As (cli-surface R17).
func TestAuthFailureExitsFour(t *testing.T) {
	h := newHarness(t, "list_unauthorized")

	code := h.run("list", "-R", "octo/hello")

	if code != 4 {
		t.Fatalf("exit = %d, want 4 on an auth failure; stderr=%q", code, h.stderr.String())
	}
}
