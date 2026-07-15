# gh-runs

**A live GitHub Actions dashboard across your repositories, where deletion is one operation.**

## Status

v2 is in development, and nothing is installable from this branch yet.

The shape is written down and the constraints are measured. [docs/PRD.md](docs/PRD.md) is the product definition, [docs/adr/](docs/adr/) records the decisions behind it, [docs/BUILD-ORDER.md](docs/BUILD-ORDER.md) says what gets built first, and [docs/CONTEXT.md](docs/CONTEXT.md) defines the terms they all use.

Plenty is still open. The feature requirements under [docs/features/](docs/features/) mark what is undecided or unmeasured in their own Open questions sections, and those marks are honest rather than decorative.

When it ships, it will install as a `gh` extension, through Homebrew, or with `go install`.

## Looking for v1?

v1 was `delete-workflow-runs`: a bash script that piped a filtered run list into `fzf --multi` and deleted whatever you selected. v2 keeps that capability and subordinates it to the live feed.

- **Source:** the [`v1.0.7`](https://github.com/jv-k/gh-runs/tree/v1.0.7) tag
- **npm:** `delete-workflow-runs@1.0.7`, still installable and no longer maintained

![Terminal recording of the v1 script selecting workflow runs in fzf and deleting them](demo.gif)

## Contributing

I'd love you to contribute to `gh-runs`, [pull requests](https://github.com/jv-k/gh-runs/issues/new/choose) are welcome for submitting issues and bugs!

## Support

If you find this useful, see [DONATE.md](DONATE.md).

## License

The scripts and documentation in this project are released under the [MIT license](https://github.com/jv-k/gh-runs/blob/main/LICENSE).
