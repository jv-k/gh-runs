# The contrast floor: WCAG 4.5:1 against two named reference backgrounds, binding both appearances

[settings](../features/settings/requirements.md) R6 gives the tool two colour sets, and [ADR-0011](./0011-package-layout-and-dependency-direction.md) puts them in `palette`. Nothing said how legible a role has to be. Issue [#93](https://github.com/jv-k/gh-runs/issues/93) reported four light values below WCAG AA and asked the question this ADR answers: whether a contrast floor is a stated requirement that binds every role added later, or a convention recorded in a comment.

It is a requirement. [settings](../features/settings/requirements.md) R22 states it and AC15 tests it. This ADR carries the four decisions R22 could not state without becoming an essay: what the floor is measured against, which standard computes it, what happens to the dark appearance, and what a role even is once a background is involved.

## The floor is a promise about a background we do not control

The tool paints foregrounds onto the terminal's own background. It asks for that background at startup (`tea.RequestBackgroundColor`) and then discards everything but `IsDark()`, whose split is HSL lightness below 0.5 in the pinned `ultraviolet`. So the light set is painted on any background from white down to mid grey, and no dark foreground clears 4.5:1 on `#808080`. A floor that claimed to hold for every background the light set is selected for would be false, and provably so.

R22 therefore names two **reference backgrounds** and calls them what they are, an assumption:

| Appearance | Reference background | What it is |
|---|---|---|
| Dark | `#2d2a2e` | Monokai Pro |
| Light | `#faf4f2` | Monokai Pro Light |

Both are real terminal themes rather than the ideal `#000000` and `#ffffff`. That choice is the whole point. Contrast between a dark foreground and a light background rises monotonically as the background lightens, so a role clearing the floor on `#faf4f2` clears it on solarized-light, on `#fafafa`, on `#f6f8fa` and on white by construction. Stating the floor on white would have been the most quotable claim and the least useful one, because every light terminal theme in use is dimmer than white. The reference is the worst case we commit to, not the best case we can cite.

Measuring against a realistic dark background rather than `#000000` is the same argument run the other way, and it is what put the dark appearance in scope. `Muted` at `#8a8a8a` is 6.08:1 on black and 4.10:1 on `#2d2a2e`.

## WCAG 2.x relative luminance, at 4.5:1

The floor is 4.5:1, WCAG 2.x AA for normal text, computed by the relative-luminance formula. Every role the tool paints is normal-size terminal text, so the 3:1 large-text allowance never applies to one.

**APCA was considered and rejected.** WCAG 2.x is known to mis-rate light text on dark backgrounds, which is exactly half of this palette, and APCA models it better. It is also a WCAG 3 draft with no ratified thresholds, no Go implementation to lean on, and a number a reader cannot check against any tool they already have. Every figure in this canon would become uncitable. WCAG 2.x is fifteen lines of arithmetic, agrees with every contrast checker in existence, and is the number issue #93 was written in.

**AAA at 7:1 was considered and rejected.** On `#2d2a2e` or `#faf4f2` a 7:1 floor forces most roles to near-black or near-white, which collapses the hue distinctions the roles exist to draw.

Values that change under this decision are picked at 5.0 or better where the hue allows. The margin is practice rather than the floor, so a later adjustment to a reference background does not silently drop a role below the line. Two existing light values sit between the floor and the margin, `Muted` at 4.82 and `Danger` at 4.96. They clear the floor and are left alone, and this paragraph is the record of that rather than an oversight.

## The dark appearance moves, and the goldens are not the reason to refuse

Exactly one dark role falls below the floor on `#2d2a2e`: `Muted` at 4.10, next closest `Danger` at 4.76. `Muted` is secondary text, so it appears in most of the tree's goldens. Counted at implementation: 66 of 68 carry it, and 61 take the change as a pure substitution, the other 5 being the `logview` goldens that also carry the `Cursor` change below. The figures first written here, 65 of 67, were estimated rather than counted and did not survive it.

That count was the stated reason to expect the dark set to be frozen, and it does not survive contact. A colour change never moves a cell width, so regenerating those files is a **pure byte substitution** of one SGR triple, verifiable with one `sed` and one `diff` rather than by reading the frames. The invariant is recorded in the `palette` package's own comment, that the dark values must not move because every golden in the tree is taken under them. It was protecting the goldens' convenience rather than any property a user could observe, and the implementation carved from this decision amends it.

**A grandfather clause was considered and rejected.** Exempting the single existing violator would have kept every golden byte-identical at the cost of publishing a floor alongside a documented precedent for ignoring it. That ambiguity is what the requirement exists to remove.

## A Highlight is a pair, because the worst contrast in the tool is a composed one

`logview` applies a background over whatever foreground the line already carried. Six foreground roles can land on the cursor line and on a search match, and the results are far worse than anything measured against a terminal background:

| Foreground | On the dark search match `#5f5f00` |
|---|---|
| `Muted` | 1.95 |
| `Danger` | 2.26 |
| `Passed` | 2.49 |
| `Accent` | 2.75 |
| `Warn` | 2.84 |
| `Requested` | 3.63 |

Issue #93 pointed at roles against a terminal background, the case the tool controls least. The worst contrast it paints is this one, the case it controls completely. Both colours are ours, so the number is exact rather than an assumption about somebody's terminal.

So the palette carries two kinds of thing, and R22 binds both:

- A **Role** is a foreground in two appearances. It is measured against the reference background.
- A **Highlight** is a foreground and a background **together**, in two appearances. Applying the background applies the foreground with it, which collapses the cross product to one exactly known pair.

A highlighted line therefore loses its severity colour for as long as it is highlighted. That costs nothing the canon requires: R16 already forbids meaning riding on colour alone, and AC10 tests it. The alternative, constraining twelve foreground-on-background pairs at once, has no assignment of values that satisfies all of them while leaving the roles recognisable.

`CursorBackground` and `MatchBackground` are renamed `Cursor` and `Match`, because each names a highlight rather than a colour.

## A background needs a second property, and a luminance ratio is the wrong tool for it

A Highlight's background must differ from the terminal background or the highlight does not exist. That is not the contrast floor, and applying the floor to it gives an answer that contradicts the eye:

| Highlight background | WCAG ratio to its reference | CIE76 ΔE to its reference |
|---|---|---|
| `Match` light `#ffff87` | 1.03 | 58.0 |
| `Match` dark `#5f5f00` | 2.11 | 54.1 |
| `Cursor` light `#d0d0d0` | 1.42 | 13.3 |
| `Cursor` dark `#303030` | 1.07 | 3.9 |

A vivid yellow on off-white is 1.03 by luminance and unmissable in fact. A luminance floor would have forced the search highlight dark, replacing a visible highlight with a compliant one, which is the failure this whole decision exists to avoid. R22 states the second property in **CIE76 ΔE, with a floor of 10**, comfortably above the roughly 2.3 just-noticeable difference and still loose enough for a subtle cursor line. CIE76 was chosen over CIEDE2000 for the same reason WCAG 2.x was chosen over APCA: it is short, it is checkable, and its extra accuracy would buy nothing at a threshold this coarse.

The measurement found a defect nobody had reported. `Cursor` dark at `#303030` is ΔE 3.9 from `#2d2a2e`, so on the terminal theme this ADR adopts as its dark reference, the cursor-line highlight is invisible. It moves to `#444444`, ΔE 11.8, which puts it beside the light side's 13.3.

## Consequences

**Six values move, and no others.** Light `Failing` `#d75f00` to `#a34700`, light `Warn` `#af5f00` to `#8c4a00`, light `Positive` `#008700` to `#007a00`, light `Success` `#00875f` to `#00785a`, dark `Muted` `#8a8a8a` to `#9e9e9e`, and the `Cursor` dark background `#303030` to `#444444`. The paired Highlight foregrounds are `#ffffff` on both dark backgrounds and `#1c1c1c` on both light ones.

**The xterm-256 cube is left behind, and that is a real cost.** Every value the palette shipped with came from the cube. At the dark end the cube has too few distinct saturated colours to give fourteen roles distinct values above the floor: darkening `Failing` and `Warn` inside it lands both on `#875f00`, which is `Attention`, and `Positive` and `Success` land on `Passed` and `Requested`. The replacements are free hex. On a 256-colour terminal `colorprofile` quantizes them back toward those same cube entries, so that terminal sees a compression this ADR cannot prevent. It is accepted, because a truecolour terminal is the common case and the cube offers no assignment that avoids it.

**Darkening compresses the palette, and the orange band takes it.** `Warn` and `Failing` fall from ΔE 19 apart to 12. That is still looser than the ΔE 10 the set already ships between `Attention` and `Queued`, and R16 means no meaning rides on the difference.

**The floor is stated per role and reference background rather than per appearance**, so the named themes issue [#93](https://github.com/jv-k/gh-runs/issues/93) raised in passing can add reference backgrounds later without reopening R22. Named themes are not decided here. R6 fixes the theme set at auto, dark and light and calls it "small and fixed rather than a gallery", so admitting more is an amendment to R6 and a decision of its own.

**The reference backgrounds become exported constants in `palette`**, so R22's numbers exist in code rather than only in prose, and the property test reads the same values the canon names.

**The measured ratios are generated, never typed.** AC15's golden holds the ratio for every role and every pair. A comment beside a value can drift from it, and the recorded numbers are the thing a future contributor is asked to beat.
