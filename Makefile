# Task entry points. Go has no package.json scripts, and make is what a Go CLI
# reaches for: it is already installed everywhere this builds, and it keeps the
# release flags in one reviewed place instead of in someone's shell history.
#
# The release path is `make bump`. See ADR-0026 for why the version lives where
# it does, and .verbumprc for the flags every bump inherits.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Kept in step with .github/workflows/go-ci.yml, which pins the same version.
GOLANGCI_VERSION := v2.12.2

DESLOPPER_VERSION := $(shell tr -d '[:space:]' < .deslopper-version)
DESLOPPER := uvx --from "git+https://github.com/jv-k/deslopper@$(DESLOPPER_VERSION)" deslopper

.PHONY: help build test vet lint prose check bump bump-release release clean

# Plain .* rather than the .*? the usual copy of this idiom carries: *? is a PCRE
# lazy quantifier with no meaning in POSIX ERE, which is what grep -E and awk parse.
# One "## " per line makes greedy matching correct anyway.
help: ## List the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  \033[1m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into ./gh-runs
	go build -o gh-runs .

test: ## Run the tests under the race detector, as CI does
	go test -race ./...

vet: ## Run go vet
	go vet ./...

# CI pins golangci-lint in .github/workflows/go-ci.yml. This target does not skip
# when the binary is absent: `make check` is what PRE_BUMP_CMD gates a release on,
# and a lint gate that quietly passes when the linter is missing is worse than one
# that fails, because the release it lets through is the one nobody linted.
lint: ## Run golangci-lint, as CI does
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint is not installed, and CI runs $(GOLANGCI_VERSION) on every pull request."; \
		echo "  brew install golangci-lint"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; }
	golangci-lint run

prose: ## Run the pinned deslopper over the Markdown, as CI does
	$(DESLOPPER) lint

check: vet test lint prose ## Everything CI runs on a pull request

# ---------------------------------------------------------------------------
# Release
# ---------------------------------------------------------------------------
#
# VerBump reads the Conventional Commits since the last tag, suggests the next
# SemVer, writes CHANGELOG.md, bumps version.json and the version constant,
# commits, and tags. .verbumprc holds the configuration, including the PRE_BUMP_CMD
# that runs `make check` before anything mutates.
#
# Pass flags through ARGS, and an explicit version through VERSION:
#
#   make bump ARGS=--dry-run          # preview every side-effect, change nothing
#   make bump VERSION=2.0.0           # skip the suggestion
#   make bump ARGS='--preid alpha'    # 2.0.0-alpha.0, a prerelease
#
# A prerelease tag is what makes an alpha safe to publish: `gh extension install`
# resolves through releases/latest, which skips prereleases, so only a tester
# naming the tag with --pin sees it (ADR-0002).

VERBUMP ?= verbump
VERSION ?=
ARGS ?=
VERBUMP_VERSION_ARG := $(if $(VERSION),-v $(VERSION),)

bump: ## Cut a release: changelog, version files, commit, tag. Pushing is prompted.
	$(VERBUMP) $(VERBUMP_VERSION_ARG) $(ARGS)

bump-release: ## Cut a release and push it, which is what triggers the release workflow.
	$(VERBUMP) $(VERBUMP_VERSION_ARG) --push origin $(ARGS)

# `release` is deliberately not wired to VerBump's --release, and this is not an
# oversight to be tidied up later.
#
# Publishing the GitHub release is already owned by .github/workflows/release.yml,
# which fires on the pushed tag and runs cli/gh-extension-precompile. That action
# creates the release and uploads the cross-compiled binaries under the exact
# {GOOS}-{GOARCH} suffixes `gh extension install` matches on (ADR-0002, risk R3).
# Running `verbump --release` as well would have two tools publishing one release
# for one tag, and the failure mode is not a clean error: it is a release that
# exists with the wrong assets, which looks complete and installs nowhere.
#
# So the split is: VerBump owns the version, the changelog and the tag. The
# workflow owns the release and its binaries. `make bump-release` pushes the tag
# that hands over between them.
release: ## Disabled: the tag push triggers .github/workflows/release.yml instead.
	@echo "make release is disabled deliberately."
	@echo
	@echo "  Publishing is owned by .github/workflows/release.yml, which fires on the"
	@echo "  pushed tag and builds the extension binaries with cli/gh-extension-precompile."
	@echo "  Use 'make bump-release' to cut and push the tag that triggers it."
	@echo
	@echo "  See the comment above this target in the Makefile, and ADR-0026."
	@exit 1

clean: ## Remove build output
	rm -f gh-runs
