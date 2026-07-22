// Command gh-runs is the composition root. It lives at the repository root, not
// under cmd/, which is what makes `go install github.com/jv-k/gh-runs/v2@latest`
// yield a binary called gh-runs and what cli/gh-extension-precompile builds by
// default (ADR-0010, ADR-0011).
//
// It is the only place that knows both store and ghclient exist. store exports an
// http.RoundTripper, ghclient takes one, and neither imports the other; wiring
// them here is the single most load-bearing decision in the tree (ADR-0011). It
// also nests the governor inside the store's transport (ADR-0012). It resolves
// settings, assembles the chain, and hands the whole thing to the CLI, whose read
// half (the list command) is the first runnable surface (BUILD-ORDER stage 6).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/jv-k/gh-runs/v2/internal/cli"
	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	"github.com/jv-k/gh-runs/v2/internal/tui/rundetail"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
)

// responseHeaderTimeout bounds how long any single request waits for its response
// headers. It is the stage-7 carry-forward the scheduler's Requester deferred (ADR-0015):
// ghclient.Request takes no context, so scheduler.Stop cannot cancel an in-flight poll,
// and without a bound a hung connection could delay quit indefinitely. Bounding the
// header wait here, on the base transport, closes that without a signature change across
// the merged packages. It is generous, because it applies to every request and a slow
// GitHub response must not be aborted as if it were hung; quit's worst case is this.
const responseHeaderTimeout = 30 * time.Second

// shutdownGrace bounds how long the process waits for the engine to unwind on quit, so
// the UI closing feels immediate. A poll still in flight past it is left to the
// response-header timeout above and reaped by process exit; nothing it does is a write.
const shutdownGrace = 2 * time.Second

func main() {
	os.Exit(run())
}

// run assembles the transport chain and discovery, wires them into the CLI's
// dependencies, and executes the command, returning gh's exit code (cli-surface
// R17). A setup failure before the command runs is a plain exit 1: nothing has
// been issued yet, so there is no auth or cancellation state to report.
func run() int {
	clk := clock.Real()

	// Settings resolve first. The governor takes its Budget share from them at
	// stage 2, and a bad config surfaces its diagnostics here, before any request
	// goes out, rather than failing the run (settings R14). No CLI exists yet
	// (stage 6), so the flag layer is empty and env locates the file (R1, R4).
	cfg, diags := config.Load(os.LookupEnv, config.Flags{})
	for _, d := range diags {
		fmt.Fprintln(os.Stderr, "gh-runs: config:", d.Message)
	}

	// The transport chain, nested per ADR-0012, ADR-0018 and BUILD-ORDER's floor:
	//
	//     store.NewTransport(governor.New(limiter.New(base, Bound), clk), dir, clk)
	//
	// The store is outermost of ours and dials through the governor, which sits
	// under it and above the network so it observes real network exchanges and
	// only those. A 304 reaches the governor as a 304, before the store rewrites
	// it to a 200. The limiter is innermost, directly above the base, bounding the
	// whole process at Bound requests on the wire (ADR-0018): a slot measures what
	// GitHub's concurrency cap measures, so it sits below the governor's pacing.
	// http.DefaultTransport is the base in production; a cassette is the base in a
	// test, injected through this same parameter one layer below the limiter
	// (local-store R19a, ADR-0018 Consequences).
	base := baseTransport()
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, storeDir(), clk)

	// go-gh installs our transport as opts.Transport with its own cache off
	// (CacheTTL: 0). ghclient exposes Request, never Get or Do.
	client, err := ghclient.New(ghclient.Options{Transport: transport})
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		return 1
	}

	// Discovery stands on the whole chain: it issues its enumeration and probes
	// through the client (so the governor accounts them and the store revalidates
	// them, repo-discovery R17, R12), persists its classification and capability
	// through the store's document primitive (local-store R2), and reads the
	// governor's Budget Readout to stop a burst that meets exhaustion (R17). The
	// store satisfies discovery.Store, the governor satisfies discovery.Budget, and
	// main.go is the one place that knows all three, exactly as it is for the
	// transport chain itself (ADR-0011). CurrentRepo is the fast-path resolver with
	// the GH_TOKEN-aware error R14 requires.
	disc := discovery.New(discovery.Options{
		Client:  client,
		Store:   transport,
		Budget:  gov,
		Clock:   clk,
		Refresh: time.Duration(cfg.DiscoveryRefreshMinutes) * time.Minute,
		Current: ghclient.CurrentRepo,
	})

	// The write engine. It is the only DELETE path and the only writer of the deletion
	// log (ADR-0011, ADR-0019). main.go supplies the log's path so ops owns no directory
	// policy, exactly as it supplies the store's directory (R29), and the two thresholds
	// config resolved and clamped (settings R12, R21). It shares the one client, so the
	// governor paces its DELETEs and the store sits above them.
	purge := ops.New(ops.Options{
		Client:           client,
		Clock:            clk,
		LogPath:          deletionLogPath(),
		ConfirmThreshold: cfg.ConfirmThreshold,
		BreakerFailures:  cfg.BreakerFailures,
	})

	// main.go picks the surface (ADR-0011, cli-surface R1, R25): bare `gh runs`, and the
	// intent-synonym bare `gh runs delete`, open the TUI, where deletion is one
	// operation; any subcommand carrying flags or arguments runs the CLI. The composition
	// root already knows both, and that is where the choice belongs.
	if opensTUI(os.Args[1:]) {
		return runTUI(cfg, clk, client, gov, transport, disc, purge)
	}

	// The read half's dependencies. The discovered set is a function so cli stays
	// clear of discovery in its import graph (ADR-0011): a fan-out paints from the
	// persisted results first (local-store R5, repo-discovery R19), and only when
	// the cache is cold does it spend a live pass to learn the account. That policy
	// is main.go's, kept out of the surface, which the Feed will refine at stage 7.
	deps := cli.Deps{
		Client:  client,
		Current: ghclient.CurrentRepo,
		Getenv:  os.LookupEnv,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Clock:   clk,
		Purge:   purge,
		// RepoSnapshot is the capability data Plan gates eligibility on (purge R10). It
		// carries only Known repositories, so a repository in scope but not yet
		// enumerated is absent and Plan fails closed rather than guessing (repo-discovery
		// R8, ADR-0019).
		RepoSnapshot: func() (map[domain.RepoID]domain.Repo, error) {
			if disc.Reload() == 0 {
				if err := disc.Pass(context.Background(), nil); err != nil {
					return nil, err
				}
			}
			records := disc.Records()
			m := make(map[domain.RepoID]domain.Repo, len(records))
			for _, r := range records {
				if r.Known {
					m[r.ID()] = r.Repo()
				}
			}
			return m, nil
		},
		Discovered: func() ([]domain.RepoID, error) {
			if disc.Reload() == 0 {
				if err := disc.Pass(context.Background(), nil); err != nil {
					return nil, err
				}
			}
			// R22 says list fans out across "every discovered repository." This wires the
			// fan-out to discovery's poll set (~26, the repositories with Runs), not every
			// discovered Record (~163), for Budget parity with the Feed, which polls exactly
			// this set (ADR-0022's one-request-per-repository cost). An empty repository lists
			// nothing, so the only visible delta is a repository whose persisted
			// classification is stale on a warm cache (HasRuns=false but since acquired Runs),
			// which a cold-cache disc.Pass reclassifies. The Feed refines this "discovered"
			// scope at stage 7; the narrowing is provisional policy here, kept out of the cli
			// surface (ADR-0011).
			return disc.PollSet(), nil
		},
	}

	return cli.Execute(deps, os.Args[1:])
}

// runTUI opens the live Feed (live-run-feed R1). It refuses when standard output is not
// a terminal, rather than emit control sequences into a pipe (cli-surface R1). It stands
// the scheduler on the chain, discovery's poll set and the governor's Budget Readout,
// hands the root the engine channel and the pulls it broadcasts, runs the program, and
// stops the engine cleanly, bounded so quit stays snappy (ADR-0015).
func runTUI(cfg config.Config, clk clock.Clock, client *ghclient.Client, gov *governor.Governor, transport *store.Transport, disc *discovery.Discovery, purge *ops.Ops) int {
	if !term.FromEnv().IsTerminalOutput() {
		fmt.Fprintln(os.Stderr, "gh-runs: standard output is not a terminal; refusing to open the dashboard. Run `gh runs list` for non-interactive output.")
		return 1
	}

	// Reject a non-github.com remote explicitly rather than silently attributing its Runs to
	// github.com (R35, AC17). Being outside a git repository, or an unresolvable remote, is
	// not a rejection: the Feed falls back to progressive reveal across the discovered
	// account (R34).
	if err := currentHostSupported(ghclient.CurrentRepo); err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		return 1
	}

	// The keybinding profile is the resolved setting (live-run-feed R7, settings R5).
	profile := keys.Standard
	if p, ok := keys.ForName(string(cfg.KeybindingProfile)); ok {
		profile = p
	}

	// Seed discovery so the poll set is not empty on a warm cache; a cold cache spends
	// one pass and the Feed reveals repositories as they arrive (R32, R33). A discovery
	// failure is not fatal to the dashboard: the Feed still paints what it can.
	if disc.Reload() == 0 {
		_ = disc.Pass(context.Background(), nil)
	}

	sched := scheduler.New(scheduler.Options{
		Client:  client,
		PollSet: disc,
		Budget:  gov,
		Clock:   clk,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)

	root := tui.New(tui.Options{
		Updates:     sched.Updates(),
		Readout:     gov.Readout,
		Repos:       func() []domain.Repo { return knownRepos(disc) },
		Revalidated: func() time.Time { return newestRevalidated(transport, disc.PollSet()) },
		SetViewport: sched.SetViewport,
		Profile:     profile,
		// The detail pane fetches its Run's Jobs over the same client the whole tool shares,
		// so the store revalidates and the governor accounts each request (ADR-0015). The
		// clock is the tool's, so the pane's timing column reads the same wall clock as
		// everything else.
		DetailFetch: rundetail.ClientFetch(client),
		Clock:       clk,
		// The Feed's delete key freezes the selection into a Plan through this engine and
		// opens the confirmation over it (purge R4 to R9). It is the same ops the CLI's
		// delete command uses, so both surfaces run one confirmation and one DELETE path.
		Ops: purge,
		// The Storage tab reads Cache and Artifact usage over the same client, so the store
		// revalidates and the governor accounts each request (storage-reclamation R1), and it
		// freezes a Cache and Artifact selection into a reclamation Plan through the same ops
		// engine, so its DELETE travels the one mutation entry a Purge does (R17).
		StorageFetch: storage.ClientFetch(client),
		StorageOps:   purge,
		// The Workflows tab reads each repository's Workflow list over the same client, so the
		// store revalidates and the governor accounts each request (workflow-management R1), and
		// it enables or disables one Workflow through the same ops engine, so a toggle is paced
		// and travels the one write path every other write does (R5).
		WorkflowFetch: workflows.ClientFetch(client),
		WorkflowOps:   purge,
		// The dispatch form the Workflows tab opens reads the Workflow YAML at a ref and the
		// repository's environments over the same client (workflow-dispatch R5, R7), triggers the
		// workflow_dispatch through the same ops engine so it is paced and travels ops's write path
		// (R16), and remembers last-used inputs in the same local-store the discovery results live
		// in (R25). One client, one ops, one store, exactly as every other surface.
		DispatchFetch: dispatch.NewClientFetch(client),
		DispatchOps:   purge,
		DispatchStore: transport,
		// The log view fetches a Job's plain text and, on request, downloads the whole-Run
		// archive to the working directory, both over the same client (log-viewer R1, R11). Its
		// deletion reuses purge, the one mutation entry, so a log DELETE is paced and logged like
		// every other (R17).
		LogFetch:  logview.ClientFetch(client),
		LogExport: logview.ClientExport(client, exportDir()),
	})

	// tea.WithContext ties the program to the same context the engine runs under, so a
	// signal that cancels one cancels both.
	_, err := tea.NewProgram(root, tea.WithContext(ctx)).Run()

	// Stop the engine, bounded: the UI is already gone, so quit must not wait on a hung
	// poll. The response-header timeout bounds any in-flight read, and process exit reaps
	// a straggler (ADR-0015's carry-forward).
	stopped := make(chan struct{})
	go func() {
		sched.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownGrace):
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		return 1
	}
	return 0
}

// currentHostSupported reports an error only when the repository the tool was launched
// inside resolves to a host gh-runs does not serve, so runTUI rejects it explicitly rather
// than attributing its Runs to the wrong host (live-run-feed R35, AC17). Resolution routes
// through the same host-qualifying resolver the rest of the tool uses, and only its typed
// UnsupportedHostError is a rejection: being outside a git repository, or an unresolvable
// remote, returns nil so the Feed falls back to progressive reveal across the account (R34).
// errors.As unwraps, so a wrapped rejection is caught too, and the check reuses the one host
// validation domain.NewRepoID and discovery already raise.
func currentHostSupported(current func() (domain.RepoID, error)) error {
	_, err := current()
	var unsupported *domain.UnsupportedHostError
	if errors.As(err, &unsupported) {
		return unsupported
	}
	return nil
}

// baseTransport is the base RoundTripper the chain dials through, a clone of
// http.DefaultTransport carrying a response-header timeout so a hung poll cannot delay
// quit (ADR-0015's carry-forward). It replaces http.DefaultTransport at the foot of the
// chain the governor and store nest above (ADR-0012, ADR-0018).
func baseTransport() http.RoundTripper {
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t := dt.Clone()
		t.ResponseHeaderTimeout = responseHeaderTimeout
		return t
	}
	return http.DefaultTransport
}

// knownRepos is the capability snapshot the root broadcasts to the Feed's gate. Only a
// repository whose capability enumeration or adoption has recorded is included, so a
// fast-path repository whose Runs are showing but whose permissions have not arrived
// stays absent and reads not-yet-known (live-run-feed R18), never inferred from the fact
// that its Runs listed.
func knownRepos(disc *discovery.Discovery) []domain.Repo {
	records := disc.Records()
	out := make([]domain.Repo, 0, len(records))
	for _, r := range records {
		if r.Known {
			out = append(out, r.Repo())
		}
	}
	return out
}

// newestRevalidated is the freshest instant anything in the poll set was seen live, which
// a paused Feed states as what it is showing and as of when (local-store R7,
// live-run-feed R30). Zero when nothing has revalidated yet.
func newestRevalidated(transport *store.Transport, ids []domain.RepoID) time.Time {
	var newest time.Time
	for _, id := range ids {
		if t, ok := transport.LastRevalidated(id); ok && t.After(newest) {
			newest = t
		}
	}
	return newest
}

// storeDir returns the local store's directory under the XDG cache home
// (local-store R1, ADR-0017). Everything this tool derives lives there, while the
// deletion log alone keeps the XDG state home. main.go supplies the path so the
// store owns no directory policy of its own (ADR-0011), exactly as it supplies the
// deletion log's path to ops.
func storeDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "gh-runs")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gh-runs")
	}
	return filepath.Join(home, ".cache", "gh-runs")
}

// exportDir is where a whole-Run log archive is written on export: the current working
// directory, where a user expects a download to land (log-viewer R11). It is not state and
// not cache: it is a file the user asked for, so it goes where they are, not under XDG. A
// working directory that cannot be resolved falls back to ".", the same directory by name.
func exportDir() string {
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

// deletionLogPath returns the append-only deletion log's path under the XDG state
// home, defaulting to ~/.local/state/gh-runs/deletions.log (purge R29, settings R2).
// It is state, not cache: nobody wants it on a second machine or in a dotfiles
// repository, and it is the one thing under the state directory recoverable from
// nowhere else. main.go owns the path so ops owns no directory policy (ADR-0011).
func deletionLogPath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "gh-runs", "deletions.log")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gh-runs", "deletions.log")
	}
	return filepath.Join(home, ".local", "state", "gh-runs", "deletions.log")
}

// opensTUI reports whether the invocation opens the TUI rather than the CLI: bare
// `gh runs`, and the intent-synonym bare `gh runs delete` with no other argument
// (cli-surface R1, R25). Any subcommand carrying a flag or an argument runs the CLI,
// where the delete command's guards apply (R26).
func opensTUI(args []string) bool {
	return len(args) == 0 || (len(args) == 1 && args[0] == "delete")
}
