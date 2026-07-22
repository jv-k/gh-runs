// Package feed is the Runs tab: one live list of Runs spanning every repository on
// the account, updating as Runs are invoked anywhere (live-run-feed Purpose). It is
// gh-runs' default view and primary surface.
//
// A tab is not a tea.Model. It exposes View() string and an Update the root drives,
// and only the root implements tea.Model (ADR-0011's tab contract). The root routes
// a tea.KeyPressMsg here only when this tab is focused, and broadcasts size, data and
// the Budget Readout here always, so an unfocused Feed keeps accumulating and its
// background reveal (R33) and ~30s liveness (R27) hold (ADR-0011 routing, ADR-0015).
//
// The Feed owns its projection: the accumulated Runs, keyed by repository and replaced
// wholesale as each RunsFetched arrives, interleaved and sorted by EffectiveStart
// (ADR-0015). It renders to a frame from held state alone, with no live terminal and
// no network, which is what makes R36's goldens cheap. feed imports domain, filter,
// keys and governor, and lipgloss and bubbles for rendering; it also imports the rundetail
// pane it opens over its selection, which is the one import direction the tab contract
// permits (a tab may import a pane). It never imports another tab or the root (ADR-0011).
//
// The Feed opens rundetail on the OpenDetail key over the Run under its cursor, closes it on
// CloseDetail, and while it is open reports the cursor's Run to it on every move so the pane
// debounces one fetch per settle (run-detail R10, AC1). The pane owns that debounce, the
// fast-tier refresh and the discard rule; the Feed reports where its cursor is and forwards
// the broadcasts the pane pauses and lays out on (ADR-0011, ADR-0015).
package feed

import (
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
	"github.com/jv-k/gh-runs/v2/internal/tui/confirm"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	"github.com/jv-k/gh-runs/v2/internal/tui/rundetail"
)

// Planner freezes a selection into an ops.Plan: the shared entry to the confirmation
// chain (ADR-0019). *ops.Ops satisfies it. It is a narrow interface so the Feed
// depends on the one call it makes and a golden test with no planner leaves it nil,
// where the delete key is inert (the destructive action stays disabled until the
// planner and the discovered capability data are wired, repo-discovery R8).
type Planner interface {
	Plan(op ops.Operation, sel []ops.Item, repos map[domain.RepoID]domain.Repo) (ops.Plan, error)
}

// ReposDiscovered carries the account's discovered repositories, so the Feed's
// capability gate distinguishes offered, read-only, not-yet-known and permanently
// read-only (R17, R18, R21, R36's third golden). ADR-0015's catalog names this event
// ReposDiscovered []domain.Repo, emitted by the engine and broadcast to every tab. The
// built scheduler relays only RunsFetched, so the root pulls discovery and broadcasts
// this value; the type is homed with its sole consumer until a tab beyond the Feed
// needs it. It carries domain.Repo because Capability() and the archived flag both ride
// on it (ADR-0014).
type ReposDiscovered []domain.Repo

// RevalidatedAt carries the most recent revalidation instant the local store recorded
// across the poll set (local-store R7). A paused Feed reads it to state what it is
// showing and as of when, rather than presenting cached rows as live (R30). It is a
// broadcast the root pulls from the store on its coarse tick.
type RevalidatedAt time.Time

// Options carries the Feed's construction seams. The root fills them: the profile is
// the resolved keybinding set (R7a), and SetViewport publishes the visible
// repositories to the scheduler's medium tier (R5, ADR-0021), nil in a golden test.
// DetailFetch and Clock are the detail pane's seams, threaded through the root from main.go
// (ADR-0015): DetailFetch backs the pane's Job fetch and Clock its timing column, both nil
// in a golden test that never opens the pane.
type Options struct {
	Profile     keys.Profile
	SetViewport func([]domain.RepoID)
	DetailFetch rundetail.Fetch
	Clock       clock.Clock
	// Ops freezes the selection into a Plan when the delete key opens the confirmation
	// (purge R4 to R9). main.go wires it to the shared ops engine; a golden test leaves
	// it nil, and the delete key is then inert.
	Ops Planner
	// LogFetch and LogExport are the log view's seams, threaded through the detail pane this
	// tab opens (log-viewer R1, R11). LogFetch reads one Job's log and LogExport downloads the
	// whole-Run archive; the log-deletion planner reuses Ops, the same engine the Feed's own
	// delete uses (R17). All are nil in a golden test, where the log pane never opens.
	LogFetch  logview.Fetch
	LogExport logview.Exporter
	// Approver runs the two approval writes and ApprovalFetch reads a Run's pending deployments,
	// both for the decision pane the Approve key opens over an awaiting Run (approvals R11, R12).
	// main.go wires Approver to the shared ops engine and ApprovalFetch over the shared client;
	// a golden test leaves them nil, and the Approve key is then inert.
	Approver      approval.Approver
	ApprovalFetch approval.Fetcher
}

// Model is the Feed tab's state. The live map is the truth per repository; the
// displayed order is the painted frame, which stays stable while the cursor is in the
// list so a poll never moves the row under the cursor (R9, R10, AC1).
type Model struct {
	width  int
	height int
	active bool // this tab is focused; the root sets it, and losing focus is idle (R10)

	profile     keys.Profile
	setViewport func([]domain.RepoID)

	// live is each repository's latest Runs, keyed by RepoID.String() and replaced
	// wholesale as its RunsFetched arrives (ADR-0015).
	live map[string][]domain.Run

	// displayedIDs is the painted order, a stable list of Run IDs that changes only
	// on apply (idle or explicit refresh), so a poll leaves every row index unchanged
	// (R9, R10, AC1). current holds each painted Run's latest fields, updated in place
	// on a repaint (R9, AC2, AC4).
	displayedIDs []int64
	current      map[int64]domain.Run

	pending pendingChanges // R11's deferred-change tally, by kind

	// selected is keyed by Run ID and survives repaint, scroll and filter change
	// (R13, R13a, AC5). The persistent count keeps an off-filter selection visible.
	selected map[int64]bool

	cursor int // index into displayedIDs
	top    int // first visible row, scrolled to keep the cursor on screen

	// engaged is true once the user has navigated or selected in the list. Deferral
	// (R10) begins only then: before it, the cold-start progressive reveal applies, so
	// the local repository paints first (R32) and the others appear as they arrive with
	// no user interaction (R33, AC16). Once engaged, a change that would move a row is
	// deferred so a poll never moves the row under the cursor (R10, AC1, AC3). This
	// resolves the tension between AC16's reveal-without-interaction and R33's
	// reveal-subject-to-R10; see the stage notes.
	engaged bool

	// filter is applied client-side over held Runs at this stage (R22, R23). The engine
	// polls unfiltered, so the server-side pushdown (R22) and R24's live cap label are
	// not wired here; the cap label renders from held totals and is golden-verified.
	filter       filter.Filter
	filterInput  textinput.Model
	filterActive bool // the filter input holds focus, so the cursor is not in the list (R10)

	// repos is the capability gate's data, keyed by RepoID.String() (R17, R18, R21).
	repos map[string]domain.Repo

	readout governor.Readout // R29, R30: shown only under pressure or on exhaustion
	asOf    time.Time        // local-store R7: what a paused Feed is showing, and as of when

	// detail is the Run detail pane, opened over the selection (ADR-0011, BUILD-ORDER stage
	// 8). detailOpen gates whether keys open or close it, whether the cursor's Run is
	// reported to it, and whether it is painted below the list. The pane owns its own
	// fetch state; the Feed owns only whether it is open and which Run it follows.
	detail     rundetail.Model
	detailOpen bool

	// confirm is the graduated-confirmation pane, opened over the frozen selection on
	// the delete key (purge R4 to R9, ADR-0011: a tab may import a pane). While it is
	// open it is a modal: the Feed routes every key to it and paints it in place of the
	// list. planner freezes the selection into the Plan it renders.
	confirm     confirm.Model
	confirmOpen bool
	planner     Planner

	// approval is the decision pane, opened over the awaiting Run under the cursor on the
	// Approve key (approvals R11, R12, ADR-0011: a tab may import a pane). While it is open it
	// is a modal: the Feed routes every key to it and paints it in place of the list. approver
	// gates whether the Approve key opens it at all, nil in a golden test where it is inert.
	approval     approval.Model
	approvalOpen bool
	approver     approval.Approver

	// approvalsFilter is the badge's saved filter (approvals R1, R9). When on, liveView keeps
	// only the Runs awaiting a decision. It is a client-side predicate over held Runs, so it
	// issues no request and spends no Budget (R5, AC4).
	approvalsFilter bool

	showHelp bool

	// totals carries each filtered repository's reachable and claimed counts for R24's
	// cap label. Empty under the unfiltered live poll; populated from held state for the
	// golden. Keyed by RepoID.String().
	totals map[string]capTotal
}

// capTotal is one repository's reachable and claimed Run counts under a filtered
// listing (R24). claimed is the response's total_count; reachable is what the cap let
// through. The repository is capped exactly when claimed exceeds reachable.
type capTotal struct {
	reachable int
	claimed   int
}

// pendingChanges counts deferred changes by kind, never folding them into one number
// (R11). Insertions are new, evictions are removed, reorders are moved.
type pendingChanges struct {
	added   int
	removed int
	moved   int
}

func (p pendingChanges) any() bool { return p.added > 0 || p.removed > 0 || p.moved > 0 }

// New returns a Feed over opts. It reads nothing and paints an empty list until the
// first RunsFetched arrives.
func New(opts Options) Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "branch:main status:failure ..."
	return Model{
		active:      true,
		profile:     opts.Profile,
		setViewport: opts.SetViewport,
		live:        make(map[string][]domain.Run),
		current:     make(map[int64]domain.Run),
		selected:    make(map[int64]bool),
		repos:       make(map[string]domain.Repo),
		totals:      make(map[string]capTotal),
		filterInput: ti,
		detail: rundetail.New(rundetail.Options{
			Fetch:      opts.DetailFetch,
			Clock:      opts.Clock,
			Profile:    opts.Profile,
			LogFetch:   opts.LogFetch,
			LogPlanner: opts.Ops, // the log-deletion planner is the same ops engine (log-viewer R17)
			LogExport:  opts.LogExport,
		}),
		confirm: confirm.New(opts.Profile),
		planner: opts.Ops,
		approval: approval.New(approval.Options{
			Profile:  opts.Profile,
			Approver: opts.Approver,
			Fetch:    opts.ApprovalFetch,
		}),
		approver: opts.Approver,
	}
}

// SetActive records whether this tab is focused. Losing focus is idle, so it applies the
// deferred changes (R10, resolved open question 5: idle means focus leaving the list) and
// leaves the filter input, so focus cannot return mid-filter with the input still holding
// keys. Gaining focus freezes the current frame again.
func (m Model) SetActive(active bool) Model {
	was := m.active
	m.active = active
	if was && !active {
		// Leaving the filter input is part of losing focus (R10's "idle means focus leaving
		// the list"), so the root's per-tab key routing sees this tab stop capturing and the
		// input never keeps focus across a tab switch.
		m.filterActive = false
		m.filterInput.Blur()
		m.applyView(m.liveView()) // idle: apply deferred (R10)
	}
	return m
}

// CapturesInput reports whether this tab holds text-input focus, which is true while
// the filter input is open (R22, R23) and while the confirmation modal is up (purge
// R7). The root reads it to route every key but the terminal interrupt straight here
// while it captures, so a digit typed into a filter value or a typed-count confirmation,
// or a y that must confirm rather than switch context, is not stolen as a global
// navigation key (ADR-0011, R7).
func (m Model) CapturesInput() bool {
	return m.filterActive || m.confirmOpen || m.approvalOpen || (m.detailOpen && m.detail.CapturesInput())
}

// Update handles one message the root routed here. Size and data broadcasts reach it
// whether or not it is focused (ADR-0011), which is what keeps R33's reveal and R27's
// liveness alive in the background. A key reaches it only when focused.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.filterInput.SetWidth(max(msg.Width-2, 0))
		// Keep the panes laid out even while closed, so their first painted frame is
		// already sized when they open (ADR-0011: width is a correctness property).
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		m.confirm, _ = m.confirm.Update(msg)
		m.approval, _ = m.approval.Update(msg)
		return m, cmd

	case scheduler.Update:
		// One repository's fresh Runs, replacing its slice wholesale (ADR-0015).
		m.live[msg.Repo.String()] = msg.Runs
		m.recompute()
		// Report the cursor's freshest Run to the open pane, so its Attempt badge tracks a
		// re-run (R17) and its liveness gate reads the current Status (R13). A same-Run
		// report refreshes fields in place and issues no fetch.
		var dcmd tea.Cmd
		m, dcmd = m.retargetDetail()
		return m, tea.Batch(m.publishViewport(), dcmd)

	case ReposDiscovered:
		for _, r := range msg {
			m.repos[r.ID.String()] = r
		}
		return m, nil

	case RevalidatedAt:
		m.asOf = time.Time(msg)
		return m, nil

	case governor.Readout:
		m.readout = msg
		// The pane pauses on the same Budget as the Feed (run-detail R16), so it must see the
		// exhaustion broadcast whether or not it is open.
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	// A message the Feed does not consume is forwarded to the panes. Each pane's own tagged
	// messages (the detail pane's debounce settle and Jobs response, the approval pane's
	// DeploymentsLoaded and Reviewed) reach the Feed as broadcasts, and only the naming pane
	// consumes them (ADR-0015's type-visibility targeting); a closed pane discards them by its
	// own open gate.
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	var acmd tea.Cmd
	m.approval, acmd = m.approval.Update(msg)
	return m, tea.Batch(cmd, acmd)
}

// handleKey matches a press against the active profile's registry bindings, never a
// key literal of its own (R7a, AC18). While the filter input is focused, printable
// keys reach it instead.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.confirmOpen {
		return m.handleConfirmKey(k)
	}
	if m.approvalOpen {
		return m.handleApprovalKey(k)
	}
	if m.filterActive {
		return m.handleFilterKey(k)
	}
	// Recursive focus: when the detail pane holds key focus (the operator has descended into its
	// Job list or opened a Job's log), it gets the key and the Feed does not also act, so the log
	// view's own fold, timestamp, search and delete keys never collide with the list's actions
	// (ADR-0011's recursive focus). A key the pane does not use is swallowed, not run as a list
	// action, because focus is the pane's while it captures.
	if m.detailOpen && m.detail.CapturesKeys() {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.HandleKey(k)
		return m, cmd
	}
	switch {
	case key.Matches(k, m.profile.Approve):
		// Open the decision over the awaiting Run under the cursor, routed by Kind (approvals
		// R11, R12). Inert on a Run awaiting nothing, so each kind offers only its own action.
		return m.openApproval()
	case key.Matches(k, m.profile.ApprovalsFilter):
		// Activate the badge's saved filter, narrowing the Feed to the Runs awaiting a decision
		// and fetching nothing to do it (approvals R9, AC8).
		return m.toggleApprovalsFilter(), m.publishViewport()
	case key.Matches(k, m.profile.Delete):
		return m.openConfirm(ops.OpDelete), nil
	case key.Matches(k, m.profile.Cancel):
		return m.openConfirm(ops.OpCancel), nil
	case key.Matches(k, m.profile.ForceCancel):
		return m.openConfirm(ops.OpForceCancel), nil
	case key.Matches(k, m.profile.Rerun):
		return m.openRerun(ops.OpRerun), nil
	case key.Matches(k, m.profile.RerunFailed):
		return m.openRerun(ops.OpRerunFailed), nil
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
	case key.Matches(k, m.profile.PageUp):
		m.moveCursor(-m.pageSize())
	case key.Matches(k, m.profile.PageDown):
		m.moveCursor(m.pageSize())
	case key.Matches(k, m.profile.FirstRow):
		m.engaged = true
		m.cursor = 0
		m.scrollToCursor()
	case key.Matches(k, m.profile.LastRow):
		m.engaged = true
		m.cursor = len(m.displayedIDs) - 1
		m.clampCursor()
		m.scrollToCursor()
	case key.Matches(k, m.profile.ToggleSelect):
		m.toggleSelect()
	case key.Matches(k, m.profile.Refresh):
		// Explicit refresh applies deferred changes, keeping the cursor on its Run ID
		// (R10, R12).
		m.applyView(m.liveView())
	case key.Matches(k, m.profile.Filter):
		// Entering the filter input is idle: apply deferred changes, then focus the
		// input so the cursor is no longer in the list (R10, R22, R23).
		m.applyView(m.liveView())
		m.filterActive = true
		return m, m.filterInput.Focus()
	case key.Matches(k, m.profile.OpenDetail):
		// Open the detail pane over the Run under the cursor (run-detail, BUILD-ORDER
		// stage 8). The pane then owns the fetch; the Feed only holds it open.
		return m.openDetail()
	case key.Matches(k, m.profile.CloseDetail):
		// Close the pane. A no-op when nothing is open; esc has no other binding in the
		// list, and while the filter input is focused it is FilterCancel's, matched above.
		m.detailOpen = false
		m.detail = m.detail.Close()
		return m, nil
	case key.Matches(k, m.profile.Help):
		m.showHelp = !m.showHelp
	}
	// A motion or selection key may have moved the cursor onto another Run; report it to the
	// open pane so the pane debounces one fetch per settle (R10, AC1). A same-Run report is a
	// no-op, so a non-motion key issues nothing.
	cmd := m.publishViewport()
	var dcmd tea.Cmd
	m, dcmd = m.retargetDetail()
	return m, tea.Batch(cmd, dcmd)
}

// openDetail opens the detail pane over the Run under the cursor, the OpenDetail key's
// handler (BUILD-ORDER stage 8). With no row under the cursor it is a no-op. The pane owns
// the debounce, the fetch and the discard rule; the Feed reports the cursor and forwards
// broadcasts (ADR-0011, ADR-0015). The Workflow's deleted State is not stamped on a Run and
// the Feed does not yet resolve it, so R8's marker stays off until that seam is wired; this
// is the one call site that will set it.
func (m Model) openDetail() (Model, tea.Cmd) {
	if m.detailOpen {
		// Already open and Feed-focused: a second open-detail press descends into the pane, so
		// motion moves its Job cursor and a further press opens the selected Job's log
		// (log-viewer R1, ADR-0011's recursive focus).
		m.detail = m.detail.Focus()
		return m, nil
	}
	r, ok := m.cursorRun()
	if !ok {
		return m, nil
	}
	m.detailOpen = true
	repo, known := m.repoCapability(r)
	m.detail = m.detail.SetRepoCapability(repo, known) // the log-delete gate reads this (log-viewer R17)
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Open(r)
	return m, cmd
}

// repoCapability is the discovered capability for a Run's repository, and whether it is known
// yet. The detail pane hands it to the log view's delete gate, which fails closed on an
// unknown repository (repo-discovery R8, log-viewer R17).
func (m Model) repoCapability(r domain.Run) (domain.Repo, bool) {
	repo, ok := m.repos[r.Repo.String()]
	return repo, ok
}

// openConfirm freezes the selection into a Plan for op and opens the confirmation modal
// over it (purge R4 to R9, run-lifecycle R16, R17). The frozen set is R4's ID-keyed
// selection, or the Run under the cursor when nothing is selected, and it freezes here
// at modal open: RunItem copies each Run by value, so a poll arriving afterwards cannot
// change the set (R5). With no planner wired, an empty set, or a repository whose
// capability is not yet known, it stays closed, keeping the destructive action disabled
// (repo-discovery R8, ADR-0019's fail-closed Plan).
//
// A single re-run or re-run-failed prices at FrictionNone (run-lifecycle R18): it takes
// no confirmation, so the pane opens no modal (ADR-0019). Launching Execute over that
// confirmed set is the running surface this stage defers, exactly as the Purge stage
// defers launching a Purge from a confirmed delete Plan. Every other case opens the
// graduated confirmation, which is one shared component reused unchanged.
func (m Model) openConfirm(op ops.Operation) Model {
	if m.planner == nil {
		return m
	}
	items := m.frozenSelection()
	if len(items) == 0 {
		return m
	}
	plan, err := m.planner.Plan(op, items, m.repoSnapshot())
	if err != nil {
		return m // fail closed: an unknown repository keeps the action disabled (repo-discovery R8)
	}
	if plan.Friction() == ops.FrictionNone {
		return m // R18: a single re-run takes no modal; launching it is the deferred running surface
	}
	m.confirm = m.confirm.Open(plan)
	m.confirmOpen = true
	return m
}

// openRerun raises a re-run or re-run-failed confirmation, first applying run-detail
// R18's Orphaned-Run exclusion: a Run whose Workflow is deleted can produce no further
// Run, so re-run is not offered for it (run-detail R18, AC15). The detail pane holds the
// Workflow State it resolved, so when it is open over an Orphaned Run the re-run key is
// inert. The permission half of the gate (push && !archived) is enforced by ops.Plan's
// eligibility stamp, consistently with the Feed and every other operation, so this adds
// only the deleted-Workflow limb the shared Plan cannot see (a Run does not carry its
// Workflow's State). The eligibility of a full multi-selection is shown in the modal's
// skip lines as for any operation.
func (m Model) openRerun(op ops.Operation) Model {
	if m.detailOpen && m.detail.IsOrphaned() {
		return m // run-detail R18, AC15: no re-run for an Orphaned Run
	}
	return m.openConfirm(op)
}

// frozenSelection builds R4's frozen set: a RunItem per selected Run ID in displayed
// order, then any off-filter selected Run from the live truth (R13a's cross-filter
// accumulation), or the Run under the cursor when the selection is empty. Each
// constructor copies the Run, so the set is frozen at this instant (R5).
func (m Model) frozenSelection() []ops.Item {
	if len(m.selected) == 0 {
		if r, ok := m.cursorRun(); ok {
			return []ops.Item{ops.RunItem(r)}
		}
		return nil
	}
	byID := m.liveByID()
	seen := make(map[int64]bool, len(m.selected))
	var items []ops.Item
	for _, id := range m.displayedIDs {
		if m.selected[id] {
			if r, ok := byID[id]; ok {
				items = append(items, ops.RunItem(r))
				seen[id] = true
			}
		}
	}
	for id := range m.selected {
		if seen[id] {
			continue
		}
		if r, ok := byID[id]; ok {
			items = append(items, ops.RunItem(r))
		}
	}
	// R30/AC22: the frozen view carries the Feed's order (R8), EffectiveStart descending
	// with Run ID descending on a tie. The first loop already walked displayedIDs in that
	// order, but R13a's off-filter tail above was appended in Go map-iteration order, which
	// is randomised. Sorting the whole assembled set restores determinism and keeps the last
	// row the oldest Run in the set by run_started_at, across a cross-filter selection. Every
	// Item here is a RunItem, so Run is always set. This mirrors liveView's comparator.
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i].Run.EffectiveStart(), items[j].Run.EffectiveStart()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return items[i].ID > items[j].ID
	})
	return items
}

// liveByID indexes every live Run by its ID across every repository, so a selected Run
// that is off the current filter still resolves to its full object for the frozen set.
func (m Model) liveByID() map[int64]domain.Run {
	out := make(map[int64]domain.Run)
	for _, runs := range m.live {
		for i := range runs {
			out[runs[i].ID] = runs[i]
		}
	}
	return out
}

// repoSnapshot is the eligibility map Plan takes, the discovered capability data keyed
// by RepoID (R10, ADR-0019). A repository absent from it makes Plan fail closed.
func (m Model) repoSnapshot() map[domain.RepoID]domain.Repo {
	out := make(map[domain.RepoID]domain.Repo, len(m.repos))
	for _, r := range m.repos {
		out[r.ID] = r
	}
	return out
}

// handleConfirmKey drives the confirmation modal while it is open, routing every key to
// the pane (R7) and acting on its Outcome. An abort dismisses it having issued nothing
// (AC6); a confirmation closes it holding the confirmed Plan and Input, and launching
// the Purge from them is the running-Purge surface this stage defers.
func (m Model) handleConfirmKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	switch m.confirm.Outcome() {
	case confirm.Aborted, confirm.Confirmed:
		m.confirm = m.confirm.Close()
		m.confirmOpen = false
	}
	return m, cmd
}

// openApproval opens the decision pane over the Run under the cursor when it is awaiting a
// decision, routing by Kind (approvals R11, R12, R3). With no approver wired, no Run under the
// cursor, or a Run awaiting nothing, it is inert, so the Approve key offers an action only
// where one exists (AC2). The pane owns the fetch and the write; the Feed only holds it open
// and paints it in place of the list.
func (m Model) openApproval() (Model, tea.Cmd) {
	if m.approver == nil {
		return m, nil
	}
	r, ok := m.cursorRun()
	if !ok {
		return m, nil
	}
	kind := approvals.Classify(r)
	if kind == approvals.KindNone {
		return m, nil // AC2: a Run awaiting nothing offers no action
	}
	var cmd tea.Cmd
	m.approval, cmd = m.approval.Open(approval.Target{
		Repo:  r.Repo,
		RunID: r.ID,
		Kind:  kind,
		Title: approvalTitle(r),
	})
	m.approvalOpen = true
	return m, cmd
}

// handleApprovalKey drives the decision pane while it is open, routing every key to it (R7)
// and dropping the modal when the pane closes itself, on esc or after the operator dismisses a
// terminal outcome. A successful write changes the Run's fields, and the Feed's badge and
// filter follow through the ordinary poll, so nothing here forces a refresh (R15).
func (m Model) handleApprovalKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.approval, cmd = m.approval.Update(k)
	if !m.approval.IsOpen() {
		m.approvalOpen = false
	}
	return m, cmd
}

// toggleApprovalsFilter applies or lifts the badge's saved filter (approvals R9), narrowing
// the Feed to the Runs awaiting a decision and back. It applies the view at once, keeping the
// cursor on its Run ID, and issues no request, because the predicate is client-side over held
// Runs (R5, AC4). It opens no view and switches no tab, so the Feed stays the focused surface
// with the filter applied (AC8).
func (m Model) toggleApprovalsFilter() Model {
	m.approvalsFilter = !m.approvalsFilter
	m.applyView(m.liveView())
	return m
}

// approvalCount is the number of held Runs awaiting a decision, counted across every
// repository's live truth (approvals R7). It counts the Runs actually held that match the
// predicate, never a total_count, and it is independent of the text filter and the saved
// filter, so the badge reflects what the Feed has reached rather than what is on screen (R10).
// Following the live truth is what lets the badge clear through the Feed's ordinary poll: when
// an approved Run's fields change on the next poll it drops out of the count with no bespoke
// refresh (R15, AC11).
func (m Model) approvalCount() int {
	n := 0
	for _, runs := range m.live {
		for i := range runs {
			if approvals.Awaiting(runs[i]) {
				n++
			}
		}
	}
	return n
}

// approvalTitle is the Run's display title for the decision pane's header, its display_title
// where the API served one, else the workflow label. The pane sanitises it before painting.
func approvalTitle(r domain.Run) string {
	if r.DisplayTitle != "" {
		return r.DisplayTitle
	}
	return workflowLabel(r)
}

// cursorRun is the Run under the cursor, or false when the list is empty.
func (m Model) cursorRun() (domain.Run, bool) {
	if m.cursor < 0 || m.cursor >= len(m.displayedIDs) {
		return domain.Run{}, false
	}
	r, ok := m.current[m.displayedIDs[m.cursor]]
	return r, ok
}

// retargetDetail reports the cursor's Run to the open pane, the "feed reports where its
// cursor is" half of the split (ADR-0011). It is a no-op while the pane is closed or the
// list is empty; the pane's SelectRun no-ops a same-Run report, so following the cursor onto
// the row it already rests on issues no fetch (R10, AC1).
func (m Model) retargetDetail() (Model, tea.Cmd) {
	if !m.detailOpen {
		return m, nil
	}
	r, ok := m.cursorRun()
	if !ok {
		return m, nil
	}
	repo, known := m.repoCapability(r)
	m.detail = m.detail.SetRepoCapability(repo, known) // keep the log-delete gate matched to the Run on screen
	var cmd tea.Cmd
	m.detail, cmd = m.detail.SelectRun(r)
	return m, cmd
}

// handleFilterKey drives the filter input. FilterAccept (enter) accepts the filter and
// returns to the list; FilterCancel (esc) cancels it and restores the unfiltered view.
// Everything else is text the input consumes, and the filter re-applies live as it is
// typed, because the cursor is not in the list while the input is focused (R10, R23).
//
// Both control keys are matched from the active profile's registry with key.Matches, never
// a key literal of its own, so the filter input is inside AC18's reach like every other
// binding (R7a).
func (m Model) handleFilterKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.FilterAccept):
		m.filterActive = false
		m.filterInput.Blur()
		m.applyFilterFromInput()
		return m, nil
	case key.Matches(k, m.profile.FilterCancel):
		m.filterActive = false
		m.filterInput.Blur()
		m.filterInput.SetValue("")
		m.filter = filter.Filter{}
		m.applyView(m.liveView())
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(k)
	m.applyFilterFromInput()
	return m, cmd
}

// applyFilterFromInput parses the input into a Filter and re-applies the view. An
// unparseable token leaves the last good filter in place rather than clearing it, so a
// half-typed query never blanks the list.
func (m *Model) applyFilterFromInput() {
	if f, err := parseFilterQuery(m.filterInput.Value()); err == nil {
		m.filter = f
	}
	m.applyView(m.liveView())
}

// moveCursor moves the cursor by delta, clamps it to the list, engages the list so
// later polls defer (R10), and scrolls the viewport to keep the cursor visible.
func (m *Model) moveCursor(delta int) {
	m.engaged = true
	m.cursor += delta
	m.clampCursor()
	m.scrollToCursor()
}

// clampCursor keeps the cursor within the displayed list, or at zero when it is empty.
func (m *Model) clampCursor() {
	if len(m.displayedIDs) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.displayedIDs) {
		m.cursor = len(m.displayedIDs) - 1
	}
}

// scrollToCursor adjusts the top of the viewport so the cursor is on screen, moving a
// row only in response to the user's own motion, never a poll (R10, AC1).
func (m *Model) scrollToCursor() {
	capRows := m.rowCapacity()
	if capRows <= 0 {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+capRows {
		m.top = m.cursor - capRows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// toggleSelect toggles the Run under the cursor in the selection, keyed by Run ID so it
// survives a repaint, a scroll or a filter change (R13, R13a).
func (m *Model) toggleSelect() {
	m.engaged = true
	if m.cursor < 0 || m.cursor >= len(m.displayedIDs) {
		return
	}
	id := m.displayedIDs[m.cursor]
	if m.selected[id] {
		delete(m.selected, id)
	} else {
		m.selected[id] = true
	}
}

// pageSize is the number of rows a page-motion key moves, one screenful of rows.
func (m Model) pageSize() int {
	if n := m.rowCapacity(); n > 1 {
		return n
	}
	return 1
}

// cursorInList reports whether the cursor is in the list, which is when this tab is
// focused, the filter input is not, and the user has engaged the list (R10, resolved
// open question 5). While it is, changes that would move a row are deferred. Before
// engagement the cold-start reveal applies, so other repositories appear with no
// interaction (R33, AC16).
func (m Model) cursorInList() bool {
	return m.active && !m.filterActive && m.engaged
}

// recompute folds the live truth into the painted frame. While the cursor is in the
// list it repaints existing rows in place and defers anything that would move a row
// (R9, R10). Otherwise it applies the changes at once (R10: idle, or a filtered view
// being typed).
func (m *Model) recompute() {
	next := m.liveView()
	if m.cursorInList() {
		m.repaintAndDefer(next)
	} else {
		m.applyView(next)
	}
}

// liveView is the interleaved, filtered, sorted truth across every live repository:
// sorted by EffectiveStart descending, Run ID descending on a tie for determinism
// (R8). The filter is client-side over held Runs (R22, R23).
func (m Model) liveView() []domain.Run {
	var all []domain.Run
	for _, runs := range m.live {
		for i := range runs {
			// The approvals saved filter is a client-side predicate applied ahead of the text
			// filter, so an active badge narrows the view to the Runs awaiting a decision without
			// a request (approvals R5, R9).
			if m.approvalsFilter && !approvals.Awaiting(runs[i]) {
				continue
			}
			if m.filter.Match(runs[i]) {
				all = append(all, runs[i])
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		ti, tj := all[i].EffectiveStart(), all[j].EffectiveStart()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].ID > all[j].ID
	})
	return all
}

// repaintAndDefer updates painted rows in place and tallies what is deferred, without
// moving a single row (R9, R10, R11). A Status or Conclusion change repaints in place
// (AC2); a re-run repaints in place now and its reorder waits (AC4); an insertion,
// eviction or reorder is counted but not applied (R11).
func (m *Model) repaintAndDefer(next []domain.Run) {
	nextByID := make(map[int64]domain.Run, len(next))
	for i := range next {
		nextByID[next[i].ID] = next[i]
	}
	displayed := make(map[int64]bool, len(m.displayedIDs))
	for _, id := range m.displayedIDs {
		displayed[id] = true
	}

	// Repaint each existing row in place, and count the ones whose sort key changed: a
	// re-run advancing run_started_at is the only thing that reorders a stable set, since
	// Run IDs never change and insertions and evictions are counted on their own (R10,
	// AC4). Comparing before the overwrite is what distinguishes a move from a repaint.
	moved := 0
	for _, id := range m.displayedIDs {
		r, ok := nextByID[id]
		if !ok {
			continue
		}
		if !r.EffectiveStart().Equal(m.current[id].EffectiveStart()) {
			moved++
		}
		m.current[id] = r // repaint in place: the row adopts its latest fields, keeps its index
	}

	added := 0
	for i := range next {
		if !displayed[next[i].ID] {
			added++
		}
	}
	removed := 0
	for _, id := range m.displayedIDs {
		if _, ok := nextByID[id]; !ok {
			removed++
		}
	}

	m.pending = pendingChanges{added: added, removed: removed, moved: moved}
}

// applyView makes the painted frame equal to the live truth and clears the deferred
// tally, keeping the cursor on the same Run ID it rested on (R12). A refresh, an idle,
// and a filtered view being typed all pass through here.
func (m *Model) applyView(next []domain.Run) {
	var cursorID int64 = -1
	if m.cursor >= 0 && m.cursor < len(m.displayedIDs) {
		cursorID = m.displayedIDs[m.cursor]
	}

	m.displayedIDs = make([]int64, 0, len(next))
	m.current = make(map[int64]domain.Run, len(next))
	for i := range next {
		m.displayedIDs = append(m.displayedIDs, next[i].ID)
		m.current[next[i].ID] = next[i]
	}
	m.pending = pendingChanges{}

	m.cursor = 0
	if cursorID >= 0 {
		for i, id := range m.displayedIDs {
			if id == cursorID {
				m.cursor = i
				break
			}
		}
	}
	m.clampCursor()
	m.scrollToCursor()
}

// selectedCount is the number of selected Runs, rendered as a persistent count so an
// off-filter selection is never invisible (R13a).
func (m Model) selectedCount() int { return len(m.selected) }

// publishViewport tells the scheduler which repositories have a row on screen, so they
// poll at the medium tier (R5, ADR-0021). It runs in a Cmd so Update stays pure, and is
// a no-op when no setter is wired (a golden test).
func (m Model) publishViewport() tea.Cmd {
	if m.setViewport == nil {
		return nil
	}
	ids := m.visibleRepos()
	set := m.setViewport
	return func() tea.Msg {
		set(ids)
		return nil
	}
}

// visibleRepos is the set of repositories with at least one row in the current
// viewport, the scheduler's medium tier (R5).
func (m Model) visibleRepos() []domain.RepoID {
	start, end := m.viewportBounds(m.rowCapacity())
	seen := make(map[string]bool)
	var ids []domain.RepoID
	for i := start; i < end && i < len(m.displayedIDs); i++ {
		r, ok := m.current[m.displayedIDs[i]]
		if !ok {
			continue
		}
		if key := r.Repo.String(); !seen[key] {
			seen[key] = true
			ids = append(ids, r.Repo)
		}
	}
	return ids
}

// parseFilterQuery parses the filter input into a Filter (R22, R23). A key:value token
// sets the named axis; a bare token is a permissive Status or Conclusion value, which
// is what R23's one input accepts. The axis names mirror the filter engine's fields and
// gh's flags. This micro-syntax is a stage-7 choice: the canon fixes the axes and their
// server or client placement, not the input's token spelling.
func parseFilterQuery(s string) (filter.Filter, error) {
	var f filter.Filter
	for _, tok := range strings.Fields(s) {
		axis, value, hasColon := strings.Cut(tok, ":")
		if !hasColon {
			if err := f.ParseStatus(tok); err != nil {
				return filter.Filter{}, err
			}
			continue
		}
		switch strings.ToLower(axis) {
		case "branch", "b":
			f.Branch = value
		case "commit", "c":
			f.Commit = value
		case "actor", "user", "u":
			f.Actor = value
		case "event", "e":
			f.Event = value
		case "workflow", "w":
			f.Workflow = value
		case "status", "s", "conclusion":
			if err := f.ParseStatus(value); err != nil {
				return filter.Filter{}, err
			}
		case "created":
			dr, err := filter.ParseCreated(value)
			if err != nil {
				return filter.Filter{}, err
			}
			f.Created = dr
		default:
			if err := f.ParseStatus(tok); err != nil {
				return filter.Filter{}, err
			}
		}
	}
	return f, nil
}
