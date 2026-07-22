// Package rundetail is the pane over the Feed's selected Run: its Jobs, their Steps,
// the Run's Attempt as a badge, and their timing (run-detail Purpose). It is a pane, not
// a tab and not a tea.Model. It exposes View() string and an Update the Feed drives, and
// it is imported by feed, which opens it over the selection (ADR-0011's pane contract). It
// imports logview, the pane it opens over a Job (stage 12), and never feed, its opener, nor
// any tab (ADR-0011, BUILD-ORDER's chain: logview cannot reach back up to rundetail). The
// Jobs are held as an ordered slice a Job cursor indexes, which is what lets the operator
// descend into the pane, pick a Job, and open its log (log-viewer R1: "from Run detail").
//
// The pane fetches for itself over an injected Fetch function, constructed in main.go and
// backed by ghclient, so tests drive it with a fake and no real transport (ADR-0015). It
// owns its own loading, loaded and no-Jobs states and the Cmds that reach them: the ~150ms
// debounce on selection settle (R10), the ~3s fast-tier refresh while the Run is live
// (R13), the discard of a response whose Run is no longer selected (R11), and the pause on
// Budget exhaustion (R16). The debounce and the refresh are this pane's own tea.Ticks on
// the tea runtime's clock, never the scheduler's injected one (ADR-0015). The injected
// clock is the wall clock the timing column reads, so a golden fixes elapsed durations
// deterministically.
package rundetail

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
)

const (
	// debounceInterval is R10's selection-settle debounce. It is chosen, not measured:
	// eager fetching while arrow-keying a 100-row Feed costs one request per keystroke, and
	// this coalesces the walk to the row the cursor rests on (run-detail constraint, AC1).
	// It must not become a user-facing setting (ADR-0007's expose-intent principle).
	debounceInterval = 150 * time.Millisecond

	// refreshInterval is R13's fast tier: while the selected Run is queued or in_progress,
	// its Jobs refresh at approximately this cadence, and at no other Status (AC6).
	refreshInterval = 3 * time.Second
)

// Fetch fetches one Run's Jobs, with their Steps inline (run-detail resolved open
// question 1). It is injected at construction, backed by ghclient in main.go and by a
// fake in tests (ADR-0015). It resolves the latest Attempt's Jobs and no prior one, and
// never uses filter=all as a means of retrieving history, because none is served (R6,
// AC4): those are properties of the path ClientFetch builds, not of this signature.
type Fetch func(repo domain.RepoID, runID int64) ([]domain.Job, error)

// Options carries the pane's construction seams, filled by the Feed from the root
// (ADR-0015). A nil Fetch resolves every selection to "no Jobs yet" rather than panicking,
// which is what a Feed constructed for a golden with no transport holds. A nil Clock
// defaults to the wall clock; a test injects a fake so the timing column is deterministic.
// The Log seams are threaded down to the logview pane this one opens over a Job (stage 12):
// Profile is the keybinding set the log view matches, LogFetch reads one Job's log, LogPlanner
// freezes the log-deletion set, and LogExport downloads the whole-Run archive. All are nil in
// a golden test, where the log pane never opens.
type Options struct {
	Fetch      Fetch
	Clock      clock.Clock
	Profile    keys.Profile
	LogFetch   logview.Fetch
	LogPlanner logview.Planner
	LogExport  logview.Exporter
}

// loadState is where the pane is in fetching the selected Run's Jobs. It is deliberately
// three values, not a branch on the response's shape: an empty list, a partial one and an
// error all resolve to stateNoJobs, so the pane never renders a distinct error surface
// (R12a, AC14).
type loadState int

const (
	statePending loadState = iota // selection changed, first response not in yet (R12)
	stateLoaded                   // Jobs present, rendered under the Run (R1)
	stateNoJobs                   // fetched empty, partial or errored, treated alike (R12a)
)

// Model is the detail pane's state. run is the selected Run the Feed handed it, refreshed
// in place as its fields move so the Attempt badge tracks a re-run (R17) and the liveness
// gate reads the current Status (R13). jobs is the latest Attempt's Jobs; the pane holds
// no prior Attempt, because none is served (R5, R6).
type Model struct {
	width int
	open  bool

	fetch   Fetch
	clk     clock.Clock
	profile keys.Profile

	run       domain.Run
	haveRun   bool
	wfState   domain.State // the selected Run's Workflow State; deleted marks an Orphaned Run (R8)
	repo      domain.Repo  // the Run's repository capability, stamped by the Feed for the log-delete gate
	repoKnown bool         // discovery has recorded the capability; the delete gate fails closed otherwise

	jobs  []domain.Job
	state loadState

	fetching bool             // a fetch is issued and its result not yet in; gates the resume below
	readout  governor.Readout // R16: the pane pauses on the same Budget as the Feed

	// The log view (stage 12): a pane this one opens over a selected Job. jobCursor indexes the
	// Jobs slice, and active is the job-focus sub-mode the operator descends into to pick a Job
	// and open its log. The Jobs are already held as an ordered slice, so the cursor slots in
	// (log-viewer R1: "from Run detail"). rundetail imports logview and opens it; logview never
	// imports rundetail (ADR-0011's pane contract, BUILD-ORDER's chain).
	log       logview.Model
	active    bool // job-focus: motion moves the Job cursor and enter opens the log
	jobCursor int
}

// settleMsg fires when the debounce elapses for the Run it names (R10). It is tagged with
// the Run ID so a settle for a row the cursor has since left issues nothing (AC1). It is
// unexported: only this package can name it, so the root's broadcast reaches every tab and
// only this pane can act on it (ADR-0015's type-visibility targeting).
type settleMsg struct{ runID int64 }

// refreshMsg fires on the fast tier's tick for the Run it names (R13). Tagged with the Run
// ID so a tick for a Run no longer selected stops the loop rather than refreshing it (R14,
// AC7).
type refreshMsg struct{ runID int64 }

// jobsMsg carries a fetch result, tagged with the Run it was issued for. R11's discard is
// one comparison of this tag against the selected Run (AC2).
type jobsMsg struct {
	runID int64
	jobs  []domain.Job
	err   error
}

// New returns a pane over opts. It is closed and holds no Run until the Feed opens it. It
// constructs the log view it opens over a Job, threading that pane's seams down (stage 12).
func New(opts Options) Model {
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	return Model{
		fetch:   opts.Fetch,
		clk:     clk,
		profile: opts.Profile,
		log: logview.New(logview.Options{
			Profile:  opts.Profile,
			Fetch:    opts.LogFetch,
			Exporter: opts.LogExport,
			Planner:  opts.LogPlanner,
			Clock:    clk,
		}),
	}
}

// IsOpen reports whether the pane is showing, which the Feed reads to decide whether to
// paint it and route the broadcast messages onward (ADR-0011's recursive focus).
func (m Model) IsOpen() bool { return m.open }

// Open shows the pane over run and arms a fresh fetch (R10). Reopening over the Run last
// targeted while closed still refetches, because a selection the operator just made is a
// fresh one whatever the pane held before.
func (m Model) Open(run domain.Run) (Model, tea.Cmd) {
	m.open = true
	m.haveRun = false // force a fresh selection even if run was last targeted while closed
	return m.target(run)
}

// Close hides the pane. It leaves the held Run and Jobs in place but stops every loop: the
// settle, refresh and fetch handlers all gate on open, so a tick in flight becomes a
// no-op (R14). The Feed stops painting it (IsOpen is false), so nothing stale is shown. It
// also closes the log view and leaves job-focus, so a reopen starts from the Jobs list.
func (m Model) Close() Model {
	m.open = false
	m.active = false
	m.log = m.log.Close()
	return m
}

// Focus descends into the pane's job-focus sub-mode, where motion moves the Job cursor and the
// open key opens the selected Job's log (log-viewer R1). The Feed calls it when the operator
// presses the open-detail key a second time over an already-open pane, which is the recursive
// focus ADR-0011 describes. It is a no-op while the pane is closed.
func (m Model) Focus() Model {
	if !m.open {
		return m
	}
	m.active = true
	return m
}

// CapturesKeys reports whether the pane holds key focus, which the Feed reads to route keys
// here rather than acting on them itself (ADR-0011's recursive focus). It is true in job-focus
// and while the log view is open, so the Feed's motion and actions stand down and the pane, or
// the log view beneath it, owns the keys.
func (m Model) CapturesKeys() bool {
	return m.open && (m.active || m.log.IsOpen())
}

// CapturesInput reports whether the pane holds text-input focus, which is true while the log
// view's search input or delete confirmation is up. The Feed and the root read it up the chain
// so a typed query or a typed confirmation count is not stolen as a global key (R7).
func (m Model) CapturesInput() bool {
	return m.open && m.log.IsOpen() && m.log.CapturesInput()
}

// SetRepoCapability stamps the Run's repository capability, which the log view's delete gate
// reads (log-viewer R17). known is false when discovery has not recorded it, which keeps log
// deletion disabled (repo-discovery R8). The Feed sets it when it opens the pane and as the
// cursor moves, so the capability always matches the Run on screen.
func (m Model) SetRepoCapability(repo domain.Repo, known bool) Model {
	m.repo = repo
	m.repoKnown = known
	return m
}

// HandleKey drives the pane while it holds key focus (CapturesKeys), returning the pane and
// any Cmd. The Feed swallows the key whether or not the pane acts on it, because focus is the
// pane's while it captures (ADR-0011's recursive focus). When the log view is open, every key
// reaches it, including its own fold, timestamp, search, delete, export and close keys. In
// job-focus, motion moves the Job cursor, the open key opens the selected Job's log
// (log-viewer R1), and the close key leaves job-focus back to the Feed-driven detail.
func (m Model) HandleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.log.IsOpen() {
		var cmd tea.Cmd
		m.log, cmd, _ = m.log.HandleKey(k)
		return m, cmd
	}
	if !m.active {
		return m, nil
	}
	switch {
	case key.Matches(k, m.profile.CloseDetail): // esc leaves job-focus, back to the Feed-driven detail
		m.active = false
	case key.Matches(k, m.profile.OpenDetail): // enter opens the selected Job's log (log-viewer R1)
		return m.openLog()
	case key.Matches(k, m.profile.RowUp):
		m.moveJobCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveJobCursor(1)
	case key.Matches(k, m.profile.PageUp):
		m.moveJobCursor(-1)
	case key.Matches(k, m.profile.PageDown):
		m.moveJobCursor(1)
	case key.Matches(k, m.profile.FirstRow):
		m.jobCursor = 0
	case key.Matches(k, m.profile.LastRow):
		m.jobCursor = len(m.jobs) - 1
		m.clampJobCursor()
	}
	return m, nil
}

// openLog opens the log view over the Job under the cursor (log-viewer R1: "from Run detail").
// It arms the log fetch and passes the Run's repository capability so the log-delete gate
// resolves. With no Job under the cursor it is a no-op.
func (m Model) openLog() (Model, tea.Cmd) {
	if m.jobCursor < 0 || m.jobCursor >= len(m.jobs) {
		return m, nil
	}
	var cmd tea.Cmd
	m.log, cmd = m.log.Open(m.run, m.jobs[m.jobCursor], m.repo, m.repoKnown)
	return m, cmd
}

// moveJobCursor moves the Job cursor by delta, clamped to the Jobs slice.
func (m *Model) moveJobCursor(delta int) {
	m.jobCursor += delta
	m.clampJobCursor()
}

// clampJobCursor keeps the Job cursor within the Jobs slice, or at zero when it is empty.
func (m *Model) clampJobCursor() {
	if len(m.jobs) == 0 {
		m.jobCursor = 0
		return
	}
	if m.jobCursor < 0 {
		m.jobCursor = 0
	}
	if m.jobCursor >= len(m.jobs) {
		m.jobCursor = len(m.jobs) - 1
	}
}

// SelectRun re-points the pane at the Feed's current selection while it is open, and is
// the Feed's report of where its cursor is (ADR-0011: "feed reports where its cursor is").
// It is a no-op while closed. A change of Run ID is a fresh selection; the same ID is a
// refresh of the held fields, so following the cursor onto the row it already rests on
// issues nothing (R10, AC1).
func (m Model) SelectRun(run domain.Run) (Model, tea.Cmd) {
	if !m.open {
		return m, nil
	}
	return m.target(run)
}

// SetWorkflowState records the selected Run's Workflow State, whose deleted value marks an
// Orphaned Run (R8, AC11). It is separate from SelectRun because a Run does not carry its
// Workflow's State, and the Feed sets it only when it has resolved one; until then the
// state is unknown, which reads as not-deleted (a Run with a live successor).
func (m Model) SetWorkflowState(s domain.State) Model {
	m.wfState = s
	return m
}

// IsOrphaned reports whether the selected Run's Workflow is deleted, which makes it an
// Orphaned Run: no further Run can follow it. run-detail R18 excludes an Orphaned Run from
// the re-run gate, because a deleted Workflow can produce no further Run, so the Feed reads
// this to keep the re-run key inert while the pane is open over one (run-detail R18, AC15,
// and its resolved open question 8). It reads the Workflow State the Feed resolved via
// SetWorkflowState; an unresolved state reads as not-deleted, a Run with a live successor.
func (m Model) IsOrphaned() bool { return m.wfState == domain.StateDeleted }

// Update handles one message the Feed forwarded. It consumes its own tagged messages, the
// size it lays out against, and the broadcast Budget Readout it pauses on (R16, ADR-0015).
// A key press does not arrive here: the Feed routes keys to HandleKey when this pane holds
// focus (recursive focus), so the two paths never collide. Any message the pane does not own,
// including the log view's own tagged fetch and export results, is forwarded to the log pane,
// whose messages are unexported and so only it can name (ADR-0015's type-visibility targeting).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		var cmd tea.Cmd
		m.log, cmd = m.log.Update(msg) // keep the log pane laid out even while closed
		return m, cmd
	case governor.Readout:
		return m.onReadout(msg)
	case settleMsg:
		return m.onSettle(msg)
	case refreshMsg:
		return m.onRefresh(msg)
	case jobsMsg:
		return m.onJobs(msg)
	}
	// A message the pane does not consume is forwarded to the log view: its logFetchedMsg and
	// exportDoneMsg reach here as broadcasts, and only that package can name them (ADR-0015).
	var cmd tea.Cmd
	m.log, cmd = m.log.Update(msg)
	return m, cmd
}

// target points the pane at run. A change of Run ID is a fresh selection: it clears the
// previous Run's Jobs to the pending state so the previous Run's Jobs are never shown under
// the new identity (R11, R12), and arms the debounce (R10). The same Run ID refreshes the
// held fields in place, so the badge tracks a re-run (R17) and the liveness gate reads the
// current Status (R13) without re-arming the debounce or clearing the Jobs.
func (m Model) target(run domain.Run) (Model, tea.Cmd) {
	if m.haveRun && m.run.ID == run.ID {
		m.run = run // refresh in place; no re-fetch (R10, R17)
		return m, nil
	}
	m.run = run
	m.haveRun = true
	m.wfState = "" // the deleted marker is per-Run; a new selection starts unknown (R8)
	m.jobs = nil
	m.state = statePending // R12: pending, never the previous Run's Jobs
	// A new Run resets the Job cursor and leaves any log view: the previous Run's Job and its
	// log are stale under the new identity (log-viewer R1). A cursor move that opened a log
	// would have captured keys and frozen the Feed cursor, so this path is defensive.
	m.jobCursor = 0
	m.active = false
	m.log = m.log.Close()
	if !m.open {
		return m, nil
	}
	return m, debounceCmd(run.ID)
}

// onSettle issues the debounced fetch, but only if the pane still holds the settled Run
// (R10). Arrow-keying past a row whose settle never matched the resting cursor issues
// nothing (AC1). Under Budget exhaustion it holds and the Readout clear resumes it (R16).
func (m Model) onSettle(msg settleMsg) (Model, tea.Cmd) {
	if !m.open || !m.haveRun || msg.runID != m.run.ID {
		return m, nil
	}
	if m.readout.Exhausted {
		return m, nil // paused; resumes on the Readout clear (R16)
	}
	return m.issueFetch()
}

// onRefresh re-fetches on the fast tier's tick, but only while the pane still holds this
// Run and the Run is still live (R13). A completed Run or a deselected one stops the loop
// (R14, AC6, AC7). Under exhaustion it stops and the Readout clear resumes it (R16, AC12).
func (m Model) onRefresh(msg refreshMsg) (Model, tea.Cmd) {
	if !m.open || !m.haveRun || msg.runID != m.run.ID || !live(m.run) {
		return m, nil
	}
	if m.readout.Exhausted {
		return m, nil // paused; the Readout clear resumes it (R16, AC12)
	}
	return m.issueFetch()
}

// onJobs consumes a fetch result. A response whose Run is no longer selected is discarded,
// one comparison and no render, so Run A's Jobs never appear under Run B (R11, AC2). An
// empty list and an error resolve alike to "no Jobs yet", so the pane never branches a
// distinct error surface (R12a, AC14); a Run whose Jobs have appeared shows them, including
// a queued Run whose Jobs the fast tier just revealed (AC14). While the Run stays live the
// fast tier re-arms (R13); a completed Run arms nothing, so the loop ends (AC6).
func (m Model) onJobs(msg jobsMsg) (Model, tea.Cmd) {
	m.fetching = false // a fetch round trip has completed, stale or not
	if !m.open || !m.haveRun || msg.runID != m.run.ID {
		return m, nil // stale: the selection moved on (R11, AC2)
	}
	if msg.err != nil || len(msg.jobs) == 0 {
		m.jobs = nil
		m.state = stateNoJobs
	} else {
		m.jobs = msg.jobs
		m.state = stateLoaded
	}
	m.clampJobCursor() // a refetch can change how many Jobs there are (log-viewer R1)
	if live(m.run) && !m.readout.Exhausted {
		return m, refreshTick(m.run.ID)
	}
	return m, nil
}

// onReadout records the broadcast Budget Readout and never re-derives it (ADR-0015): the
// pane reads the exhaustion flag the governor published. When exhaustion clears it resumes
// a paused selection, so a Run selected or refreshed under exhaustion is not stranded: a
// live Run re-arms its fast tier and a not-yet-loaded Run issues its held fetch (R16, AC12).
// It resumes only when no fetch is outstanding: an exhaust/recover flip within one round trip
// would otherwise inject a fetch while the in-flight one still re-arms the tier on arrival,
// leaving two concurrent fast-tier loops (code review).
func (m Model) onReadout(r governor.Readout) (Model, tea.Cmd) {
	was := m.readout.Exhausted
	m.readout = r
	if was && !r.Exhausted && m.open && m.haveRun && !m.fetching && m.needsFetch() {
		return m.issueFetch()
	}
	return m, nil
}

// needsFetch reports whether a resume after exhaustion has work to do: a live Run's fast
// tier is due to re-arm, or a Run whose Jobs never loaded owes its first fetch (R16).
func (m Model) needsFetch() bool {
	return live(m.run) || m.state != stateLoaded
}

// live reports whether a Run's Jobs refresh at the fast tier, which is exactly while it is
// queued or in_progress and at no other Status (R13). A Run parked at waiting, requested or
// pending updates at its repository's ambient tier, not here (ADR-0021).
func live(r domain.Run) bool {
	return r.Status == domain.StatusQueued || r.Status == domain.StatusInProgress
}

// issueFetch marks a fetch outstanding and returns the Cmd that runs it. Every issue site
// routes through here, so the outstanding-fetch flag is set at all of them and onReadout's
// resume can tell whether a fetch is already circulating (code review). The flag clears in
// onJobs when the result arrives.
func (m Model) issueFetch() (Model, tea.Cmd) {
	m.fetching = true
	return m, m.fetchCmd(m.run)
}

// fetchCmd runs the injected Fetch off the Update loop and tags its result with the Run ID
// (R11). A nil Fetch resolves to an empty result rather than panicking, which is what a
// Feed built for a golden with no transport holds.
func (m Model) fetchCmd(run domain.Run) tea.Cmd {
	fetch := m.fetch
	repo, id := run.Repo, run.ID
	if fetch == nil {
		return func() tea.Msg { return jobsMsg{runID: id} }
	}
	return func() tea.Msg {
		jobs, err := fetch(repo, id)
		return jobsMsg{runID: id, jobs: jobs, err: err}
	}
}

// debounceCmd is R10's selection-settle debounce, this pane's own tea.Tick on the tea
// runtime's clock, not the scheduler's injected one (ADR-0015). Only the settle whose Run
// still matches the cursor issues a fetch, so arrow-keying coalesces to one request (AC1).
func debounceCmd(runID int64) tea.Cmd {
	return tea.Tick(debounceInterval, func(time.Time) tea.Msg { return settleMsg{runID: runID} })
}

// refreshTick is R13's fast tier, likewise this pane's own tea.Tick. It re-arms only while
// the Run is live (onRefresh, onJobs), so a completed Run's loop ends (AC6).
func refreshTick(runID int64) tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshMsg{runID: runID} })
}
