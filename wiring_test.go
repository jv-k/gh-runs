package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jonboulle/clockwork"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	storagetab "github.com/jv-k/gh-runs/v2/internal/tui/storage"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// blobMatcher matches a live request against a taped one on method and full URL. The full URL
// matters because a download spans two hosts, the API and the signed blob it redirects to. It
// deliberately does not match on If-None-Match: the store adds that header once it holds an
// ETag, and this test needs the request to replay either way so it can observe what the store
// did rather than fail on what it sent.
func blobMatcher(r *http.Request, i cassette.Request) bool {
	iu, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	return r.Method == i.Method && r.URL.String() == iu.String()
}

// wiringCassette opens the wiring cassette fresh, so two chains in one test each replay it
// from the start.
func wiringCassette(t *testing.T) http.RoundTripper {
	t.Helper()
	rec, err := recorder.New("testdata/wiring",
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(blobMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder: %v", err)
		}
	})
	return rec
}

// storeEntries counts the entries the local-store persisted into dir. The store writes one
// .json per cached resource beside its lock file, so the glob is the whole population.
func storeEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob store entries: %v", err)
	}
	return len(entries)
}

// artifact is the live Artifact the cassette serves.
func artifact() domain.Artifact {
	return domain.Artifact{
		ID:          12345,
		Name:        "build-logs",
		SizeInBytes: 145212,
		Repo:        domain.RepoID{Host: domain.HostGitHub, Owner: "o", Name: "r"},
	}
}

// TestArtifactDownloadDoesNotFeedTheLocalStore pins the wiring an Artifact download depends
// on (storage-reclamation R13). The chain is assembled exactly as run() assembles it, store
// over governor over limiter over the base, and the two clients newClients returns are the
// ones the tool uses.
//
// The local-store persists any 200 GET carrying an ETag: it reads the whole body into memory
// and writes it base64-in-JSON to disk (local-store R19). That is right for an API resource,
// which is small, revalidatable and worth not fetching twice, and wrong for an Artifact
// archive, which is an arbitrarily large one-shot binary behind a signed single-use URL that
// no future request can ever revalidate. Routing a download through the store would spend
// memory and disk proportional to the download in order to cache something unusable, in a
// tool whose subject is reclaiming storage.
//
// So the download dials the blob client, which shares the governor and the limiter (a
// download costs rate-limit points and must be paced and bounded like anything else) and
// stops short of the store. The control half asserts the shared client does persist, which is
// what stops the first assertion passing vacuously.
func TestArtifactDownloadDoesNotFeedTheLocalStore(t *testing.T) {
	t.Run("the seam the root receives leaves the store empty", func(t *testing.T) {
		dir := t.TempDir()
		cl, gov := wiredClients(t, dir)

		// The assertion is over the value the root is handed, not over the function that
		// produces it. Pinning the producer alone left the deciding line uncovered: the
		// pre-fix expression could be pasted back into the options literal and the whole
		// suite stayed green. Reading opts.StorageDownload is what closes that.
		opts := cl.tuiOptions(tuiDeps{
			Config:    config.Config{},
			Profile:   keys.Standard,
			Clock:     clockwork.NewFakeClock(),
			Scheduler: scheduler.New(scheduler.Options{}),
			Governor:  gov,
			Downloads: t.TempDir(),
		})

		path, err := opts.StorageDownload(artifact())
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		if path == "" {
			t.Fatal("the download wrote no file")
		}
		if n := storeEntries(t, dir); n != 0 {
			t.Errorf("an Artifact download left %d local-store entries, want none: an archive is a one-shot binary behind a signed URL and is not a cacheable resource", n)
		}
	})

	t.Run("the shared client would persist it", func(t *testing.T) {
		dir := t.TempDir()
		cl, _ := wiredClients(t, dir)

		if _, err := storagetab.ClientDownload(cl.shared, t.TempDir())(artifact()); err != nil {
			t.Fatalf("download: %v", err)
		}
		if n := storeEntries(t, dir); n == 0 {
			t.Fatal("the shared client persisted nothing, so the assertion above proves nothing; the control has stopped controlling")
		}
	})
}

// exportedRun is the Run whose log archive the cassette serves.
func exportedRun() (domain.RepoID, int64) {
	return domain.RepoID{Host: domain.HostGitHub, Owner: "o", Name: "r"}, 98765
}

// TestLogExportDoesNotFeedTheLocalStore pins the wiring the whole-Run log export depends on
// (log-viewer R11). It is the Artifact download's pair: the same defect, one endpoint over.
//
// The archive is an arbitrarily large one-shot zip served from a signed URL that lives about
// a minute (log-viewer R13). Routed through the store, persist reads the whole body into
// memory before ClientExport streams a byte of it, and then writes it base64-in-JSON to disk,
// so a caller asking for an N-byte archive pays N resident plus about 1.33N in the store on
// top of the N-byte file they wanted. The entry buys nothing back: the signed URL is
// single-use, and it carries no /repos/ path, so repoOf yields "" and no repository
// invalidation can ever reclaim it. It sits there until LRU evicts something useful to make
// room.
//
// So the export dials the blob client, which shares the governor and the limiter (an export
// costs rate-limit points and must be paced and bounded like anything else) and stops short
// of the store. The control half asserts the shared client does persist, which is what stops
// the first assertion passing vacuously.
func TestLogExportDoesNotFeedTheLocalStore(t *testing.T) {
	t.Run("the seam the root receives leaves the store empty", func(t *testing.T) {
		dir := t.TempDir()
		cl, gov := wiredClients(t, dir)

		// The assertion is over the value the root is handed, not over the function that
		// produces it, for the reason the Artifact case records: pinning a producer leaves
		// the options literal free to name the wrong client and the suite stays green.
		opts := cl.tuiOptions(tuiDeps{
			Config:    config.Config{},
			Profile:   keys.Standard,
			Clock:     clockwork.NewFakeClock(),
			Scheduler: scheduler.New(scheduler.Options{}),
			Governor:  gov,
			Downloads: t.TempDir(),
		})

		repo, runID := exportedRun()
		path, err := opts.LogExport(repo, runID)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		if path == "" {
			t.Fatal("the export wrote no file")
		}
		if n := storeEntries(t, dir); n != 0 {
			t.Errorf("a whole-Run log export left %d local-store entries, want none: an archive is a one-shot binary behind a signed URL and is not a cacheable resource", n)
		}
	})

	t.Run("the shared client would persist it", func(t *testing.T) {
		dir := t.TempDir()
		cl, _ := wiredClients(t, dir)

		repo, runID := exportedRun()
		if _, err := logview.ClientExport(cl.shared, t.TempDir())(repo, runID); err != nil {
			t.Fatalf("export: %v", err)
		}
		if n := storeEntries(t, dir); n == 0 {
			t.Fatal("the shared client persisted nothing, so the assertion above proves nothing; the control has stopped controlling")
		}
	})
}

// wiredClients assembles the chain exactly as run() assembles it, store over governor over
// limiter over the base, with a cassette at the foot and the store rooted at dir. It returns
// the governor too, because tuiOptions takes it for the Budget readout.
func wiredClients(t *testing.T, dir string) (clients, *governor.Governor) {
	t.Helper()
	clk := clockwork.NewFakeClock()
	gov := governor.New(limiter.New(wiringCassette(t), limiter.Bound), clk)
	cl, err := newClients(store.NewTransport(gov, dir, clk), gov, "dummy-fixed-token")
	if err != nil {
		t.Fatalf("build clients: %v", err)
	}
	return cl, gov
}

// TestTheConfiguredScopesReachTheTabs pins [settings] R19 at the composition root, which is
// the one place the loaded scopes and the tabs that read them meet. Both tabs carry both code
// paths and both resolvers are wired, so the setting was decoded, validated, rendered as a
// Settings row and then read by nobody: a config file stating this-repo was accepted and
// silently ignored, which is worse than an unrecognised key.
//
// The assertion is over the value the root is handed rather than over the tabs, because the
// tabs already have tests for what each scope fans out over. What was missing is the wire.
//
// The four combinations pin R19's independence directly: the two scopes are separate
// settings, so scoping one tab must leave the other alone, and a single field feeding both
// would pass any one row of this table.
func TestTheConfiguredScopesReachTheTabs(t *testing.T) {
	all, this := config.ScopeAllRepos, config.ScopeThisRepo
	for _, tc := range []struct {
		name          string
		workflows     config.Scope
		storage       config.Scope
		wantWorkflows workflows.Scope
		wantStorage   storagetab.Scope
	}{
		{"both default", all, all, workflows.ScopeAllRepos, storagetab.ScopeAllRepos},
		{"both this-repo", this, this, workflows.ScopeThisRepo, storagetab.ScopeThisRepo},
		{"workflows alone", this, all, workflows.ScopeThisRepo, storagetab.ScopeAllRepos},
		{"storage alone", all, this, workflows.ScopeAllRepos, storagetab.ScopeThisRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl, gov := wiredClients(t, t.TempDir())
			opts := cl.tuiOptions(tuiDeps{
				Config:    config.Config{WorkflowsScope: tc.workflows, StorageScope: tc.storage},
				Profile:   keys.Standard,
				Clock:     clockwork.NewFakeClock(),
				Scheduler: scheduler.New(scheduler.Options{}),
				Governor:  gov,
				Downloads: t.TempDir(),
			})

			if opts.WorkflowScope != tc.wantWorkflows {
				t.Errorf("workflows_scope %q reached the tab as %q, want %q (R19)", tc.workflows, opts.WorkflowScope, tc.wantWorkflows)
			}
			if opts.StorageScope != tc.wantStorage {
				t.Errorf("storage_scope %q reached the tab as %q, want %q (R19)", tc.storage, opts.StorageScope, tc.wantStorage)
			}
			// this-repo means the repository of the working directory, so a stated scope with no
			// resolver behind it would fall back to all-repos and the setting would still not work.
			if opts.WorkflowCurrentRepo == nil || opts.StorageCurrentRepo == nil {
				t.Error("a tab was handed a scope with no working-directory resolver; this-repo would fall back to all-repos (R19)")
			}
		})
	}
}
