# gh-runs

**A live GitHub Actions dashboard across your repositories, where deletion is one operation.**

`gh runs` opens a Feed of the Runs across every repository you have, and keeps it current as new ones are invoked. Select any number of Runs and delete, cancel or re-run the whole selection at once. A second tab turns Workflows on and off, and dispatches them. A third shows what your Caches and Artifacts are actually costing, and reclaims it.

`gh` covers the one-shot operations well. This tool exists for the four things it has no answer for: bulk actions, a view across repositories, a surface that updates itself, and storage.

## Status

2.0.0 is code complete and untagged, so there is no release to install from yet. `go install` from `main` works today, and the `gh extension install` path opens with the first v2 tag.

## Install

**As a gh extension.** Once 2.0.0 is tagged:

```sh
gh extension install jv-k/gh-runs
gh runs
```

That resolves the latest stable release. Prereleases are invisible to it, so a tester opts into one by naming the tag:

```sh
gh extension install jv-k/gh-runs --pin v2.0.0-alpha.0
```

**As a standalone binary.** The command is `gh-runs`, and it needs no gh:

```sh
go install github.com/jv-k/gh-runs/v2@main     # today
go install github.com/jv-k/gh-runs/v2@latest   # once 2.0.0 is tagged
```

**Homebrew** ships from `jv-k/homebrew-tap` starting at 2.0.0 stable. The tap does not exist yet, so for now Homebrew is a plan rather than a channel ([ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md)).

## Authentication

With gh installed and authenticated there is nothing to do. The token comes from gh, keyring included.

Without gh, set `GH_TOKEN`:

```sh
export GH_TOKEN=...
```

That is not a convenience. go-gh reaches a keyring only by shelling out to the gh binary, so on a machine that never had gh there is no other path to a token ([ADR-0002](docs/adr/0002-go-gh-with-dual-distribution.md)). The token needs read access to the repositories you want to see, and write access wherever you delete, cancel, re-run or dispatch. 2.0.0 serves github.com alone.

## The dashboard

Bare `gh runs` opens the TUI, and so does bare `gh runs delete`. It refuses to start when standard output is not a terminal, rather than write control sequences into a pipe.

| Tab | What it holds |
|---|---|
| **Feed** (`1`) | Runs across your repositories, updating as they are invoked, with a badge for the ones blocked on a human decision |
| **Workflows** (`2`) | Every Workflow, its state, and the `workflow_dispatch` form |
| **Storage** (`3`) | Caches and Artifacts by size, largest first, and the reclamation that deletes them |

Polling across repositories is affordable because a conditional request answering 304 costs nothing against the rate limit. Idle repositories are close to free, and whatever you are looking at is polled hardest.

### Keys

Two profiles, Vim and Standard. They differ on motion and nowhere else.

| Key | Action |
|---|---|
| `k` `j` or `↑` `↓` | Move a row |
| `g` `G` or `home` `end` | First row, last row |
| `tab` `shift+tab`, `1` `2` `3` | Change tab |
| `space` | Select the row, toggling |
| `enter` `esc` | Open detail, close it |
| `/` | Filter |
| `d` | Delete the selection |
| `c` `C` | Cancel, force-cancel |
| `R` `F` `J` | Re-run, re-run failed Jobs, re-run a Job by name |
| `A` `b` | Approve or review, filter to what is awaiting one |
| `s` `x` | Enable or disable a Workflow, dispatch one |
| `a` `w` | Artifacts only, download the one under the cursor |
| `t` `D` `e` `n` `N` | In a log: timestamps, delete, export, next and previous match |
| `r` `u` | Refresh, re-enumerate and re-probe the account |
| `ctrl+x` `ctrl+r` | Stop a running operation, retry only its failures |
| `,` `?` `q` | Settings, help, quit |

The two chords are chords deliberately. A long operation keeps running while you navigate a tab whose bare letters delete and re-run, so the key that stops it does not sit one keystroke away from them.

## The CLI

Every operation the dashboard performs has a non-interactive form, so the tool is scriptable and does not need the TUI to be useful.

```sh
gh runs list --status failure --limit 100
gh runs list --all-repos --workflow ci.yml --json databaseId,conclusion

gh runs delete --all-repos --status failure --dry-run          # report, delete nothing
gh runs delete --all-repos --status failure --all --yes

gh runs cancel 123456 789012
gh runs cancel --all-repos --status in_progress --all --yes --force

gh runs rerun 123456 --failed
gh runs rerun --branch main --all --yes --job-name "build (ubuntu-latest)"
```

`list` speaks gh's flags, `--json`, `--jq` and `--template` included. Every mutating command takes `--dry-run`, and none of them acts on a matched set without both `--all` and `--yes`.

Outside a repository the commands fan out across every discovered repository. Inside one they act on that repository, the way gh does, until you pass `--all-repos`.

## Deletion is irreversible, and treated that way

A Purge cannot be undone, so four things stand in front of it.

The confirmation is graduated. A small selection confirms with `y`. At or above the configured threshold you type the affected count, and `enter` on its own aborts, because the default answer is no. `v` inspects the frozen set before you commit to it.

The set is frozen at the moment you confirm. What you reviewed is what gets deleted, even as the Feed keeps moving underneath it.

Every deletion is appended to `$XDG_STATE_HOME/gh-runs/deletions.log`, because a Purge destroys things and nothing else records what it destroyed. Nothing reads that log back. If it cannot be written, the deletion does not happen.

A run of consecutive failures stops the operation rather than grinding through the rest of the set.

## Configuration

Settings live in `config.yml`, under `$XDG_CONFIG_HOME/gh-runs` if that is set, else `%AppData%\gh-runs` on Windows, else `~/.config/gh-runs`. The Settings view (`,`) writes the same file. A missing file is not an error, and neither is an unrecognised value: it is reported, and the default stands.

| Key | What it does |
|---|---|
| `budget` | The share of the rate limit polling may spend |
| `confirm_threshold` | The set size at which a destructive action starts demanding a typed count |
| `purge_breaker_failures` | Consecutive failures at which a Purge stops itself |
| `discovery_refresh_minutes` | How often discovery re-probes |
| `keybinding_profile` | `Vim` or `Standard` |
| `theme`, `timestamp` | `auto`, `dark` or `light`, and absolute or relative times |
| `exclude`, `pin` | Repositories to drop from discovery, and ones to poll harder |
| `workflows_scope`, `storage_scope` | Each tab's repository scope, set independently |
| `launch_filter` | The filter the Feed opens with |

There is no stored setting that skips confirmation, and there will not be one.

Two other paths matter. The local store, the on-disk record of what the API said, lives under `$XDG_CACHE_HOME/gh-runs`. The deletion log lives under `$XDG_STATE_HOME/gh-runs`. Exports and downloads land in the working directory, where you would look for them.

## Looking for v1?

v1 was `delete-workflow-runs`, a bash script that piped a filtered run list into `fzf --multi` and deleted whatever you selected. v2 keeps that capability and subordinates it to the live Feed.

- **Source:** the [`v1.0.7`](https://github.com/jv-k/gh-runs/tree/v1.0.7) tag
- **npm:** `delete-workflow-runs@1.0.7`, still installable and no longer maintained

![Terminal recording of the v1 script selecting workflow runs in fzf and deleting them](demo.gif)

## How it is built

The design is written down, and those documents are the good part of this repository.

| Doc | What it is |
|---|---|
| [docs/PRD.md](docs/PRD.md) | The product definition, and a constraints table where every row was measured against the live API |
| [docs/CONTEXT.md](docs/CONTEXT.md) | The glossary, which is binding |
| [docs/adr/](docs/adr/) | The decisions, and the options each one beat |
| [docs/features/](docs/features/) | Sixteen requirement sets, numbered and testable |
| [docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) | What was built in what order, and why that order |

Notifications are deferred to 2.1, on correctness rather than cost: `osascript` exits 0 whether or not a toast rendered, so a precompiled binary cannot honestly report whether delivery worked ([ADR-0013](docs/adr/0013-dependency-pins.md)).

## Contributing

Contributions are welcome. The requirements under [docs/features/](docs/features/) each end in an Open questions section, and those questions are real.

[Open an issue](https://github.com/jv-k/gh-runs/issues/new/choose) to report a bug or request a feature. See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## Support

If you find this useful, see [DONATE.md](DONATE.md).

## License

The code and documentation in this project are released under the [MIT license](https://github.com/jv-k/gh-runs/blob/main/LICENSE).
