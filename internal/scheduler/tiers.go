package scheduler

import (
	"time"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Tier is one of the scheduler's three cadences (R5). A repository is polled at the
// fastest tier it qualifies for (R7).
type Tier int

const (
	// tierFast (~3s) tracks a repository whose recent Runs include one at Status
	// queued or in_progress: a Run that is moving (R5, ADR-0021). The ~3s figure
	// matches gh run watch's default (R8).
	tierFast Tier = iota
	// tierMedium (~5s) tracks a repository on screen (at least one row in the Feed's
	// current viewport) or pinned (settings R7, R5, ADR-0021). The viewport half is
	// self-capping at terminal height; the pin half is operator-authored and unbounded,
	// so the tier's base is auto-scaled against the secondary ceiling instead, and it
	// never meets the 100-repository cliff either way (mediumSetInterval).
	tierMedium
	// tierSlow (~30s) is the ambient tier: a repository in the poll set that
	// qualifies for neither of the above (R5). A Run invoked elsewhere surfaces
	// within this interval (R8).
	tierSlow
)

func (t Tier) String() string {
	switch t {
	case tierFast:
		return "fast"
	case tierMedium:
		return "medium"
	case tierSlow:
		return "slow"
	default:
		return "unknown"
	}
}

// Target intervals from R5's table. They are the undemoted cadences; R15's demotion
// stretches them under Budget pressure. slowTarget is the whole poll set's ambient
// interval and is the one budget.go auto-scales for a very large poll set (R11).
const (
	fastTarget   = 3 * time.Second
	mediumTarget = 5 * time.Second
	slowTarget   = 30 * time.Second
)

// tierOf reports the tier a repository qualifies for at the current viewport and
// its last-known Runs. It reads Status and visibility alone, never Conclusion (R6)
// and never capability (R4): a repository the account cannot write to polls at the
// same tiers as one it can, because tier selection has no capability input at all
// (AC5).
func (s *Scheduler) tierOf(id domain.RepoID) Tier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tierOfLocked(id)
}

// tierOfLocked is tierOf under the caller's lock, for the scheduling loop that
// already holds it.
func (s *Scheduler) tierOfLocked(id domain.RepoID) Tier {
	// Fastest tier wins (R7): being on screen must never slow a repository that has
	// a live Run.
	if s.hasLiveRunLocked(id) {
		return tierFast
	}
	// On screen, or pinned. The two are one tier: the viewport says "I am looking at this
	// now" and a pin says "I care about this one" persistently, and neither is a reason to
	// poll at a different rate from the other (ADR-0021, settings R7, #97).
	if s.viewport[id.String()] || s.pinned[id.String()] {
		return tierMedium
	}
	return tierSlow
}

// hasLiveRunLocked reports whether a repository's last-known Runs include one whose
// Status is queued or in_progress (R5, R6). The parked statuses (waiting, requested,
// pending) qualify it for nothing: a parked Run's next event is a human acting, so
// ~3s polling buys nothing (ADR-0021, AC18). Conclusion is never read: it is null
// until Status reaches completed, so a Conclusion-driven tier would be null exactly
// when liveness matters (R6).
func (s *Scheduler) hasLiveRunLocked(id domain.RepoID) bool {
	for _, r := range s.lastRuns[id.String()] {
		if r.Status == domain.StatusQueued || r.Status == domain.StatusInProgress {
			return true
		}
	}
	return false
}
