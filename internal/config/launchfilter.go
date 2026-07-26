package config

import (
	"fmt"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/jv-k/gh-runs/v2/internal/filter"
)

// The launch filter is settings R9: the filter the Feed opens with, stored as ADR-0016's
// structured Filter rather than as the CLI's permissive -s/--status string, whose
// conflation of Status and Conclusion is a poor thing to persist. So the file spells the
// two as distinct keys, and a value written under the wrong one is refused by name rather
// than quietly classified:
//
//	launch_filter:
//	  branch: main
//	  status: [queued, in_progress]
//	  conclusion: [failure]
//
// Every sub-key is optional, exactly as every top-level key is (R3). An absent launch
// filter is the zero Filter, which matches every Run.
//
// The repository axis ADR-0016 gives Filter has no sub-key here, deliberately. R17 makes the
// view and the file the same settings, the view edits this filter in the input grammar
// filter owns, and that grammar carries no repository token: filter's QueryString says so in
// its own doc, and its file records that token spellings are that package's choice rather
// than the canon's. A stored repos key would therefore be a setting the view could neither
// show nor edit, which is the half of R17 a key like that breaks.
//
// This is not ADR-0016's decision, and that ADR leans the other way: its repository-axis
// section names "the Feed's filter input" as the consumer that needs Repos. So the canon
// expects that surface to carry the axis and the grammar does not, which is a disagreement
// worth naming rather than resolving here. Issue #102 holds it, and settings R9's note
// records what it costs.
const (
	axisBranch     = "branch"
	axisCommit     = "commit"
	axisActor      = "actor"
	axisEvent      = "event"
	axisWorkflow   = "workflow"
	axisCreated    = "created"
	axisStatus     = "status"
	axisConclusion = "conclusion"
)

// launchFilterAxes is the file's spelling of the axes above, in the order the marshaller
// writes them, which is the order ADR-0016's type declares them. The writer walks this
// list and the loader switches on the same constants, so a key one half knows is a key
// the other half knows.
var launchFilterAxes = []string{
	axisBranch, axisCommit, axisActor, axisEvent,
	axisWorkflow, axisCreated, axisStatus, axisConclusion,
}

// resolveLaunchFilter decodes the launch_filter key (R9). A node that is not a mapping is
// the wrong shape outright: the whole filter falls back to the empty default and says so
// (R14), because a filter half-read from a scalar would narrow the Feed by something
// nobody wrote. Inside a mapping each axis is resolved from its own node, so one bad
// clause drops that clause alone and the rest of the filter stands, which is the rule
// resolveFile already applies to the file's top level. Sub-keys are visited in sorted
// order so the diagnostics are stable.
func resolveLaunchFilter(key string, node yaml.Node, diags []Diagnostic) (filter.Filter, []Diagnostic) {
	var raw map[string]yaml.Node
	if node.Decode(&raw) != nil {
		return filter.Filter{}, append(diags, typeErr(key, "a mapping of filter axes", node))
	}
	subs := make([]string, 0, len(raw))
	for k := range raw {
		subs = append(subs, k)
	}
	sort.Strings(subs)

	var f filter.Filter
	for _, sub := range subs {
		item := raw[sub]
		if item.Tag == "!!null" {
			continue // a present but empty axis is treated as absent, as a top-level key is
		}
		path := key + "." + sub
		switch sub {
		case axisBranch:
			f.Branch, diags = filterScalar(path, item, "a branch name", diags)
		case axisCommit:
			f.Commit, diags = filterScalar(path, item, "a commit SHA", diags)
		case axisActor:
			f.Actor, diags = filterScalar(path, item, "a login", diags)
		case axisEvent:
			f.Event, diags = filterScalar(path, item, "an event name", diags)
		case axisWorkflow:
			f.Workflow, diags = filterScalar(path, item, "a Workflow name, filename or ID", diags)
		case axisCreated:
			f.Created, diags = filterCreated(path, item, diags)
		case axisStatus, axisConclusion:
			diags = resolveStatusAxis(path, sub, item, &f, diags)
		default:
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"%s: unrecognised filter axis, ignored", path)})
		}
	}
	return f, diags
}

// filterScalar reads one free-form axis. It takes the scalar's own text rather than
// decoding into a string, because a Workflow selector may be a numeric ID (ADR-0016) and
// YAML types `workflow: 12345` as an int, which a string decode would refuse for a value
// the axis explicitly admits. Anything that is not a scalar is the wrong shape: that axis
// falls back to empty and names itself (R14), leaving the rest of the filter alone.
func filterScalar(path string, node yaml.Node, want string, diags []Diagnostic) (string, []Diagnostic) {
	if node.Kind != yaml.ScalarNode {
		return "", append(diags, typeErr(path, want, node))
	}
	return node.Value, diags
}

// filterCreated parses the created clause through filter.ParseCreated, the same door every
// other consumer uses, so gh's date syntax is validated once and a bad range is rejected by
// name before anything reads it (ADR-0016, cli-surface R6).
func filterCreated(path string, node yaml.Node, diags []Diagnostic) (filter.DateRange, []Diagnostic) {
	if node.Kind != yaml.ScalarNode {
		return filter.DateRange{}, append(diags, typeErr(path, "a date or date range", node))
	}
	dr, err := filter.ParseCreated(node.Value)
	if err != nil {
		return filter.DateRange{}, append(diags, Diagnostic{Message: fmt.Sprintf(
			"%s: %v (line %d); ignoring it", path, err, node.Line)})
	}
	return dr, diags
}

// resolveStatusAxis decodes one half of ADR-0016's permissive pair into its own typed set.
// axis is the key being read, axisStatus or axisConclusion, and it is also the axis a value
// must classify into: the status key fills Statuses and the conclusion key fills
// Conclusions, which is exactly the distinction R9 asks the stored form to keep.
//
// Every value is classified through filter.ParseStatus, the single validation point each
// consumer shares (ADR-0016), so an unknown value is refused by the same code and the same
// message a flag or the Feed's filter input would refuse it with. A value that is real but
// written under the other key is named as such and dropped rather than quietly moved,
// because quietly moving it is the conflation R9 exists to refuse. Values that classify
// correctly are kept, mirroring the exclude list's rule: dropping a whole clause over one
// typo would widen the filter where the operator asked to narrow it.
func resolveStatusAxis(path, axis string, node yaml.Node, f *filter.Filter, diags []Diagnostic) []Diagnostic {
	items, ok := scalarItems(node)
	if !ok {
		return append(diags, typeErr(path, "a value or a list of values", node))
	}
	for _, item := range items {
		owner, err := classifyStatus(item.Value)
		switch {
		case err != nil:
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"%s: %v (line %d); ignoring it", path, err, item.Line)})
		case owner != axis:
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"%s: %q is a %s, not a %s (line %d); put it under %s",
				path, item.Value, kindName(owner), kindName(axis), item.Line, owner)})
		default:
			// Classified above, so this cannot fail. ParseStatus appends into the set that
			// owns the value and ignores a repeat, which is what makes a duplicated entry a
			// no-op rather than a second comparison Match would pay for.
			_ = f.ParseStatus(item.Value)
		}
	}
	return diags
}

// classifyStatus reports which of the two keys owns a value, axisStatus or axisConclusion,
// through filter's own parser so the accepted 15 values and the rejection message stay one
// implementation rather than a copy this package would have to keep in step. The parser is
// permissive by design, which is what the CLI's -s flag needs and what R9 refuses to store,
// so reading which set it landed in is how a permissive classifier serves a strict one.
func classifyStatus(value string) (string, error) {
	var probe filter.Filter
	if err := probe.ParseStatus(value); err != nil {
		return "", err
	}
	if len(probe.Statuses) == 1 {
		return axisStatus, nil
	}
	return axisConclusion, nil
}

// kindName spells an axis the way CONTEXT.md's glossary spells the thing it holds, for the
// misfiled-value diagnostic: what the value actually is, and what the key wanted.
func kindName(axis string) string {
	if axis == axisStatus {
		return "Status"
	}
	return "Conclusion"
}

// scalarItems reads an axis's value as a list of scalars, accepting a lone scalar as a
// one-item list. A hand-written config spells a single value `status: queued` far more
// often than `status: [queued]`, and refusing the short form would be a diagnostic about
// YAML rather than about a setting. The marshaller always writes the sequence form, so the
// two spellings never fight over what a Save produces.
func scalarItems(node yaml.Node) ([]yaml.Node, bool) {
	if node.Kind == yaml.ScalarNode {
		return []yaml.Node{node}, true
	}
	var items []yaml.Node
	if node.Decode(&items) != nil {
		return nil, false
	}
	for _, item := range items {
		if item.Kind != yaml.ScalarNode {
			return nil, false
		}
	}
	return items, true
}

// launchFilterMapping renders a Filter into the file's sub-keys, in launchFilterAxes order.
// An axis the filter does not carry is rendered as a nil value, which setMapping reads as
// "remove this key": an axis the operator cleared in the view leaves the file rather than
// lingering there as a clause nothing applies. Every sub-key the mapping carries that this
// version does not know is left exactly where it is (R17).
func launchFilterMapping(f filter.Filter) mappingValue {
	out := make(mappingValue, 0, len(launchFilterAxes))
	for _, axis := range launchFilterAxes {
		out = append(out, change{key: axis, value: launchFilterValue(f, axis)})
	}
	return out
}

// launchFilterValue is one axis's value as the file spells it, or nil where the filter does
// not carry that axis. The created clause is written verbatim, exactly as it was accepted,
// because re-serialising a parsed range is the one way to shift a boundary (ADR-0016).
func launchFilterValue(f filter.Filter, axis string) any {
	switch axis {
	case axisBranch:
		return scalarOrNil(f.Branch)
	case axisCommit:
		return scalarOrNil(f.Commit)
	case axisActor:
		return scalarOrNil(f.Actor)
	case axisEvent:
		return scalarOrNil(f.Event)
	case axisWorkflow:
		return scalarOrNil(f.Workflow)
	case axisCreated:
		return scalarOrNil(f.Created.String())
	case axisStatus:
		return listOrNil(namesOf(f.Statuses))
	case axisConclusion:
		return listOrNil(namesOf(f.Conclusions))
	default:
		return nil
	}
}

// scalarOrNil and listOrNil map an empty axis to the nil that removes its key.
func scalarOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func listOrNil(vs []string) any {
	if len(vs) == 0 {
		return nil
	}
	return vs
}

// namesOf renders a set of domain enum values as the strings the file carries. Both sets in
// the permissive pair are string-kinded enums (ADR-0014), so one conversion serves each.
func namesOf[T ~string](vs []T) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, string(v))
	}
	return out
}

// sameLaunchFilter reports whether two launch filters are the same value, which is what
// decides whether a Save writes the key at all. It compares reflectively rather than field
// by field on purpose: a hand-written comparison would keep passing on the day ADR-0016's
// Filter grows an axis, and silently stop writing that axis back. This runs once per
// keystroke that changes a setting, never in a poll.
func sameLaunchFilter(a, b filter.Filter) bool { return reflect.DeepEqual(a, b) }

// emptyFilter reports a Filter that constrains nothing, which matches every Run and is the
// launch filter's default (R3). Load reads it to tell "no filter flag was passed" from a
// flag that set one.
//
// It asks what the value constrains, never how it was allocated. A caller that builds its
// sets with make() and appends nothing holds a Filter that states no filter, and comparing
// against the zero value would read that as one and let it destroy the file's launch filter
// with no diagnostic, which is R4 and AC3 inverted. Nothing fills Flags.LaunchFilter today
// and ParseQuery returns nil sets, so the zero comparison happened to hold: that is luck,
// and this is the contract.
//
// It reads QueryString for the eight axes the grammar spells and names the ninth, which has
// no token, rather than enumerating all nine here. A tenth axis would slip past this the way
// Repos would have, and TestLaunchFilterAxisCountIsDeliberate is what fails the day one
// arrives.
func emptyFilter(f filter.Filter) bool { return f.QueryString() == "" && len(f.Repos) == 0 }
