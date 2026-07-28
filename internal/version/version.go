// Package version reports which build of gh-runs is running. It imports nothing
// internal and depends only on the standard library, so every surface can name a
// version without pulling a subsystem in behind it (ADR-0011).
//
// A tool that irreversibly deletes Runs, Caches and Artifacts has to be able to
// answer "which build did that", and until ADR-0026 nothing here could: there was
// no version symbol in the tree and no --version flag.
//
// The answer comes from the toolchain's own build metadata first and from a
// constant second, and never from a linker flag, because the flag is not ours to
// set: cli/gh-extension-precompile builds every shipped binary with a fixed
// -ldflags="-s -w" and takes no ldflags input, so a -X-injected version would be
// empty in exactly the builds most users install (ADR-0026).
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the release this source tree was last cut from. VerBump rewrites
// this one line on a release, from BUMP_FILES in .verbumprc, and tags the commit
// that carries it in the same run, so the constant and the tag agree by
// construction rather than by discipline.
//
// Its value must equal version.json's, because VerBump builds its search by
// substituting the *previous* version into the pattern and rewrites every line
// carrying the result. Two consequences follow, and both are asserted rather
// than trusted: a value that has drifted from version.json is searched for and
// never found, and a second line carrying the same string would be rewritten
// alongside this one. Neither failure is visible until a release is already cut.
// See TestVersionConstantMatchesTheVersionSource and
// TestVersionConstantIsTheOnlyBumpTarget (ADR-0026).
var Version = "0.0.0-dev"

// String returns the version to show a user.
//
// Build metadata wins when the toolchain recorded one, because it cannot go
// stale the way a constant can. Measured against a sandbox repository, on the
// toolchain in go.mod:
//
//	built with HEAD at tag v2.9.9   ->  v2.9.9
//	built one commit past that tag  ->  v2.9.10-0.20260728014940-fb9ed062706f
//
// So a release binary reports its tag without this package doing anything, and a
// build from an untagged tree reports a pseudo-version that names the commit,
// which is a truer answer than a constant naming the last release.
//
// The constant is the fallback for a build carrying no VCS stamp at all:
// -buildvcs=false, an export with no repository, a vendored tree. It is kept in
// step with the release by VerBump rather than by hand, so the fallback is a
// real version rather than a placeholder. A revision is appended when one was
// recorded but no version was.
func String() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	return describe(bi)
}

// describe is String's decision, separated from reading the build info so a test
// can hand it the three cases that matter (a module version, a stamped revision,
// neither) instead of asserting against whatever toolchain built the test binary.
func describe(bi *debug.BuildInfo) string {
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, modified := vcs(bi)
	if rev == "" {
		return Version
	}
	v := Version + "+" + rev
	if modified {
		v += ".dirty"
	}
	return v
}

// vcs returns the short revision the toolchain stamped into the build and
// whether the tree was dirty when it ran. Both settings are absent when the
// build did not come from a repository, which is why String treats an empty
// revision as "say nothing more" rather than as an error.
func vcs(bi *debug.BuildInfo) (rev string, modified bool) {
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
		case "vcs.modified":
			modified = strings.EqualFold(s.Value, "true")
		}
	}
	return rev, modified
}
