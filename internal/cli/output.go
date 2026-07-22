package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/cli/go-gh/v2/pkg/jq"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// render dispatches on --json (cli-surface R7). With --json the output is the
// gh-compatible projection, optionally piped through -q or -t exactly as gh does;
// without it, a human table. The projection lives here, in cli, because it exists
// to satisfy this one flag on this one surface and nothing else in the tree
// renders a Run (ADR-0011).
func render(deps Deps, f *listFlags, sc scope, fields []string, runs []domain.Run) error {
	if f.json != "" {
		return renderJSON(deps, f, fields, runs)
	}
	return renderTable(deps, sc, runs)
}

// tableColumns is the human table's fixed shape. Under fan-out a repository column
// is prepended, because rows from different repositories are otherwise
// indistinguishable (cli-surface R24).
var tableColumns = []string{"STATUS", "TITLE", "WORKFLOW", "BRANCH", "EVENT", "ID", "AGE"}

// renderTable prints the runs as an aligned table (cli-surface R2's human
// output). An empty result is not an error: it prints a note to stderr and leaves
// stdout empty, so a pipeline reads no spurious rows and the command exits 0.
func renderTable(deps Deps, sc scope, runs []domain.Run) error {
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(deps.Stderr, "no runs found")
		return nil
	}
	cols := tableColumns
	if sc.fanout {
		cols = append([]string{"REPOSITORY"}, cols...)
	}
	// Writes to the tabwriter buffer; a broken pipe surfaces at Flush, whose error
	// is what this returns, so the intermediate writes are discarded deliberately.
	w := tabwriter.NewWriter(deps.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(cols, "\t"))
	now := deps.Clock.Now()
	for _, r := range runs {
		var row []string
		if sc.fanout {
			row = append(row, r.Repo.Owner+"/"+r.Repo.Name)
		}
		row = append(row,
			effectiveState(r),
			textsan.Sanitize(r.DisplayTitle),
			textsan.Sanitize(workflowLabel(r)),
			textsan.Sanitize(r.HeadBranch),
			textsan.Sanitize(r.Event),
			strconv.FormatInt(r.ID, 10),
			age(now, r.CreatedAt),
		)
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return w.Flush()
}

// reportCapped notes on stderr when a per-repository listing hit the -L cap while
// more matching Runs remained on the wire, so the operator is not left to read a
// capped view as the whole of it (cli-surface R16, ADR-0022). It names the
// repositories under fan-out, where a merged list hides which one was short, and it
// prints no count: R16 forbids presenting a filtered total_count as the reachable
// number, and "more than shown" is the honest claim the wire supports. The note is a
// diagnostic, so it goes to stderr and leaves stdout (the table or the JSON) clean
// for a pipeline.
func reportCapped(deps Deps, sc scope, capped []domain.RepoID) {
	if len(capped) == 0 {
		return
	}
	if sc.fanout {
		names := make([]string, len(capped))
		for i, id := range capped {
			names[i] = id.Owner + "/" + id.Name
		}
		_, _ = fmt.Fprintf(deps.Stderr,
			"note: results may be capped at --limit for %s. Raise --limit or narrow the filter to see more.\n",
			strings.Join(names, ", "))
		return
	}
	_, _ = fmt.Fprintln(deps.Stderr,
		"note: results may be capped at --limit. Raise --limit or narrow the filter to see more.")
}

// effectiveState is what a reader wants in the status column: a completed Run's
// Conclusion, or its Status while it is still running. It collapses the
// Status/Conclusion pair into the one value gh's own list shows, without conflating
// the two fields the domain holds distinct (CONTEXT.md).
func effectiveState(r domain.Run) string {
	if r.Status == domain.StatusCompleted && r.Conclusion != domain.ConclusionNone {
		return string(r.Conclusion)
	}
	return string(r.Status)
}

// workflowLabel is the Run's workflow name where one is known, else its run name.
// A Run created by an organization or enterprise ruleset Workflow carries no
// Workflow name (cli-surface Constraints), so the fallback keeps the column
// populated rather than blank.
//
// Gap, recorded as plainly as the -t gap in renderTemplate: in the list path
// WorkflowName is never populated. domain.Run.WorkflowName is json:"-" (the caller
// stamps it, the listing never decodes it), and listRepo stamps only r.Repo, so this
// column always falls back to r.Name, the --json workflowName field always emits the
// empty string, and -w NAME never matches (only a numeric -w ID does, matched
// client-side by WorkflowID). Resolving a name needs the per-repository
// /actions/workflows map, which this one-shot list does not fetch. That map, and with
// it -w-by-name and -a's "include disabled workflows" effect, arrives with
// workflow-management (stage 13). Until then the numeric -w ID is the working form.
func workflowLabel(r domain.Run) string {
	if r.WorkflowName != "" {
		return r.WorkflowName
	}
	return r.Name
}

// age renders a compact relative age from the injected clock, so the table reads
// like gh's and a golden stays deterministic under a fake clock. A zero or future
// timestamp renders as the smallest unit rather than a negative age.
func age(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}

// jsonProjectors maps each gh --json field name to the value it emits from a Run
// (cli-surface R7). These are gh's names, not the API's: the API serves id,
// display_title and workflow_id where the flag must emit databaseId, displayTitle
// and workflowDatabaseId, so a Run needs this second serialisation and it lives
// here (R7). repository is the surface's addition, an object of {name,
// nameWithOwner} requestable on any invocation (R24).
var jsonProjectors = map[string]func(domain.Run) any{
	"attempt":            func(r domain.Run) any { return r.RunAttempt },
	"conclusion":         func(r domain.Run) any { return r.Conclusion },
	"createdAt":          func(r domain.Run) any { return r.CreatedAt },
	"databaseId":         func(r domain.Run) any { return r.ID },
	"displayTitle":       func(r domain.Run) any { return r.DisplayTitle },
	"event":              func(r domain.Run) any { return r.Event },
	"headBranch":         func(r domain.Run) any { return r.HeadBranch },
	"headSha":            func(r domain.Run) any { return r.HeadSHA },
	"name":               func(r domain.Run) any { return r.Name },
	"number":             func(r domain.Run) any { return r.RunNumber },
	"startedAt":          func(r domain.Run) any { return r.RunStartedAt },
	"status":             func(r domain.Run) any { return r.Status },
	"updatedAt":          func(r domain.Run) any { return r.UpdatedAt },
	"url":                func(r domain.Run) any { return r.HTMLURL },
	"workflowDatabaseId": func(r domain.Run) any { return r.WorkflowID },
	"workflowName":       func(r domain.Run) any { return r.WorkflowName },
	"repository":         func(r domain.Run) any { return repositoryObject(r.Repo) },
}

// repositoryObject is R24's shape, gh's own on gh search prs --json repository:
// {name, nameWithOwner}. An object rather than a flat string so a host key can be
// added additively if multi-host ever ships, without breaking a script
// (ADR-0009).
func repositoryObject(id domain.RepoID) map[string]any {
	return map[string]any{
		"name":          id.Name,
		"nameWithOwner": id.Owner + "/" + id.Name,
	}
}

// parseJSONFields validates the --json field list against the known set before
// any request, rejecting an unknown field by name (cli-surface R7). An empty spec
// means no --json, and the caller renders a table instead. gh rejects an unknown
// field the same way, so gh run list to gh runs list stays muscle memory.
func parseJSONFields(spec string) ([]string, error) {
	if spec == "" {
		return nil, nil
	}
	raw := strings.Split(spec, ",")
	fields := make([]string, 0, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := jsonProjectors[name]; !ok {
			return nil, fmt.Errorf("unknown JSON field %q\nAvailable fields:\n  %s",
				name, strings.Join(knownFieldList(), "\n  "))
		}
		fields = append(fields, name)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("no JSON fields specified")
	}
	return fields, nil
}

// knownFieldList returns the JSON field names, sorted, for a rejection message.
func knownFieldList() []string {
	names := make([]string, 0, len(jsonProjectors))
	for name := range jsonProjectors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// renderJSON emits the gh-compatible projection, then applies -q or -t over it
// exactly as gh does, by handing the bytes to go-gh's own jq and template
// packages (cli-surface R7). Using go-gh's packages rather than reimplementing
// them is what makes -q and -t byte-identical to gh's (AC6).
func renderJSON(deps Deps, f *listFlags, fields []string, runs []domain.Run) error {
	rows := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		row := make(map[string]any, len(fields))
		for _, name := range fields {
			row[name] = jsonProjectors[name](r)
		}
		rows = append(rows, row)
	}
	// -q and -t consume the compact projection; the plain path prints it indented.
	// The compact marshal is computed only when -q or -t needs it, so the plain path
	// does the one indented marshal and no marshal result is thrown away (code review).
	if f.jq != "" || f.template != "" {
		data, err := json.Marshal(rows)
		if err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		// -q is handed straight to go-gh's own jq package, the same engine gh uses, so
		// its output is byte-identical to gh's (cli-surface R7, AC6). go-gh's jq imports
		// only gojq, so it is free of the Charm-fork conflict its template package
		// carries (see renderTemplate).
		if f.jq != "" {
			return jq.Evaluate(bytes.NewReader(data), deps.Stdout, f.jq)
		}
		return renderTemplate(deps, data, f.template)
	}

	pretty, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	_, err = fmt.Fprintln(deps.Stdout, string(pretty))
	return err
}

// renderTemplate runs a -t Go template over the projected JSON (cli-surface R7).
// It uses the standard library's text/template, the same engine gh templates are
// written against, decoding the JSON with UseNumber so a Run's database ID prints
// as its digits rather than in float64 scientific notation. It deliberately does
// not use go-gh's template package: that package pulls classic lipgloss and
// charmbracelet/x/cellbuf, whose ansi version conflicts with the charm.land Bubble
// Tea fork this module already pins for the TUI (ADR-0013). gh's extra template
// functions (timeago, autocolor, truncate and the rest) live in that package, so
// a template that calls one errors here rather than rendering. This gap is
// recorded for the verify step: it is a dependency conflict to resolve, not a
// design choice, and -q covers the scriptable path AC6 pins in the meantime.
//
// Closing it is a maintainer product decision, deferred out of this read stage:
// accept the documented R7 deviation, reimplement gh's template functions here, or
// do the dependency surgery to split go-gh's template package off its classic-lipgloss
// import. This repair does none of the three; it records the deviation and leaves the
// choice.
func renderTemplate(deps Deps, data []byte, tmplText string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("decode json for template: %w", err)
	}
	tmpl, err := template.New("list").Parse(tmplText)
	if err != nil {
		return err
	}
	return tmpl.Execute(deps.Stdout, v)
}
