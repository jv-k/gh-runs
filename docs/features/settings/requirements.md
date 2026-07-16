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

**R8.** The share of the API **Budget** this tool may spend **polling** MUST be settable, expressed as a share of the person's primary rate allowance and nothing else. It MUST NOT be expressed as an interval, a request rate, or a delete rate. The polling scheduler derives its intervals from it.

**The word "polling" is load-bearing and MUST NOT be dropped.** The Budget is a share of the **primary** limit, which is what reads spend. A Purge's writes are bound by the **secondary** limit, so this setting never throttles one, and [rate-governor](../rate-governor/requirements.md) R19 forbids letting it. A label promising a share of everything would be a lie in the one case a user would most notice: setting 25% and finding their Purge still runs at full rate.

**R9.** The filter applied to the Feed at launch MUST be settable.

**R10.** Timestamps MUST be switchable between relative and absolute.

**R11.** Notification events MUST be selectable as a **flat list of per-event options** in the Settings menu, one option per event in [notifications](../notifications/requirements.md) R4's table. They MUST NOT be arranged as a grid of events against scope, or of events against Conclusion. Each option MUST be expressed at the level of intent: a person deciding whether to turn one on MUST be able to answer from their own context, without knowing which endpoint or event name produces it. The default MUST be conservative, and R4's table is where it is fixed.

**R12.** The threshold above which a destructive action requires the operator to **type the affected count** MUST be settable, MUST default to **50**, and MUST be bounded by a hard maximum of **500** that the config cannot exceed. Note the inversion: raising the threshold *lowers* protection, so the floor under protection is implemented as an **upper bound on the setting's value**. A value above the maximum MUST be clamped with a diagnostic, not honoured. There MUST be no lower bound. A person may demand the typed confirmation for every deletion.

**Neither number is settable past the clamp, and that is R13's test applied rather than an exception to it.** The threshold is intent: "how many deletions do I want to have to think about" is answerable from a person's own context. The maximum is not, and it is not offered. [purge](../purge/requirements.md) R7 and R8 carry the reasoning for both numbers and its open question 1 records it. The short of it: 50 is roughly where a frozen set stops being reviewable by eye, and 500 sits three orders of magnitude below the reference repository's ~28,700 Runs, so a real Purge types its count whatever this key says.

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

**R19.** The Workflows and Storage tabs MUST each carry an **independent scope**, settable to `all-repos` or `this-repo`, and both MUST default to `all-repos`. They MUST be settable separately, so that scoping one tab leaves the other alone. `this-repo` MUST resolve to the repository of the working directory. Where there is no such repository, the tab MUST fall back to `all-repos` and MUST say so, rather than paint an empty view or interrupt with a picker. This is intent-level under R13's test: "do I want to see every repository, or the one I am in" is answerable from the person's own context, unlike a fan-out cost.

**The Runs tab gets no scope setting of its own, and the honest reason is narrower than it first appears.** [live-run-feed](../live-run-feed/requirements.md) **R1 and R2** and that document's Purpose line are what fix the Feed cross-repository: the Feed is the launch view and spans every repository on the account. R3 is a different requirement and says something more specific. It forbids per-repository **navigation** (no tabs, no sections, because ~26 tabs is not navigable) and mandates a repository **column and filter**. An earlier wording here cited R3 as the thing that "fixes it cross-repository", which it does not.

**And R3's filter, plus R9's settable launch filter, already amount to a persistent this-repo scope by another name.** A launch filter pinned to the working directory's repository gives the Runs tab exactly what R19 gives the other two, reached through the filter rather than through a scope key. So the distinction R19 draws is about **mechanism, not capability**: the Feed expresses scope as a filter it already has, and the other two tabs have no filter to express it through. What R19 must not claim is that the Runs tab has no equivalent. It has one, spelled differently, and open question "How is the default launch filter expressed (R9)?" is where that spelling gets settled.

## Acceptance criteria

**AC1: No config file is a valid config.** With `$XDG_CONFIG_HOME` set to a temporary directory containing no config file, the tool starts, and every setting reports its default. Writing an empty `config.yml` changes nothing.

**AC2: State never enters the config file.** With `$XDG_CONFIG_HOME` set, no file is created under `~/.config/gh-runs/`. No ETag, repository list, or window state ever appears inside `config.yml`.

**AC3: Precedence holds.** A config file setting the default launch filter, overridden by the equivalent flag, yields the flag's value. With the flag absent it yields the config's value, and with both absent, the default.

**AC4: Exactly two keybinding profiles.** The keybinding profile accepts exactly `vim` and `standard`. Any other value, including `mac` or `windows`, is rejected with a diagnostic listing the two valid values.

**AC5: Exclusion reaches the network.** Excluding a repository removes it from the Feed and results in zero HTTP requests to it across a full polling cycle.

**AC6: The Budget knob is intent, not mechanism.** The Budget setting's schema admits no unit of time and no unit of requests. There is no config key, flag, or environment variable whose value is a poll interval, a delete rate, a cache TTL, or a concurrency level (R13).

**AC7: A rejected key gets its own reason.** Setting `poll_interval: 5` in `config.yml` starts normally and emits a diagnostic containing that key's reason from R13: that choosing it requires the token tier, repo count and points model. The same holds for the other four. Setting `some_future_key: 1` emits a generic unknown-key diagnostic and also starts normally.

**AC8: The confirm threshold is clamped.** With no config file the effective threshold is 50. Setting it to 5,000 clamps it to 500 and emits a diagnostic. Deleting a set of 500 then requires typing the count, and so does any cross-repository set at any size ([purge](../purge/requirements.md) R7). Setting it to 1 is honoured, because there is no lower bound. No config file and no environment variable produces a state in which a bulk deletion proceeds with no confirmation at all: in the TUI the modal still opens, and in the CLI `--yes` is still required on the command line.

**AC9: `NO_COLOR` beats the theme.** With `NO_COLOR` set to any value, including the empty string, output contains no ANSI escape sequences, whatever the theme setting says.

**AC10: Meaning survives monochrome.** With colour stripped, every Status and Conclusion in the Feed remains distinguishable by text alone. As it happens, v1's `SKIP`/`GOOD`/`FAIL` labels already satisfied this.

**AC11: The view writes the file without damage.** Editing a setting in the TUI and quitting leaves `config.yml` changed in that key only, with unrelated comments and key order intact.

**AC12: Goldens hold the Settings view.** Rendering the Settings view from held state, with no terminal and no network, reproduces the stored golden byte for byte. The golden contains no row, label, field or help text for a poll interval, deletes per second, a cache TTL, a concurrency level, or a stored setting that skips confirmation. Adding any of the five to the view fails it.

## Constraints

**The user has 163 repositories and cares about roughly 10** (~26 have Runs at all). That ratio is what makes R7 a real setting rather than a convenience: the difference between the discovered set and the interesting set is more than an order of magnitude, and no heuristic recovers the user's intent.

**Conditional requests are free** (a 304 consumes zero primary rate limit, verified by interleaved measurement), and ETag revalidation is simultaneously cheaper and more correct than any expiry policy. This is why cache TTL is not merely unnecessary but *meaningless*: every value a person could choose is strictly worse than the behaviour they already have.

**DELETE costs 5 points against a ~900 points/min secondary limit (~180/min), while GitHub's prose advises at least one second between writes (~60/min)**. That is a 3× disagreement, both figures documented rather than measured, and on an 18,258-Run Purge that published band is ~100 minutes versus ~5 hours. The governor reaches neither end: it caps at 150/min and floors at 30/min, so a real Purge runs ~2 hours to ~10 hours, and ~155 minutes with the Feed polling ([rate-governor](../rate-governor/requirements.md) R20). Any fixed rate is wrong for somebody: Enterprise Cloud has 3× the primary budget, GitHub Apps scale differently, and a token shared with CI has less headroom than the limit suggests. The adaptive governor observes what the account actually tolerates ([ADR-0007](../../adr/0007-adaptive-delete-throttle.md)). This is the whole argument for R8 and against R13's first two rows.

**A terminal erases the distinction that a third keybinding profile would encode.** On macOS, Cmd never reaches the application (Terminal.app and iTerm2 consume it for tabs and copy/paste), and Ctrl+C is SIGINT everywhere, so a "Windows" profile cannot map Ctrl+C to copy. Strip out what terminals actually deliver and "Mac keys" and "Windows keys" collapse into the same thing: arrows plus Ctrl. Shipping three profiles would cargo-cult a distinction that does not survive contact with the terminal (R5).

**`NO_COLOR` semantics are gh's, verified on gh 2.92.0** (`gh help environment`): "set to **any value** to avoid printing ANSI escape sequences for color output". Any value, not any non-empty value. R15 matches that reading deliberately.

**gh's config-directory precedent is verified**: `GH_CONFIG_DIR`, else `$XDG_CONFIG_HOME/gh`, else `$AppData/GitHub CLI` on Windows, else `$HOME/.config/gh`. R1 follows the XDG limb of it.

**Fine-grained PATs expose no scopes**, so no setting can be validated against the token's actual permissions ahead of time, and the API remains the final authority. A 403 can arrive despite `push: true`. A setting that gated on presumed permissions would be lying.

Historical note: v1's `SKIP`/`GOOD`/`FAIL` labels were derived by string-substituting the **Conclusion** field, and were plain text with no colour. That made them accidentally the accessible choice, and R16 carries the principle forward deliberately rather than by luck.

## Open questions

- **Resolved: the confirm threshold defaults to 50 and its hard maximum is 500.** The test this question set is the one the number meets. 500 sits three orders of magnitude below the reference repository's ~28,700 Runs, so the protection binds on every real Purge: the count gets typed whatever the config says, which is the intended outcome rather than a corner. 50 is roughly where a frozen set stops being reviewable by eye, so below it a `y`/`N` confirms rather than rubber-stamps. R12 carries both numbers, AC8 pins them, and [purge](../purge/requirements.md) R7, R8 and open question 1 own the reasoning.
- **How is the Budget share expressed (R8)?** A percentage, or named intent tiers ("background", "normal", "greedy")? A percentage is precise but invites the same false confidence as a poll interval. Tiers are honest about the resolution actually available. Undecided.
- **Resolved: the Budget share governs polling, and never a Purge.** The question assumed one allowance. There are two. The Budget is a share of the **primary** limit, which is observable and is what background polling spends. A Purge's throughput is bound by the **secondary** limit, a different pool with no counter to read. A Purge therefore does not saturate the Budget, and "spend 25% of my Budget" leaves a ~155-minute Purge at ~155 minutes rather than turning it into a multi-hour one. The knob's label stays honest, because it is a share of the thing polling actually spends. [ADR-0007](../../adr/0007-adaptive-delete-throttle.md) settles this on measurement and always did, so the earlier claim here that it does not address the interaction was wrong. [rate-governor](../rate-governor/requirements.md) R19 and AC14 are corrected to match.
- **Do we honour gh's accessibility configuration?** gh 2.92.0 defines `GH_ACCESSIBLE_COLORS` / `accessible_colors` (4-bit palettes chosen against terminal background), `GH_ACCESSIBLE_PROMPTER` / `accessible_prompter` (prompts compatible with speech synthesis and braille displays), and `GH_SPINNER_DISABLED` / `spinner disabled` (text progress instead of motion). All are verified, and the first two are marked preview. [ADR-0008](../../adr/0008-full-cli-surface-despite-gh-overlap.md) commits us to compatibility with gh's *flags*, and says nothing about gh's *config*. Someone who has already configured these for gh would reasonably expect a gh extension to respect them. The spinner question is not hypothetical: a Purge shows progress for ~155 minutes in the normal case, and for as long as ~10 hours at the throttle's floor. Undecided.
- **Do `CLICOLOR` and `CLICOLOR_FORCE` apply?** gh honours both alongside `NO_COLOR` (`CLICOLOR=0` disables, `CLICOLOR_FORCE` non-zero keeps colour when piped). R15 mandates only `NO_COLOR`. Whether the trio is honoured as a set is undecided.
- **Resolved: there are no axes. It is a flat list of per-event options, living in the Settings menu.** [notifications](../notifications/requirements.md) R4 already ships that list, four events with their defaults, and R11 renders it as options. No events × scope grid and no events × Conclusion grid. A second axis would multiply four answerable questions into cells nobody can answer, which is the exact failure R11's intent test exists to prevent, and R4's events already carry their scope in their own wording ("a Run you triggered", "a repository you can push to"). Which cells the conservative default turns on was the other half of the question, and it is answered the same way: R4's table decides, and it is decided there already. This closes a circular deferral, where R4 shipped the table and this question called the axes undecided.
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
