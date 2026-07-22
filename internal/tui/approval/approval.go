// Package approval is the decision pane the Feed opens over a Run blocked on a human
// decision (approvals R11, R12). It is a pane, not a tab and not a tea.Model: it exposes
// View() string and an Update its opener drives, and it is imported by the Feed that opens
// it over the awaiting Run under the cursor (ADR-0011's pane contract). It never imports a
// tab or the root.
//
// It routes by Kind, so each kind offers only its own action (approvals R2, R3, AC2). A
// fork-PR Run offers approve alone, a single POST to /approve with no comment. A pending
// deployment offers the review: it fetches the environments the Run awaits, presents them,
// and submits an approve or a reject targeting their ids with a required comment (R12, R13).
// The two take different requests, so conflating them would send the wrong one; the pane
// holds the Kind the Feed classified and never re-derives it.
//
// It issues no DELETE and opens no deletion log: both writes are single POSTs ops owns
// beside Execute (ADR-0011, ADR-0019), reached through the injected Approver. A 403 from
// either is R14's expected outcome, the account is not a designated reviewer, rendered as an
// outcome rather than an error and never retried. Every API-derived string it paints, an
// environment name, a reviewer label, the Run's title, is author or third-party influenced,
// so it is sanitised through textsan before it reaches the screen (security review).
package approval

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/approvals"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// Approver runs the two approval writes (R11, R12). *ops.Ops satisfies it via ApproveRun
// and ReviewDeployment. It is a narrow interface so the pane depends on the two writes it
// makes, and a golden test leaves it nil, where the submit key issues nothing (the mutation
// stays disabled until the engine is wired).
type Approver interface {
	ApproveRun(ctx context.Context, repo domain.RepoID, runID int64) error
	ReviewDeployment(ctx context.Context, req ops.ReviewRequest) error
}

// Fetcher reads a Run's pending deployments, the environments it awaits with their ids,
// their current_user_can_approve flag and their reviewers (R12). It is injected, backed by
// ghclient in main.go and a cassette in tests. A nil Fetcher leaves the pane driven by
// injected messages alone, which is the golden path (no network).
type Fetcher interface {
	PendingDeployments(repo domain.RepoID, runID int64) ([]PendingDeployment, error)
}

// PendingDeployment is one environment a Run awaits, read from the pending_deployments
// endpoint (R12). The ids arrive with the names to label them, and current_user_can_approve
// and the reviewers ride along, which is what R10 surfaces at the row when a review is
// opened. The name and reviewer labels are author-controlled and sanitised before painting.
type PendingDeployment struct {
	EnvironmentID         int64
	EnvironmentName       string
	CurrentUserCanApprove bool
	Reviewers             []string
}

// Target is what the Feed hands the pane when opening the decision over the cursor Run. The
// Feed classified the Run and passes the Kind, so the pane never re-derives it (R3). Title
// is the Run's display title, painted so the operator confirms which Run they are acting on.
type Target struct {
	Repo  domain.RepoID
	RunID int64
	Kind  approvals.Kind
	Title string
}

// DeploymentsLoaded carries the fetched pending deployments into the pane (R12). The pane's
// own fetch Cmd produces it, and a golden test injects it directly with no network. Err is a
// fetch failure the pane surfaces rather than a silent empty list.
type DeploymentsLoaded struct {
	Deployments []PendingDeployment
	Err         error
}

// Reviewed carries an approve or a review outcome back into the pane (R11, R12, R14). A nil
// Err is the write accepted; a 403 is R14's not-a-designated-reviewer outcome, which
// ops.NotAReviewer reads; any other error is a genuine failure surfaced with the API's
// reason.
type Reviewed struct {
	Err error
}

// Model is the decision pane's state. It holds the target Run and its Kind, the fetched
// environments for a review, the review's collected state and comment, and the write seam.
// It renders from held state alone, which is what makes the goldens cheap.
type Model struct {
	profile  keys.Profile
	approver Approver
	fetcher  Fetcher

	open          bool
	width, height int

	repo  domain.RepoID
	runID int64
	kind  approvals.Kind
	title string

	// Deployment-review collection state (R12). deployments is the fetched environments,
	// state the approve-or-reject decision, and comment the required note (R13).
	deployments []PendingDeployment
	loading     bool
	loadErr     string
	state       ops.ReviewState
	comment     string
	editing     bool
	editInput   textinput.Model

	submitting bool
	status     string
	statusErr  bool
	// done marks a terminal outcome, a successful write or R14's not-a-reviewer outcome,
	// after which the pane shows the result and only close acts, so a second submit cannot
	// re-issue the write (R14 forbids a retry). A genuine, correctable failure leaves it
	// false so the operator can adjust and resubmit.
	done bool
}

// Options carries the pane's construction seams. main.go fills them: Profile is the resolved
// keybinding set, Approver runs the two writes through the shared ops engine, and Fetch reads
// the pending deployments over the shared client. A golden test leaves Approver and Fetch nil
// and injects messages, so the pane renders held state and issues nothing.
type Options struct {
	Profile  keys.Profile
	Approver Approver
	Fetch    Fetcher
}

// New returns a closed pane over opts. It holds no target until Open.
func New(opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "a comment is required"
	return Model{
		profile:   opts.Profile,
		approver:  opts.Approver,
		fetcher:   opts.Fetch,
		editInput: ti,
	}
}

// Open shows the decision over a Target, resetting all collection state so a reopened pane
// never carries a prior Run's environments or a prior review's answer. A pending deployment
// fetches its environments first, so the review is over exactly the ids the Run awaits (R12);
// a fork-PR approval needs no fetch. A golden test ignores the returned Cmd and injects a
// DeploymentsLoaded directly.
func (m Model) Open(t Target) (Model, tea.Cmd) {
	m.open = true
	m.repo = t.Repo
	m.runID = t.RunID
	m.kind = t.Kind
	m.title = t.Title
	m.deployments = nil
	m.loadErr = ""
	m.state = ops.ReviewApproved
	m.comment = ""
	m.editing = false
	m.editInput.SetValue("")
	m.submitting = false
	m.status = ""
	m.statusErr = false
	m.done = false
	if t.Kind == approvals.KindPendingDeployment {
		m.loading = true
		return m, m.loadDeploymentsCmd()
	}
	m.loading = false
	return m, nil
}

// Close hides the pane. The opener calls it once the operator dismisses the decision.
func (m Model) Close() Model {
	m.open = false
	m.editing = false
	return m
}

// IsOpen reports whether the pane is showing, which the opener reads to paint it and route
// keys to it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// CapturesInput reports whether the pane holds input focus. It does whenever it is open, so
// the root's global navigation keys stand down and a comment's text, or A pressed to submit,
// is the pane's rather than a tab switch or a quit (R7's spirit, shared with the Feed's
// filter and the confirm modal).
func (m Model) CapturesInput() bool { return m.open }

// Update handles one message the opener routed here. It lays out on size, applies the fetched
// deployments and the write outcome, and drives the decision on keys. A message for a closed
// pane is discarded by the open gate, the same way the Feed's other panes discard their
// tagged messages when closed (ADR-0015).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.editInput.SetWidth(max(msg.Width-4, 0))
		return m, nil
	case DeploymentsLoaded:
		if !m.open {
			return m, nil
		}
		return m.applyDeployments(msg), nil
	case Reviewed:
		if !m.open {
			return m, nil
		}
		return m.applyReviewed(msg), nil
	case tea.KeyPressMsg:
		if !m.open {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// applyDeployments adopts the fetched environments (R12). A fetch failure becomes an explicit
// load error, never a silent empty list, so the operator sees the review could not be
// prepared rather than an empty form.
func (m Model) applyDeployments(msg DeploymentsLoaded) Model {
	m.loading = false
	if msg.Err != nil {
		m.loadErr = "could not load the pending deployments: " + msg.Err.Error()
		return m
	}
	m.loadErr = ""
	m.deployments = msg.Deployments
	return m
}

// applyReviewed records a write outcome (R11, R12, R14). A 403 is R14's expected outcome,
// rendered neutrally as not-a-designated-reviewer and never retried; the pane is done, so a
// second submit cannot re-issue it, and the Feed's badge and filter are untouched because the
// Run's fields did not change (R15, AC10). A genuine failure surfaces the API's reason and
// leaves the pane open to a corrected resubmit; a success states what was done.
func (m Model) applyReviewed(msg Reviewed) Model {
	m.submitting = false
	if msg.Err != nil {
		if ops.NotAReviewer(msg.Err) {
			m.status = "you are not a designated reviewer for this run"
			m.statusErr = false // an expected outcome, not a failure (R14)
			m.done = true       // no retry (R14)
			return m
		}
		m.status = "failed: " + msg.Err.Error()
		m.statusErr = true
		m.done = false
		return m
	}
	m.status = m.successMessage()
	m.statusErr = false
	m.done = true
	return m
}

// successMessage states what the accepted write did, by Kind and, for a review, by decision.
func (m Model) successMessage() string {
	if m.kind == approvals.KindForkPR {
		return "fork-PR run approved"
	}
	if m.state == ops.ReviewRejected {
		return "deployment rejected"
	}
	return "deployment approved"
}

// handleKey drives the decision, matching against the registry with key.Matches and never a
// key literal of its own (R7a, AC18). While the comment is being edited the text input
// consumes the key; otherwise Approve submits, and for a review ToggleSelect flips the
// decision and OpenDetail edits the comment. CloseDetail closes the pane.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.editing {
		return m.handleEditKey(k)
	}
	switch {
	case key.Matches(k, m.profile.CloseDetail):
		return m.Close(), nil
	case key.Matches(k, m.profile.Approve):
		return m.submit()
	case key.Matches(k, m.profile.ToggleSelect):
		if m.kind == approvals.KindPendingDeployment && !m.done {
			m = m.toggleState()
		}
		return m, nil
	case key.Matches(k, m.profile.OpenDetail):
		if m.kind == approvals.KindPendingDeployment && !m.done {
			return m.beginEdit()
		}
		return m, nil
	}
	return m, nil
}

// handleEditKey drives the comment text input. FilterAccept (enter) commits the typed
// comment, FilterCancel (esc) discards the edit, and everything else is text the input
// consumes. Matching both control keys from the registry keeps the input inside AC18's reach.
func (m Model) handleEditKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.FilterAccept):
		m.comment = m.editInput.Value()
		m.editing = false
		m.editInput.Blur()
		return m, nil
	case key.Matches(k, m.profile.FilterCancel):
		m.editing = false
		m.editInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.editInput, cmd = m.editInput.Update(k)
	return m, cmd
}

// beginEdit enters comment-edit mode, seeding the input with the current comment so an edit
// refines rather than clears it.
func (m Model) beginEdit() (Model, tea.Cmd) {
	m.editing = true
	m.editInput.SetValue(m.comment)
	return m, m.editInput.Focus()
}

// toggleState flips the review's decision between approve and reject (R12).
func (m Model) toggleState() Model {
	if m.state == ops.ReviewApproved {
		m.state = ops.ReviewRejected
	} else {
		m.state = ops.ReviewApproved
	}
	return m
}

// submit issues the write for the Kind, gating first on the things that cost no request. A
// review refuses an empty comment and issues nothing, naming the requirement (R13, AC9); a
// review with no environments loaded issues nothing. A second submit while one is in flight,
// or after a terminal outcome, is inert, so a double keypress cannot issue two writes and a
// 403 is never retried (R14). With no Approver wired it is inert (the golden path).
func (m Model) submit() (Model, tea.Cmd) {
	if m.submitting || m.done || m.approver == nil {
		return m, nil
	}
	switch m.kind {
	case approvals.KindForkPR:
		m.submitting = true
		m.status = "approving…"
		m.statusErr = false
		return m, m.approveRunCmd()
	case approvals.KindPendingDeployment:
		if m.loading {
			return m, nil
		}
		if strings.TrimSpace(m.comment) == "" {
			m.status = "a deployment review needs a comment"
			m.statusErr = true
			return m, nil // R13, AC9: refuse, and issue nothing
		}
		if len(m.deployments) == 0 {
			m.status = "no environments are awaiting review"
			m.statusErr = true
			return m, nil
		}
		m.submitting = true
		m.status = "submitting review…"
		m.statusErr = false
		return m, m.reviewCmd()
	default:
		return m, nil
	}
}

// approveRunCmd issues the fork-PR approval through ops on a Cmd, returning a Reviewed. With
// no Approver wired it is nil (the golden path).
func (m Model) approveRunCmd() tea.Cmd {
	if m.approver == nil {
		return nil
	}
	a, repo, runID := m.approver, m.repo, m.runID
	return func() tea.Msg {
		return Reviewed{Err: a.ApproveRun(context.Background(), repo, runID)}
	}
}

// reviewCmd issues the deployment review through ops on a Cmd, targeting every fetched
// environment's id with the collected state and comment (R12). With no Approver wired it is
// nil.
func (m Model) reviewCmd() tea.Cmd {
	if m.approver == nil {
		return nil
	}
	a := m.approver
	req := ops.ReviewRequest{
		Repo:           m.repo,
		RunID:          m.runID,
		EnvironmentIDs: m.environmentIDs(),
		State:          m.state,
		Comment:        m.comment,
	}
	return func() tea.Msg {
		return Reviewed{Err: a.ReviewDeployment(context.Background(), req)}
	}
}

// loadDeploymentsCmd fetches the Run's pending deployments, returning a DeploymentsLoaded
// (R12). With no fetcher wired it is nil (the golden path injects the message directly).
func (m Model) loadDeploymentsCmd() tea.Cmd {
	if m.fetcher == nil {
		return nil
	}
	f, repo, runID := m.fetcher, m.repo, m.runID
	return func() tea.Msg {
		ds, err := f.PendingDeployments(repo, runID)
		return DeploymentsLoaded{Deployments: ds, Err: err}
	}
}

// environmentIDs is every fetched environment's id, the ids the review targets (R12). The
// plural is intentional: the request carries several whether or not a Run awaits several
// (R12's resolved open question).
func (m Model) environmentIDs() []int64 {
	ids := make([]int64, 0, len(m.deployments))
	for _, d := range m.deployments {
		ids = append(ids, d.EnvironmentID)
	}
	return ids
}
