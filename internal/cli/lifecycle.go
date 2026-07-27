package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// lifecycleFlags holds the cancel and rerun commands' flags. It embeds listFlags so the
// filter axes and the scope flags parse and validate through exactly the same code as list
// and delete (cli-surface R6, ADR-0016), and adds the write-specific ones. Each command
// binds only the modifiers that are its own: --force belongs to cancel (R6) and --failed
// and --debug to rerun (R13, R14), so neither offers a flag the other's endpoints ignore.
type lifecycleFlags struct {
	lf       listFlags
	matchAll bool  // --all: act on every Run in scope, the zero filter asked for by name
	dryRun   bool  // --dry-run: resolve and report, write nothing, exit 0
	yes      bool  // --yes: the non-interactive confirmation
	force    bool  // cancel only: force-cancel, a distinct operation (R6)
	failed   bool  // rerun only: re-run failed Jobs, a distinct operation (R13)
	debug    bool  // rerun only: enable_debug_logging, off by default (R14, AC14)
	job      int64 // rerun only: -j/--job, re-run the Job this id names (R28a)
}

// newCancelCmd builds the cancel command, the non-interactive form of run-lifecycle's
// cancel and force-cancel (that feature's Related section, ADR-0008). `gh run cancel` and
// `gh run cancel --force` are the shapes it mirrors, extended with the filter axes so a
// cancel can name a set the way a Purge does rather than one Run at a time.
func newCancelCmd(deps Deps) *cobra.Command {
	f := &lifecycleFlags{}
	cmd := &cobra.Command{
		Use:   "cancel [<run-id>...]",
		Short: "Cancel workflow runs",
		Long: "Cancel one or more Runs, either by naming their Run IDs or by matching a filter.\n\n" +
			"Cancel is asynchronous: the API accepting the request does not mean the Run has\n" +
			"stopped, and only a later poll shows the cancelled conclusion. A Run that is not\n" +
			"cancelable is skipped, not failed, and --force escalates to force-cancel against\n" +
			"its own endpoint. Cancelling requires --yes; --dry-run reports and cancels nothing.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			op := ops.OpCancel
			if f.force {
				op = ops.OpForceCancel // R6: distinct, never silently substituted
			}
			return runLifecycle(deps, f, op, args)
		},
	}
	bindLifecycleFlags(cmd, f, "Cancel")
	cmd.Flags().BoolVar(&f.force, "force", false, "Escalate to force-cancel, a distinct endpoint")
	return cmd
}

// newRerunCmd builds the rerun command, the non-interactive form of re-run and re-run
// failed Jobs (ADR-0008: `gh run rerun` exposes --failed and --debug, and both spellings
// are mirrored here).
func newRerunCmd(deps Deps) *cobra.Command {
	f := &lifecycleFlags{}
	cmd := &cobra.Command{
		Use:   "rerun [<run-id>...]",
		Short: "Re-run workflow runs",
		Long: "Re-run one or more Runs, either by naming their Run IDs or by matching a filter.\n\n" +
			"A re-run does not create a Run. It adds an attempt to the Run that already exists,\n" +
			"whose status returns to queued and whose conclusion clears. --failed re-runs only\n" +
			"the failed jobs, and --debug enables debug logging for the attempt. A single\n" +
			"re-run needs no confirmation; re-running a matched set requires --yes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			op := ops.OpRerun
			switch {
			case f.job != 0 && f.failed:
				// Two distinct operations against two distinct endpoints. Neither reading of
				// the pair is what the operator asked for (R28, ADR-0008).
				return fmt.Errorf("--failed and -j select different operations: pass one")
			case f.job != 0:
				op = ops.OpRerunJob // R28a, run-lifecycle R14a
			case f.failed:
				op = ops.OpRerunFailed // R13: distinct, and offered only where Jobs failed
			}
			return runLifecycle(deps, f, op, args)
		},
	}
	bindLifecycleFlags(cmd, f, "Re-run")
	cmd.Flags().BoolVar(&f.failed, "failed", false, "Re-run only the jobs that failed")
	cmd.Flags().BoolVarP(&f.debug, "debug", "d", false, "Enable debug logging for the new attempt")
	cmd.Flags().Int64VarP(&f.job, "job", "j", 0, "Re-run the job this ID names, and the jobs declared after it")
	return cmd
}

// bindLifecycleFlags binds the axes both commands share. They are delete's, verbatim, so a
// filter means the same thing whichever verb it is handed to (cli-surface R6). verb is the
// word the help text uses, so the two commands read as themselves rather than as a
// parameterised third thing.
func bindLifecycleFlags(cmd *cobra.Command, f *lifecycleFlags, verb string) {
	fl := cmd.Flags()
	fl.StringVarP(&f.lf.branch, "branch", "b", "", "Filter runs by branch")
	fl.StringVarP(&f.lf.commit, "commit", "c", "", "Filter runs by the SHA of the commit")
	fl.StringVar(&f.lf.created, "created", "", "Filter runs by the date it was created")
	fl.StringVarP(&f.lf.event, "event", "e", "", "Filter runs by which event triggered the run")
	fl.StringVarP(&f.lf.status, "status", "s", "", "Filter runs by status")
	fl.StringVarP(&f.lf.user, "user", "u", "", "Filter runs by user who triggered the run")
	fl.StringVarP(&f.lf.workflow, "workflow", "w", "", "Filter runs by workflow")
	fl.StringVarP(&f.lf.repo, "repo", "R", "", "Select another repository using the [HOST/]OWNER/REPO format")
	fl.BoolVar(&f.lf.allRepos, "all-repos", false, verb+" runs across every discovered repository")
	fl.BoolVar(&f.matchAll, "all", false, verb+" every Run in scope (required to match all)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "Report what would be affected and change nothing")
	fl.BoolVar(&f.yes, "yes", false, "Confirm without prompting")
}

// runLifecycle is the pipeline both verbs share, and it is delete's with one difference:
// the affected set can arrive as positional Run IDs as well as from a crawl. Everything
// past resolution is identical, because a bulk lifecycle mutation is the same walk over a
// frozen set under the same failure contract as a Purge (run-lifecycle R21, R23).
func runLifecycle(deps Deps, f *lifecycleFlags, op ops.Operation, args []string) error {
	if deps.Purge == nil {
		return fmt.Errorf("this command is not available in this build")
	}
	// The two grammars are exclusive. Naming Run 8 and passing -s failure asks two
	// different questions, and answering the one that happens to be checked first is how a
	// write lands on a set nobody named.
	if len(args) > 0 && (f.hasFilter() || f.matchAll) {
		return fmt.Errorf("refusing to guess: pass Run IDs or a filter, not both")
	}

	// SIGINT cancels the crawl and the walk, so an interrupted operation stops promptly and
	// exits 2, leaving what it already did done (cli-surface R16, R17, AC13).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sc, err := resolveScope(deps, &f.lf)
	if err != nil {
		return err
	}
	items, err := resolveLifecycleSet(ctx, deps, f, sc, args)
	if err != nil {
		return err
	}
	snapshot, err := deps.RepoSnapshot()
	if err != nil {
		return err
	}
	plan, err := deps.Purge.Plan(op, items, snapshot)
	if err != nil {
		return namingTheExclusion(deps, sc.repos, err)
	}
	if f.debug {
		plan = plan.WithDebugLogging() // R14, AC14: opt-in, and it rides the request body
	}

	if f.dryRun {
		return printLifecycleDryRun(deps, plan, op)
	}

	// The confirmation the Plan priced is the confirmation required, so R18's asymmetry is
	// read off ops rather than restated here: a single re-run prices at FrictionNone and
	// needs no --yes, and everything else does. Deriving it keeps one table (ADR-0019) and
	// means a change to the pricing cannot leave this surface behind.
	in := ops.NoInput()
	if plan.Friction() != ops.FrictionNone {
		if !f.yes {
			return fmt.Errorf("refusing to %s %d %s without --yes; pass --yes to confirm, or --dry-run to preview",
				op, plan.Total(), plural(plan.Total(), "run", "runs"))
		}
		in = ops.NonInteractiveYes()
	}
	confirmed, err := deps.Purge.Confirm(plan, in)
	if err != nil {
		return err
	}
	sum, err := deps.Purge.Execute(ctx, confirmed)
	if err != nil {
		return err
	}
	printLifecycleSummary(deps, sum, op)
	return exitFromSummary(sum)
}

// resolveLifecycleSet builds the frozen set from whichever grammar was used.
//
// Positional Run IDs name their targets, so they issue no crawl at all: `gh runs cancel 8`
// costs one request, which is what makes it answer as fast as gh's own. They carry no Run
// object, so Plan stamps no Status or Orphaned-Run skip over them and the API is the
// authority for both, which is what R3 and R15 already say it is. The repository gate is
// unaffected: it reads the discovered snapshot, not the Run.
//
// A filter resolves through the same crawl the delete command uses, so the set a --dry-run
// reports and the set the operation walks are produced by one code path (cli-surface R10).
func resolveLifecycleSet(ctx context.Context, deps Deps, f *lifecycleFlags, sc scope, args []string) ([]ops.Item, error) {
	if f.job != 0 {
		// A Job id names one Job of one Run and so names no repository, which puts -j under
		// the rule a bare Run ID takes: refused under a fan-out, where there is no repository
		// to address the Job endpoint against (R28a, R29).
		if len(sc.repos) != 1 {
			return nil, fmt.Errorf("a Job ID names a Job in one repository: pass -R OWNER/REPO, or run inside the repository")
		}
		if len(args) > 0 || f.hasFilter() || f.matchAll {
			return nil, fmt.Errorf("refusing to guess: -j names one Job, so it takes no Run IDs and no filter")
		}
		// No Run lookup, and no request before the Plan. AC22 fixes this: "-j <id> addresses
		// the Job endpoint with that id and no Run endpoint at all", so the Item needs the
		// repository and the id and nothing the API would have to be asked for.
		return []ops.Item{ops.JobItem(domain.Job{Repo: sc.repos[0], ID: f.job})}, nil
	}
	if len(args) > 0 {
		// A bare Run ID is meaningful against one repository only. Under a fan-out it names
		// nothing, and picking one of the discovered repositories, or broadcasting the write
		// to all of them, are both worse than refusing.
		if len(sc.repos) != 1 {
			return nil, fmt.Errorf("a Run ID names a Run in one repository: pass -R OWNER/REPO, or run inside the repository")
		}
		return runItemsFromIDs(sc.repos[0], args)
	}
	// R26's rule, applied to these verbs: the zero filter matches every Run, so acting on
	// all of them is asked for by name. Without it a bare `gh runs cancel --yes` would
	// cancel every running Run in scope.
	if !f.hasFilter() && !f.matchAll {
		return nil, fmt.Errorf("refusing to act on every Run: pass Run IDs, a filter (for example -s in_progress), or --all")
	}
	flt, err := buildFilter(&f.lf) // client-side validation before any request (R6)
	if err != nil {
		return nil, err
	}
	return deps.Purge.Crawl(ctx, sc.repos, flt)
}

// runItemsFromIDs turns positional arguments into a frozen set against one repository. The
// parse is client-side and before any request, the same rule R6 fixes for filter values: a
// typo costs nothing and says so, rather than becoming a 404 the failure contract has to
// interpret.
func runItemsFromIDs(repo domain.RepoID, args []string) ([]ops.Item, error) {
	items := make([]ops.Item, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid run ID %q: a Run ID is a positive integer", a)
		}
		items = append(items, ops.RunItem(domain.Run{Repo: repo, ID: id}))
	}
	return items, nil
}

// hasFilter reports whether any filter axis was set. The scope flags (-R, --all-repos) are
// not filters: they select repositories, not Runs.
func (f *lifecycleFlags) hasFilter() bool {
	l := f.lf
	return l.branch != "" || l.commit != "" || l.created != "" || l.event != "" ||
		l.status != "" || l.user != "" || l.workflow != ""
}

// printLifecycleDryRun reports exactly what would be affected: one row per Run, each naming
// its repository and Run ID, so grep and wc -l answer questions about it (cli-surface R10).
// It matters more here than for a delete, because a bulk re-run spends Actions minutes and
// this is the only way to see the size of that bill before paying it.
func printLifecycleDryRun(deps Deps, plan ops.Plan, op ops.Operation) error {
	for _, it := range plan.Items() {
		row := it.Repo.String() + "\t" + strconv.FormatInt(it.ID, 10)
		if it.Skip != ops.SkipNone {
			row += "\t(skipped: " + string(it.Skip) + ")"
		}
		_, _ = fmt.Fprintln(deps.Stdout, row)
	}
	note := fmt.Sprintf("gh-runs: dry run: %s would be requested for %d %s",
		op, plan.Total()-plan.Skipped(), plural(plan.Total()-plan.Skipped(), "run", "runs"))
	if plan.Skipped() > 0 {
		note += fmt.Sprintf(", %d skipped", plan.Skipped())
	}
	// The trailer names the write and not "no request", because the set was resolved by the
	// same crawl the real operation runs, so GETs did travel. --dry-run withholds the
	// mutation alone, which is the claim the delete trailer already makes about its DELETE.
	_, _ = fmt.Fprintln(deps.Stderr, note+" (no POST issued)")
	return nil
}

// plural picks the noun form for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// printLifecycleSummary reports what this pass did. It is deliberately not the Purge's
// summary: nothing here was deleted, so the counts that matter are the requests the API
// accepted and the Runs it declined to act on. R4's distinction is kept in the wording,
// because a 202 means the request was accepted and not that the Run has stopped.
func printLifecycleSummary(deps Deps, sum ops.Summary, op ops.Operation) {
	_, _ = fmt.Fprintf(deps.Stdout, "%s: %d accepted, %d skipped, %d failed of %d Runs.\n",
		op, sum.Acted, sum.Skipped, sum.FailedCount(), sum.Total)
	printGroups(deps, sum.Failures, "failed: ")
	// The skips carry the reasons, and for cancel one of them names force-cancel as the
	// escalation a 409 permits (R5, AC6). A count with no words would leave the operator of
	// a non-interactive cancel with nothing to act on.
	printGroups(deps, sum.Skips, "skipped: ")
	if sum.Reason != "" {
		_, _ = fmt.Fprintln(deps.Stdout, sum.Reason)
	}
}
