package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// Resolution is what one by-name Job resolution produced over a selected set of Runs, and
// its three fields are three different things a caller must not merge (run-lifecycle R17a).
//
// Items are the Runs that held a Job of the name, each addressing that Job's own id.
// Unmatched are the Runs that were asked and held none, a definite answer.
// Unreached is how many selected Runs were never asked, the absence of an answer, with
// UnreachedReason naming why. Folding the unreached into Unmatched would put them in the Plan's
// Total, which would price a set larger than the one the operator confirms and undo R17a's
// ruling by the back door.
//
// Items and Unmatched go to Plan together, which freezes them into one set. Unreached and
// UnreachedReason go to the surface, which states them in R17a's non-blocking note or, where there
// is no confirm surface, in the CLI's summary beside a non-zero exit.
type Resolution struct {
	Items     []Item
	Unmatched []Unmatched
	Unreached int
	// UnreachedReason is why the resolution stopped, and it is named apart from
	// Unmatched.Reason deliberately: that one says why a Run matched nothing, and this one
	// says why other Runs were never asked. Two fields called Reason one struct apart is
	// how the distinction R17a turns on gets lost.
	UnreachedReason string
}

// StoppedEarly reports whether the resolution failed to reach every selected Run. It is
// what the CLI's exit code and the confirm surface's note both branch on: cli-surface R17
// makes this exit 1, because nothing failed but not everything the operator asked for
// happened, which is a different outcome from a name that matched nothing (R28b's exit 0).
func (r Resolution) StoppedEarly() bool { return r.Unreached > 0 }

// ResolveJobsByName resolves a Job name against each selected Run, one Jobs request per
// Run, and returns the frozen members that resolution produced (cli-surface R28b,
// run-lifecycle R14a, R17a).
//
// It lives in ops rather than in a surface because both cli and tui need it, neither may
// import the other, and ops is below both (ADR-0011). It is the only lifecycle resolution
// that issues a request ahead of its own confirmation, which is what R17a exists to rule on.
//
// At most one Item per Run, which is R14a's bound arriving here rather than at Plan: the
// first Job of the name wins, so a Run declaring the name twice cannot produce the pair
// checkOneJobPerRun would then refuse. Refusing the whole set for a Workflow that happens
// to run one name in two matrix legs would make the flag unusable where it is most wanted,
// and the pair is refused only when the operator selected it.
//
// It stops at the first Run it cannot get an answer for, rather than carrying on past it.
// That is what makes the frozen set "what resolution resolved" (R17a): a Run whose listing
// was rate-limited has no answer, and neither does the next one, so counting either as a
// definite skip would assert something this resolution cannot know. The remaining Runs are
// reported as unreached with the reason, and the surfaces render that separately.
//
// A cancelled context is the one stop that is not an unreached set. R17a is about the API
// cutting the resolution short, and the surfaces answer that by pricing what resolved; an
// operator pressing Ctrl-C asked for the command to stop, and cli-surface R17 gives that its
// own exit code. Returning it as an error is what keeps a partial set from falling through
// into Plan, Confirm and Execute after the operator has already said stop.
func (o *Ops) ResolveJobsByName(ctx context.Context, sel []Item, name string) (Resolution, error) {
	// One reason per invocation, built from the name and not from the Run, so groupByReason
	// collapses every member into one group with a count rather than one line per Run
	// (ADR-0019 amended, run-lifecycle AC14c).
	reason := unmatchedReason(name)
	var res Resolution
	for i := range sel {
		if sel[i].Kind != KindRun || sel[i].Run == nil {
			return Resolution{}, fmt.Errorf("ops: a by-name Job resolution takes a set of Runs; it was handed a %q Item", sel[i].Kind)
		}
	}
	for i := range sel {
		if err := ctx.Err(); err != nil {
			return Resolution{}, err
		}
		jobs, why, err := o.listJobs(ctx, sel[i].Repo, sel[i].ID)
		if err != nil {
			return Resolution{}, err
		}
		if why != "" {
			return res.stoppedAt(i, len(sel), why), nil
		}
		if job, ok := firstJobNamed(jobs, name); ok {
			res.Items = append(res.Items, JobItem(job))
			continue
		}
		res.Unmatched = append(res.Unmatched, Unmatched{Repo: sel[i].Repo, RunID: sel[i].ID, Reason: reason})
	}
	return res, nil
}

// unmatchedReason is the one reason string a by-name resolution stamps on every member it
// answered negatively. It names the absent Job, which is what AC14c requires, and names no
// Run, which is what makes the group collapse.
func unmatchedReason(name string) string {
	return fmt.Sprintf("no job named %q in this run", name)
}

// stoppedAt records that the resolution reached sel[:at] and no further, so every Run from
// at onwards is unreached. The Run that produced the missing answer is unreached too: it
// was asked and did not answer, which is not the same as answering no (R17a).
func (r Resolution) stoppedAt(at, total int, why string) Resolution {
	r.Unreached = total - at
	r.UnreachedReason = why
	return r
}

// listJobs issues one Jobs listing for a Run and returns its Jobs, or the reason no answer
// was had. A rate limit the governor classified, any other non-200, and a transport error
// are all the same thing to this caller: no answer. It reports them with the API's own
// words where there are any, because R17a's note has to name why.
//
// The error return is for a cancelled context alone, which is not "no answer" but "stop
// asking", and the caller propagates it rather than folding it into an unreached count.
func (o *Ops) listJobs(ctx context.Context, repo domain.RepoID, runID int64) ([]domain.Job, string, error) {
	resp, err := o.client.RequestWithContext(ctx, http.MethodGet, jobsListPath(repo, runID), nil)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, "", cerr
		}
		return nil, "the jobs listing failed: " + err.Error(), nil
	}
	defer func() { _ = resp.Body.Close() }()
	// The governor's stamped classification is read first, because a secondary-limit 403 can
	// arrive with a healthy x-ratelimit-remaining and only the body-shape classification
	// tells it from an authorization 403 (rate-governor open question 1).
	if governor.RateLimited(resp) {
		return nil, "the API rate-limited the jobs listing: " + failureReason(resp), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "the jobs listing failed: " + failureReason(resp), nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "the jobs listing could not be read: " + err.Error(), nil
	}
	var page apiJobsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "the jobs listing could not be decoded: " + err.Error(), nil
	}
	// Stamp the repository the request was made against. The payload does not carry it, and
	// this is the one place both facts are in hand, which is what lets JobItem derive its
	// tuple from the object rather than from an argument beside it (ADR-0019).
	for i := range page.Jobs {
		page.Jobs[i].Repo = repo
	}
	return page.Jobs, "", nil
}

// apiJobsPage is the fragment of an actions/runs/{id}/jobs listing this resolution reads.
// total_count is deliberately unread: the listing is the latest Attempt's and builds no
// history, and a filtered listing's total_count is never a number to stand behind (R3).
//
// This is knowingly parallel to the Run detail pane's own decode of the same endpoint, and
// the duplication is chosen rather than drifted into. ops may not import tui (ADR-0011),
// and moving the path and the struct into a package both reach would create one to hold a
// string builder and a two-field struct. The two also want different things from the same
// payload: the pane renders the Steps inline, and this reads a name and an id. If a third
// caller appears, that is when the shared home earns its place.
type apiJobsPage struct {
	Jobs []domain.Job `json:"jobs"`
}

// jobsListPath is a Run's Jobs listing, the latest Attempt's. Its only query is
// per_page=100, the API's page ceiling: a 38-job Run was measured served 30 with a Link
// rel=next, so "one request per Run" holds only at the full page. This follows no Link
// header, so a Run declaring more than 100 Jobs resolves against the first 100. The query
// carries neither filter=all, measured to return only the latest Attempt anyway, nor an
// attempts/N/jobs segment, which serves total_count zero.
func jobsListPath(repo domain.RepoID, runID int64) string {
	return "repos/" + repo.Owner + "/" + repo.Name + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/jobs?per_page=100"
}

// firstJobNamed is R14a's at-most-one-per-Run bound: the first Job of the name wins, and a
// second declaring the same name is not resolved beside it. The match is exact, because a
// Job name is what the Workflow declares and a fuzzy match would re-run a Job the operator
// did not name.
func firstJobNamed(jobs []domain.Job, name string) (domain.Job, bool) {
	for i := range jobs {
		if jobs[i].Name == name {
			return jobs[i], true
		}
	}
	return domain.Job{}, false
}
