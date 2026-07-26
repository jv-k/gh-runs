package running_test

import (
	"testing"
	"time"

	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
	"github.com/jv-k/gh-runs/v2/internal/tui/running"
)

// The goldens render the surface from held state alone, at 100 columns, with no terminal,
// no network and no clock: elapsed, the rate and the governor's bounds all arrive on the
// fabricated frame (ADR-0015's golden seam, purge R27). lipgloss v2 renders truecolour
// regardless of the environment, so these bytes are stable on any machine (ADR-0013).
// Regenerate with: go test ./internal/tui/running/ -run Golden -update.

// newGolden is the pane as the product wires it: the Standard profile, a retrier so R22's
// keystroke is offered where there is something to re-attempt, and 100 columns.
func newGolden() running.Model {
	return sized(running.New(keys.Standard).WithRetrier(&retrier{}), 100)
}

// TestGoldenProgressRange fixes R15's live line at reference scale, with the remaining
// time as a range because the observed rate and the governor's ceiling disagree: 17,026
// outstanding is ~2h25m at the 1.96/sec ceiling and ~4h44m at the observed 1.0/sec
// (AC23). Every figure R15 names is on it.
func TestGoldenProgressRange(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindRun,
		Sum: ops.Summary{
			Total: 18258, Deleted: 1220, Gone: 8, Skipped: 12,
			Failures: []ops.FailureGroup{{Reason: "HTTP 500", Count: 3}},
		},
		Outstanding: 17026,
		Elapsed:     21 * time.Minute,
		Rate:        1.0,
		Ceiling:     1.96,
		Floor:       0.5,
	})
	goldie.New(t).Assert(t, "progress_range", []byte(m.View()))
}

// TestGoldenProgressCollapsed fixes R15's collapse rule: the observed rate has caught up
// with the ceiling, both ends round to the same displayed figure, and the range folds to
// one figure labelled an estimate. Nothing but display granularity switched it (AC23).
func TestGoldenProgressCollapsed(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:          ops.OpDelete,
		Kind:        ops.KindRun,
		Sum:         ops.Summary{Total: 18258, Deleted: 17900, Gone: 8, Skipped: 12},
		Outstanding: 338,
		Elapsed:     2*time.Hour + 34*time.Minute,
		Rate:        1.96,
		Ceiling:     1.96,
		Floor:       0.5,
	})
	goldie.New(t).Assert(t, "progress_collapsed", []byte(m.View()))
}

// TestGoldenSummaryWithFailures fixes R22 and AC18: the terminal account, failures grouped
// by reason with a count for each, and the single keystroke that re-attempts only them.
func TestGoldenSummaryWithFailures(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindRun,
		Done: true,
		Sum: ops.Summary{
			Total: 18258, Deleted: 18201, Gone: 14, Skipped: 36,
			Failures: []ops.FailureGroup{
				{Reason: "HTTP 403: Must have admin rights to Repository.", Count: 5},
				{Reason: "HTTP 500", Count: 2},
			},
			Skips: []ops.FailureGroup{{Reason: "run is not completed", Count: 36}},
		},
	})
	goldie.New(t).Assert(t, "summary_failures", []byte(m.View()))
}

// TestGoldenSummaryLogFailed fixes AC20's visible half: a Purge stopped by R29's log
// names the log as the reason it stopped, and the deletions it made stay reported.
func TestGoldenSummaryLogFailed(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindRun,
		Done: true,
		Sum: ops.Summary{
			Total: 18258, Deleted: 4102,
			LogFailed: true,
			Reason:    "append to deletions.log: write /home/o/.local/state/gh-runs/deletions.log: no space left on device",
		},
	})
	goldie.New(t).Assert(t, "summary_log_failed", []byte(m.View()))
}

// TestGoldenCancelledSummary fixes R16's account: a stopped Purge says it stopped, reports
// what it deleted, and does not read as a clean finish. Cancelling stops a Purge, it does
// not reverse one, and re-running the same Purge is the resume (R24).
func TestGoldenCancelledSummary(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:   ops.OpDelete,
		Kind: ops.KindRun,
		Done: true,
		Sum:  ops.Summary{Total: 18258, Deleted: 6403, Gone: 3, Cancelled: true},
	})
	goldie.New(t).Assert(t, "summary_cancelled", []byte(m.View()))
}

// TestGoldenNarrowFrame fixes the layout at 60 columns over the R19a skip reason, which
// R13 and AC14a make the expected fine-grained-PAT outcome and which renders at 177
// columns unclamped. Every line is cut to the frame and marked, so the strip draws the
// rows the root reserved for it rather than wrapping into the tab beneath (R14).
func TestGoldenNarrowFrame(t *testing.T) {
	reason := "still rejected after 3 attempts, so the backoff was abandoned and the Run skipped (R19a). " +
		"The API said: HTTP 403: Resource not accessible by personal access token"
	m := sized(running.New(keys.Standard).WithRetrier(&retrier{}), 60).
		Start(ops.Started{Op: ops.OpDelete, Kind: ops.KindRun, Total: 18258, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op: ops.OpDelete, Kind: ops.KindRun, Done: true,
		Sum: ops.Summary{
			Total: 18258, Deleted: 17800, Gone: 14, Skipped: 439,
			Failures: []ops.FailureGroup{{Reason: "HTTP 500", Count: 5}},
			Skips:    []ops.FailureGroup{{Reason: reason, Count: 439}},
		},
	})
	goldie.New(t).Assert(t, "narrow_frame", []byte(m.View()))
}

// TestGoldenReclamationVerb fixes CONTEXT.md's vocabulary on the shared surface: the same
// OpDelete over Caches and Artifacts is a Reclamation, never a Purge, which is what
// storage-reclamation inherits when it joins here (#64).
func TestGoldenReclamationVerb(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpDelete, Kind: "", Total: 664, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:          ops.OpDelete,
		Sum:         ops.Summary{Total: 664, Deleted: 210, Gone: 3},
		Outstanding: 451,
		Elapsed:     4 * time.Minute,
		Rate:        1.4,
		Ceiling:     2.5,
		Floor:       0.5,
	})
	goldie.New(t).Assert(t, "reclamation_progress", []byte(m.View()))
}

// TestGoldenLifecycleProgress fixes the surface rendering a verb that is not a Purge, the
// shape run-lifecycle and storage-reclamation join it on (#61, #64): the same two lines,
// the operation's own noun, and the API-accepted mutations counted under Acted rather
// than under Deleted.
func TestGoldenLifecycleProgress(t *testing.T) {
	m := newGolden().Start(ops.Started{Op: ops.OpCancel, Kind: ops.KindRun, Total: 240, Cancel: func() {}})
	m, _ = m.Update(ops.Progress{
		Op:          ops.OpCancel,
		Kind:        ops.KindRun,
		Sum:         ops.Summary{Total: 240, Acted: 96, Skipped: 4},
		Outstanding: 140,
		Elapsed:     90 * time.Second,
		Rate:        1.2,
		Ceiling:     2.5,
		Floor:       0.5,
	})
	goldie.New(t).Assert(t, "lifecycle_progress", []byte(m.View()))
}
