package cli_test

import (
	"strings"
	"testing"
)

// The four lifecycle operations, non-interactively. They mirror gh's own surface
// (`gh run cancel [--force]`, `gh run rerun [--failed] [--debug]`) and add this tool's
// filter-driven bulk form over the same ops.Plan/Confirm/Execute chain the Purge runs on
// (run-lifecycle Related, cli-surface R10, R11, ADR-0008).
//
// No test here issues a live cancel, force-cancel or re-run: every one replays a
// cassette. A live cancel kills work somebody is waiting on and a live re-run spends
// their Actions minutes, and neither can be undone (run-lifecycle R28).

// TestCancelRefusesWithoutFilterOrAll is cli-surface R26's guard, one operation over: the
// zero filter matches every Run in scope, so cancelling everything must be asked for by
// name. The offline transport fails any wire request, so a leaked crawl fails loudly.
func TestCancelRefusesWithoutFilterOrAll(t *testing.T) {
	h := newHarnessOffline(t)
	code := h.run("cancel", "--yes")
	if code == 0 {
		t.Errorf("cancel with no Run ID, no filter and no --all exited 0; it must refuse (R26)")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestCancelRefusesWithoutYes is R11 at the cancel command, and run-lifecycle R18's
// single-Run y/N in its non-interactive form: --yes is the explicit act on a surface with
// no modal, so a cancel without it requests nothing.
func TestCancelRefusesWithoutYes(t *testing.T) {
	for _, args := range [][]string{
		{"cancel", "--all"},
		{"cancel", "11"},
	} {
		h := newHarnessOffline(t).withCurrent(gh("o", "r"))
		if code := h.run(args...); code == 0 {
			t.Errorf("%v exited 0 without --yes; the confirmation is always required (R11, R18)", args)
		}
		if n := len(h.counting.writes()); n != 0 {
			t.Errorf("%v issued %d writes without --yes, want zero", args, n)
		}
	}
}

// TestCancelAllYesRequestsCancellation is the headless path end to end: it crawls the
// affected set through the same code the Purge uses, cancels each Run, and exits 0. The
// wording is run-lifecycle R4's: a 202 means the request was accepted, not that the Run
// was cancelled, so the summary reports a requested cancellation and claims no outcome.
func TestCancelAllYesRequestsCancellation(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--yes")
	if code != 0 {
		t.Fatalf("cancel --all --yes exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if got := h.counting.postsMatching("/cancel"); got != 2 {
		t.Errorf("issued %d cancel requests, want 2 (one per crawled Run)", got)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "Cancellation requested for 2") {
		t.Errorf("the summary does not report the cancellation as requested rather than done (R4):\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "cancelled") {
		t.Errorf("the summary claims a cancelled outcome a 202 does not carry (R4, AC5):\n%s", out)
	}
}

// TestLifecycleWritesNoDeletionLog is run-lifecycle R24 and purge R29's scope: none of the
// four operations is a deletion, so none of them records a line. Each leaves an object
// standing on GitHub carrying its own record, and that record is better than ours.
func TestLifecycleWritesNoDeletionLog(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	if code := h.runDriven("cancel", "--all", "--yes"); code != 0 {
		t.Fatalf("cancel exited %d", code)
	}
	if h.logExists() {
		t.Errorf("a cancel wrote the deletion log; R29 MUST NOT record a non-deletion (run-lifecycle R24)")
	}
}

// TestCancelForceUsesTheDistinctEndpoint is run-lifecycle R6: force-cancel is a distinct
// operation against a distinct endpoint, offered as an escalation and never silently
// substituted. --force is gh's own spelling on `gh run cancel --force`. The cassette
// carries no plain-cancel interaction, so a command that sent the softer verb would find
// nothing to play.
func TestCancelForceUsesTheDistinctEndpoint(t *testing.T) {
	h := newHarness(t, "cancel_force").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--yes", "--force")
	if code != 0 {
		t.Fatalf("cancel --force exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if got := h.counting.postsMatching("/force-cancel"); got != 2 {
		t.Errorf("issued %d force-cancel requests, want 2 (R6)", got)
	}
	if got := h.counting.postsMatching("/runs/11/cancel"); got != 0 {
		t.Errorf("--force still issued %d plain cancels; the two must never be substituted (R6)", got)
	}
}

// TestCancelConflictIsASkipThatNamesForceCancel is run-lifecycle R5, R20 and AC6 on the
// headless surface: a 409 is a fact about the Run's Status rather than an error, so it is
// recorded as a skip that does not fail the command, and the reason it is recorded with
// names force-cancel as the escalation. The clean cancel beside it still lands.
func TestCancelConflictIsASkipThatNamesForceCancel(t *testing.T) {
	h := newHarness(t, "cancel_conflict").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--yes")
	if code != 0 {
		t.Fatalf("a 409 failed the command with exit %d; it is a skip, not a failure (R5, R20, AC12). stderr: %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "skipped 1") {
		t.Errorf("the 409 was not counted as a skip (R20, AC12):\n%s", out)
	}
	if !strings.Contains(out, "not cancelable") {
		t.Errorf("the summary does not state that the Run is not cancelable (R5, AC6):\n%s", out)
	}
	if !strings.Contains(out, "force-cancel") {
		t.Errorf("the summary does not offer force-cancel on the 409 (R5, AC6):\n%s", out)
	}
}

// TestCancelDryRunIssuesNoWrite is cli-surface R10 and R20 applied to a cancel: --dry-run
// resolves the affected set through the same crawl the real operation uses, reports one
// row per Run, and requests nothing.
func TestCancelDryRunIssuesNoWrite(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--dry-run")
	if code != 0 {
		t.Fatalf("cancel --dry-run exited %d, want 0 (R10)", code)
	}
	if n := len(h.counting.writes()); n != 0 {
		t.Errorf("a dry run issued %d writes; it must issue none (R10)", n)
	}
	out := h.stdout.String()
	for _, want := range []string{"github.com/o/r\t11", "github.com/o/r\t12"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing a row %q; each row names its repository and Run ID (R10, AC9):\n%s", want, out)
		}
	}
}

// TestRerunByRunIDNeedsNoYes is run-lifecycle R18 and AC11's asymmetry, non-interactively:
// a single-Run re-run takes no confirmation, because correcting a failed Run is the most
// common action and neither destroys a Run. `gh run rerun <id>` is gh's own shape, so
// `gh runs rerun <id>` is muscle memory (ADR-0008).
func TestRerunByRunIDNeedsNoYes(t *testing.T) {
	h := newHarness(t, "rerun_one").withCurrent(gh("o", "r"))
	code := h.runDriven("rerun", "31")
	if code != 0 {
		t.Fatalf("rerun 31 exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if got := h.counting.postsMatching("/runs/31/rerun"); got != 1 {
		t.Errorf("issued %d re-run requests, want 1", got)
	}
	if out := h.stdout.String(); !strings.Contains(out, "Re-run requested for 1") {
		t.Errorf("the summary does not report the re-run (R8):\n%s", out)
	}
}

// TestRerunDebugIsOptIn is run-lifecycle R14 and AC14: both re-run endpoints accept
// enable_debug_logging, the option defaults to off, and it rides in the request body,
// which no URL and no cassette matcher carries.
func TestRerunDebugIsOptIn(t *testing.T) {
	plain := newHarness(t, "rerun_one").withCurrent(gh("o", "r"))
	if code := plain.runDriven("rerun", "31"); code != 0 {
		t.Fatalf("rerun exited %d", code)
	}
	for _, w := range plain.counting.writes() {
		if strings.Contains(w.body, "enable_debug_logging") {
			t.Errorf("the default re-run sent %q; debug logging defaults to off (R14, AC14)", w.body)
		}
	}

	debug := newHarness(t, "rerun_one").withCurrent(gh("o", "r"))
	if code := debug.runDriven("rerun", "31", "--debug"); code != 0 {
		t.Fatalf("rerun --debug exited %d", code)
	}
	sent := debug.counting.writes()
	if len(sent) != 1 || !strings.Contains(sent[0].body, `"enable_debug_logging":true`) {
		t.Errorf("--debug did not send enable_debug_logging; the issued bodies were %v (R14, AC14)", sent)
	}
}

// TestRerunFailedIsADistinctEndpoint is run-lifecycle R13: re-run failed Jobs is a
// distinct operation from re-run, against its own endpoint, and gh spells it --failed.
func TestRerunFailedIsADistinctEndpoint(t *testing.T) {
	h := newHarness(t, "rerun_one").withCurrent(gh("o", "r"))
	if code := h.runDriven("rerun", "31", "--failed"); code != 0 {
		t.Fatalf("rerun --failed exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if got := h.counting.postsMatching("/rerun-failed-jobs"); got != 1 {
		t.Errorf("issued %d re-run-failed requests, want 1 (R13)", got)
	}
	if got := h.counting.postsMatching("/runs/31/rerun"); got != 0 {
		t.Errorf("--failed also issued %d plain re-runs; they are distinct operations (R13)", got)
	}
}

// TestBulkRerunStillNeedsYes is run-lifecycle R17 and AC10: only the single-Run case is
// exempt from confirmation. A filter-driven re-run is a bulk operation whatever it turns
// out to match, so it takes the flag, and it takes it before the crawl.
func TestBulkRerunStillNeedsYes(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))
	if code := h.run("rerun", "--all"); code == 0 {
		t.Errorf("a bulk re-run exited 0 with no --yes; AC10 requires the confirmation")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests, want zero", h.counting.count())
	}
}

// TestRerun404IsAFailureAndCancel404IsASkip is run-lifecycle R22 and AC13: the same status
// reads by the requested end state. A re-run of a deleted Run cannot gain an Attempt, so
// it fails and the command exits 1. A cancel of the same Run finds it not running, which
// is what was asked for, so it is a skip and the command exits 0.
func TestRerun404IsAFailureAndCancel404IsASkip(t *testing.T) {
	rerun := newHarness(t, "rerun_missing").withCurrent(gh("o", "r"))
	if code := rerun.runDriven("rerun", "--all", "--yes"); code != 1 {
		t.Errorf("a re-run whose Run 404s exited %d, want 1 (R22, AC13). stderr: %s", code, rerun.stderr.String())
	}

	cancel := newHarness(t, "rerun_missing").withCurrent(gh("o", "r"))
	if code := cancel.runDriven("cancel", "--all", "--yes"); code != 0 {
		t.Errorf("a cancel whose Run 404s exited %d, want 0 (R22, AC13). stderr: %s", code, cancel.stderr.String())
	}
	if out := cancel.stdout.String(); !strings.Contains(out, "skipped 1") {
		t.Errorf("the cancel's 404 was not recorded as a skip (R22, AC13):\n%s", out)
	}
}

// TestRunIDNeedsARepository pins that a bare Run ID resolves against one repository, as
// gh's does. Under fan-out an id belongs to no repository in particular, so the command
// says which flag names one rather than guessing at the first discovered repository.
func TestRunIDNeedsARepository(t *testing.T) {
	h := newHarnessOffline(t).withDiscovered(gh("o", "r"), gh("o", "s"))
	code := h.run("cancel", "11", "--yes")
	if code == 0 {
		t.Errorf("a Run ID outside a repository exited 0; it must name the repository first")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests, want zero", h.counting.count())
	}
	if !strings.Contains(h.stderr.String(), "-R") {
		t.Errorf("the refusal does not name the flag that resolves it:\n%s", h.stderr.String())
	}
}

// TestRunIDAndFilterAreExclusive pins that the two ways of naming a set are not mixed. A
// Run ID is an exact set and a filter is a query, and silently letting one win would make
// the resolved set unpredictable on a command that mutates Runs.
func TestRunIDAndFilterAreExclusive(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))
	if code := h.run("cancel", "11", "-s", "failure", "--yes"); code == 0 {
		t.Errorf("a Run ID beside a filter exited 0; the two must not be mixed")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests, want zero", h.counting.count())
	}
}

// TestLifecycleValidatesFiltersBeforeTheWire is cli-surface R6 and AC5 on the new
// commands: an unrecognised filter value is rejected by name, client-side, before any
// request. The API answers a bad value with a silent zero and an unknown parameter with
// everything, so a typo caught here is a typo caught at all.
func TestLifecycleValidatesFiltersBeforeTheWire(t *testing.T) {
	for _, sub := range []string{"cancel", "rerun"} {
		h := newHarnessOffline(t).withCurrent(gh("o", "r"))
		if code := h.run(sub, "-s", "faliure", "--yes"); code == 0 {
			t.Errorf("%s -s faliure exited 0; an unrecognised value is rejected by name (R6, AC5)", sub)
		}
		if h.counting.count() != 0 {
			t.Errorf("%s -s faliure issued %d requests; it must issue zero (AC5)", sub, h.counting.count())
		}
	}
}
