#!/usr/bin/env bash
#
# The release gate: what CI runs on a pull request, run locally before anything
# mutates. This is the single definition of that gate. The Makefile's `check`
# target calls it, and .verbumprc's PRE_BUMP_CMD calls it, so a release cannot be
# gated on a weaker set of checks than a pull request is.
#
# It deliberately does not go through make. VerBump runs PRE_BUMP_CMD on the
# maintainer's machine, and on macOS `make` is a shim through xcrun that a broken
# or mismatched Xcode install takes down with it. A release path that stops
# working because the Command Line Tools moved is a release path that gets run
# with --no-hooks, which is the same as not having a gate.
#
# The steps mirror .github/workflows/go-ci.yml (build, vet, test) and
# .github/workflows/writing-style.yml (deslopper). Neither of those runs on a tag
# push, so this really is the last gate before a release, not a preview of one.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ -t 1 ]; then
  bold=$'\033[1m'; green=$'\033[32m'; reset=$'\033[0m'
else
  bold=''; green=''; reset=''
fi
# warn and die write to fd 2, which is redirected independently of fd 1, so their
# colour is decided by fd 2 rather than by whether stdout happens to be a terminal.
if [ -t 2 ]; then
  red=$'\033[31m'; yellow=$'\033[33m'; ereset=$'\033[0m'
else
  red=''; yellow=''; ereset=''
fi

step() { printf '%s==>%s %s\n' "$bold" "$reset" "$1"; }
warn() { printf '%s warning:%s %s\n' "$yellow" "$ereset" "$1" >&2; }
die()  { printf '%s error:%s %s\n' "$red" "$ereset" "$1" >&2; exit 1; }

# golangci-lint is often installed under the Go bin directory, which is on an
# interactive PATH more often than it is on a hook's.
GOBIN_DIR="$(go env GOBIN)"
[ -n "$GOBIN_DIR" ] || GOBIN_DIR="$(go env GOPATH)/bin"
case ":$PATH:" in
  *":$GOBIN_DIR:"*) ;;
  *) PATH="$GOBIN_DIR:$PATH" ;;
esac
export PATH

# The version CI pins, read from the workflow so this file holds no second copy of
# it. The || true matters: an assignment takes its command substitution's status,
# so under `set -e` a grep that matches nothing would end the script here, and the
# install guidance below would never print. An empty result is handled, a dead
# script is not.
pinned_golangci="$(grep -oE 'version: v[0-9]+\.[0-9]+\.[0-9]+' .github/workflows/go-ci.yml | head -1 | cut -d' ' -f2 || true)"

# cgo_works reports whether the toolchain can build cgo, which the race detector
# needs. It is probed rather than assumed: `go env CGO_ENABLED` reports the
# setting, not whether the C compiler behind it actually runs, and on macOS the
# compiler is an xcrun shim that fails independently of Go.
cgo_works() {
  [ "$(go env CGO_ENABLED)" = "1" ] || return 1
  printf 'int main(void){return 0;}\n' | "$(go env CC)" -x c -o /dev/null - >/dev/null 2>&1
}

step "go build"
go build ./...

step "go vet"
go vet ./...

if cgo_works; then
  step "go test -race"
  go test -race ./...
else
  step "go test (no race detector)"
  go test ./...
  warn "the race detector needs cgo, and this toolchain cannot build it."
  warn "Nothing downstream covers this: go-ci.yml runs on pull requests and pushes to main,"
  warn "and a tag push triggers only release.yml, which builds binaries and runs no tests."
  warn "What this leaves is the race check that ran when these commits merged to main."
  warn "On macOS the usual cause is a broken Xcode or Command Line Tools install: try 'xcrun --version'."
fi

step "golangci-lint"
if ! command -v golangci-lint >/dev/null; then
  die "golangci-lint is not installed, and CI runs ${pinned_golangci:-a pinned version} on every pull request.
    brew install golangci-lint
    CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${pinned_golangci:-latest}
  CGO_ENABLED=0 because building it needs cgo, which a broken Xcode cannot provide."
fi
# A local linter older or newer than the pinned one reports a different set of
# findings, so a clean run here would not mean a clean run in CI.
installed_golangci="$(golangci-lint version --short 2>/dev/null || true)"
if [ -n "$pinned_golangci" ] && [ -n "$installed_golangci" ] &&
   [ "v${installed_golangci#v}" != "$pinned_golangci" ]; then
  warn "golangci-lint here is v${installed_golangci#v} and CI pins ${pinned_golangci}, so the two can disagree."
fi
golangci-lint run

step "deslopper"
deslopper_version="$(tr -d '[:space:]' < .deslopper-version)"
command -v uvx >/dev/null || die "uvx is not installed, and the prose gate is pinned to deslopper ${deslopper_version}.
    brew install uv"
uvx --from "git+https://github.com/jv-k/deslopper@${deslopper_version}" deslopper lint

printf '%s==>%s %sall checks passed%s\n' "$bold" "$reset" "$green" "$reset"
