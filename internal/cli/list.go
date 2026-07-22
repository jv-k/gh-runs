package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// listFlags holds the parsed flags of the list command, one field per gh flag
// (cli-surface R2). The flag names, shorthands and the -L default of 20 are gh's
// contract, verified against gh 2.92.0, not ours (R2, R3, Constraints).
type listFlags struct {
	all      bool // -a: include disabled workflows (interacts with -w, Constraints)
	branch   string
	commit   string
	created  string
	event    string
	jq       string
	json     string
	limit    int
	status   string
	template string
	user     string
	workflow string
	repo     string // -R [HOST/]OWNER/REPO
	allRepos bool   // --all-repos: force fan-out (R22, long form only, -a is taken)
}

// newListCmd builds the list command and its alias ls (cli-surface R3). The flag
// set is gh run list's exactly, with --conclusion deliberately absent: no
// server-side conclusion parameter exists, so cobra rejecting it as an unknown
// flag is the correct behaviour (R5, AC4). --all-repos and the repository JSON
// field are the surface's additions where gh has no precedent (R22, R24, ADR-0022).
func newListCmd(deps Deps) *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List workflow runs",
		Long: "List workflow runs across your repositories.\n\n" +
			"Outside a repository, with no -R, list fans out across every discovered\n" +
			"repository. Inside a repository it lists that repository, as gh does.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runList(deps, f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.all, "all", "a", false, "Include disabled workflows")
	fl.StringVarP(&f.branch, "branch", "b", "", "Filter runs by branch")
	fl.StringVarP(&f.commit, "commit", "c", "", "Filter runs by the SHA of the commit")
	fl.StringVar(&f.created, "created", "", "Filter runs by the date it was created")
	fl.StringVarP(&f.event, "event", "e", "", "Filter runs by which event triggered the run")
	fl.StringVarP(&f.jq, "jq", "q", "", "Filter JSON output using a jq `expression`")
	fl.StringVar(&f.json, "json", "", "Output JSON with the specified `fields`")
	fl.IntVarP(&f.limit, "limit", "L", 20, "Maximum number of runs to fetch")
	fl.StringVarP(&f.status, "status", "s", "", "Filter runs by status")
	fl.StringVarP(&f.template, "template", "t", "", "Format JSON output using a Go template")
	fl.StringVarP(&f.user, "user", "u", "", "Filter runs by user who triggered the run")
	fl.StringVarP(&f.workflow, "workflow", "w", "", "Filter runs by workflow")
	fl.StringVarP(&f.repo, "repo", "R", "", "Select another repository using the [HOST/]OWNER/REPO format")
	fl.BoolVar(&f.allRepos, "all-repos", false, "List runs across every discovered repository")
	return cmd
}

// runList is the read half's whole pipeline: validate every input client-side,
// resolve the scope, list through the chain, and render. Validation precedes the
// first request without exception, because the API answers a bad value with a
// full result set or a silent zero rather than an error, so a typo must be caught
// here or not at all (cli-surface R6, AC5). The scope's host check and the JSON
// field check are the same story: an unsupported host and an unknown field are
// both rejected before any request leaves (R9, R7, AC7).
func runList(deps Deps, f *listFlags) error {
	if f.limit < 1 {
		return fmt.Errorf("--limit must be a positive integer, got %d", f.limit)
	}
	// -q and -t operate over --json's output, exactly as they do in gh, so neither
	// is meaningful without it (cli-surface R7).
	if f.jq != "" && f.json == "" {
		return fmt.Errorf("cannot use --jq without --json")
	}
	if f.template != "" && f.json == "" {
		return fmt.Errorf("cannot use --template without --json")
	}

	flt, err := buildFilter(f)
	if err != nil {
		return err
	}
	fields, err := parseJSONFields(f.json)
	if err != nil {
		return err
	}
	sc, err := resolveScope(deps, f)
	if err != nil {
		return err
	}

	runs, err := listRuns(deps.Client, sc.repos, flt, f.limit)
	if err != nil {
		return err
	}
	return render(deps, f, sc, fields, runs)
}

// buildFilter is the thin adapter the whole feature turns on: it maps the flags
// onto a filter.Filter and lets the engine parse and validate (cli-surface R6,
// ADR-0016). It reimplements nothing. -s rides through filter.ParseStatus, which
// spans Status and Conclusion permissively and rejects a typo by name (R4, R6),
// and --created rides through filter.ParseCreated. It never sets a conclusion
// axis from a flag, because no --conclusion flag exists (R5).
func buildFilter(f *listFlags) (filter.Filter, error) {
	flt := filter.Filter{
		Branch:   f.branch,
		Commit:   f.commit,
		Actor:    f.user,
		Event:    f.event,
		Workflow: f.workflow,
	}
	if f.status != "" {
		if err := flt.ParseStatus(f.status); err != nil {
			return filter.Filter{}, err
		}
	}
	if f.created != "" {
		created, err := filter.ParseCreated(f.created)
		if err != nil {
			return filter.Filter{}, err
		}
		flt.Created = created
	}
	return flt, nil
}
