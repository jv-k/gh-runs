// Command gh-runs is the composition root. It lives at the repository root, not
// under cmd/, which is what makes `go install github.com/jv-k/gh-runs/v2@latest`
// yield a binary called gh-runs and what cli/gh-extension-precompile builds by
// default (ADR-0010, ADR-0011).
//
// It is the only place that knows both store and ghclient exist. store exports an
// http.RoundTripper, ghclient takes one, and neither imports the other; wiring
// them here is the single most load-bearing decision in the tree (ADR-0011). It
// also nests the governor inside the store's transport (ADR-0012). This floor
// build assembles the chain and exits, which is enough to prove the wiring
// compiles and composes.
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/governor"
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

	// The transport chain, nested per ADR-0012 and BUILD-ORDER's floor:
	//
	//     store.NewTransport(governor.New(base, clk), dir, clk)
	//
	// The store is outermost of ours and dials through the governor, which sits
	// under it and above the network so it observes real network exchanges and
	// only those. A 304 reaches the governor as a 304, before the store rewrites
	// it to a 200. http.DefaultTransport is the base in production; a cassette is
	// the base in a test, injected through this same parameter (local-store R19a).
	base := http.DefaultTransport
	gov := governor.New(base, clk)
	transport := store.NewTransport(gov, storeDir(), clk)

	// go-gh installs our transport as opts.Transport with its own cache off
	// (CacheTTL: 0). ghclient exposes Request, never Get or Do.
	client, err := ghclient.New(ghclient.Options{Transport: transport})
	if err != nil {
		return err
	}
	_ = client // The surfaces that exercise the client arrive in later stages.

	fmt.Println("gh-runs: transport chain wired (store over governor over network)")
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
