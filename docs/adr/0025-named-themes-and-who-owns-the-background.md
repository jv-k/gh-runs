# Named themes: borrowed identities, adapted to the floor, on a background the tool paints

[settings](../features/settings/requirements.md) R6 fixed the theme set at `auto`, `dark` and `light`, and refused more in the words "the set is small and fixed rather than a gallery". Issue [#152](https://github.com/jv-k/gh-runs/issues/152) asked for three or four named themes. That is an amendment to R6 rather than a feature request, and the amendment has to say what replaced those words as the line that stops the set growing without bound.

Underneath the ask is a larger question, and answering it is what this ADR mostly does. **Does a named theme carry its own background, and does the tool paint it?** [ADR-0024](./0024-the-contrast-floor-and-its-reference-backgrounds.md) had to name two reference backgrounds and call them an assumption, because a contrast floor cannot be a guarantee about a background the tool neither sets nor looks at. A theme that owns its background removes the assumption for every member that has one. A theme that is a foreground set inheriting the terminal's background multiplies the assumption by the number of members instead.

Eight decisions follow. The code is 2.1 and no Go file changes on this decision.

## A named theme is a borrowed identity, and the identity includes the background

A member such as `nord` or `dracula` names a theme that exists outside this tool and carries its own background. It is not an in-house foreground variant with a decorative name, and it is not a name attached to a background the tool never sets.

**In-house names were considered and rejected.** A set called something like `calm`, `vivid` and `high-contrast` avoids every fidelity question below, because there is nothing to be unfaithful to. It also answers a request nobody made. What an operator wants from a theme setting is the thing their editor and their terminal already are, and a name they have to learn is a name that carries no expectation to satisfy.

**A borrowed name over an unpainted background was considered and rejected**, and it is the option this whole decision exists to refuse. Nord's foregrounds are chosen for `#2e3440`. Painting them onto a solarized-light terminal is not Nord, it is a set of muted blues on cream, and the name would be false in the one way an operator could see.

## The background is painted by SGR cell fill, and never by OSC 11

Every style carries the background, and the root fills the viewport to its full width and height, so every cell on screen is one the tool painted.

**`tea.View.BackgroundColor` was considered and rejected**, and it is the obvious mechanism. It emits OSC 11, which recolours the terminal window rather than a region. Three things follow, and each is worse than the last. It leaks outside the tool's own frame, so the operator's scrollback and their shell prompt change colour too. It is restored on a clean teardown and not on a SIGKILL, so a crash leaves the terminal wearing a colour the tool chose and no longer owns. And it is invisible to every test seam this repository has: goldens capture `View().Content`, and AC15 asserts over palette values, so neither would see an OSC 11 sequence at all.

A cell-filled background is an SGR 48 sequence. That puts it inside the assertion AC9 already makes for `NO_COLOR`, which forbids "no SGR 30 to 49, no 38/48 extended forms", so the painted background is covered by an existing acceptance criterion and needs no new clause to keep `NO_COLOR` honest.

## auto is the only member that does not paint

`auto` inherits the terminal background, resolves dark or light from it exactly as today, and remains the default. Every other member paints, `dark` and `light` included.

**Painting only the named members was considered and rejected.** It reads as the conservative choice and it produces two classes of explicit member: `dark`, which asks the terminal for a background it then does not use, and `nord`, which sets one. The rule stops being a sentence and becomes a table. One sentence is what a person can hold: **auto is the only member that does not paint.**

**`dark` and `light` paint [ADR-0024](./0024-the-contrast-floor-and-its-reference-backgrounds.md)'s two reference backgrounds, `#2d2a2e` and `#faf4f2`.** Every member that paints has to name a background, and for these two the value already exists: it is the background their roles were measured against when the floor was set. Taking anything else would invalidate every figure ADR-0024 recorded for no gain. So the arithmetic does not move, and what changes is its standing: those two numbers stop being an assumption about the operator's terminal and become a property of a background the tool sets.

The cost is real and is accepted. An operator who sets `dark` today gets their own terminal background and after 2.1 gets ours. That is a visible change to a setting they already chose, and R6 names 2.1 as when it happens rather than letting it arrive unannounced.

## One key, and one member names one painted background

`theme` gains members. It does not gain a sibling key.

**A second key was considered and rejected.** The argument for it is real: `auto` is a resolution strategy and `nord` is a name, so they are different kinds of thing sharing one key's value space. The argument against it is fatal. With two keys, `theme: auto` beside a named family means unpainted borrowed foregrounds on an arbitrary background, which is exactly the failure the first decision above refuses. A shape whose valid combinations include the one thing the design exists to prevent is the wrong shape, whatever its taxonomy.

One member naming one painted background is also what keeps the rest of the canon simple. R22 is one clause per member rather than a cross product of families and variants, AC15's generated golden is one block per member, and R14's diagnostic lists what is typeable. A theme family with a dark and a light variant ships as two members.

## A member takes its theme's background exactly and its foregrounds approximately

A member takes its theme's exact background and its hue identity, and every role is then moved until it clears 4.5:1 against that background. The result is recognisably Nord rather than byte-faithful Nord.

**Exempting named themes from R22 was considered and rejected**, on ADR-0024's own reasoning. That ADR refused a grandfather clause for a single existing value because it would publish a floor beside a documented precedent for ignoring it. A whole class of exempt members is that precedent at four times the size.

This is not a precaution against a hypothetical. Measured with the same WCAG 2.x relative luminance arithmetic R22 mandates, each theme's own set against its own background:

| Theme | Roles clearing 4.5:1 | Worst role |
|---|---|---|
| Dracula | 7 of 8 | comment `#6272a4` at 3.03:1 |
| Nord | 5 of 8 | comment `#4c566a` at 1.69:1 |
| Gruvbox Dark | 4 of 8 | red `#cc241d` at 2.69:1 |
| Solarized Dark | 4 of 8 | comment `#586e75` at 2.79:1 |
| Solarized Light | 0 of 8 | comment `#93a1a1` at 2.48:1, and its own body foreground `#657b83` at 4.13:1 |

Nord's comment colour at 1.69:1 is worse than the 1.95:1 search match that issue [#93](https://github.com/jv-k/gh-runs/issues/93) was opened over, and Solarized Light's own body foreground does not clear the floor against its own background. Faithful borrowing was never on the table. The only question this ADR had was whether to adapt or to exempt.

**Said in this ADR's own voice, because it will draw reports: `theme: nord` is not byte-identical to Nord.** Somebody will diff our blue against the Nord specification and file it. The answer is here rather than in a maintainer's memory. The background matches their terminal exactly, the hues are Nord's, and the values are moved as far as the floor requires and no further. What is kept is the part an operator sees at a glance and the part a contrast checker measures. What is given up is byte fidelity to a palette three of whose eight roles do not clear the floor against its own background.

## The floor is the bound, and it replaces "small and fixed rather than a gallery"

No theme joins the set unless every one of its roles clears 4.5:1 against the background it paints, and both of its highlights clear 4.5:1 internally and CIE76 ΔE 10 against that same background. The floor is a gate on the way in and not a rule that admits: clearing it makes a theme eligible for the set, and a decision still puts it there. Stated the other way round, as an "if and only if", it would make every conforming theme in existence a member, which is the gallery this bound exists to refuse.

**A count was considered and rejected.** "At most seven members" is a line a reviewer has to hold and an arbitrary one to defend at the eighth. The floor is a line AC15 enforces, so growth is bounded by a test rather than by a sentence somebody has to remember to quote. It also bounds the right thing: the objection to a gallery was never the number of names, it was that a gallery is where quality stops being checked.

## Built-in only, and the registry is what ships in the binary

Themes loadable from the config file are the obvious extension and are out of scope here.

**Config-file themes were considered and deferred.** The floor is currently a build-time property, proved over the whole registry by a test that runs before anything ships. A user-supplied theme makes it a runtime property, which means deciding what the tool does with a theme that fails: refuse it and the setting silently does nothing, accept it and R22 stops being true of the running tool. That is its own decision with its own options, and nothing in this one presumes an answer to it.

## The set at 2.1 is seven members

`auto`, `dark`, `light`, `nord`, `dracula`, `gruvbox-dark`, `solarized-light`.

The light member is deliberate rather than a nod to balance. Solarized Light is the hardest case in the table above, the only one where not a single role clears the floor and the only one whose own body foreground fails against its own background. Shipping it proves the adaptation rule on the case that would otherwise be quietly avoided. Solarized Dark is the member left out, on the grounds that the set already carries three dark members and its light sibling is the one that tests something.

Adding a member later is not a decision this ADR reopens. The bound above governs it.

## Consequences

**ADR-0024's assumption narrows rather than disappearing.** Its two reference backgrounds now bind `auto` alone. For every painted member the floor is exact arithmetic against a background the tool set, so the guarantee is stronger for six of the seven members and unchanged for the seventh. That ADR is amended in place rather than superseded, because its consequences section already said named themes were not decided there and pointed at the amendment this is.

**The palette stops being a pair and becomes a registry.** A colour is modelled today as two values, dark first, resolved against an ambient appearance as a style renders. Under seven members that is a lookup into a member's own set. `Appearance` survives and its scope narrows: it is how `auto` resolves, rather than how every member does.

**Every golden regenerates, and it is a substitution again.** Adding a background to every style site changes the SGR sequences in every frame. ADR-0024 already established that a colour change moves no cell width and is verifiable by substitution rather than by reading frames, and the same holds here. Issue [#151](https://github.com/jv-k/gh-runs/issues/151) ships in 2.0.0 untouched and its goldens regenerate again in 2.1, which is a cost that was already accepted rather than a reason to hold it.

**AC15 walks the registry rather than two appearances**, so adding a member below either floor fails it, and the generated golden carries a figure for every role and every highlight of every member. That is seven members' worth of figures where there were two appearances' worth.

**A painted background defeats a transparent terminal, and that is the cost of decision 2.** An operator running a translucent or image-backed terminal keeps that background under `auto` and loses it under every other member, because a filled cell is opaque by definition. There is no partial version of this: the whole reason cell fill was chosen over OSC 11 is that it paints a region the tool owns, and a region it owns has no transparency to inherit. `auto` remains the default, so the operator who cares keeps it by doing nothing. This is recorded as a consequence rather than a [PRD](../PRD.md) risk row, because it is a known and decided cost of a 2.1 mechanism rather than an unmeasured unknown, and the risk table holds the latter.

**The 2.1 build is carved from this canon when it merges** and is not part of it: the full-viewport fill, a background on every style site, the palette restructured to a registry, roughly sixteen measured values per member, and the goldens regenerated. This ADR changes no Go file.
