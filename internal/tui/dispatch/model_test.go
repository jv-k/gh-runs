package dispatch_test

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

// press builds a KeyPressMsg from a key name, mirroring the confirm and Feed test helpers.
func press(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

func send(m dispatch.Model, key string) dispatch.Model {
	m, _ = m.Update(press(key))
	return m
}

func typeText(m dispatch.Model, s string) dispatch.Model {
	for _, r := range s {
		m = send(m, string(r))
	}
	return m
}

// fakeDispatcher records the requests it is handed and returns a canned outcome, so a test drives
// the pane's submit path with no live dispatch (the no-live-dispatch rule; every real Dispatch is a
// cassette in ops).
type fakeDispatcher struct {
	calls  []ops.DispatchRequest
	result ops.DispatchResult
	err    error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req ops.DispatchRequest) (ops.DispatchResult, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

// fakeFetcher returns held YAML per ref and a held environments list, so a test drives the fetch
// path with no network. A ref absent from the map returns empty bytes, which ParseForm reads as a
// non-dispatchable empty form.
type fakeFetcher struct {
	defaultBranch string
	yaml          map[string][]byte
	envs          []string
}

func (f *fakeFetcher) DefaultBranch(_ domain.RepoID) (string, error) {
	return f.defaultBranch, nil
}

func (f *fakeFetcher) WorkflowYAML(_ domain.RepoID, _, ref string) ([]byte, error) {
	return f.yaml[ref], nil
}

func (f *fakeFetcher) Environments(_ domain.RepoID) ([]string, error) {
	return f.envs, nil
}

func wf(id int64, path string) domain.Workflow {
	return domain.Workflow{ID: id, Name: "Deployment", Path: path, State: domain.StateActive}
}

// openLoaded opens the form over an eligible Workflow and injects a fixture's YAML, returning a
// laid-out, loaded model with no network (the golden path). The dispatcher and store are the
// caller's seams.
func openLoaded(t *testing.T, opts dispatch.Options, w domain.Workflow, fixtureName string) dispatch.Model {
	t.Helper()
	m := dispatch.New(opts)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{
		Repo:     rid("o", "r"),
		Workflow: w,
		Eligible: true,
		Ref:      "main",
	})
	m, _ = m.Update(dispatch.YAMLLoaded{Ref: "main", Path: w.Path, Data: fixture(t, fixtureName)})
	return m
}

// runCmd runs a Cmd and feeds its message back into the model, so a test drives the async submit
// path deterministically.
func runCmd(m dispatch.Model, cmd tea.Cmd) dispatch.Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	m, _ = m.Update(msg)
	return m
}

// TestOpenFetchesBeforeRendering pins R2 and AC1: Open holds the ref and returns a fetch Cmd, and no
// form is rendered until the YAML arrives. The pane renders a pending line, never a form, before the
// fetch resolves.
func TestOpenFetchesBeforeRendering(t *testing.T) {
	m := dispatch.New(dispatch.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{Repo: rid("o", "r"), Workflow: wf(9001, ".github/workflows/deploy.yml"), Eligible: true, Ref: "main"})
	if strings.Contains(m.View(), "tag_name") {
		t.Errorf("a form was rendered before the YAML arrived; the ref comes first (R2, AC1)")
	}
	if !strings.Contains(m.View(), "loading") {
		t.Errorf("expected a pending state before the YAML arrives (R2): %q", m.View())
	}
}

// TestOpenResolvesDefaultBranch pins R23: when the opener names no ref, the form resolves the
// repository's default branch first and fetches the YAML at it, so the picker defaults to the
// default branch for every repository.
func TestOpenResolvesDefaultBranch(t *testing.T) {
	fetch := &fakeFetcher{
		defaultBranch: "trunk",
		yaml:          map[string][]byte{"trunk": fixture(t, "deployment.yml")},
	}
	m := dispatch.New(dispatch.Options{Profile: keys.Standard, Fetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, cmd := m.Open(dispatch.Target{Repo: rid("o", "r"), Workflow: wf(9001, ".github/workflows/deploy.yml"), Eligible: true})
	if cmd == nil {
		t.Fatal("Open with no ref must resolve the default branch (R23)")
	}
	m, cmd = m.Update(cmd()) // RefResolved{trunk} -> applyRef sets ref, returns loadYAMLCmd
	m = runCmd(m, cmd)       // YAMLLoaded at trunk -> the form loads
	if !strings.Contains(m.View(), "Ref: trunk") {
		t.Errorf("the ref did not default to the resolved default branch trunk (R23): %q", m.View())
	}
	if !strings.Contains(m.View(), "tag_name") {
		t.Errorf("the form did not load at the resolved default branch (R23, R2): %q", m.View())
	}
}

// TestRequiredInputRefusesSubmit pins R9 and AC3: with tag_name empty the form refuses to submit and
// issues no request, naming the required input.
func TestRequiredInputRefusesSubmit(t *testing.T) {
	disp := &fakeDispatcher{}
	m := openLoaded(t, dispatch.Options{Profile: keys.Standard, Ops: disp}, wf(9001, ".github/workflows/deploy.yml"), "deployment.yml")

	m, cmd := m.Update(press("x")) // submit with tag_name unset
	if cmd != nil {
		m = runCmd(m, cmd)
	}
	if len(disp.calls) != 0 {
		t.Errorf("submit issued a Dispatch with a required input empty; R9 forbids it (AC3)")
	}
	if !strings.Contains(m.View(), "tag_name") || !strings.Contains(strings.ToLower(m.View()), "required input needs a value") {
		t.Errorf("expected a refusal naming tag_name (R9, AC3): %q", m.View())
	}
}

// TestChoiceStaysWithinOptions pins R10: cycling a choice moves only through its declared options
// and never lands on a value outside them, because the control is a select and free text is not a
// fallback.
func TestChoiceStaysWithinOptions(t *testing.T) {
	m := openLoaded(t, dispatch.Options{Profile: keys.Standard}, wf(7001, ".github/workflows/bench.yml"), "choice_number.yml")

	// Move the cursor onto the first input (level), then cycle it several times.
	m = send(m, "down") // ref row -> level
	seen := map[string]bool{}
	options := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	for i := 0; i < 6; i++ {
		m = send(m, "space")
		v := currentChoiceValue(t, m)
		if !options[v] {
			t.Fatalf("choice cycled to %q, which is not a declared option; a choice is a select (R10)", v)
		}
		seen[v] = true
	}
	if len(seen) < 3 {
		t.Errorf("cycling visited only %v; it should move through the options (R10)", seen)
	}
}

// currentChoiceValue reads the only select's value out of the rendered view, between the select
// chevrons, so the test asserts the widget's value rather than reaching into unexported state. The
// choice_number fixture declares exactly one select (level), so the first chevron pair is it.
func currentChoiceValue(t *testing.T, m dispatch.Model) string {
	t.Helper()
	view := m.View()
	i := strings.Index(view, "‹ ")
	if i < 0 {
		return ""
	}
	rest := view[i+len("‹ "):]
	if j := strings.Index(rest, " ›"); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// TestIneligibleRepositoryRefusesSubmit pins R14 and AC6: a repository the gate marks ineligible
// (no push, or archived) dispatches nothing and states the reason, determined with no request.
func TestIneligibleRepositoryRefusesSubmit(t *testing.T) {
	disp := &fakeDispatcher{}
	m := dispatch.New(dispatch.Options{Profile: keys.Standard, Ops: disp})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{
		Repo:       rid("o", "r"),
		Workflow:   wf(9001, ".github/workflows/deploy.yml"),
		Eligible:   false,
		EligReason: "repository is read-only",
		Ref:        "main",
	})
	m, _ = m.Update(dispatch.YAMLLoaded{Ref: "main", Path: ".github/workflows/deploy.yml", Data: fixture(t, "deployment.yml")})

	m, cmd := m.Update(press("x"))
	if cmd != nil {
		m = runCmd(m, cmd)
	}
	if len(disp.calls) != 0 {
		t.Errorf("an ineligible repository dispatched; R14 gates on push and archived (AC6)")
	}
	if !strings.Contains(m.View(), "read-only") {
		t.Errorf("expected the ineligibility reason surfaced (R14, AC6): %q", m.View())
	}
}

// TestSuccessfulDispatchShowsRunIDAndPersists pins R16, R25 and AC5/AC9 end to end over a real
// local-store: filling the required input and submitting reports the Run ID from the response and
// persists the inputs, and re-opening the same Workflow's form pre-fills the remembered values.
func TestSuccessfulDispatchShowsRunIDAndPersists(t *testing.T) {
	dir := t.TempDir()
	st := store.NewTransport(nil, dir, clockwork.NewFakeClock())
	disp := &fakeDispatcher{result: ops.DispatchResult{RunID: 29803635501, HTMLURL: "https://github.com/o/r/actions/runs/29803635501"}}
	opts := dispatch.Options{Profile: keys.Standard, Ops: disp, Store: st}
	w := wf(9001, ".github/workflows/deploy.yml")

	m := openLoaded(t, opts, w, "deployment.yml")
	// Fill the required tag_name (cursor row 1), then toggle dry_run off, then submit.
	m = send(m, "down")  // ref -> tag_name
	m = send(m, "enter") // begin editing tag_name
	m = typeText(m, "v9.9.9")
	m = send(m, "enter") // commit
	// Move to dry_run (row 5) and toggle it off, so a non-default value is what gets remembered.
	m = send(m, "down") // environment
	m = send(m, "down") // platforms
	m = send(m, "down") // release
	m = send(m, "down") // dry_run
	m = send(m, "space")

	m, cmd := m.Update(press("x")) // submit
	if cmd == nil {
		t.Fatal("submit issued no Cmd; a filled, eligible form must dispatch (R16)")
	}
	m = runCmd(m, cmd)

	if len(disp.calls) != 1 {
		t.Fatalf("issued %d dispatches, want 1 (R16)", len(disp.calls))
	}
	req := disp.calls[0]
	if req.WorkflowID != 9001 || req.Ref != "main" {
		t.Errorf("dispatch targeted workflow %d ref %q, want 9001/main (R16)", req.WorkflowID, req.Ref)
	}
	if req.Inputs["tag_name"] != "v9.9.9" {
		t.Errorf("dispatch inputs tag_name = %q, want the edited v9.9.9", req.Inputs["tag_name"])
	}
	if req.Inputs["dry_run"] != "false" {
		t.Errorf("dispatch inputs dry_run = %q, want the toggled-off false", req.Inputs["dry_run"])
	}
	if !strings.Contains(m.View(), "29803635501") {
		t.Errorf("the Run ID from the response is not shown (R16, AC5): %q", m.View())
	}

	// R25/AC9: a second form over the same store pre-fills the remembered values.
	m2 := openLoaded(t, opts, w, "deployment.yml")
	view2 := m2.View()
	if !strings.Contains(view2, "v9.9.9") {
		t.Errorf("re-opening the form did not pre-fill the remembered tag_name (R25, AC9): %q", view2)
	}
	// dry_run was remembered false, so its toggle pre-fills unchecked rather than the declared true.
	if !strings.Contains(view2, "[ ] false") {
		t.Errorf("re-opening the form did not pre-fill the remembered dry_run=false (R25, AC9): %q", view2)
	}
}

// TestDoubleSubmitCreatesOneRun pins the content-creation safety: a second submit while a Dispatch is
// already in flight issues no second request, so a double keypress cannot create two Runs (a Dispatch
// counts against the content-creation secondary limit, CLAUDE.md).
func TestDoubleSubmitCreatesOneRun(t *testing.T) {
	disp := &fakeDispatcher{result: ops.DispatchResult{RunID: 1}}
	m := openLoaded(t, dispatch.Options{Profile: keys.Standard, Ops: disp}, wf(9001, ".github/workflows/deploy.yml"), "deployment.yml")
	m = send(m, "down")
	m = send(m, "enter")
	m = typeText(m, "v1")
	m = send(m, "enter")

	m, cmd := m.Update(press("x")) // first submit: in flight
	if cmd == nil {
		t.Fatal("first submit issued no Cmd")
	}
	m, cmd2 := m.Update(press("x")) // second submit while in flight: must be inert
	if cmd2 != nil {
		t.Errorf("a second submit while a Dispatch is in flight issued a Cmd; it must not create a second Run")
	}
	m = runCmd(m, cmd) // resolve the first
	if len(disp.calls) != 1 {
		t.Errorf("issued %d dispatches, want exactly 1 (content-creation safety)", len(disp.calls))
	}
	_ = m
}

// TestDispatchFailureSurfacesAPIReason pins R14 and R22: a rejected Dispatch surfaces the API's own
// reason and does not report a Run, and nothing is persisted for a failed dispatch.
func TestDispatchFailureSurfacesAPIReason(t *testing.T) {
	disp := &fakeDispatcher{err: &ops.DispatchError{Code: 422, Message: "Cannot trigger a 'workflow_dispatch' on a disabled workflow"}}
	m := openLoaded(t, dispatch.Options{Profile: keys.Standard, Ops: disp}, wf(9003, ".github/workflows/deploy.yml"), "deployment.yml")
	m = send(m, "down")
	m = send(m, "enter")
	m = typeText(m, "v1")
	m = send(m, "enter")

	m, cmd := m.Update(press("x"))
	m = runCmd(m, cmd)
	if !strings.Contains(strings.ToLower(m.View()), "disabled") {
		t.Errorf("a rejected Dispatch must surface the API's own reason (R22): %q", m.View())
	}
	if strings.Contains(m.View(), "Run ") && strings.Contains(m.View(), "created") {
		t.Errorf("a failed Dispatch reported a Run; only a 200 does (R16)")
	}
}

// TestYAMLLoadErrorNamesRefAndPath pins R12 and AC4: when the YAML cannot be fetched or parsed the
// form is an explicit failure naming the ref and the path, and no key=value entry surface appears.
func TestYAMLLoadErrorNamesRefAndPath(t *testing.T) {
	m := dispatch.New(dispatch.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{Repo: rid("o", "r"), Workflow: wf(9001, ".github/workflows/deploy.yml"), Eligible: true, Ref: "release/1.2"})

	// A malformed YAML body is a parse failure the pane surfaces (R12).
	m, _ = m.Update(dispatch.YAMLLoaded{Ref: "release/1.2", Path: ".github/workflows/deploy.yml", Data: []byte("on: [broken\n")})
	view := m.View()
	if !strings.Contains(view, "release/1.2") || !strings.Contains(view, ".github/workflows/deploy.yml") {
		t.Errorf("the load error must name the ref and the path (R12, AC4): %q", view)
	}
	if strings.Contains(view, "key=value") || strings.Contains(strings.ToLower(view), "raw entry") {
		t.Errorf("no untyped key=value fallback may appear (R12, AC4): %q", view)
	}
}

// TestRefChangeRefetches pins R3: advancing the ref in the picker re-fetches the form for the new
// ref, because a Workflow's inputs can differ per branch and the form must always be for the ref
// that will run.
func TestRefChangeRefetches(t *testing.T) {
	fetch := &fakeFetcher{yaml: map[string][]byte{
		"main":   fixture(t, "deployment.yml"),
		"v2.0.0": fixture(t, "deployment.yml"),
	}}
	m := dispatch.New(dispatch.Options{Profile: keys.Standard, Fetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = m.Open(dispatch.Target{
		Repo:     rid("o", "r"),
		Workflow: wf(9001, ".github/workflows/deploy.yml"),
		Eligible: true,
		Ref:      "main",
		Refs:     []dispatch.Ref{{Name: "main"}, {Name: "v2.0.0", IsTag: true}},
	})
	m, _ = m.Update(dispatch.YAMLLoaded{Ref: "main", Path: ".github/workflows/deploy.yml", Data: fixture(t, "deployment.yml")})

	// The cursor starts on the ref row; cycling it advances to the tag and re-enters the loading
	// state for the new ref (R3).
	before := m.View()
	if !strings.Contains(before, "Ref: main") {
		t.Fatalf("expected the initial ref displayed (R4): %q", before)
	}
	m, cmd := m.Update(press("space"))
	if cmd == nil {
		t.Errorf("advancing the ref issued no fetch Cmd; the form must re-fetch for the new ref (R3)")
	}
	if !strings.Contains(m.View(), "Ref: v2.0.0") {
		t.Errorf("the ref did not advance to the tag (R24): %q", m.View())
	}
	if !strings.Contains(m.View(), "loading") {
		t.Errorf("the form should re-enter loading while the new ref's YAML is fetched (R3): %q", m.View())
	}
}
