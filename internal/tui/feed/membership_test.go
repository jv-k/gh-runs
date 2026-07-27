package feed

import (
	"errors"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// R37 gives the Feed a membership set of its own, carrying every record discovery
// still holds whether or not its capability is known, and requires both prunes to read
// that and never absence from the capability set R18 gates on.
//
// The prunes' live path is pinned where it already was, by
// TestFailureClearsWhenTheRepositoryLeavesDiscovery and
// TestCancelRequestedClearsWhenTheRepositoryLeavesThePollSet, both now driven by
// RepoMembership. What is here is the other direction, which nothing covered: the two
// sets differ for a real repository, and reading the wrong one clears a live failure.

// TestFailureSurvivesARepositoryAwaitingItsCapability is the mirror-image bug, and the
// one a regression would silently reintroduce. A repository present in membership but
// absent from the capability set has not departed, it is waiting on enumeration, and
// pruning against the capability set would clear a failure that is still live.
//
// This is not hypothetical. A fast-path repository paints its Runs before enumeration
// returns its permissions (R14, R18), so it is a member with no recorded capability for
// as long as that takes, and a poll of it can fail in that window.
func TestFailureSurvivesARepositoryAwaitingItsCapability(t *testing.T) {
	m := newFeed(100, 24)
	awaiting, known := repoID("acme", "awaiting"), repoID("acme", "known")

	// Both are members. Only one has had its capability recorded.
	m, _ = m.Update(RepoMembership{awaiting, known})
	m, _ = m.Update(ReposDiscovered{{ID: known, Permissions: domain.Permissions{Push: true}}})
	m = feedFailure(m, awaiting, errors.New("poll returned HTTP 502 Bad Gateway"))

	if got := m.View(); !strings.Contains(got, "acme/awaiting") {
		t.Errorf("a repository awaiting its capability lost a live failure indicator:\n%s", got)
	}
}

// TestTheCapabilitySetPrunesNothing states the rule directly rather than through a
// symptom. However many capability snapshots arrive, and whatever they omit, they move
// neither prune. Without this the handler could quietly regain its old prune call and
// only the test above would notice, and only for one of the two prunes.
func TestTheCapabilitySetPrunesNothing(t *testing.T) {
	m := newFeed(100, 24)
	id := repoID("acme", "api")
	m, _ = m.Update(RepoMembership{id})
	m = feedFailure(m, id, errors.New("poll returned HTTP 502 Bad Gateway"))
	m.cancelRequested = map[int64]domain.RepoID{42: id}

	// A non-empty capability snapshot that does not name the failing repository.
	m, _ = m.Update(ReposDiscovered{{ID: repoID("acme", "other"), Permissions: domain.Permissions{Push: true}}})

	if got := m.View(); !strings.Contains(got, "acme/api") {
		t.Errorf("a capability snapshot pruned a failure, want only membership to prune:\n%s", got)
	}
	if _, ok := m.cancelRequested[42]; !ok {
		t.Error("a capability snapshot pruned a cancellation mark, want only membership to prune")
	}
}
