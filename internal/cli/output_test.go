package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestJSONWithJQEmitsBareIDs pins AC6: --json with -q emits a bare list of IDs,
// the shape gh produces, because -q is handed to go-gh's own jq engine (cli-surface
// R7).
func TestJSONWithJQEmitsBareIDs(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "databaseId,status,conclusion", "-q", ".[].databaseId")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	got := strings.Fields(strings.TrimSpace(h.stdout.String()))
	if strings.Join(got, ",") != "101,102" {
		t.Errorf("jq output = %v, want [101 102]", got)
	}
}

// TestJSONProjectionUsesGhNames pins R7: the projection emits gh's field names,
// not the API's. databaseId, displayTitle and workflowDatabaseId are gh's
// spellings of the API's id, display_title and workflow_id.
func TestJSONProjectionUsesGhNames(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "databaseId,displayTitle,workflowDatabaseId,conclusion")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, h.stdout.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	first := rows[0]
	for _, key := range []string{"databaseId", "displayTitle", "workflowDatabaseId", "conclusion"} {
		if _, ok := first[key]; !ok {
			t.Errorf("row missing gh field %q: %v", key, first)
		}
	}
	if first["databaseId"].(float64) != 101 {
		t.Errorf("databaseId = %v, want 101", first["databaseId"])
	}
	if first["workflowDatabaseId"].(float64) != 9001 {
		t.Errorf("workflowDatabaseId = %v, want 9001", first["workflowDatabaseId"])
	}
}

// TestRepositoryFieldShape pins AC17: --json repository emits an object of {name,
// nameWithOwner} for every row, on a single-repository invocation, and no
// repository key appears when the field is not requested (cli-surface R24).
func TestRepositoryFieldShape(t *testing.T) {
	t.Run("requested, single-repo", func(t *testing.T) {
		h := newHarness(t, "list_single")
		if code := h.run("list", "-R", "octo/hello", "--json", "repository,databaseId"); code != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(h.stdout.Bytes(), &rows); err != nil {
			t.Fatalf("output is not JSON: %v", err)
		}
		repo, ok := rows[0]["repository"].(map[string]any)
		if !ok {
			t.Fatalf("repository is not an object: %v", rows[0]["repository"])
		}
		if repo["name"] != "hello" || repo["nameWithOwner"] != "octo/hello" {
			t.Errorf("repository = %v, want {name: hello, nameWithOwner: octo/hello}", repo)
		}
	})

	t.Run("not requested, absent", func(t *testing.T) {
		h := newHarness(t, "list_single")
		if code := h.run("list", "-R", "octo/hello", "--json", "databaseId"); code != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(h.stdout.Bytes(), &rows); err != nil {
			t.Fatalf("output is not JSON: %v", err)
		}
		if _, ok := rows[0]["repository"]; ok {
			t.Errorf("repository key present though not requested: %v", rows[0])
		}
	})
}

// TestUnknownJSONFieldRejected pins R7's validation: an unknown --json field is
// rejected by name before any request, matching gh.
func TestUnknownJSONFieldRejected(t *testing.T) {
	h := newHarnessOffline(t)

	code := h.run("list", "-R", "octo/hello", "--json", "databaseId,bogusField")
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero for an unknown JSON field")
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("wire requests = %d, want 0 (unknown field caught before the wire)", n)
	}
	if !strings.Contains(h.stderr.String(), "bogusField") {
		t.Errorf("rejection did not name the field; stderr=%q", h.stderr.String())
	}
}

// TestTemplateOverJSON pins the -t path over a standard Go template (cli-surface
// R7). Numbers decode through UseNumber, so a database ID prints as its digits,
// never in float64 scientific notation.
func TestTemplateOverJSON(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "databaseId", "-t", "{{range .}}{{.databaseId}}\n{{end}}")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	got := strings.Fields(strings.TrimSpace(h.stdout.String()))
	if strings.Join(got, ",") != "101,102" {
		t.Errorf("template output = %v, want [101 102]", got)
	}
}

// TestTemplateTimeagoUsesInjectedClock pins AC21's first case: timeago renders gh's
// wording, computed against the injected clock rather than the wall clock, so the
// output is deterministic (cli-surface R7, ADR-0023). The cassette's runs were created
// 2026-07-20T10:00:00Z and 2026-07-19T08:00:00Z; the harness clock stands at
// 2026-07-22T12:00:00Z, which is 50 and 76 hours later.
func TestTemplateTimeagoUsesInjectedClock(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "createdAt",
		"-t", `{{range .}}{{timeago .createdAt}}{{"\n"}}{{end}}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if got, want := h.stdout.String(), "2 days ago\n3 days ago\n"; got != want {
		t.Errorf("timeago output = %q, want %q", got, want)
	}
}

// TestTemplateTruncateShortensToDisplayWidth pins AC21's second case: truncate
// shortens to the requested display width and marks the cut with gh's "..." ellipsis,
// so the whole cell is exactly the requested width (cli-surface R7, ADR-0023).
func TestTemplateTruncateShortensToDisplayWidth(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "displayTitle",
		"-t", `{{range .}}{{truncate 10 .displayTitle}}{{"\n"}}{{end}}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	// "Fix the bug" is 11 cells and "Break the build" is 15, so both are cut to seven
	// cells plus the three-cell ellipsis.
	if got, want := h.stdout.String(), "Fix the...\nBreak t...\n"; got != want {
		t.Errorf("truncate output = %q, want %q", got, want)
	}
}

// TestTemplateDroppedFuncsErrorByName pins AC21's third case: the four gh functions
// that need a colour library or gh's table printer are unsupported, and a template
// calling one fails with an error naming it, not the standard library's bare
// function "color" not defined (cli-surface R7, ADR-0023). The template must parse, as
// it does under gh, and fail at execution.
func TestTemplateDroppedFuncsErrorByName(t *testing.T) {
	cases := map[string]string{
		"color":       `{{range .}}{{color "green" .status}}{{end}}`,
		"autocolor":   `{{range .}}{{autocolor "green" .status}}{{end}}`,
		"tablerow":    `{{range .}}{{tablerow .status .databaseId}}{{end}}`,
		"tablerender": `{{range .}}{{.status}}{{end}}{{tablerender}}`,
	}
	for name, tmpl := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "list_single")

			code := h.run("list", "-R", "octo/hello", "--json", "status,databaseId", "-t", tmpl)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero: %s is unsupported", name)
			}
			stderr := h.stderr.String()
			if !strings.Contains(stderr, name) {
				t.Errorf("error does not name %q: %q", name, stderr)
			}
			if strings.Contains(stderr, "not defined") {
				t.Errorf("error is the standard library's undefined-function message: %q", stderr)
			}
			// The list of what -t does carry is derived from the registered map, not
			// restated, so it cannot drift into telling the operator to use something
			// that is not there. Pinning it here is what keeps the derivation honest.
			_, supported, ok := strings.Cut(stderr, "Supported functions: ")
			if !ok {
				t.Fatalf("error does not offer the supported set: %q", stderr)
			}
			for _, want := range []string{"timeago", "truncate", "hyperlink", "regexMatch"} {
				if !strings.Contains(supported, want) {
					t.Errorf("supported set omits %q: %q", want, supported)
				}
			}
			for _, dropped := range []string{"color", "tablerow", "tablerender"} {
				if strings.Contains(supported, dropped) {
					t.Errorf("supported set advertises dropped %q: %q", dropped, supported)
				}
			}
		})
	}
}

// TestTemplateDroppedFuncInUntakenBranch pins the other half of ADR-0023's stub
// contract: an unsupported function inside a branch the template never takes must not
// fire, so a template that only conditionally colours still renders.
func TestTemplateDroppedFuncInUntakenBranch(t *testing.T) {
	h := newHarness(t, "list_single")

	code := h.run("list", "-R", "octo/hello", "--json", "status",
		"-t", `{{range .}}{{if false}}{{color "green" .status}}{{else}}{{.status}}{{end}}{{"\n"}}{{end}}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	if got, want := h.stdout.String(), "completed\ncompleted\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestTemplatePureFuncsMatchGh pins the rest of ADR-0023's subset: gh's remaining
// pure functions render as gh's do (cli-surface R7). pluck and join compose over the
// whole projection, timefmt reformats an RFC3339 timestamp, and hyperlink emits its
// OSC 8 escape unconditionally, because -t output stays raw and unsanitised exactly as
// gh and -q leave it.
func TestTemplatePureFuncsMatchGh(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		want string
	}{
		{
			name: "pluck and join",
			tmpl: `{{join ", " (pluck "databaseId" .)}}`,
			want: "101, 102",
		},
		{
			name: "timefmt",
			tmpl: `{{range .}}{{timefmt "2006-01-02" .createdAt}}{{"\n"}}{{end}}`,
			want: "2026-07-20\n2026-07-19\n",
		},
		{
			name: "hyperlink",
			tmpl: `{{range .}}{{hyperlink .url .displayTitle}}{{"\n"}}{{end}}`,
			want: "\x1b]8;;https://github.com/octo/hello/actions/runs/101\x1b\\Fix the bug\x1b]8;;\x1b\\\n" +
				"\x1b]8;;https://github.com/octo/hello/actions/runs/102\x1b\\Break the build\x1b]8;;\x1b\\\n",
		},
		{
			name: "hyperlink falls back to the link as its text",
			tmpl: `{{range .}}{{hyperlink .url ""}}{{"\n"}}{{end}}`,
			want: "\x1b]8;;https://github.com/octo/hello/actions/runs/101\x1b\\https://github.com/octo/hello/actions/runs/101\x1b]8;;\x1b\\\n" +
				"\x1b]8;;https://github.com/octo/hello/actions/runs/102\x1b\\https://github.com/octo/hello/actions/runs/102\x1b]8;;\x1b\\\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "list_single")

			code := h.run("list", "-R", "octo/hello",
				"--json", "databaseId,createdAt,url,displayTitle", "-t", tc.tmpl)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
			}
			if got := h.stdout.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTemplateSprigStringFuncs pins the curated five gh takes from sprig, reimplemented
// here rather than pulling the dependency (ADR-0023). Their argument order is sprig's
// own, needle before haystack, so that a value pipes into them; getting it backwards
// would silently invert every predicate. The cassette's titles are "Fix the bug" and
// "Break the build".
func TestTemplateSprigStringFuncs(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		want string
	}{
		{"contains", `{{range .}}{{contains "bug" .displayTitle}}{{"\n"}}{{end}}`, "true\nfalse\n"},
		{"hasPrefix", `{{range .}}{{hasPrefix "Fix" .displayTitle}}{{"\n"}}{{end}}`, "true\nfalse\n"},
		{"hasSuffix", `{{range .}}{{hasSuffix "build" .displayTitle}}{{"\n"}}{{end}}`, "false\ntrue\n"},
		{"regexMatch", `{{range .}}{{regexMatch "^B.*d$" .displayTitle}}{{"\n"}}{{end}}`, "false\ntrue\n"},
		{"replace", `{{range .}}{{replace "the" "a" .displayTitle}}{{"\n"}}{{end}}`, "Fix a bug\nBreak a build\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "list_single")

			code := h.run("list", "-R", "octo/hello", "--json", "displayTitle", "-t", tc.tmpl)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
			}
			if got := h.stdout.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJQWithoutJSONRejected pins that -q requires --json, gh's own rule
// (cli-surface R7).
func TestJQWithoutJSONRejected(t *testing.T) {
	h := newHarnessOffline(t)

	code := h.run("list", "-R", "octo/hello", "-q", ".[].databaseId")
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero: -q without --json must be rejected")
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("wire requests = %d, want 0", n)
	}
}
