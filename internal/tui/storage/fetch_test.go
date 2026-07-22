package storage_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// storageMatcher matches a live request against a taped one on method and URL path. The path
// alone disambiguates the three storage GETs (cache/usage, caches, artifacts), so the match is
// robust to how go-gh orders or encodes the query it appends.
func storageMatcher(r *http.Request, i cassette.Request) bool {
	iu, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	return r.Method == i.Method && r.URL.Path == iu.Path
}

// TestClientFetchDecodesOneRepositorysStorage pins the read half of the view against a
// cassette, with no live network: the Cache totals come from one request and are the
// endpoint's own figures (R1), the Cache list carries last_accessed_at (R7), the Artifact
// list carries expired and expires_at (R9, R12), the enumeration is marked complete because
// the Link header has no next page (R3), and every object is stamped with its repository so a
// merged list keeps each row's owner (R4).
func TestClientFetchDecodesOneRepositorysStorage(t *testing.T) {
	rec, err := recorder.New("testdata/repo_storage",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(storageMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder: %v", err)
		}
	})
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: rec})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	repo := rid("o", "r")
	rs := storage.ClientFetch(client)(repo)
	if rs.Err != nil {
		t.Fatalf("ClientFetch returned an error: %v", rs.Err)
	}

	// R1: the two figures are the endpoint's, from one request.
	if rs.ActiveCachesSizeInBytes != 10587236096 || rs.ActiveCachesCount != 83 {
		t.Errorf("cache usage = %d bytes/%d, want 10587236096/83 (R1)", rs.ActiveCachesSizeInBytes, rs.ActiveCachesCount)
	}
	// R7: the Cache carries last_accessed_at, and is stamped with its repository.
	if len(rs.Caches) != 1 {
		t.Fatalf("decoded %d Caches, want 1", len(rs.Caches))
	}
	c := rs.Caches[0]
	if c.ID != 987654321 || c.SizeInBytes != 302460229 {
		t.Errorf("Cache id/size = %d/%d, want 987654321/302460229", c.ID, c.SizeInBytes)
	}
	if !c.LastAccessedAt.Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Cache last_accessed_at = %v, want 2026-07-10 (R7)", c.LastAccessedAt)
	}
	if c.Repo != repo {
		t.Errorf("Cache repo = %v, want it stamped with %v (R4)", c.Repo, repo)
	}
	// R9, R12: the Artifacts carry expired and expires_at, and one is a Tombstone.
	if len(rs.Artifacts) != 2 {
		t.Fatalf("decoded %d Artifacts, want 2", len(rs.Artifacts))
	}
	var tombstones int
	for _, a := range rs.Artifacts {
		if a.Repo != repo {
			t.Errorf("Artifact repo = %v, want it stamped with %v (R4)", a.Repo, repo)
		}
		if a.Tombstone() {
			tombstones++
			if a.ReclaimableBytes() != 0 {
				t.Errorf("a Tombstone's reclaimable bytes = %d, want 0 (R10)", a.ReclaimableBytes())
			}
		}
	}
	if tombstones != 1 {
		t.Errorf("decoded %d Tombstones, want 1 (R9)", tombstones)
	}
	// R12: a live Artifact's expires_at decodes.
	if want := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC); !rs.Artifacts[0].ExpiresAt.Equal(want) {
		t.Errorf("live Artifact expires_at = %v, want %v (R12)", rs.Artifacts[0].ExpiresAt, want)
	}
	// R3: the enumeration is complete, because the Link header carried no next page.
	if !rs.ArtifactsComplete {
		t.Errorf("ArtifactsComplete = false, want true (no Link rel=next means a complete enumeration) (R3)")
	}
}

// TestClientFetchRecordsAForbiddenResponse pins R21: a 403 on a fine-grained PAT is recorded
// as an outcome on the RepoStorage rather than treated as a fault, so the fan-out continues.
func TestClientFetchRecordsAForbiddenResponse(t *testing.T) {
	rec, err := recorder.New("testdata/repo_forbidden",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(storageMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() { _ = rec.Stop() })
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: rec})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	rs := storage.ClientFetch(client)(rid("o", "r"))
	if rs.Err == nil {
		t.Errorf("a 403 must be recorded on the RepoStorage, not swallowed (R21)")
	}
}
