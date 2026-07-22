package ops

import (
	"context"
	"fmt"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// ToggleWorkflow enables or disables one Workflow through Execute, the single write path
// (ADR-0011, ADR-0019, workflow-management R5). It is a thin convenience over
// Plan then Confirm then Execute: it freezes the one Workflow into a Plan for op, Confirms
// it with NoInput because a reversible toggle prices at FrictionNone and shows no modal,
// and Executes it. It issues no request of its own, so "Execute is the only call that
// issues a write" still holds, and the two toggles are paced by the governor and travel
// the one mutation entry exactly as the other eight writes do (rate-governor R2). It spares
// the Workflows tab from orchestrating three calls inside a Cmd, and keeps that orchestration
// in the package that owns writes. repos is the eligibility snapshot Plan gates on, so an
// archived or read-only repository, or a deleted Workflow, is skipped with no request (R6,
// R9, AC2), and an unknown repository fails the Plan closed rather than guessing (ADR-0019).
// The returned Summary reports whether the toggle was Acted, Skipped or Failed, which the
// tab reads to reflect the API's reported state (R8) or to surface a permission failure (R7).
func (o *Ops) ToggleWorkflow(ctx context.Context, op Operation, wf domain.Workflow, repos map[domain.RepoID]domain.Repo) (Summary, error) {
	if op != OpEnable && op != OpDisable {
		return Summary{}, fmt.Errorf("ops: ToggleWorkflow expects OpEnable or OpDisable, got %q", op)
	}
	plan, err := o.Plan(op, []Item{WorkflowItem(wf)}, repos)
	if err != nil {
		return Summary{}, err
	}
	confirmed, err := o.Confirm(plan, NoInput())
	if err != nil {
		return Summary{}, err
	}
	return o.Execute(ctx, confirmed)
}
