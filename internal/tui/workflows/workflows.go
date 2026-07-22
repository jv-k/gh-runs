// Package workflows is the Workflows tab (workflow-management R0 to R11). It lists the
// Workflows across the discovered repositories with their state, and enables or disables one
// from the cursor. Because Runs outlive the Workflow that produced them, this surface is the
// only honest source of Orphaned Runs, the deleted Workflows whose Runs persist forever (R11,
// R12).
//
// A tab is not a tea.Model. It exposes View() string and an Update the root drives, and only
// the root implements tea.Model (ADR-0011's tab contract). The root routes a tea.KeyPressMsg
// here only when this tab is focused, and broadcasts size and data here always. A Workflow
// toggle is reversible, so the workflow-management canon asks for no graduated confirmation
// (R5, R8): this tab imports no confirm pane, and the toggle travels ops.ToggleWorkflow, the
// convenience over Plan then Confirm then Execute that keeps enable and disable on the one
// write path (ADR-0011, ADR-0019). It never imports another tab.
//
// It renders to a frame from held state alone, with no live terminal and no network, which is
// what makes the goldens cheap. The list is loaded and refreshed deliberately rather than
// polled (ADR-0004, resolved), and R8 re-reads the state after a toggle rather than flipping
// the displayed value optimistically: what the API says the state is, is what the list shows.
package workflows

import (
	"context"
	"sort"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
)

// Toggler enables or disables one Workflow through ops's single write path (R5). *ops.Ops
// satisfies it via ToggleWorkflow. It is a narrow interface so the tab depends on the one
// call it makes, and a golden test with no toggler leaves it nil, where the toggle key is
// inert (the mutation stays disabled until the toggler and the discovered capability are
// wired, repo-discovery R8's spirit).
type Toggler interface {
	ToggleWorkflow(ctx context.Context, op ops.Operation, wf domain.Workflow, repos map[domain.RepoID]domain.Repo) (ops.Summary, error)
}

// Options carries the tab's construction seams. main.go fills them: the profile is the
// resolved keybinding set (R7a), Fetch reads one repository's Workflow list over the shared
// client, Repos is the discovered capability data the gate reads (R6) and the fan-out covers
// (R0), and Ops toggles one Workflow through the shared write engine (R5). A golden test
// leaves Fetch and Ops nil, where the tab renders held state and the toggle key is inert.
type Options struct {
	Profile keys.Profile
	Fetch   Fetch
	Repos   func() []domain.Repo
	Ops     Toggler

	// The dispatch pane's seams, opened over the Workflow under the cursor (workflow-dispatch R2).
	// DispatchFetch reads the YAML at a ref and the environments, DispatchOps triggers the
	// workflow_dispatch through the shared write engine, and DispatchStore remembers last-used
	// inputs (R5, R7, R16, R25). All three are nil in a golden test, where the pane stays closed.
	DispatchFetch dispatch.Fetcher
	DispatchOps   dispatch.Dispatcher
	DispatchStore dispatch.DocStore
}

// toggleResultMsg carries one toggle's outcome back into the message loop. On an accepted
// toggle the tab re-reads the affected repository to reflect the API's reported state (R8);
// on a rejection it surfaces the failure and leaves the displayed state unchanged (R7, AC3).
type toggleResultMsg struct {
	repo domain.RepoID
	op   ops.Operation
	wf   domain.Workflow
	sum  ops.Summary
	err  error
}

// Model is the Workflows tab's state. The workflows map is the truth per repository, keyed by
// RepoID.String() and replaced wholesale as each WorkflowsFetched arrives; order keeps the
// repositories in a stable rollup order. The merged list is computed from held state on
// demand, so a refresh never reorders beneath the cursor between its request and its result.
type Model struct {
	width, height int
	active        bool

	profile keys.Profile
	fetch   Fetch
	repos   func() []domain.Repo
	toggler Toggler

	// dispatch is the workflow_dispatch form pane, opened over the Workflow under the cursor and
	// closed by the pane itself (workflow-dispatch R2). A tab may import a pane (ADR-0011).
	dispatch dispatch.Model

	workflows map[string][]domain.Workflow
	complete  map[string]bool
	errs      map[string]error
	order     []domain.RepoID

	// capability is the eligibility gate's data, keyed by RepoID.String(), pulled from the
	// Repos seam on refresh (R6). A repository absent from it makes a toggle fail closed.
	capability map[string]domain.Repo

	cursor int
	top    int

	// status is the last toggle's outcome, surfaced to the operator (R7, AC3): the state the
	// API reflected, or the permission failure it rejected the toggle with. statusErr colours
	// it as a failure. Both are cleared when a fresh refresh or toggle begins.
	status    string
	statusErr bool
}

// New returns a Workflows tab over opts. It holds no Workflows until the first
// WorkflowsFetched arrives, and paints an empty view until then.
func New(opts Options) Model {
	return Model{
		profile: opts.Profile,
		fetch:   opts.Fetch,
		repos:   opts.Repos,
		toggler: opts.Ops,
		dispatch: dispatch.New(dispatch.Options{
			Profile: opts.Profile,
			Fetch:   opts.DispatchFetch,
			Ops:     opts.DispatchOps,
			Store:   opts.DispatchStore,
		}),
		workflows:  make(map[string][]domain.Workflow),
		complete:   make(map[string]bool),
		errs:       make(map[string]error),
		capability: make(map[string]domain.Repo),
	}
}

// SetActive records whether this tab is focused. The list is loaded and refreshed
// deliberately rather than polled (ADR-0004), so gaining focus does not fetch: the operator
// presses the refresh key to load, which the empty view names. That keeps a tab switch from
// spending a burst of Budget nobody asked for, and keeps SetActive a pure state change
// matching the other tabs (ADR-0011).
func (m Model) SetActive(active bool) Model {
	m.active = active
	return m
}

// CapturesInput reports whether this tab holds input focus. It does while the dispatch form is
// open, because that form has free-text fields and a submit key: a digit typed into a number
// field, or x pressed to dispatch, must be the form's rather than a tab switch or a quit
// (workflow-dispatch R9, R16). The reversible Workflow toggle still opens no modal, so with the
// form closed the root's global keys are live over this tab (R5).
func (m Model) CapturesInput() bool { return m.dispatch.IsOpen() }

// Update handles one message the root routed here. Size and data broadcasts reach it whether
// or not it is focused (ADR-0011); a key reaches it only when focused. While the dispatch form is
// open a key is the form's, and the form's own tagged messages (the ref resolution, the YAML
// load, the environments, the Dispatch outcome) reach the tab as broadcasts and are forwarded to
// the pane, which discards them when closed (ADR-0015).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		var cmd tea.Cmd
		m.dispatch, cmd = m.dispatch.Update(msg) // keep the pane laid out even while closed
		return m, cmd

	case WorkflowsFetched:
		m.applyFetched(RepoWorkflows(msg))
		return m, nil

	case toggleResultMsg:
		return m.applyToggle(msg)

	case tea.KeyPressMsg:
		if m.dispatch.IsOpen() {
			var cmd tea.Cmd
			m.dispatch, cmd = m.dispatch.Update(msg)
			return m, cmd
		}
		return m.handleKey(msg)
	}
	// A message the tab does not consume is forwarded to the dispatch pane, which names its own.
	var cmd tea.Cmd
	m.dispatch, cmd = m.dispatch.Update(msg)
	return m, cmd
}

// applyFetched replaces one repository's held Workflow list wholesale and records it in the
// rollup order on first sight. It also clamps the cursor, because a fetch can change how many
// rows the list has.
func (m *Model) applyFetched(rw RepoWorkflows) {
	k := rw.Repo.String()
	m.order = appendOnce(m.order, rw.Repo)
	if rw.Err != nil {
		m.errs[k] = rw.Err
		m.clampCursor()
		return
	}
	delete(m.errs, k)
	m.workflows[k] = rw.Workflows
	m.complete[k] = rw.Complete
	m.clampCursor()
}

// appendOnce records a repository in the rollup order the first time it is seen, so an error
// followed by a successful fetch does not list it twice.
func appendOnce(order []domain.RepoID, id domain.RepoID) []domain.RepoID {
	for _, o := range order {
		if o == id {
			return order
		}
	}
	return append(order, id)
}

// handleKey matches a press against the active profile's registry bindings, never a key
// literal of its own (R7a, AC18).
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.ToggleWorkflow):
		return m.startToggle()
	case key.Matches(k, m.profile.Dispatch):
		return m.openDispatch()
	case key.Matches(k, m.profile.Refresh):
		return m.startFetch()
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
	case key.Matches(k, m.profile.PageUp):
		m.moveCursor(-m.pageSize())
	case key.Matches(k, m.profile.PageDown):
		m.moveCursor(m.pageSize())
	case key.Matches(k, m.profile.FirstRow):
		m.cursor = 0
		m.scrollToCursor()
	case key.Matches(k, m.profile.LastRow):
		m.cursor = len(m.displayRows()) - 1
		m.clampCursor()
		m.scrollToCursor()
	}
	return m, nil
}

// startFetch issues one Fetch per in-scope repository, each returning a WorkflowsFetched the
// tab accumulates (R0's fan-out, ADR-0003). With no Fetch wired it is a no-op, which is the
// golden path. The in-scope set is the discovered capability data, which also gates
// eligibility (R6), so the pull refreshes both.
func (m Model) startFetch() (Model, tea.Cmd) {
	m.status, m.statusErr = "", false
	if m.repos != nil {
		m.capability = make(map[string]domain.Repo)
		for _, r := range m.repos() {
			m.capability[r.ID.String()] = r
		}
	}
	if m.fetch == nil {
		return m, nil
	}
	scope := m.scopeRepos()
	if len(scope) == 0 {
		return m, nil
	}
	fetch := m.fetch
	cmds := make([]tea.Cmd, 0, len(scope))
	for _, id := range scope {
		id := id
		cmds = append(cmds, func() tea.Msg { return WorkflowsFetched(fetch(id)) })
	}
	return m, tea.Batch(cmds...)
}

// scopeRepos is the set the fan-out covers: the discovered repositories under all-repos (R0).
// It reads the capability map the refresh populated, so the fan-out and the gate cover the
// same set. this-repo resolves to one repository via [settings] R19, which owns the scope
// setting; until that lands the tab covers the discovered set, the R0 default.
func (m Model) scopeRepos() []domain.RepoID {
	out := make([]domain.RepoID, 0, len(m.capability))
	for _, r := range m.capability {
		out = append(out, r.ID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// startToggle enables or disables the Workflow under the cursor through ops (R5). It fails
// closed: with no toggler, no row, an ineligible repository (R6), or a deleted or
// unrecognised state (R9), it offers nothing and issues no request, which is the gate that
// costs no API call (AC2). The op is chosen from the row's own State, so the one key disables
// an active Workflow and enables a disabled one, exactly what R5 describes.
func (m Model) startToggle() (Model, tea.Cmd) {
	if m.toggler == nil {
		return m, nil
	}
	row, ok := m.rowUnderCursor()
	if !ok {
		return m, nil
	}
	act := m.actionFor(row)
	if !act.offered {
		return m, nil // R6, R9: no action offered, so none is issued (AC2)
	}
	toggler := m.toggler
	repos := m.repoSnapshot()
	wf, repo, op := row.wf, row.repo, act.op
	m.status, m.statusErr = "", false
	return m, func() tea.Msg {
		sum, err := toggler.ToggleWorkflow(context.Background(), op, wf, repos)
		return toggleResultMsg{repo: repo, op: op, wf: wf, sum: sum, err: err}
	}
}

// openDispatch opens the workflow_dispatch form over the Workflow under the cursor, applying the
// gates that cost no request (workflow-dispatch R14, R15, R22, AC6). A deleted Workflow offers no
// Dispatch, because its id 404s whatever ref it names (R15). A disabled Workflow is enabled first
// rather than dispatched, because a Dispatch to it is rejected with 422 (R22). An archived or
// read-only repository is ineligible under R14's push gate, determined from the discovered
// capability with no API request. Only an eligible, active Workflow opens the form, which then
// resolves the default branch (R23) and fetches its YAML (R5).
func (m Model) openDispatch() (Model, tea.Cmd) {
	row, ok := m.rowUnderCursor()
	if !ok {
		return m, nil
	}
	if row.wf.State == domain.StateDeleted {
		m.status, m.statusErr = "cannot dispatch a deleted Workflow, its Runs are orphaned", true
		return m, nil
	}
	if isDisabledState(row.wf.State) {
		m.status, m.statusErr = "enable this Workflow first ("+m.profile.ToggleWorkflow.Help().Key+"), then dispatch", true
		return m, nil
	}
	repo, known := m.capability[row.repo.String()]
	if !known || !repo.Permissions.Push || repo.Archived {
		m.status, m.statusErr = "dispatch unavailable: "+ineligibleReason(repo, known), true
		return m, nil
	}
	m.status, m.statusErr = "", false
	var cmd tea.Cmd
	m.dispatch, cmd = m.dispatch.Open(dispatch.Target{
		Repo:     row.repo,
		Workflow: row.wf,
		Eligible: true,
	})
	return m, cmd
}

// isDisabledState reports whether a Workflow is in any disabled state, which R22 requires be
// enabled before a Dispatch rather than dispatched directly.
func isDisabledState(s domain.State) bool {
	return s == domain.StateDisabledManually || s == domain.StateDisabledInactivity || s == domain.StateDisabledFork
}

// ineligibleReason states why a repository is ineligible for a Dispatch under R14's push gate,
// distinguishing archived (permanent) from read-only (might change) from not-yet-known (fail
// closed until discovery reports).
func ineligibleReason(repo domain.Repo, known bool) string {
	switch {
	case !known:
		return "repository capability is not yet known"
	case repo.Archived:
		return "repository is archived"
	default:
		return "repository is read-only"
	}
}

// applyToggle records a toggle's outcome. On an accepted toggle it re-reads the affected
// repository's Workflow list so the displayed state is the API's reported one (R8), never an
// optimistic flip. On a rejection, a permission failure among them (R7), it surfaces the
// reason and leaves the displayed state unchanged (AC3).
func (m Model) applyToggle(res toggleResultMsg) (Model, tea.Cmd) {
	if res.err != nil {
		m.status, m.statusErr = "toggle failed: "+res.err.Error(), true
		return m, nil
	}
	if res.sum.Acted >= 1 {
		m.status, m.statusErr = actionPast(res.op)+" "+res.wf.Name+", re-reading state", false
		return m.refetchRepo(res.repo) // R8: reflect the API's reported state
	}
	m.status, m.statusErr = toggleFailure(res), true
	return m, nil
}

// toggleFailure states why a toggle did not take effect, so a rejection is reported as a
// permission failure rather than as a bug (R7). It reads the API's own reason where Execute
// grouped one, and falls back to a bounded-retry authorization skip otherwise (rate-governor
// R19a): a fine-grained PAT's 403 can be classified as a rate limit on this endpoint and
// reclassified as an authorization failure after three backoffs, which is still a permission
// failure to report, never a success.
func toggleFailure(res toggleResultMsg) string {
	verb := actionVerb(res.op)
	for _, f := range res.sum.Failures {
		if f.Reason != "" {
			return "could not " + verb + ": " + f.Reason
		}
	}
	return "could not " + verb + ": the API rejected it (a permission failure)"
}

// refetchRepo issues one Fetch for the toggled repository, so R8's re-read reflects the API's
// reported state. With no Fetch wired it is a no-op.
func (m Model) refetchRepo(id domain.RepoID) (Model, tea.Cmd) {
	if m.fetch == nil {
		return m, nil
	}
	fetch := m.fetch
	return m, func() tea.Msg { return WorkflowsFetched(fetch(id)) }
}

// repoSnapshot is the eligibility map ToggleWorkflow's Plan takes, the discovered capability
// data keyed by RepoID (R6). A repository absent from it makes the toggle fail closed.
func (m Model) repoSnapshot() map[domain.RepoID]domain.Repo {
	out := make(map[domain.RepoID]domain.Repo, len(m.capability))
	for _, r := range m.capability {
		out[r.ID] = r
	}
	return out
}

// rowUnderCursor is the row the cursor is on, and whether the list is non-empty.
func (m Model) rowUnderCursor() (wfRow, bool) {
	rows := m.displayRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return wfRow{}, false
	}
	return rows[m.cursor], true
}

// wfRow is one row of the list: a Workflow with its owning repository (R0, R1).
type wfRow struct {
	repo domain.RepoID
	wf   domain.Workflow
}

// displayRows is the cross-repository Workflow list, grouped by repository and sorted within
// each by name, with a deterministic tiebreak so the order is stable across refreshes and the
// goldens are byte-stable. It lists every state, including all disabled states and deleted
// (R10): filtering any out would hide exactly the rows this surface exists to show.
func (m Model) displayRows() []wfRow {
	var rows []wfRow
	for _, id := range m.order {
		for _, w := range m.workflows[id.String()] {
			rows = append(rows, wfRow{repo: id, wf: w})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].repo.String() != rows[j].repo.String() {
			return rows[i].repo.String() < rows[j].repo.String()
		}
		if rows[i].wf.Name != rows[j].wf.Name {
			return rows[i].wf.Name < rows[j].wf.Name
		}
		return rows[i].wf.ID < rows[j].wf.ID
	})
	return rows
}

// rowAction is what a row's ACTION column shows, and what the toggle key does on it (R5, R6,
// R9). Exactly one of an offered toggle and a stated reason applies.
type rowAction struct {
	op      ops.Operation // OpEnable or OpDisable, meaningful only when offered
	offered bool
	label   string // the verb when offered, else the reason no action is (R6, R9, R11)
}

// actionFor decides the action a row offers, applying the gate that costs no request (AC2). A
// deleted Workflow offers neither toggle and its Runs are Orphaned (R9, R11). An archived or
// read-only repository offers neither, with the reason stated and distinct (R6): archived is
// permanent, read-only might change. An active Workflow offers Disable, and any disabled
// state offers Enable, disabled_fork best-effort per the resolved open question. An
// unrecognised state renders verbatim in the STATE column (R3) but offers no action here,
// because the tool does not toggle a state it cannot reason about.
func (m Model) actionFor(row wfRow) rowAction {
	if row.wf.State == domain.StateDeleted {
		return rowAction{label: "orphaned Runs"} // R9, R11
	}
	if r, ok := m.capability[row.repo.String()]; ok {
		if r.Archived {
			return rowAction{label: "archived"} // R6: permanent read-only
		}
		if !r.Permissions.Push {
			return rowAction{label: "read-only"} // R6: might change
		}
	} else {
		return rowAction{label: "-"} // capability not yet known: fail closed, offer nothing
	}
	switch row.wf.State {
	case domain.StateActive:
		return rowAction{op: ops.OpDisable, offered: true, label: "Disable"}
	case domain.StateDisabledManually, domain.StateDisabledInactivity, domain.StateDisabledFork:
		return rowAction{op: ops.OpEnable, offered: true, label: "Enable"}
	default:
		return rowAction{label: "-"} // an unrecognised state: no action reasoned about (R3)
	}
}

// moveCursor moves the cursor by delta, clamped, scrolling the viewport.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	n := len(m.displayRows())
	if n == 0 {
		m.cursor, m.top = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	m.scrollToCursor()
}

func (m *Model) scrollToCursor() {
	rows := m.listCapacity()
	if rows <= 0 {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// pageSize is the number of rows a page-motion key moves.
func (m Model) pageSize() int {
	if n := m.listCapacity(); n > 1 {
		return n
	}
	return 1
}
