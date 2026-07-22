package cli

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/filter"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// TestRenderTableSanitisesControlBytes pins the human-table hardening: untrusted run
// data carrying ANSI escape sequences or other C0 control bytes is stripped before it
// reaches the terminal, so a hostile run title from a fanned-out third-party
// repository cannot move the cursor or rewrite prior lines (security review). Only the
// table is sanitised; the -q and --json data paths are left raw.
func TestRenderTableSanitisesControlBytes(t *testing.T) {
	var out bytes.Buffer
	deps := Deps{Stdout: &out, Stderr: &out, Clock: clock.Real()}
	runs := []domain.Run{{
		ID:           7,
		DisplayTitle: "\x1b[31mpwned\x1b[0m\x1b[2K",
		HeadBranch:   "ma\x1b[0min",
		Event:        "pu\tsh",
		Status:       domain.StatusCompleted,
		Conclusion:   domain.ConclusionSuccess,
	}}
	if err := renderTable(deps, scope{}, runs); err != nil {
		t.Fatalf("renderTable: %v", err)
	}
	got := out.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("table output still carries an ESC byte: %q", got)
	}
	if !strings.Contains(got, "pwned") {
		t.Errorf("visible title text was lost: %q", got)
	}
	if !strings.Contains(got, "main") {
		t.Errorf("branch lost its text to the escape strip: %q", got)
	}
	if !strings.Contains(got, "push") {
		t.Errorf("event tab was not stripped into a single cell: %q", got)
	}
}

// TestPrintSummarySanitisesControlBytes pins that a failure reason derived from a hostile
// API error body is stripped of terminal control bytes before it reaches the terminal, so a
// crafted "message" from a fanned-out third-party repository cannot move the cursor or
// rewrite prior lines through the end-of-Purge summary (security review). It mirrors the
// list table's defense. The R29 deletion log's recorded reason is write-only and stays raw
// by design, so only the terminal render is sanitised, not what ops records.
func TestPrintSummarySanitisesControlBytes(t *testing.T) {
	var out bytes.Buffer
	deps := Deps{Stdout: &out, Stderr: &out, Clock: clock.Real()}
	sum := ops.Summary{
		Total:   3,
		Deleted: 2,
		Failures: []ops.FailureGroup{
			{Reason: "HTTP 403: \x1b[31mForbidden\x1b[0m\x1b[2K", Count: 1},
		},
	}
	printSummary(deps, sum)
	got := out.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("summary output still carries an ESC byte: %q", got)
	}
	if !strings.Contains(got, "Forbidden") {
		t.Errorf("visible failure reason text was lost to the escape strip: %q", got)
	}
}

// canned builds a 200 response with a JSON body and an optional Link header, for the
// fake Requester below.
func canned(bodyJSON, link string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if link != "" {
		h.Set("Link", link)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(bodyJSON)),
	}
}

// pagedRequester answers the first request with page and its Link, and any later
// request with an empty page, so a crawl that respects the limit makes exactly one
// call and one that does not still terminates.
type pagedRequester struct {
	page, link string
	calls      int
}

func (p *pagedRequester) Request(_, _ string, _ io.Reader) (*http.Response, error) {
	p.calls++
	if p.calls == 1 {
		return canned(p.page, p.link), nil
	}
	return canned(`{"workflow_runs":[]}`, ""), nil
}

// TestListRepoFlagsCappedAtLimit pins R16's capped signal: a repository that fills the
// requested limit while a rel="next" page still remains is reported capped, and the
// next page is not fetched, because the limit is already met (cli-surface R16, R23).
func TestListRepoFlagsCappedAtLimit(t *testing.T) {
	repo := domain.RepoID{Host: "github.com", Owner: "octo", Name: "hello"}
	page := `{"total_count":9999,"workflow_runs":[` +
		`{"id":1,"created_at":"2026-07-20T05:00:00Z"},` +
		`{"id":2,"created_at":"2026-07-20T04:00:00Z"}]}`
	next := `<https://api.github.com/repos/octo/hello/actions/runs?per_page=2&page=2>; rel="next"`
	pr := &pagedRequester{page: page, link: next}

	runs, capped, err := listRuns(pr, []domain.RepoID{repo}, filter.Filter{}, 2)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	if len(capped) != 1 || capped[0] != repo {
		t.Errorf("capped = %v, want [%v] (filled the limit with a next page on the wire)", capped, repo)
	}
	if pr.calls != 1 {
		t.Errorf("wire calls = %d, want 1 (the next page is not fetched once the limit is met)", pr.calls)
	}
}

// TestListRepoNotCappedWhenExhausted pins the other side: a listing that returns fewer
// than the limit and has no next page is not capped, so the note never fires on a
// complete result (cli-surface R16).
func TestListRepoNotCappedWhenExhausted(t *testing.T) {
	repo := domain.RepoID{Host: "github.com", Owner: "octo", Name: "hello"}
	page := `{"total_count":2,"workflow_runs":[` +
		`{"id":1,"created_at":"2026-07-20T05:00:00Z"},` +
		`{"id":2,"created_at":"2026-07-20T04:00:00Z"}]}`
	pr := &pagedRequester{page: page, link: ""}

	_, capped, err := listRuns(pr, []domain.RepoID{repo}, filter.Filter{}, 20)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(capped) != 0 {
		t.Errorf("capped = %v, want empty (the listing exhausted below the limit)", capped)
	}
}
