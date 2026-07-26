package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// lifecycleFlags holds the cancel and rerun commands' flags. It embeds listFlags for the
// same reason delete does: the filter axes and the scope flags parse and validate through
// exactly the same code as list, so a value is rejected by the same message wherever it
// arrives from (cli-surface R6, ADR-0016). Above that sit the two shared write flags, and
// the three that select which of the four operations is being asked for.
//
// force, failed and debug are gh's own spellings, verified against `gh run cancel --help`
// and `gh run rerun --help`: `--force`, `--failed` and `-d/--debug`. Compatibility is a
// stated requirement here (ADR-0008), so none of the three is renamed.
type lifecycleFlags struct {
	lf       listFlags
	matchAll bool // --all: act on every Run in scope, the zero filter asked for by name (R26)
	dryRun   bool // --dry-run: resolve and report, request nothing, exit 0 (R10)
	yes      bool // --yes: the non-interactive confirmation (R11)
	force    bool // --force: escalate a cancel to force-cancel (run-lifecycle R6)
	failed   bool // --failed: re-run only the failed Jobs (run-lifecycle R13)
	debug    bool // -d/--debug: enable_debug_logging on a re-run (run-lifecycle R14)
}

// newCancelCmd builds the cancel command (run-lifecycle R1, R4, R5, R6). It takes gh's
// shape, `gh run cancel [<run-id>] [--force]`, and adds this tool's filter-driven bulk
// form over the same frozen-set chain the Purge runs on (R16, R17).
func newCancelCmd(deps Deps) *cobra.Command {
	f := &lifecycleFlags{}
	cmd := &cobra.Command{
		Use:   "cancel [<run-id>...]",
		Short: "Cancel workflow runs",
		Long: "Cancel runs, named by id or matched by a filter, across one or more repositories.\n\n" +
			"Cancel is asynchronous: a request is accepted, and only a later poll shows whether\n" +
			"the Run stopped. A Run the API reports as not cancelable is skipped, and --force\n" +
			"sends force-cancel instead, which is a distinct operation and never a substitution.\n" +
			"Cancelling requires --yes. --dry-run reports what would be cancelled and requests\n" +
			"nothing.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			op := ops.OpCancel
			if f.force {
				op = ops.OpForceCancel
			}
			return runLifecycle(deps, f, op, args)
		},
	}
	bindLifecycleFlags(cmd, f, "Cancel")
	cmd.Flags().BoolVar(&f.force, "force", false, "Force cancel a workflow run")
	return cmd
}

// newRerunCmd builds the rerun command (run-lifecycle R8, R13, R14). A re-run adds an
// Attempt to the Run that already exists and never creates one, which is what the summary
// wording reports and what R8 calls the most confusable behaviour in the product.
//
// gh's `-j/--job` is deliberately absent: it re-runs one Job of a Run, and no Job-level
// operation exists in this tool's write engine. The gap is recorded in run-lifecycle's
// requirements rather than papered over with a flag that would silently re-run the whole
// Run instead.
func newRerunCmd(deps Deps) *cobra.Command {
	f := &lifecycleFlags{}
	cmd := &cobra.Command{
		Use:   "rerun [<run-id>...]",
		Short: "Rerun runs, or only their failed jobs",
		Long: "Re-run runs, named by id or matched by a filter, across one or more repositories.\n\n" +
			"A re-run adds an Attempt to the Run that already exists. It creates no Run, and the\n" +
			"prior Attempt's Jobs stop being served once the new one starts. Re-running a single\n" +
			"named Run takes no confirmation; a filter-driven re-run requires --yes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			op := ops.OpRerun
			if f.failed {
				op = ops.OpRerunFailed
			}
			return runLifecycle(deps, f, op, args)
		},
	}
	bindLifecycleFlags(cmd, f, "Re-run")
	cmd.Flags().BoolVar(&f.failed, "failed", false, "Rerun only failed jobs, including dependencies")
	cmd.Flags().BoolVarP(&f.debug, "debug", "d", false, "Rerun with debug logging")
	return cmd
}

// bindLifecycleFlags binds the axes both commands share. They are list's flags exactly
// (R6), plus delete's three write flags with the verb swapped into their help. --all has
// no shorthand, for the same reason it has none on delete: -a is taken by list's
// unrelated "include disabled workflows" (R26, Constraints).
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
	fl.BoolVar(&f.dryRun, "dry-run", false, "Report what would be acted on and request nothing")
	fl.BoolVar(&f.yes, "yes", false, "Confirm without prompting (required for a bulk operation)")
}

// runLifecycle is the write pipeline the four operations share, and it is delete's
// pipeline with one verb swapped: guard the blast radius, resolve the affected set, then
// either print it (--dry-run) or Confirm and Execute it over the same ops.Plan chain
// (ADR-0019, cli-surface R10, R11, R20). Interruption is wired to a context, so SIGINT
// stops the walk and exits 2 (R17, AC13).
func runLifecycle(deps Deps, f *lifecycleFlags, op ops.Operation, args []string) error {
	ids, err := parseRunIDs(args)
	if err != nil {
		return err
	}
	if err := guardLifecycle(f, op, ids); err != nil {
		return err
	}
	if deps.Purge == nil {
		return fmt.Errorf("the %s command is not available in this build", op)
	}

	flt, err := buildFilter(&f.lf) // client-side validation before any request (R6, AC5)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	items, err := resolveLifecycleSet(ctx, deps, sc, flt, ids)
	if err != nil {
		return err
	}
	plan, err := deps.Purge.Plan(op, items, snapshot)
	if err != nil {
		return namingTheExclusion(deps, sc.repos, err)
	}
	if f.debug {
		// R14, AC14: the opt-in rides on the Plan, so Execute sends enable_debug_logging on
		// the two re-run bodies and nowhere else. It is inert on a cancel Plan, which is why
		// the flag is bound on rerun alone.
		plan = plan.WithDebugLogging()
	}

	if f.dryRun {
		return printDryRun(deps, op, plan) // R10: report, request nothing, exit 0
	}

	confirmed, err := deps.Purge.Confirm(plan, lifecycleInput(plan, f))
	if err != nil {
		return err
	}
	sum, err := deps.Purge.Execute(ctx, confirmed)
	if err != nil {
		return err
	}
	printSummary(deps, op, sum)
	return exitFromSummary(op, sum)
}

// guardLifecycle refuses the shapes that must not reach the wire, before anything does.
//
// The zero filter matches every Run in scope, so acting on everything is asked for by
// name with --all, exactly as a Purge asks (cli-surface R26). Naming Run IDs is the other
// way to spell a set, and mixing the two is refused rather than resolved by precedence: a
// Run ID is an exact set and a filter is a query, and a command that mutates Runs must not
// leave which one won to a rule nobody reads.
//
// --yes is required on every shape but one: a single named Run re-run, which run-lifecycle
// R18 and AC11 exempt from confirmation because correcting a failed Run is the most common
// action and neither re-run destroys a Run. Every cancel takes it, which is R18's other
// half, and a filter-driven re-run takes it whatever it turns out to match, because a
// filter is a bulk shape and R17's bulk re-run still confirms.
func guardLifecycle(f *lifecycleFlags, op ops.Operation, ids []int64) error {
	if len(ids) > 0 && (f.hasFilter() || f.matchAll) {
		return fmt.Errorf("refusing to %s: pass Run IDs or a filter, not both", op)
	}
	if len(ids) == 0 && !f.hasFilter() && !f.matchAll {
		return fmt.Errorf("refusing to %s: name a Run ID, pass a filter (for example -s failure), or pass --all to act on every Run in scope", op)
	}
	if f.dryRun || f.yes {
		return nil
	}
	if singleRunRerun(op, ids) {
		return nil // R18, AC11: a single-Run re-run takes no confirmation
	}
	return fmt.Errorf("refusing to %s without --yes; pass --yes to confirm, or --dry-run to preview", op)
}

// singleRunRerun reports whether this invocation is the one shape run-lifecycle R18
// exempts from confirmation: a re-run or re-run-failed of exactly one named Run.
func singleRunRerun(op ops.Operation, ids []int64) bool {
	return (op == ops.OpRerun || op == ops.OpRerunFailed) && len(ids) == 1
}

// lifecycleInput is the explicit act this surface makes, chosen from the friction the Plan
// priced rather than from the flags (ADR-0019: the friction is a property of the returned
// value). A Plan priced at FrictionNone is owed nothing, and NoInput says exactly that:
// the surface collected nothing because nothing was due. Everything else carries --yes,
// which cli-surface R11 defines as this surface's confirmation, an explicit act made once
// per invocation and never a skip of one.
func lifecycleInput(plan ops.Plan, f *lifecycleFlags) ops.Input {
	if plan.Friction() == ops.FrictionNone && !f.yes {
		return ops.NoInput()
	}
	return ops.NonInteractiveYes()
}

// hasFilter reports whether any filter axis was set, which is R26's test for whether --all
// is required. The scope flags (-R, --all-repos) are not filters: they select
// repositories, not Runs.
func (f *lifecycleFlags) hasFilter() bool {
	l := f.lf
	return l.branch != "" || l.commit != "" || l.created != "" || l.event != "" ||
		l.status != "" || l.user != "" || l.workflow != ""
}

// parseRunIDs parses the positional arguments as Run IDs, gh's `gh run cancel <run-id>`
// shape. A non-numeric argument is rejected by name rather than treated as a filter.
func parseRunIDs(args []string) ([]int64, error) {
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a Run ID; pass a numeric id, or use the filter flags to select Runs", a)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// resolveLifecycleSet resolves the frozen set, by either of the two ways of naming one.
//
// Named ids are fetched one request each, against the single repository in scope. A bare
// id belongs to no repository in particular, so a fan-out is refused by naming the flag
// that resolves it rather than guessing at the first discovered repository. gh imposes the
// same rule for the same reason.
//
// A filter resolves through ops.Crawl, the one resolution --dry-run and the real operation
// share, which walks unfiltered past the silent 1,000 cap and matches client-side (R15,
// ADR-0005, cli-surface R20).
func resolveLifecycleSet(ctx context.Context, deps Deps, sc scope, flt filter.Filter, ids []int64) ([]ops.Item, error) {
	if len(ids) == 0 {
		return deps.Purge.Crawl(ctx, sc.repos, flt)
	}
	if sc.fanout || len(sc.repos) != 1 {
		return nil, fmt.Errorf("a Run ID names a Run in one repository: pass -R OWNER/REPO, set GH_REPO, or run inside the repository")
	}
	repo := sc.repos[0]
	items := make([]ops.Item, 0, len(ids))
	for _, id := range ids {
		run, err := fetchRun(deps.Client, repo, id)
		if err != nil {
			return nil, err
		}
		items = append(items, ops.RunItem(run))
	}
	return items, nil
}

// fetchRun reads one Run by id so a named-id invocation can freeze it into an Item. It is
// the one read the lifecycle commands add over delete's, and it is what `gh run cancel
// <id>` costs too.
//
// The Run arrives without its Workflow's State, which is stamped by the fan-out's join and
// not served on this payload (ADR-0014). So the Orphaned-Run skip ops.Plan applies to a
// re-run does not fire on this path, and the API decides instead, which R3 and R15 already
// make the authority: the tool does not pre-emptively reject a re-run, it surfaces the
// API's own reason if one is refused.
func fetchRun(client Requester, repo domain.RepoID, id int64) (domain.Run, error) {
	path := fmt.Sprintf("repos/%s/%s/actions/runs/%d", repo.Owner, repo.Name, id)
	run, err := getRun(client, path)
	if err != nil {
		return domain.Run{}, err
	}
	run.Repo = repo // stamp the owning repository the payload does not carry
	return run, nil
}
