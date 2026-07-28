# The version comes from the build, backed by a constant VerBump bumps, and the tag is cut locally

`gh-runs` reports a version through `--version`, resolved from the toolchain's build metadata first and from a constant in `internal/version` second. A release is cut by running VerBump locally (`make bump`), which writes the changelog, bumps `version.json` and that constant, commits and tags. Pushing the tag is what publishes: `.github/workflows/release.yml` fires on it and `cli/gh-extension-precompile` builds and uploads the binaries. Nothing runs VerBump in CI, and `make release` is deliberately disabled.

Until this decision the tool had no version at all: no symbol in the tree, no flag, and nothing stamped into a shipped binary. For a tool that irreversibly deletes Runs, Caches and Artifacts at a scale of tens of thousands, "which build did that" is a question a bug report has to be able to answer.

## Where the version comes from at runtime

**Measured**, in a sandbox repository on the toolchain `go.mod` pins:

| Build | `debug.ReadBuildInfo().Main.Version` |
|---|---|
| HEAD at tag `v2.9.9` | `v2.9.9` |
| one commit past that tag | `v2.9.10-0.20260728014940-fb9ed062706f` |
| `go install .../v2@v2.0.0` | the module version, `v2.0.0` |

So a binary built at a tag already knows its tag, and the release workflow builds from a checkout of exactly that tag. Build metadata is therefore the first source, because unlike a constant it cannot go stale: a build one commit past a release reports a pseudo-version naming that commit, where a constant would still claim the release.

The constant is the fallback for a build carrying no VCS stamp: `-buildvcs=false`, an export with no repository, a vendored tree. It is kept in step with the release by VerBump rather than by hand, so the fallback names a real version.

**`-ldflags -X` was the obvious option and is not available.** `cli/gh-extension-precompile` builds every shipped binary with a fixed `-ldflags="-s -w"` (`build_and_release.sh`), and its `GO_BUILD_OPTIONS` input lands in the package position, not the flag position. There is no input that injects a version. An ldflags-only scheme would therefore be empty in exactly the builds most users install, while working perfectly on the maintainer's machine, which is the worst available failure shape.

## Where the version lives at rest

`version.json`, a two-line JSON file at the root holding `.version`, is VerBump's `SOURCE_FILE`. The Go constant is its one `BUMP_FILES` target.

**`package.json` was considered and rejected.** VerBump defaults to it, and it would work. But [ADR-0002](./0002-go-gh-with-dual-distribution.md) rejected the npm channel, `delete-workflow-runs@1.0.7` is frozen and unmaintained, and a `package.json` in this tree would advertise a channel that does not exist. `version.json` is the same mechanism without the claim.

**Letting VerBump fall back to the latest git tag was considered and rejected, and this is the sharp edge.** With no source file VerBump reads the current version from the newest matching tag, which here is `v1.0.7`: a bash script, on a line the Go rewrite does not continue. A text bump searches for the pattern carrying the **previous** version and rewrites every line holding it (`lib/textbump.sh`), so a tag-derived `1.0.7` would search `internal/version/version.go` for `Version = "1.0.7"`, find nothing, and report a non-fatal error. The release would ship with its constant unbumped, and the failure would be a log line in the middle of a successful-looking release. `version.json` makes the previous version explicit instead of inferring it from a lineage that broke at v1.

Two properties follow from that mechanism, and both are asserted rather than trusted, in `internal/version/version_test.go`:

- The constant and `version.json` must agree at rest, or the next bump searches for a string that is not there.
- Exactly one line may carry the search string. The search is a literal substring, so a second identifier ending in `Version` (`devVersion`, `defaultVersion`, anything camel-cased) would match and be rewritten alongside it.

Neither failure is visible until a release is already cut, which is why they are tests rather than comments. The tests read `.verbumprc` for the pattern rather than restating it, so a config change cannot leave them passing against a rule that no longer applies.

## Why the release is cut locally

**A GitHub Actions workflow running VerBump was considered and rejected.** A tag pushed by a workflow authenticated with the default `GITHUB_TOKEN` does not trigger other workflows. GitHub blocks that recursion by design. So an Actions-based bump would compute the version, write the changelog, tag and push perfectly, and `release.yml` would never fire. The result is a tag with no release and no binaries: a failure that reports success everywhere it is visible, and is discovered by a user whose `gh extension install` finds nothing.

Making it work needs a PAT or a GitHub App token held as a repository secret. That is standing credential surface, with permission to push to `main` and to tag, for something run a handful of times a year, by one maintainer, from a tool built to run in a terminal. The trade is not worth it at this project's size.

**`verbump --release` was considered and rejected for the same class of reason.** Publishing the GitHub release is already owned by `release.yml` through `cli/gh-extension-precompile`, which creates the release and names its assets with the exact `{GOOS}-{GOARCH}` suffixes `gh extension install` matches on ([ADR-0002](./0002-go-gh-with-dual-distribution.md), risk R3). Two tools publishing one release for one tag does not fail cleanly: it produces a release that exists with the wrong assets, which looks complete and installs nowhere. That is the precise failure ADR-0002 rejected goreleaser to avoid, and it is not worth reintroducing from the other end.

So the split is: **VerBump owns the version, the changelog and the tag. The workflow owns the release and its binaries.** `make bump-release` pushes the tag that hands over between them, and `make release` exists only to say so and exit non-zero.

## Why a Makefile, and why the release is a script under it

Go has no `package.json` scripts. `make` is what a Go CLI reaches for, it is installed wherever this builds, and it keeps the release flags in one reviewed file rather than in a shell history. The targets are `bump`, `bump-release` and the disabled `release`, plus the individual checks. Taskfile was the alternative and adds an install step for the same three commands.

**Two things are scripts rather than targets, and the reason is that `make` is not dependable on the machine that cuts a release.** On macOS `make` is a shim through `xcrun`, and a mismatched Xcode or Command Line Tools install takes it down: measured on the maintainer's machine, `make help` fails with `unable to load libxcrun`, and the same broken toolchain also stops cgo, and with it the race detector and any `go install` that needs a C compiler.

`scripts/check.sh` is the gate: the build, `go vet`, the tests, `golangci-lint`, and the pinned deslopper. `PRE_BUMP_CMD` runs it and `make check` delegates to it, so a release and a pull request are held to one standard defined in one place, and neither depends on `make` being healthy. A gate that stops working because the Command Line Tools moved is a gate that gets skipped with `--no-hooks`, which is the same as not having one.

**It is the last gate, not a preview of one.** `go-ci.yml` triggers on pull requests and on pushes to `main`. A tag push triggers `release.yml` alone, which builds and uploads binaries and runs no tests. So nothing re-checks the tree between this hook and the published release, and the script says so where it matters: when it cannot build cgo it drops `-race` and reports that the only race coverage the release carries is whatever ran when those commits merged to `main`. It probes for a working compiler rather than trusting `CGO_ENABLED`, because that variable reports the setting and not whether the compiler behind it runs.

`scripts/release.sh` is the preflight VerBump has no opinion about: on `main`, no uncommitted changes to tracked files, in step with `origin/main` in both directions, and the tag not already taken locally or on the remote. The last one matters here because this clone is shared between worktrees and sessions. It then states what the push sets in motion, including whether the version's hyphen makes it a prerelease, and hands over to VerBump.

The version is its first argument and everything after it passes through untouched, so a flag taking a separate value (`--preid alpha`, `-m "message"`) survives. Parsing those here would mean keeping a list of which flags take a value in step with the tool that owns that list. Two exceptions earn their place: a second `-v` is refused, because VerBump's flags are last-wins and a version the preflight never checked for a collision would ship, and `--no-hooks` and `--allow-dirty` are warned about, because each discards something the step before it just established.

## Consequences

`--version` is answered in `main.go` before the transport chain is built, rather than by cobra alone. Reporting a version needs no token, no network and no local store, and client construction fails outright when no token resolves, so leaving it to cobra would make the first command a bug report asks for fail on the machine most likely to be filing one. `TestAsksVersionAgreesWithCobra` pins the short-circuit to the spellings cobra actually registers, so the two cannot drift apart.

**`--help` has the same defect and this does not fix it.** Measured with no token and no `gh` on `PATH`, `gh-runs --help`, `gh-runs list --help` and bare `gh-runs` all exit 1 with "authentication token not found for host github.com". The root cause is that `main.go` resolves clients eagerly, before cobra sees the arguments, and the fix is either lazy client construction or an earlier help path. Both are larger than this decision and are tracked separately.

`CHANGELOG.md` stops being hand-written. Its existing entries are auto-generated commit dumps from the v1 npm era. From 2.0.0 VerBump writes it grouped by Conventional Commit type. The file is already excluded from deslopper in `deslopper.config.json`, so generated prose does not have to pass a style gate written for prose a person wrote.
