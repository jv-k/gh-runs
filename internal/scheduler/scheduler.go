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
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// Update is one repository's fresh Runs, emitted when a poll returns a 200 whose
// content changed (R19, AC16). A 304 emits nothing: it carries no change and does no
// re-render work. The Runs are stamped with their Repo (ADR-0018); the Feed is the
// first consumer, at stage 7.
type Update struct {
	Repo domain.RepoID
	Runs []domain.Run
}

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is exactly ghclient.Client's surface
// (ADR-0012: Request, never Get or Do, so the response headers survive), narrowed
// to what the scheduler uses. A cassette-backed ghclient.Client fills it for the
// wire-fidelity tests; a gated fake fills it where the property under test is
// orchestration rather than the wire (R22).
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

// Options carries the scheduler's seams. main.go fills them at stage 7: the client
// is the transport chain, the poll set is discovery, the Budget is the governor,
// and the clock is the injected one.
type Options struct {
	Client  Requester
	PollSet PollSet
	Budget  Budget
	Clock   clock.Clock
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

	dem demotion // R15's staged demotion, guarded by mu

	paused       bool      // R16: scheduling is stopped by Budget exhaustion
	pausedResume time.Time // R16: the instant it resumes, from the Readout

	// updates carries a changed poll's Runs to the Feed (ADR-0018). It is unbuffered:
	// a send blocked on a busy consumer stalls only that repository's poll goroutine,
	// which holds its in-flight flag so the next tick skips it, exactly the case the
	// skip rule already covers (ADR-0018).
	updates chan Update

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

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a Scheduler over opts. It reads nothing and issues no request: a
// caller starts the engine with Start and stops it with Stop.
func New(opts Options) *Scheduler {
	return &Scheduler{
		opts:     opts,
		lastRuns: make(map[string][]domain.Run),
		viewport: make(map[string]bool),
		lastPoll: make(map[string]time.Time),
		inFlight: make(map[string]bool),
		lastETag: make(map[string]string),
		updates:  make(chan Update),
		wake:     make(chan struct{}, 1),
	}
}

// Updates is the stream of changed polls the Feed consumes (ADR-0018). Stop closes
// it, which ADR-0015 already made the root's quit signal.
func (s *Scheduler) Updates() <-chan Update {
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
// the cancelled context.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	close(s.updates)
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
	defer s.mu.Unlock()
	s.viewport = next
}
