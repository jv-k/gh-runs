package discovery_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// corruptDiscoveryDoc overwrites the persisted discovery document with garbage, so
// a reload has a malformed file to discard (local-store R13). The document lives in
// the store's docs subdirectory, out of the entry cache's namespace.
func corruptDiscoveryDoc(t *testing.T, storeDir string) {
	t.Helper()
	dir := filepath.Join(storeDir, "docs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make docs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "discovery.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt discovery doc: %v", err)
	}
}

// TestResultsSurviveRestartWithoutReprobing is local-store R2 and AC7, the whole
// reason this stage owns a persistence primitive. A first session runs a pass and
// persists its classification and capability through the store. A second session
// over the same store directory reloads them and reaches the same poll set with
// the same recorded capability, having issued zero requests: no re-probe of the
// enumerated repositories, which is the cost R2 exists to avoid.
func TestResultsSurviveRestartWithoutReprobing(t *testing.T) {
	first := newHarness(t, "pass_basic", "")
	if err := first.disc.Pass(context.Background(), nil); err != nil {
		t.Fatalf("first session Pass: %v", err)
	}
	wantPoll := pollSetKeys(first.disc)

	// A second session over the same store directory. It reloads the persisted
	// results and probes nothing.
	second := newHarness(t, "pass_basic", first.dir)
	n := second.disc.Reload()
	if n == 0 {
		t.Fatal("Reload admitted no records; the first session's results did not persist")
	}

	if got := second.counting.count(); got != 0 {
		t.Errorf("second session issued %d requests, want 0 (AC7: a cold start reloads without re-probing)", got)
	}

	if got := pollSetKeys(second.disc); strings.Join(got, ",") != strings.Join(wantPoll, ",") {
		t.Errorf("reloaded poll set = %v, want %v", got, wantPoll)
	}

	// AC7: the capability recorded for each repository is the capability that was
	// persisted, archived's permanence included.
	cases := []struct {
		id        domain.RepoID
		want      domain.Capability
		permanent bool
	}{
		{gh("jv-k", "alpha"), domain.CapabilityPermitted, false},
		{gh("jv-k", "gamma"), domain.CapabilityRefused, true},
		{gh("jv-k", "epsilon"), domain.CapabilityRefused, false},
	}
	byID := recordsByID(second.disc)
	for _, c := range cases {
		if got := second.disc.Capability(c.id); got != c.want {
			t.Errorf("reloaded %s capability = %v, want %v", c.id, got, c.want)
		}
		if rec, ok := byID[c.id.String()]; ok {
			if rec.Permanent() != c.permanent {
				t.Errorf("reloaded %s Permanent() = %v, want %v", c.id, rec.Permanent(), c.permanent)
			}
		}
	}
}

// TestReloadDiscardsCorruptDocument pins local-store R13 at discovery's public
// seam: a corrupt persisted document reloads as nothing rather than failing, so a
// launch over a bad store starts empty and rebuilds by probing (the store is
// derived and always safe to discard, R11). Reload returning zero without a crash
// is the launch that did not fail.
func TestReloadDiscardsCorruptDocument(t *testing.T) {
	h := newHarness(t, "pass_basic", "")
	corruptDiscoveryDoc(t, h.dir)

	if n := h.disc.Reload(); n != 0 {
		t.Errorf("Reload admitted %d records from a corrupt document, want 0", n)
	}
	if got := h.disc.PollSet(); len(got) != 0 {
		t.Errorf("poll set after a discarded reload = %v, want empty", got)
	}
}
