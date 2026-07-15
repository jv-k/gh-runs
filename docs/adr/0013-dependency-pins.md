# The dependency pins, and why go-vcr must be v4

[ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed the tree and [ADR-0012](./0012-transport-chain-and-the-client-surface.md) fixed the wiring through it. Neither says what `go.mod` contains, and `go.mod` is the first file [BUILD-ORDER](../BUILD-ORDER.md) stage 0 asks for. This ADR fixes every line of it.

It exists because `go.mod` records **what** and cannot record **why**. Half the pins below are ordinary and would survive a `go get -u` unharmed. The other half are load-bearing, and a routine upgrade that moved them would leave the build green and the tests dishonest. That asymmetry is invisible from the file itself, which is what an ADR is for.

**Every version below was resolved live against the module proxy, and the whole set was built and vetted together before this was written.** Where gh-dash is cited as the reference implementation ([PRD](../PRD.md), "gh-dash is the proof it carries this exact domain"), it is cited for the choice and not for the number. Its Charm pins are several patches stale, and copying them would have inherited that.

## The pins

| Line | Pin | Tier |
|---|---|---|
| `go` directive | `1.25.0` | Computed, not chosen. See below |
| `toolchain` directive | `go1.26.5` | Current stable |
| `github.com/cli/go-gh/v2` | `v2.13.0` | Ordinary |
| `charm.land/bubbletea/v2` | `v2.0.8` | **Path is load-bearing** |
| `charm.land/lipgloss/v2` | `v2.0.5` | **Path is load-bearing** |
| `charm.land/bubbles/v2` | `v2.1.1` | **Path is load-bearing** |
| `github.com/spf13/cobra` | `v1.10.2` | Ordinary |
| `gopkg.in/dnaeon/go-vcr.v4` | `v4.0.7` | **Major version is load-bearing** |
| `github.com/jonboulle/clockwork` | `v0.5.0` | Ordinary, replaces an archived library |
| `github.com/sebdah/goldie/v2` | `v2.8.0` | Ordinary |

## The Go floor is computed from the dependencies, not picked

**`go 1.25.0` is not a taste.** go-gh v2.13.0, bubbletea v2.0.8, lipgloss v2.0.5 and bubbles v2.1.1 each declare `go 1.25.0` in their own `go.mod`. That is the floor, and nothing below it can resolve. The question "does a lower floor include more contributors" has no answer here, because there is no lower floor available.

The floor is also not a promise about what builds our binaries. Those are two different lines, and conflating them is the trap:

- **`go 1.25.0` is the promise.** It is the minimum a contributor needs. A contributor with `GOTOOLCHAIN=local` and Go 1.25 installed builds this repo, and that is what the line commits us to.
- **`toolchain go1.26.5` is what actually builds.** It is the current stable release. Go supports only the two most recent majors, which today are 1.26.5 and 1.25.12, so the floor sits inside a supported major rather than below one.

**[go-ci.yml](../../.github/workflows/go-ci.yml) uses `go-version-file: go.mod`, and that makes this file the single source for CI and for every released binary.** The `toolchain` line is what makes that safe. `actions/setup-go` reads the `toolchain` directive **in preference to** the `go` directive, and falls back to `go` only when the toolchain line is absent or `GOTOOLCHAIN` is `local` (verified in `setup-go`'s `parseGoVersionFile`). It then resolves the string as a semver range, so an exact `1.25.0` means **exactly Go 1.25.0**, not the newest 1.25.x.

That last detail is the whole argument. **Without a `toolchain` line, `go 1.25.0` would build our releases on an unpatched Go 1.25.0 and never move.** A floor is the right thing to state loosely and the wrong thing to build with. Two lines say both.

`cli/cli` reaches the same shape independently, with `go 1.26.0` and `toolchain go1.26.5`. gh-dash does not, and carries a bare `go 1.25.8`, which pins its CI to one patch by accident rather than by decision.

**Bumping `toolchain` is a deliberate act, and it belongs in a commit of its own.** It moves the compiler under every shipped binary.

## The Charm import paths moved, and one failure is silent

**Bubble Tea v2 lives at `charm.land/bubbletea/v2`.** Its own `go.mod` reads `module charm.land/bubbletea/v2`. The vanity domain is the canonical path, and lipgloss and bubbles moved with it. This ADR records the paths because [ADR-0011](./0011-package-layout-and-dependency-direction.md) and the [PRD](../PRD.md) both name Bubble Tea and Lipgloss without naming where they now live, and recall reaches for the old path.

There are two wrong paths, and they fail differently:

| Written | Result |
|---|---|
| `github.com/charmbracelet/bubbletea/v2` | **Fails loudly.** The proxy lists versions to v2.0.8 and `go list -m @latest` resolves, so the path looks alive. A build then stops with `module declares its path as: charm.land/bubbletea/v2 but was required as: github.com/charmbracelet/bubbletea/v2`. Measured. |
| `github.com/charmbracelet/bubbletea` | **Fails silently.** It resolves to **v1.3.10**, a live and maintained v1. It compiles. It is a different API. |

The suffixed GitHub path is safe precisely because it breaks. The unsuffixed one is the hazard: it is a real module that builds, and nothing about a green compile says the TUI is written against the wrong major. **The `/v2` suffix is not the signal. The domain is.**

`charm.land/bubbles/v2 v2.1.1` is pinned because the [PRD](../PRD.md)'s stack argument depends on it by name: gocui and raw tcell lost on "hand-rolling lists, viewports and key handling that bubbles supplies". A dependency that carries a stack decision belongs in the file that records the stack decision.

## go-gh, cobra and the clock

**go-gh v2.13.0** is the current release and the client [ADR-0002](./0002-go-gh-with-dual-distribution.md) already chose. [ADR-0012](./0012-transport-chain-and-the-client-surface.md) measured go-gh's transport stack at this exact version, and the [PRD](../PRD.md)'s cache constraint was measured at v2.9.0. Nothing in either reading moved between the two, which ADR-0012 already states and this pin keeps true.

**cobra v1.10.2** is what `gh` itself pins, alongside `pflag v1.0.10`. That is the reason, and it is a stronger one than framework preference. [ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md) commits to a **drop-in superset of `gh run`'s flags**, with "identical names and semantics", and calls compatibility "a stated requirement, not an accident". Flag parsing is where that requirement is either kept or quietly broken, so we parse flags with the library that defines the behaviour we are copying. kong and urfave/cli are both good and both lose here for one reason: they would make gh-compatibility something we reimplement and test, rather than something we inherit. **The point is not that cobra is better. The point is that it is the same.**

**clockwork v0.5.0** carries [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s injected clock into five packages. The obvious alternative, `benbjohnson/clock`, is **archived**, confirmed against the GitHub API: `archived: true`, last pushed 2023-05-18. It is widely recommended and no longer maintained. clockwork is active, last pushed 2025-11-21.

## go-vcr must be v4, and this is the entry that matters

**Cassettes are not a testing preference here. They are how [local-store](../features/local-store/requirements.md) R18 proves R8 rather than assuming it**, and R18 says why in its own words: "a hand-written fake would return whatever we believed a conditional request does, and would keep returning it long after the API changed". A cassette library whose matcher ignores the request is a hand-written fake with extra steps.

**go-vcr v3's `DefaultMatcher` ignores headers entirely.** Verified against the source at v3.2.0, in full:

```go
func DefaultMatcher(r *http.Request, i Request) bool {
	return r.Method == i.Method && r.URL.String() == i.URL
}
```

Method and URL. Nothing else. **A conditional request carrying `If-None-Match` therefore matches a taped unconditional 200**, which is measured and not inferred: reproducing that matcher verbatim and running a conditional request against an unconditional recording returns `true`.

The consequence is the reason this ADR exists. [local-store](../features/local-store/requirements.md) **AC5 ("Revalidation is real, not TTL")** asserts that a request is still issued and that it carries `If-None-Match`. Under a v3 matcher the header is never compared, so a store that sent no `If-None-Match` at all would replay the taped 200 and **AC5 would pass vacuously**. The suite would be green, the store would look correct, and it would never revalidate. That is R18's stated failure mode arriving through the library that was supposed to prevent it.

**v4's default matcher compares the request.** Verified at v4.0.7: method, URL, `Proto`, `ProtoMajor`, `ProtoMinor`, the **full header map**, the body, `ContentLength`, `TransferEncoding`, `Host`, `Form`, `Trailer`, `RemoteAddr` and `RequestURI`. AC5 cannot pass without the header, because the header is now part of the match.

Honesty costs three headers. go-gh injects five, and three of them are unstable:

| Header | go-gh sets it to | Why it breaks a cassette |
|---|---|---|
| `Time-Zone` | the machine's local zone, via `tzlocal` | A cassette recorded in one timezone fails in another, and fails in CI. **Measured**: v4's matcher returns `false` for `Europe/London` against `America/New_York`. |
| `User-Agent` | `go-gh <version>`, read from `debug.ReadBuildInfo()` | Version-bound at runtime. Bumping go-gh changes it **without touching a line of our code**, and every cassette breaks at once. |
| `Authorization` | `token <tok>` | A cassette must never carry a real token, so the recorded value is redacted. The live value is not. They cannot match. |

`Accept` and `Content-Type` are the other two and are stable, so they stay matched.

The matcher is therefore constructed once, and never by default:

```go
cassette.NewDefaultMatcher(
	cassette.WithIgnoreHeaders("Time-Zone", "User-Agent", "Authorization"),
)
```

**`WithIgnoreHeaders` is case-sensitive, which is measured and is not documented.** It performs a raw `delete()` on the header map rather than calling `Header.Del()`, so it does no MIME canonicalisation. `WithIgnoreHeaders("time-zone")` **silently ignores nothing** and every cassette fails to replay. Pass canonical form, or use the provided `WithIgnoreUserAgent()` and `WithIgnoreAuthorization()` options, which spell their own headers correctly.

**The redaction hook and the ignore list are one decision in two places.** [local-store](../features/local-store/requirements.md) R20 requires that no recoverable form of the token reaches disk, and AC14a greps the state directory for it. A `BeforeSaveHook` redacts `Authorization` on the way to the cassette. If the matcher does not also ignore `Authorization`, the redacted tape can never match a live request and **nothing replays**. Neither half works alone, and neither half's absence is visible from the other.

**Cassette-backed store tests need a fixed dummy token.** R20 hashes the `Authorization` header into the store's key. That is our key, not go-vcr's matcher, and the two are independent. But a token that varies between recording and replay shifts the store's own keys underneath the test, so the token is a constant in `testdata` and not the developer's real one. The matcher ignoring `Authorization` is what makes that constant harmless.

## Golden files: goldie, not teatest

This is a real fork, and the brief that prompted this ADR guessed it would be settled by teatest targeting Bubble Tea v1. **It does not, and that guess was wrong.** `github.com/charmbracelet/x/exp/teatest/v2` exists and requires `charm.land/bubbletea/v2`. The fork is settled on other grounds.

**A Bubble Tea `View()` is a pure function of model state.** Golden-testing it needs a model and a string. goldie asserts bytes against `testdata` and supplies the `-update` flag, which is the whole requirement [ADR-0011](./0011-package-layout-and-dependency-direction.md) records for the `tui/*` seam.

teatest does something else and something more. `NewTestModel` runs a real `tea.Program`, and `WaitFor` polls the output with a **default duration of 1s and a check interval of 50ms**, with `WithFinalTimeout` on top. That is real wall-clock waiting, reintroduced into the one suite whose [injected clock](./0011-package-layout-and-dependency-direction.md) exists to remove it. We would be driving an event loop to observe a function of state.

Two further facts settle it. teatest has **no tagged release**, and resolves only to a pseudo-version (`v2.0.0-20260713092006-0d683c34c74b`). It sits in `x/exp`, which is where Charm says it is experimental, and it pins `charm.land/bubbletea/v2 v2.0.0-rc.1`, a release candidate, against our v2.0.8.

**gh-dash is not precedent here either way.** It uses neither. It hand-rolls `.golden.yml` comparison for config and does not golden-test its TUI at all.

goldie v2.8.0 is tagged, maintained (last pushed 2025-11-22) and does exactly one thing.

## Considered Options

**Copy gh-dash's pins verbatim.** It is the reference implementation, and this was the tempting move. It is stale: bubbletea v2.0.2 against v2.0.8, lipgloss v2.0.1 against v2.0.5, bubbles v2.0.0 against v2.1.1. Its `go 1.25.8` with no `toolchain` line would also hand our releases to whatever patch that string happens to resolve to. gh-dash proves the **stack**, which is what the PRD claims for it. It does not vouch for the versions.

**go-vcr v3.** The v3 module path (`gopkg.in/dnaeon/go-vcr.v3`) still resolves and v3.2.0 is still there. It is easier: no header ignore list, no redaction agreement, no timezone hazard. It buys that ease by not checking the request, and AC5 is the requirement that pays. **Rejected on measurement, not on version number.**

**go-vcr v4 with a hand-written matcher on method and URL.** Identical to v3's behaviour, reached deliberately instead of by default. Recorded here so a future reader knows the shape is available and that choosing it discards AC5.

**benbjohnson/clock.** Archived since 2023, verified. The most-recommended option and the wrong one.

**teatest for golden files.** Covered above. Untagged, experimental, pinned to an rc, and it trades a pure function for an event loop with real timeouts.

**kong or urfave/cli.** Both are cleaner than cobra in isolation. Both lose to ADR-0008's compatibility requirement, which is satisfied by using gh's own library rather than by matching its output.

**No `toolchain` line.** Simpler, and one line shorter. It builds every release on the exact patch named in the `go` directive, forever, because that is how `setup-go` resolves an exact semver.

## Consequences

**`go get -u` is not a safe routine here, and this ADR is the reason to check.** Three specific upgrades break things quietly:

- **Anything that moves cassette matching back to a v3-style method-and-URL comparison** makes local-store AC5 pass vacuously. The suite stays green. The store stops revalidating and nothing says so.
- **Any edit that returns a Charm import to `github.com/charmbracelet/bubbletea`** (unsuffixed) resolves to a live v1.3.10 and compiles against the wrong major.
- **A go-gh bump changes the `User-Agent` header** that go-gh derives from build info. Without `WithIgnoreHeaders("User-Agent")` every cassette fails to replay at once, and the diff that caused it touches one version string.

**A dependency bump that touches go-gh, go-vcr or a Charm path is a decision, not a chore.** The rest are ordinary and need no ceremony.

**Cassettes are recorded once and are portable only because three headers are ignored.** Add a header to a request and it joins the match. That is the design working, and the failure will look like an unrelated cassette miss.

**The `go` and `toolchain` lines mean different things and drift apart on purpose.** The floor moves when a dependency forces it. The toolchain moves when we decide to ship on a new compiler. Neither implies the other, and a bump that edits both at once is probably two commits.

**This ADR pins versions and will go stale.** That is acceptable and expected. The pins are a record of what was current and verified when stage 0 began, and the reasoning is what has to survive. A version here being out of date is not a bug in this document. A version here being upgraded **without reading the tier column** is.
