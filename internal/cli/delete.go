package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/ops"
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
		return err
	}

	if f.dryRun {
		return printDryRun(deps, plan) // R10: report, delete nothing, exit 0, write no log
	}

	confirmed, err := deps.Purge.Confirm(plan, ops.NonInteractiveYes()) // R11
	if err != nil {
		return err
	}
	sum, err := deps.Purge.Execute(ctx, confirmed)
	if err != nil {
		return err
	}
	printSummary(deps, sum)
	return exitFromSummary(sum)
}

// hasFilter reports whether any filter axis was set, which is R26's test for whether
// --all is required. The scope flags (-R, --all-repos) are not filters: they select
// repositories, not Runs.
func (f *deleteFlags) hasFilter() bool {
	l := f.lf
	return l.branch != "" || l.commit != "" || l.created != "" || l.event != "" ||
		l.status != "" || l.user != "" || l.workflow != ""
}

// printDryRun reports exactly what would be deleted: one row per Run in the resolved
// set, each naming its repository and Run ID, so `grep` and `wc -l` answer questions
// about it (cli-surface R10, AC9). A skipped Run carries its reason. It writes no line
// to the deletion log and requires no writable log, because it issues no DELETE.
func printDryRun(deps Deps, plan ops.Plan) error {
	items := plan.Items()
	for _, it := range items {
		row := it.Repo.String() + "\t" + strconv.FormatInt(it.ID, 10)
		if it.Skip != ops.SkipNone {
			row += "\t(skipped: " + string(it.Skip) + ")"
		}
		_, _ = fmt.Fprintln(deps.Stdout, row)
	}
	note := fmt.Sprintf("gh-runs: dry run: %d Runs would be deleted", plan.Total()-plan.Skipped())
	if plan.Skipped() > 0 {
		note += fmt.Sprintf(", %d skipped", plan.Skipped())
	}
	_, _ = fmt.Fprintln(deps.Stderr, note+" (no DELETE issued, no log written)")
	return nil
}

// printSummary reports what this pass did, and only this pass (purge R25): the
// deletions, the successes-by-being-gone, the skips, and the failures grouped by
// reason (R22, AC18). It is the terminal account the CLI prints; the live progress a
// TUI shows is the running-Purge surface's.
func printSummary(deps Deps, sum ops.Summary) {
	_, _ = fmt.Fprintf(deps.Stdout, "Deleted %d, gone %d, skipped %d, failed %d of %d Runs.\n",
		sum.Deleted, sum.Gone, sum.Skipped, sum.FailedCount(), sum.Total)
	groups := append([]ops.FailureGroup(nil), sum.Failures...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Reason < groups[j].Reason })
	for _, g := range groups {
		_, _ = fmt.Fprintf(deps.Stdout, "  %d x %s\n", g.Count, g.Reason)
	}
	if sum.Reason != "" {
		_, _ = fmt.Fprintln(deps.Stdout, sum.Reason)
	}
}

// exitFromSummary maps the pass to gh's exit codes (cli-surface R17): a cancelled Purge
// exits 2 and states that re-running resumes it, a circuit-break or a log failure or
// any real failure exits 1, and everything else (including zero matches and a Purge
// that deleted nothing because all were skipped) exits 0.
func exitFromSummary(sum ops.Summary) error {
	switch {
	case sum.Cancelled:
		return &exitError{code: exitCancelled, msg: "purge interrupted; re-run the same command to resume it"}
	case sum.LogFailed:
		return &exitError{code: exitFailure, msg: "purge stopped: " + sum.Reason}
	case sum.CircuitBroke:
		return &exitError{code: exitFailure, msg: "purge circuit-broke: " + sum.Reason}
	case sum.FailedCount() > 0:
		return &exitError{code: exitFailure, msg: fmt.Sprintf("purge completed with %d failures", sum.FailedCount())}
	default:
		return nil
	}
}
