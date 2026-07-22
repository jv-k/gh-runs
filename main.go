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
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui"
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

	// main.go picks the surface (ADR-0011, cli-surface R1): bare `gh runs` opens the
	// TUI, and any subcommand or argument runs the CLI. The composition root already
	// knows both, and that is where the choice belongs.
	if len(os.Args[1:]) == 0 {
		return runTUI(cfg, clk, client, gov, transport, disc)
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
func runTUI(cfg config.Config, clk clock.Clock, client *ghclient.Client, gov *governor.Governor, transport *store.Transport, disc *discovery.Discovery) int {
	if !term.FromEnv().IsTerminalOutput() {
		fmt.Fprintln(os.Stderr, "gh-runs: standard output is not a terminal; refusing to open the dashboard. Run `gh runs list` for non-interactive output.")
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
// store owns no directory policy of its own (ADR-0011), exactly as it will supply
// the deletion log's path to ops.
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
