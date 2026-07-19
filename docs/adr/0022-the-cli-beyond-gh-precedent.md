# The CLI beyond gh's precedent: fan-out, the bare delete, and the match-all spelling

[ADR-0008](./0008-full-cli-surface-despite-gh-overlap.md) committed the CLI to being a drop-in superset of `gh run`, and [cli-surface](../features/cli-surface/requirements.md) R2 pins parity wherever gh has behaviour to mirror. Several of that feature's open questions sat exactly where gh has none: outside a repository gh errors, bare `gh run delete` prompts, and gh has no cross-repository surface at all. This ADR fixes the superset's shape where parity is silent. The exit-code, host-message and skip-set resolutions taken in the same session are requirement grade and live in cli-surface (R17, R9, R12) without an ADR.

## Outside a repository, list fans out

`gh runs list`, invoked outside a repository with no `-R` and no `GH_REPO`, fans out across every discovered repository rather than failing as gh does. Inside a repository, no `-R` means that repository, which is gh's own rule, so R2's parity is untouched.

The product is cross-repository first, and the TUI already has this exact rule: [settings](../features/settings/requirements.md) R19's tab scopes default to `all-repos`, and where there is no working-directory repository the `this-repo` scope falls back to `all-repos` and says so, rather than paint an error. Adopting gh's dead end would have made the scriptable surface the one place the product answers "which repositories" with a failure. The fan-out is the same client-side motion [ADR-0003](./0003-multi-repo-via-client-side-fanout.md) fixed for every consumer: one listing request per repository (163 discovered for the reference user, ~26 with Runs), paid from the shared Budget, one shot, with no scheduler involvement.

`--all-repos` forces fan-out from anywhere, including inside a repository, and is accepted redundantly where fan-out is already the behaviour. Long form only, with no shorthand: `-a` is taken by "include disabled workflows" (cli-surface Constraints), and a shorthand can be granted later but never taken back. The spelling is settings R19's own vocabulary.

## -L bounds the merged total

Under fan-out, `-L/--limit` bounds one merged list, newest-first by `CreatedAt`, defaulting to 20 as gh does. The flag keeps one meaning, how many Runs come back, in every scope, and the output keeps the Feed's shape, so the CLI is the scriptable view of the same thing the dashboard shows. How many Runs each per-repository request fetches before the merge is implementation below this decision.

## The repository field

`--json` gains `repository`, an object of `{name, nameWithOwner}`, requestable on any invocation, single-repo included. The shape is gh's own: `gh search prs --json repository` emits exactly this object (verified, gh 2.92.0), and the nearest-precedent rule that gave R11 its `--yes` gives this field its shape. An object extends additively, so a `host` key can arrive with multi-host ([ADR-0009](./0009-host-qualified-repo-identity.md)) without breaking a script. Under fan-out the human table carries a repository column, because rows from different repositories are otherwise indistinguishable.

## Bare delete opens the TUI

`gh runs delete`, with no Run ID and no filter flags, opens the TUI, plain, identically to bare `gh runs`. The command is an intent-synonym: the person typed "delete", and the TUI is where deletion is one operation. When standard output is not a terminal it fails with a diagnostic, R1's rule verbatim.

This is the surface's one deliberate divergence from a gh behaviour, recorded in cli-surface's Constraints: gh's bare `gh run delete` prompts an interactive single-Run picker. Building a second interactive selector beside the TUI would duplicate the confirmation surface [ADR-0019](./0019-ops-plan-and-confirmed.md) made rigorous, with none of its friction pricing.

## Match-all is spelled --all

A Purge matching everything must be asked for by name. `gh runs delete --all --yes` deletes every Run in scope. `gh runs delete --yes`, with no Run ID, no filter and no `--all`, fails with a usage diagnostic. [ADR-0016](./0016-the-filter-representation.md)'s zero-value Filter matches every Run, so without this rule two words would quietly spell "delete everything". The zero filter is unreachable by omission. The spelling is gh's precedent on `gh cache delete --all`, given no `-a` shorthand so it stays visually distinct from `list`'s unrelated `-a`.

## --all-repos reaches delete

`--all-repos` works on `delete` with the same semantics as on `list`, so a cross-repository Purge is expressible non-interactively. The canon expected it: [purge](../features/purge/requirements.md) calls cross-repo frozen sets "the ordinary case rather than the exception", and cli-surface R10's dry-run rows name each Run's repository for exactly this reason. The guard rails are the existing ones rather than new ones: `--yes` (R11), `--all` for match-all, `--dry-run`'s per-repository rows (R10), and the deletion log (purge R29). Forbidding the CLI what the TUI can do would make the scriptable surface the weaker one, inverting R10's argument that the CLI is where a resolved set is cheapest to verify.

The widest expressible command is `gh runs delete --all-repos --all --yes`. Every word of it is an explicit opt-in, and no default or working directory reaches it by accident.

## Considered Options

**Fail outside a repository, with a diagnostic naming the flag.** The drafted recommendation: gh's failure, plus discoverability. Rejected by the product owner because it hides the product's identity. A cross-repository dashboard whose CLI errors outside a repository answers its defining question with a dead end, and the TUI's own scope rule (settings R19) already falls back to `all-repos` rather than failing.

**Strict parity, no cross-repo CLI in 2.0.0.** The TUI as the only cross-repository surface. Rejected for the same reason, and because it strands scripts entirely.

**`-L` per repository.** Grouped output, hundreds of rows at reference scale for a default invocation, and the flag's meaning would change with scope. Grouping is what `-R` in a loop is for.

**A flat `owner/repo` string, or two flat fields, for `repository`.** Simpler jq, but no gh precedent, and multi-host later would have to change a string's format, which breaks scripts silently. The object gains a key instead.

**Mirroring gh's interactive picker on bare `delete`.** A second interactive surface beside the TUI, with its own confirmation story to invent, for a convenience the TUI already does better.

**Failing bare `delete` with usage.** Honest and cheap, but it sends the person who typed "delete" away empty-handed when the product's whole interactive surface is one command away. The TUI open keeps the intent.

**Pre-arming the TUI from argv.** Opening bare `delete` in a selection mode. Every TUI state so far is reached from inside the TUI, argv would become a second entry condition to test, and deletion being one operation from anywhere means pre-arming saves nothing.

**`--yes` alone as match-all.** Two words delete every Run in the repository, reachable by forgetting a filter. Rejected outright.

**No match-all spelling at all.** Delete-everything is v1's founding use case and deserves a name. Forcing a synthetic wide filter would spell the same operation dishonestly.

**`--all-repos` on `list` only.** Bounds the blast radius, but the TUI can already purge cross-repo, the rails all exist, and the asymmetry would make the verifiable surface the crippled one.

## Consequences

**cli-surface gains R22 through R27, and their acceptance criteria, amended in this ADR's commit.** R9, R12 and R17 are amended in the same commit with the session's requirement-grade resolutions.

**ADR-0003's fan-out gains a one-shot consumer.** The CLI iterates the discovered set once and exits. Nothing here touches [ADR-0021](./0021-the-scheduler-cadence-policy.md)'s tiers, which serve the TUI alone.

**R16's honesty rule extends per repository.** Under fan-out, a filtered listing caps at 1,000 within each repository, so a capped label must be per-repository or the merge must say which repositories were capped. The label's exact rendering is stage 6's to shape under R16.

**The discovered set becomes CLI-reachable.** [ADR-0020](./0020-discovery-scope-adoption-and-refresh.md)'s scope rules, including the local repository's session adoption, now feed a non-interactive surface as well as the Feed.
