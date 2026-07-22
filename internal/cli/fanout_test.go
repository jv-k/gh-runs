package cli_test

import (
	"strings"
	"testing"
)

// TestFanOutOutsideRepository pins AC15's first half: outside a repository, with
// no -R, list fans out across the discovered set, one listing request per
// repository, and emits a merged list (cli-surface R22). The default Current
// resolver returns an error (not inside a repository), which is the fan-out
// trigger, not a failure.
func TestFanOutOutsideRepository(t *testing.T) {
	h := newHarness(t, "list_fanout").withDiscovered(gh("acme", "alpha"), gh("acme", "beta"), gh("acme", "gamma"))

	if code := h.run("list"); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	// One request per discovered repository, three in all.
	if n := h.counting.count(); n != 3 {
		t.Errorf("wire requests = %d, want 3 (one per discovered repository)", n)
	}
	for _, repo := range []string{"acme/alpha", "acme/beta", "acme/gamma"} {
		if n := h.counting.countMatching("/repos/" + repo + "/actions/runs"); n != 1 {
			t.Errorf("%s listing requests = %d, want 1", repo, n)
		}
	}
	// The merged list carries every repository's Runs.
	out := h.stdout.String()
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(out, id) {
			t.Errorf("merged output missing Run %s\n%s", id, out)
		}
	}
	// R24: under fan-out the table carries a repository column.
	if !strings.Contains(out, "REPOSITORY") {
		t.Errorf("fan-out table missing REPOSITORY column\n%s", out)
	}
}

// TestInsideRepositoryListsThatRepoAlone pins AC15's second half: inside a
// repository, no -R means that repository, matching gh, so no fan-out happens and
// only that repository is requested (cli-surface R22, R2 parity).
func TestInsideRepositoryListsThatRepoAlone(t *testing.T) {
	h := newHarness(t, "list_fanout").withCurrent(gh("acme", "alpha")).
		withDiscovered(gh("acme", "alpha"), gh("acme", "beta"), gh("acme", "gamma"))

	if code := h.run("list"); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if n := h.counting.count(); n != 1 {
		t.Fatalf("wire requests = %d, want 1 (this repository alone)", n)
	}
	if n := h.counting.countMatching("/repos/acme/alpha/actions/runs"); n != 1 {
		t.Errorf("alpha requests = %d, want 1", n)
	}
	for _, repo := range []string{"acme/beta", "acme/gamma"} {
		if n := h.counting.countMatching("/repos/" + repo + "/"); n != 0 {
			t.Errorf("%s was requested (%d) but the tool was launched inside alpha", repo, n)
		}
	}
}

// TestAllReposForcesFanOutInsideRepository pins AC15's third half: --all-repos
// fans out even inside a repository (cli-surface R22).
func TestAllReposForcesFanOutInsideRepository(t *testing.T) {
	h := newHarness(t, "list_fanout").withCurrent(gh("acme", "alpha")).
		withDiscovered(gh("acme", "alpha"), gh("acme", "beta"), gh("acme", "gamma"))

	if code := h.run("list", "--all-repos"); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if n := h.counting.count(); n != 3 {
		t.Errorf("wire requests = %d, want 3 (--all-repos forces fan-out)", n)
	}
}

// TestMergedLimitBoundsTheTotal pins AC16: under fan-out -L bounds the merged
// total, newest-first by CreatedAt, never L per repository (cli-surface R23). With
// nine Runs across three repositories and -L 5, exactly the newest five come back
// in creation order, drawn from all three repositories, so a per-repository limit
// would have produced up to fifteen rows.
func TestMergedLimitBoundsTheTotal(t *testing.T) {
	h := newHarness(t, "list_merged_limit").withDiscovered(gh("acme", "alpha"), gh("acme", "beta"), gh("acme", "gamma"))

	code := h.run("list", "-L", "5", "--json", "databaseId", "-q", ".[].databaseId")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	got := strings.Fields(strings.TrimSpace(h.stdout.String()))
	want := []string{"11", "21", "31", "12", "22"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("merged newest-five = %v, want %v (cross-repository sort by CreatedAt, capped at 5)", got, want)
	}
}
