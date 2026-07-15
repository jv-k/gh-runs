# Rename to gh-runs

v2 broadens from deleting Runs to watching, dispatching, re-running and reclaiming, so the name `delete-workflow-runs` would actively misdescribe a tool that mostly does not delete. We rename the repository to `jv-k/gh-runs`, invoked as `gh runs`. That sits deliberately next to gh's own `gh run` noun, where `gh run` is the one-shot command and `gh runs` is the interactive manager.

## Considered Options

**Keep `delete-workflow-runs`.** Preserves 42 stars and name recognition, at the cost of a name that lies about the product.

**Fresh repository.** Clean history, but abandons stars, watchers and inbound links for no benefit the rename does not already provide.

## Consequences

GitHub's redirects make this cheap, and we have direct evidence rather than faith. This repository **has already been renamed once**, from `delete-workflow-runs` to `delete-gh-workflow-runs`, and the original URL still 301s today, years later. Stars, watchers and issue history survive.

The npm package stays published and working at 1.0.7, with `npm deprecate` pointing at the successor. See [ADR-0002](./0002-go-gh-with-dual-distribution.md) for why npm is not a v2 channel. Note that `package.json`'s `homepage` and `repository` are already stale from the *previous* rename and need updating in the deprecation release.

An unrelated `Nick2bad4u/gh-runs-cleanup` exists, with 1 star. Different repository name, so there is no install-path or registration conflict.
