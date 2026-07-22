package logview

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// maxLogBytes caps how much of a Job log is read into memory. A Job log has no upper bound
// (R21) and its size cannot be known before the download starts (R14), so the read is
// bounded rather than trusting the body: a runaway or hostile log cannot exhaust memory. The
// ceiling is far above any real Job log and is not a setting, because answering "how many
// megabytes of log" needs a measurement the user does not have (settings R13's mechanism
// test).
const maxLogBytes = 25 << 20

// Requester issues a request through the transport chain and returns the response for the
// caller to read and close. It is ghclient.Client's Request surface (ADR-0012), narrowed to
// the log fetch and the archive export. Its http.Client follows the 302 the logs endpoint
// returns to a signed blob URL (R13, taped in testdata/job_log.yaml). A cassette-backed
// ghclient fills it in production and in tests, so a fetch replays what the API actually
// said with no live network.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Fetch fetches one Job's plain-text log (R1). It is injected at construction, backed by
// ghclient in main.go and by a cassette or a fake in tests. It resolves the per-Job plain
// text and never the run-level archive (R2), which is a property of the path ClientFetch
// builds, not of this signature.
type Fetch func(repo domain.RepoID, jobID int64) ([]byte, error)

// Exporter downloads the whole-Run log archive and writes it to disk as received, returning
// the path it wrote (R11). It is the one place fetching everything is the user's intent, and
// it is a seam distinct from Fetch so no code path that renders a log can reach the archive
// (R2, AC5). A nil Exporter leaves the export key inert.
type Exporter func(repo domain.RepoID, runID int64) (path string, err error)

// ClientFetch is the production Fetch, wired in main.go over the shared ghclient (ADR-0015).
// It issues one GET for a Job's log, follows the 302 to the signed blob, and returns the
// bytes up to maxLogBytes. It re-requests the endpoint every call and reuses no signed URL:
// the URL expires in about a minute, so each fetch follows the redirect afresh and nothing
// persists, caches or logs it (R13). A non-200 (a 404 for deleted or in-progress logs, or a
// 5xx), and an error the RESTClient raises for a non-2xx, both yield an empty result and no
// surfaced failure, which the pane renders as R18's empty state (AC7).
func ClientFetch(client Requester) Fetch {
	return func(repo domain.RepoID, jobID int64) ([]byte, error) {
		resp, err := client.Request(http.MethodGet, jobLogPath(repo, jobID), nil)
		if err != nil {
			return nil, nil // R18: a 404 or 5xx is the empty state, not a surfaced failure (AC7)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, nil
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
	}
}

// ClientExport is the production Exporter, wired in main.go over the shared ghclient and a
// target directory main.go resolves. It fetches the run-level archive, follows the 303, and
// streams the zip to disk exactly as received, without unpacking, because unpacking assumes a
// directory the user did not ask for (R11). It writes one deterministic filename per Run and
// streams rather than buffering, so a large archive never lands in memory. The archive is the
// one fetch of everything, and it is here and nowhere the render path can reach (AC5).
func ClientExport(client Requester, dir string) Exporter {
	return func(repo domain.RepoID, runID int64) (string, error) {
		resp, err := client.Request(http.MethodGet, archivePath(repo, runID), nil)
		if err != nil {
			return "", fmt.Errorf("log export: fetching the archive failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("log export: the archive endpoint returned HTTP %d", resp.StatusCode)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("log export: creating %s: %w", dir, err)
		}
		name := fmt.Sprintf("%s-%s-run-%d-logs.zip", repo.Owner, repo.Name, runID)
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			return "", fmt.Errorf("log export: creating %s: %w", path, err)
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("log export: writing %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("log export: closing %s: %w", path, err)
		}
		return path, nil
	}
}

// jobLogPath is a single Job's plain-text log endpoint (R1). It names the Job by id, which is
// the latest Attempt's Job, and never the run-level archive (R2) and never a prior Attempt,
// which is not served (R16): those are properties of this path, not of the caller.
func jobLogPath(repo domain.RepoID, jobID int64) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/jobs/" + strconv.FormatInt(jobID, 10) + "/logs"
}

// archivePath is the run-level log archive endpoint, the export's alone (R2, R11). It is a
// distinct path from jobLogPath, so a reader can see at a glance that the render path and the
// export path reach different endpoints (AC5).
func archivePath(repo domain.RepoID, runID int64) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/logs"
}
