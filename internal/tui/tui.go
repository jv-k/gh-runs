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
// A tab that wants another tab shown asks the root, which is why the Workflows tab's
// navigation to a deleted Workflow's Orphaned Runs arrives here as a message rather than
// reaching into the Feed (workflow-management R13, AC4). Focus and cross-tab delivery are
// the root's, because a tab may import a pane and never another tab.
//
// A launched write's progress travels a second channel of its own, adapted with the same
// receive-one loop and broadcast the same way, because a Purge outlives the operator's
// attention and must keep painting whichever tab is focused (ADR-0015). The root owns the
// surface it paints into for that reason, as it owns settings for the parallel one, and
// paints it as a strip above the focused tab rather than a modal over it, because purge
// R14 forbids a Purge from being modal.
//
// tui imports the tabs, the engine's event and Readout types, ops, keys and domain, and
// lipgloss for the tab bar. main.go constructs it and wires the channels and the pulls;
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
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
	"github.com/jv-k/gh-runs/v2/internal/tui/feed"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	"github.com/jv-k/gh-runs/v2/internal/tui/rundetail"
	"github.com/jv-k/gh-runs/v2/internal/tui/running"
	"github.com/jv-k/gh-runs/v2/internal/tui/settings"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// tabBarHeight is the one line the root reserves for the tab bar, taken off the height
// the tabs receive so a tab lays out within the space it actually gets (R4a).
const tabBarHeight = 1

// feedTabIndex is the Feed's position among the tabs, the Runs tab New builds first and
// focuses at launch (R2). A cross-tab navigation to the Feed names it here rather than
// searching for the tab by title, because the order is the root's own construction.
const feedTabIndex = 0

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

// progressFrame is one frame of a launched operation's stream, tagged with the channel it
// came from. The tag is the root's alone and never leaves it: the tabs receive the bare
// ops.Progress, which is what ADR-0015's catalog names. It exists so a frame from a stream
// the root has already replaced can be discarded rather than applied to its successor,
// which is the same discard-by-tag rule the detail pane uses for a stale response.
type progressFrame struct {
	stream <-chan ops.Progress
	p      ops.Progress
}

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
// scheduler's medium-tier control, SetFilter hands it the Feed's active filter to push
// server-side (R22), and the profile is the resolved keybinding set.
type Options struct {
	Updates     <-chan scheduler.Event
	Readout     func() governor.Readout
	Repos       func() []domain.Repo
	Revalidated func() time.Time
	SetViewport func([]domain.RepoID)
	SetFilter   func(filter.Filter)
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
	// StorageDownload writes the Artifact under the Storage tab's cursor to disk
	// (storage-reclamation R13). It is a seam of its own rather than a mode of StorageOps,
	// because a download is a GET that destroys nothing: it routes through no confirmation
	// and writes no deletion-log line, and R14's expired Artifact is refused here rather than
	// discovered by a request.
	StorageDownload storage.Downloader
	// StorageScope is the set of repositories the Storage tab covers, all-repos or this-repo,
	// and StorageCurrentRepo resolves what this-repo means (storage-reclamation R0, settings
	// R19). They are the Workflows tab's pair applied to the other scoped surface, and they
	// are chosen once at construction for the same reason: narrowing the scope while running
	// also means dropping the held storage, because the tab accumulates each repository as it
	// arrives and would otherwise leave the wider scope's rows and its rollup on screen.
	StorageScope       storage.Scope
	StorageCurrentRepo func() (domain.RepoID, bool)
	// WorkflowFetch reads one repository's Workflow list for the Workflows tab, and WorkflowOps
	// enables or disables one Workflow through the shared ops engine (workflow-management R1,
	// R5). main.go wires both over the same client and ops, so a toggle is paced by the
	// governor and travels the one write path exactly as every other write does.
	WorkflowFetch workflows.Fetch
	WorkflowOps   workflows.Toggler
	// WorkflowScope is the set of repositories the Workflows tab covers, all-repos or
	// this-repo, and WorkflowCurrentRepo resolves what this-repo means (workflow-management
	// R0, settings R19). The zero scope is all-repos, the default R0 fixes, and it is what
	// main.go states: the setting that selects the other is settings R19's and is not built.
	// The resolver is wired regardless, so the scope is chosen once, here, at construction.
	//
	// Making it changeable while running needs two things this does not have: a setter on the
	// tab, and a reset of the held list when the scope changes, because the tab merges each
	// repository's Workflows as they arrive and a narrowed scope never removes the rows a
	// wider one left behind. Both belong with settings R19, which is what will call them.
	WorkflowScope       workflows.Scope
	WorkflowCurrentRepo func() (domain.RepoID, bool)
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
	// Retrier re-attempts a finished operation's recorded failures, purge R22's keystroke.
	// main.go wires it to the shared ops engine, which is the authority that the retry set
	// is a subset of an already-confirmed one; a headless test leaves it nil and the
	// summary then offers no retry.
	Retrier running.Retrier
}

// Model is the root. It holds the three tabs, the focused index, and the seams it pulls
// on the coarse tick.
type Model struct {
	tabs    []tab
	active  int
	width   int
	height  int
	profile keys.Profile

	updates     <-chan scheduler.Event
	readout     func() governor.Readout
	repos       func() []domain.Repo
	revalidated func() time.Time

	lastReadout governor.Readout
	haveReadout bool

	// settings is the root's own pane, opened over whichever tab is focused on the Settings
	// key (ADR-0011: a setting reachable from any tab cannot belong to one). It is not a tab
	// and not a fourth peer; the root holds it directly and routes keys to it while it is open.
	settings settings.Model

	// running is the running-operation surface (purge R15, R16, R22). It is the root's for
	// the same reason settings is: a launched Purge outlives the operator's attention and
	// must keep painting whichever tab is focused (ADR-0015), and no tab can own a strip
	// that has to survive a tab switch. It is a strip above the focused tab rather than a
	// modal, because R14 forbids a Purge from being one.
	running running.Model
	// progress is the launched operation's stream, adapted with the same receive-one-then-
	// reschedule command as the engine's channel (ADR-0015). It is a separate channel and
	// deliberately not the engine's: riding progress on that one would make the scheduler a
	// courier for ops.
	progress <-chan ops.Progress
	// stripHeight is how many rows the running surface currently occupies, which comes off
	// what the tabs are laid out in. It is tracked so the root re-broadcasts a size only
	// when the strip's height actually changes, rather than on every frame.
	stripHeight int

	// terminalDark is what the terminal last reported about its own background, which the
	// auto theme derives its palette from (settings R6). It starts dark, which is gh's
	// fallback and the set every view carried before the theme existed, and the terminal's
	// answer replaces it as soon as it arrives.
	terminalDark bool
}

// New returns the root over opts. The Feed occupies Runs and starts focused (R2); Storage and
// Workflows are the real Reclamation and Workflow-management tabs (stage 11), each fanning one
// request out over the account's discovered repositories and gating its mutation on the same
// capability data the Feed's gate reads.
func New(opts Options) Model {
	f := feed.New(feed.Options{
		Profile:     opts.Profile,
		SetViewport: opts.SetViewport,
		SetFilter:   opts.SetFilter,
		// The Feed opens on the settings' launch filter (settings R9, AC3), which is already
		// resolved: config.Load applied the flag over the file over the default before this
		// value reached the root. There is no second precedence here, and there must not be.
		Filter: opts.Config.LaunchFilter,
		// The STARTED column's rendering is not set here. It is a live setting, so it travels
		// applyTimestamp below, which reads the Settings pane and is the only place the root
		// resolves it. Construction goes through that same path so there is one read.
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
		Profile:  opts.Profile,
		Fetch:    opts.StorageFetch,
		Repos:    opts.Repos,
		Ops:      opts.StorageOps,
		Download: opts.StorageDownload,

		Scope:       opts.StorageScope,
		CurrentRepo: opts.StorageCurrentRepo,
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
		Scope:         opts.WorkflowScope,
		CurrentRepo:   opts.WorkflowCurrentRepo,
		DispatchFetch: opts.DispatchFetch,
		DispatchOps:   opts.DispatchOps,
		DispatchStore: opts.DispatchStore,
	})
	// The Settings pane is the root's, constructed once over the resolved config and the
	// persister so it is the authority for the running instance (R17): it edits its own copy
	// and writes changed keys back, and does not re-read the file while running.
	set := settings.New(opts.Profile, opts.Config, opts.SaveSettings)
	m := Model{
		tabs: []tab{
			feedTab{m: f.SetActive(true)},
			workflowsTab{m: wf},
			storageTab{m: st},
		},
		active:       0,
		profile:      opts.Profile,
		updates:      opts.Updates,
		readout:      opts.Readout,
		repos:        opts.Repos,
		revalidated:  opts.Revalidated,
		settings:     set,
		running:      running.New(opts.Profile).WithRetrier(opts.Retrier),
		terminalDark: true,
	}
	// Both live settings are applied at construction, so the first frame is already painted in
	// the theme and the timestamp form the config resolved rather than repainting after it
	// (R6, R10). Reading them off the pane here is what makes launch and every later frame one
	// path instead of two.
	return m.applyPalette().applyTimestamp()
}

// applyPalette resolves the theme the Settings pane holds against the terminal's reported
// background and makes it the palette every view paints with (settings R6). The pane is the
// authority for the running instance (R17), so this reads its config rather than the one the
// root was constructed with, which is what makes a theme change apply from the next frame.
func (m Model) applyPalette() Model {
	palette.Use(palette.ResolveAppearance(m.settings.Config().Theme, m.terminalDark))
	return m
}

// applyTimestamp pushes the STARTED column's rendering the Settings pane holds into the Feed
// (settings R10), and is applyPalette's sibling: the pane is the authority for the running
// instance (R17), so this reads its config rather than the one the root was constructed with,
// which is what makes a timestamp change apply from the next frame.
//
// It is a push rather than a message because the palette's ambience does not generalise. A
// palette reaches every view without travelling, and a tab's own field cannot. Pushing it
// from the root here adds no message class and no routing rule, so nothing changes about
// which messages reach an inactive tab and live-run-feed R33's background reveal is
// untouched. It also holds whether or not the Feed is the focused tab, because the root
// holds every tab and reaches this one by index.
//
// The tab's vocabulary is its own, so the config value is converted here, exactly as the two
// tab scopes are converted for their tabs (ADR-0011).
func (m Model) applyTimestamp() Model {
	ft, ok := m.tabs[feedTabIndex].(feedTab)
	if !ok {
		return m
	}
	f := feed.TimestampFormat(m.settings.Config().Timestamp)
	m.tabs[feedTabIndex] = feedTab{m: ft.m.SetTimestampFormat(f)}
	return m
}

// Init starts the engine adapter and the coarse tick, and asks the terminal for its
// background colour so the auto theme has something to derive its palette from (R6). The
// query is a message rather than a blocking read, so a terminal that never answers costs
// nothing and the dark default stands.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.listen(), tickCmd(), tea.RequestBackgroundColor)
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

// listenProgress is the same receive-one-then-reschedule adapter as listen, over the
// launched operation's stream (ADR-0015). A closed channel yields nothing: the terminal
// frame has already told the surface the pass is over, so the close is the stream's end
// rather than the program's, which is the one way this loop differs from the engine's.
func (m Model) listenProgress() tea.Cmd {
	ch := m.progress
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return progressFrame{stream: ch, p: p}
	}
}

// stripSize is the space the running surface lays out in: the terminal less the tab bar's
// line, which is all it can occupy. Handing it the whole terminal instead lets its share
// exceed what is left once the tab bar is drawn, on a terminal short enough that the
// difference is most of the screen.
func (m Model) stripSize() tea.WindowSizeMsg {
	h := m.height - tabBarHeight
	if h < 0 {
		h = 0
	}
	return tea.WindowSizeMsg{Width: m.width, Height: h}
}

// innerSize is the space the tabs are laid out in: the terminal less the tab bar's line
// and the running surface's rows, so a Purge's indicator never overlaps the list it sits
// above (R4a, R14).
func (m Model) innerSize() tea.WindowSizeMsg {
	h := m.height - tabBarHeight - m.stripHeight
	if h < 0 {
		// The strip bounds itself to a share of the terminal, so this is a floor under an
		// arithmetic that must never go negative rather than a second policy: a tab laid out
		// in negative rows is a height no terminal has, and the root would paint past the
		// bottom of the screen.
		h = 0
	}
	return tea.WindowSizeMsg{Width: m.width, Height: h}
}

// reserveStrip re-lays the tabs when the running surface's height changes, and does
// nothing when it has not. A frame lands roughly twice a second at the governor's rates,
// and re-broadcasting a size on each one would repaint three tabs for no reason.
func (m Model) reserveStrip() (Model, tea.Cmd) {
	h := m.running.Height()
	if h == m.stripHeight {
		return m, nil
	}
	m.stripHeight = h
	inner := m.innerSize()
	m.settings, _ = m.settings.Update(inner)
	return m.broadcast(inner)
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
		// The running surface lays out first, because its height is what the tabs' is
		// computed from. It is given the space below the tab bar rather than the whole
		// terminal, so the share it takes is a share of what it can actually occupy.
		m.running, _ = m.running.Update(m.stripSize())
		m.stripHeight = m.running.Height()
		inner := m.innerSize()
		// The Settings pane lays out within the same inner size the tabs get, so it is sized
		// whether or not it is open when the terminal resizes.
		m.settings, _ = m.settings.Update(inner)
		return m.broadcast(inner)

	case ops.Started:
		// ADR-0015: the initiating Cmd's first message hands the channel to the root, which
		// adapts it with the same receive-one loop as the engine's. A retry's handle arrives
		// here too, so a re-attempt is a launch like any other.
		m.running = m.running.Start(msg)
		m.progress = msg.Progress
		next, cmd := m.reserveStrip()
		return next, tea.Batch(cmd, next.listenProgress())

	case progressFrame:
		// A frame from a stream the root has already replaced is discarded, exactly as the
		// detail pane discards a response for a Run no longer selected (ADR-0015). The
		// window is narrow and real: the engine frees its launch gate just before the
		// terminal frame goes out, so a new operation can be launched while the finished
		// one's last frame is still in flight, and applying that frame would mark the new
		// operation finished before it had deleted anything.
		if msg.stream != m.progress {
			return m, nil
		}
		// Progress is broadcast, because a Purge outlives the operator's attention and must
		// keep painting whichever tab is focused (ADR-0015). The tabs see the ops type; the
		// stream tag is the root's own bookkeeping. The root's surface consumes it too, and
		// the adapter re-arms until the terminal frame.
		m.running, _ = m.running.Update(msg.p)
		next, cmd := m.reserveStrip()
		bnext, bcmd := next.broadcast(msg.p)
		cmds := []tea.Cmd{cmd, bcmd}
		if !msg.p.Done {
			cmds = append(cmds, bnext.listenProgress())
		} else {
			bnext.progress = nil
		}
		return bnext, tea.Batch(cmds...)

	case ops.LaunchFailed:
		// A refused launch is reported rather than dropped: a keystroke that silently does
		// nothing is the defect this surface exists to fix.
		m.running = m.running.Fail(msg)
		return m.reserveStrip()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.BackgroundColorMsg:
		// The terminal answered the query Init sent. An auto theme follows it, as gh does,
		// and an explicit dark or light ignores it (settings R6). It is broadcast onward
		// like any other data message, so a component that wants it is not cut off.
		m.terminalDark = msg.IsDark()
		next, cmd := m.applyPalette().broadcast(msg)
		return next, cmd

	case scheduler.Event:
		// Every member of ADR-0015's catalog travels the one channel and takes the one
		// route: broadcast to every tab, which discriminates on the concrete type. The
		// root adds no per-event knowledge, so a catalog member added in scheduler needs
		// no change here.
		next, cmd := m.broadcast(msg)
		// Pull the Readout on the engine event too, not only the coarse tick (ADR-0015: the
		// root pulls whenever an engine event arrives and on the tick), so a pressure or
		// exhaustion transition during active traffic surfaces at once rather than up to a
		// tick late.
		next, rcmd := next.pullReadout()
		return next, tea.Batch(cmd, rcmd, next.listen())

	case workflows.NavigateToRuns:
		return m.showRuns(msg)

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
	// R16 says a Purge is cancellable at any point while it runs, and "any point" includes
	// while the Settings pane is open and while a tab holds text-input focus. The strip is
	// on screen saying which key stops it throughout, so the key has to work throughout.
	//
	// This is ahead of the capture rule rather than an exception to it. That rule exists
	// because q, n and a digit are filter text and a typed count, and a ctrl chord never
	// is, which is the same reasoning that already lets ctrl+c through above. The surface
	// claims nothing while it is idle, so a tab that wants these chords still gets them.
	if m.running.Handles(k) {
		var cmd tea.Cmd
		m.running, cmd = m.running.Update(k)
		next, rcmd := m.reserveStrip()
		return next, tea.Batch(cmd, rcmd)
	}
	// The Settings pane is the root's, and while it is open it is the sole key target: the
	// root routes every key but the interrupt to it and takes no global key, so esc closes it,
	// its own edit keys reach it, and a tab switch or a quit key does not fire on the tab
	// underneath (ADR-0011: focus resolution's one exception is settings).
	if m.settings.IsOpen() {
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(k)
		// The theme and the timestamp form apply from the next frame rather than the next
		// launch (R17). Both are read back off the pane here, which is the whole mechanism:
		// the root pulls after every key it routed, and the pane pushes nothing and issues no
		// Cmd for either.
		return m.applyPalette().applyTimestamp(), cmd
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

// showRuns carries the Workflows tab's navigation across to the Feed (workflow-management
// R13, AC4): it moves focus to the Feed and delivers the filter that Workflow's Runs are
// found under. The tab named the destination and asked; both halves happen here, because a
// tab may import a pane but never another tab, so no tab can focus or narrow another
// (ADR-0011's tab contract).
//
// The filter travels the ordinary broadcast, the one route every non-key message takes, and
// only the Feed names its type (ADR-0015's type-visibility targeting), so the root needs no
// handle on the Feed beyond the tab interface the other three routes use.
func (m Model) showRuns(req workflows.NavigateToRuns) (tea.Model, tea.Cmd) {
	return m.switchTab(feedTabIndex).broadcast(feed.ShowRuns(req.Filter))
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
	parts := []string{m.tabBar()}
	// The running surface sits between the tab bar and the body, above whichever tab is
	// focused and above the Settings pane too: a Purge keeps running while either is on
	// screen, and R14 forbids it from being modal over any of them.
	if m.running.Active() {
		parts = append(parts, m.running.View())
	}
	content := lipgloss.JoinVertical(lipgloss.Left, append(parts, body)...)
	// The root paints at most the terminal it was given, whatever its children returned.
	// Every component lays out within the size it is handed, so on any usable terminal this
	// changes nothing; it is the floor that keeps a short one from being painted past its
	// bottom edge, where the tab bar and the strip's own floor are already most of it.
	if m.height > 0 {
		content = lipgloss.NewStyle().MaxHeight(m.height).Render(content)
	}
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
