package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// repoRoot is this package's distance from the tree root. The two release files
// these tests reconcile, .verbumprc and version.json, live there, and a test is
// the only thing that can hold them to the source: nothing else fails when a
// release config points at a line that no longer exists.
const repoRoot = "../.."

func TestDescribePrefersTheRecordedModuleVersion(t *testing.T) {
	// go install github.com/jv-k/gh-runs/v2@v2.0.0 records this, and it cannot go
	// stale the way the constant can, so it wins.
	bi := &debug.BuildInfo{Main: debug.Module{Version: "v2.0.0"}}
	if got := describe(bi); got != "v2.0.0" {
		t.Errorf("describe() = %q, want %q", got, "v2.0.0")
	}
}

func TestDescribePrefersAPseudoVersionOverTheConstant(t *testing.T) {
	// go install ...@main records a pseudo-version. The constant still names the
	// last release, which on a tree that has moved past it is the wrong answer.
	const pseudo = "v2.0.1-0.20260728005635-e0286075b241"
	bi := &debug.BuildInfo{
		Main:     debug.Module{Version: pseudo},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "e0286075b241b7c6ac552d0cf2c78dcad3a392c1"}},
	}
	if got := describe(bi); got != pseudo {
		t.Errorf("describe() = %q, want %q", got, pseudo)
	}
}

func TestDescribeFallsBackToTheConstantWithTheRevision(t *testing.T) {
	// A stamped revision with no version: what a build with the VCS present but
	// the module version unresolved leaves behind. The constant names the release
	// and the revision names the commit, which is as close as this case gets.
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "e0286075b241b7c6ac552d0cf2c78dcad3a392c1"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	want := Version + "+e028607"
	if got := describe(bi); got != want {
		t.Errorf("describe() = %q, want %q", got, want)
	}
}

func TestDescribeMarksADirtyTree(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "e0286075b241b7c6ac552d0cf2c78dcad3a392c1"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	want := Version + "+e028607.dirty"
	if got := describe(bi); got != want {
		t.Errorf("describe() = %q, want %q", got, want)
	}
}

func TestDescribeFallsBackToTheConstantAlone(t *testing.T) {
	// No module version and no stamp, which is what a build with -buildvcs=false
	// leaves behind. There is nothing truer to say than the constant.
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := describe(bi); got != Version {
		t.Errorf("describe() = %q, want %q", got, Version)
	}
}

// TestVersionConstantMatchesTheVersionSource holds the invariant the whole
// release path rests on. VerBump builds its search string by substituting the
// *previous* version into the {{version}} pattern, and reads that previous
// version from version.json. If the two ever disagree at rest, the search finds
// nothing, the bump is reported as a non-fatal error, and the release ships a
// binary reporting the version before last. Nothing else in the repository
// fails when that happens.
func TestVersionConstantMatchesTheVersionSource(t *testing.T) {
	if src := sourceVersion(t); src != Version {
		t.Errorf("version.json has %q but the Version constant is %q; a release would search "+
			"internal/version/version.go for a line carrying %q and find none", src, Version, src)
	}
}

// TestVersionConstantIsTheOnlyBumpTarget models what lib/textbump.sh does:
// every line containing the literal search is rewritten, and every occurrence
// within those lines. One matching line is the only safe number. Two would let
// a release rewrite a line nobody meant to bump, and none would leave the
// constant behind, and neither failure is visible until the release is cut.
func TestVersionConstantIsTheOnlyBumpTarget(t *testing.T) {
	current := sourceVersion(t)

	for _, spec := range bumpFiles(t) {
		file, pattern, ok := strings.Cut(spec, ":")
		if !ok {
			t.Fatalf("BUMP_FILES entry %q is not file:pattern", spec)
		}
		if !strings.Contains(pattern, "{{version}}") {
			// A spec without the placeholder is a structured target (JSON, TOML,
			// YAML), which this test has nothing to say about.
			continue
		}
		// The search VerBump will actually run, built exactly as it builds it.
		search := strings.ReplaceAll(pattern, "{{version}}", current)

		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf(".verbumprc names %s, which cannot be read: %v", file, err)
		}

		var lines, occurrences int
		for _, line := range strings.Split(string(data), "\n") {
			if n := strings.Count(line, search); n > 0 {
				lines++
				occurrences += n
			}
		}
		if lines != 1 || occurrences != 1 {
			t.Errorf("searching %s for %q matched %d line(s) and %d occurrence(s), want 1 and 1",
				file, search, lines, occurrences)
		}
	}
}

// sourceVersion returns the version VerBump reads from SOURCE_FILE.
func sourceVersion(t *testing.T) string {
	t.Helper()

	source, ok := assignment(verbumprc(t), "SOURCE_FILE")
	if !ok {
		t.Fatal(".verbumprc declares no SOURCE_FILE, so the previous version would come from " +
			"the latest tag, which on this repository is still v1.0.7")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(source)))
	if err != nil {
		t.Fatalf(".verbumprc names %s as SOURCE_FILE, which cannot be read: %v", source, err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", source, err)
	}
	if doc.Version == "" {
		t.Fatalf("%s carries no top-level version", source)
	}
	return doc.Version
}

// bumpFiles returns the BUMP_FILES entries declared in .verbumprc. The tests
// read the release config rather than restating it, because a copy here would
// keep passing after the config it claims to check had changed.
func bumpFiles(t *testing.T) []string {
	t.Helper()

	raw, ok := assignment(verbumprc(t), "BUMP_FILES")
	if !ok {
		t.Fatal(".verbumprc declares no BUMP_FILES, so no version line is bumped on a release")
	}

	var specs []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			specs = append(specs, line)
		}
	}
	if len(specs) == 0 {
		t.Fatal("BUMP_FILES in .verbumprc is empty")
	}
	return specs
}

func verbumprc(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot, ".verbumprc"))
	if err != nil {
		t.Fatalf("reading .verbumprc: %v", err)
	}
	return string(data)
}

// assignment pulls a shell-style KEY="value" out of the rc file, unescaping the
// backslash-quoted double quotes the pattern needs. It is deliberately small:
// the file is ours, its shape is fixed, and a real shell parser here would be
// more machinery than the two keys these tests read.
func assignment(rc, key string) (string, bool) {
	_, after, ok := strings.Cut(rc, "\n"+key+"=\"")
	if !ok {
		if !strings.HasPrefix(rc, key+"=\"") {
			return "", false
		}
		after = strings.TrimPrefix(rc, key+"=\"")
	}
	var value strings.Builder
	for i := 0; i < len(after); i++ {
		switch {
		case after[i] == '\\' && i+1 < len(after) && after[i+1] == '"':
			value.WriteByte('"')
			i++
		case after[i] == '"':
			return value.String(), true
		default:
			value.WriteByte(after[i])
		}
	}
	return "", false
}
