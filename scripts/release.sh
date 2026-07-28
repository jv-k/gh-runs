#!/usr/bin/env bash
#
# Cut a release: preflight, then hand over to VerBump.
#
# The version comes first and everything after it goes to VerBump untouched, so a
# flag that takes a separate value (--preid alpha, -m "message") passes through
# intact. Parsing the flags here would mean keeping a list of which ones take a
# value in step with a tool that owns that list already.
#
# VerBump does the work: it bumps version.json and the version constant, writes
# CHANGELOG.md, commits and tags, and runs scripts/check.sh first through
# PRE_BUMP_CMD. What this script adds is the preflight VerBump has no opinion
# about, and a statement of what the push sets in motion. See ADR-0026.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ -t 1 ]; then
  bold=$'\033[1m'; dim=$'\033[2m'; reset=$'\033[0m'
else
  bold=''; dim=''; reset=''
fi
if [ -t 2 ]; then
  red=$'\033[31m'; yellow=$'\033[33m'; ereset=$'\033[0m'
else
  red=''; yellow=''; ereset=''
fi

warn() { printf '%s warning:%s %s\n' "$yellow" "$ereset" "$1" >&2; }
die()  { printf '%s error:%s %s\n' "$red" "$ereset" "$1" >&2; exit 1; }

usage() {
  cat <<'EOF'
Cut a release: preflight, then hand over to VerBump.

  scripts/release.sh <version> [--push] [verbump flags...]

  <version>    the SemVer to release, without a leading v (2.0.0, 2.0.0-alpha.0).
               It must come first. Everything after it is passed to VerBump.
  --push       push the commit and tag without prompting
  --dry-run    preview every side-effect and change nothing (a VerBump flag)

Examples:
  scripts/release.sh 2.0.0-alpha.0 --dry-run   preview the alpha
  scripts/release.sh 2.0.0-alpha.0             cut it, prompting before the push
  scripts/release.sh 2.0.0-alpha.0 --push      cut it and push
  scripts/release.sh 2.0.0 -m "the first stable release"

See ADR-0026 and .verbumprc.
EOF
}

# Usage on request goes to stdout and exits 0. Usage after a mistake goes to
# stderr, under a line saying what the mistake was.
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  "") printf '%s error:%s no version given.\n\n' "$red" "$ereset" >&2; usage >&2; exit 1 ;;
esac

version="$1"
shift

# A leading v is the tag's, not the version's. VerBump adds it from TAG_PREFIX,
# and passing it here would tag vv2.0.0.
case "$version" in
  -*) printf '%s error:%s the version must come first, before any flag. Got %s.\n\n' "$red" "$ereset" "$version" >&2
      usage >&2; exit 1 ;;
  v*) die "pass the version without the leading v: '${version#v}', not '$version'. VerBump adds the tag prefix." ;;
esac
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  die "'$version' is not a SemVer version."
fi

push=false
args=()
for arg in "$@"; do
  case "$arg" in
    --push) push=true ;;
    # The script owns the version, and VerBump's flags are last-wins. A second one
    # in the passthrough would ship a version the preflight above never checked for
    # a tag collision.
    -v|--version|-v=*|--version=*)
      die "the version is this script's first argument, not a passthrough flag. Drop '$arg'." ;;
    *) args+=("$arg") ;;
  esac
done

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

command -v verbump >/dev/null || die "verbump is not installed.
    brew install jv-k/tap/verbump
    curl -fsSL https://raw.githubusercontent.com/jv-k/VerBump/main/install.sh | bash"

# Reachable despite the cd above: it fires when the script is copied somewhere
# without the rest of the repository, which is how a stale copy announces itself.
[ -f .verbumprc ] && [ -f go.mod ] || die "this does not look like the repository root"

branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "main" ] || die "releases are cut from main, and this is '$branch'.
  RELEASE_BRANCHES in .verbumprc refuses anything else, and a release from a
  feature branch would tag a tree that never merged."

# Untracked files are deliberately allowed, matching VerBump's own dirty check:
# a stray download or a scratch directory has no bearing on what gets tagged.
if ! git diff --quiet || ! git diff --cached --quiet; then
  die "the working tree has uncommitted changes to tracked files.
  Commit or discard them: whatever is in the tree at this moment is what gets tagged."
fi

git fetch --quiet origin main || die "cannot reach origin"
local_head="$(git rev-parse HEAD)"
remote_head="$(git rev-parse origin/main)"
if [ "$local_head" != "$remote_head" ]; then
  if git merge-base --is-ancestor "$local_head" "$remote_head"; then
    die "main is behind origin/main. Pull first, so the release includes what is already merged."
  elif git merge-base --is-ancestor "$remote_head" "$local_head"; then
    die "main is ahead of origin/main by $(git rev-list --count "$remote_head..$local_head") commit(s).
  Push them first. A tag on an unpushed commit points at history nobody else has."
  else
    die "main and origin/main have diverged. Reconcile them before tagging."
  fi
fi

# This clone is shared with other worktrees and other sessions, so the tag may
# already exist even though this checkout has never seen it.
tag="v$version"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  die "tag $tag already exists locally. Delete it or pick another version."
fi
if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  die "tag $tag already exists on origin. A released version is not re-cut."
fi

# Passing these through is legitimate, and both weaken what the preflight and the
# hook just established, so neither goes by silently.
for arg in ${args[@]+"${args[@]}"}; do
  case "$arg" in
    --no-hooks)    warn "--no-hooks skips scripts/check.sh, so this release is gated by nothing." ;;
    --allow-dirty) warn "--allow-dirty releases a tree that does not match what was checked." ;;
  esac
done

# ---------------------------------------------------------------------------
# What the push sets in motion
# ---------------------------------------------------------------------------

printf '%s==>%s releasing from %s at %s\n' "$bold" "$reset" "$branch" "$(git rev-parse --short HEAD)"
printf '    %stag: %s%s\n' "$dim" "$tag" "$reset"
case "$version" in
  *-*) printf '    %sprerelease: gh extension install ignores it unless a tester passes --pin %s%s\n' "$dim" "$tag" "$reset" ;;
  *)   printf '    %sstable: this becomes the default install for everyone%s\n' "$dim" "$reset" ;;
esac
printf '    %spushing the tag triggers .github/workflows/release.yml, which builds and publishes the binaries%s\n\n' "$dim" "$reset"

set -- -v "$version"
$push && set -- "$@" -p origin

exec verbump "$@" ${args[@]+"${args[@]}"}
