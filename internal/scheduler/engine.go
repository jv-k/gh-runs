package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

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
	// steady-state poll is conditional and a 304 is free (R1, AC2, ADR-0004). The
	// active filter's Query() is pushed server-side, so the response is the newest
	// matches rather than the newest Runs (R22, ADR-0016). A non-empty query makes
	// this a filtered listing, whose total_count is the claimed match count R24 reads.
	query := s.activeFilter().Query()
	filtered := len(query) > 0
	resp, err := s.opts.Client.Request(http.MethodGet, runsPath(id, query), nil)
	if err != nil {
		// Both failure shapes arrive here. A transport error carries no status. Every
		// non-2xx also arrives here rather than on a response, because the RESTClient
		// converts it into an *api.HTTPError and returns a nil response (ghclient.Request's
		// stated contract), which is why the status test below reads the error and not
		// resp.StatusCode.
		if isRateLimitError(err) {
			// An account condition the governor has already classified into the Readout's
			// exhaustion, which the loop acts on and the Feed already states. Reporting it
			// per repository would state one account-wide condition once per repository,
			// and would mark repositories that are perfectly well (ADR-0018).
			return
		}
		// Everything else is this repository's own: a 404 for one that has gone or lost
		// visibility, a 401, a 5xx, a refused connection. Each is a transient the next
		// tick retries, and each is indistinguishable from an unanswered repository
		// until it is reported (ADR-0015).
		s.emit(RepoPollFailed{Repo: id, Err: err})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Only a 2xx that is not 200 can reach here, every other status having become
		// the error above. The Run listing has never answered one, so this is a
		// defensive report rather than a path with a known trigger.
		s.emit(RepoPollFailed{Repo: id, Err: statusError(resp.StatusCode)})
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.emit(RepoPollFailed{Repo: id, Err: err})
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
		// A 200 whose body will not parse is this repository's failure too. Returning
		// silently here would hold the repository's last-known Runs on screen with
		// nothing saying they had stopped refreshing.
		s.emit(RepoPollFailed{Repo: id, Err: err})
		return
	}
	runs := page.WorkflowRuns
	// The fan-out stamps what the run object does not carry, before the event leaves the
	// worker (ADR-0014, ADR-0018). Repo is the identity the payload has no key for; the
	// Workflow's State is the join against the repository's Workflow list, read here
	// because this is where both sides are held. A Run is whole when it is emitted, and no
	// consumer joins anything (ADR-0015).
	var wf map[int64]domain.State
	if len(runs) > 0 {
		wf = s.workflowStates(id)
	}
	for i := range runs {
		runs[i].Repo = id
		runs[i].WorkflowState = wf[runs[i].WorkflowID] // absent or unread reads as the empty State
	}

	if s.recordPoll(id, runs, etag) {
		// The tier changed, so the repository's cadence changed: wake the loop to
		// reschedule it (R8). A live Run just revealed must poll at ~3s, not wait out
		// the slow interval it was polled at.
		s.signalWake()
	}
	// A filtered poll carries its claimed match count; an unfiltered one carries none
	// (R24, ADR-0016). total_count is meaningful only when the listing was filtered:
	// unfiltered it is the repository's whole count, never a cap.
	claimed := 0
	if filtered {
		claimed = page.TotalCount
	}
	s.emit(Update{Repo: id, Runs: runs, Filtered: filtered, ClaimedTotal: claimed})
}

// workflowStates is a repository's Workflow ID to State join, read through the injected
// WorkflowLister the first time it is needed and held for the process (run-detail R8).
//
// Once per repository is the whole cost of the marker. A Workflow's State changes only
// when a person edits, disables or deletes it, so re-reading the list on every poll would
// buy a rarer event than the poll itself at the price of doubling the engine's request
// count. Reading it lazily, from the poll that has Runs to stamp, means a repository the
// operator never sees Runs from costs nothing at all.
//
// A failed read is not remembered, so a transient 502 or a 403 that later clears does not
// disable the marker for the session: the repository asks again on its next changed poll,
// which is the same retry rule a failed poll already has. A successful read of an empty
// list is remembered, so a repository with no Workflows is asked exactly once.
//
// It is called from the poll goroutine, which already holds the repository's
// single-flight flag, so two reads of one repository never overlap and the wire bound
// stays the transport limiter's (ADR-0018). A failure emits nothing: the poll itself
// succeeded, and a RepoPollFailed here would report a repository whose Runs have just
// arrived as failing to update.
func (s *Scheduler) workflowStates(id domain.RepoID) map[int64]domain.State {
	key := id.String()
	s.mu.Lock()
	held, read := s.wfStates[key]
	s.mu.Unlock()
	if read {
		return held
	}
	if s.opts.Workflows == nil {
		return nil
	}
	ws, err := s.opts.Workflows(id)
	if err != nil {
		return nil
	}
	states := make(map[int64]domain.State, len(ws))
	for _, w := range ws {
		states[w.ID] = w.State
	}
	s.mu.Lock()
	s.wfStates[key] = states
	s.mu.Unlock()
	return states
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

// emit delivers one Event to the Feed, or abandons it if the engine is stopping
// (ADR-0018: responses have no consumer after quit). A blocked emit holds the
// repository's in-flight flag, so the next tick skips it.
func (s *Scheduler) emit(e Event) {
	select {
	case s.updates <- e:
	case <-s.ctx.Done():
	}
}

// isRateLimitError reports whether err is the account-wide rate-limit condition the
// governor owns rather than this repository's own failure. The governor has already
// folded that condition into the Readout the loop reads, so the poll reports nothing
// and the Feed's existing exhaustion state stands (ADR-0018, rate-governor R14).
//
// It reads the governor's verdict and does not re-derive it. The status alone cannot
// decide this: a 403 is rate limiting or authorization depending on its body, only
// the governor has looked, and it publishes exhaustion for the first and not the
// second. Dropping every 403 here would leave a repository whose access has been
// revoked showing no rows, no indicator and no banner, which is the invisibility this
// event exists to remove.
//
// The verdict arrives on the error rather than on a response because go-gh's
// RESTClient turns every non-2xx into an *api.HTTPError, carrying a copy of the
// response headers, and returns a nil response. The errors.As read is the same one
// the CLI's exit-code mapping already uses (cli-surface R17).
//
// A transport error carries no headers and is therefore this repository's, which is
// the right default: nothing else in the process has seen it, so declining to report
// it would leave it invisible.
func isRateLimitError(err error) bool {
	var he *api.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return governor.RateLimitedHeaders(he.Headers)
}

// statusError is the error a non-200 becomes on its way into a RepoPollFailed. It
// carries the code verbatim, because the Feed's indicator is only as useful as what
// it can say about why: a 404 and a 502 want different things from the operator.
func statusError(code int) error {
	return fmt.Errorf("poll returned HTTP %d %s", code, http.StatusText(code))
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

// apiRunsPage is the fragment of an actions/runs listing the scheduler reads. The
// tiers read the Runs' Statuses, never total_count, which a filtered listing inflates
// past the silent 1,000 cap (ADR-0005). total_count is read only to carry a filtered
// poll's claimed match count back for R24's cap label, and only then (poll).
type apiRunsPage struct {
	TotalCount   int          `json:"total_count"`
	WorkflowRuns []domain.Run `json:"workflow_runs"`
}

// runsPath is a repository's Run listing, the resource every tier polls. With no
// active filter q is empty and the path is the unfiltered listing; with a filter the
// query is url.Values-encoded onto the path, so the request pushes it server-side
// (R22, ADR-0016) and a cassette can match the URL exactly. The store keys its cache
// on the whole URL, so a filter change is a new resource with its own ETag.
func runsPath(id domain.RepoID, q url.Values) string {
	base := "repos/" + id.Owner + "/" + id.Name + "/actions/runs"
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}
