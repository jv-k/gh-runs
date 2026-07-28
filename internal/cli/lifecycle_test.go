package cli_test

import (
	"errors"
	"strings"
	"testing"
)

// errNoRepo is the working-directory resolver failing, which is not a failure: it means the
// invocation fans out across the discovered set (cli-surface R8, R22).
var errNoRepo = errors.New("not inside a repository")

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

// TestRerunJobFlagAddressesTheJobEndpoint pins cli-surface R28a and AC22: "-j <id> addresses
// the Job endpoint with that id and no Run endpoint at all". It needs no Run lookup, so it
// issues no request before the write, and it prices at FrictionNone like any single re-run,
// so it needs no --yes.
func TestRerunJobFlagAddressesTheJobEndpoint(t *testing.T) {
	h := newHarness(t, "lifecycle_byid").withCurrent(gh("o", "r"))

	code := h.runDriven("rerun", "-j", "4242")

	if code != 0 {
		t.Fatalf("rerun -j 4242 exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	if !h.postedTo("/actions/jobs/4242/rerun") {
		t.Errorf("-j did not POST the Job endpoint (R28a, AC22). urls: %v", h.counting.urls)
	}
	for _, u := range h.counting.urls {
		if strings.Contains(u, "/actions/runs/") {
			t.Errorf("-j touched a Run endpoint %q; AC22 requires none at all", u)
		}
	}
}

// TestRerunJobFlagIsRefusedWhereItNamesNothing pins the two refusals R28a and R28c fix. A Job
// id names one Job of one Run and so names no repository, which puts it under the rule a bare
// Run ID takes: refused under a fan-out. And -j selects a different operation from --failed,
// so the pair is refused rather than one of them silently winning.
//
// Both must issue zero requests, which the offline transport proves by failing any wire call.
func TestRerunJobFlagIsRefusedWhereItNamesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"under a fan-out there is no repository to address", []string{"rerun", "-j", "4242", "--all-repos"}},
		{"beside --failed it selects two operations", []string{"rerun", "-j", "4242", "--failed"}},
		{"beside a Run ID it names the target twice", []string{"rerun", "9", "-j", "4242"}},
		{"beside a filter it names the target twice", []string{"rerun", "-j", "4242", "-s", "failure"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOffline(t).withCurrent(gh("o", "r"))
			if code := h.run(tc.args...); code == 0 {
				t.Errorf("%v exited 0, want a usage refusal (R28a, R28c)", tc.args)
			}
			if h.counting.count() != 0 {
				t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
			}
		})
	}
}

// TestRerunJobNameSkipsWhatItDidNotMatchAndExitsZero pins run-lifecycle AC14c and
// cli-surface R28b at the figures the criterion names. A by-name re-run across 12 Runs of
// which 9 have a Job of that name issues 9 re-run requests, reports the other 3 under skips
// with a reason naming the absent Job, none under failures, and exits 0.
//
// The summing clauses are the point: the frozen count is 12 and not 9, the 3 group under one
// reason rather than three, and the summary's accepted, skipped and failed figures sum to
// the same 12.
func TestRerunJobNameSkipsWhatItDidNotMatchAndExitsZero(t *testing.T) {
	h := newHarness(t, "rerun_job_name").withCurrent(gh("o", "r"))

	code := h.runDriven("rerun", "--all", "--job-name", "build", "--yes")

	if code != 0 {
		t.Fatalf("rerun --job-name exited %d, want 0: a name that matched nothing is a skip (R28b, AC14c). stderr: %s",
			code, h.stderr.String())
	}
	posts := 0
	for _, u := range h.counting.urls {
		if strings.Contains(u, "/actions/jobs/") && strings.HasSuffix(u, "/rerun") {
			posts++
		}
		if strings.Contains(u, "/actions/runs/") && strings.HasSuffix(u, "/rerun") {
			t.Errorf("a by-name re-run POSTed the Run endpoint %q; it addresses the Job endpoint alone (R14a)", u)
		}
	}
	if posts != 9 {
		t.Errorf("issued %d per-Job re-runs, want 9: one per Run holding a Job of that name (AC14c)", posts)
	}
	out := h.stdout.String()
	if !strings.Contains(out, "3 skipped") || !strings.Contains(out, "of 12 Runs") {
		t.Errorf("summary does not report 3 skipped of a frozen 12 (AC14c):\n%s", out)
	}
	if !strings.Contains(out, "0 failed") {
		t.Errorf("summary reports a failure; an unmatched name is a skip and never a failure (AC14c):\n%s", out)
	}
	if !strings.Contains(out, `no job named "build" in this run`) {
		t.Errorf("summary does not name the absent Job (AC14c):\n%s", out)
	}
	// One group with a count, not one line per Run: the resolver builds the reason once from
	// the name, so groupByReason collapses them (ADR-0019, AC14c).
	if got := strings.Count(out, `no job named "build" in this run`); got != 1 {
		t.Errorf("the absent-Job reason appears %d times, want one group (AC14c)", got)
	}
	if !strings.Contains(out, `3 x skipped: no job named "build" in this run`) {
		t.Errorf("the one skip group carries no count; 3 Runs share it (AC14c):\n%s", out)
	}
}

// TestRerunJobNameResolutionCutShortPricesWhatItResolvedAndExitsOne pins run-lifecycle
// R17a and AC14d at the criterion's own figures: 40 selected Runs spanning two
// repositories, whose resolution is rate-limited after 12 Runs of which every one holds the
// named Job.
//
// Exactly 12 re-run requests follow, which is the whole of the frozen set because none of
// the 12 was unmatched. The 28 appear in no count and under no skip reason, because they
// were never answered. R28b's exit 0 covers a name that matched nothing, which this is not:
// nothing failed, but not everything the operator asked for happened, and cli-surface R17's
// one failure bit is what that exits under. The count is stated rather than only signalled,
// because an exit code names no number (cli-surface R16).
func TestRerunJobNameResolutionCutShortPricesWhatItResolvedAndExitsOne(t *testing.T) {
	h := newHarness(t, "rerun_job_name_limited").
		withCurrentErr(errNoRepo).
		withDiscovered(gh("o", "a"), gh("o", "b")).
		withWritable(gh("o", "a"), gh("o", "b"))

	code := h.runDriven("rerun", "--all-repos", "--all", "--job-name", "build", "--yes")

	if code != 1 {
		t.Fatalf("a resolution that stopped early exited %d, want 1 (R17a, AC14d). stderr: %s",
			code, h.stderr.String())
	}
	posts := 0
	for _, u := range h.counting.urls {
		if strings.Contains(u, "/actions/jobs/") && strings.HasSuffix(u, "/rerun") {
			posts++
		}
	}
	if posts != 12 {
		t.Errorf("issued %d per-Job re-runs, want 12: the frozen set is what resolution resolved (R17a, AC14d)", posts)
	}
	out := h.stdout.String() + h.stderr.String()
	if !strings.Contains(out, "28") {
		t.Errorf("the output states no count for the 28 Runs the resolution never reached (R17a, AC14d):\n%s", out)
	}
	if strings.Contains(out, "of 40 Runs") {
		t.Errorf("the summary priced the whole selection; the frozen set is the 12 that resolved (R17a):\n%s", out)
	}
	if !strings.Contains(out, "of 12 Runs") {
		t.Errorf("the summary does not price the 12 that resolved (R17a, AC14d):\n%s", out)
	}
}

// TestRerunJobNameIsRefusedBesideTheJobIDFlag pins cli-surface R28c. The two flags are
// separate and must also be refused together: they select one operation's target two ways,
// and no reading of the pair is the operator's stated intent. Merging them into one flag
// read by context is what R28c forbids, and this is the refusal that keeps them apart.
func TestRerunJobNameIsRefusedBesideTheJobIDFlag(t *testing.T) {
	h := newHarnessOffline(t).withCurrent(gh("o", "r"))

	if code := h.run("rerun", "-j", "4242", "--job-name", "build"); code == 0 {
		t.Error("-j beside --job-name exited 0, want a usage refusal (R28c)")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestRerunJobNameIsRefusedUnderAFanOutWithNoRepository pins R29's rule applied to this
// flag. --job-name resolves against a set of Runs, so it needs a repository to address the
// listings against exactly as -j does, and a bare Run ID under a fan-out names nothing.
func TestRerunJobNameIsRefusedUnderAFanOutWithNoRepository(t *testing.T) {
	h := newHarnessOffline(t).withCurrentErr(errNoRepo).withDiscovered(gh("o", "a"), gh("o", "b"))

	if code := h.run("rerun", "9", "--job-name", "build"); code == 0 {
		t.Error("a bare Run ID under a fan-out exited 0, want a usage refusal (R29)")
	}
	if h.counting.count() != 0 {
		t.Errorf("the refusal issued %d requests; it must issue zero", h.counting.count())
	}
}

// TestRerunJobNameDryRunListsEveryMemberAndClaimsTheFrozenTotal pins cli-surface R10 over
// the second kind of member. A dry run over 12 Runs of which 9 match prints 12 rows, 3 of
// them marked skipped, and claims 12: the trailer reads Total and Skipped, which count the
// Item-less members, so the listing and the claim cannot disagree.
func TestRerunJobNameDryRunListsEveryMemberAndClaimsTheFrozenTotal(t *testing.T) {
	h := newHarness(t, "rerun_job_name").withCurrent(gh("o", "r"))

	code := h.runDriven("rerun", "--all", "--job-name", "build", "--dry-run")

	if code != 0 {
		t.Fatalf("--dry-run exited %d, want 0. stderr: %s", code, h.stderr.String())
	}
	for _, u := range h.counting.urls {
		if strings.HasSuffix(u, "/rerun") {
			t.Errorf("--dry-run issued a re-run POST to %q; it withholds the write (R10)", u)
		}
	}
	rows := strings.Count(strings.TrimRight(h.stdout.String(), "\n"), "\n") + 1
	if rows != 12 {
		t.Errorf("--dry-run printed %d rows, want 12: every member of the frozen set gets one (R10)", rows)
	}
	if got := strings.Count(h.stdout.String(), "(skipped:"); got != 3 {
		t.Errorf("--dry-run marked %d rows skipped, want 3 (R10)", got)
	}
	if !strings.Contains(h.stderr.String(), "3 skipped") {
		t.Errorf("--dry-run trailer does not report the 3 skipped:\n%s", h.stderr.String())
	}
}
