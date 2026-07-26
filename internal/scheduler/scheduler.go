// Package scheduler is the polling scheduler: the tiers, their intervals and the
// conditional-revalidation engine over time. It turns ADR-0004's free 304s into
// liveness without tripping the secondary limit (polling-scheduler Purpose).
//
// It stands on discovery's poll set (the ~26 repositories with Runs, R2), the
// local-store's ETags (every poll is conditional, R1), and the rate-governor's
// Budget Readout (it demotes under pressure and stops at exhaustion, R13-R16). It
// never parses a rate-limit header, keeps no Budget accounting of its own and
// throttles no write (R14): it consumes the Readout and honours the Budget.
//
// The wire-concurrency bound is not the scheduler's. It is the transport limiter
// nested innermost in the chain (ADR-0018), so the scheduler spawns a goroutine per
// due poll, single-flight per repository, and holds no bound of its own. All of its
// timing comes from an injected clock, so its tests advance virtual time and
// complete in microseconds (R20-R22).
//
// scheduler imports domain, clock and governor (for the Readout type it consumes),
// and never store, discovery or tui (ADR-0011). It reaches the transport only
// through the Requester seam below, which a test fills with a cassette-backed
// ghclient.Client, and the poll set and Budget through their seams, which a test
// fills with a fake.
package scheduler

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// Event is one thing the engine learned, carried on the single channel ADR-0015
// fixes. The catalog is closed and the root broadcasts every member to every tab, so
// a consumer discriminates on the concrete type rather than on a route.
//
// The interface is unexported-method sealed: only this package can add a member, so
// the catalog cannot be widened from a consumer and a type switch over it stays
// exhaustive by construction.
type Event interface{ isSchedulerEvent() }

func (Update) isSchedulerEvent()         {}
func (RepoPollFailed) isSchedulerEvent() {}

// RepoPollFailed is one repository's failed poll (ADR-0015). It is emitted when a
// poll never reached a 200 it could read: the transport errored, the status was one
// the engine cannot use, or the body would not parse. Without it a repository whose
// polls are failing is indistinguishable from one that has not answered yet, which is
// the whole reason the catalog names it.
//
// A rate-limit 403 or 429 is never a RepoPollFailed. It is an account condition the
// governor has already folded into the Readout's exhaustion, which the loop acts on
// and the Feed already states, so surfacing it per repository would report one
// account-wide condition once per repository (ADR-0018).
//
// The event is a report, not a retry signal. The next tick retries on the repository's
// own cadence exactly as it did before, and a later Update clears the Feed's
// indicator, so a transient failure heals without anything cancelling it.
type RepoPollFailed struct {
	Repo domain.RepoID
	Err  error
}

// Update is one repository's fresh Runs, emitted when a poll returns a 200 whose
// content changed (R19, AC16). A 304 emits nothing: it carries no change and does no
// re-render work. The Runs are stamped with their Repo (ADR-0018).
//
// When the poll pushed a server-side filter (the active Filter's Query() was
// non-empty), Filtered is true and ClaimedTotal is the response's total_count, the
// claimed match count R24's cap label reads (ADR-0015, ADR-0016). An unfiltered poll
// leaves both zero: its total_count is the repository's whole count, not a claimed
// match count, so it is never a cap.
type Update struct {
	Repo         domain.RepoID
	Runs         []domain.Run
	Filtered     bool
	ClaimedTotal int
}

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is exactly ghclient.Client's surface
// (ADR-0012: Request, never Get or Do, so the response headers survive), narrowed
// to what the scheduler uses. A cassette-backed ghclient.Client fills it for the
// wire-fidelity tests; a gated fake fills it where the property under test is
// orchestration rather than the wire (R22).
//
// Stage-7 carry-forward: Request takes no context (ADR-0012), so Stop cannot cancel an
// in-flight poll (a hung connection can delay quit) and the loop learns of exhaustion
// only at its next timer fire (the pause banner can lag one tier interval). Both are
// the safe direction, no hammering. The Feed closes this when it wires the scheduler
// at stage 7, by adding Stop cancellation and a transport response-header timeout or a
// context on ghclient.Request. This is the recurring ghclient-context carry-forward.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// PollSet supplies the repositories to poll: discovery's ~26 classified as having
// Runs (R2), never the ~163-repository probe set. *discovery.Discovery satisfies
// it. The scheduler reads it on every scheduling decision, so a repository added or
// removed by a discovery re-probe enters or leaves the rotation with no restart
// (R3, AC7).
type PollSet interface {
	PollSet() []domain.RepoID
}

// Budget reports the account's rate limiting so the scheduler demotes under
// pressure (R15) and stops at exhaustion (R16), taking its figure from the Readout
// rather than from a tally of its own requests (R13). *governor.Governor satisfies
// it. The scheduler consumes the Readout and never parses a rate-limit header
// itself (R14).
type Budget interface {
	Readout() governor.Readout
}

// WorkflowLister reads one repository's Workflows, the other half of the join the
// fan-out completes. A Run carries a WorkflowID and no Workflow state key at all
// (measured: all 35 keys were enumerated), so the Workflow's State exists for a Run
// only once this list has been read and joined (ADR-0014). Its consumer is
// run-detail R8's deleted marker, which distinguishes an Orphaned Run from a Run
// whose Workflow still exists.
//
// It is a function over domain types rather than an import, so the engine takes the
// list without knowing which package produces it: main.go is the only place that
// knows both, exactly as it is for the store and the client (ADR-0011). It is nil
// wherever no Workflow list is wired, and then nothing is stamped and the marker
// stays dark, which is the honest reading of a join that was never made.
//
// It is called at most once per repository per process, whatever it returns, so an
// implementation owes no memo of its own and pays for no backoff (workflowStates).
// An incomplete listing is returned as what it is rather than as an error, so the
// Workflows on the first page still resolve.
type WorkflowLister func(domain.RepoID) ([]domain.Workflow, error)

// Options carries the scheduler's seams. main.go fills them at stage 7: the client
// is the transport chain, the poll set is discovery, the Budget is the governor,
// and the clock is the injected one. Workflows is the Workflow-list source the
// fan-out joins against.
type Options struct {
	Client  Requester
	PollSet PollSet
	// First is the repository the tool was launched inside, resolved from the git
	// remote before the engine starts (live-run-feed R32, repo-discovery R14). It is
	// polled alone, first, and the rest of the poll set is held back until its opening
	// poll lands, so a cold start issues exactly one Run listing before that repository
	// is painted (AC16). It holds its place in the schedule only until discovery has
	// classified it, after which the poll set is the whole answer (Classified).
	//
	// The zero value is a launch outside any git repository, or one whose remote did not
	// resolve. Nothing is gated then and every discovered repository is due at the cold
	// start, which is R34's fallback. It is also how the composition root declines to name
	// a fast path at all, which is what an excluded launch repository gets (settings R7).
	First domain.RepoID
	// Classified reports whether discovery holds a record for a repository, and is the
	// scheduler's cue to stop treating First as a special case. It is a function over
	// domain types rather than an import, exactly as WorkflowLister is: main.go is the only
	// place that knows both sides (ADR-0011).
	//
	// It exists because R32 and polling-scheduler R2 want opposite things and both are
	// right. R32 needs the launch repository polled before discovery has said anything
	// about it. R2 says the scheduler polls **only** what discovery classified as having
	// Runs, and repo-discovery R22 admits the launch repository to the poll set "if it has
	// Runs", not regardless. So the union is a bridge over the window where discovery has
	// no answer, and it ends when the answer arrives: a launch repository classified as
	// having Runs is in the poll set already, and one classified without them leaves the
	// rotation like any other.
	//
	// Reporting false for a repository discovery never enumerated is deliberate rather than
	// incidental. A clone the account does not own never appears in /user/repos, so no pass
	// will ever record it, and dropping it on the pass completing would blank the Feed for the
	// repository the operator is sitting in. repo-discovery R22 answers that case by adopting
	// it for the session, at which point a record exists and this branch stops carrying it.
	// That adoption is written and unreached (issue #100), so today the branch is what holds
	// such a repository in the schedule, and it is load-bearing for a reason it should not
	// have to be.
	//
	// A nil Classified is "discovery has classified nothing", which keeps First in the
	// schedule. That is the honest reading for a caller that wired no discovery at all.
	Classified func(domain.RepoID) bool
	Budget     Budget
	Clock      clock.Clock
	Workflows  WorkflowLister
}

// Scheduler is the stateful engine. It holds each repository's last-known Runs
// (from which the fast tier is read), the Feed's current viewport (the medium
// tier), each repository's last poll time and in-flight flag (the single-flight
// cadence), the last ETag it observed per repository (which distinguishes a 304
// from a 200 above the store's reconstitution, R19), and the demotion state
// machine. It is safe for concurrent use: the poll goroutines and the Feed's
// viewport publication both touch it, so the state is guarded throughout.
type Scheduler struct {
	opts Options

	mu       sync.Mutex
	lastRuns map[string][]domain.Run // per repo: the Runs its last 200 carried, for the fast tier
	viewport map[string]bool         // the Feed's current viewport, the medium tier (R5)
	lastPoll map[string]time.Time    // per repo: the injected-clock instant of its last poll
	inFlight map[string]bool         // per repo: a poll is out, so the next due tick is skipped (R18)
	lastETag map[string]string       // per repo: the ETag its last 200 carried, to spot a 304 (R19)

	// wfLists is each repository's Workflow list as the engine uses it, read once through
	// the WorkflowLister and held for the process. An entry exists once the read has been
	// attempted, successfully or not, so the listing is read at most once per repository
	// whatever it answered (workflowList has the reasoning). A zero value is a read that
	// failed, and answers the empty State for every Workflow ID and resolves no selector.
	wfLists map[string]workflowList

	// filt is the Feed's active filter, guarded by mu (ADR-0016). A poll derives its
	// server-side query from filt.Query() (R22), and a filtered poll carries the
	// response's total_count back for R24's cap label. The zero Filter is the
	// unfiltered listing, the cold-start default.
	filt filter.Filter

	dem demotion // R15's staged demotion, guarded by mu

	paused       bool      // R16: scheduling is stopped by Budget exhaustion
	pausedResume time.Time // R16: the instant it resumes, from the Readout

	// updates carries the engine's Events to the Feed (ADR-0015's one channel). It is
	// unbuffered: a send blocked on a busy consumer stalls only that repository's poll
	// goroutine, which holds its in-flight flag so the next tick skips it, exactly the
	// case the skip rule already covers (ADR-0018).
	updates chan Event

	// wake lets a poll goroutine ask the loop to re-evaluate when it has changed a
	// repository's tier, so a repository a poll just revealed as live is rescheduled
	// to its ~3s cadence rather than left on the interval it was polled at (R8). It is
	// buffered one and signalled non-blocking, so a burst of changes coalesces into a
	// single re-evaluation.
	wake chan struct{}

	// settled, when non-nil, receives the wait the loop chose each time it sleeps. It
	// is a test seam: it lets a deterministic test observe the loop converging on a
	// stable cadence after asynchronous poll completions. Production leaves it nil, so
	// the loop never touches it.
	settled chan time.Duration

	// polled, when non-nil, receives once per poll goroutine as it finishes. It is a
	// test seam: it lets a deterministic test barrier on a poll being fully done,
	// including a 304 having decided to emit nothing, which is the negative AC16
	// asserts. Production leaves it nil.
	polled chan struct{}

	// primed is closed once the fast path's opening poll has finished, which is the gate
	// on the rest of the schedule (R32, AC16). It is a channel rather than a flag because
	// its consumer is main.go's discovery goroutine, which waits on it alongside a
	// cancellation, and because closing it is a broadcast to any number of waiters. With
	// no First configured it is closed at construction and gates nothing.
	primed    chan struct{}
	primeOnce sync.Once

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once // Stop is idempotent: only the first call closes updates
}

// New returns a Scheduler over opts. It reads nothing and issues no request: a
// caller starts the engine with Start and stops it with Stop.
func New(opts Options) *Scheduler {
	s := &Scheduler{
		opts:     opts,
		lastRuns: make(map[string][]domain.Run),
		viewport: make(map[string]bool),
		lastPoll: make(map[string]time.Time),
		inFlight: make(map[string]bool),
		lastETag: make(map[string]string),
		wfLists:  make(map[string]workflowList),
		updates:  make(chan Event),
		wake:     make(chan struct{}, 1),
		primed:   make(chan struct{}),
	}
	if opts.First == (domain.RepoID{}) {
		// No fast path (R34): nothing is held back, so the gate is open before the engine
		// starts and every scheduling decision sees the whole poll set.
		s.primeOnce.Do(func() { close(s.primed) })
	}
	return s
}

// Primed reports when the fast path has finished its opening poll, by returning a
// channel closed at that instant (R32). It is what lets main.go start repository
// discovery behind the fast path rather than in front of it: discovery's pass probes
// every repository's Run list, so starting it first would put ~163 Run listings on the
// wire before the repository the operator is sitting in had painted, which is exactly
// what AC16 counts.
//
// It closes on the poll finishing, whatever it found: a 200, a 304, a failed request and
// a repository with no Runs all release the gate, because each one is the fast path
// having had its turn on the wire. With no First configured the channel is closed from
// construction.
//
// It does not close while scheduling is paused by Budget exhaustion (R16), because no
// poll is spawned then. That is the safe direction: a waiter is held rather than firing a
// discovery burst into an exhausted limit, and it is released by cancelling the context
// it waits on alongside.
func (s *Scheduler) Primed() <-chan struct{} { return s.primed }

// markPrimed opens the gate when the fast path's poll goroutine finishes, and does
// nothing for any other repository. It wakes the loop so the rest of the poll set is
// scheduled at once rather than at the end of the wait the loop chose while the set was
// still one repository wide.
//
// It is called from a deferred call in poll, after the repository's single-flight flag has
// been cleared, so the loop it wakes finds the fast path schedulable rather than skipping
// it as still in flight.
func (s *Scheduler) markPrimed(id domain.RepoID) {
	if id != s.opts.First {
		return
	}
	s.primeOnce.Do(func() {
		close(s.primed)
		s.signalWake()
	})
}

// isPrimed reports whether the gate is open. It reads the closed channel rather than a
// second flag, so there is one source of truth for a state two consumers observe.
func (s *Scheduler) isPrimed() bool {
	select {
	case <-s.primed:
		return true
	default:
		return false
	}
}

// Updates is the stream of Events the Feed consumes (ADR-0015's one channel). Stop
// closes it, which ADR-0015 already made the root's quit signal.
func (s *Scheduler) Updates() <-chan Event {
	return s.updates
}

// Start launches the engine over a root context derived from ctx (ADR-0018). It
// spawns one long-lived loop goroutine that drives the schedule on the injected
// clock; the loop spawns a goroutine per due poll. Start returns at once.
func (s *Scheduler) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.loop()
}

// Stop cancels the root context, waits for the loop and every in-flight poll to
// unwind through the WaitGroup, then closes Updates (ADR-0018). In-flight reads
// complete rather than draining, because they are side-effect free and their
// responses have no consumer after quit. A poll blocked on an emit is released by
// the cancelled context. Stop is idempotent: a second call is a safe no-op, so a
// defensive Stop from a shutdown path never closes the already-closed Updates channel.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
		close(s.updates)
	})
}

// Paused reports whether scheduling is stopped by Budget exhaustion and the instant
// it resumes (R16). The Feed reads it to state the pause honestly rather than going
// quietly stale: a paused Feed that says "resumes 14:32" is correct, one that looks
// live and is not is the failure R16 exists to prevent.
func (s *Scheduler) Paused() (resume time.Time, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausedResume, s.paused
}

// setLastRuns records the Runs a repository's last 200 carried, from which the fast
// tier is read on the next scheduling decision (R5).
func (s *Scheduler) setLastRuns(id domain.RepoID, runs []domain.Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRuns[id.String()] = runs
}

// pollSetLocked is discovery's poll set, or nil when none is wired. Reading it every
// tick is what adopts a live set change (R3); the size feeds the slow tier's
// auto-scale (R11).
func (s *Scheduler) pollSetLocked() []domain.RepoID {
	if s.opts.PollSet == nil {
		return nil
	}
	return s.opts.PollSet.PollSet()
}

// scheduleSetLocked is what the loop may poll at this decision, and the slow tier's base
// interval. The two are returned together because both come from one read of the poll
// set, which the loop must resolve once per tick rather than once per repository.
//
// Until the fast path has landed the set is that repository alone, which is the whole of
// R32: one Run listing goes out, for the repository the operator launched inside, and no
// other repository's response is waited on or even asked for (AC16). After it lands the set
// is the poll set, with the fast path's repository unioned in only while discovery holds no
// record for it. That window is a bridge and not a permanent exemption: polling-scheduler R2
// admits only what discovery classified as having Runs, so once it has an answer the answer
// stands (Options.Classified carries the reasoning and the one case that never resolves).
//
// pollSetBase is the slow tier's base interval, and it is deliberately **not** derived from
// the ids returned beside it: it is computed from the poll set, whatever the gate did to the
// schedule. It is a Budget projection over what will be polled in the steady state (R11), and
// stretching it on the strength of a one-repository opening window would be reading the gate
// as scale. The name says so, because `wholeSetInterval(len(ids))` is the thing a later
// reader would otherwise assume it equals.
func (s *Scheduler) scheduleSetLocked() (ids []domain.RepoID, pollSetBase time.Duration) {
	set := s.pollSetLocked()
	pollSetBase = wholeSetInterval(len(set))

	first := s.opts.First
	if first == (domain.RepoID{}) {
		return set, pollSetBase
	}
	if !s.isPrimed() {
		return []domain.RepoID{first}, pollSetBase
	}
	if s.opts.Classified != nil && s.opts.Classified(first) {
		return set, pollSetBase // discovery has answered: its answer is the whole schedule (R2)
	}
	for _, id := range set {
		if id == first {
			return set, pollSetBase
		}
	}
	return append([]domain.RepoID{first}, set...), pollSetBase
}

// recordPoll stores a changed poll's Runs and ETag and reports whether the
// repository's tier changed as a result, which is the loop's cue to reschedule it
// (R8). A Run that just reached queued or in_progress, or one that just completed,
// moves the repository between tiers.
func (s *Scheduler) recordPoll(id domain.RepoID, runs []domain.Run, etag string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := id.String()
	before := s.tierOfLocked(id)
	s.lastRuns[key] = runs
	if etag != "" {
		s.lastETag[key] = etag
	}
	return s.tierOfLocked(id) != before
}

// lastETagOf is the ETag a repository's last 200 carried, empty before its first
// poll. An equal ETag on a later poll is the store's reconstituted 304 (R19).
func (s *Scheduler) lastETagOf(id domain.RepoID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastETag[id.String()]
}

// clearInFlight releases a repository's single-flight flag when its poll goroutine
// ends, so the next due tick may poll it again (R18).
func (s *Scheduler) clearInFlight(id domain.RepoID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, id.String())
}

// signalWake asks the loop to re-evaluate, non-blocking so a poll goroutine never
// stalls on a full wake channel (the pending wake already carries the request).
func (s *Scheduler) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// signalPolled tells the test seam a poll goroutine has finished, and is inert in
// production where polled is nil. It sends non-blocking behind a generous buffer, so
// a test that stops draining never stalls a poll goroutine.
func (s *Scheduler) signalPolled() {
	if s.polled == nil {
		return
	}
	select {
	case s.polled <- struct{}{}:
	default:
	}
}

// probeSettle publishes the wait the loop just chose to the test seam, latest value
// wins, and is inert in production where settled is nil.
func (s *Scheduler) probeSettle(wait time.Duration) {
	if s.settled == nil {
		return
	}
	select {
	case <-s.settled:
	default:
	}
	select {
	case s.settled <- wait:
	default:
	}
}

// setPaused records that scheduling is stopped by exhaustion and when it resumes
// (R16).
func (s *Scheduler) setPaused(resume time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	s.pausedResume = resume
}

// clearPaused records that scheduling has resumed.
func (s *Scheduler) clearPaused() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
	s.pausedResume = time.Time{}
}

// SetViewport publishes the Feed's current viewport: the repositories with at least
// one row on screen, which are the medium tier (R5, ADR-0021). The Feed calls it on
// every scroll, and R3's live set-change machinery adopts it with no restart (AC17).
// A repository leaving the viewport falls back to the tier it otherwise holds at the
// next decision.
func (s *Scheduler) SetViewport(ids []domain.RepoID) {
	next := make(map[string]bool, len(ids))
	for _, id := range ids {
		next[id.String()] = true
	}
	s.mu.Lock()
	s.viewport = next
	s.mu.Unlock()
	// A scroll can promote a repository from the slow tier to the medium tier, so wake
	// the loop to adopt the new cadence now rather than at the next scheduling
	// decision, which could be a full slow interval (30s) away while the loop sleeps.
	// This is symmetric with the poll-driven tier change that wakes the loop (R8).
	s.signalWake()
}

// PollSetChanged tells the engine its poll set may have changed, so it re-evaluates now
// rather than at the end of the wait it last chose (R3, live-run-feed R33). The set is
// already read on every scheduling decision, and this is what makes the next decision
// happen: without it the loop sleeps until the next repository it already knew about is
// due, so one classified a moment after it slept waits out that whole interval, up to the
// slow tier's 30s, before its first Run reaches the screen.
//
// Its caller is main.go's discovery, which now runs behind the fast path rather than in
// front of it, and which publishes each repository as its probe returns (repo-discovery
// R15). It is symmetric with the viewport and filter wakes, and costs nothing when nothing
// changed: a redundant wake re-evaluates the same state, spawns no poll, and settles on the
// same wait.
func (s *Scheduler) PollSetChanged() { s.signalWake() }

// SetFilter publishes the Feed's active filter, whose Query() every subsequent poll
// pushes server-side (R22, ADR-0016). The Feed calls it when the filter input is
// accepted or cleared; the zero Filter restores the unfiltered listing.
//
// A filter change that changes the request makes every repository a different resource, so
// the last-poll stamps and the per-repository ETag memory are cleared: every repository is
// due at once, and no poll of the new resource is false-skipped against the prior filter's
// ETag (which the store keys by URL, not by repository, so it never confuses the two; this
// clears the scheduler's own repository-keyed shortcut). The loop is then woken so the
// re-poll happens at once rather than waiting out the up-to-30s slow interval, symmetric
// with the viewport wake (R8).
//
// A change that leaves the request identical resets nothing. Not every axis reaches the
// wire: a Conclusion and the repository set are client-side only (ADR-0016), and the Feed
// narrows over the Runs it holds the moment they change, with no poll involved. Resetting
// there would spend one conditional GET per repository on a resource nobody stopped polling.
func (s *Scheduler) SetFilter(f filter.Filter) {
	s.mu.Lock()
	changed := !sameResource(s.filt, f)
	s.filt = f
	if changed {
		s.lastPoll = make(map[string]time.Time)
		s.lastETag = make(map[string]string)
	}
	s.mu.Unlock()
	if changed {
		s.signalWake()
	}
}

// sameResource reports whether two Filters project to the same request: the same query
// parameters, and the same endpoint. The endpoint is the Workflow selector's, the axis with
// no parameter form (ADR-0016), and it is compared raw rather than resolved because
// resolution is per repository while this decision is not: two selectors that differ in text
// address the same endpoint only if they resolve alike everywhere, which is not knowable here
// and is not worth a request to learn.
func sameResource(a, b filter.Filter) bool {
	return a.Workflow == b.Workflow && a.Query().Encode() == b.Query().Encode()
}

// activeFilter reads the Feed's active filter under the lock, so a poll goroutine
// composing its request never races SetFilter.
func (s *Scheduler) activeFilter() filter.Filter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filt
}
