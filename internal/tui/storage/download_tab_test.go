package storage_test

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
)

// errDiskFull stands for a genuine failure a retry might fix, the thing R14 forbids a 410
// from being reported as.
var errDiskFull = errors.New("no space left on device")

// recordingDownloader records what it was asked to download and answers with a canned
// result, so a tab test can assert both that the key reached the seam and what it carried.
type recordingDownloader struct {
	asked []domain.Artifact
	path  string
	err   error
}

func (d *recordingDownloader) download(a domain.Artifact) (string, error) {
	d.asked = append(d.asked, a)
	return d.path, d.err
}

// downloadTab builds a Storage tab wired to a recording Downloader, holding one repository's
// Artifacts.
func downloadTab(t *testing.T, d *recordingDownloader, arts ...domain.Artifact) storage.Model {
	t.Helper()
	m := storage.New(storage.Options{Profile: keys.Standard, Download: d.download})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m, _ = m.Update(storage.StorageFetched(storage.RepoStorage{
		Repo: rid("cli", "cli"), Artifacts: arts, ArtifactsComplete: true,
	}))
	return m
}

// runCmd runs a command and feeds its message back into the tab, the way the root's event
// loop would.
func runCmd(m storage.Model, cmd tea.Cmd) storage.Model {
	if cmd == nil {
		return m
	}
	m, _ = m.Update(cmd())
	return m
}

// TestDownloadKeyDownloadsTheArtifactUnderTheCursor pins R13: the download key downloads the
// Artifact under the cursor from its row and reports the path it wrote. Downloading is a
// genuine non-storage use case, so it is offered whether or not the Artifact is worth
// deleting: this one is tiny and would reclaim almost nothing.
func TestDownloadKeyDownloadsTheArtifactUnderTheCursor(t *testing.T) {
	d := &recordingDownloader{path: "/tmp/cli-cli-artifact-42-build-logs.zip"}
	m := downloadTab(t, d, domain.Artifact{ID: 42, Name: "build-logs", SizeInBytes: 145212, ExpiresAt: day})

	m2, cmd := m.Update(press("w"))
	if cmd == nil {
		t.Fatalf("the download key issued no command (R13)")
	}
	m2 = runCmd(m2, cmd)

	if len(d.asked) != 1 {
		t.Fatalf("the Downloader was asked %d times, want once (R13)", len(d.asked))
	}
	if d.asked[0].ID != 42 || d.asked[0].Repo != rid("cli", "cli") {
		t.Errorf("downloaded artifact %d of %v, want 42 of cli/cli (R13)", d.asked[0].ID, d.asked[0].Repo)
	}
	if got := m2.View(); !strings.Contains(got, "cli-cli-artifact-42-build-logs.zip") {
		t.Errorf("the tab does not report the path the download wrote (R13):\n%s", got)
	}
}

// TestDownloadIsUnavailableOnATombstone pins R14 and AC9's first sentence: download is
// unavailable on every row with expired: true, so the key issues no request over one. The
// bytes are already gone, and the tab says so rather than letting a request discover it.
func TestDownloadIsUnavailableOnATombstone(t *testing.T) {
	d := &recordingDownloader{path: "/tmp/should-not-happen.zip"}
	m := downloadTab(t, d, domain.Artifact{ID: 7, Name: "old-test-logs", SizeInBytes: 234131, Expired: true})

	m2, cmd := m.Update(press("w"))
	m2 = runCmd(m2, cmd)

	if len(d.asked) != 0 {
		t.Errorf("the download key reached the Downloader over a Tombstone; download must be unavailable on an expired Artifact (R14, AC9)")
	}
	got := m2.View()
	if !strings.Contains(got, "gone") {
		t.Errorf("pressing download on a Tombstone must report the bytes as gone (R14, AC9):\n%s", got)
	}
	if strings.Contains(got, "failed") {
		t.Errorf("a Tombstone's unavailable download must not read as a failure (R14, AC9):\n%s", got)
	}
}

// TestDownloadKeyIsInertOnACacheRow pins R13's scope: only an Artifact has an archive to
// download. A Cache row leaves the key inert, issuing nothing.
func TestDownloadKeyIsInertOnACacheRow(t *testing.T) {
	d := &recordingDownloader{path: "/tmp/should-not-happen.zip"}
	m := storage.New(storage.Options{Profile: keys.Standard, Download: d.download})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = fetched(m, oneCache("cli", "cli", 1, "setup-go", 302460229))

	m2, cmd := m.Update(press("w"))
	runCmd(m2, cmd)

	if len(d.asked) != 0 {
		t.Errorf("the download key reached the Downloader over a Cache row; only an Artifact has an archive (R13)")
	}
}

// TestA410ReadsAsGoneNotAsAFailure pins AC9's second sentence: given a stub answering 410
// Gone, the outcome reads as the bytes being gone rather than as a network failure a retry
// might fix. The Artifact was live in the listing and expired before the keystroke, which is
// the only way this response reaches a row the tab offered download on.
func TestA410ReadsAsGoneNotAsAFailure(t *testing.T) {
	d := &recordingDownloader{err: storage.ErrArtifactGone}
	m := downloadTab(t, d, domain.Artifact{ID: 9, Name: "build-logs", SizeInBytes: 145212, ExpiresAt: day})

	m2, cmd := m.Update(press("w"))
	if cmd == nil {
		t.Fatalf("the download key issued no command over a live Artifact (R13)")
	}
	m2 = runCmd(m2, cmd)

	got := m2.View()
	if !strings.Contains(got, "gone") {
		t.Errorf("410 Gone must read as the bytes being gone (R14, AC9):\n%s", got)
	}
	for _, transient := range []string{"failed", "try again", "retry"} {
		if strings.Contains(strings.ToLower(got), transient) {
			t.Errorf("410 Gone read as a transient failure (%q); the bytes are gone for good (R14, AC9):\n%s", transient, got)
		}
	}
}

// TestDownloadFailureReadsAsAFailure pins the other side of AC9: a genuine failure, which a
// retry might fix, is reported as one and is not dressed up as expiry.
func TestDownloadFailureReadsAsAFailure(t *testing.T) {
	d := &recordingDownloader{err: errDiskFull}
	m := downloadTab(t, d, domain.Artifact{ID: 9, Name: "build-logs", SizeInBytes: 145212, ExpiresAt: day})

	m2, cmd := m.Update(press("w"))
	m2 = runCmd(m2, cmd)

	got := m2.View()
	if !strings.Contains(got, "failed") {
		t.Errorf("a genuine download failure must read as a failure (R14):\n%s", got)
	}
	if strings.Contains(got, "gone") {
		t.Errorf("a genuine failure must not read as the bytes being gone (R14, AC9):\n%s", got)
	}
}

// TestDownloadKeyInertWithoutDownloader pins the golden path: with no Downloader wired the
// key is inert, exactly as the delete key is with no planner.
func TestDownloadKeyInertWithoutDownloader(t *testing.T) {
	m := storage.New(storage.Options{Profile: keys.Standard})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m, _ = m.Update(storage.StorageFetched(storage.RepoStorage{
		Repo:      rid("cli", "cli"),
		Artifacts: []domain.Artifact{{ID: 1, Name: "a", SizeInBytes: 10, ExpiresAt: day}},
	}))
	if _, cmd := m.Update(press("w")); cmd != nil {
		t.Errorf("the download key issued a command with no Downloader wired; it must be inert")
	}
}
