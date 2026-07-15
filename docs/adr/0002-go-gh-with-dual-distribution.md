# Build on go-gh, ship as both a gh extension and a standalone binary

Every v1 user already has `gh` installed, because v1 required it. We build on `github.com/cli/go-gh` and ship `gh-runs` two ways: as a gh extension (`gh extension install jv-k/gh-runs`) and as a standalone binary via Homebrew or `go install`. This is the gh-dash pattern. go-gh gives us token discovery, host resolution and a REST client for free, which is the entire authentication story we would otherwise own.

## Considered Options

**google/go-github, standalone.** Drops the gh association, but we would implement token discovery ourselves: environment variables, then config, then keyring, then device-flow OAuth. That is a lot of security-sensitive code written to avoid a dependency our users already have.

**Pure gh extension.** Simplest, but surrenders Homebrew and `go install` as channels.

**npm, as v1 used.** A Go binary on npm requires a postinstall that downloads a platform binary. That is an anti-pattern which reliably breaks behind corporate proxies and offline installs, and the audience is 179 downloads a month. v1.0.7 stays published and functional instead.

## Consequences

**This ADR rests on unverified risk R1.** The reference token lives in the **OS keyring**, not in `hosts.yml`. If go-gh's resolution chain cannot reach a keyring-stored token without the gh binary present, then "standalone" is a shim that silently depends on gh, and both this decision and [ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md) need revisiting. ADR-0008 is justified partly *by* standalone coherence, so the two fall together. Verify before building the distribution pipeline.

Risk R3, the exact asset naming that lets `gh extension install` find a precompiled binary, blocks release automation but not design.
