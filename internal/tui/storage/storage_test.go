package storage_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

func rid(owner, name string) domain.RepoID {
	return domain.RepoID{Host: domain.HostGitHub, Owner: owner, Name: name}
}

func writable(owner, name string) domain.Repo {
	return domain.Repo{ID: rid(owner, name), Permissions: domain.Permissions{Push: true}}
}

// press builds a single-key press, matching the Feed's helper so key.Matches resolves the
// same bindings (R7a).
func press(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}

// newStorage builds a Storage tab sized w x h, wired to a real planner and the discovered
// repositories so the delete key can freeze a Cache and Artifact selection (ops.Plan needs
// no transport). Fetch is nil, so no request is issued and the held state is injected.
func newStorage(t *testing.T, w, h int, repos ...domain.Repo) storage.Model {
	t.Helper()
	planner := ops.New(ops.Options{ConfirmThreshold: 50, BreakerFailures: 50})
	rr := append([]domain.Repo(nil), repos...)
	m := storage.New(storage.Options{
		Profile: keys.Standard,
		Ops:     planner,
		Repos:   func() []domain.Repo { return rr },
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func fetched(m storage.Model, rs storage.RepoStorage) storage.Model {
	m, _ = m.Update(storage.StorageFetched(rs))
	return m
}

// send routes a keystroke and returns the updated model, discarding the Cmd.
func send(m storage.Model, s string) storage.Model {
	m, _ = m.Update(press(s))
	return m
}

var day = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

// oneCache is a repository holding a single reclaimable Cache.
func oneCache(owner, name string, id int64, key string, size int64) storage.RepoStorage {
	return storage.RepoStorage{
		Repo:                    rid(owner, name),
		ActiveCachesSizeInBytes: size,
		ActiveCachesCount:       1,
		Caches:                  []domain.Cache{{ID: id, Key: key, SizeInBytes: size, LastAccessedAt: day}},
		ArtifactsComplete:       true,
	}
}

// TestDeleteKeyOpensReclamationConfirmation pins R15 and R17: with a Cache selected, the
// delete key freezes the selection into a Plan and opens the shared confirmation, which the
// tab paints in place of the list, led by the reclaim figure. The tab then captures input so
// a typed count is not stolen as a global key (R7).
func TestDeleteKeyOpensReclamationConfirmation(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, oneCache("cli", "cli", 987654321, "setup-go", 302460229))
	m = send(m, "r")     // pull the discovered repositories into the eligibility gate
	m = send(m, "space") // select the Cache under the cursor
	m = send(m, "d")     // open the confirmation

	if !m.CapturesInput() {
		t.Fatalf("the delete key did not open the confirmation modal (R15, R17)")
	}
	got := m.View()
	if !strings.Contains(got, "Delete") || !strings.Contains(got, "Cache") {
		t.Errorf("the confirmation does not name the reclamation:\n%s", got)
	}
	if !strings.Contains(got, "Reclaims 302.46 MB") {
		t.Errorf("the confirmation does not show the reclaimable bytes (R11):\n%s", got)
	}
}

// TestReclaimFigureIsZeroForTombstone pins R11 and AC8: confirming deletion of an expired
// Artifact shows a reclaim figure of zero bytes, because its bytes are already gone though it
// still reports its original size_in_bytes.
func TestReclaimFigureIsZeroForTombstone(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, storage.RepoStorage{
		Repo:              rid("cli", "cli"),
		Artifacts:         []domain.Artifact{{ID: 5, Name: "old-logs", SizeInBytes: 258000, Expired: true}},
		ArtifactsComplete: true,
	})
	m = send(m, "r")
	m = send(m, "space")
	m = send(m, "d")

	if !m.CapturesInput() {
		t.Fatalf("the delete key did not open a confirmation over the Tombstone")
	}
	got := m.View()
	if !strings.Contains(got, "Reclaims 0 B") {
		t.Errorf("confirming a Tombstone must show a reclaim figure of zero bytes (R11, AC8):\n%s", got)
	}
}

// TestGateBlocksArchivedRepo pins R20 and AC13: with the repository archived, the delete key
// offers no action and opens no modal, so no delete request can follow. An archived
// repository's storage can never be reclaimed.
func TestGateBlocksArchivedRepo(t *testing.T) {
	archived := domain.Repo{ID: rid("old", "legacy"), Permissions: domain.Permissions{Push: true}, Archived: true}
	m := newStorage(t, 100, 20, archived)
	m = fetched(m, oneCache("old", "legacy", 1, "stale", 500000))
	m = send(m, "r")
	m = send(m, "space")
	m = send(m, "d")

	if m.CapturesInput() {
		t.Errorf("the delete key opened a modal over an archived repository; no action must be offered (R20, AC13)")
	}
}

// TestDeleteKeyInertWithoutPlanner pins the fail-closed default: with no planner wired (a
// golden test, or before discovery has recorded capability), the delete key is inert
// (repo-discovery R8).
func TestDeleteKeyInertWithoutPlanner(t *testing.T) {
	m := storage.New(storage.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = fetched(m, oneCache("cli", "cli", 1, "k", 1000))
	m = send(m, "space")
	m = send(m, "d")
	if m.CapturesInput() {
		t.Errorf("the delete key opened a modal with no planner wired; it must be inert (repo-discovery R8)")
	}
}

// TestConfirmationAbortReturnsToTheList pins that aborting the modal dismisses it and
// returns the tab to the list, having issued nothing (purge AC6).
func TestConfirmationAbortReturnsToTheList(t *testing.T) {
	m := newStorage(t, 100, 20, writable("cli", "cli"))
	m = fetched(m, oneCache("cli", "cli", 1, "k", 302460229))
	m = send(m, "r")
	m = send(m, "d")
	m = send(m, "n") // abort
	if m.CapturesInput() {
		t.Errorf("aborting the modal did not return the tab to the list (AC6)")
	}
}

// TestRefreshFansOutOverDiscoveredRepos pins R0: a refresh issues one Fetch per discovered
// repository, the fan-out that leads with the per-repository rollup. The Fetch is a fake, so
// no network is touched; the assertion is the set it was asked for.
func TestRefreshFansOutOverDiscoveredRepos(t *testing.T) {
	var fetched []domain.RepoID
	fetch := func(id domain.RepoID) storage.RepoStorage {
		fetched = append(fetched, id)
		return storage.RepoStorage{Repo: id, ArtifactsComplete: true}
	}
	repos := []domain.Repo{writable("cli", "cli"), writable("octo", "hello")}
	m := storage.New(storage.Options{
		Profile: keys.Standard,
		Fetch:   fetch,
		Repos:   func() []domain.Repo { return repos },
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	_, cmd := m.Update(press("r"))
	if cmd == nil {
		t.Fatalf("refresh issued no fan-out command (R0)")
	}
	drainStorageCmd(t, m, cmd) // run the batched fetch commands
	if len(fetched) != 2 {
		t.Errorf("the fan-out fetched %d repositories, want one per discovered repo (R0): %v", len(fetched), fetched)
	}
}

// drainStorageCmd runs a (possibly batched) command and applies every StorageFetched it
// produces, so a test can assert what the fan-out fetched.
func drainStorageCmd(t *testing.T, m storage.Model, cmd tea.Cmd) storage.Model {
	t.Helper()
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if sf, ok := c().(storage.StorageFetched); ok {
				m, _ = m.Update(sf)
			}
		}
		return m
	}
	if sf, ok := msg.(storage.StorageFetched); ok {
		m, _ = m.Update(sf)
	}
	return m
}
