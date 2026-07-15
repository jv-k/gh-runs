# Settings

> Terms are defined in [CONTEXT.md](../../CONTEXT.md). Decisions are recorded in [adr/](../../adr/).

## Purpose

Settings **expose intent, never mechanism**. A setting earns its place only if a person can answer it from their own context. A question that requires the token tier, the points model and the repo count to answer belongs to the scheduler, which has all three, and not to the user, who has none.

## Requirements

**R1.** Settings MUST live in a single config file at `$XDG_CONFIG_HOME/gh-runs/config.yml`, defaulting to `~/.config/gh-runs/config.yml`.

**R2.** State and caches (persisted ETags, discovered repositories, window state) MUST live under the XDG state directory and MUST NOT be written into the config file. A person MUST be able to copy their config file to another machine, or commit it to a dotfiles repository, without carrying machine-local state with it.

**R3.** Every key MUST be optional. A missing config file, and an empty one, MUST both be valid and MUST produce identical behaviour to a file containing every default.

**R4.** Precedence MUST be flags, then environment, then config file, then defaults (highest first).

**R5.** The keybinding profile MUST offer **exactly two** values: **Vim** and **Standard**. A third profile MUST NOT be added on the grounds of platform.

**R6.** A theme setting MUST exist.

**R7.** Repositories MUST be excludable and pinnable by host-qualified identity. Exclusion MUST remove a repository from discovery, the Feed and all polling. Pinning MUST prioritise it.

**R8.** The share of the API **Budget** this tool may spend MUST be settable, expressed as a share of the person's rate allowance and nothing else. It MUST NOT be expressed as an interval, a request rate, or a delete rate. The polling scheduler and the rate governor MUST derive their mechanism from it.

**R9.** The filter applied to the Feed at launch MUST be settable.

**R10.** Timestamps MUST be switchable between relative and absolute.

**R11.** Notification events MUST be selectable as a matrix, expressed at the level of intent. A person deciding whether to turn one on MUST be able to answer from their own context, without knowing which endpoint or event name produces it. The default MUST be conservative.

**R12.** The threshold above which a destructive action requires the operator to **type the affected count** MUST be settable, and MUST be bounded by a hard maximum that the config cannot exceed. Note the inversion: raising the threshold *lowers* protection, so the floor under protection is implemented as an **upper bound on the setting's value**. A value above the maximum MUST be clamped with a diagnostic, not honoured. There MUST be no lower bound. A person may demand the typed confirmation for every deletion.

**R13.** The following MUST NOT exist as settings, in the config file, as flags, or as environment variables. They are absent, not hidden.

| Rejected | Reason |
|---|---|
| Poll interval in seconds | Mechanism. Choosing it needs the token tier, the repo count and the points model. The scheduler has all three, the user has none |
| Deletes per second | Mechanism, and dangerous. The adaptive governor beats any fixed number. This knob's only real function is letting someone get their account blocked |
| Cache TTL | Meaningless. ETag revalidation is already free *and* correct, and a TTL could only make data staler |
| Concurrency | Internal. Bounded by the secondary rate limit, not by taste |
| A stored setting that skips confirmation | The one thing standing between a keystroke and 5,000 deleted Runs. The CLI's `--yes` is not this and is not affected: a mandatory per-invocation flag *is* that surface's confirmation ([cli-surface](../cli-surface/requirements.md) R11), where a persistent setting could only waive one. What is rejected here is the stored form, including any key that would supply `--yes` on the operator's behalf |

**R14.** An unrecognised config key MUST produce a diagnostic and MUST NOT fail the run. Each of R13's five rejected keys MUST produce a **specific** diagnostic naming its reason from that table, rather than a generic "unknown key". The point of R13 is that these are refused for stated reasons, and someone who reaches for one deserves the reason rather than silence.

**R15.** `NO_COLOR`, set to any value, MUST suppress ANSI colour output, and MUST override the theme setting (R6) rather than be overridden by it. Accessibility is not a preference to be configured away.

**R16.** Meaning MUST NEVER be encoded in colour alone. Every Status, Conclusion and outcome distinction the interface draws MUST survive being rendered in monochrome, carrying a text or glyph label that is not redundant with its colour.

**R17.** The TUI's Settings view and the config file MUST be the same settings. A change made in the view MUST persist to the file, and MUST NOT discard comments, key order, or keys the running version does not recognise.

**R18.** The Settings view MUST render to a frame from held state alone, with no live terminal and no network, and that frame MUST be verified by golden-file tests. One of those goldens MUST assert that none of R13's rejected settings appears in the rendered view. R13's claim is that they are absent, not hidden, and absence is the one claim prose cannot keep: a view built by walking a schema grows a row the moment someone adds a key, and R14's diagnostic fires on a key that was *typed*, never on one this view offers. No other requirement here would catch it.

## Acceptance criteria

**AC1: No config file is a valid config.** With `$XDG_CONFIG_HOME` set to a temporary directory containing no config file, the tool starts, and every setting reports its default. Writing an empty `config.yml` changes nothing.

**AC2: State never enters the config file.** With `$XDG_CONFIG_HOME` set, no file is created under `~/.config/gh-runs/`. No ETag, repository list, or window state ever appears inside `config.yml`.

**AC3: Precedence holds.** A config file setting the default launch filter, overridden by the equivalent flag, yields the flag's value. With the flag absent it yields the config's value, and with both absent, the default.

**AC4: Exactly two keybinding profiles.** The keybinding profile accepts exactly `vim` and `standard`. Any other value, including `mac` or `windows`, is rejected with a diagnostic listing the two valid values.

**AC5: Exclusion reaches the network.** Excluding a repository removes it from the Feed and results in zero HTTP requests to it across a full polling cycle.

**AC6: The Budget knob is intent, not mechanism.** The Budget setting's schema admits no unit of time and no unit of requests. There is no config key, flag, or environment variable whose value is a poll interval, a delete rate, a cache TTL, or a concurrency level (R13).

**AC7: A rejected key gets its own reason.** Setting `poll_interval: 5` in `config.yml` starts normally and emits a diagnostic containing that key's reason from R13: that choosing it requires the token tier, repo count and points model. The same holds for the other four. Setting `some_future_key: 1` emits a generic unknown-key diagnostic and also starts normally.

**AC8: The confirm threshold is clamped.** Setting the confirm threshold above the hard maximum clamps it to the maximum and emits a diagnostic. Deleting a set larger than the effective threshold requires typing the count. No config file and no environment variable produces a state in which a bulk deletion proceeds with no confirmation at all: in the TUI the modal still opens, and in the CLI `--yes` is still required on the command line.

**AC9: `NO_COLOR` beats the theme.** With `NO_COLOR` set to any value, including the empty string, output contains no ANSI escape sequences, whatever the theme setting says.

**AC10: Meaning survives monochrome.** With colour stripped, every Status and Conclusion in the Feed remains distinguishable by text alone. As it happens, v1's `SKIP`/`GOOD`/`FAIL` labels already satisfied this.

**AC11: The view writes the file without damage.** Editing a setting in the TUI and quitting leaves `config.yml` changed in that key only, with unrelated comments and key order intact.

**AC12: Goldens hold the Settings view.** Rendering the Settings view from held state, with no terminal and no network, reproduces the stored golden byte for byte. The golden contains no row, label, field or help text for a poll interval, deletes per second, a cache TTL, a concurrency level, or a stored setting that skips confirmation. Adding any of the five to the view fails it.

## Constraints

**The user has 163 repositories and cares about roughly 10** (~26 have Runs at all). That ratio is what makes R7 a real setting rather than a convenience: the difference between the discovered set and the interesting set is more than an order of magnitude, and no heuristic recovers the user's intent.

**Conditional requests are free** (a 304 consumes zero primary rate limit, verified by interleaved measurement), and ETag revalidation is simultaneously cheaper and more correct than any expiry policy. This is why cache TTL is not merely unnecessary but *meaningless*: every value a person could choose is strictly worse than the behaviour they already have.

**DELETE costs 5 points against a ~900 points/min secondary limit (~180/min), while GitHub's prose advises at least one second between writes (~60/min)**. That is a 3× disagreement, and on an 18,260-Run Purge it resolves to ~100 minutes versus ~5 hours. Any fixed rate is wrong for somebody: Enterprise Cloud has 3× the primary budget, GitHub Apps scale differently, and a token shared with CI has less headroom than the limit suggests. The adaptive governor observes what the account actually tolerates ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)). This is the whole argument for R8 and against R13's first two rows.

**A terminal erases the distinction that a third keybinding profile would encode.** On macOS, Cmd never reaches the application (Terminal.app and iTerm2 consume it for tabs and copy/paste), and Ctrl+C is SIGINT everywhere, so a "Windows" profile cannot map Ctrl+C to copy. Strip out what terminals actually deliver and "Mac keys" and "Windows keys" collapse into the same thing: arrows plus Ctrl. Shipping three profiles would cargo-cult a distinction that does not survive contact with the terminal (R5).

**`NO_COLOR` semantics are gh's, verified on gh 2.92.0** (`gh help environment`): "set to **any value** to avoid printing ANSI escape sequences for color output". Any value, not any non-empty value. R15 matches that reading deliberately.

**gh's config-directory precedent is verified**: `GH_CONFIG_DIR`, else `$XDG_CONFIG_HOME/gh`, else `$AppData/GitHub CLI` on Windows, else `$HOME/.config/gh`. R1 follows the XDG limb of it.

**Fine-grained PATs expose no scopes**, so no setting can be validated against the token's actual permissions ahead of time, and the API remains the final authority. A 403 can arrive despite `push: true`. A setting that gated on presumed permissions would be lying.

Historical note: v1's `SKIP`/`GOOD`/`FAIL` labels were derived by string-substituting the **Conclusion** field, and were plain text with no colour. That made them accidentally the accessible choice, and R16 carries the principle forward deliberately rather than by luck.

## Open questions

- **What is the hard maximum for the confirm threshold (R12)?** UNKNOWN. It must sit far enough below the reference repository's 28,707 Runs that the protection still binds on a real Purge, but no number has been chosen.
- **How is the Budget share expressed (R8)?** A percentage, or named intent tiers ("background", "normal", "greedy")? A percentage is precise but invites the same false confidence as a poll interval. Tiers are honest about the resolution actually available. Undecided.
- **Does the Budget share govern a Purge's deletes, or only polling?** This is a real conflict, not a gap. At the points ceiling a Purge spends 180 deletes/min × 5 points = **900 points/min, the entire secondary allowance**, so a Purge inherently saturates the Budget. Either ADR-0007's ramp ignores the Budget setting, which makes the setting a half-truth, or the setting throttles the Purge, in which case "spend 25% of my Budget" turns a 100-minute Purge into a ~7-hour one and the honest label for the knob is no longer a share. UNKNOWN. ADR-0007 does not address the interaction.
- **Do we honour gh's accessibility configuration?** gh 2.92.0 defines `GH_ACCESSIBLE_COLORS` / `accessible_colors` (4-bit palettes chosen against terminal background), `GH_ACCESSIBLE_PROMPTER` / `accessible_prompter` (prompts compatible with speech synthesis and braille displays), and `GH_SPINNER_DISABLED` / `spinner disabled` (text progress instead of motion). All are verified, and the first two are marked preview. [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) commits us to compatibility with gh's *flags*, and says nothing about gh's *config*. Someone who has already configured these for gh would reasonably expect a gh extension to respect them. The spinner question is not hypothetical: a Purge shows progress for 100 minutes to 5 hours. Undecided.
- **Do `CLICOLOR` and `CLICOLOR_FORCE` apply?** gh honours both alongside `NO_COLOR` (`CLICOLOR=0` disables, `CLICOLOR_FORCE` non-zero keeps colour when piped). R15 mandates only `NO_COLOR`. Whether the trio is honoured as a set is undecided.
- **What are the notification matrix's axes (R11)?** "Do I want to know when my Dispatch finishes?" is the example of an answerable question, but whether the matrix is events × scope (mine / anyone's), events × Conclusion, or a flat list is undecided, as is which cells the conservative default turns on. Owned jointly with [notifications](../notifications/requirements.md).
- **How is the default launch filter expressed (R9)?** Reusing the CLI's flag vocabulary would mean one filter language across both surfaces, but the CLI's `-s/--status` is deliberately permissive across Status and Conclusion, which is a poor thing to bake into a stored default. Undecided.
- **Does the theme setting auto-detect the terminal background?** gh derives 4-bit palettes from dark and light backgrounds. Whether our theme list includes an "auto" member, and how many themes ship, is undecided.
- **Are hand-edits to `config.yml` picked up by a running instance, or only at startup?** R17 requires the view and the file to agree. It does not say whether the file is watched. Undecided.
- **What happens when a repository is both excluded and pinned (R7)?** Exclusion should presumably win, but the precedence is unstated.
- **Is there a Windows config path?** gh falls back to `$AppData/GitHub CLI` when `$XDG_CONFIG_HOME` is unset. The PRD does not state which platforms 2.0.0 targets, so whether R1 needs a Windows limb is UNKNOWN.

## Related

- [ADR-0007: Adaptive delete throttle, not a fixed rate](../../adr/0007-adaptive-delete-throttle.md) (why deletes-per-second is not a setting)
- [ADR-0004: Liveness via conditional ETag polling](../../adr/0004-conditional-polling-for-liveness.md) (why cache TTL is meaningless and why ETags must persist)
- [ADR-0009: Repo identity is host-qualified, though 2.0.0 ships github.com only](../../adr/0009-host-qualified-repo-identity.md) (R7's identity format)
- [ADR-0003: Multi-repo Feed via client-side fan-out](../../adr/0003-multi-repo-via-client-side-fanout.md) (why exclusion has a direct rate cost)
- [ADR-0008: A full CLI surface, mirroring gh's flags, despite the overlap](../../adr/0008-full-cli-surface-despite-gh-overlap.md)
- Siblings: [polling-scheduler](../polling-scheduler/requirements.md) and [rate-governor](../rate-governor/requirements.md) (own the mechanism R8 refuses to expose), [notifications](../notifications/requirements.md) (owns R11's events), [repo-discovery](../repo-discovery/requirements.md) (owns the set R7 filters), [local-store](../local-store/requirements.md) (owns R2's state directory), [purge](../purge/requirements.md) (owns R12's confirmation), [cli-surface](../cli-surface/requirements.md) (owns R4's flags), [live-run-feed](../live-run-feed/requirements.md) (owns R9's filter)
