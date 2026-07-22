// Package rundetail is the pane over the Feed's selected Run: its Jobs, their Steps,
// the Run's Attempt as a badge, and their timing (run-detail Purpose). It is a pane, not
// a tab and not a tea.Model. It exposes View() string and an Update the Feed drives, and
// it is imported by feed, which opens it over the selection (ADR-0011's pane contract). It
// imports nothing in tui: not feed (its opener), not logview (the pane stage 12 opens from
// it). Structured so logview slots in later without a rewrite, the Jobs are held as an
// ordered slice a Job cursor can index when that stage arrives.
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

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
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
type Options struct {
	Fetch Fetch
	Clock clock.Clock
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

	fetch Fetch
	clk   clock.Clock

	run     domain.Run
	haveRun bool
	wfState domain.State // the selected Run's Workflow State; deleted marks an Orphaned Run (R8)

	jobs  []domain.Job
	state loadState

	fetching bool             // a fetch is issued and its result not yet in; gates the resume below
	readout  governor.Readout // R16: the pane pauses on the same Budget as the Feed
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

// New returns a pane over opts. It is closed and holds no Run until the Feed opens it.
func New(opts Options) Model {
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	return Model{fetch: opts.Fetch, clk: clk}
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
// no-op (R14). The Feed stops painting it (IsOpen is false), so nothing stale is shown.
func (m Model) Close() Model {
	m.open = false
	return m
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
// A key press is the Feed's: the pane holds no binding at this stage (motion moves the
// Feed's cursor, and its close key is the Feed's), so keys never reach here.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case governor.Readout:
		return m.onReadout(msg)
	case settleMsg:
		return m.onSettle(msg)
	case refreshMsg:
		return m.onRefresh(msg)
	case jobsMsg:
		return m.onJobs(msg)
	}
	return m, nil
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
