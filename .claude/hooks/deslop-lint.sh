#!/usr/bin/env bash
# De-slop lint hook. Runs deslopper (github.com/jv-k/deslopper) over any Markdown
# file Claude writes or edits, and blocks only on a real error-tier finding.
#
# Reads the PostToolUse hook payload as JSON on stdin.
#
# Exit codes:
#   0  not Markdown, excluded by config, or clean
#   1  the linter could not run. Reported to the user, does not block.
#   2  error-tier tells found. Blocks, and the findings are fed back to Claude.
#
# The 1-vs-2 split is the point. An earlier version reported every non-zero exit
# as "deslopper found machine-writing tells", so an offline machine or a dead SHA
# blocked the edit with a message asserting the opposite of the truth.

set -u

root="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

# Fail loudly, not open. Without this the pipeline exited 0 when jq was absent,
# silently disabling the gate and looking identical to "not a Markdown file".
if ! command -v jq >/dev/null 2>&1; then
  printf 'deslop hook: jq is not on PATH, so the lint did not run.\n' >&2
  exit 1
fi

f=$(jq -r '.tool_input.file_path // empty')
[ -n "$f" ] || exit 0

case "$f" in
  *.md | *.markdown) ;;
  *) exit 0 ;;
esac

# Honour deslopper.config.json's exclude list. Passing an explicit path to
# deslopper bypasses it, which would otherwise make this hook stricter than the
# CI gate, which lints via git ls-files and does respect the excludes.
cfg="$root/deslopper.config.json"
if [ -f "$cfg" ] && jq -e --arg b "$(basename "$f")" \
  '(.files.exclude // []) | index($b)' "$cfg" >/dev/null 2>&1; then
  exit 0
fi

ver="$root/.deslopper-version"
if [ ! -f "$ver" ]; then
  printf 'deslop hook: %s is missing, so the lint did not run.\n' "$ver" >&2
  exit 1
fi
pin=$(tr -d '[:space:]' <"$ver")

out=$(uvx --from "git+https://github.com/jv-k/deslopper@${pin}" deslopper lint "$f" 2>&1)
code=$?

# deslopper always prints an "N error(s), M warning(s)" summary. If that is
# absent the tool never ran, and whatever went wrong is not a lint finding.
case "$out" in
*"error(s)"*) ;;
*)
  printf 'deslop hook: could not run deslopper (exit %s). The lint did NOT run and this is NOT a lint finding.\n%s\n' \
    "$code" "$out" >&2
  exit 1
  ;;
esac

if [ "$code" -eq 0 ]; then
  exit 0
fi

printf 'deslopper found machine-writing tells in %s. Fix them: the error tier blocks CI.\n%s\n' "$f" "$out" >&2
exit 2
