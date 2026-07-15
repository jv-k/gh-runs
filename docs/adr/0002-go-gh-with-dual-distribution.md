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

Risk R3, the exact asset naming that lets `gh extension install` find a precompiled binary, blocks release automation but not design.
