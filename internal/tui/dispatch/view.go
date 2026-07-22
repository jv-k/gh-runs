package dispatch

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jv-k/gh-runs/v2/internal/textsan"
)

// Layout constants. The form renders from held state alone at a fixed geometry, so a golden over
// View() is byte-stable (R21). lipgloss v2 renders truecolour regardless of TERM or NO_COLOR, so
// the bytes are stable on any machine (ADR-0013).
const (
	nameW   = 16
	minW    = 60
	indent  = "  "
	marker  = "> "
	nomark  = "  "
	selOpen = "‹ "
	selClz  = " ›"
)

// Styles mirror the confirm pane's palette so the two panes read as one product.
var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	styleErr   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f5f"))
	styleOK    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fd75f"))
	styleFocus = lipgloss.NewStyle().Bold(true)
)

// View renders the form from held state alone, with no live terminal and no network (R21). It is
// empty while closed, an explicit error while the YAML could not be loaded or parsed (R12, AC4), a
// pending line while loading, and the typed form once the schema is held. It never degrades to an
// untyped key=value surface (R12).
func (m Model) View() string {
	if !m.open {
		return ""
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("Dispatch: " + textsan.Sanitize(m.workflow.Name)))
	b.WriteString("\n")
	b.WriteString(m.refLine())
	b.WriteString("\n\n")

	switch {
	case m.loadErr != "":
		// R12, AC4: an explicit failure naming the ref and the path, and no key=value fallback.
		b.WriteString(styleErr.Render(indent + textsan.Sanitize(m.loadErr)))
		b.WriteString("\n")
		b.WriteString(m.footer())
		return b.String()
	case m.loading:
		b.WriteString(styleDim.Render(indent + "loading the form for " + textsan.Sanitize(m.ref) + "…"))
		b.WriteString("\n")
		b.WriteString(m.footer())
		return b.String()
	case !m.form.Dispatchable:
		b.WriteString(styleDim.Render(indent + "this Workflow declares no workflow_dispatch trigger at " + textsan.Sanitize(m.ref)))
		b.WriteString("\n")
		b.WriteString(m.footer())
		return b.String()
	}

	for i, in := range m.form.Inputs {
		b.WriteString(m.inputBlock(i, in))
	}
	if notes := m.limitNotes(); len(notes) > 0 {
		b.WriteString("\n")
		for _, n := range notes {
			b.WriteString(styleDim.Render(indent + n))
			b.WriteString("\n")
		}
	}
	b.WriteString(m.footer())
	return b.String()
}

// refLine is R4's always-visible target ref, focusable as the first row so a picker can advance it
// (R23, R24). It labels the ref a branch or a tag when the picker knows which (R24).
func (m Model) refLine() string {
	mark := nomark
	label := "Ref: " + textsan.Sanitize(m.ref)
	if m.cursor == 0 {
		mark = marker
		label = styleFocus.Render(label)
	}
	kind := m.refKind()
	if kind != "" {
		label += styleDim.Render("  (" + kind + ")")
	}
	if len(m.refs) > 1 && m.cursor == 0 {
		label += styleDim.Render("  " + m.profile.ToggleSelect.Help().Key + " to switch")
	}
	return mark + label
}

// refKind labels the current ref a branch or a tag from the picker set, or empty when the set does
// not name it (R24).
func (m Model) refKind() string {
	for _, r := range m.refs {
		if r.Name == m.ref {
			if r.IsTag {
				return "tag"
			}
			return "branch"
		}
	}
	return ""
}

// inputBlock renders one input's control line and, where the author declared one, its sanitised
// description on a dimmed line (R6, R8). The row under the cursor carries the marker.
func (m Model) inputBlock(i int, in InputSpec) string {
	mark := nomark
	name := textsan.Sanitize(in.Name)
	if in.Required {
		name += " *"
	}
	label := pad(name, nameW)
	if m.cursor == i+1 {
		mark = marker
		label = styleFocus.Render(label)
	}
	line := mark + label + "  " + m.control(in) + "  " + styleDim.Render(m.annotation(in))
	out := line + "\n"
	if in.Description != "" {
		out += styleDim.Render(indent+indent+textsan.Sanitize(in.Description)) + "\n"
	}
	return out
}

// control renders the type-appropriate widget with its current value (R6): free text in brackets, a
// boolean as a checkbox, and a choice or environment in select chevrons. R10's promise that a choice
// is a select rather than free text is a claim about this widget. While a free-text field is being
// edited the text input is shown so the operator sees the cursor.
func (m Model) control(in InputSpec) string {
	v := textsan.Sanitize(m.values[in.Name])
	switch in.Type {
	case TypeBoolean:
		if m.values[in.Name] == "true" {
			return "[x] true"
		}
		return "[ ] false"
	case TypeChoice, TypeEnvironment:
		return selOpen + v + selClz
	default: // string, number, unrecognised: free text
		if m.editing && m.editName == in.Name {
			return m.editInput.View()
		}
		return "[ " + v + " ]"
	}
}

// annotation is the dimmed type note beside a control (R6, R8, R11): a choice lists its options so
// the select is legible, an environment lists the fetched environments when they are loaded, an
// unrecognised type names the author's declared value (R11), and a required input says so (R8).
func (m Model) annotation(in InputSpec) string {
	var base string
	switch in.Type {
	case TypeChoice:
		base = "choice: " + strings.Join(sanitizeAll(in.Options), " | ")
	case TypeEnvironment:
		if len(m.environments) > 0 {
			base = "environment: " + strings.Join(sanitizeAll(m.environments), " | ")
		} else {
			base = "environment"
		}
	case TypeUnrecognized:
		base = "unrecognised: " + textsan.Sanitize(in.RawType)
	case TypeNumber:
		base = "number"
	case TypeBoolean:
		base = "boolean"
	default:
		base = "string"
	}
	if in.Required {
		base += ", required"
	}
	return "(" + base + ")"
}

// limitNotes surfaces R13's two limits without enforcing either: the authoritative 25-input cap
// when the Workflow declares more, and the community-sourced, unverified character limit when the
// serialised inputs exceed it, labelled as such rather than attributed to GitHub.
func (m Model) limitNotes() []string {
	var notes []string
	if n := len(m.form.Inputs); n > MaxInputs {
		notes = append(notes, "this Workflow declares "+strconv.Itoa(n)+" inputs; GitHub's dispatch schema caps them at "+strconv.Itoa(MaxInputs)+" (authoritative), so the API will reject the extra")
	}
	if n := m.serializedInputLength(); n > MaxInputChars {
		notes = append(notes, "serialised inputs are "+strconv.Itoa(n)+" characters; a community-sourced, unverified limit of "+strconv.Itoa(MaxInputChars)+" may apply (it is not in GitHub's REST docs)")
	}
	return notes
}

// footer renders the pending, result or error status and the key hints. On a successful Dispatch it
// shows the Run the response returned (R16, AC5); an error is the API's own reason (R14, R22).
func (m Model) footer() string {
	var b strings.Builder
	b.WriteString("\n")
	if m.status != "" {
		style := styleOK
		if m.statusErr {
			style = styleErr
		}
		b.WriteString(style.Render(indent + textsan.Sanitize(m.status)))
		if m.result != nil && m.result.HTMLURL != "" {
			b.WriteString(styleDim.Render("  " + textsan.Sanitize(m.result.HTMLURL)))
		}
		b.WriteString("\n")
	}
	b.WriteString(styleDim.Render(indent + m.hint()))
	return b.String()
}

// hint names the keys the pane acts on, drawn from the registry so it advertises exactly the
// bindings it matches (AC18).
func (m Model) hint() string {
	return fmt.Sprintf("%s edit  %s toggle/cycle  %s dispatch  %s close",
		m.profile.OpenDetail.Help().Key,
		m.profile.ToggleSelect.Help().Key,
		m.profile.Dispatch.Help().Key,
		m.profile.CloseDetail.Help().Key,
	)
}

// pad right-pads s to w columns, leaving a longer value untouched. Width is rune count, which equals
// display width for the ASCII input names carry.
func pad(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// sanitizeAll sanitises every string in a slice, for the author-controlled option and environment
// lists painted in an annotation.
func sanitizeAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = textsan.Sanitize(x)
	}
	return out
}
