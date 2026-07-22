package scheduler

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// defaultIdle is how long the loop waits when nothing is due to poll (an empty poll
// set, or every repository already in flight). It re-reads the poll set and the
// Readout at least this often so a repository discovery adds, or pressure the
// governor reports, is picked up promptly.
const defaultIdle = fastTarget

// loop is the single clock-driven schedule (ADR-0018: one schedule, not a timer per
// repository). Each iteration reads the Budget Readout, either pauses at exhaustion
// or polls what is due and demotes under pressure, then sleeps until the next due
// poll on the injected clock. A poll goroutine that changes a repository's tier wakes
// it early so the repository is rescheduled to its new cadence (R8).
func (s *Scheduler) loop() {
	defer s.wg.Done()
	for {
		now := s.opts.Clock.Now()
		r := s.readout()

		var wait time.Duration
		if r.Exhausted {
			// R16, ADR-0018: at exhaustion, primary or secondary, scheduling stops and
			// the Feed states when it resumes. In-flight polls already spawned complete
			// and emit; the loop simply declines to schedule until the reset instant.
			s.setPaused(r.Reset)
			wait = untilResume(r.Reset, now)
		} else {
			s.clearPaused()
			s.observePressure(r, now) // R15: fold pressure into the demotion state
			s.pollDue(now)            // spawn a goroutine per due, not-in-flight repository
			wait = s.nextWait(now)
		}

		timer := s.opts.Clock.NewTimer(wait)
		s.probeSettle(wait)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.Chan():
		case <-s.wake:
			// A poll changed a tier: re-evaluate now. The timer is abandoned; a
			// redundant wake re-evaluates the same state, spawns no new poll, and
			// settles on the same wait, so it is harmless.
			timer.Stop()
		}
	}
}

// readout reads the Budget Readout, or a zero Readout when no Budget is wired (the
// orchestration tests that exercise cadence alone). A zero Readout is neither
// exhausted nor under pressure, so the scheduler polls at its undemoted intervals.
func (s *Scheduler) readout() governor.Readout {
	if s.opts.Budget == nil {
		return governor.Readout{}
	}
	return s.opts.Budget.Readout()
}

// pollDue spawns a goroutine for every repository whose tier interval has elapsed
// and whose previous poll is not still in flight (R17, R18). It marks each polled
// repository in flight and stamps its poll time under one lock, so the wire bound is
// left entirely to the transport limiter (ADR-0018) and this fan-out holds none of
// its own. The poll set is read fresh every tick, so a repository discovery added or
// removed enters or leaves the rotation with no restart (R3).
func (s *Scheduler) pollDue(now time.Time) {
	s.mu.Lock()
	ids := s.pollSetLocked()
	slowBase := wholeSetInterval(len(ids))
	var due []domain.RepoID
	for _, id := range ids {
		key := id.String()
		if s.inFlight[key] {
			continue // single-flight: a superseded tick is skipped, never reissued (ADR-0018)
		}
		interval := s.intervalForLocked(s.tierOfLocked(id), now, slowBase)
		last, polled := s.lastPoll[key]
		if !polled || !now.Before(last.Add(interval)) {
			s.inFlight[key] = true
			s.lastPoll[key] = now
			due = append(due, id)
		}
	}
	s.mu.Unlock()

	for _, id := range due {
		s.wg.Add(1)
		go s.poll(id)
	}
}

// poll issues one conditional Run listing for a repository, distinguishes a 304 from
// a 200 above the store's reconstitution, and emits the change (R1, R19, ADR-0018).
// It holds the repository's in-flight flag for its whole life, including a send
// blocked on a busy consumer, so the next tick skips the repository rather than
// queueing behind it (ADR-0018).
func (s *Scheduler) poll(id domain.RepoID) {
	defer s.wg.Done()
	defer s.signalPolled()
	defer s.clearInFlight(id)

	// The store sends If-None-Match when it holds an ETag for this resource, so a
	// steady-state poll is conditional and a 304 is free (R1, AC2, ADR-0004).
	resp, err := s.opts.Client.Request(http.MethodGet, runsPath(id), nil)
	if err != nil {
		return // a transport error is retried on the next tick, not surfaced here
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// A rate-limit 403 or 429 is an account condition the governor has already
		// classified into the Readout's exhaustion, which the loop acts on; it is
		// never a per-repository failure (ADR-0018). A 404 or 5xx is a transient the
		// next tick retries. Neither is a change to emit.
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	// The store reconstitutes a 304 into a 200 carrying the cached ETag, so an
	// unchanged ETag is the 304 the scheduler may never see as a status code (R19,
	// ADR-0012). No change means no re-render work (AC16); the governor already
	// counted the 304 as free (ADR-0004).
	etag := resp.Header.Get("ETag")
	if etag != "" && etag == s.lastETagOf(id) {
		return
	}

	var page apiRunsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return
	}
	runs := page.WorkflowRuns
	for i := range runs {
		runs[i].Repo = id // ADR-0018: stamp Repo before the event leaves the worker
	}

	if s.recordPoll(id, runs, etag) {
		// The tier changed, so the repository's cadence changed: wake the loop to
		// reschedule it (R8). A live Run just revealed must poll at ~3s, not wait out
		// the slow interval it was polled at.
		s.signalWake()
	}
	s.emit(Update{Repo: id, Runs: runs})
}

// nextWait is the duration until the earliest not-in-flight repository is next due
// (R20: all timing on the injected clock). An empty poll set or an all-in-flight one
// yields defaultIdle, so the loop re-reads its inputs periodically rather than
// sleeping forever.
func (s *Scheduler) nextWait(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.pollSetLocked()
	slowBase := wholeSetInterval(len(ids))
	var next time.Time
	found := false
	for _, id := range ids {
		key := id.String()
		var due time.Time
		if last, polled := s.lastPoll[key]; polled {
			due = last.Add(s.intervalForLocked(s.tierOfLocked(id), now, slowBase))
		} else {
			due = now // never polled: due at once
		}
		// A repository already overdue but whose poll is still in flight is skipped:
		// single-flight will not reissue it, so a wakeup for it would only busy-wait.
		// A just-polled repository is in flight too, but its next due is in the
		// future, so it is scheduled normally.
		if s.inFlight[key] && !due.After(now) {
			continue
		}
		if !found || due.Before(next) {
			next, found = due, true
		}
	}
	if !found {
		return defaultIdle
	}
	if w := next.Sub(now); w > 0 {
		return w
	}
	// A repository is due now but was not polled this tick (for example one added
	// between pollDue and here): re-evaluate promptly rather than spin.
	return time.Millisecond
}

// emit delivers a changed poll to the Feed, or abandons it if the engine is
// stopping (ADR-0018: responses have no consumer after quit). A blocked emit holds
// the repository's in-flight flag, so the next tick skips it.
func (s *Scheduler) emit(u Update) {
	select {
	case s.updates <- u:
	case <-s.ctx.Done():
	}
}

// untilResume is how long the loop waits out an exhaustion (R16). It waits until the
// reset instant when one is known and in the future, otherwise it re-checks on the
// slow-tier interval: the governor may supply a resume time on a later response, and
// a zero reset (exhausted with no time to wait for) must not busy-spin.
func untilResume(reset, now time.Time) time.Duration {
	if reset.After(now) {
		return reset.Sub(now)
	}
	return slowTarget
}

// apiRunsPage is the fragment of an actions/runs listing the scheduler reads. It
// takes workflow_runs and ignores total_count, which a filtered listing inflates
// past the silent 1,000 cap (ADR-0005): the tiers read the Runs' Statuses, never a
// count.
type apiRunsPage struct {
	TotalCount   int          `json:"total_count"`
	WorkflowRuns []domain.Run `json:"workflow_runs"`
}

// runsPath is a repository's Run listing, the resource every tier polls. It carries
// no query, so it is the unfiltered listing; the Feed's filtered listing (ADR-0005)
// is the Feed's to compose at stage 7.
func runsPath(id domain.RepoID) string {
	return "repos/" + id.Owner + "/" + id.Name + "/actions/runs"
}
