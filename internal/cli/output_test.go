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
