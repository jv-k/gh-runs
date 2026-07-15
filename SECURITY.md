# Security policy

## Reporting a vulnerability

Report privately through GitHub, not in a public issue: open the repository's
[Security tab](https://github.com/jv-k/gh-runs/security/advisories/new) and use "Report a vulnerability".
That opens a private advisory only you and the maintainer can read.

If that form is unavailable, email git@jvk.to with "gh-runs security" in the subject.

Expect a first reply within 7 days. This is a single-maintainer project, so a fix may take longer than
the acknowledgement. You will be credited in the advisory unless you ask not to be.

Never include a real token in a report. Redact it.

## Supported versions

| Version | Supported |
| --- | --- |
| v2 (prerelease) | Yes |
| v1 (`delete-workflow-runs`) | No, unmaintained |

v2 is in development and nothing is installable yet. Once v2 ships, fixes land on the latest v2 release.

## What is in scope

Two properties of this tool make a bug worth reporting privately.

It handles a GitHub token. Anything that leaks `GH_TOKEN`, a token read via the `gh` CLI, or a token
belonging to another host into logs, crash output, terminal titles, telemetry, or a request to the
wrong host is in scope. So is any path that sends a token to a host it was not scoped to.

It deletes irreversibly. GitHub does not restore deleted workflow runs. Anything that causes a delete
the user did not confirm is in scope, including a selection that does not match what the confirmation
showed, a filter that matches more than it displays, and a repository targeted that the user did not
choose.

## What is out of scope

- Vulnerabilities in the `gh` CLI, GitHub's API, or Go dependencies. Report those upstream. If a
  dependency advisory affects gh-runs specifically, tell us how.
- A token exposed by the reporter's own shell history, CI logs, or screen recording.
- Missing hardening with no demonstrated impact.
