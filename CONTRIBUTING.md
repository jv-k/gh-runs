# Contributing to gh-runs

Thanks for looking. This project follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Start here: there is no Go code yet

v2 is a rewrite of a v1 bash script, and it is currently a design, not a program. Nothing is
installable from `main`. That makes the useful contribution today **docs review**, not code.

The design is written down and meant to be argued with:

- [docs/PRD.md](docs/PRD.md) is the product definition
- [docs/features/](docs/features/) holds the requirements, each ending in its own Open questions
- [docs/adr/](docs/adr/) records decisions and the reasoning behind them
- [docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) says what gets built first
- [docs/CONTEXT.md](docs/CONTEXT.md) defines the terms the rest use

`docs/` is the canon. If the code and the docs disagree, that is a bug in one of them. Changing a
decision means writing an ADR, not editing the old one in place. An ADR is a record of what was
decided and why, so it stays even when it is superseded.

The Open questions sections are real. Answering one, or showing that a requirement is wrong, is worth
more than a typo fix.

## Prose gate

Markdown is linted by [deslopper](https://github.com/jv-k/deslopper), and error-tier findings fail the
build. The main rule that catches people: **no em dashes**. Use a colon, a comma, parentheses, or two
sentences.

Run it locally before you push:

```sh
uvx --from "git+https://github.com/jv-k/deslopper@$(cat .deslopper-version)" deslopper lint
```

The version is pinned in `.deslopper-version`. That single pin is read by the CI gate
(`.github/workflows/writing-style.yml`) and by the local editor hook, so they cannot drift. Bump it
there, once.

Warn-tier findings annotate a PR without blocking it. Error-tier findings must be zero.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/), with a scope:

```
docs(prd): narrow the purge confirmation to matched runs
fix(release): stop firing the Go build on v1 tags
```

Common types here are `feat`, `fix`, `docs`, `chore`, `refactor`, `test`. Pick a scope that names the
area, such as `prd`, `adr`, `release`, `tui`, `cli`.

## Pull requests

Keep them focused. One concern per PR reviews faster than five.

Say what changed and why. If it touches a decision recorded in an ADR, link the ADR. If it changes a
decision, add a new one.

## Reporting bugs and asking for features

Use the [issue templates](https://github.com/jv-k/gh-runs/issues/new/choose). For a bug, the install
channel and how your token resolves are the two answers that matter most, so please fill them in.

## Security

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).
