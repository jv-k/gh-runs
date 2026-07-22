package dispatch_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
)

// The goldens render the generated form from held state alone, at 100 columns, with no terminal and
// no network (R21, AC7). The form is generated from YAML the tool did not write and cannot predict,
// so the painted frame is the only evidence R6's type-per-control mapping and R10's select-not-free-
// text promise held. lipgloss v2 renders truecolour regardless of the environment, so these bytes
// are stable on any machine (ADR-0013). Regenerate with:
// go test ./internal/tui/dispatch/ -run Golden -update.

// goldenForm opens the form over a named Workflow and injects a fixture's YAML, returning a laid-out,
// loaded model. The cursor rests on the ref row, so the frame is the freshly opened form.
func goldenForm(t *testing.T, id int64, name, path, fixtureName string) dispatch.Model {
	t.Helper()
	m := dispatch.New(dispatch.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{
		Repo:     rid("o", "r"),
		Workflow: domain.Workflow{ID: id, Name: name, Path: path, State: domain.StateActive},
		Eligible: true,
		Ref:      "main",
	})
	m, _ = m.Update(dispatch.YAMLLoaded{Ref: "main", Path: path, Data: fixture(t, fixtureName)})
	return m
}

// TestGoldenDeploymentForm fixes AC7's first fixture: tag_name free text marked required, platforms
// free text pre-filled with its default, release and dry_run toggles each pre-filled true, and
// environment a select pre-filled production. Changing any control's type fails this golden.
func TestGoldenDeploymentForm(t *testing.T) {
	m := goldenForm(t, 9001, "Deployment", ".github/workflows/deploy.yml", "deployment.yml")
	goldie.New(t).Assert(t, "deployment_form", []byte(m.View()))
}

// TestGoldenChoiceNumberForm fixes AC7's second fixture: a choice rendered as a select over its
// declared options (R10), and a number rendered as a numeric entry (R6).
func TestGoldenChoiceNumberForm(t *testing.T) {
	m := goldenForm(t, 7001, "Bench", ".github/workflows/bench.yml", "choice_number.yml")
	goldie.New(t).Assert(t, "choice_number_form", []byte(m.View()))
}

// TestGoldenUnrecognizedForm fixes AC7's third fixture: an input whose declared type is unrecognised
// renders as free text labelled unrecognised, rather than blocking the Dispatch (R11).
func TestGoldenUnrecognizedForm(t *testing.T) {
	m := goldenForm(t, 6001, "Custom", ".github/workflows/custom.yml", "unrecognized.yml")
	goldie.New(t).Assert(t, "unrecognized_form", []byte(m.View()))
}
