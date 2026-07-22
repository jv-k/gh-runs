package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// TestFormatLogLineShape pins R29's line shape and AC19: six tab-separated fields in
// fixed order, an RFC 3339 UTC timestamp from the injected clock, a host-qualified
// repo, and an empty reason on a deletion. The expected bytes are a hand-written
// literal, an independent source of truth, not a re-derivation of the code.
func TestFormatLogLineShape(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	rec := logRecord{
		repo:    domain.RepoID{Host: "github.com", Owner: "cli", Name: "cli"},
		kind:    KindRun,
		id:      4675883901,
		outcome: outcomeDeleted,
		reason:  "",
	}
	got := formatLogLine(now, rec)
	want := "2026-07-22T12:00:00Z\tgithub.com/cli/cli\trun\t4675883901\tdeleted\t\n"
	if got != want {
		t.Errorf("formatLogLine =\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(strings.TrimSuffix(got, "\n"), "\t"); n != 5 {
		t.Errorf("line has %d tabs, want 5 (six fields, R29)", n)
	}
}

// TestFormatLogLineEscapesReason pins R29's escaping: a reason carrying a tab and a
// newline stays one line with six fields, so grep and cut read it whole.
func TestFormatLogLineEscapesReason(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	rec := logRecord{
		repo:    domain.RepoID{Host: "github.com", Owner: "o", Name: "r"},
		kind:    KindRun,
		id:      7,
		outcome: outcomeFailed,
		reason:  "bad\ttoken\nline two",
	}
	got := formatLogLine(now, rec)
	if strings.Count(got, "\n") != 1 {
		t.Errorf("escaped line carries %d newlines, want 1 (the terminator only): %q", strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, `bad\ttoken\nline two`) {
		t.Errorf("reason not escaped: %q", got)
	}
	if n := strings.Count(strings.TrimSuffix(got, "\n"), "\t"); n != 5 {
		t.Errorf("escaped line has %d tabs, want 5 (a raw tab in the reason would add a seventh field)", n)
	}
}

// TestOpenDeletionLogRefusesUnwritablePath pins AC20's precondition: a log that
// cannot be opened makes openDeletionLog error and name the path, so Execute refuses
// to start and issues zero DELETEs (R29). The parent is a regular file, so creating
// the log's directory under it fails on every platform.
func TestOpenDeletionLogRefusesUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	_, err := openDeletionLog(filepath.Join(blocker, "gh-runs", "deletions.log"), clockwork.NewFakeClock())
	if err == nil {
		t.Fatal("openDeletionLog admitted a path under a regular file; it must prove writability before the first DELETE (R29, AC20)")
	}
	if !strings.Contains(err.Error(), "deletion log") {
		t.Errorf("error %q does not name the log (R29, AC20)", err)
	}
}

// TestLogRotationBounded pins AC21: the log rotates when it crosses the size bound,
// and at most logGenerations rotated files exist beside the active log, whatever the
// deletion count. It writes a few very large lines to cross 8 MB with little I/O,
// then rotates past the generation count and asserts the cap holds.
func TestLogRotationBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deletions.log")
	l, err := openDeletionLog(path, clockwork.NewFakeClock())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = l.close() })

	// A ~4 MB reason means three writes cross the 8 MB bound and force one rotation,
	// without writing tens of megabytes.
	big := strings.Repeat("x", 4<<20)
	rec := func() logRecord {
		return logRecord{repo: domain.RepoID{Host: "github.com", Owner: "o", Name: "r"}, kind: KindRun, id: 1, outcome: outcomeDeleted, reason: big}
	}
	for i := 0; i < 3; i++ {
		if err := l.write(rec()); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected a rotated .1 after crossing 8 MB (AC21): %v", err)
	}

	// Rotate past the generation count. Renames are cheap, so this proves the cap
	// without writing gigabytes: no more than logGenerations rotated files survive.
	for i := 0; i < logGenerations+3; i++ {
		if err := l.rotate(); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}
	rotated := 0
	for i := 1; i <= logGenerations+3; i++ {
		if _, err := os.Stat(path + "." + itoa(i)); err == nil {
			rotated++
		}
	}
	if rotated > logGenerations {
		t.Errorf("%d rotated generations exist, want at most %d (AC21: the footprint is bounded)", rotated, logGenerations)
	}
}

func itoa(i int) string {
	return string(rune('0' + i))
}
