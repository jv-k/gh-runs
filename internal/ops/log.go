package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// The deletion log's bounds (purge R29). Neither is settable, by flag, config key
// or environment variable: answering "how many megabytes of deletion log" needs the
// reference scale and the bytes-per-line figure, which is settings R13's test for
// mechanism. A reference-scale Purge writes ~1.8 MB, so 8 MB carries four Purges and
// none rotates in its own middle; four generations beside the active log is ~40 MB
// and roughly twenty Purges of history.
const (
	logRotateSize  = 8 << 20 // 8 MB
	logGenerations = 4        // rotated generations kept beside the active log
)

// logOutcome is R29's closed outcome vocabulary, a column of its own so a count is a
// grep and not a regex. deleted and gone are held apart because "I deleted it" and
// "it was already gone" are different facts (R29, R18). skipped and failed carry
// R20's reason in the sixth field.
type logOutcome string

const (
	outcomeDeleted logOutcome = "deleted"
	outcomeGone    logOutcome = "gone"
	outcomeSkipped logOutcome = "skipped"
	outcomeFailed  logOutcome = "failed"
)

// logRecord is one deletion attempt's six R29 fields before formatting. The
// timestamp is taken at write time from the injected clock, never the wall clock.
type logRecord struct {
	repo    domain.RepoID
	kind    Kind
	id      int64
	outcome logOutcome
	reason  string
}

// deletionLog is the append-only record of every deletion (R29). It is write-only:
// no method reads it back, parses it, or offers to resume from it, so R24 stays the
// only resume and the file carries no schema. It rotates at logRotateSize keeping
// logGenerations generations. main.go supplies the path so ops owns no directory
// policy (ADR-0011).
type deletionLog struct {
	path string
	clk  clock.Clock
	f    *os.File
	size int64
}

// openDeletionLog opens the log for append and proves it writable, which R29 makes a
// precondition of the first DELETE: an operation that cannot open it must refuse to
// start and name the log as the reason. Opening with O_CREATE on a creatable path is
// the proof; it never writes a probe byte that would pollute the record. The parent
// directory is created first, because the state directory may not exist on a first run.
func openDeletionLog(path string, clk clock.Clock) (*deletionLog, error) {
	if path == "" {
		return nil, fmt.Errorf("ops: no deletion log path configured; refusing to delete without a record (purge R29)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("ops: cannot create the deletion log directory %s: %w (purge R29)", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ops: cannot open the deletion log %s: %w (purge R29)", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ops: cannot stat the deletion log %s: %w (purge R29)", path, err)
	}
	return &deletionLog{path: path, clk: clk, f: f, size: info.Size()}, nil
}

// write appends one attempt's line, rotating first when the line would carry the
// active log past logRotateSize (R29). A write it cannot complete returns the error,
// which Execute treats exactly as R21's breaker: no further DELETE, and the summary
// names the log. The line is written after the response is classified and before the
// next DELETE, so the log is never more than one attempt behind reality (R29).
func (l *deletionLog) write(rec logRecord) error {
	line := formatLogLine(l.clk.Now(), rec)
	if l.size > 0 && l.size+int64(len(line)) > logRotateSize {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.f.WriteString(line)
	l.size += int64(n)
	if err != nil {
		return fmt.Errorf("ops: deletion log write failed: %w (purge R29)", err)
	}
	return nil
}

// rotate shifts deletions.log to .1, .1 to .2, and so on, dropping the oldest so at
// most logGenerations rotated files exist beside a fresh active log (R29). A missing
// generation is not an error: an early rotation has fewer than the full set.
func (l *deletionLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("ops: deletion log rotate (close): %w (purge R29)", err)
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, logGenerations)) // drop the oldest
	for i := logGenerations; i > 1; i-- {
		from := fmt.Sprintf("%s.%d", l.path, i-1)
		to := fmt.Sprintf("%s.%d", l.path, i)
		_ = os.Rename(from, to) // a missing generation is fine
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return fmt.Errorf("ops: deletion log rotate (rename active): %w (purge R29)", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("ops: deletion log rotate (reopen): %w (purge R29)", err)
	}
	l.f = f
	l.size = 0
	return nil
}

// close closes the active log file.
func (l *deletionLog) close() error { return l.f.Close() }

// formatLogLine renders one attempt as R29's six tab-separated fields in fixed order:
// an RFC 3339 UTC timestamp from the injected clock, the host-qualified repo, the
// kind, the id, the outcome, and the escaped reason. Tab-separated because the only
// moment this file is read is the worst possible moment to need a parser (R29).
func formatLogLine(now time.Time, rec logRecord) string {
	fields := []string{
		now.UTC().Format(time.RFC3339),
		rec.repo.String(),
		string(rec.kind),
		strconv.FormatInt(rec.id, 10),
		string(rec.outcome),
		escapeReason(rec.reason),
	}
	return strings.Join(fields, "\t") + "\n"
}

// escapeReason escapes the two characters R29 names, tab and newline, plus the
// carriage return that would tear a row and the backslash that would make the
// escaping ambiguous, so a hostile or multi-line API message stays one field on one
// line. It is deliberately not textsan: the log is a file for grep and cut, not a
// terminal rendering, so control bytes are escaped for the TSV rather than stripped.
func escapeReason(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\t", "\\t")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}
