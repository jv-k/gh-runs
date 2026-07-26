package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// deleteFlags holds the delete command's flags. It embeds listFlags so the filter
// axes and the scope flags parse and validate through exactly the same code as list
// (cli-surface R6, ADR-0016), and adds the four write-specific flags. list's -a/--all
// ("include disabled workflows") is deliberately NOT bound here: on delete, --all is
// gh's match-all spelling (R26), a different flag with no shorthand, bound to matchAll.
type deleteFlags struct {
	lf       listFlags
	matchAll bool // --all: delete every Run in scope, the zero filter asked for by name (R26)
	dryRun   bool // --dry-run: resolve and report, delete nothing, exit 0 (R10)
	yes      bool // --yes: the non-interactive confirmation, always required to delete (R11)
}

// newDeleteCmd builds the delete command (cli-surface R25, R26, R27). Bare `gh runs
// delete` with no arguments opens the TUI, which main.go's surface picker handles
// before the CLI runs; reaching this command means a flag was passed, so a bare
// destructive invocation is guarded here (R26). The filter and scope flags mirror
// list's; the write flags are --all, --all-repos, --dry-run and --yes.
func newDeleteCmd(deps Deps) *cobra.Command {
	f := &deleteFlags{}
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete workflow runs across your repositories (a Purge)",
		Long: "Delete every Run matching a filter, across one or more repositories.\n\n" +
			"A destructive delete requires --yes. --dry-run reports exactly what would be\n" +
			"deleted and deletes nothing. Match every Run in scope with --all; fan out across\n" +
			"every discovered repository with --all-repos. Bare `gh runs delete` opens the TUI.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDelete(deps, f)
		},
	}
	fl := cmd.Flags()
	// The filter axes, identical to list's (R6). -s rides filter.ParseStatus, --created
	// rides filter.ParseCreated, and no --conclusion exists (R5).
	fl.StringVarP(&f.lf.branch, "branch", "b", "", "Filter runs by branch")
	fl.StringVarP(&f.lf.commit, "commit", "c", "", "Filter runs by the SHA of the commit")
	fl.StringVar(&f.lf.created, "created", "", "Filter runs by the date it was created")
	fl.StringVarP(&f.lf.event, "event", "e", "", "Filter runs by which event triggered the run")
	fl.StringVarP(&f.lf.status, "status", "s", "", "Filter runs by status")
	fl.StringVarP(&f.lf.user, "user", "u", "", "Filter runs by user who triggered the run")
	fl.StringVarP(&f.lf.workflow, "workflow", "w", "", "Filter runs by workflow")
	fl.StringVarP(&f.lf.repo, "repo", "R", "", "Select another repository using the [HOST/]OWNER/REPO format")
	fl.BoolVar(&f.lf.allRepos, "all-repos", false, "Delete runs across every discovered repository")
	// The write flags. --all is match-all (R26), no shorthand so it stays distinct from
	// list's unrelated -a. --dry-run and --yes are R10 and R11.
	fl.BoolVar(&f.matchAll, "all", false, "Delete every Run in scope (required to match all)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "Report what would be deleted and delete nothing")
	fl.BoolVar(&f.yes, "yes", false, "Confirm the deletion without prompting (required to delete)")
	return cmd
}

// runDelete is the write half's pipeline: guard the blast radius, resolve the affected
// set through the same crawl-and-Plan code path --dry-run and the real operation share,
// then either print the plan (--dry-run) or Confirm and Execute it (cli-surface R10,
// R11, R20). Interruption is wired to a context so SIGINT stops the Purge and exits 2
// (R17, AC13).
func runDelete(deps Deps, f *deleteFlags) error {
	// R26: the zero filter matches every Run, so "delete everything" must be asked for
	// by name. A delete with no filter and no --all refuses and deletes nothing.
	if !f.hasFilter() && !f.matchAll {
		return fmt.Errorf("refusing to delete: pass a filter (for example -s failure) or --all to delete every Run in scope")
	}
	// R11: a destructive delete requires --yes and refuses without it. --dry-run needs
	// neither --yes nor a writable log, because it issues no DELETE (R10).
	if !f.dryRun && !f.yes {
		return fmt.Errorf("refusing to delete without --yes; pass --yes to confirm, or --dry-run to preview")
	}
	if deps.Purge == nil {
		return fmt.Errorf("the delete command is not available in this build")
	}

	flt, err := buildFilter(&f.lf) // client-side validation before any request (R6)
	if err != nil {
		return err
	}
	sc, err := resolveScope(deps, &f.lf)
	if err != nil {
		return err
	}
	snapshot, err := deps.RepoSnapshot()
	if err != nil {
		return err
	}

	// SIGINT cancels the crawl and the Purge, so an interrupted Purge stops promptly and
	// exits 2, leaving deleted Runs deleted (R16, R17, AC13).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	items, err := deps.Purge.Crawl(ctx, sc.repos, flt)
	if err != nil {
		return err
	}
	plan, err := deps.Purge.Plan(ops.OpDelete, items, snapshot)
	if err != nil {
		return namingTheExclusion(deps, sc.repos, err)
	}

	if f.dryRun {
		return printDryRun(deps, ops.OpDelete, plan) // R10: report, delete nothing, exit 0, write no log
	}

	confirmed, err := deps.Purge.Confirm(plan, ops.NonInteractiveYes()) // R11
	if err != nil {
		return err
	}
	sum, err := deps.Purge.Execute(ctx, confirmed)
	if err != nil {
		return err
	}
	printSummary(deps, ops.OpDelete, sum)
	return exitFromSummary(ops.OpDelete, sum)
}

// hasFilter reports whether any filter axis was set, which is R26's test for whether
// --all is required. The scope flags (-R, --all-repos) are not filters: they select
// repositories, not Runs.
func (f *deleteFlags) hasFilter() bool {
	l := f.lf
	return l.branch != "" || l.commit != "" || l.created != "" || l.event != "" ||
		l.status != "" || l.user != "" || l.workflow != ""
}

// printDryRun reports exactly what would be acted on: one row per Run in the resolved
// set, each naming its repository and Run ID, so `grep` and `wc -l` answer questions
// about it (cli-surface R10, AC9). A skipped Run carries its reason. A Purge's dry run
// writes no line to the deletion log and requires no writable log, because it issues no
// DELETE; a lifecycle operation has no log to write either way (run-lifecycle R24).
func printDryRun(deps Deps, op ops.Operation, plan ops.Plan) error {
	items := plan.Items()
	for _, it := range items {
		row := it.Repo.String() + "\t" + strconv.FormatInt(it.ID, 10)
		if it.Skip != ops.SkipNone {
			row += "\t(skipped: " + string(it.Skip) + ")"
		}
		_, _ = fmt.Fprintln(deps.Stdout, row)
	}
	verb := "would be deleted"
	trailer := " (no DELETE issued, no log written)"
	if op != ops.OpDelete {
		verb = "would be acted on by " + string(op)
		trailer = " (no request issued)"
	}
	note := fmt.Sprintf("gh-runs: dry run: %d Runs %s", plan.Total()-plan.Skipped(), verb)
	if plan.Skipped() > 0 {
		note += fmt.Sprintf(", %d skipped", plan.Skipped())
	}
	_, _ = fmt.Fprintln(deps.Stderr, note+trailer)
	return nil
}

// printSummary reports what this pass did, and only this pass (purge R25): what landed,
// the skips, and the failures grouped by reason (R22, AC18). It is the terminal account
// the CLI prints; the live progress a TUI shows is the running-operation surface's.
//
// The headline is the operation's, because a Purge and a lifecycle mutation count
// different things. A Purge reports its deletions and its successes-by-being-gone. A
// cancel reports a requested cancellation and never a cancelled one, because a 202 means
// the request was accepted and only a later poll can say what became of the Run
// (run-lifecycle R4, AC5). A re-run reports a requested re-run for the same reason: what
// the 201 proves is that an Attempt was added, not that it has finished.
func printSummary(deps Deps, op ops.Operation, sum ops.Summary) {
	if op == ops.OpDelete {
		_, _ = fmt.Fprintf(deps.Stdout, "Deleted %d, gone %d, skipped %d, failed %d of %d Runs.\n",
			sum.Deleted, sum.Gone, sum.Skipped, sum.FailedCount(), sum.Total)
	} else {
		_, _ = fmt.Fprintf(deps.Stdout, "%s %d, skipped %d, failed %d of %d Runs.\n",
			requestedHeadline(op), sum.Acted, sum.Skipped, sum.FailedCount(), sum.Total)
	}
	// Both lists are labelled, and neither by omission. They print into one flat list, so
	// an unlabelled group would be read as whichever kind the reader assumed, and the
	// reasons carry API text this command does not author.
	printGroups(deps, sum.Failures, "failed: ")
	// A skip is not a failure and changes no exit code, but a count with no words leaves
	// the operator of a non-interactive Purge with nothing to act on: purge AC14a records
	// the skip with its reason, and on R19a's path that reason carries the API's own 403.
	printGroups(deps, sum.Skips, "skipped: ")
	if sum.Reason != "" {
		_, _ = fmt.Fprintln(deps.Stdout, sum.Reason)
	}
}

// printGroups renders one grouped-reason list under label, sorted by reason so the
// output is stable (R22, AC18).
func printGroups(deps Deps, groups []ops.FailureGroup, label string) {
	sorted := append([]ops.FailureGroup(nil), groups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Reason < sorted[j].Reason })
	for _, g := range sorted {
		// A reason carries the API's error message verbatim (ops.failureReason), which a
		// hostile third-party repository controls, so it is sanitised here at the terminal
		// boundary exactly as the list table sanitises a Run's own fields (security review,
		// textsan). The R29 deletion log records the reason raw and write-only, so only
		// this render is stripped, never what ops writes.
		_, _ = fmt.Fprintf(deps.Stdout, "  %d x %s%s\n", g.Count, label, textsan.Sanitize(g.Reason))
	}
}

// requestedHeadline names what a lifecycle pass asked for, in the words the requirements
// use. Every one of them is a request that was accepted, never an outcome observed: R4
// says so of cancel outright, and R8 says a re-run adds an Attempt whose result is a later
// poll's to report. The wording is what keeps the headline from claiming the thing the
// product exists to stop conflating.
func requestedHeadline(op ops.Operation) string {
	switch op {
	case ops.OpCancel:
		return "Cancellation requested for"
	case ops.OpForceCancel:
		return "Force-cancellation requested for"
	case ops.OpRerun:
		return "Re-run requested for"
	case ops.OpRerunFailed:
		return "Re-run of failed jobs requested for"
	default:
		return "Acted on"
	}
}

// exitFromSummary maps the pass to gh's exit codes (cli-surface R17): an interrupted pass
// exits 2 and states that re-running resumes it, a circuit-break or a log failure or any
// real failure exits 1, and everything else (including zero matches and a pass that acted
// on nothing because all were skipped) exits 0.
//
// The resume line is a Purge's alone. A Purge's filter is its job state and re-running the
// same command picks up where it stopped (ADR-0006), but a cancel or a re-run over the
// same filter would act again on the Runs it already acted on, so telling an operator to
// re-run one would be advice to spend Actions minutes twice.
func exitFromSummary(op ops.Operation, sum ops.Summary) error {
	noun := string(op)
	if op == ops.OpDelete {
		noun = "purge"
	}
	switch {
	case sum.Cancelled && op == ops.OpDelete:
		return &exitError{code: exitCancelled, msg: "purge interrupted; re-run the same command to resume it"}
	case sum.Cancelled:
		return &exitError{code: exitCancelled, msg: noun + " interrupted; the Runs it had not reached were not touched"}
	case sum.LogFailed:
		return &exitError{code: exitFailure, msg: noun + " stopped: " + sum.Reason}
	case sum.CircuitBroke:
		return &exitError{code: exitFailure, msg: noun + " circuit-broke: " + sum.Reason}
	case sum.FailedCount() > 0:
		return &exitError{code: exitFailure, msg: fmt.Sprintf("%s completed with %d failures", noun, sum.FailedCount())}
	default:
		return nil
	}
}

// namingTheExclusion adds the real cause to a planning failure over a repository the
// config excludes. Exclusion keeps a repository out of discovery (settings R7), so it
// carries no recorded capability, and Plan refuses a repository absent from the
// eligibility snapshot (purge R10, ADR-0019). That refusal is correct and stays: it is
// the fail-closed rule, and the operator's own config produced the state it fails on.
// What was wrong is the message, which named the snapshot and left the operator to
// work out that a config line put the repository outside it.
//
// It is a diagnostic and never a refusal, which is the distinction settings R4 turns
// on: an explicit -R outranks the config file, so the request proceeds as far as it
// can and only the wording changes. A repository not on the list gets the original
// error untouched.
func namingTheExclusion(deps Deps, repos []domain.RepoID, err error) error {
	var named []string
	for _, id := range repos {
		if slices.Contains(deps.Exclude, id) {
			named = append(named, id.Owner+"/"+id.Name)
		}
	}
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf(
		"%w. Your config's exclude list names %s, so discovery never recorded capability there: remove it from exclude to act on it",
		err, strings.Join(named, ", "))
}
