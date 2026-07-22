package workflows_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

func writable(owner, name string) domain.Repo {
	return domain.Repo{ID: rid(owner, name), Permissions: domain.Permissions{Push: true}}
}

func workflow(id int64, name, path string, state domain.State, repo domain.RepoID) domain.Workflow {
	return domain.Workflow{ID: id, Name: name, Path: path, State: state, Repo: repo}
}

// press builds a single-key press, matching the Feed's helper so key.Matches resolves the
// same bindings (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
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

// newClient builds a ghclient over a transport (a cassette), for the fetch tests.
func newClient(t *testing.T, transport http.RoundTripper) workflows.Requester {
	t.Helper()
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: transport})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// newWorkflows builds a Workflows tab sized w x h, wired to the given toggler and fetch and
// the discovered repositories. Repos is a func so the gate can be populated by a refresh, as
// it is in production.
func newWorkflows(t *testing.T, w, h int, tog workflows.Toggler, fetch workflows.Fetch, repos ...domain.Repo) workflows.Model {
	t.Helper()
	rr := append([]domain.Repo(nil), repos...)
	m := workflows.New(workflows.Options{
		Profile: keys.Standard,
		Ops:     tog,
		Fetch:   fetch,
		Repos:   func() []domain.Repo { return rr },
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func fetched(m workflows.Model, rw workflows.RepoWorkflows) workflows.Model {
	m, _ = m.Update(workflows.WorkflowsFetched(rw))
	return m
}

// send routes a keystroke and discards the Cmd, for keys whose effect is synchronous (a
// refresh that only populates the gate, a cursor move).
func send(m workflows.Model, s string) workflows.Model {
	m, _ = m.Update(press(s))
	return m
}

// act routes a keystroke and drains the Cmd it returns, applying every message the Cmd (and
// any Cmd those messages produce) yields, so a toggle's whole chain runs: the toggle, its
// result, and the R8 re-read.
func act(t *testing.T, m workflows.Model, s string) workflows.Model {
	t.Helper()
	m, cmd := m.Update(press(s))
	return drain(t, m, cmd)
}

func drain(t *testing.T, m workflows.Model, cmd tea.Cmd) workflows.Model {
	t.Helper()
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		if msg == nil {
			return m
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = drain(t, m, c)
			}
			return m
		}
		m, cmd = m.Update(msg)
	}
	return m
}

// server models the API for the toggle tests: the toggler flips a Workflow's state and the
// fetch reads it back, so a test exercises R8 (reflect the API's reported state after a
// toggle) rather than an optimistic flip in the tab. It records the ops it was asked for.
type server struct {
	state domain.State
	calls []ops.Operation
}

func (s *server) fetch(id domain.RepoID) workflows.RepoWorkflows {
	return workflows.RepoWorkflows{
		Repo:      id,
		Complete:  true,
		Workflows: []domain.Workflow{workflow(9001, "CI", ".github/workflows/ci.yml", s.state, id)},
	}
}

func (s *server) ToggleWorkflow(_ context.Context, op ops.Operation, _ domain.Workflow, _ map[domain.RepoID]domain.Repo) (ops.Summary, error) {
	s.calls = append(s.calls, op)
	switch op {
	case ops.OpDisable:
		s.state = domain.StateDisabledManually
	case ops.OpEnable:
		s.state = domain.StateActive
	}
	return ops.Summary{Acted: 1}, nil
}

// stubToggler returns a fixed outcome and records the ops it was asked for, for the gate and
// failure tests where no state change is modelled.
type stubToggler struct {
	result ops.Summary
	err    error
	calls  []ops.Operation
}

func (s *stubToggler) ToggleWorkflow(_ context.Context, op ops.Operation, _ domain.Workflow, _ map[domain.RepoID]domain.Repo) (ops.Summary, error) {
	s.calls = append(s.calls, op)
	return s.result, s.err
}

// TestToggleChoosesTheOpFromState pins R5: the one toggle key disables an active Workflow and
// enables a disabled one, and R8: after an accepted toggle the tab re-reads the list and shows
// the API's reported state, not an optimistic flip. The server flips its own state and the
// fetch reads it back, so the displayed state follows the API.
func TestToggleChoosesTheOpFromState(t *testing.T) {
	srv := &server{state: domain.StateActive}
	m := newWorkflows(t, 100, 20, srv, srv.fetch, writable("o", "r"))
	m = act(t, m, "r") // fan out: fetch the active Workflow and populate the gate

	if !strings.Contains(m.View(), "active") {
		t.Fatalf("the list did not show the active Workflow before the toggle:\n%s", m.View())
	}

	m = act(t, m, "s") // toggle: an active Workflow offers disable (R5)
	if len(srv.calls) != 1 || srv.calls[0] != ops.OpDisable {
		t.Fatalf("toggling an active Workflow asked for %v, want one OpDisable (R5)", srv.calls)
	}
	// R8: the re-read reflects the API's reported state, which the server flipped to disabled.
	if !strings.Contains(m.View(), "disabled_manually") {
		t.Errorf("after disabling, the list did not reflect the API's reported state (R8):\n%s", m.View())
	}

	m = act(t, m, "s") // toggle again: a disabled Workflow offers enable (R5)
	if len(srv.calls) != 2 || srv.calls[1] != ops.OpEnable {
		t.Fatalf("toggling a disabled Workflow asked for %v, want an OpEnable second (R5)", srv.calls)
	}
	// R8: enabling re-reads active, so the disabled state is no longer shown anywhere.
	if strings.Contains(m.View(), "disabled_manually") {
		t.Errorf("after enabling, the list still showed the disabled state; R8 re-reads the API's active state:\n%s", m.View())
	}
}

// TestGateOffersNoToggleWithoutARequest pins R6 and AC2: for an archived or read-only
// repository the toggle key offers nothing and issues no command, so no request can follow,
// and the reason is stated on the row. Determining that reads only the discovered capability,
// which cost no request of its own.
func TestGateOffersNoToggleWithoutARequest(t *testing.T) {
	archived := domain.Repo{ID: rid("o", "arch"), Permissions: domain.Permissions{Push: true}, Archived: true}
	readonly := domain.Repo{ID: rid("o", "ro"), Permissions: domain.Permissions{Push: false}}

	cases := []struct {
		name string
		repo domain.Repo
		want string
	}{
		{"archived is never toggleable (R6)", archived, "archived"},
		{"read-only is never toggleable (R6)", readonly, "read-only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tog := &stubToggler{}
			m := newWorkflows(t, 100, 20, tog, nil, c.repo)
			m = fetched(m, workflows.RepoWorkflows{
				Repo:      c.repo.ID,
				Complete:  true,
				Workflows: []domain.Workflow{workflow(1, "CI", ".github/workflows/ci.yml", domain.StateActive, c.repo.ID)},
			})
			m = send(m, "r") // populate the gate; Fetch is nil, so no fan-out

			if _, cmd := m.Update(press("s")); cmd != nil {
				t.Errorf("the toggle key issued a command over a gated repository; it must offer nothing (R6, AC2)")
			}
			if len(tog.calls) != 0 {
				t.Errorf("a gated toggle reached the toggler; it must issue nothing (R6, AC2)")
			}
			if !strings.Contains(m.View(), c.want) {
				t.Errorf("the row did not state the gate reason %q (R6):\n%s", c.want, m.View())
			}
		})
	}
}

// TestDeletedWorkflowOffersNoToggle pins R9 and R11: a deleted Workflow offers neither enable
// nor disable, and its row labels its Runs as Orphaned. The toggle key issues no command.
func TestDeletedWorkflowOffersNoToggle(t *testing.T) {
	tog := &stubToggler{}
	m := newWorkflows(t, 100, 20, tog, nil, writable("o", "r"))
	m = fetched(m, workflows.RepoWorkflows{
		Repo:      rid("o", "r"),
		Complete:  true,
		Workflows: []domain.Workflow{workflow(1, "Old", ".github/workflows/old.yml", domain.StateDeleted, rid("o", "r"))},
	})
	m = send(m, "r")

	if _, cmd := m.Update(press("s")); cmd != nil {
		t.Errorf("the toggle key issued a command over a deleted Workflow; R9 offers neither action")
	}
	if len(tog.calls) != 0 {
		t.Errorf("a deleted Workflow's toggle reached the toggler; R9 forbids it")
	}
	if !strings.Contains(m.View(), "orphaned Runs") {
		t.Errorf("a deleted Workflow did not label its Runs as Orphaned (R11):\n%s", m.View())
	}
}

// TestFailedToggleSurfacesAndLeavesStateUnchanged pins R7 and AC3: a 403 on a toggle is
// reported as a permission failure and the displayed state is left unchanged, because R8
// re-reads only on an accepted toggle. The stub returns a failure carrying the API's reason.
func TestFailedToggleSurfacesAndLeavesStateUnchanged(t *testing.T) {
	tog := &stubToggler{result: ops.Summary{
		Failures: []ops.FailureGroup{{Reason: "HTTP 403: Resource not accessible by personal access token", Count: 1}},
	}}
	m := newWorkflows(t, 100, 20, tog, nil, writable("o", "r"))
	m = fetched(m, workflows.RepoWorkflows{
		Repo:      rid("o", "r"),
		Complete:  true,
		Workflows: []domain.Workflow{workflow(1, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("o", "r"))},
	})
	m = send(m, "r")

	m = act(t, m, "s") // the toggle is attempted and rejected
	if len(tog.calls) != 1 {
		t.Fatalf("an eligible toggle did not reach the toggler; calls = %v (R5)", tog.calls)
	}
	got := m.View()
	if !strings.Contains(got, "could not disable") || !strings.Contains(got, "403") {
		t.Errorf("a rejected toggle did not surface the permission failure (R7, AC3):\n%s", got)
	}
	// AC3: the displayed state is unchanged, so the disabled state the toggle would have set
	// never appears; only an accepted toggle re-reads (R8).
	if strings.Contains(got, "disabled_manually") {
		t.Errorf("a rejected toggle changed the displayed state; it must stay as the API last reported (AC3):\n%s", got)
	}
}

// TestToggleInertWithoutToggler pins the fail-closed default: with no toggler wired (a golden
// test, or before ops is available), the toggle key is inert and issues no command.
func TestToggleInertWithoutToggler(t *testing.T) {
	m := workflows.New(workflows.Options{Profile: keys.Standard, Repos: func() []domain.Repo { return []domain.Repo{writable("o", "r")} }})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = fetched(m, workflows.RepoWorkflows{
		Repo:      rid("o", "r"),
		Complete:  true,
		Workflows: []domain.Workflow{workflow(1, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("o", "r"))},
	})
	m = send(m, "r")
	if _, cmd := m.Update(press("s")); cmd != nil {
		t.Errorf("the toggle key issued a command with no toggler wired; it must be inert")
	}
}

// TestRefreshFansOutOverDiscoveredRepos pins R0: a refresh issues one Fetch per discovered
// repository, the fan-out that a cross-repo view opens with. The Fetch is a fake, so no
// network is touched; the assertion is the set it was asked for.
func TestRefreshFansOutOverDiscoveredRepos(t *testing.T) {
	var fetchedIDs []domain.RepoID
	fetch := func(id domain.RepoID) workflows.RepoWorkflows {
		fetchedIDs = append(fetchedIDs, id)
		return workflows.RepoWorkflows{Repo: id, Complete: true}
	}
	m := newWorkflows(t, 100, 20, nil, fetch, writable("cli", "cli"), writable("octo", "hello"))
	_, cmd := m.Update(press("r"))
	if cmd == nil {
		t.Fatalf("refresh issued no fan-out command (R0)")
	}
	drain(t, m, cmd)
	if len(fetchedIDs) != 2 {
		t.Errorf("the fan-out fetched %d repositories, want one per discovered repo (R0): %v", len(fetchedIDs), fetchedIDs)
	}
}

// TestStateVocabularyNeverSaysStatus pins R4 and AC6: the Workflows view names a Workflow's
// STATE and never its Status, because State belongs to a Workflow and Status to a Run.
func TestStateVocabularyNeverSaysStatus(t *testing.T) {
	m := newWorkflows(t, 100, 20, nil, nil, writable("o", "r"))
	m = fetched(m, workflows.RepoWorkflows{
		Repo:     rid("o", "r"),
		Complete: true,
		Workflows: []domain.Workflow{
			workflow(1, "CI", ".github/workflows/ci.yml", domain.StateActive, rid("o", "r")),
			workflow(2, "Rel", ".github/workflows/rel.yml", domain.StateDisabledManually, rid("o", "r")),
		},
	})
	if got := m.View(); strings.Contains(got, "Status") || strings.Contains(got, "status") {
		t.Errorf("the Workflows view used Run/Job vocabulary; a Workflow has a State, never a Status (R4, AC6):\n%s", got)
	}
}
