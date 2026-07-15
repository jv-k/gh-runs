# Rename to gh-runs

v2 broadens from deleting Runs to watching, dispatching, re-running and reclaiming, so the name `delete-workflow-runs` would actively misdescribe a tool that mostly does not delete. We rename the repository to `jv-k/gh-runs`, invoked as `gh runs`. That sits deliberately next to gh's own `gh run` noun, where `gh run` is the one-shot command and `gh runs` is the interactive manager.

## Consequences

**The URL is cheap to change. The identity is not.** GitHub's redirects carry the address across, and we have direct evidence rather than faith: this repository **has already been renamed once**, from `delete-workflow-runs` to `delete-gh-workflow-runs`, and the original URL still 301s today, years later. All 42 stars, watchers and issue history survive. What the 301 establishes is that the *old address* keeps resolving, not that the *new name* is cheap to unmake. Once people have installed `gh runs`, `npm deprecate` points at gh-runs, and the extension is registered under that name, reverting is not a revert. It is a second rename, and a second migration of everyone who moved for the first.

That previous rename is also what the cost looks like in practice. `package.json`'s `homepage` and `repository` stayed stale from it for **years**, pointing at a name the repository had already left, and were corrected only because this rename forced someone to look. A redirect keeps an old URL resolving. It does not chase down the references, and the references are where an identity actually lives.

The npm package stays published and working at 1.0.7, with `npm deprecate` pointing at the successor. See [ADR-0002](./0002-go-gh-with-dual-distribution.md) for why npm is not a v2 channel.

An unrelated `Nick2bad4u/gh-runs-cleanup` exists, with 1 star. Different repository name, so there is no install-path or registration conflict.
