package cli

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

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

// TestTimeAgoBuckets pins -t's timeago wording bucket by bucket, on gh's own
// boundaries (cli-surface AC21, ADR-0023). Every bucket truncates rather than rounds,
// which is why 29 days is still days and 364 days is still months, and a future
// timestamp reads as "just now" rather than as a negative age.
func TestTimeAgoBuckets(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{0, "just now"},
		{59 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{90 * time.Second, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{59*time.Minute + 59*time.Second, "59 minutes ago"},
		{time.Hour, "1 hour ago"},
		{3 * time.Hour, "3 hours ago"},
		{23*time.Hour + 59*time.Minute, "23 hours ago"},
		{day, "1 day ago"},
		{29*day + 23*time.Hour, "29 days ago"},
		{30 * day, "1 month ago"},
		{364 * day, "12 months ago"},
		{365 * day, "1 year ago"},
		{730 * day, "2 years ago"},
	}
	for _, tc := range cases {
		if got := timeAgo(tc.ago); got != tc.want {
			t.Errorf("timeAgo(%s) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

// TestTruncateToWidthMeasuresDisplayCells pins -t's truncate as a display-width
// operation, not a rune count (cli-surface AC21, ADR-0023). A CJK title spends two
// cells per rune, so a rune-count cut would overflow the column by its own width
// again. Below five cells there is no room for the ellipsis and the cut is silent, and
// a cut that lands between the cells of a wide rune pads to the requested width.
func TestTruncateToWidthMeasuresDisplayCells(t *testing.T) {
	cases := []struct {
		name     string
		maxWidth int
		in       string
		want     string
	}{
		{"fits untouched", 10, "abc", "abc"},
		{"exact width untouched", 3, "abc", "abc"},
		{"ascii with ellipsis", 10, "Fix the bug", "Fix the..."},
		{"below the ellipsis floor", 4, "abcdef", "abcd"},
		{"at the ellipsis floor", 5, "abcdef", "ab..."},
		{"wide runes cost two cells", 5, "日本語です", "日..."},
		{"wide cut pads to width", 6, "日本語です", "日... "},
		{"emoji costs two cells", 5, "🚀🚀🚀🚀", "🚀..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateToWidth(tc.maxWidth, tc.in)
			if got != tc.want {
				t.Errorf("truncateToWidth(%d, %q) = %q, want %q", tc.maxWidth, tc.in, got, tc.want)
			}
			if w := lipgloss.Width(got); w > tc.maxWidth {
				t.Errorf("truncateToWidth(%d, %q) rendered %d cells wide", tc.maxWidth, tc.in, w)
			}
		})
	}
}

// TestTruncateToWidthKeepsEscapesOutOfTheBudget pins that a coloured value is cut on
// what the terminal shows: the escape sequences cost no cells and are copied through,
// and a cut inside an unreset colour emits a reset so the colour cannot leak into the
// rest of the line. -t output is raw and unsanitised by design (ADR-0023), so the
// escapes reaching truncate is the expected case, not a hostile one.
func TestTruncateToWidthKeepsEscapesOutOfTheBudget(t *testing.T) {
	got := truncateToWidth(8, "\x1b[31mhello world\x1b[0m")
	want := "\x1b[31mhello...\x1b[0m"
	if got != want {
		t.Errorf("truncateToWidth = %q, want %q", got, want)
	}
	if w := lipgloss.Width(got); w != 8 {
		t.Errorf("rendered width = %d, want 8", w)
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
