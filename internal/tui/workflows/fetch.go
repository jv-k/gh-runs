package workflows

import "github.com/jv-k/gh-runs/v2/internal/workflowlist"

// The reader itself lives in internal/workflowlist, not here (issue #95). Nothing in it was
// tab-shaped: it holds no model, paints nothing and knows no key binding, and three surfaces
// want it. The scheduler joins the list against each Run's workflow_id (run-detail R8) and
// cli needs it for -w NAME, and cli may never import a tab, so the reader had to leave before
// either could reach it without main.go handing this package's constructor down. What stays
// here is what a tab owns: the model, the message below, and the rendering.
//
// Fetch and RepoWorkflows are aliases rather than declarations of their own, so the tab reads
// in its own vocabulary while there is exactly one type behind each name. A second set of
// declarations would need a conversion at every seam, which is what the reader's move was
// meant to remove.
type (
	// Fetch reads one repository's Workflows. The tab issues one per in-scope repository (R0).
	Fetch = workflowlist.Fetch
	// RepoWorkflows is one repository's Workflows as the tab holds them, carrying any error
	// rather than failing the fan-out (R7).
	RepoWorkflows = workflowlist.RepoWorkflows
)

// WorkflowsFetched carries one repository's freshly fetched Workflow list, replacing its held
// slice wholesale, exactly as the Feed replaces a repository's Runs on a RunsFetched
// (ADR-0015). Under all-repos the fan-out emits one per repository; a golden test injects them
// directly, with no network. A toggle re-reads one repository this way to reflect the API's
// reported state (R8). It is a defined type and not an alias, because it is this tab's message
// and the root routes it by type.
type WorkflowsFetched RepoWorkflows
