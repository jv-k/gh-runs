// Package tui is the root Bubble Tea model, the only tea.Model in the tree (ADR-0011's
// tab contract). It owns the three tabs (Runs, Workflows, Storage), routes messages to
// them per class, and adapts the scheduler's engine channel into the message loop
// (ADR-0015). A tab is not a tea.Model: it exposes View() string and an Update the root
// drives, and the eleven terminal-wide fields of tea.View are the root's alone to set.
//
// Routing is two routes: a key press reaches exactly the focused tab, and every other
// message reaches every tab, so an unfocused Feed keeps accumulating and its background
// reveal (R33) and ~30s liveness (R27) hold. The root reads the Budget Readout on a
// coarse tick and broadcasts it on change (ADR-0015), and the async engine channel is
// turned into messages with the canonical receive-one-then-reschedule command. When the
// engine closes its channel the root quits (ADR-0015).
//
// tui imports the tabs, the engine's event and Readout types, keys and domain, and
// lipgloss for the tab bar. main.go constructs it and wires the channel and the pulls;
// nothing imports tui (ADR-0011).
package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
	"github.com/jv-k/gh-runs/v2/internal/tui/feed"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	"github.com/jv-k/gh-runs/v2/internal/tui/rundetail"
	"github.com/jv-k/gh-runs/v2/internal/tui/settings"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// tabBarHeight is the one line the root reserves for the tab bar, taken off the height
// the tabs receive so a tab lays out within the space it actually gets (R4a).
const tabBarHeight = 1

// readoutTick is the coarse cadence at which the root pulls the Budget Readout, the
// discovered repositories and the store's last-revalidated time, so exhaustion and the
// reset countdown stay live while the engine channel is quiet (R30 must not wait for
// traffic to notice recovery).
const readoutTick = time.Second

// schedulerClosedMsg is the adapter's signal that the engine closed its channel, which
// ADR-0015 makes the root's quit.
type schedulerClosedMsg struct{}

// tickMsg drives the coarse pull of the Readout and the other broadcast status the root
// sources by polling rather than by event.
type tickMsg struct{}

// tab is the root's uniform handle to a tab. A concrete tab exposes Update returning its
// own type and View() string (ADR-0011); the adapters below lift each into this
// interface so the root routes to all three the same way, and calls SetActive on a focus
// change so a tab losing focus can apply what it deferred (R10).
type tab interface {
	Update(tea.Msg) (tab, tea.Cmd)
	View() string
	SetActive(bool) tab
	Title() string
	// CapturesInput reports whether the tab holds text-input focus (the Feed's filter). While
	// it does, the root routes every key but the terminal interrupt to it, so the global
	// navigation keys stand down and typed text is not stolen (R7, R23).
	CapturesInput() bool
}

// Options carries the root's seams. main.go fills them: the channel is the scheduler's
// Updates, the pulls are the governor, discovery and the store, SetViewport is the
// scheduler's medium-tier control, and the profile is the resolved keybinding set.
type Options struct {
	Updates     <-chan scheduler.Update
	Readout     func() governor.Readout
	Repos       func() []domain.Repo
	Revalidated func() time.Time
	SetViewport func([]domain.RepoID)
	Profile     keys.Profile
	// Config is the resolved settings the Settings pane opens over, and SaveSettings
	// persists the pane's changes back to the config file (settings R17). main.go wires
	// SaveSettings to config.Save over the resolved config path, so the pane's only write is
	// that one local file and never the API. A nil SaveSettings makes the pane edit in memory
	// alone, which a headless test uses.
	Config       config.Config
	SaveSettings func(prev, next config.Config) error
	// DetailFetch and Clock are the run-detail pane's seams, constructed in main.go and
	// handed down through the Feed that opens the pane (ADR-0015): DetailFetch backs its Job
	// fetch over ghclient, and Clock is the wall clock its timing column reads.
	DetailFetch rundetail.Fetch
	Clock       clock.Clock
	// Ops freezes the Feed's selection into a Plan when the delete key opens the
	// confirmation (purge R4 to R9). main.go wires it to the shared ops engine.
	Ops feed.Planner
	// StorageFetch reads one repository's Cache and Artifact usage for the Storage tab,
	// and StorageOps freezes its Cache and Artifact selection into a reclamation Plan
	// (storage-reclamation R1, R17). main.go wires both over the shared client and the same
	// ops engine, so the Storage tab's DELETE travels the one mutation entry a Purge does.
	StorageFetch storage.Fetch
	StorageOps   storage.Planner
	// WorkflowFetch reads one repository's Workflow list for the Workflows tab, and WorkflowOps
	// enables or disables one Workflow through the shared ops engine (workflow-management R1,
	// R5). main.go wires both over the same client and ops, so a toggle is paced by the
	// governor and travels the one write path exactly as every other write does.
	WorkflowFetch workflows.Fetch
	WorkflowOps   workflows.Toggler
	// The dispatch form the Workflows tab opens over a Workflow reads its YAML at a ref and the
	// repository's environments (DispatchFetch), triggers the workflow_dispatch through the shared
	// ops engine (DispatchOps), and remembers last-used inputs in the local-store (DispatchStore)
	// (workflow-dispatch R5, R7, R16, R25). main.go wires all three over the same client, ops and
	// store the rest of the tool uses.
	DispatchFetch dispatch.Fetcher
	DispatchOps   dispatch.Dispatcher
	DispatchStore dispatch.DocStore
	// LogFetch reads one Job's log and LogExport downloads the whole-Run archive, both for the
	// log view the Feed's detail pane opens over a Job (log-viewer R1, R11). main.go wires them
	// over the shared client; the log-deletion planner reuses Ops, the one mutation entry.
	LogFetch  logview.Fetch
	LogExport logview.Exporter
	// The approvals decision pane the Feed opens over an awaiting Run runs its two writes through
	// the shared ops engine (Approver) and reads a Run's pending deployments over the shared client
	// (ApprovalFetch) (approvals R11, R12). main.go wires both, so an approve and a review are paced
	// and travel ops's write path exactly as every other write does.
	Approver      approval.Approver
	ApprovalFetch approval.Fetcher
}

// Model is the root. It holds the three tabs, the focused index, and the seams it pulls
// on the coarse tick.
type Model struct {
	tabs    []tab
	active  int
	width   int
	height  int
	profile keys.Profile

	updates     <-chan scheduler.Update
	readout     func() governor.Readout
	repos       func() []domain.Repo
	revalidated func() time.Time

	lastReadout governor.Readout
	haveReadout bool

	// settings is the root's own pane, opened over whichever tab is focused on the Settings
	// key (ADR-0011: a setting reachable from any tab cannot belong to one). It is not a tab
	// and not a fourth peer; the root holds it directly and routes keys to it while it is open.
	settings settings.Model
}

// New returns the root over opts. The Feed occupies Runs and starts focused (R2); Storage and
// Workflows are the real Reclamation and Workflow-management tabs (stage 11), each fanning one
// request out over the account's discovered repositories and gating its mutation on the same
// capability data the Feed's gate reads.
func New(opts Options) Model {
	f := feed.New(feed.Options{
		Profile:       opts.Profile,
		SetViewport:   opts.SetViewport,
		DetailFetch:   opts.DetailFetch,
		Clock:         opts.Clock,
		Ops:           opts.Ops,
		LogFetch:      opts.LogFetch,
		LogExport:     opts.LogExport,
		Approver:      opts.Approver,
		ApprovalFetch: opts.ApprovalFetch,
	})
	// The Storage tab shares the account's discovered repositories with the Feed's gate: it
	// fans one cache-usage request out over them (R0) and reads their permissions and
	// archived flags to gate reclamation (R20). It reads the same Repos pull the root
	// broadcasts, so a repository unknown to discovery is unknown to both.
	st := storage.New(storage.Options{
		Profile: opts.Profile,
		Fetch:   opts.StorageFetch,
		Repos:   opts.Repos,
		Ops:     opts.StorageOps,
	})
	// The Workflows tab reads the same discovered repositories: it fans one Workflow-list
	// request out over them (R0) and reads their permissions and archived flags to gate enable
	// and disable (R6). A toggle travels the shared ops engine, so it is paced and travels the
	// one write path exactly as the Feed's and Storage's mutations do (R5).
	wf := workflows.New(workflows.Options{
		Profile:       opts.Profile,
		Fetch:         opts.WorkflowFetch,
		Repos:         opts.Repos,
		Ops:           opts.WorkflowOps,
		DispatchFetch: opts.DispatchFetch,
		DispatchOps:   opts.DispatchOps,
		DispatchStore: opts.DispatchStore,
	})
	// The Settings pane is the root's, constructed once over the resolved config and the
	// persister so it is the authority for the running instance (R17): it edits its own copy
	// and writes changed keys back, and does not re-read the file while running.
	set := settings.New(opts.Profile, opts.Config, opts.SaveSettings)
	return Model{
		tabs: []tab{
			feedTab{m: f.SetActive(true)},
			workflowsTab{m: wf},
			storageTab{m: st},
		},
		active:      0,
		profile:     opts.Profile,
		updates:     opts.Updates,
		readout:     opts.Readout,
		repos:       opts.Repos,
		revalidated: opts.Revalidated,
		settings:    set,
	}
}

// Init starts the engine adapter and the coarse tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.listen(), tickCmd())
}

// listen is ADR-0015's receive-one-then-reschedule adapter: it blocks on the engine
// channel, returns the received event as a message, and Update re-issues it. A closed
// channel is the root's quit. A nil channel (a headless test) yields no command.
func (m Model) listen() tea.Cmd {
	ch := m.updates
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return schedulerClosedMsg{}
		}
		return u
	}
}

// tickCmd schedules the next coarse pull.
func tickCmd() tea.Cmd {
	return tea.Tick(readoutTick, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update routes one message. Size and data reach every tab; a key reaches exactly the
// focused tab after the root has taken the global navigation keys; the engine event is
// broadcast and the adapter re-armed; the tick pulls and broadcasts the Readout and the
// other polled status; a closed channel quits.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		inner := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - tabBarHeight}
		// The Settings pane lays out within the same inner size the tabs get, so it is sized
		// whether or not it is open when the terminal resizes.
		m.settings, _ = m.settings.Update(inner)
		return m.broadcast(inner)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case scheduler.Update:
		next, cmd := m.broadcast(msg)
		// Pull the Readout on the engine event too, not only the coarse tick (ADR-0015: the
		// root pulls whenever an engine event arrives and on the tick), so a pressure or
		// exhaustion transition during active traffic surfaces at once rather than up to a
		// tick late.
		next, rcmd := next.pullReadout()
		return next, tea.Batch(cmd, rcmd, next.listen())

	case schedulerClosedMsg:
		return m, tea.Quit

	case tickMsg:
		return m.onTick()

	default:
		return m.broadcast(msg)
	}
}

// handleKey takes the global navigation keys from the registry, then routes everything
// else to the focused tab alone (ADR-0011). Two tabs acting on one keystroke is the bug
// the final clause prevents.
//
// While the focused tab is capturing text input (the Feed's filter), the root takes no
// global key but the terminal interrupt: a created: date is all digits, and a digit, q or
// comma typed into the filter must be its text, not a tab switch, a quit or a settings open
// (R7, R23). ctrl+c stays unconditional because the terminal sends it as SIGINT, and it is
// the one Quit key that is never filter text.
func (m Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if isInterrupt(k) {
		return m, tea.Quit
	}
	// The Settings pane is the root's, and while it is open it is the sole key target: the
	// root routes every key but the interrupt to it and takes no global key, so esc closes it,
	// its own edit keys reach it, and a tab switch or a quit key does not fire on the tab
	// underneath (ADR-0011: focus resolution's one exception is settings).
	if m.settings.IsOpen() {
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(k)
		return m, cmd
	}
	if m.tabs[m.active].CapturesInput() {
		return m.routeKeyToActive(k)
	}
	switch {
	case key.Matches(k, m.profile.Quit):
		return m, tea.Quit
	case key.Matches(k, m.profile.NextTab):
		return m.switchTab(m.active + 1), nil
	case key.Matches(k, m.profile.PrevTab):
		return m.switchTab(m.active - 1), nil
	case key.Matches(k, m.profile.SelectTab):
		if idx, ok := tabIndex(k); ok {
			return m.switchTab(idx), nil
		}
		return m, nil
	case key.Matches(k, m.profile.Settings):
		// Open the root's Settings pane over the focused tab (ADR-0011, R2). From here every
		// key routes to the pane until esc closes it.
		m.settings = m.settings.Open()
		return m, nil
	}
	return m.routeKeyToActive(k)
}

// isInterrupt reports whether k is the terminal's SIGINT (ctrl+c). It quits unconditionally,
// even while a tab holds text-input focus, so it is recognised by its physical form rather
// than routed through the registry's Quit binding: that binding also carries q, and q is
// filter text mid-filter while ctrl+c never is (R7). ctrl+c is still in the registry and
// AC18 still enumerates it there; this only disambiguates the one member of that binding
// that must survive input capture.
func isInterrupt(k tea.KeyPressMsg) bool {
	return k.Mod&tea.ModCtrl != 0 && (k.Code == 'c' || k.Code == 'C')
}

// switchTab moves focus, wrapping for next and previous. The tab losing focus is told so
// it applies what it deferred (R10), and the tab gaining focus is told so it freezes its
// frame again.
func (m Model) switchTab(idx int) Model {
	n := len(m.tabs)
	idx = ((idx % n) + n) % n
	if idx == m.active {
		return m
	}
	m.tabs[m.active] = m.tabs[m.active].SetActive(false)
	m.active = idx
	m.tabs[m.active] = m.tabs[m.active].SetActive(true)
	return m
}

// routeKeyToActive sends a key press to the focused tab only (ADR-0011).
func (m Model) routeKeyToActive(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t, cmd := m.tabs[m.active].Update(k)
	m.tabs[m.active] = t
	return m, cmd
}

// broadcast sends a message to every tab, so size, data and the Budget Readout reach all
// three (ADR-0011). It threads the model and gathers the tabs' commands.
func (m Model) broadcast(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for i := range m.tabs {
		t, cmd := m.tabs[i].Update(msg)
		m.tabs[i] = t
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// onTick pulls the Readout and the other polled status and broadcasts what changed, then
// re-arms the tick. The Readout is broadcast on change (ADR-0015); the repositories are
// idempotent in the Feed and cheap to pull, so they ride the tick. The revalidation instant
// is a disk scan, read only when the Budget is under pressure or exhausted, which is the
// only time a paused Feed shows it (R30): idle scans nothing (R28's spirit).
func (m Model) onTick() (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{tickCmd()}

	var rcmd tea.Cmd
	m, rcmd = m.pullReadout()
	if rcmd != nil {
		cmds = append(cmds, rcmd)
	}
	if m.repos != nil {
		if repos := m.repos(); len(repos) > 0 {
			var c tea.Cmd
			m, c = m.broadcast(feed.ReposDiscovered(repos))
			cmds = append(cmds, c)
		}
	}
	// The revalidation instant is a disk scan the store performs. Defer it into a Cmd so
	// Model.Update does no filesystem I/O, matching how publishViewport already defers its
	// work (code review). It is deferred only under pressure or exhaustion, when a paused
	// Feed states what it is showing and as of when (R30).
	if m.revalidated != nil && m.haveReadout && (m.lastReadout.Pressure || m.lastReadout.Exhausted) {
		cmds = append(cmds, m.revalidateCmd())
	}
	return m, tea.Batch(cmds...)
}

// pullReadout reads the Budget Readout and broadcasts it to every tab when it differs from
// the last one sent (ADR-0015). Four comparable fields make change detection one ==. It
// threads the model and returns the broadcast Cmd, nil when nothing changed or no readout
// getter is wired (a headless test).
func (m Model) pullReadout() (Model, tea.Cmd) {
	if m.readout == nil {
		return m, nil
	}
	r := m.readout()
	if m.haveReadout && r == m.lastReadout {
		return m, nil
	}
	m.lastReadout = r
	m.haveReadout = true
	return m.broadcast(r)
}

// revalidateCmd defers the store's last-revalidated scan off the Update loop and into a
// Cmd, matching publishViewport, because the scan globs and reads the local store and
// Model.Update must stay pure and non-blocking (code review). The instant it finds is
// delivered back as a feed.RevalidatedAt and broadcast on the next loop; a zero instant
// (nothing revalidated yet) yields no message.
func (m Model) revalidateCmd() tea.Cmd {
	rev := m.revalidated
	return func() tea.Msg {
		at := rev()
		if at.IsZero() {
			return nil
		}
		return feed.RevalidatedAt(at)
	}
}

// View composes the tab bar over the focused tab's content and sets the terminal-wide
// fields the tab contract reserves for the root (ADR-0011).
func (m Model) View() tea.View {
	// The Settings pane, when open, occupies the body over whichever tab is focused, with the
	// tab bar kept for context (ADR-0011: it is opened over a tab, not as a fourth peer).
	body := m.tabs[m.active].View()
	if m.settings.IsOpen() {
		body = m.settings.View()
	}
	content := lipgloss.JoinVertical(lipgloss.Left, m.tabBar(), body)
	return tea.View{
		Content:     content,
		AltScreen:   true,
		WindowTitle: "gh-runs",
	}
}

// tabBar renders the three tab labels, the focused one highlighted (R2).
func (m Model) tabBar() string {
	parts := make([]string, 0, len(m.tabs))
	for i, t := range m.tabs {
		label := " " + t.Title() + " "
		if i == m.active {
			parts = append(parts, styleActiveTab.Render(label))
		} else {
			parts = append(parts, styleInactiveTab.Render(label))
		}
	}
	return strings.Join(parts, " ")
}

// tabIndex maps the SelectTab press to a tab position. The binding matched from the
// registry already (R7a); this only reads which of its three keys it was.
func tabIndex(k tea.KeyPressMsg) (int, bool) {
	switch k.String() {
	case "1":
		return 0, true
	case "2":
		return 1, true
	case "3":
		return 2, true
	}
	return 0, false
}

var (
	styleActiveTab   = lipgloss.NewStyle().Bold(true).Reverse(true)
	styleInactiveTab = lipgloss.NewStyle().Faint(true)
)

// feedTab lifts the Feed into the tab interface (ADR-0011). The Feed occupies Runs.
type feedTab struct{ m feed.Model }

func (t feedTab) Update(msg tea.Msg) (tab, tea.Cmd) {
	nm, cmd := t.m.Update(msg)
	return feedTab{nm}, cmd
}
func (t feedTab) View() string         { return t.m.View() }
func (t feedTab) SetActive(a bool) tab { return feedTab{t.m.SetActive(a)} }
func (t feedTab) Title() string        { return "Runs" }
func (t feedTab) CapturesInput() bool  { return t.m.CapturesInput() }

// workflowsTab lifts the Workflows tab into the tab interface (ADR-0011). It lists the
// Workflows across the discovered repositories and enables or disables one from the cursor
// (workflow-management R1, R5). A reversible toggle opens no modal, so it never captures
// input, and the root's global keys stay live over it.
type workflowsTab struct{ m workflows.Model }

func (t workflowsTab) Update(msg tea.Msg) (tab, tea.Cmd) {
	nm, cmd := t.m.Update(msg)
	return workflowsTab{nm}, cmd
}
func (t workflowsTab) View() string         { return t.m.View() }
func (t workflowsTab) SetActive(a bool) tab { return workflowsTab{t.m.SetActive(a)} }
func (t workflowsTab) Title() string        { return "Workflows" }
func (t workflowsTab) CapturesInput() bool  { return t.m.CapturesInput() }

// storageTab lifts the Storage tab into the tab interface (ADR-0011). It is the Reclamation
// surface: a per-repository rollup and a merged Cache-and-Artifact list, from which a
// selection is deleted through the shared confirmation. It captures input while its
// confirmation modal is up, exactly as the Feed does, so a typed count is not stolen as a
// global key (R7).
type storageTab struct{ m storage.Model }

func (t storageTab) Update(msg tea.Msg) (tab, tea.Cmd) {
	nm, cmd := t.m.Update(msg)
	return storageTab{nm}, cmd
}
func (t storageTab) View() string         { return t.m.View() }
func (t storageTab) SetActive(a bool) tab { return storageTab{t.m.SetActive(a)} }
func (t storageTab) Title() string        { return "Storage" }
func (t storageTab) CapturesInput() bool  { return t.m.CapturesInput() }
