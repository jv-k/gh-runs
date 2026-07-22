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
| `github.com/charmbracelet/colorprofile` | `v0.4.3` | Bubble Tea's own, promoted to direct. See below |
| `github.com/spf13/cobra` | `v1.10.2` | Ordinary |
| `gopkg.in/dnaeon/go-vcr.v4` | `v4.0.7` | **Major version is load-bearing** |
| `github.com/jonboulle/clockwork` | `v0.5.0` | Ordinary, replaces an archived library |
| `github.com/sebdah/goldie/v2` | `v2.8.0` | Ordinary |
| `golang.org/x/sys` | `v0.45.0` | Ordinary, promoted to direct for the lock, MVS-forced to `v0.45.0` by bubbles v2.1.1. See below |

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

**Pinning bubbles v2.1.1 raises two indirect lines through MVS.** Its own `go.mod` requires `golang.org/x/sys v0.45.0` and `github.com/lucasb-eyer/go-colorful v1.4.0`. Both sat lower before bubbles arrived, `x/sys` at `v0.31.0` (from go-gh and `x/term`) and go-colorful at `v1.2.0` (from go-gh and termenv), and MVS takes the maximum, so both rose the moment bubbles landed. Neither is a `go get -u` drift, both are tidy-clean and verified, and both stay Ordinary tier. `x/sys` is the one the pin table now records at `v0.45.0`, because it is also promoted to direct for the Windows lock below. go-colorful stays indirect and needs no line of its own.

**bubbles also sets a version trap for two direct requires that do not exist yet.** Its `go.mod` requires `charm.land/bubbletea/v2 v2.0.7` and `charm.land/lipgloss/v2 v2.0.4`, both below the `v2.0.8` and `v2.0.5` this ADR pins. Today they are phantom-graph only, present in `go mod graph`, absent from `go.sum`, never compiled, because nothing yet imports a package that reaches them. **A later TUI stage that adds `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2` as direct requires must pin those exact versions by hand.** A bare `go get charm.land/bubbletea/v2` resolves it to `v2.0.7` through bubbles' requirement, and lipgloss to `v2.0.4`, and the build stays green at the lower pair. MVS never reaches `v2.0.8` and `v2.0.5` on its own, so nothing but an explicit require will hold them. The path is load-bearing above for one reason and the version is load-bearing here for another.

## colorprofile is already here, and we promote it rather than adopt it

**`github.com/charmbracelet/colorprofile v0.4.3` is what `charm.land/bubbletea/v2 v2.0.8` resolves today**, verified against the module graph, where it sits `// indirect`. It becomes a direct require for one reason: [settings](../features/settings/requirements.md) R15a resolves `NO_COLOR` **itself** and hands the answer to `tea.WithColorProfile`, rather than letting this library detect it. The version does not move. The line moves from indirect to direct, which is a `go.mod` line and therefore this ADR's business.

**The reason we override its detection is measured, and it is an accessibility defect rather than a preference.** At v0.4.3, `Detect` resolves `NO_COLOR` through `strconv.ParseBool` and gates the result on `isatty`, while leaving `CLICOLOR_FORCE` ungated. On a real PTY:

| Environment | `Detect` returns | Against R15 |
|---|---|---|
| `NO_COLOR=1` | `Ascii` | Suppresses colour, and **keeps bold** |
| `NO_COLOR=` | `TrueColor` | R15 and AC9 name the empty string explicitly |
| `NO_COLOR=yes` | `TrueColor` | R15 says any value |
| `NO_COLOR=0` | `TrueColor` | gh would suppress |
| `NO_COLOR=1` piped, `CLICOLOR_FORCE=1` | `TrueColor`, full escapes | R15 forbids the override |
| `CLICOLOR=0` | `TrueColor` | gh would suppress. `cliColor` only ever raises |

Three specifications disagree here and the library follows none of them. no-color.org says present and **not** an empty string. gh 2.92.0 says **any** value. `strconv.ParseBool` accepts eleven spellings and rejects `yes`. The canon followed gh deliberately ([settings](../features/settings/requirements.md) R15), so the canon resolves it, in one function, and the library is left to do the part it is good at: **`colorprofile.Writer` is still the only thing that degrades a styled string, and the profile handed to it is ours to choose.** Its own documentation states the split we are relying on, that `NO_COLOR` "will disable colors but not text decoration".

## The advisory lock adds no module, and promotes one line

[local-store](../features/local-store/requirements.md) R21's advisory file lock needs `flock(2)` semantics on unix and `LockFileEx` on Windows, and R22 leaves the spelling to this ADR to record when stage 1 lands. It is recorded here, and the headline is that the lock adds no new module.

On unix the lock reaches `flock(2)` through the standard library alone, `syscall.Flock(fd, LOCK_EX|LOCK_NB)`, so that limb pins nothing. A separate open of the same file is denied even within one process, which is what lets one holder exclude another and makes [local-store](../features/local-store/requirements.md) AC18 provable in a single test process.

On Windows there is no `flock`, and `LockFileEx` is not in the standard `syscall` package. It lives in `golang.org/x/sys/windows`, already in the module graph. The Windows limb imports it directly, so the line moves from indirect to direct, exactly as colorprofile's did above: a `go.mod` line changes and no new module arrives. The version is a separate story, and unlike colorprofile's it did move. The promotion alone does not move it, and when this limb was first specified `x/sys` sat at `v0.31.0`. Adding `charm.land/bubbles/v2 v2.1.1` later raised it to `v0.45.0` through MVS, as the pin table and the bubbles section above both record, so `go.sum` now carries the `v0.45.0` hashes and not the old ones. No new module arrives even so: one line moved from indirect to direct and one version floated up, and the pin set is otherwise the set it already was. 2.0.0 ships Windows ([PRD](../PRD.md), Scope), so this limb is required rather than optional.

A wrapper library was the alternative, `gofrs/flock` being the usual one. It would add a module and its transitive graph to spare two short platform files, and it buys nothing the standard library and `golang.org/x/sys` do not already give, both of which are pinned here regardless.

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

**A Bubble Tea `View()` is a pure function of model state.** That is the whole of the argument, and it survives v2 intact. What does not survive is the sentence that used to follow it, which read "golden-testing it needs a model and **a string**" and named an API that no longer exists. **Verified at v2.0.8: `View()` returns `tea.View`, a struct.** `Init() Cmd` and `Update(Msg) (Model, Cmd)` are unchanged, `tea.KeyMsg` became an interface and a press arrives as `tea.KeyPressMsg`, and the string is now `tea.View.Content`. The fork below is settled on purity, and purity is a property of the function rather than of its return type, so the decision stands and its load-bearing sentence was describing v1.

**Golden `[]byte(m.View().Content)`.** goldie asserts bytes against `testdata` (`Assert(t testing.TB, name string, actualData []byte)`, verified at v2.8.0) and supplies the `-update` flag, which is the whole requirement [ADR-0011](./0011-package-layout-and-dependency-direction.md) records for the `tui/*` seam.

teatest does something else and something more. `NewTestModel` runs a real `tea.Program`, and `WaitFor` polls the output with a **default duration of 1s and a check interval of 50ms**, with `WithFinalTimeout` on top. That is real wall-clock waiting, reintroduced into the one suite whose [injected clock](./0011-package-layout-and-dependency-direction.md) exists to remove it. We would be driving an event loop to observe a function of state.

Two further facts settle it. teatest has **no tagged release**, and resolves only to a pseudo-version (`v2.0.0-20260713092006-0d683c34c74b`). It sits in `x/exp`, which is where Charm says it is experimental, and it pins `charm.land/bubbletea/v2 v2.0.0-rc.1`, a release candidate, against our v2.0.8.

**gh-dash is not precedent here either way.** It uses neither. It hand-rolls `.golden.yml` comparison for config and does not golden-test its TUI at all.

goldie v2.8.0 is tagged, maintained (last pushed 2025-11-22) and does exactly one thing.

### The pipeline, stated once

**Six requirements mandate goldens and not one says what is goldened.** [live-run-feed](../features/live-run-feed/requirements.md) R36, [run-detail](../features/run-detail/requirements.md) R19, [log-viewer](../features/log-viewer/requirements.md) R20, [storage-reclamation](../features/storage-reclamation/requirements.md) R25, [workflow-dispatch](../features/workflow-dispatch/requirements.md) R21 and [settings](../features/settings/requirements.md) R18 each require a frame verified byte for byte, and none names a subject, a width or a colour profile. Six implementers would pick six. It is stated here because the trap below is a consequence of *this* ADR's own fork, not of any feature's requirement.

**One golden is `[]byte(m.View().Content)`, rendered at a fixed width fed by a fabricated `tea.WindowSizeMsg`, with the colour profile named by the test and never detected.**

**That is simpler than it looks, and the reason is measured: lipgloss v2 does not consult the environment at render time.** `Style.Render` returns byte-identical output under every combination of `TERM`, `COLORTERM`, `NO_COLOR` and `CLICOLOR_FORCE`, on a TTY and piped: 34 bytes of truecolor in every case, verified at v2.0.5. Degradation happens **downstream**, in `colorprofile.Writer` or in Bubble Tea's renderer, and never in the style. **So a golden over `View().Content` is byte-stable across a truecolor dev box and a dumb CI terminal by construction.** An implementer who does not know this will spend a day defending against a problem that does not exist, which is the only reason this paragraph is longer than one line.

**The width is not free, and the trap is ours.** This ADR rejected teatest for running a real `tea.Program`, which is defensible and unchanged. But `tea.WithColorProfile` and `tea.WithWindowSize` are both `ProgramOption`s, so **choosing goldie means neither lever exists** and the test pins both by hand: construct the model, send a `tea.WindowSizeMsg{Width, Height}` through `Update` as the runtime's first message would, then read `View()`. Nothing about that is hard. Everything about it is invisible from goldie's API, which is why it is written down.

**The width is 100 columns**, which is [live-run-feed](../features/live-run-feed/requirements.md) R4a's minimum and the narrowest terminal the Feed will paint a row into. 80 cannot hold R4's mandated row and R4a carries the arithmetic. A golden at a width the product refuses to run at would fix a frame nobody can see.

**[settings](../features/settings/requirements.md) AC9 and AC10 cannot be goldens over `View().Content`, and that follows from the measurement above.** The content is truecolor whatever the environment says, so a NO_COLOR golden and a colour golden are the same bytes, and an AC9 asserted that way passes without testing anything. Those two assert over a **downstream** stage instead: run the content through an explicit `colorprofile.Writer` at the profile [settings](../features/settings/requirements.md) R15a's resolver returns, and assert on the writer's output. That is the only place in the suite where a profile appears at all, and AC9 is the only reason it does.

## No notification library is pinned, and that is a decision

**[notifications](../features/notifications/requirements.md) R11 mandates three OS backends, and this file has no line for them.** That absence was silence rather than a decision, which is what this section corrects. The recommendation was that **2.0.0 ships no notification backend**, and the product owner has since ruled for it: the feature defers to 2.1. This ADR recommended the cut rather than making it, because the feature was already contested and overridden once, and cutting it was not this ADR's to do quietly. The ruling of 2026-07-16 is what settled it.

**A pure-Go cross-compiled option does exist, and it was measured rather than assumed.** `github.com/gen2brain/beeep v0.11.2` builds under `CGO_ENABLED=0` for `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64` and `windows/amd64`, verified. So the cost is not the one this feature was cut for the first time. It is not a subsystem we write. It is one require line and roughly ten transitive modules.

**The blocker is elsewhere, and no pin fixes it.** [ADR-0002](./0002-go-gh-with-dual-distribution.md) ships precompiled cross-platform binaries through `cli/gh-extension-precompile`, so cgo is off and there is no application bundle. `UNUserNotificationCenter` needs both. That leaves a subprocess, and beeep's macOS path is exactly that: `terminal-notifier` if `exec.LookPath` finds it, else `osascript -e 'display notification …'`. Three measured consequences:

| Measured | Consequence |
|---|---|
| `osascript` exits **0** whether or not the toast rendered | **R13 is unsatisfiable on macOS.** "Report the channel's unavailability in Settings" needs a signal, and there is none. Settings could only ever claim availability it cannot know |
| A toast from `osascript` is attributed to the AppleScript host, not to gh-runs, and inherits **that app's** notification permission | R14's content still lands. The badge names somebody else. The doc's open question, whether first run should ask before the OS's own prompt, is answered: the prompt is not ours to spend, and we cannot ask for it |
| `terminal-notifier` was **present on the reference machine** at `/opt/homebrew/bin/terminal-notifier` | Delivery, attribution and reliability differ between two users on the same OS according to an unrelated Homebrew package. That is not a behaviour a shipped binary should have |

**Linux closes the loop with [ADR-0002](./0002-go-gh-with-dual-distribution.md), and the two documents never met.** beeep sends over D-Bus through `esiqveland/notify`, falling back to `notify-send`. ADR-0002 rejected go-keyring partly because "on headless Linux it needs a D-Bus Secret Service that is often absent". Secret Service and Notifications are different D-Bus services, and they need the same session bus, which is the thing that is absent. So the case ADR-0002 already reasoned about is precisely [notifications](../features/notifications/requirements.md) R12's degrade-silently path, reached by the same mechanism. R12 holds on Linux. **On macOS it is R12 that is the problem**, because a toast that was never displayed and a toast that was delivered are the same exit code, so the feature degrades silently in the case where it was supposed to work.

**The position: cut notifications to 2.1, and pin nothing. The product owner ruled for the cut on 2026-07-16, so this is decided rather than recommended.** The argument that lost the first time was cost, and measurement has retired it. The argument now is correctness, and it is stronger: on the reference platform the feature would attribute its toasts to another application, report an availability it cannot observe (R13), and fail silently in exactly the way R12 reserves for a channel that is genuinely absent. This canon spends R24, R29 and R30 on not saying things it cannot know. A Settings row reading "Notifications: available" would be the one place it does.

**When 2.1 revives the feature, `beeep v0.11.2` is the pin**, and these caveats are what it ships with. It is the honest option and it is not a bad one. R13 would then need rewording to what a subprocess can support, and that reword is a requirement change rather than a dependency choice, which is why this ADR does not make it.

## Considered Options

**Copy gh-dash's pins verbatim.** It is the reference implementation, and this was the tempting move. It is stale: bubbletea v2.0.2 against v2.0.8, lipgloss v2.0.1 against v2.0.5, bubbles v2.0.0 against v2.1.1. Its `go 1.25.8` with no `toolchain` line would also hand our releases to whatever patch that string happens to resolve to. gh-dash proves the **stack**, which is what the PRD claims for it. It does not vouch for the versions.

**go-vcr v3.** The v3 module path (`gopkg.in/dnaeon/go-vcr.v3`) still resolves and v3.2.0 is still there. It is easier: no header ignore list, no redaction agreement, no timezone hazard. It buys that ease by not checking the request, and AC5 is the requirement that pays. **Rejected on measurement, not on version number.**

**go-vcr v4 with a hand-written matcher on method and URL.** Identical to v3's behaviour, reached deliberately instead of by default. Recorded here so a future reader knows the shape is available and that choosing it discards AC5.

**benbjohnson/clock.** Archived since 2023, verified. The most-recommended option and the wrong one.

**teatest for golden files.** Covered above. Untagged, experimental, pinned to an rc, and it trades a pure function for an event loop with real timeouts.

**kong or urfave/cli.** Both are cleaner than cobra in isolation. Both lose to ADR-0008's compatibility requirement, which is satisfied by using gh's own library rather than by matching its output.

**`gen2brain/beeep v0.11.2` for notifications.** The best option available, and the feature it serves is deferred to 2.1, above. It is genuinely pure Go and genuinely cross-compiles, which is more than was assumed. What it cannot do is give a bundle-less binary an identity on macOS or a delivery receipt on any platform, and those are R13's and R12's requirements rather than beeep's shortcomings. Recorded here because it is the pin to reach for when the feature returns, and because the next reader will otherwise re-run this measurement.

**Writing the three backends ourselves.** Strictly worse than beeep on every axis. The macOS path would still be `osascript`, so it inherits the same two defects and adds the Linux D-Bus client and the Windows COM client to our maintenance. There is no version of this feature where the platform surface is ours to improve.

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
