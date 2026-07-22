package rundetail

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jonboulle/clockwork"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// The goldens render the pane's frame from held state alone, at 100 columns, with no
// terminal and no network (R19, AC13). lipgloss v2 renders truecolour regardless of the
// environment, so these bytes are stable on any machine (ADR-0013), and the injected fake
// clock fixes every elapsed duration. Run with -update to regenerate:
// go test ./internal/tui/rundetail/ -run Golden -update.

func gRun(id int64, owner, name, workflow string, num, attempt int, st domain.Status, cc domain.Conclusion) domain.Run {
	return domain.Run{
		ID: id, RunNumber: num, RunAttempt: attempt, Name: workflow, WorkflowName: workflow,
		Status: st, Conclusion: cc, Repo: repoID(owner, name),
	}
}

func gJob(name string, st domain.Status, cc domain.Conclusion, started, completed time.Time, steps ...domain.Step) domain.Job {
	return domain.Job{Name: name, Status: st, Conclusion: cc, StartedAt: started, CompletedAt: completed, Steps: steps}
}

func gStep(num int, name string, st domain.Status, cc domain.Conclusion, started, completed time.Time) domain.Step {
	return domain.Step{Number: num, Name: name, Status: st, Conclusion: cc, StartedAt: started, CompletedAt: completed}
}

func sec(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

// goldenPane builds a pane at 100 columns, opened over r, with jobs applied by the same
// jobsMsg path a fetch takes, so the golden asserts what the state machine actually paints.
func goldenPane(r domain.Run, jobs []domain.Job) Model {
	m := New(Options{Clock: clockwork.NewFakeClockAt(t0)})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100})
	m, _ = m.Open(r)
	m, _ = m.Update(jobsMsg{runID: r.ID, jobs: jobs})
	return m
}

// TestGoldenJobsAndSteps fixes AC13's first case: a Run's Jobs with their Steps rendered
// under them, each with its own Status and Conclusion (R1, R2, R3).
func TestGoldenJobsAndSteps(t *testing.T) {
	r := gRun(29516338954, "cli", "cli", "CI", 4821, 1, completed, success)
	jobs := []domain.Job{
		gJob("build", completed, success, sec(-64), sec(0),
			gStep(1, "Set up job", completed, success, sec(-64), sec(-62)),
			gStep(2, "Run tests", completed, success, sec(-62), sec(-4)),
		),
		gJob("test (ubuntu-latest)", completed, success, sec(-58), sec(-2),
			gStep(1, "Set up job", completed, success, sec(-58), sec(-55)),
			gStep(2, "Go test", completed, success, sec(-55), sec(-2)),
		),
	}
	goldie.New(t).Assert(t, "jobs_and_steps", []byte(goldenPane(r, jobs).View()))
}

// TestGoldenInProgressJob fixes AC13's second case: a Job at Status in_progress renders an
// empty Conclusion field, and the split holds at Run, Job and Step alike (R2, R3, AC5).
func TestGoldenInProgressJob(t *testing.T) {
	r := gRun(57, "acme", "api", "Deploy", 57, 1, inProgress, "")
	jobs := []domain.Job{
		gJob("build", completed, success, sec(-90), sec(-30),
			gStep(1, "Set up job", completed, success, sec(-90), sec(-88)),
			gStep(2, "Compile", completed, success, sec(-88), sec(-30)),
		),
		gJob("deploy", inProgress, "", sec(-12), time.Time{},
			gStep(1, "Set up job", completed, success, sec(-12), sec(-9)),
			gStep(2, "Push image", inProgress, "", sec(-9), time.Time{}),
		),
	}
	goldie.New(t).Assert(t, "in_progress_job", []byte(goldenPane(r, jobs).View()))
}

// TestGoldenAttemptBadge fixes AC13's third case: a Run with run_attempt 3 renders
// "Attempt 3" against the Run's identity and not within the Jobs list (R4, AC3). Moving the
// badge into the Jobs list fails this golden, because no Job row prints an Attempt.
func TestGoldenAttemptBadge(t *testing.T) {
	r := gRun(4821, "cli", "cli", "CI", 4821, 3, completed, success)
	jobs := []domain.Job{
		gJob("build", completed, success, sec(-64), sec(0),
			gStep(1, "Set up job", completed, success, sec(-64), sec(-62)),
			gStep(2, "Run tests", completed, success, sec(-62), sec(0)),
		),
	}
	goldie.New(t).Assert(t, "attempt_badge", []byte(goldenPane(r, jobs).View()))
}

// TestGoldenNoJobs fixes AC14: a not-started Run renders an explicit "no Jobs yet" state.
func TestGoldenNoJobs(t *testing.T) {
	r := gRun(58, "acme", "api", "CI", 58, 1, queued, "")
	goldie.New(t).Assert(t, "no_jobs", []byte(goldenPane(r, nil).View()))
}

// TestGoldenWorkflowDeleted fixes AC11: for a Run whose Workflow is deleted, the pane marks
// the Workflow deleted, so an Orphaned Run is distinguishable from one with a live successor.
func TestGoldenWorkflowDeleted(t *testing.T) {
	r := gRun(4821, "cli", "cli", "Old Pipeline", 4821, 1, completed, failure)
	jobs := []domain.Job{
		gJob("build", completed, failure, sec(-40), sec(-1),
			gStep(1, "Set up job", completed, success, sec(-40), sec(-38)),
			gStep(2, "Run tests", completed, failure, sec(-38), sec(-1)),
		),
	}
	m := goldenPane(r, jobs).SetWorkflowState(domain.StateDeleted)
	goldie.New(t).Assert(t, "workflow_deleted", []byte(m.View()))
}

// TestGoldenPaused fixes AC12: at Budget exhaustion the pane states it paused and when it
// resumes, and the held Jobs are not presented as live.
func TestGoldenPaused(t *testing.T) {
	r := gRun(57, "acme", "api", "Deploy", 57, 1, inProgress, "")
	jobs := []domain.Job{
		gJob("deploy", inProgress, "", sec(-12), time.Time{},
			gStep(1, "Set up job", completed, success, sec(-12), sec(-9)),
			gStep(2, "Push image", inProgress, "", sec(-9), time.Time{}),
		),
	}
	m := goldenPane(r, jobs)
	m, _ = m.Update(governor.Readout{Exhausted: true, Reset: time.Date(2026, 7, 15, 17, 9, 0, 0, time.UTC)})
	goldie.New(t).Assert(t, "paused", []byte(m.View()))
}
