package storage

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// ErrArtifactGone is the outcome of asking for an Artifact whose bytes are already gone
// (R14, AC9). It is a distinct sentinel rather than a wrapped HTTP status because a 410 is
// not a failure the operator can act on: the Artifact expired, the archive was destroyed by
// the retention policy, and no retry, token or network will bring it back. Reporting it as a
// transient failure is the specific mistake R14 forbids.
var ErrArtifactGone = errors.New("the Artifact has expired and its bytes are gone")

// maxNameSlug caps how much of an Artifact's name reaches the filename. The name is
// user-controlled text of unbounded length, and a path is not the place to trust it.
const maxNameSlug = 48

// Downloader downloads one Artifact's archive and writes it to disk, returning the path it
// wrote (R13). It is a seam distinct from Fetch, and distinct again from the ops engine every
// deletion travels: downloading is the one genuine non-storage use case in this view and the
// one action here that destroys nothing, so it neither routes through the confirmation nor
// touches the deletion log. A nil Downloader leaves the download key inert, which is the
// golden path.
type Downloader func(a domain.Artifact) (path string, err error)

// ClientDownload is the production Downloader, wired in main.go over the shared ghclient and
// a target directory main.go resolves. It refuses a Tombstone outright, issuing no request,
// because an expired Artifact's download answers 410 Gone and its bytes are already gone
// (R14). Otherwise it GETs the Artifact's zip endpoint, follows the redirect to the signed
// blob, and streams the archive to disk exactly as received, without unpacking, because
// unpacking assumes a directory layout the user did not ask for. A 410 arriving anyway, from
// an Artifact that expired between the listing and the keystroke, reads as ErrArtifactGone
// and not as a transient failure (R14, AC9).
func ClientDownload(client Requester, dir string) Downloader {
	return func(a domain.Artifact) (string, error) {
		if a.Tombstone() {
			return "", ErrArtifactGone // R14: no download is offered on an expired Artifact
		}
		resp, err := client.Request(http.MethodGet, artifactArchivePath(a), nil)
		if err != nil {
			if statusOf(err) == http.StatusGone {
				return "", ErrArtifactGone // R14, AC9
			}
			return "", fmt.Errorf("artifact download: fetching the archive failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusGone {
			return "", ErrArtifactGone // R14, AC9: a raw transport reports the status rather than an error
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("artifact download: the archive endpoint returned HTTP %d", resp.StatusCode)
		}
		return writeArchive(dir, archiveName(a), resp.Body)
	}
}

// writeArchive streams body into dir/name and returns the path written. It streams rather
// than buffering, so an Artifact larger than memory still lands, and it removes a partial
// file on failure so no half-written archive is left looking like a download that worked.
func writeArchive(dir, name string, body io.Reader) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("artifact download: creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("artifact download: creating %s: %w", path, err)
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("artifact download: writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("artifact download: closing %s: %w", path, err)
	}
	return path, nil
}

// statusOf reads the HTTP status out of an error the RESTClient raised for a non-2xx, which
// is how a 410 arrives on the Request surface (ADR-0012: go-gh converts a non-2xx to an
// *api.HTTPError). It reports zero for anything else.
func statusOf(err error) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// artifactArchivePath is the Artifact download endpoint, which answers a redirect to a signed
// blob for a live Artifact and 410 Gone for an expired one (R13, R14, measured). zip is the
// only archive format the API serves.
func artifactArchivePath(a domain.Artifact) string {
	return "repos/" + a.Repo.Owner + "/" + a.Repo.Name +
		"/actions/artifacts/" + strconv.FormatInt(a.ID, 10) + "/zip"
}

// archiveName is the file one download writes: the repository, the Artifact's id and a slug
// of its name. The id makes it unique, so two Artifacts sharing a name never collide, and the
// name makes it recognisable months later. The slug keeps only characters safe in a filename,
// so a hostile Artifact name cannot escape the target directory or hide as a dotfile.
func archiveName(a domain.Artifact) string {
	base := a.Repo.Owner + "-" + a.Repo.Name + "-artifact-" + strconv.FormatInt(a.ID, 10)
	if slug := nameSlug(a.Name); slug != "" {
		base += "-" + slug
	}
	return base + ".zip"
}

// nameSlug reduces an Artifact's name to filename-safe characters, collapsing every run of
// anything else to a single hyphen and capping the length. It returns the empty string when
// nothing survives, in which case the id alone names the file.
func nameSlug(name string) string {
	var b strings.Builder
	lastHyphen := true // leading hyphens are dropped, so a name of punctuation yields nothing
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
		if b.Len() >= maxNameSlug {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
