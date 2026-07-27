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
	"github.com/cli/go-gh/v2/pkg/auth"
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
	"github.com/jv-k/gh-runs/v2/internal/palette"
	"github.com/jv-k/gh-runs/v2/internal/scheduler"
	"github.com/jv-k/gh-runs/v2/internal/store"
	"github.com/jv-k/gh-runs/v2/internal/tui"
	"github.com/jv-k/gh-runs/v2/internal/tui/approval"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
	"github.com/jv-k/gh-runs/v2/internal/tui/logview"
	"github.com/jv-k/gh-runs/v2/internal/tui/rundetail"
	"github.com/jv-k/gh-runs/v2/internal/tui/storage"
	"github.com/jv-k/gh-runs/v2/internal/tui/workflows"
	"github.com/jv-k/gh-runs/v2/internal/workflowlist"
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
	//
	// The token is resolved once, here, and handed to both surfaces. go-gh resolves an empty
	// AuthToken itself, but it does so per RESTClient and per HTTPClient, and two surfaces
	// means four resolutions. Each one can shell out to the gh binary to reach the keyring
	// (ADR-0002), so leaving them to resolve costs a keyring-backed user three extra
	// subprocess spawns at every launch. An empty result is passed through rather than
	// reported here, so go-gh raises its own "authentication token not found" message and the
	// failure a user sees is unchanged. The host is resolved the way go-gh resolves it and is
	// used only to select the token: it is deliberately not passed as an option, because
	// ghclient derives its REST prefix from that field and 2.0.0 serves github.com alone
	// (ADR-0009).
	authHost, _ := auth.DefaultHost()
	token, _ := auth.TokenForHost(authHost)
	cl, err := newClients(transport, gov, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		return 1
	}
	client := cl.shared

	// Discovery stands on the whole chain: it issues its enumeration and probes
	// through the client (so the governor accounts them and the store revalidates
	// them, repo-discovery R17, R12), persists its classification and capability
	// through the store's document primitive (local-store R2), and reads the
	// governor's Budget Readout to stop a burst that meets exhaustion (R17). The
	// store satisfies discovery.Store, the governor satisfies discovery.Budget, and
	// main.go is the one place that knows all three, exactly as it is for the
	// transport chain itself (ADR-0011). CurrentRepo is the fast-path resolver with
	// the GH_TOKEN-aware error R14 requires.
	//
	// Exclude is settings R7's exclude list, resolved by config and handed over the same
	// seam as the refresh interval: discovery may not import config (ADR-0011), so
	// main.go is where a setting becomes an argument. Exclusion removes a repository
	// from discovery, the Feed and all polling, so it receives zero requests (AC5).
	disc := discovery.New(discovery.Options{
		Client:  client,
		Store:   transport,
		Budget:  gov,
		Clock:   clk,
		Refresh: time.Duration(cfg.DiscoveryRefreshMinutes) * time.Minute,
		Current: ghclient.CurrentRepo,
		Exclude: cfg.Exclude,
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
		// The governor is also the pacer: a running operation's progress carries the write
		// ceiling and floor it is being paced between, so the surface computes purge R15's
		// remaining-time range from the governor's own bounds rather than from a guess
		// (AC23). It is the same governor the transport chain is built on, so the ceiling
		// the surface reports is the one the DELETEs are actually issued under.
		Pacing: gov,
	})

	// main.go picks the surface (ADR-0011, cli-surface R1, R25): bare `gh runs`, and the
	// intent-synonym bare `gh runs delete`, open the TUI, where deletion is one
	// operation; any subcommand carrying flags or arguments runs the CLI. The composition
	// root already knows both, and that is where the choice belongs.
	if opensTUI(os.Args[1:]) {
		return runTUI(cfg, clk, cl, gov, transport, disc, purge)
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
		// The same exclude list discovery got (settings R7), so naming an excluded
		// repository with -R is refused before any request rather than crawled and then
		// failed at Plan with a capability message that names the wrong cause.
		Exclude: cfg.Exclude,
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
func runTUI(cfg config.Config, clk clock.Clock, cl clients, gov *governor.Governor, transport *store.Transport, disc *discovery.Discovery, purge *ops.Ops) int {
	client := cl.shared
	if !term.FromEnv().IsTerminalOutput() {
		fmt.Fprintln(os.Stderr, "gh-runs: standard output is not a terminal; refusing to open the dashboard. Run `gh runs list` for non-interactive output.")
		return 1
	}

	// The repository the tool was launched inside, which the engine polls first (R32). A
	// non-github.com remote is rejected explicitly here rather than having its Runs silently
	// attributed to github.com (R35, AC17). Being outside a git repository, or an
	// unresolvable remote, is not a rejection: there is simply no fast path, and the Feed
	// falls back to progressive reveal across the discovered account (R34).
	launched, err := fastPathRepo(ghclient.CurrentRepo, cfg.Exclude)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-runs:", err)
		return 1
	}
	first := launched.repo
	if advice := launched.advice; advice != nil {
		// R14's instruction, printed before the alt screen is entered and therefore still on
		// the scrollback the operator returns to. It is not fatal: the dashboard opens and
		// shows whatever the token can reach, which without one is nothing, and this line is
		// the only thing on screen that says why.
		fmt.Fprintln(os.Stderr, "gh-runs:", advice)
	}

	// The keybinding profile is the resolved setting (live-run-feed R7, settings R5).
	profile := keys.Standard
	if p, ok := keys.ForName(string(cfg.KeybindingProfile)); ok {
		profile = p
	}

	// The colour profile is resolved here and handed to Bubble Tea explicitly, rather than
	// left to colorprofile.Detect, which resolves NO_COLOR through strconv.ParseBool and so
	// resolves it against R15 (settings R15a, ADR-0013). DetectCapability reports what the
	// output stream can carry with the three colour variables kept out of it, and
	// palette.ColorProfile is the only thing that reads them.
	colorProfile := palette.ColorProfile(os.LookupEnv, palette.DetectCapability(os.Stdout, os.Environ()))

	// The palette the views paint with is not resolved here. The root applies the theme as it
	// is constructed, asks the terminal for its background as it starts, and re-resolves when
	// the answer arrives or the operator changes the setting, so auto follows the terminal as
	// gh does and a change applies from the next frame (settings R6, R17).

	// Reload the persisted classification before the engine exists. It is a local-store read
	// that issues nothing, so it is not the repository discovery R32 forbids waiting on, and
	// it must not sit behind the fast path's gate for two reasons. It is what makes the gate
	// load-bearing: without it the poll set is empty at every launch, the gate has nothing to
	// hold back, and R32 would rest on the discovery goroutine's wait alone. And the root
	// reads the same records for the Feed's capability gate (Repos below), which behind the
	// gate would stay empty until a network response landed, or for a whole exhaustion window
	// if the Budget were exhausted at launch, despite a fully populated local-store.
	seeded := disc.Reload()

	sched := scheduler.New(scheduler.Options{
		Client:  client,
		PollSet: disc,
		// R32: the engine polls this repository alone until its opening poll lands, so the
		// Feed paints it from a single Run listing (AC16). Zero outside a git repository,
		// where nothing is held back and the whole set reveals progressively (R34).
		First: first,
		// The exemption above ends when discovery answers, so the launch repository does not
		// outstay polling-scheduler R2: classified with Runs it is in the poll set already,
		// classified without them it leaves the rotation like any other.
		Classified: classifiedBy(disc),
		Budget:     gov,
		Clock:      clk,
		Workflows:  cl.workflowLister(),
	})
	// The launch filter is published before the first poll, so the opening listing is already
	// the filtered one rather than an unfiltered page the Feed narrows on arrival (settings
	// R9, live-run-feed R22). The Feed holds the same value from its first frame; this is the
	// server-side half of one setting, and both halves read the resolved config.
	sched.SetFilter(cfg.LaunchFilter)
	// R7's pin list, published before the first poll for the same reason the launch filter
	// is: a pinned repository should be on its promoted cadence from the opening tick rather
	// than from whenever the list happens to reach the scheduler (settings R7, #97).
	sched.SetPinned(cfg.Pin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)

	// The discovery pass runs behind the fast path rather than in front of it (R32, R33). It
	// is the one thing here that must not be waited on: it costs an enumeration and a
	// Run-listing probe per repository, so running it first is both the wait R32 forbids and
	// ~163 Run listings ahead of the one AC16 counts. A warm local-store has already seeded
	// the poll set above and spends no pass at all.
	go discoverBehind(ctx, sched, disc, first, seeded)

	root := tui.New(cl.tuiOptions(tuiDeps{
		Config:    cfg,
		Profile:   profile,
		Clock:     clk,
		Scheduler: sched,
		Governor:  gov,
		Store:     transport,
		Discovery: disc,
		Ops:       purge,
		Downloads: downloadDir(),
	}))

	// tea.WithContext ties the program to the same context the engine runs under, so a
	// signal that cancels one cancels both. tea.WithColorProfile is R15a's requirement in
	// one call: the program renders at the profile resolved above and never detects its own.
	_, err = tea.NewProgram(root, tea.WithContext(ctx), tea.WithColorProfile(colorProfile)).Run()

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

// fastPathRepo resolves the repository the tool was launched inside, which the engine polls
// first and alone (live-run-feed R32, repo-discovery R14).
//
// **It must stay the only caller of the resolver on the TUI path.** The resolver shells out
// to git, and one launch needs the answer three times: as the engine's Options.First, as
// R35's host gate, and as the identity R22's adoption admits. It is resolved once here and
// used for all three. discovery.Options.Current names the same resolver and nothing on this
// path calls it: adoption takes the resolved identity through AdoptLaunchRepo precisely so it
// does not, and the FastPath and Discover entrypoints that would are for a caller holding no
// answer of its own, which this is not. Reaching for the resolver again would be a second git
// subprocess per launch.
//
// An excluded repository is never the fast path. settings R7 removes an excluded repository
// from "discovery, the Feed and all polling", and Options.First is a polling path that
// bypasses discovery entirely, so leaving it unfiltered would poll a repository the operator
// told the tool to leave alone. discovery.FastPath already refuses one on the same ground
// (fastpath.go), so this keeps the two entrypoints agreeing rather than inventing a rule.
// Returning the zero repository, not an error: an excluded launch repository is not a
// failure, it is a session with no fast path, which is R34's fallback exactly.
//
// **Resolution has three outcomes, and they are returned separately because they want
// three different things from the caller.**
//
// reject is the one failure that stops the session: the repository resolves to a host
// gh-runs does not serve, so runTUI refuses rather than attributing its Runs to the wrong
// host (R35, AC17). Only the typed UnsupportedHostError is a rejection, and errors.As
// unwraps, so a wrapped one is caught too. The check reuses the one host validation
// domain.NewRepoID and discovery already raise.
//
// advice is a failure the operator can fix, reported without stopping anything
// (repo-discovery R14). go-gh gates its answer on auth.KnownHosts, which never reads the
// keyring, so on a machine where gh was never installed resolution fails even though git
// works and the remote is plainly github.com. Setting GH_TOKEN clears it. Saying nothing
// leaves that operator watching every request fail with no idea why, which is the state
// this closes (issue #100): R14's instruction was written and unreachable.
//
// Everything else is neither. Being outside a git repository, or holding a remote that
// simply does not resolve, yields the zero repository and no word to the operator: there
// is no fast path, the Feed falls back to progressive reveal across the account (R34), and
// an instruction here would name a problem they do not have. An excluded launch repository
// takes the same silent path, because it is a configured choice rather than a failure.
func fastPathRepo(current func() (domain.RepoID, error), exclude []domain.RepoID) (launch, error) {
	id, err := current()
	var unsupported *domain.UnsupportedHostError
	if errors.As(err, &unsupported) {
		return launch{}, unsupported
	}
	if errors.Is(err, ghclient.ErrRemoteHostUnrecognised) {
		return launch{advice: err}, nil
	}
	if err != nil {
		return launch{}, nil
	}
	for _, ex := range exclude {
		if ex == id {
			return launch{}, nil
		}
	}
	return launch{repo: id}, nil
}

// launch is what resolving the launch repository yielded, short of a rejection. The two
// fields travel together because they are one answer to one question, and because the
// alternative shape returns two errors from one call, where a caller can transpose them
// silently and still compile.
//
// The zero value is a complete answer: no fast path and nothing to say about it, which is
// every session launched outside a git repository and every one whose launch repository the
// operator excluded.
type launch struct {
	// repo is the repository the engine polls first and alone (R32), or the zero value when
	// there is no fast path.
	repo domain.RepoID
	// advice is a resolution failure the operator can act on, to be reported without
	// stopping anything (R14). Nil unless there is something worth their attention.
	advice error
}

// classifiedBy is the engine's Classified seam over discovery's record set: whether
// discovery holds a record for a repository, and therefore whether its classification is the
// answer (scheduler.Options.Classified). It is a function over domain types so the engine
// imports no discovery, exactly as the Workflow-list seam is (ADR-0011).
//
// Only the launch repository is ever asked about, at most twice per scheduling decision, so
// the scan is over ~163 records a few times a second at the fast tier and is not worth an
// index. A repository discovery excluded is in no record (settings R7), which reads here as
// unclassified, but no excluded repository can reach this: fastPathRepo has already declined
// to name one as the fast path, so the engine never asks.
func classifiedBy(disc *discovery.Discovery) func(domain.RepoID) bool {
	return func(id domain.RepoID) bool {
		for _, r := range disc.Records() {
			if r.ID() == id {
				return true
			}
		}
		return false
	}
}

// discoverBehind runs repository discovery behind the fast path, never in front of it
// (live-run-feed R32, R33). It is the ordering AC16 measures, and it is a named function
// rather than an inline goroutine body because that ordering is the whole feature: the
// composition root is where it is decided, and this is the line a test can hold.
//
// It waits for the engine's opening poll of the launch repository to land before it reads
// anything. A discovery pass costs an account enumeration plus one Run-listing probe per
// repository, ~163 of them at reference scale (repo-discovery R1, R16), so starting it first
// puts every one of those on the wire ahead of the single Run listing AC16 counts, and makes
// the Feed wait on repository discovery, which is exactly what R32 forbids. Running it after
// costs the fast path nothing: the pass then reveals the rest of the account progressively,
// and the scheduler adopts the growing poll set with no restart (polling-scheduler R3).
//
// Each repository it learns about is handed to the engine as it is learned, which is what
// makes R33's reveal progressive rather than a batch: discovery publishes a repository as
// its probe returns (repo-discovery R15), and the engine polls it at the next decision
// instead of at the end of the wait it was already asleep in.
//
// seeded is what the caller's local-store reload returned, so the reload itself stays in
// front of the engine (it issues nothing, and the poll set it seeds is what gives the gate
// something to hold back) while only the pass, which is all network, runs behind the gate.
//
// first is the launch repository, already resolved by the caller, and it is what closes
// repo-discovery R22 here (issue #100). Adoption runs after the record set is populated,
// whether the pass filled it or the reload did, because R22's condition is that enumeration
// does not hold the repository and that is just as true of a warm launch. It runs behind the
// same gate for the same reason the pass does, so it can add no request in front of the one
// AC16 counts. The identity is passed rather than the resolver deliberately: the resolver
// shells out to git and the caller has already paid for the answer, so discovery reaching
// for it again would be a second subprocess per launch.
//
// A cancelled context releases the wait, so quit is never held by a fast path that never
// landed. A discovery failure is not fatal to the dashboard: the Feed still paints what it
// can, which on a warm local-store is the whole persisted set.
//
// It recovers rather than letting a panic through. This code used to run before the UI
// opened, where a crash left a usable terminal behind. It now runs alongside a live Bubble
// Tea program holding the terminal in raw mode, and an unrecovered panic in this goroutine
// would take the process down without giving the program a chance to restore it, leaving the
// user's shell without an echo or a working newline. Losing discovery is a degraded Feed;
// losing the terminal is a broken session.
func discoverBehind(ctx context.Context, sched *scheduler.Scheduler, disc *discovery.Discovery, first domain.RepoID, seeded int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "gh-runs: repository discovery failed:", r)
		}
	}()

	select {
	case <-sched.Primed():
	case <-ctx.Done():
		return
	}
	tellEngine := func(discovery.Record) { sched.PollSetChanged() }

	// A warm local-store already holds the whole classified set (local-store R5,
	// repo-discovery R19), so no pass is spent and there is nothing to tell the engine about
	// it: the poll set was seeded before the engine was constructed, and opening the gate
	// already woke it to re-read that set. A cold one spends the pass, and the engine is told
	// per repository as each is classified (repo-discovery R15, live-run-feed R33). The wakes
	// coalesce, so ~163 of them cost a handful of re-evaluations and no extra request.
	if seeded == 0 {
		if err := disc.Pass(ctx, tellEngine); err != nil {
			// R22 adopts "when enumeration completes". A pass that failed has not completed,
			// and its record set is partial, so a repository missing from it is not evidence
			// that enumeration would never return it. Adopting on that evidence would mark an
			// ordinary member Adopted, which is a session-scoped membership the next launch
			// would not re-admit. Declining leaves the capability not-yet-known, the safe
			// state, and the next launch tries again.
			return
		}
	}

	// R22: the launch repository is adopted for the session when enumeration did not return
	// it, which is a clone the account does not own. Its failure is not reported: adoption is
	// a single-request convenience, and failing it leaves the repository painted with its
	// Runs and its capability not-yet-known, which is the safe state (R8, AC8). Both launches
	// reach this, warm and cold, because a warm store no more contains a repository
	// enumeration never returned than a fresh pass does.
	_ = disc.AdoptLaunchRepo(ctx, first, tellEngine)
}

// clients is the pair of request surfaces the tool dials one assembled chain with.
//
// shared is everything's client: it enters at the store, so a read revalidates and costs
// nothing when nothing changed (local-store R8, ADR-0012). blob enters one layer lower, at
// the governor, so it is still paced and still bounded by the limiter, and the store is not
// in its path.
//
// The distinction exists because the store persists any 200 GET carrying an ETag: it reads
// the whole body into memory and writes it base64-in-JSON to disk (local-store R19). For an
// API resource that is the point. For an Artifact archive it is a defect with teeth: the
// archive is an arbitrarily large one-shot binary served from a signed, single-use blob URL,
// so caching it spends memory and disk proportional to the download and caches something no
// later request can revalidate or even address. The blob URL carries no /repos/ path either,
// so repoOf yields "" and no repository invalidation could ever reclaim the entry. A tool
// whose subject is reclaiming storage must not consume multiples of an Artifact in hidden
// storage to hand the user a copy of it.
//
// Three transfers are routed this way, and they are the three that have that shape: the
// Artifact download, the whole-Run log export and the per-Job log fetch. Every other request
// is an API resource, small and revalidatable, and belongs on shared.
//
// The shape is the redirect to a signed URL, not the size. The per-Job fetch was wired to
// shared for three stages because it is plain text and reads like an ordinary API call, and
// the comment above it said so. It is not one: the store persists the blob the redirect leads
// to, keyed by a URL that expires in about a minute (log-viewer R13). Size is what makes the
// archive cases expensive, and it is not what makes any of them wrong.
type clients struct {
	shared *ghclient.Client
	blob   *ghclient.Client
}

// newClients builds both surfaces over one chain. token is resolved once by the caller and
// passed to both, so neither reaches the gh keyring again (ADR-0002); an empty one is passed
// through, leaving go-gh to raise its own not-authenticated error. A test injects a fixed one.
func newClients(chain, gov http.RoundTripper, token string) (clients, error) {
	shared, err := ghclient.New(ghclient.Options{AuthToken: token, Transport: chain})
	if err != nil {
		return clients{}, err
	}
	blob, err := ghclient.New(ghclient.Options{AuthToken: token, Transport: gov})
	if err != nil {
		return clients{}, err
	}
	return clients{shared: shared, blob: blob}, nil
}

// storageDownload is the Storage tab's download seam (storage-reclamation R13). It is a named
// function rather than an inline expression because which of the two surfaces it dials is the
// whole decision: blob, never shared, so an Artifact archive does not travel the store.
func (c clients) storageDownload(dir string) storage.Downloader {
	return storage.ClientDownload(c.blob, dir)
}

// logExport is the log view's whole-Run archive seam (log-viewer R11), and storageDownload's
// pair. Same reason for being named, and the same answer: blob, never shared. The archive is
// larger than an Artifact more often than not, and the signed URL it arrives from lives about
// a minute (log-viewer R13), so a store entry for it is unreachable the moment it is written.
func (c clients) logExport(dir string) logview.Exporter {
	return logview.ClientExport(c.blob, dir)
}

// logFetch is the log view's per-Job plain-text seam (log-viewer R1), and the third member of
// the pair above. Same reason for being named, and the same answer: blob, never shared.
//
// It reads like an API resource and is not one. The Job-log endpoint answers a 302 to a signed
// blob URL, and http.Client follows that redirect through the same Transport, so on shared the
// blob GET reaches the store. persist runs only on a 200, so it never sees the redirect and
// always sees the blob, and store.key hashes the request URL, so the entry is addressed by a
// single-use URL that expires in about a minute and no later request can ever carry. repoOf
// yields "" for that path, so no repository invalidation reclaims it either. Nothing revalidates
// it, and R13 says nothing should persist per signed fetch at all.
//
// The store's own 8 MiB ceiling (local-store R25) does not close this, and #153 says so at
// length: it bounds the read, so a log above it costs a bounded buffer and writes nothing, but
// a log below it still writes an entry that is unreachable forever and still carries Repo "".
// A store-side rule and a client-side choice are defence in depth, not alternatives.
func (c clients) logFetch() logview.Fetch {
	return logview.ClientFetch(c.blob)
}

// tuiDeps is everything the root's dependency set needs that is not a request surface: the
// resolved settings, the engines, and the directory a download lands in. It is a struct rather
// than nine parameters because the composition root's dependency list is exactly the kind of
// thing that grows, and a positional list of that length is a swap waiting to happen.
type tuiDeps struct {
	Config    config.Config
	Profile   keys.Profile
	Clock     clock.Clock
	Scheduler *scheduler.Scheduler
	Governor  *governor.Governor
	Store     *store.Transport
	Discovery *discovery.Discovery
	Ops       *ops.Ops
	Downloads string
}

// tuiOptions assembles the root's dependency set. It is extracted from runTUI so a test can
// assert over the value the root is actually handed rather than over the functions that
// produce it: pinning storageDownload's body alone left this literal uncovered, and the
// pre-fix expression could be pasted back into it with the whole suite staying green. That is
// the difference between guarding a decision and guarding the place it is taken.
//
// Every field naming a client names shared, the surface that enters at the store, except the
// Artifact download, the whole-Run log export and the per-Job log fetch, which name blob (see
// the clients doc above).
func (c clients) tuiOptions(d tuiDeps) tui.Options {
	client := c.shared
	return tui.Options{
		Updates:     d.Scheduler.Updates(),
		Readout:     d.Governor.Readout,
		Repos:       func() []domain.Repo { return knownRepos(d.Discovery) },
		Revalidated: func() time.Time { return newestRevalidated(d.Store, d.Discovery.PollSet()) },
		SetViewport: d.Scheduler.SetViewport,
		SetFilter:   d.Scheduler.SetFilter,
		// The Feed's filter carries the repository axis, so repo:this-repo resolves through
		// the same working-directory resolver the other two tabs' scope keys use (ADR-0016).
		FeedCurrentRepo: currentRepoID,
		Profile:         d.Profile,
		// The Settings pane opens over the resolved config and writes changed keys back through
		// config.Save, preserving comments, key order and keys this version does not recognise
		// (settings R17, AC11). os.LookupEnv locates the same config.yml Load read at startup, so
		// the pane's only write is that one local file and never the API.
		Config:       d.Config,
		SaveSettings: func(prev, next config.Config) error { return config.Save(os.LookupEnv, prev, next) },
		// The detail pane fetches its Run's Jobs over the same client the whole tool shares,
		// so the store revalidates and the governor accounts each request (ADR-0015). The
		// clock is the tool's, so the pane's timing column reads the same wall clock as
		// everything else.
		DetailFetch: rundetail.ClientFetch(client),
		Clock:       d.Clock,
		// The Feed's delete key freezes the selection into a Plan through this engine and
		// opens the confirmation over it (purge R4 to R9). It is the same ops the CLI's
		// delete command uses, so both surfaces run one confirmation and one DELETE path.
		Ops: d.Ops,
		// The Storage tab reads Cache and Artifact usage over the same client, so the store
		// revalidates and the governor accounts each request (storage-reclamation R1), and it
		// freezes a Cache and Artifact selection into a reclamation Plan through the same ops
		// engine, so its DELETE travels the one mutation entry a Purge does (R17).
		// A download is the one Storage action that destroys nothing, so it takes its own seam
		// and lands in the working directory beside a log export (storage-reclamation R13). It
		// dials the blob client, which shares the governor and the limiter but not the store:
		// an Artifact archive is a one-shot binary behind a signed URL, and caching it would
		// spend memory and disk proportional to the download for an entry nothing can ever
		// revalidate (see the clients doc). An expired Artifact is refused before any request,
		// and a 410 arriving anyway reads as the bytes being gone (R14).
		StorageFetch:    storage.ClientFetch(client),
		StorageOps:      d.Ops,
		StorageDownload: c.storageDownload(d.Downloads),
		// The Storage tab's scope is the loaded setting, and this is the wire settings R19 was
		// missing: the loader decoded storage_scope, the Settings view rendered it and toggled
		// it, and nothing read it, so a config file stating this-repo was accepted and then
		// ignored. The two scopes stay separate settings feeding separate tabs, which is what
		// R19 means by independent.
		//
		// this-repo means the repository of the working directory, resolved by the same
		// resolver discovery's fast path takes, with the GH_TOKEN-aware error it already
		// translates. Where there is none the tab falls back to all-repos and says so, so the
		// resolver is wired under either scope.
		StorageScope:       storage.Scope(d.Config.StorageScope),
		StorageCurrentRepo: currentRepoID,
		// The Workflows tab reads each repository's Workflow list over the same client, so the
		// store revalidates and the governor accounts each request (workflow-management R1), and
		// it enables or disables one Workflow through the same ops engine, so a toggle is paced
		// and travels the one write path every other write does (R5).
		WorkflowFetch: workflowlist.ClientFetch(client),
		WorkflowOps:   d.Ops,
		// The tab's scope is the loaded setting, the other half of the same wire, and the two
		// are read separately so scoping one tab leaves the other alone (settings R19).
		WorkflowScope:       workflows.Scope(d.Config.WorkflowsScope),
		WorkflowCurrentRepo: currentRepoID,
		// The dispatch form the Workflows tab opens reads the Workflow YAML at a ref and the
		// repository's environments over the same client (workflow-dispatch R5, R7), triggers the
		// workflow_dispatch through the same ops engine so it is paced and travels ops's write path
		// (R16), and remembers last-used inputs in the same local-store the discovery results live
		// in (R25). One client, one ops, one store, exactly as every other surface.
		DispatchFetch: dispatch.NewClientFetch(client),
		DispatchOps:   d.Ops,
		DispatchStore: d.Store,
		// Both log seams dial blob, and for one reason: each arrives from a signed URL behind a
		// redirect, which is the response shape ADR-0012 routes around the store. The archive is
		// an unbounded one-shot zip; the per-Job fetch is plain text and looks like an ordinary
		// API read, which is exactly why it was wired to shared for three stages and why the
		// deciding line is now a named function on each (see the clients doc). Log deletion
		// reuses purge, the one mutation entry, so a log DELETE is paced and logged like every
		// other (R17).
		LogFetch:  c.logFetch(),
		LogExport: c.logExport(d.Downloads),
		// The approvals decision pane approves a fork-PR Run or reviews a Run's pending deployments
		// through the same ops engine every other write uses, so an approve and a review are paced
		// and travel ops's write path (approvals R11, R12), and it reads the pending deployments
		// over the same client the whole tool shares (R12). An approve and a review are single POSTs
		// beside Execute, so the sole-DELETE and sole-deletion-log invariant is untouched.
		Approver:      d.Ops,
		ApprovalFetch: approval.NewClientFetch(client),
		// The running-operation surface re-attempts a finished pass's recorded failures
		// through the same engine (purge R22). ops is the authority that the retry set is a
		// subset of an already-confirmed frozen set, so the exemption from a fresh
		// confirmation is enforced there rather than trusted here.
		Retrier: d.Ops,
	}
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

// workflowLister is the engine's Workflow-list seam (run-detail R8, ADR-0014). It reads the
// listing over shared, the surface that enters at the store, so the read is revalidated and
// accounted like every other. The reader is workflowlist's, the same one the Workflows tab
// uses, because a second decoder here would be a second place that knows the endpoint and its
// envelope. The two consumers share the reader and not the list: the tab still fans out and
// holds its own copy.
//
// The reader is a neutral package rather than the tab's (issue #95), so this line no longer
// hands a tab's constructor to the engine, and cli can reach the same map for -w NAME and the
// workflowName column without importing tui, which ADR-0011 forbids it.
//
// The engine takes a function over domain types and imports no tab, which keeps ADR-0011's
// direction: main.go is the only place that knows both sides, exactly as it is for the store
// and the client.
//
// The reader records a failed read on the value it returns rather than as a Go error, because
// a 403 on a fine-grained PAT is an outcome the tab's own view states. The engine has no view
// to state it in, so the failure is raised as an error here and the engine stamps nothing.
//
// An incomplete list (the reader's Complete flag, one page at the API's ceiling) is passed
// through as what it is, and the engine memoises it. A Workflow past the first page resolves
// to no State for the session, and its Runs read as not-deleted, which is the answer a join
// that finds nothing gives anywhere else. Refusing the whole list over a missing tail would
// lose the Workflows that are on it, and re-reading it would fetch the same first page
// forever. Paginating it is its own change, split off from the move at triage, and it now has
// one site to change rather than three.
func (c clients) workflowLister() scheduler.WorkflowLister {
	fetch := workflowlist.ClientFetch(c.shared)
	return func(id domain.RepoID) ([]domain.Workflow, error) {
		rw := fetch(id)
		if rw.Err != nil {
			return nil, rw.Err
		}
		return rw.Workflows, nil
	}
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

// downloadDir is where a file the user asked for is written: a whole-Run log archive on
// export (log-viewer R11) and an Artifact's archive on download (storage-reclamation R13).
// It is the current working directory, where a person expects a download to land. It is not
// state and not cache, so it does not go under XDG the way the local-store and the deletion
// log do: those are the tool's own derivations, while these two are the user's files. One
// directory serves both, because "where did my download go" should have one answer. A
// working directory that cannot be resolved falls back to ".", the same directory by name.
func downloadDir() string {
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

// currentRepoID resolves the working directory's repository for the TUI's this-repo scope
// (settings R19), reporting false where there is none. It is the same resolver discovery's
// fast path takes; a scope is a presence question, so the resolver's error is answered as
// "no repository here" and the tab falls back to all-repos and says so, rather than
// interrupting a running TUI with a message about the working directory.
func currentRepoID() (domain.RepoID, bool) {
	id, err := ghclient.CurrentRepo()
	if err != nil {
		return domain.RepoID{}, false
	}
	return id, true
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
