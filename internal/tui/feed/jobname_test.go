package feed

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sebdah/goldie/v2"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/keys"
	"github.com/jv-k/gh-runs/v2/internal/ops"
)

// typeInto sends each rune of s to the model as a key press, so a test types a Job name
// the way an operator does.
func typeInto(m Model, s string) Model {
	for _, r := range s {
		m = m.Update2(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// TestJobNameFormOpensOverTheSelectionAndCapturesInput pins the form's opening. It is a
// text input like the filter's, so while it is up the tab captures every key: a name
// holding d, c or R must reach the input rather than start a deletion, a cancel or a
// whole-Run re-run (ADR-0011's routing, R7).
func TestJobNameFormOpensOverTheSelectionAndCapturesInput(t *testing.T) {
	m, _ := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("J"))

	if !m.CapturesInput() {
		t.Fatal("the by-name form is open but the tab does not capture input; a typed d would delete")
	}
	m = typeInto(m, "build")
	if got := m.jobNameInput.Value(); got != "build" {
		t.Errorf("the input holds %q, want %q: the keys reached the list instead", got, "build")
	}
}

// TestJobNameFormCancelsWithoutResolving pins the abort path. Escape closes the form and
// issues nothing: the resolution costs one request per selected Run, so a form abandoned
// before it is accepted must not spend any of them.
func TestJobNameFormCancelsWithoutResolving(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("J"))
	m = typeInto(m, "build")
	m, cmd := m.Update(press("esc"))

	if m.CapturesInput() {
		t.Error("escape left the by-name form open")
	}
	if cmd != nil {
		t.Error("escape returned a command; an abandoned form resolves nothing")
	}
	if spy.resolved != 0 {
		t.Errorf("the form resolved %d times after being abandoned, want 0", spy.resolved)
	}
}

// TestJobNameFormResolvesThenConfirms pins the whole path R17a describes: the name is
// resolved against each selected Run before R17's count is frozen, and only then does the
// modal open. The frozen set is what resolution resolved, so the Runs that matched become
// Job Items and the Run that did not becomes an Item-less member inside the same count.
func TestJobNameFormResolvesThenConfirms(t *testing.T) {
	m, spy := feedWithSpy(t)
	spy.resolution = ops.Resolution{
		Items: []ops.Item{ops.JobItem(domain.Job{ID: 9001, RunID: 1, Repo: repoID("o", "r"), Name: "build"})},
		Unmatched: []ops.Unmatched{
			{Repo: repoID("o", "r"), RunID: 2, Reason: `no job named "build" in this run`},
		},
	}
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // select Run 1
	m = m.Update2(press("down"))
	m = m.Update2(press("space")) // select Run 2
	m = m.Update2(press("J"))
	m = typeInto(m, "build")
	m, cmd := m.Update(press("enter"))

	if cmd == nil {
		t.Fatal("accepting the form returned no command; the resolution has to leave the update loop")
	}
	if spy.resolved != 0 {
		t.Fatal("the resolution ran inside Update; it issues one request per Run and must not block the loop")
	}
	m = m.Update2(cmd())
	if spy.resolved != 1 || spy.resolvedName != "build" {
		t.Fatalf("resolved %d times for %q, want once for \"build\"", spy.resolved, spy.resolvedName)
	}
	if !m.confirmOpen {
		t.Fatal("the resolution did not open the confirmation")
	}
	p := m.confirm.Plan()
	if p.Operation() != ops.OpRerunJob {
		t.Errorf("the modal priced %q, want a per-Job re-run", p.Operation())
	}
	if p.Total() != 2 || len(p.Items()) != 1 || len(p.Unmatched()) != 1 {
		t.Errorf("frozen set = total %d, %d Items, %d unmatched; want 2/1/1 (AC14c)",
			p.Total(), len(p.Items()), len(p.Unmatched()))
	}
}

// TestJobNameFormRendersTheUnreachedNote pins R17a's mandated note on this surface. A
// resolution that did not reach every selected Run freezes a count smaller than the set the
// operator named, and nothing in the friction machinery would say so.
func TestJobNameFormRendersTheUnreachedNote(t *testing.T) {
	m, spy := feedWithSpy(t)
	spy.resolution = ops.Resolution{
		Items:     []ops.Item{ops.JobItem(domain.Job{ID: 9001, RunID: 1, Repo: repoID("o", "r"), Name: "build"})},
		Unreached: 28,
		Reason:    "the API rate-limited the jobs listing",
	}
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("J"))
	m = typeInto(m, "build")
	m, cmd := m.Update(press("enter"))
	m = m.Update2(cmd())

	if !m.confirmOpen {
		t.Fatal("the resolution did not open the confirmation")
	}
	view := m.View()
	if !strings.Contains(view, "28") {
		t.Errorf("the modal states no count for the 28 Runs the resolution did not reach (R17a):\n%s", view)
	}
	if !strings.Contains(view, "rate-limited") {
		t.Errorf("the note does not say why the resolution stopped (R17a):\n%s", view)
	}
}

// TestJobNameFormIsInertWithNothingSelected pins the form's one refusal. With no Run under
// the cursor and nothing selected there is no set to resolve a name against, so the key
// does nothing rather than opening a form that can only be abandoned.
func TestJobNameFormIsInertWithNothingSelected(t *testing.T) {
	spy := newLaunchSpy()
	m := New(Options{Profile: keys.Standard, Ops: spy})
	m = m.Update2(tea.WindowSizeMsg{Width: 100, Height: 24})

	m = m.Update2(press("J"))

	if m.CapturesInput() {
		t.Error("the by-name form opened over an empty Feed; there is no set to resolve against")
	}
}

// TestJobNameFormRefusesAnEmptyName pins the second refusal. An empty name matches no Job
// in any Run, so accepting it would spend one request per selected Run to produce a frozen
// set of nothing but Item-less members.
func TestJobNameFormRefusesAnEmptyName(t *testing.T) {
	m, spy := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("J"))
	m, cmd := m.Update(press("enter"))

	if cmd != nil {
		t.Error("an empty name returned a command; it resolves nothing")
	}
	if spy.resolved != 0 {
		t.Errorf("an empty name resolved %d times, want 0", spy.resolved)
	}
	if !m.CapturesInput() {
		t.Error("an empty name closed the form; it should stay open for the operator to type one")
	}
}

// TestGoldenJobNameForm fixes the form's frame. It sits where the filter input sits,
// because it is the same kind of thing: a text input the tab captures every key for while
// it is open. The list stays visible behind it, unlike the confirmation modal, because
// nothing is frozen yet and the operator is still looking at the Runs they selected.
func TestGoldenJobNameForm(t *testing.T) {
	m, _ := feedWithSpy(t)
	m = m.Update2(press("down"))
	m = m.Update2(press("space"))
	m = m.Update2(press("J"))
	m = typeInto(m, "build")

	goldie.New(t).Assert(t, "job_name_form", []byte(m.View()))
}
