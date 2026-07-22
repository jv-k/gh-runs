// Package dispatch is the workflow_dispatch form pane (workflow-dispatch R1 to R25). It is a
// pane, not a tab and not a tea.Model: it exposes View() string and an Update its opener drives,
// and it is imported by the Workflows tab that opens it over a selected Workflow (ADR-0011's pane
// contract). A pane may add a package without an ADR, which is what this one is.
//
// The form is generated from a Workflow's YAML at the ref about to run, never from the Workflow
// object, which carries neither the events it declares nor its inputs (R1, Constraints). So the
// pane fetches the YAML through a read seam, parses it here into a typed schema, and paints one
// control per input by its declared type (R6). The input DEFINITIONS come from a file an author on
// a fanned-out repository controls, so the view sanitises every author-controlled string through
// textsan before painting; this parser holds them verbatim.
//
// The Dispatch itself is a write, and a tab renders and calls but never issues a write of its own
// (ADR-0011): the pane calls ops.Dispatch, which sends return_run_details:true and reads the Run
// ID from the response (R16). The retired correlation poll (R17 to R19) is never built.
package dispatch

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// InputType is the declared type of a workflow_dispatch input (R6). Its zero value is the
// unrecognised type (R11): a type the author declared that is not one of R6's five.
type InputType string

const (
	TypeString       InputType = "string"
	TypeBoolean      InputType = "boolean"
	TypeChoice       InputType = "choice"
	TypeNumber       InputType = "number"
	TypeEnvironment  InputType = "environment"
	TypeUnrecognized InputType = ""
)

// MaxInputs is GitHub's OpenAPI maxProperties on this endpoint's inputs object, and it is
// authoritative (R13): the schema itself refuses a 26th input, so the API's rejection is
// predictable rather than opaque. The form surfaces a Workflow declaring more than this.
const MaxInputs = 25

// MaxInputChars is the community-sourced, UNVERIFIED total-payload character limit (R13). It is
// absent from the official REST documentation for this endpoint and traces to community discussion
// 120093, so the form surfaces it labelled as such and enforces nothing on it. The two limits do
// not carry equal weight, and the form must not pretend they do: only the API's rejection is a true
// signal for this one.
const MaxInputChars = 65535

// InputSpec is one declared workflow_dispatch input (R6, R8). Every field but Name is
// author-controlled YAML, so a surface sanitises Name, Description, Default and Options before
// painting (textsan). Options is populated for choice inputs alone; an environment select draws its
// options from the repository's environments, fetched separately (R7).
type InputSpec struct {
	Name        string
	Type        InputType
	RawType     string // the author's declared type verbatim, for R11's unrecognised label
	Description string
	Default     string
	Required    bool
	Options     []string // choice only (R6, R10)
}

// Form is a Workflow's parsed workflow_dispatch schema (R1). Dispatchable is whether the YAML
// declares the trigger at all, a fact only the YAML carries.
type Form struct {
	Dispatchable bool
	Inputs       []InputSpec
}

// HasEnvironmentInput reports whether any input is an environment select. It decides whether the
// form fetches the repository's environments at all: a form with no environment input must not
// fetch them, and one with several fetches them at most once per render (R7).
func (f Form) HasEnvironmentInput() bool {
	for _, in := range f.Inputs {
		if in.Type == TypeEnvironment {
			return true
		}
	}
	return false
}

// ParseForm resolves a Workflow's dispatchability and its input schema from its YAML (R1). A parse
// failure is an error the caller surfaces naming the ref and the path (R12), never a silent empty
// form that would degrade into the untyped key=value entry R12 forbids. Declared input order is
// preserved, so the form renders inputs as the author wrote them.
func ParseForm(data []byte) (Form, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Form{}, fmt.Errorf("parsing the workflow YAML failed: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil {
		return Form{}, errors.New("the workflow YAML has no mapping at its root")
	}
	on := mappingValue(root, "on")
	if on == nil {
		return Form{}, nil // no `on` key, so nothing is dispatchable and no inputs exist (R1)
	}
	wd, dispatchable := workflowDispatchNode(on)
	if !dispatchable {
		return Form{}, nil
	}
	form := Form{Dispatchable: true}
	if wd != nil && wd.Kind == yaml.MappingNode {
		if inputs := mappingValue(wd, "inputs"); inputs != nil && inputs.Kind == yaml.MappingNode {
			form.Inputs = parseInputs(inputs)
		}
	}
	return form, nil
}

// InitialValues reconciles a remembered input map against the current schema and returns the
// pre-fill for the form (R25, AC9). Each input pre-fills from its remembered value only where that
// value still validates: a choice value still among its options, a boolean value still a boolean.
// Anything that no longer fits falls back to the declared default (R8), and an input the schema
// dropped is simply absent because only current inputs are keyed. With no remembered map every
// input takes its default, so a deleted local-store returns the form to its declared defaults.
func InitialValues(form Form, remembered map[string]string) map[string]string {
	out := make(map[string]string, len(form.Inputs))
	for _, in := range form.Inputs {
		if v, ok := remembered[in.Name]; ok && valueFits(in, v) {
			out[in.Name] = v
			continue
		}
		out[in.Name] = in.Default
	}
	return out
}

// valueFits reports whether a remembered value still satisfies an input's schema (R25). A choice
// must still be one of its options (R10) and a boolean must still be a boolean; every other type
// accepts any string, and a value the API would reject is caught by the API as the final authority
// rather than pre-judged here.
func valueFits(in InputSpec, v string) bool {
	switch in.Type {
	case TypeChoice:
		for _, o := range in.Options {
			if o == v {
				return true
			}
		}
		return false
	case TypeBoolean:
		return v == "true" || v == "false"
	default:
		return true
	}
}

// parseInputs builds one InputSpec per declared input in declared order, which yaml.v3 preserves in
// a mapping node's content (R6).
func parseInputs(inputs *yaml.Node) []InputSpec {
	out := make([]InputSpec, 0, len(inputs.Content)/2)
	for i := 0; i+1 < len(inputs.Content); i += 2 {
		out = append(out, parseInput(inputs.Content[i].Value, inputs.Content[i+1]))
	}
	return out
}

// parseInput reads one input's declared type, description, default, required flag and options
// (R6, R8). A missing type defaults to string, which is how workflow_dispatch treats an input with
// no declared type; a declared type outside R6's five is unrecognised, keeping the author's verbatim
// value for R11's label rather than erroring.
func parseInput(name string, node *yaml.Node) InputSpec {
	spec := InputSpec{Name: name, Type: TypeString}
	if node == nil || node.Kind != yaml.MappingNode {
		return spec
	}
	if d := mappingValue(node, "description"); d != nil {
		spec.Description = d.Value
	}
	if t := mappingValue(node, "type"); t != nil && t.Value != "" {
		spec.Type, spec.RawType = resolveType(t.Value)
	}
	if d := mappingValue(node, "default"); d != nil && d.Kind == yaml.ScalarNode {
		spec.Default = d.Value
	}
	if r := mappingValue(node, "required"); r != nil {
		spec.Required = r.Value == "true"
	}
	if o := mappingValue(node, "options"); o != nil && o.Kind == yaml.SequenceNode {
		for _, item := range o.Content {
			if item.Kind == yaml.ScalarNode {
				spec.Options = append(spec.Options, item.Value)
			}
		}
	}
	return spec
}

// resolveType maps a declared type string to its InputType, returning TypeUnrecognized with the raw
// declared value for a type outside R6's five (R11).
func resolveType(declared string) (InputType, string) {
	switch declared {
	case "string":
		return TypeString, ""
	case "boolean":
		return TypeBoolean, ""
	case "choice":
		return TypeChoice, ""
	case "number":
		return TypeNumber, ""
	case "environment":
		return TypeEnvironment, ""
	default:
		return TypeUnrecognized, declared
	}
}

// documentRoot returns the mapping at the root of a parsed YAML document, or nil where there is
// none (an empty file, or a non-mapping root).
func documentRoot(doc *yaml.Node) *yaml.Node {
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind == yaml.MappingNode {
		return n
	}
	return nil
}

// mappingValue returns the value node for key in a mapping node, or nil. It compares the key node's
// verbatim Value, so the YAML 1.1 `on`-is-true gotcha does not apply: yaml.v3 keeps `on` a string
// key, and this reads that string.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// workflowDispatchNode resolves whether the `on` node declares workflow_dispatch, and returns its
// value node where `on` is a mapping (the only shape that can carry inputs). A bare scalar or a
// sequence declares the trigger with no inputs, and a mapping whose workflow_dispatch value is null
// is dispatchable with no inputs too (R1).
func workflowDispatchNode(on *yaml.Node) (node *yaml.Node, dispatchable bool) {
	switch on.Kind {
	case yaml.ScalarNode:
		return nil, on.Value == "workflow_dispatch"
	case yaml.SequenceNode:
		for _, item := range on.Content {
			if item.Kind == yaml.ScalarNode && item.Value == "workflow_dispatch" {
				return nil, true
			}
		}
		return nil, false
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if on.Content[i].Value == "workflow_dispatch" {
				return on.Content[i+1], true
			}
		}
		return nil, false
	default:
		return nil, false
	}
}
