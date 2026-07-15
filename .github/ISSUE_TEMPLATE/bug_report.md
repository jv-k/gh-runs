---
name: Bug report
about: Report a defect in the gh-runs CLI or TUI
title: ''
labels: ''
assignees: ''

---

**Describe the bug**
What happened, and what you expected instead.

**To reproduce**
The exact command you ran, with its flags. Redact repository names or tokens if you need to.

1. Ran `gh runs ...`
2. ...
3. Saw ...

**Install channel and version**
Tick one channel and give the version it reports.

- [ ] `gh` extension: `gh extension list`
- [ ] Homebrew: `brew list --versions gh-runs`
- [ ] `go install`: `gh-runs version`

Version:

**Token resolution**
Answer both parts. Most reports here turn out to be token resolution, not the feature you were using.

- Is the `gh` CLI installed? Paste `gh --version`, or write "not installed".
- Where does your token come from: `GH_TOKEN` or `GITHUB_TOKEN` in the environment, or `gh auth login`?

gh-runs reads a token stored by `gh auth login` only by asking the `gh` binary for it. On a machine without `gh`, that token is unreachable and you must set `GH_TOKEN`. Never paste the token itself.

**Environment**
- OS and architecture (e.g. macOS 15 arm64, Ubuntu 24.04 amd64, Windows 11 amd64):
- Terminal emulator, and whether you run under tmux or screen (e.g. iTerm2, Ghostty, Windows Terminal):

**Output**
Paste terminal output in a fenced code block. For a rendering or layout bug, attach a screenshot.

**Additional context**
Anything else worth knowing.
