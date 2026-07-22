// Command gh-runs is the composition root. It lives at the repository root, not
// under cmd/, which is what makes `go install github.com/jv-k/gh-runs/v2@latest`
// yield a binary called gh-runs and what cli/gh-extension-precompile builds by
// default (ADR-0010, ADR-0011).
//
// It is the only place that knows both store and ghclient exist. store exports an
// http.RoundTripper, ghclient takes one, and neither imports the other; wiring
// them here is the single most load-bearing decision in the tree (ADR-0011). It
// also nests the governor inside the store's transport (ADR-0012). This floor
// build resolves settings, assembles the chain and exits, which is enough to
// prove the wiring compiles and composes.
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/config"
	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
	"github.com/jv-k/gh-runs/v2/internal/limiter"
	"github.com/jv-k/gh-runs/v2/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		os.Exit(1)
	}
}

func run() error {
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
	base := http.DefaultTransport
	gov := governor.New(limiter.New(base, limiter.Bound), clk)
	transport := store.NewTransport(gov, storeDir(), clk)

	// go-gh installs our transport as opts.Transport with its own cache off
	// (CacheTTL: 0). ghclient exposes Request, never Get or Do.
	client, err := ghclient.New(ghclient.Options{Transport: transport})
	if err != nil {
		return err
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

	// A cold start paints from the persisted results before any request goes out
	// (local-store R5, repo-discovery R19). Running the live discovery pass belongs
	// to the surface that displays it (cli-surface, stage 6, exercises stages 1 to
	// 3), so this floor reloads and reports, proving the composition rather than
	// issuing a burst with nothing yet to render it.
	loaded := disc.Reload()

	fmt.Printf("gh-runs: transport chain and discovery wired, budget tier %q, "+
		"%d repositories reloaded from the local store\n", cfg.Budget, loaded)
	return nil
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
