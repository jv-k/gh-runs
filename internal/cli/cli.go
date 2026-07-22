// Package cli is the non-interactive surface: the first thing a user can run, and
// the first place the store, the governor, discovery and the filter engine stand
// up together end to end (BUILD-ORDER stage 6). It is a drop-in superset of
// gh run's flags (ADR-0008), so gh run list to gh runs list is muscle memory,
// and it fans out across the discovered repositories where gh would fail
// (ADR-0022). This stage builds the read half, the list command; the write flags
// wait for the Purge at stage 9 (cli-surface R10, BUILD-ORDER).
//
// The flags are a thin adapter over internal/filter, never a second filter
// implementation: a value is validated by the same code with the same message
// wherever it arrives from (cli-surface R6, ADR-0016). cli imports domain and
// filter and reaches the transport only through the Requester seam below, which
// main.go fills with a ghclient over the store-then-governor chain and a test
// fills with the same ghclient over a cassette (ADR-0011, ADR-0012). It never
// imports tui, discovery, store or governor: the discovered set and the
// working-directory resolver arrive as injected functions (ADR-0011).
package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/spf13/cobra"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is exactly ghclient.Client's surface
// (ADR-0012: Request, never Get or Do, so the response's Link and rate-limit
// headers survive), narrowed to what the CLI uses. A cassette-backed ghclient
// fills it in tests, so the command is exercised against what the API actually
// said with no live network (cli-surface R19).
//
// Carry-forward, decided not to change in this read stage: the seam is context-free.
// The list command issues read-only one-shots that nothing cancels, so no read path
// reads a context. It gains a context.Context parameter at stage 7, where the Feed's
// Stop() cancels an in-flight poll, and at stage 9, where the write half exits 2 on
// SIGINT with in-flight cancellation (cli-surface R17, AC13). go-gh already exposes
// RequestWithContext, so the widening is a known extension, and ghclient.Client.Request
// mirrors this signature and moves with it.
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Deps carries the surface's seams. main.go fills them from the chain and
// discovery it already assembles; a test fills them with a cassette-backed client,
// a fixed discovered set and buffers. Keeping the discovered set and the
// working-directory resolver as functions is what lets cli stay clear of
// discovery and ghclient in its import graph (ADR-0011).
type Deps struct {
	// Client issues every request the CLI makes, through the whole chain.
	Client Requester
	// Discovered returns the repositories a fan-out lists across (cli-surface R22).
	// main.go wires it to discovery's poll set; it returns an error so an auth or
	// enumeration failure during discovery reaches the exit-code taxonomy (R17).
	Discovered func() ([]domain.RepoID, error)
	// Current resolves the working-directory repository, host-qualified
	// (cli-surface R8). An error means the tool was not launched inside a
	// repository, which is not a failure: it means fan out (R22).
	Current func() (domain.RepoID, error)
	// Getenv reads GH_REPO and GH_HOST (cli-surface R8). main.go passes a wrapper
	// over os.LookupEnv; a test passes a map, so no test reads the real environment.
	Getenv func(string) (string, bool)
	Stdout io.Writer
	Stderr io.Writer
	// Clock is the injected clock, read for the table's relative age column so a
	// golden output is deterministic (BUILD-ORDER's testing seams).
	Clock clock.Clock
}

// The process exit codes, gh's documented taxonomy (cli-surface R17, verified
// against gh help exit-codes). The read half reaches 0, 1 and 4; 2 (a cancelled
// Purge) belongs to the write half at stage 9.
const (
	exitOK      = 0
	exitFailure = 1
	exitAuth    = 4
)

// Execute builds the command over deps, runs it against args, and returns a
// process exit code (cli-surface R17). main.go passes os.Args[1:] and calls
// os.Exit on the result. It is the one place that maps an error to a code, so the
// taxonomy lives in one function rather than scattered across the commands.
func Execute(deps Deps, args []string) int {
	root := newRootCmd(deps)
	root.SetArgs(args)
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(deps.Stderr, "gh-runs:", err)
		return classify(err)
	}
	return exitOK
}

// classify maps an error to gh's exit-code taxonomy (cli-surface R17). An
// unresolved credential surfaces as a 401 from the wire, wrapped through
// discovery or the lister, and errors.As reaches it however deep it was wrapped,
// so authentication exits 4 rather than a generic 1 (AC14). Everything else is a
// failure, exit 1.
func classify(err error) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
		return exitAuth
	}
	return exitFailure
}

// newRootCmd is the cobra root. Invoked bare it opens the TUI (cli-surface R1),
// which is stage 7 and not yet built, so for now it prints its help. The list
// subcommand is the read half this stage delivers.
func newRootCmd(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:           "gh-runs",
		Short:         "A live GitHub Actions dashboard across your repositories, where deletion is one operation.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newListCmd(deps))
	return root
}
