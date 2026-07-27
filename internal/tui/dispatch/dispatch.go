package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// Dispatcher triggers a workflow_dispatch and returns the Run it created (R16). *ops.Ops satisfies
// it via Dispatch. It is a narrow interface so the pane depends on the one write it makes, and a
// golden test leaves it nil, where the submit key issues nothing (the mutation stays disabled until
// the dispatcher is wired).
type Dispatcher interface {
	Dispatch(ctx context.Context, req ops.DispatchRequest) (ops.DispatchResult, error)
}

// Fetcher reads a Workflow's YAML at a ref (R5), the repository's environments (R7), the picker's
// branches and tags (R24), and the repository's default branch (R23, the fallback the Workflows tab
// normally makes unnecessary by handing the discovered branch through Target). Injected at
// construction, backed by ghclient in main.go and a cassette in tests. A nil Fetcher leaves the pane
// driven by injected messages alone, which is the golden path (no network).
type Fetcher interface {
	DefaultBranch(repo domain.RepoID) (string, error)
	WorkflowYAML(repo domain.RepoID, path, ref string) ([]byte, error)
	Environments(repo domain.RepoID) ([]string, error)
	Refs(repo domain.RepoID) ([]Ref, error)
}

// DocStore persists and recalls a named JSON document, the local-store primitive R2a uses to
// remember a Workflow's last-used inputs (R25). *store.Transport satisfies it via SaveDoc/LoadDoc. A
// nil store disables remembering, and the form still works from the declared defaults, because the
// store is derived and safe to delete (local-store R11).
type DocStore interface {
	SaveDoc(name string, v any)
	LoadDoc(name string, v any) bool
}

// Ref is one selectable ref in the picker: a branch or a tag, labelled distinctly (R24). gh's --ref
// accepts either, so the picker offers both; a repository with no tags shows branches alone.
type Ref struct {
	Name  string
	IsTag bool
}

// Target is what the Workflows tab hands the pane when it opens the form over the Workflow under the
// cursor. Eligible is R14's gate (permissions.push && !archived), computed by the opener from the
// discovered repository so it costs no request (AC6); the pane refuses to submit when it is false
// and states EligReason.
//
// Ref is the default branch the opener supplies (R23). It rides the enumeration payload the gate
// above rides, so the ordinary open pre-selects the default branch at no request; an empty Ref sends
// the pane to its own resolver instead.
//
// Refs pre-seeds the branches-and-tags picker set (R24). It is normally empty: R24 fetches the list
// lazily, so the pane enumerates on the picker's first use rather than making every form open pay
// for a list most opens never touch. A non-empty Refs is a set the opener already holds, and
// suppresses the fetch entirely.
type Target struct {
	Repo       domain.RepoID
	Workflow   domain.Workflow
	Eligible   bool
	EligReason string
	Ref        string
	Refs       []Ref
}

// RefResolved carries the repository's default branch into the pane, which becomes the initial ref
// when the opener supplied none (R23). It is the first step of R2's fixed order when the ref is not
// pre-chosen: resolve the default branch, then fetch the YAML at it. A golden test supplies the ref
// directly, so this step is skipped there.
type RefResolved struct {
	Ref string
	Err error
}

// YAMLLoaded carries a Workflow's fetched YAML at a ref into the pane, replacing the held form (R2,
// R3). The pane's own fetch Cmd produces it, and a golden test injects it directly with no network,
// exactly as the Feed injects RunsFetched. Err is a fetch or read failure the pane surfaces naming
// the ref and the path (R12), never a silent empty form.
type YAMLLoaded struct {
	Ref  string
	Path string
	Data []byte
	Err  error
}

// EnvironmentsLoaded carries the repository's environments for the environment selects (R7). It is
// fetched at most once per render, and only when the form declares an environment input.
type EnvironmentsLoaded struct {
	Environments []string
	Err          error
}

// RefsLoaded carries the picker's branches and tags into the pane (R24). It arrives once per picker
// session, in answer to the first press on the ref row, so a form dispatched at its default branch
// never spends the two requests behind it. Err leaves the picker on the ref it already holds and
// says so, rather than moving the form to a ref nobody chose (R4).
type RefsLoaded struct {
	Refs []Ref
	Err  error
}

// Dispatched carries a Dispatch's outcome back into the pane (R16). On success the pane shows the
// Run ID and persists the inputs (R25); on failure it surfaces the API's own reason (R14, R22, R15).
type Dispatched struct {
	Result ops.DispatchResult
	Err    error

	// issued is the submission this outcome belongs to, stamped by the Cmd that sent it. It is
	// unexported because only this package issues a Dispatch, and it is carried rather than inferred
	// so an outcome settles the record it came from rather than whatever the pane happens to hold
	// when it lands, which may be a different Workflow entirely.
	issued dispatchTarget
}

// Model is the dispatch form pane's state. It holds the parsed schema, the per-input current values,
// the target ref, and the fetch, dispatch and persistence seams. It renders from held state alone,
// which is what makes the goldens cheap (R21).
type Model struct {
	profile    keys.Profile
	fetcher    Fetcher
	dispatcher Dispatcher
	store      DocStore

	open          bool
	width, height int

	repo       domain.RepoID
	workflow   domain.Workflow
	eligible   bool
	eligReason string

	ref  string
	refs []Ref
	// refsAsked is R24's "at most once per picker session": the branch and tag list is enumerated on
	// the picker's first use and never again for this form, whether the enumeration succeeded,
	// failed, or was pre-seeded by the opener. Open resets it, which is what makes a session one
	// open of the form.
	refsAsked bool
	// refsLoading and refsErr are the picker's own pending and failure state, shown on the ref row
	// rather than on the shared status line. The status line carries Dispatch outcomes, and a picker
	// that borrowed it would either wipe an outcome or be wiped by one depending on which arrived
	// last, which is a race over a line the operator reads to know whether a Run was created.
	refsLoading bool
	refsErr     string
	// refHome is the ref the form opened on, which the picker's cycle keeps reachable even when the
	// enumeration does not name it. It is the branch R23 chose, and losing the way back to it would
	// make the picker a one-way door.
	refHome string

	form         Form
	values       map[string]string
	environments []string

	loading bool
	loadErr string // R12: names the ref and the path when the YAML cannot be fetched or parsed

	cursor    int  // 0 is the ref row, 1..len(inputs) are the input rows
	editing   bool // a free-text field holds the text input
	editName  string
	editInput textinput.Model

	// issued is the re-submit guard: every Dispatch this pane has sent, keyed by the whole
	// submission, and it is not reset by Open or Close. A Dispatch creates a real Run, so the only
	// acceptable failure mode is refusing a Dispatch the operator wanted, never issuing one they
	// did not.
	//
	// The guard compares rather than releases. An earlier form of it released the latch whenever a
	// value changed, which is a different predicate from "this submission differs from the one
	// already sent" and has four ways through: toggling a boolean and toggling it back, leaving a
	// ref and returning, and above all the reopen after an outcome landed on a closed pane, where
	// R25 persisted nothing, R9 forces the required input to be retyped, and the retyping released
	// the latch on the very submission it was holding. Keying on the submission makes every one of
	// those a match, and makes a genuinely different Dispatch a different key with nothing to do.
	//
	// The map is shared across the Model's copies, which for a latch is the safe direction: a copy
	// that lags still sees what was sent.
	//
	// It grows by one entry per Run actually created in this session, and by nothing else: a
	// rejection deletes its entry, so a session that dispatches nothing successfully leaves it
	// empty. A session creating enough Runs for the map to matter has a far larger problem than the
	// map.
	//
	// Whitespace is content here and is not content to R9's required-input gate, which trims. The
	// two disagree deliberately: a trailing space is a different value to the API, so it is a
	// different submission, while an input holding only spaces is still unfilled.
	issued map[dispatchTarget]issueState

	status    string
	statusErr bool
	result    *ops.DispatchResult // R16: the Run the Dispatch created, shown until the form closes
}

// dispatchTarget identifies one whole Dispatch: the repository, the Workflow, the ref, and the
// inputs actually sent. Two submissions equal in all four are the same Dispatch, and issuing the
// second creates a duplicate Run of the first.
type dispatchTarget struct {
	repo     domain.RepoID
	workflow int64
	ref      string
	inputs   string // the inputs map canonically encoded, so the key stays comparable
}

// issueState is what became of one issued Dispatch: still in flight, or done and carrying the Run
// the response reported (R16). A rejected Dispatch is removed rather than recorded, because it
// created no Run and its submission must stay issuable.
type issueState struct {
	inflight bool
	runID    int64
}

// target is the Dispatch the form would issue right now, which the guard looks up.
func (m Model) target() dispatchTarget {
	return dispatchTarget{
		repo:     m.repo,
		workflow: m.workflow.ID,
		ref:      m.ref,
		inputs:   encodeInputs(m.buildInputs()),
	}
}

// encodeInputs renders the inputs map as a canonical string for the guard's key. encoding/json sorts
// a map's keys, so two equal maps encode identically whatever order they were built in. Marshalling
// a map[string]string cannot fail; were it to, the empty encoding would only make two submissions
// compare equal that are not, which refuses a Dispatch rather than issuing one, and that is the
// direction a write path should fail in.
func encodeInputs(in map[string]string) string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return string(b)
}

// statusDispatching is the pending line a Dispatch in flight shows. Open restores it when the form
// is reopened over a POST for this Workflow that has not resolved, so a reopened form states what it
// is doing rather than looking ready to do it again.
const statusDispatching = "dispatching…"

// Options carries the pane's construction seams. main.go fills them: Profile is the resolved
// keybinding set, Fetch reads the YAML and the environments over the shared client, Ops dispatches
// through the shared write engine, and Store persists last-used inputs in the local-store (R25). A
// golden test leaves Fetch, Ops and Store nil and injects messages, so the pane renders held state
// and issues nothing.
type Options struct {
	Profile keys.Profile
	Fetch   Fetcher
	Ops     Dispatcher
	Store   DocStore
}

// New returns a closed pane over opts. It holds no form until Open.
func New(opts Options) Model {
	ti := textinput.New()
	return Model{
		profile:    opts.Profile,
		fetcher:    opts.Fetch,
		dispatcher: opts.Ops,
		store:      opts.Store,
		editInput:  ti,
		issued:     make(map[dispatchTarget]issueState),
	}
}

// Open shows the form over a Target, resetting all collection state so a reopened pane never carries
// a prior Workflow's form or a prior Dispatch's result. It follows R2's fixed order: it holds the
// chosen ref and returns a Cmd that fetches the YAML at that ref, and no form is rendered until the
// YAML arrives (AC1). A golden test ignores the returned Cmd and injects a YAMLLoaded directly.
func (m Model) Open(t Target) (Model, tea.Cmd) {
	m.open = true
	m.repo = t.Repo
	m.workflow = t.Workflow
	m.eligible = t.Eligible
	m.eligReason = t.EligReason
	m.ref = t.Ref
	m.refs = t.Refs
	// An opener-supplied set is the picker set and suppresses the fetch, but only when it is a set
	// worth having. A single entry is not a picker, and treating it as one would leave the session
	// with no way to reach any other ref.
	m.refsAsked = len(t.Refs) > 1
	m.refsLoading = false
	m.refsErr = ""
	m.refHome = t.Ref
	m.form = Form{}
	m.values = nil
	m.environments = nil
	m.loading = true
	m.loadErr = ""
	m.cursor = 0
	m.editing = false
	m.editName = ""
	m.status = ""
	m.statusErr = false
	m.result = nil
	// issued is the re-submit guard and is deliberately not reset here. Resetting it is exactly what
	// let closing the form mid-POST and reopening it issue a second Dispatch, and a second Dispatch
	// is a second Run.
	if m.inflightHere() {
		m.status = statusDispatching
	}
	// R2's fixed order: when the opener names no ref, resolve the repository's default branch first
	// (R23), then fetch the YAML at it. When the opener supplies a ref, fetch at once.
	if m.ref == "" {
		return m, m.resolveRefCmd()
	}
	return m, m.loadYAMLCmd()
}

// Close hides the pane. The opener calls it once the operator dismisses the form.
func (m Model) Close() Model {
	m.open = false
	m.editing = false
	return m
}

// IsOpen reports whether the form is showing, which the opener reads to paint it and route keys to
// it (ADR-0011).
func (m Model) IsOpen() bool { return m.open }

// CapturesInput reports whether the pane holds input focus. It does whenever the form is open, so
// the root's global navigation keys stand down and a digit typed into a number field, or x pressed
// to submit, is the form's rather than a tab switch or a quit (R7's spirit, shared with the Feed's
// filter and the confirm modal).
func (m Model) CapturesInput() bool { return m.open }

// Update handles one message the opener routed here. It lays out on size, applies the fetched YAML,
// environments and Dispatch outcome, and drives the form on keys. A message for a closed pane is
// discarded by the open gate, the same way the Feed's panes discard their tagged messages when
// closed (ADR-0015).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.editInput.SetWidth(max(msg.Width-4, 0))
		return m, nil
	case RefResolved:
		if !m.open {
			return m, nil
		}
		return m.applyRef(msg)
	case YAMLLoaded:
		if !m.open {
			return m, nil
		}
		return m.applyYAML(msg)
	case EnvironmentsLoaded:
		if !m.open {
			return m, nil
		}
		if msg.Err == nil {
			m.environments = msg.Environments
		}
		return m, nil
	case RefsLoaded:
		if !m.open {
			return m, nil
		}
		return m.applyRefs(msg)
	case Dispatched:
		// The guard settles whether or not the form is still open. The pane issued the POST, so it
		// must not forget it because the window closed: a closed pane that forgot is precisely what
		// let a reopen issue a second Dispatch.
		m = m.settle(msg)
		// The visible outcome is narrower. It belongs to the form that produced it, not to whichever
		// form is open when it lands. R16 and AC5 make that status line the only record that a Run
		// was created, so painting one Workflow's Run on another's form names the wrong Workflow and
		// offers its URL, and R25 would persist values that were never submitted as the open
		// Workflow's last-used inputs.
		if !m.open || !m.showsOutcomeFor(msg.issued) {
			return m, nil
		}
		return m.applyDispatched(msg)
	case tea.KeyPressMsg:
		if !m.open {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// applyRef adopts the resolved default branch as the initial ref and fetches the YAML at it (R23,
// R2). A resolution failure falls back to a conventional default so the form still loads and the
// API remains the final authority on whether the ref exists.
func (m Model) applyRef(msg RefResolved) (Model, tea.Cmd) {
	if m.ref != "" {
		return m, nil // a ref was chosen while this was in flight; keep it
	}
	if msg.Err != nil || msg.Ref == "" {
		m.ref = "main"
	} else {
		m.ref = msg.Ref
	}
	m.refHome = m.ref // the resolved branch is the one the picker's cycle must keep reachable
	return m, m.loadYAMLCmd()
}

// applyYAML parses the fetched YAML into the form and reconciles the remembered inputs against it
// (R2, R25). A stale response for a ref no longer selected is discarded (R3). A fetch or parse
// failure becomes an explicit load error naming the ref and the path, never an untyped fallback
// (R12, AC4). When the form declares an environment input it fetches the environments once (R7).
func (m Model) applyYAML(msg YAMLLoaded) (Model, tea.Cmd) {
	if msg.Ref != m.ref {
		return m, nil
	}
	m.loading = false
	if msg.Err != nil {
		m.loadErr = fmt.Sprintf("could not load %s at %s: %v", msg.Path, msg.Ref, msg.Err)
		return m, nil
	}
	form, err := ParseForm(msg.Data)
	if err != nil {
		m.loadErr = fmt.Sprintf("could not parse %s at %s: %v", msg.Path, msg.Ref, err)
		return m, nil
	}
	m.loadErr = ""
	m.form = form
	m.values = InitialValues(form, m.loadRemembered())
	m.cursor = 0
	if form.HasEnvironmentInput() {
		return m, m.loadEnvironmentsCmd()
	}
	return m, nil
}

// settle records a Dispatch's outcome in the re-submit guard, whether or not the form is still open.
// The pane issued the POST, so it must not forget it because the window closed. The outcome is
// matched to the submission that produced it rather than to whatever the pane holds now, so a second
// Workflow's outcome cannot clear the first Workflow's record.
//
// A success stays recorded with its Run, so a stray press cannot repeat it. A rejection is removed,
// because it created no Run and the same submission must stay issuable.
func (m Model) settle(msg Dispatched) Model {
	rec, ok := m.issued[msg.issued]
	if !ok {
		return m
	}
	if !rec.inflight {
		// Already settled. A record only ever moves from in flight to done, never back, so a
		// duplicate or late outcome cannot reopen a submission whose Run exists. Bubble Tea delivers
		// one message per Cmd, so this is defence in depth, which is the right posture on the path
		// that creates Runs.
		return m
	}
	if msg.Err != nil {
		delete(m.issued, msg.issued)
		return m
	}
	rec.inflight = false
	rec.runID = msg.Result.RunID
	m.issued[msg.issued] = rec
	return m
}

// showsOutcomeFor reports whether the form on screen is the one that produced a Dispatch, which is
// the only form its outcome may paint on or persist for. It matches the repository and the Workflow
// and not the ref or the inputs: those can have moved on since the POST (R3 re-fetches on a ref
// change, and R9 makes the operator retype after a reopen), and the outcome is still this form's to
// report. A different Workflow's outcome is never this form's.
func (m Model) showsOutcomeFor(t dispatchTarget) bool {
	return t.repo == m.repo && t.workflow == m.workflow.ID
}

// inflightHere reports whether a Dispatch of the Workflow now on screen has not resolved, which Open
// reads to say so rather than paint a form that looks ready. It is per Workflow rather than per pane,
// because an unrelated Workflow's POST is not this form's business.
func (m Model) inflightHere() bool {
	for k, rec := range m.issued {
		if rec.inflight && k.repo == m.repo && k.workflow == m.workflow.ID {
			return true
		}
	}
	return false
}

// applyDispatched records a Dispatch's outcome on the open form. On success it shows the Run ID from
// the response and persists the inputs so the next Dispatch of this Workflow pre-fills them (R16,
// R25, AC5). On failure it surfaces the API's own reason, which a DispatchError carries for a 422,
// 404 or 403 (R14, R22, R15).
func (m Model) applyDispatched(msg Dispatched) (Model, tea.Cmd) {
	if msg.Err != nil {
		m.status = "dispatch failed: " + msg.Err.Error()
		m.statusErr = true
		return m, nil
	}
	m.result = &msg.Result
	m.status = fmt.Sprintf("dispatched. Run %d created", msg.Result.RunID)
	m.statusErr = false
	m.saveRemembered()
	return m, nil
}

// handleKey drives the form, matching against the registry with key.Matches and never a key literal
// of its own (R7a, AC18). While a free-text field is being edited the text input consumes the key;
// otherwise motion moves the cursor, ToggleSelect flips or cycles the focused control, OpenDetail
// begins editing a text field, Dispatch submits, and CloseDetail closes the form.
func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.editing {
		return m.handleEditKey(k)
	}
	switch {
	case key.Matches(k, m.profile.CloseDetail):
		return m.Close(), nil
	case key.Matches(k, m.profile.Dispatch):
		return m.submit()
	case key.Matches(k, m.profile.RowUp):
		m.moveCursor(-1)
	case key.Matches(k, m.profile.RowDown):
		m.moveCursor(1)
	case key.Matches(k, m.profile.FirstRow):
		m.cursor = 0
	case key.Matches(k, m.profile.LastRow):
		m.cursor = m.rowCount() - 1
	case key.Matches(k, m.profile.ToggleSelect):
		return m.cycleFocused()
	case key.Matches(k, m.profile.OpenDetail):
		return m.beginEdit()
	}
	return m, nil
}

// handleEditKey drives the text input for a free-text field. FilterAccept (enter) commits the typed
// value, FilterCancel (esc) discards it, and everything else is text the input consumes. The value a
// choice or environment can take is never edited here, because those are selects, never free text
// (R10).
func (m Model) handleEditKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.profile.FilterAccept):
		m.setValue(m.editName, m.editInput.Value())
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

// beginEdit enters text-edit mode over the focused input, but only for a free-text control: a
// boolean is a toggle and a choice or environment is a select, so neither is edited as text (R6,
// R10). It seeds the input with the current value so an edit refines rather than clears it.
func (m Model) beginEdit() (Model, tea.Cmd) {
	in, ok := m.focusedInput()
	if !ok || !isFreeText(in.Type) {
		return m, nil
	}
	m.editing = true
	m.editName = in.Name
	m.editInput.SetValue(m.values[in.Name])
	return m, m.editInput.Focus()
}

// cycleFocused flips or advances the focused control by one step (R6). On the ref row it advances
// the ref and re-fetches the form for the new ref (R3, R24). On a boolean it flips true and false;
// on a choice it advances through the declared options, which is the only way its value changes, so
// a value outside the options is unreachable (R10); on an environment it advances through the fetched
// environments. A free-text control is unchanged here, because it is edited rather than cycled.
func (m Model) cycleFocused() (Model, tea.Cmd) {
	if m.cursor == 0 {
		return m.cycleRef()
	}
	in, ok := m.focusedInput()
	if !ok {
		return m, nil
	}
	switch in.Type {
	case TypeBoolean:
		if m.values[in.Name] == "true" {
			m.setValue(in.Name, "false")
		} else {
			m.setValue(in.Name, "true")
		}
	case TypeChoice:
		m.setValue(in.Name, cycle(in.Options, m.values[in.Name]))
	case TypeEnvironment:
		m.setValue(in.Name, cycle(m.environments, m.values[in.Name]))
	}
	return m, nil
}

// setValue writes one input's value. It is the one door every operator-driven change to the form
// goes through, which is what lets the submission be compared as a whole rather than tracked change
// by change.
func (m *Model) setValue(name, value string) {
	m.values[name] = value
}

// applyRefs adopts the enumerated branches and tags as the picker set (R24). The current ref is left
// alone: the operator asked for the list, not for a different ref, and R4 says which ref will run
// must never be ambiguous. A failure keeps the form on the ref it holds and says so on the ref row,
// and refsAsked stays set, so a picker that could not be listed is not retried on every press.
func (m Model) applyRefs(msg RefsLoaded) (Model, tea.Cmd) {
	m.refsLoading = false
	if msg.Err != nil {
		m.refsErr = "could not list refs: " + msg.Err.Error()
		return m, nil
	}
	m.refsErr = ""
	m.refs = msg.Refs
	return m, nil
}

// cycleRef enumerates the picker on its first use and thereafter advances to the next ref and
// re-fetches the form for it, because a Workflow's inputs can differ per branch and the form on the
// screen must always be the form for the ref that will run (R3, R24).
//
// The enumeration is lazy by R24's letter: a Dispatch at the default branch, which is most of them,
// spends nothing on a list it never looks at, and the two requests are paid by the press that asks
// to switch. With nothing to enumerate and no picker set it is a no-op and the default branch stays
// (R23).
func (m Model) cycleRef() (Model, tea.Cmd) {
	if !m.refsAsked && m.fetcher != nil {
		m.refsAsked = true // R24: at most once per picker session, whatever the enumeration returns
		m.refsLoading = true
		m.refsErr = ""
		return m, m.loadRefsCmd()
	}
	names := m.cycleNames()
	if len(names) < 2 {
		return m, nil
	}
	next := cycle(names, m.ref)
	if next == m.ref {
		return m, nil
	}
	m.ref = next
	m.loading = true
	m.loadErr = ""
	return m, m.loadYAMLCmd()
}

// cycleNames is the order the picker walks. It is the enumerated set, plus the ref the form opened
// on when the enumeration did not name it, so the loop is always closed and the ref R23 chose is
// always reachable again. Without that, a ref the listing does not carry (a SHA, or a branch past the
// pages walked) sends the first press to the alphabetically first entry and the opening ref can never
// be cycled back to, which is a one-way door on a form whose whole promise is that the ref that will
// run is never ambiguous (R4).
//
// It is keyed to the opening ref rather than to the current one, because the current one rejoins the
// enumerated set after a single press and the loop would close over the wrong thing from then on.
func (m Model) cycleNames() []string {
	names := make([]string, 0, len(m.refs)+1)
	listed := false
	for _, r := range m.refs {
		if r.Name == "" {
			continue // an unnamed ref is not a ref, and landing on one would make the target ambiguous (R4)
		}
		names = append(names, r.Name)
		if r.Name == m.refHome {
			listed = true
		}
	}
	if !listed && m.refHome != "" {
		names = append(names, m.refHome)
	}
	return names
}

// submit dispatches the form, gating first on the two things that cost no request: R14's push gate
// and R9's required inputs. An ineligible repository or a Workflow that is not dispatchable issues
// nothing and states why (R14, AC6). A required input with no value issues nothing and names the
// input (R9, AC3). Otherwise it builds the inputs map and dispatches, and the API is the final
// authority on the rest (R14).
func (m Model) submit() (Model, tea.Cmd) {
	if m.ref == "" {
		// R4: which ref a Dispatch will run must never be ambiguous, and an empty ref is the API's
		// choice rather than the operator's. Nothing reaches here today; it is stated because Target
		// is an opener-filled struct and the cost of being wrong is a Run on an unintended ref.
		m.status = "no ref is selected, so there is nothing to dispatch"
		m.statusErr = true
		return m, nil
	}
	if m.loadErr != "" || !m.form.Dispatchable {
		m.status = "this Workflow declares no workflow_dispatch trigger at this ref"
		m.statusErr = true
		return m, nil
	}
	if !m.eligible {
		m.status = "dispatch unavailable: " + m.eligReason
		m.statusErr = true
		return m, nil
	}
	if missing := m.missingRequired(); missing != "" {
		m.status = "required input needs a value: " + missing
		m.statusErr = true
		return m, nil
	}
	// The guard comes after the gates above so it only ever speaks about a submission that would
	// otherwise have gone out. This exact Dispatch, the same Workflow at the same ref with the same
	// inputs, has already been sent, so sending it again would create a duplicate Run. Changing the
	// ref or any input makes the next submit a different key with nothing recorded against it, which
	// is how a deliberate second Dispatch gets through and an accidental one does not.
	key := m.target()
	if rec, ok := m.issued[key]; ok {
		m.status = refusal(rec)
		m.statusErr = true
		return m, nil
	}
	if m.dispatcher == nil {
		return m, nil
	}
	if m.issued == nil {
		m.issued = make(map[dispatchTarget]issueState)
	}
	m.issued[key] = issueState{inflight: true}
	m.status = statusDispatching
	m.statusErr = false
	return m, m.dispatchCmd(key)
}

// refusal states why a submission was not sent, and how to make a genuinely new one. The Run is
// named where one is recorded, which is every settled success under R16: the response carries the
// Run ID, so a settled Dispatch with none is a 200 the API is not documented to return, and printing
// "Run 0" for it would be worse than saying nothing.
//
// It is kept short on purpose. The pane wraps nothing, so a line long enough to exceed the terminal
// wraps in the renderer, and this is the line standing between a keypress and a duplicate Run.
func refusal(rec issueState) string {
	switch {
	case rec.inflight:
		return "this Dispatch is already in flight"
	case rec.runID == 0:
		return "already dispatched. Change the ref or an input to dispatch again"
	default:
		return fmt.Sprintf("already dispatched as Run %d. Change the ref or an input to dispatch again", rec.runID)
	}
}

// missingRequired returns the name of the first required input with no value, or empty when every
// required input is filled (R9). A value of only whitespace does not satisfy a required input.
func (m Model) missingRequired() string {
	for _, in := range m.form.Inputs {
		if in.Required && strings.TrimSpace(m.values[in.Name]) == "" {
			return in.Name
		}
	}
	return ""
}

// buildInputs is the inputs map sent to the API and persisted (R16, R25): every declared input's
// current value, keyed by name. The API receives each as a string, which is what the Workflow reads
// them as anyway.
func (m Model) buildInputs() map[string]string {
	out := make(map[string]string, len(m.form.Inputs))
	for _, in := range m.form.Inputs {
		out[in.Name] = m.values[in.Name]
	}
	return out
}

// serializedInputLength is the character length of the serialised inputs, which R13 surfaces against
// the community-sourced limit without enforcing anything on it.
func (m Model) serializedInputLength() int {
	b, err := json.Marshal(m.buildInputs())
	if err != nil {
		return 0
	}
	return len(b)
}

// dispatchCmd issues the Dispatch through ops on a Cmd, returning a Dispatched message tagged with
// the submission that produced it, so the guard settles the right record however many are in flight.
// With no dispatcher wired it is nil, which is the golden path.
func (m Model) dispatchCmd(key dispatchTarget) tea.Cmd {
	if m.dispatcher == nil {
		return nil
	}
	d := m.dispatcher
	req := ops.DispatchRequest{Repo: m.repo, WorkflowID: m.workflow.ID, Ref: m.ref, Inputs: m.buildInputs()}
	return func() tea.Msg {
		res, err := d.Dispatch(context.Background(), req)
		return Dispatched{Result: res, Err: err, issued: key}
	}
}

// resolveRefCmd fetches the repository's default branch, returning a RefResolved (R23). With no
// fetcher wired it is nil, and the pane stays on whatever ref it holds.
func (m Model) resolveRefCmd() tea.Cmd {
	if m.fetcher == nil {
		return nil
	}
	f, repo := m.fetcher, m.repo
	return func() tea.Msg {
		ref, err := f.DefaultBranch(repo)
		return RefResolved{Ref: ref, Err: err}
	}
}

// loadYAMLCmd fetches the Workflow's YAML at the current ref, returning a YAMLLoaded. With no fetcher
// wired it is nil (the golden path injects the message directly).
func (m Model) loadYAMLCmd() tea.Cmd {
	if m.fetcher == nil {
		return nil
	}
	f, repo, path, ref := m.fetcher, m.repo, m.workflow.Path, m.ref
	return func() tea.Msg {
		data, err := f.WorkflowYAML(repo, path, ref)
		return YAMLLoaded{Ref: ref, Path: path, Data: data, Err: err}
	}
}

// loadRefsCmd enumerates the repository's branches and tags once per picker session, returning a
// RefsLoaded (R24). With no fetcher wired it is nil, and cycleRef never reaches it.
func (m Model) loadRefsCmd() tea.Cmd {
	if m.fetcher == nil {
		return nil
	}
	f, repo := m.fetcher, m.repo
	return func() tea.Msg {
		refs, err := f.Refs(repo)
		return RefsLoaded{Refs: refs, Err: err}
	}
}

// loadEnvironmentsCmd fetches the repository's environments once, returning an EnvironmentsLoaded
// (R7). With no fetcher wired it is nil.
func (m Model) loadEnvironmentsCmd() tea.Cmd {
	if m.fetcher == nil {
		return nil
	}
	f, repo := m.fetcher, m.repo
	return func() tea.Msg {
		envs, err := f.Environments(repo)
		return EnvironmentsLoaded{Environments: envs, Err: err}
	}
}

// loadRemembered reads this Workflow's last-used inputs from the local-store, keyed by the
// host-qualified repository and the Workflow path (R25, R2a). A missing or unreadable document reads
// as nothing remembered, so the form falls back to the declared defaults.
func (m Model) loadRemembered() map[string]string {
	if m.store == nil {
		return nil
	}
	var v map[string]string
	if m.store.LoadDoc(docName(m.repo, m.workflow.Path), &v) {
		return v
	}
	return nil
}

// saveRemembered persists this Workflow's last-used inputs on a successful Dispatch, keyed by the
// host-qualified repository and the Workflow path (R25, R2a). It is best-effort: a write failure
// costs only the next pre-fill (local-store R11), so it is not surfaced.
func (m Model) saveRemembered() {
	if m.store == nil {
		return
	}
	m.store.SaveDoc(docName(m.repo, m.workflow.Path), m.buildInputs())
}

// docName is R25's local-store key: the host-qualified repository and the Workflow path. The store
// hashes it to a safe filename, so the raw string only needs to be stable and unique per Workflow.
func docName(repo domain.RepoID, path string) string {
	return "dispatch-inputs/" + repo.String() + "/" + path
}

// rowCount is the number of focusable rows: the ref row plus one per input.
func (m Model) rowCount() int { return 1 + len(m.form.Inputs) }

// focusedInput is the input under the cursor, and whether the cursor is on an input rather than the
// ref row.
func (m Model) focusedInput() (InputSpec, bool) {
	if m.cursor <= 0 || m.cursor-1 >= len(m.form.Inputs) {
		return InputSpec{}, false
	}
	return m.form.Inputs[m.cursor-1], true
}

// moveCursor moves the cursor by delta, clamped to the focusable rows.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if n := m.rowCount(); m.cursor >= n {
		m.cursor = n - 1
	}
}

// isFreeText reports whether a control is edited as text rather than toggled or selected (R6): a
// string, a number, or an unrecognised type rendered as free text (R11).
func isFreeText(t InputType) bool {
	return t == TypeString || t == TypeNumber || t == TypeUnrecognized
}

// cycle returns the option after current in options, wrapping, and the first option when current is
// absent. An empty options slice returns current unchanged, so an environment select with no fetched
// environments does not blank its value.
func cycle(options []string, current string) string {
	if len(options) == 0 {
		return current
	}
	for i, o := range options {
		if o == current {
			return options[(i+1)%len(options)]
		}
	}
	return options[0]
}

// SetProfile adopts the keybinding profile the operator chose in Settings, pushed down from
// the root through the Workflows tab that owns this pane (settings R5, R17).
func (m Model) SetProfile(p keys.Profile) Model {
	m.profile = p
	return m
}
