# Build on go-gh, ship as both a gh extension and a standalone binary

Every v1 user already has `gh` installed, because v1 required it. We build on `github.com/cli/go-gh` and ship `gh-runs` two ways: as a gh extension (`gh extension install jv-k/gh-runs`) and as a standalone binary via Homebrew or `go install`. This is the gh-dash pattern. go-gh gives us token discovery, host resolution and a REST client for free, which is the entire authentication story we would otherwise own.

## Considered Options

**google/go-github, standalone.** Drops the gh association, but we would implement token discovery ourselves: environment variables, then config, then keyring, then device-flow OAuth. That is a lot of security-sensitive code written to avoid a dependency our users already have.

**npm, as v1 used.** A Go binary on npm requires a postinstall that downloads a platform binary. That is an anti-pattern which reliably breaks behind corporate proxies and offline installs, and the audience is 179 downloads a month. v1.0.7 stays published and functional instead.

## Consequences

**Risk R1 is resolved: go-gh cannot reach a keyring token without the gh binary.** It has no keyring code and no keyring dependency, and `go list -m all` over the full transitive graph returns zero keyring modules. Its own doc comment says as much: the source can be an environment variable, a configuration file, or the system keyring, and in the last case it shells out to `gh auth token`. That shellout (`pkg/auth/auth.go:103`) is the only keyring path there is. Measured on the reference machine, with the token in the keyring, no `oauth_token` in `hosts.yml`, and no environment variables set:

| Scenario | Result |
|---|---|
| `TokenForHost`, gh on PATH | token found, source `"gh"` (subprocess) |
| `TokenForHost`, gh **off** PATH | **empty, source `"default"`** |
| gh off PATH, `GH_TOKEN` set | token found, source `"GH_TOKEN"` |

**The decision is to require `GH_TOKEN` for users without gh, and to document it.** Anyone with gh installed gets their token free through the shellout, which is most of this audience, because v1 required gh. Anyone without gh sets `GH_TOKEN`, which is the normal contract for a CLI and what CI already does. It also fixes the `repository.Current()` trap below for free.

This does not weaken the decision above. go-gh still supplies the client, host resolution, and the token itself for gh users. It supplies one thing less than was assumed, and the gap is one documented environment variable. **[ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md)'s standalone-coherence argument survives**: the binary needs no gh, only a token.

**The resolution chain is branched by host type, not flat**, which corrects an assumption in the canon:

1. If the host is `github.com`, a tenancy host (`*.ghe.com`), or `github.localhost`: `GH_TOKEN`, then `GITHUB_TOKEN`
2. Otherwise (GHES): `GH_ENTERPRISE_TOKEN`, then `GITHUB_ENTERPRISE_TOKEN`
3. `hosts.yml`'s `oauth_token`
4. `GH_PATH`, else `safeexec.LookPath("gh")`
5. If gh was found, shell out to it. This is the only keyring path.
6. Otherwise `("", "default")`

`GH_TOKEN` is **never** consulted for a GHES host, and `GH_ENTERPRISE_TOKEN` is **never** consulted for github.com. 2.0.0 serves github.com alone ([ADR-0009](./0009-host-qualified-repo-identity.md)), so `GH_TOKEN` is the only spelling that matters here.

**`repository.Current()` carries a trap.** It shells out to **git**, not gh, and needs git on PATH or `GH_REPO` set. But it calls `auth.KnownHosts()`, which reads only environment variables and `hosts.yml`, never the keyring. On a machine where gh was never installed, `Current()` fails with "none of the git remotes point to a known GitHub host" even though git works and the remote is plainly github.com. Setting `GH_TOKEN` populates `KnownHosts()` and fixes it.

Two remedies were rejected.

**Vendor `zalando/go-keyring`** and read gh's entry at service name `"gh:" + hostname`. Verified working on the reference machine with no GUI prompt. Rejected because it only helps someone who *had* gh and removed it: a true standalone user never had gh, so there is no keyring entry to find. It also couples us to gh's `internal/` storage scheme, which is not a public interface and can change without notice, and on headless Linux it needs a D-Bus Secret Service that is often absent.

**Drop standalone and ship the gh extension alone.** Rejected: it abandons Homebrew and `go install`, and knocks a leg out from under ADR-0008.

**Risk R3 is resolved: the asset name must end with `{GOOS}-{GOARCH}`.** gh selects a release asset with `strings.HasSuffix(a.Name, platform+ext)`, where `platform` is `GOOS-GOARCH` and `ext` is `.exe` on Windows and empty elsewhere (`pkg/cmd/extension/manager.go`, `installBin`). The prefix is ours to choose and the suffix is not, so `gh-runs_v2.0.0-alpha.0_darwin-arm64` matches and `gh-runs_2.0.0_Darwin_arm64` does not. Case and separator are both load-bearing. On Apple silicon gh falls back to a `darwin-amd64` asset through Rosetta 2 when no `darwin-arm64` asset exists. When nothing matches, install fails with "unsupported for {platform}" and tells the user to open an issue against this repository.

Goreleaser's stock naming title-cases the OS and joins with underscores, which does not match, and a mismatch produces a release that looks complete and installs nowhere. Release automation therefore uses `cli/gh-extension-precompile`, gh's own action, which already names assets to this convention.

**Prereleases are invisible to `gh extension install`, which is what makes an alpha safe.** gh resolves an unpinned install through `repos/{owner}/{repo}/releases/latest`, and GitHub defines that endpoint as the most recent non-prerelease, non-draft release. A release marked prerelease is therefore skipped. Testers opt in explicitly with `gh extension install jv-k/gh-runs --pin v2.0.0-alpha.0`, which resolves through `releases/tags/{tag}` instead and does see it.

**Homebrew ships from a personal tap, `jv-k/homebrew-tap`.** homebrew-core's notability guidance sits above this repository's 42 stars, and core would also own the formula's update cadence. A tap is the channel we control, and it keeps Homebrew and the extension on the same release.

**That tap does not exist, and until it does Homebrew is a claim rather than a channel.** The paragraph above states it as settled and three places lean on it: this ADR's decision line, its rejection of shipping the extension alone, and [ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md)'s standalone-coherence argument, which reasons about a Homebrew user who has no `gh` to fall back on. The [README](../../README.md) tells a reader the tool will install through Homebrew. Nothing implements it. `.github/workflows/release.yml` has one step, `cli/gh-extension-precompile`, which cross-compiles and publishes binaries to the GitHub release and does not publish a formula, because publishing a formula is not a thing it does. Three pieces are missing, and each is a decision rather than a task:

| Missing | What it is |
|---|---|
| `jv-k/homebrew-tap` | A repository. `brew tap jv-k/tap` resolves that exact name and nothing else does |
| `Formula/gh-runs.rb` | A formula. For a Go binary the ordinary shape builds from the release's source tarball with `depends_on "go" => :build` |
| A tap job on the release tag | The step that rewrites the formula's `url` and `sha256` when a tag ships, so the tap and the release never sit on different versions |

**The normal path is a formula-bump action, not goreleaser.** goreleaser's Homebrew block generates a formula pointing at goreleaser's **own** artifacts, so adopting it means goreleaser builds and uploads the release. That is the pipeline this ADR rejected two paragraphs up, on R3's naming. The two can coexist narrowly: gh matches an asset with `HasSuffix(name, "darwin-arm64")`, which no goreleaser `.tar.gz` ever satisfies, so the assets would not collide and the extension would still install. What it would cost is two release tools writing one release on one tag, to produce one file. A bump action builds nothing. It computes the source tarball's checksum and commits the formula, `mislav/bump-homebrew-formula-action` and `dawidd6/action-homebrew-bump-formula` being the two in common use, and either leaves `cli/gh-extension-precompile` untouched. That last property is the one that matters, because that action is the only thing standing between R3 and a release that looks complete and installs nowhere.

**The alpha does not ship to Homebrew, so the tap is not on the alpha's path.** A prerelease is invisible to `gh extension install`, which resolves through `releases/latest`, and `go install ...@latest` skips it for the same reason, so both channels default to stable while a tester opts in by naming the tag. Homebrew has no such rule. A tap formula is a file, and whatever version it names is what `brew install` hands everyone who taps it. There is no prerelease for it to skip and no tag for a tester to pin. Publishing v2.0.0-alpha.0 to the tap would make the alpha the default install on the one channel that cannot mark it as one, which is the exact property the hyphen-in-the-tag rule protects on the other two. **Homebrew therefore starts at v2.0.0.** The tap must exist by then, and it need not exist before.
