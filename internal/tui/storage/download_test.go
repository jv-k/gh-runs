package storage_test

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// downloadMatcher matches a live request against a taped one on method, full URL and the
// empty If-None-Match header, the header-matched shape the tree pins go-vcr v4 for
// (CLAUDE.md). The full URL matters here because a download spans two hosts, the API and the
// signed blob it redirects to, and the path alone would not tell one from the other.
func downloadMatcher(r *http.Request, i cassette.Request) bool {
	if r.Method != i.Method || r.URL.String() != i.URL {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

// downloadClient builds a ghclient over a replay-only cassette, so a download is exercised
// against what the API actually said with no live network.
func downloadClient(t *testing.T, name string) *ghclient.Client {
	t.Helper()
	rec, err := recorder.New("testdata/"+name,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(downloadMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", name, err)
		}
	})
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: rec})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// liveArtifact is an Artifact the listing reports as live, the only kind R13 offers a
// download for.
func liveArtifact(id int64, name string) domain.Artifact {
	return domain.Artifact{ID: id, Name: name, SizeInBytes: 145212, Repo: rid("o", "r")}
}

// TestClientDownloadStreamsTheArchiveToDisk pins R13: downloading a live Artifact hits the
// archive endpoint, follows the 302 to the signed blob, and writes the bytes to a file under
// the target directory, returning the path it wrote. The download is a GET and nothing else:
// nothing is deleted, and the file is the whole point of the action.
func TestClientDownloadStreamsTheArchiveToDisk(t *testing.T) {
	dir := t.TempDir()
	client := downloadClient(t, "artifact_download")

	path, err := storage.ClientDownload(client, dir)(liveArtifact(12345, "build-logs"))
	if err != nil {
		t.Fatalf("ClientDownload returned an error: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("download landed at %s, want a file in %s (R13)", path, dir)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("the download wrote no readable file at %s: %v (R13)", path, readErr)
	}
	if !strings.Contains(string(body), "artifact-zip-payload") {
		t.Errorf("the written file does not carry the taped archive bytes: %q (R13)", body)
	}
	// The name identifies the Artifact it came from, so two downloads never collide.
	base := filepath.Base(path)
	if !strings.Contains(base, "12345") || !strings.Contains(base, "build-logs") || !strings.HasSuffix(base, ".zip") {
		t.Errorf("download filename = %q, want it to name the Artifact's id and name and end .zip (R13)", base)
	}
}

// TestClientDownloadReportsA410AsGone pins R14 and AC9: an Artifact that expired between the
// listing and the download answers 410 Gone, and the outcome reads as the bytes being gone,
// never as a transient failure a retry might fix. No file is left behind.
func TestClientDownloadReportsA410AsGone(t *testing.T) {
	dir := t.TempDir()
	client := downloadClient(t, "artifact_gone")

	path, err := storage.ClientDownload(client, dir)(liveArtifact(67890, "old-test-logs"))
	if !errors.Is(err, storage.ErrArtifactGone) {
		t.Fatalf("410 Gone yielded %v, want ErrArtifactGone: the bytes are gone, not a transient failure (R14, AC9)", err)
	}
	if path != "" {
		t.Errorf("a gone Artifact reported a written path %q, want none", path)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read target dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a gone Artifact left %d files behind, want none", len(entries))
	}
}

// requesterFunc adapts a function to storage.Requester, ignoring the body a GET never
// carries. It returns the response for any status, which is what a raw transport does and
// what the Requester interface permits, so the 410 arrives as a status rather than as the
// error the RESTClient raises for one.
type requesterFunc func(method, path string) (*http.Response, error)

func (f requesterFunc) Request(method, path string, _ io.Reader) (*http.Response, error) {
	return f(method, path)
}

// TestClientDownloadReadsARaw410AsGone pins R14 and AC9 on the other half of the Requester
// contract: a transport that hands back the response for any status, rather than converting a
// non-2xx to an error, must still read 410 as the bytes being gone.
func TestClientDownloadReadsARaw410AsGone(t *testing.T) {
	raw := requesterFunc(func(string, string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusGone, Body: http.NoBody, Header: http.Header{}}, nil
	})
	_, err := storage.ClientDownload(raw, t.TempDir())(liveArtifact(1, "logs"))
	if !errors.Is(err, storage.ErrArtifactGone) {
		t.Errorf("a raw 410 yielded %v, want ErrArtifactGone (R14, AC9)", err)
	}
}

// TestClientDownloadKeepsAHostileNameInsideTheTargetDirectory pins that an Artifact's name is
// untrusted text and never a path. A name carrying separators or leading dots must not escape
// the target directory or land as a dotfile: the download writes one file, in the directory it
// was given, named after the Artifact's id.
func TestClientDownloadKeepsAHostileNameInsideTheTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	raw := requesterFunc(func(string, string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("payload")),
			Header:     http.Header{},
		}, nil
	})
	path, err := storage.ClientDownload(raw, dir)(liveArtifact(99, "../../etc/passwd"))
	if err != nil {
		t.Fatalf("ClientDownload returned an error: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("download landed at %s, outside the target directory %s", path, dir)
	}
	if base := filepath.Base(path); strings.HasPrefix(base, ".") || !strings.Contains(base, "99") {
		t.Errorf("download filename = %q, want a non-hidden name carrying the Artifact's id", base)
	}
}

// TestClientDownloadContainsTheRepositoryNameToo pins that the same containment covers every
// remote string in the filename, not only the Artifact's name. The owner and the repository
// are API text like any other, and a filename that trusts two of its three components has no
// standard at all.
//
// The fixture puts the target directory inside another that already exists, and aims the
// escape one level up at it. That is deliberate: writeArchive creates the target directory and
// no other, so an escape into a directory that does not exist fails on ENOENT and the test
// would pass for the wrong reason, reporting a filesystem error rather than a containment
// violation. Aiming at a directory that exists means an uncontained name lands successfully
// and the assertion below is the one that fires. It also matches how the escape would be
// exploited in the first place.
func TestClientDownloadContainsTheRepositoryNameToo(t *testing.T) {
	outside := t.TempDir()
	dir := filepath.Join(outside, "downloads")
	raw := requesterFunc(func(string, string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("payload")),
			Header:     http.Header{},
		}, nil
	})
	hostile := domain.Artifact{
		ID:   7,
		Name: "logs",
		Repo: domain.RepoID{Host: domain.HostGitHub, Owner: "../escaped", Name: "repo"},
	}
	path, err := storage.ClientDownload(raw, dir)(hostile)
	if err != nil {
		t.Fatalf("ClientDownload returned an error: %v", err)
	}
	if got := filepath.Dir(path); got != dir {
		t.Errorf("download landed in %s, outside the target directory %s: a repository name is remote text and is not a path", got, dir)
	}
	if base := filepath.Base(path); strings.HasPrefix(base, ".") {
		t.Errorf("download filename = %q, want a non-hidden name", base)
	}
	if entries, err := os.ReadDir(outside); err == nil {
		for _, e := range entries {
			if e.Name() != "downloads" {
				t.Errorf("the download created %q beside the target directory, escaping it", e.Name())
			}
		}
	}
}

// TestClientDownloadRefusesATombstoneWithoutARequest pins R14's first half: download is
// offered on no expired Artifact, so the seam itself refuses one and issues no request. The
// cassette tapes a 200, so a seam that asked anyway would succeed and write a file, and this
// test would fail on the file rather than on the error.
func TestClientDownloadRefusesATombstoneWithoutARequest(t *testing.T) {
	dir := t.TempDir()
	client := downloadClient(t, "artifact_download")

	tombstone := liveArtifact(12345, "build-logs")
	tombstone.Expired = true
	path, err := storage.ClientDownload(client, dir)(tombstone)
	if !errors.Is(err, storage.ErrArtifactGone) {
		t.Fatalf("downloading a Tombstone yielded %v, want ErrArtifactGone with no request issued (R14)", err)
	}
	if path != "" {
		t.Errorf("a Tombstone reported a written path %q, want none", path)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read target dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a Tombstone download wrote %d files, want none: its bytes are already gone (R14)", len(entries))
	}
}
