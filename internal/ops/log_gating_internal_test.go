package ops

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// fakeRequester returns a 204 for every DELETE and counts them, so a white-box test
// can drive Execute without the transport chain and prove exactly how many DELETEs
// were issued before a mid-operation log failure stopped it.
type fakeRequester struct {
	deletes int
}

func (f *fakeRequester) RequestWithContext(_ context.Context, method, _ string, _ io.Reader) (*http.Response, error) {
	if method == http.MethodDelete {
		f.deletes++
	}
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}, nil
}

// failingSink writes to memory and fails on the failAt'th write, standing in for a
// disk that fills mid-Purge. It proves R29's "a write that fails mid-operation stops
// it" without needing a real full disk (AC20).
type failingSink struct {
	writes int
	failAt int
}

func (s *failingSink) write(logRecord) error {
	s.writes++
	if s.writes == s.failAt {
		return errors.New("ops: deletion log write failed: no space left on device (purge R29)")
	}
	return nil
}

func (s *failingSink) close() error { return nil }

// TestMidOperationLogFailureStopsThePurge pins AC20's second half: when a log write
// fails after N deletions, no DELETE is issued afterwards, the N stay deleted, and
// the summary names the log. The DELETE whose record could not be written already
// happened and is not un-done; the Purge stops before the next one (R29).
func TestMidOperationLogFailureStopsThePurge(t *testing.T) {
	fake := &fakeRequester{}
	sink := &failingSink{failAt: 3} // the third log write fails
	o := New(Options{Clock: clockwork.NewFakeClock(), ConfirmThreshold: 50, BreakerFailures: 50})
	o.client = fake
	o.openLog = func(string, clock.Clock) (logSink, error) { return sink, nil }

	sel := make([]Item, 5)
	repo := domain.Repo{ID: domain.RepoID{Host: "github.com", Owner: "o", Name: "r"}, Permissions: domain.Permissions{Push: true}}
	for i := range sel {
		sel[i] = RunItem(domain.Run{ID: int64(i + 1), Repo: repo.ID, Status: domain.StatusCompleted})
	}
	p, err := o.Plan(OpDelete, sel, map[domain.RepoID]domain.Repo{repo.ID: repo})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c, err := o.Confirm(p, NonInteractiveYes())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	sum, err := o.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !sum.LogFailed {
		t.Errorf("Execute did not report the log failure (R29, AC20)")
	}
	if !strings.Contains(sum.Reason, "log") {
		t.Errorf("summary reason %q does not name the log (AC20)", sum.Reason)
	}
	// The first two DELETEs were recorded; the third DELETE happened but its record
	// failed, so the Purge stopped. No fourth or fifth DELETE was issued.
	if fake.deletes != 3 {
		t.Errorf("issued %d DELETEs, want 3 (two recorded, the third's record failed and stopped it) (AC20)", fake.deletes)
	}
	if sum.Deleted != 2 {
		t.Errorf("counted %d deletions, want 2 (only the recorded ones) (AC20)", sum.Deleted)
	}
}
