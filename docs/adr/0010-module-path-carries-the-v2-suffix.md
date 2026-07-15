# The Go module path carries the /v2 suffix

Shipping as 2.0.0 decides this for us. Go's module reference is lexical and admits no exception: "Starting with major version 2, module paths must have a major version suffix like `/v2` that matches the major version." The module path is therefore `github.com/jv-k/gh-runs/v2`, and `go.mod` says so from the first commit.

The rule keys on the major number, not on stability, so **the first tag it binds is `v2.0.0-alpha.0`**, not the eventual stable release. There is no window in which we can tag a v2 prerelease and defer the suffix.

## Considered Options

**Develop on v0.x, adopt v2 at ship.** v0 needs no suffix, so the module path stays clean while the code is in flux. Rejected on two counts. It contradicts [ADR-0001](./0001-rename-to-gh-runs.md) and the PRD, which fix the product identity at 2.0.0. The alpha would ship as v0.1.0 and mean something different from what every other artefact calls it. The suffix would also land at ship anyway, changing the `go install` path at the exact moment the most people are following it. That is the same class of stale-reference cost ADR-0001 was written about.

## Consequences

**The suffix touches one of three channels.** `gh extension install` and Homebrew both consume built binaries from a GitHub release and never resolve a module path, so neither sees it. Only `go install` does:

```sh
go install github.com/jv-k/gh-runs/v2@latest
```

That is the whole cost: one uglier line in the install docs, for the channel with the smallest audience.

**Getting it wrong fails closed, not silently.** A `v2.0.0-alpha.0` tag on a module declared as `github.com/jv-k/gh-runs` makes `go install` refuse the version outright rather than resolve something stale. We find out on first use of the channel, not from a user's broken binary.

**The repository directory keeps its name.** The `/v2` suffix is a module path element, not a directory. There is no `v2/` subtree, and the major version lives in `go.mod` and the tag alone.
