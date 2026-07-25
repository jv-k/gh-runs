# Configurable Default Tab

This project does not support a configurable default or startup tab. The Feed, which occupies the Runs tab, is the fixed launch view.

## Why this is out of scope

live-run-feed R1 is a merged MUST: "The Feed must be the view presented on launch, with no intervening menu, splash or repository picker." R1 carries two clauses. The first mandates the Feed as the launch view. The second forbids friction (a menu, a splash or a picker) between launch and live data.

A configurable default tab adds no friction, so it does not collide with the second clause. It collides with the first: it would let launch land on Workflows or Storage instead of the Feed.

That first clause is load-bearing for a measured reason. The PRD's cold-start guarantee is Feed-specific: "Cold start paints in under a second when launched inside a repository, with the rest of the Feed filling in behind it." The Feed is the surface engineered for a sub-second first paint. Workflows and Storage carry no equivalent promise, so launching into one of them forfeits the guarantee R1 exists to protect.

R2 reinforces this. The application presents exactly three top-level tabs (Runs, Workflows, Storage), with the Feed occupying Runs, and Settings reachable from any tab rather than as a fourth peer. The tab set is fixed and the Feed is its anchor.

Nothing in the canon backs the alternative. There is no requirement, no ADR, and no config field for a default or startup tab. The config models per-tab repository scope, budget tier, keybinding profile, and three numeric thresholds, none of them a launch target. The settings build (PR #72) shipped seven settings and did not include a default tab. Per the issue, the build and its code review reached this conclusion independently.

## The case that was weighed

The strongest argument for the feature: a user whose whole use of gh-runs is Cache reclamation on the Storage tab, never watching Runs, lands on the Feed at every launch and presses one key to reach Storage. A default-tab setting would save that single keystroke.

It was rejected because it buys one keystroke at the cost of amending a foundational MUST and forfeiting the Feed's sub-second cold start. Grilled in triage on 2026-07-25, no recurring situation surfaced where landing on the Feed costs a user more than that one keystroke. The decision was to keep the Feed as the fixed launch view.

## Reconsidering

This would need an ADR that amends live-run-feed R1 first, carrying a concrete user harm that the current behaviour causes. A preference to skip one keystroke is not that harm. If such a case appears, delete this file and let the issue proceed through normal triage.

## Prior requests

- #74 (decision: a configurable default tab collides with live-run-feed R1/R2)
