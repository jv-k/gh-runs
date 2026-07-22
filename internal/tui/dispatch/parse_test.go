package dispatch_test

import (
	"os"
	"testing"

	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// TestParseDeploymentFixture pins R1, R6 and R8 against the checked-in deployment.yml: one typed
// input per declared input, in declared order, each with its type, default, description and
// required flag. The count comes from the fixture (five: tag_name, environment, platforms,
// release, dry_run), never a live third-party file, which is the AC2 lesson.
func TestParseDeploymentFixture(t *testing.T) {
	form, err := dispatch.ParseForm(fixture(t, "deployment.yml"))
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if !form.Dispatchable {
		t.Fatal("deployment.yml declares workflow_dispatch; Dispatchable must be true (R1)")
	}
	if len(form.Inputs) != 5 {
		t.Fatalf("parsed %d inputs, want 5 from the fixture (R6, AC2): %+v", len(form.Inputs), form.Inputs)
	}

	// Declared order is preserved, so the form renders inputs as the author wrote them.
	order := []string{"tag_name", "environment", "platforms", "release", "dry_run"}
	for i, want := range order {
		if form.Inputs[i].Name != want {
			t.Errorf("input %d = %q, want %q in declared order (R6)", i, form.Inputs[i].Name, want)
		}
	}

	byName := map[string]dispatch.InputSpec{}
	for _, in := range form.Inputs {
		byName[in.Name] = in
	}
	if in := byName["tag_name"]; in.Type != dispatch.TypeString || !in.Required {
		t.Errorf("tag_name = type %q required %v, want string/required (R6, R8)", in.Type, in.Required)
	}
	if in := byName["environment"]; in.Type != dispatch.TypeEnvironment || in.Default != "production" {
		t.Errorf("environment = type %q default %q, want environment/production (R6, R8)", in.Type, in.Default)
	}
	if in := byName["platforms"]; in.Type != dispatch.TypeString || in.Default != "linux/amd64,linux/arm64" {
		t.Errorf("platforms = type %q default %q, want string with its default (R8)", in.Type, in.Default)
	}
	if in := byName["release"]; in.Type != dispatch.TypeBoolean || in.Default != "true" {
		t.Errorf("release = type %q default %q, want boolean/true (R6, R8)", in.Type, in.Default)
	}
	if in := byName["dry_run"]; in.Type != dispatch.TypeBoolean || in.Default != "true" {
		t.Errorf("dry_run = type %q default %q, want boolean/true (R6, R8)", in.Type, in.Default)
	}
	if !form.HasEnvironmentInput() {
		t.Errorf("deployment.yml declares an environment input; HasEnvironmentInput must be true (R7)")
	}
}

// TestParseChoiceAndNumber pins R6 and R10 for the two types deployment.yml omits: a choice
// carries its declared options and a number is numeric entry. A choice with no options would be
// a select over nothing, so the options must decode.
func TestParseChoiceAndNumber(t *testing.T) {
	form, err := dispatch.ParseForm(fixture(t, "choice_number.yml"))
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	byName := map[string]dispatch.InputSpec{}
	for _, in := range form.Inputs {
		byName[in.Name] = in
	}
	level := byName["level"]
	if level.Type != dispatch.TypeChoice {
		t.Fatalf("level = type %q, want choice (R6)", level.Type)
	}
	wantOpts := []string{"debug", "info", "warn", "error"}
	if len(level.Options) != len(wantOpts) {
		t.Fatalf("level options = %v, want %v (R10)", level.Options, wantOpts)
	}
	for i, o := range wantOpts {
		if level.Options[i] != o {
			t.Errorf("level option %d = %q, want %q (R10)", i, level.Options[i], o)
		}
	}
	if level.Default != "info" {
		t.Errorf("level default = %q, want info (R8)", level.Default)
	}
	if it := byName["iterations"]; it.Type != dispatch.TypeNumber || it.Default != "100" || !it.Required {
		t.Errorf("iterations = type %q default %q required %v, want number/100/required (R6, R8)", it.Type, it.Default, it.Required)
	}
	if form.HasEnvironmentInput() {
		t.Errorf("choice_number.yml declares no environment input; HasEnvironmentInput must be false so no environments are fetched (R7)")
	}
}

// TestParseUnrecognizedType pins R11: an input whose declared type is not one of R6's five parses
// as unrecognised, keeping the author's declared type verbatim so the form can label it, rather
// than erroring and making the Workflow undispatchable.
func TestParseUnrecognizedType(t *testing.T) {
	form, err := dispatch.ParseForm(fixture(t, "unrecognized.yml"))
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if len(form.Inputs) != 1 {
		t.Fatalf("parsed %d inputs, want 1 (R11)", len(form.Inputs))
	}
	in := form.Inputs[0]
	if in.Type != dispatch.TypeUnrecognized {
		t.Errorf("colour type = %q, want unrecognised (R11)", in.Type)
	}
	if in.RawType != "rainbow" {
		t.Errorf("colour RawType = %q, want the author's declared type verbatim for the label (R11)", in.RawType)
	}
	if in.Default != "teal" {
		t.Errorf("colour default = %q, want teal (R8, R11)", in.Default)
	}
}

// TestParseNonDispatchable pins R1: a Workflow declaring no workflow_dispatch trigger resolves as
// not dispatchable, which is a property only the YAML carries.
func TestParseNonDispatchable(t *testing.T) {
	form, err := dispatch.ParseForm(fixture(t, "not_dispatchable.yml"))
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if form.Dispatchable {
		t.Errorf("not_dispatchable.yml declares only push; Dispatchable must be false (R1)")
	}
	if len(form.Inputs) != 0 {
		t.Errorf("a non-dispatchable Workflow declares no dispatch inputs, got %d (R1)", len(form.Inputs))
	}
}

// TestParseDispatchableShapes pins R1 across the three shapes the `on` key takes: a bare scalar,
// a sequence, and a mapping whose workflow_dispatch value is null. Each is dispatchable with no
// inputs, so a form still renders (the ref and a submit), never an error.
func TestParseDispatchableShapes(t *testing.T) {
	cases := map[string]string{
		"scalar":       "on: workflow_dispatch\njobs: {}\n",
		"sequence":     "on: [push, workflow_dispatch]\njobs: {}\n",
		"null mapping": "on:\n  workflow_dispatch:\n  push:\njobs: {}\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := dispatch.ParseForm([]byte(doc))
			if err != nil {
				t.Fatalf("ParseForm(%s): %v", name, err)
			}
			if !form.Dispatchable {
				t.Errorf("%s declares workflow_dispatch; Dispatchable must be true (R1)", name)
			}
			if len(form.Inputs) != 0 {
				t.Errorf("%s declares no inputs, got %d (R1)", name, len(form.Inputs))
			}
		})
	}
}

// TestParseInvalidYAMLErrors pins R12: unparseable YAML is an error the caller surfaces (the pane
// names the ref and the path), never a silent empty form that would degrade to untyped entry.
func TestParseInvalidYAMLErrors(t *testing.T) {
	if _, err := dispatch.ParseForm([]byte("on: [unterminated\n")); err == nil {
		t.Errorf("ParseForm accepted malformed YAML; R12 requires an explicit failure")
	}
}

// TestInitialValuesReconciles pins R25 and AC9: on recall each input pre-fills from its remembered
// value only where that value still validates against the current schema. A choice value no longer
// among its options falls back to the declared default, a remembered value that still fits is kept,
// an input the schema dropped is simply absent (only current inputs are keyed), and an input with
// no remembered value takes its default.
func TestInitialValuesReconciles(t *testing.T) {
	form, err := dispatch.ParseForm(fixture(t, "choice_number.yml"))
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	// Remembered: a valid choice, a still-fitting number, and a stale key for an input the schema
	// no longer declares.
	remembered := map[string]string{
		"level":      "warn",     // still among the options: kept
		"iterations": "500",      // any number string fits: kept
		"gone":       "whatever", // the schema dropped this input: dropped on recall
	}
	got := dispatch.InitialValues(form, remembered)
	if got["level"] != "warn" {
		t.Errorf("level = %q, want the remembered warn kept (it is still an option) (R25)", got["level"])
	}
	if got["iterations"] != "500" {
		t.Errorf("iterations = %q, want the remembered 500 kept (R25)", got["iterations"])
	}
	if _, ok := got["gone"]; ok {
		t.Errorf("a remembered value for an input the schema dropped must be absent on recall (R25, AC9)")
	}

	// A remembered choice value no longer among the options falls back to the declared default.
	stale := map[string]string{"level": "trace"} // not an option any more
	got2 := dispatch.InitialValues(form, stale)
	if got2["level"] != "info" {
		t.Errorf("stale choice = %q, want fallback to the declared default info (R25, AC9)", got2["level"])
	}

	// With nothing remembered, every input takes its declared default (deleting the store returns
	// every input to its default, AC9).
	fresh := dispatch.InitialValues(form, nil)
	if fresh["level"] != "info" || fresh["iterations"] != "100" {
		t.Errorf("fresh defaults = level %q iterations %q, want info/100 (R8, AC9)", fresh["level"], fresh["iterations"])
	}
}
