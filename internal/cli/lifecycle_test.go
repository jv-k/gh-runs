package cli_test

import (
	"strings"
	"testing"
)

// The non-interactive form of run-lifecycle's four operations (that feature's Related
// section, ADR-0008's gh run cancel / gh run cancel --force / gh run rerun compatibility).
// Two grammars reach the same Plan/Confirm/Execute chain the TUI and gh runs delete use:
// positional Run IDs, which name their targets and issue no crawl, and the filter axes,
// which resolve a set the way delete does.

// TestCancelByRunIDIssuesOneRequestAndNoCrawl pins the positional grammar. Naming the Run
// is the scope, so nothing is listed: the command issues exactly the write, which is what
// makes `gh runs cancel 8` answer as fast as gh's own.
func TestCancelByRunIDIssuesOneRequestAndNoCrawl(t *testing.T) {
	h := newHarness(t, "lifecycle_byid").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "8", "--yes")
	if code != 0 {
		t.Fatalf("cancel 8 --yes exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if got := h.counting.count(); got != 1 {
		t.Errorf("issued %d requests, want exactly 1: a named Run needs no crawl. urls: %v", got, h.counting.urls)
	}
	if !h.postedTo("/actions/runs/8/cancel") {
		t.Errorf("no cancel POST for Run 8. urls: %v", h.counting.urls)
	}
}

// TestForceCancelByRunIDUsesItsOwnEndpoint pins R6 at the CLI: --force is a distinct
// operation against a distinct endpoint and is never silently substituted for cancel.
// ADR-0008 names gh run cancel --force as the shape to mirror.
func TestForceCancelByRunIDUsesItsOwnEndpoint(t *testing.T) {
	h := newHarness(t, "lifecycle_byid").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "7", "--force", "--yes")
	if code != 0 {
		t.Fatalf("cancel 7 --force --yes exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if !h.postedTo("/actions/runs/7/force-cancel") {
		t.Errorf("--force did not POST the force-cancel endpoint (R6). urls: %v", h.counting.urls)
	}
	if h.postedTo("/actions/runs/7/cancel") {
		t.Errorf("--force POSTed the plain cancel endpoint; it is a distinct operation (R6)")
	}
}

// TestRerunByRunIDNeedsNoYes pins R18's asymmetry at the CLI, and it is the one place the
// two grammars visibly differ from delete. A single re-run prices at FrictionNone, so it
// takes no confirmation and --yes is not required: correcting a failed Run is the common
// action, and a re-run destroys nothing. cancel is not exempt, and its own test pins that.
func TestRerunByRunIDNeedsNoYes(t *testing.T) {
	h := newHarness(t, "lifecycle_byid").withCurrent(gh("o", "r"))
	code := h.runDriven("rerun", "9")
	if code != 0 {
		t.Fatalf("rerun 9 exited %d, want 0; a single re-run takes no confirmation (R18). stderr: %s", code, h.stderr.String())
	}
	if !h.postedTo("/actions/runs/9/rerun") {
		t.Errorf("no rerun POST for Run 9. urls: %v", h.counting.urls)
	}
}

// TestRerunFailedAndDebugRideTheirFlags pins R13 and R14/AC14: --failed selects the
// distinct rerun-failed-jobs endpoint, and --debug is the opt-in that puts
// enable_debug_logging on the request. gh run rerun exposes both spellings.
func TestRerunFailedAndDebugRideTheirFlags(t *testing.T) {
	h := newHarness(t, "lifecycle_byid").withCurrent(gh("o", "r"))
	code := h.runDriven("rerun", "10", "--failed", "--debug")
	if code != 0 {
		t.Fatalf("rerun 10 --failed --debug exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if !h.postedTo("/actions/runs/10/rerun-failed-jobs") {
		t.Errorf("--failed did not POST the rerun-failed-jobs endpoint (R13). urls: %v", h.counting.urls)
	}
	if h.postedTo("/actions/runs/10/rerun") {
		t.Errorf("--failed POSTed plain rerun; the two are distinct operations (R13)")
	}
}

// TestCancelRefusesWithoutYes mirrors cli-surface R11 onto cancel. R18 requires a y/N even
// for a single Run, because cancelled work cannot be recovered, and --yes is that surface's
// confirmation. The offline transport fails any wire request, so a leaked write fails loudly.
func TestCancelRefusesWithoutYes(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))
	code := h.run("cancel", "8")
	if code == 0 {
		t.Errorf("cancel without --yes exited 0; R18 requires a confirmation even for one Run")
	}
	if h.counting.count() != 0 {
		t.Errorf("cancel refused but issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestLifecycleRefusesWithoutFilterOrAll applies cli-surface R26's rule to the filter
// grammar: the zero filter matches every Run, so acting on all of them must be asked for by
// name. Without it, a bare `gh runs cancel` would cancel the account's every running Run.
func TestLifecycleRefusesWithoutFilterOrAll(t *testing.T) {
	for _, cmd := range []string{"cancel", "rerun"} {
		h := newHarnessOffline(t).withCurrent(gh("o", "r"))
		code := h.run(cmd, "--yes")
		if code == 0 {
			t.Errorf("%s with no filter, no --all and no Run ID exited 0; it must refuse (R26)", cmd)
		}
		if h.counting.count() != 0 {
			t.Errorf("%s refused but issued %d requests; it must issue zero", cmd, h.counting.count())
		}
	}
}

// TestCancelAllYesCancelsAndSkipsThe409 pins the filter grammar end to end, and with it
// R5, R20 and AC12: the crawl resolves the set, each Run is cancelled, and the Run that
// answers 409 is recorded as a skip rather than a failure, so the pass exits 0. The skip
// carries the API's own reason and names force-cancel as the escalation (R5, AC6).
func TestCancelAllYesCancelsAndSkipsThe409(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--yes")
	if code != 0 {
		t.Fatalf("cancel --all --yes exited %d, want 0: a 409 is a skip, not a failure (R20, AC12). stderr: %s",
			code, h.stderr.String())
	}
	if !h.postedTo("/actions/runs/1/cancel") || !h.postedTo("/actions/runs/2/cancel") {
		t.Errorf("the crawl's Runs were not both attempted. urls: %v", h.counting.urls)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "skipped") {
		t.Errorf("the summary does not report the 409 as a skip (R20, AC12):\n%s", out)
	}
	if !strings.Contains(out, "force-cancel") {
		t.Errorf("the 409's reason does not offer force-cancel as the escalation (R5, AC6):\n%s", out)
	}
}

// TestLifecycleWritesNoDeletionLog pins R24, the rule that separates these four operations
// from a Purge: none of them is a deletion, so purge R29's log must not record them. Each
// leaves an object standing on GitHub that carries its own record.
func TestLifecycleWritesNoDeletionLog(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	if code := h.runDriven("cancel", "--all", "--yes"); code != 0 {
		t.Fatalf("cancel --all --yes exited %d, want 0", code)
	}
	if h.logExists() {
		t.Errorf("a bulk cancel wrote the deletion log; run-lifecycle R24 forbids it")
	}
}

// TestLifecycleDryRunIssuesNoWrite pins cli-surface R10's shape on both verbs: --dry-run
// resolves the affected set by the same code path as the real operation and acts on
// nothing. It is the only way to see what a filter selects before spending Actions minutes
// on it, which a bulk re-run does.
func TestLifecycleDryRunIssuesNoWrite(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	code := h.runDriven("cancel", "--all", "--dry-run")
	if code != 0 {
		t.Fatalf("cancel --all --dry-run exited %d, want 0 (R10)", code)
	}
	if h.postedTo("/cancel") {
		t.Errorf("a dry run issued a cancel; it must issue none (R10). urls: %v", h.counting.urls)
	}
	out := h.stdout.String()
	for _, want := range []string{"github.com/o/r\t1", "github.com/o/r\t2"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing row %q; each row names its repository and Run ID (R10):\n%s", want, out)
		}
	}
}

// TestLifecycleDryRunTrailerClaimsOnlyTheWrite pins that the trailer says what it observed
// and no more. A dry run resolves the set through the same crawl the real operation uses,
// so GETs were issued and a trailer reading "no request issued" is false. What the flag
// actually withholds is the write, which is the claim the delete trailer already makes
// accurately about its DELETE.
//
// It matters because --dry-run is the flag an operator reaches for when they are unsure
// what a filter selects, and a trailer that overstates its own restraint is the wrong
// thing to be teaching them about a tool that spends Actions minutes.
func TestLifecycleDryRunTrailerClaimsOnlyTheWrite(t *testing.T) {
	h := newHarness(t, "cancel_all").withCurrent(gh("o", "r"))
	if code := h.runDriven("cancel", "--all", "--dry-run"); code != 0 {
		t.Fatalf("cancel --all --dry-run exited %d, want 0 (R10)", code)
	}
	if len(h.counting.urls) == 0 {
		t.Fatal("the dry run issued no request at all, so this test is pinning nothing")
	}
	err := h.stderr.String()
	if strings.Contains(err, "no request issued") {
		t.Errorf("the trailer claims no request was issued, but the crawl issued %d:\n%s",
			len(h.counting.urls), err)
	}
	if !strings.Contains(err, "no POST issued") {
		t.Errorf("the trailer does not name the write it withheld:\n%s", err)
	}
}

// TestRunIDAndFilterAreMutuallyExclusive pins that the two grammars do not silently
// combine. Naming Run 8 and passing -s failure asks two different questions, and guessing
// which one was meant is how a write lands somewhere nobody named.
func TestRunIDAndFilterAreMutuallyExclusive(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))
	code := h.run("cancel", "8", "-s", "failure", "--yes")
	if code == 0 {
		t.Errorf("a Run ID and a filter together exited 0; the two grammars are exclusive")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestRunIDNeedsOneRepository pins the other half of the positional grammar: a bare Run ID
// is only meaningful against one repository, so a fan-out must refuse rather than pick one
// or broadcast the write across every discovered repository.
func TestRunIDNeedsOneRepository(t *testing.T) {
	h := newHarnessOffline(t).withDiscovered(gh("o", "r"), gh("o", "s"))
	code := h.run("cancel", "8", "--all-repos", "--yes")
	if code == 0 {
		t.Errorf("a Run ID under a fan-out exited 0; a bare id names no repository")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestRunIDRejectsANonNumericArgument pins client-side validation of the positional form,
// the same rule cli-surface R6 fixes for filter values: the check is ours, offline, and
// before any request.
func TestRunIDRejectsANonNumericArgument(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))
	code := h.run("cancel", "not-a-run-id", "--yes")
	if code == 0 {
		t.Errorf("a non-numeric Run ID exited 0; it must be rejected client-side (R6's rule)")
	}
	if h.counting.count() != 0 {
		t.Errorf("the rejection issued %d requests; it must issue zero", h.counting.count())
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
	out := cancel.stdout.String()
	if !strings.Contains(out, "1 skipped") {
		t.Errorf("the cancel's 404 was not recorded as a skip (R22, AC13):\n%s", out)
	}
	if !strings.Contains(out, "0 failed") {
		t.Errorf("the cancel's 404 was counted as a failure; the requested end state holds (R22, AC13):\n%s", out)
	}
}
