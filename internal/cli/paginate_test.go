package cli_test

import (
	"strings"
	"testing"
)

// TestPaginationFillsTheLimit pins the per-repository crawl: when a client-side
// filter drops rows from a page, the command follows the Link header's rel="next"
// to fill the requested limit rather than returning one short page (cli-surface
// R23). -w 9001 filters by workflow ID client-side, so the three workflow-9001
// Runs (1, 3, 4) are collected across two pages while 2 and 5 are excluded.
func TestPaginationFillsTheLimit(t *testing.T) {
	h := newHarness(t, "list_paginate")

	code := h.run("list", "-R", "octo/hello", "-w", "9001", "-L", "3", "--json", "databaseId", "-q", ".[].databaseId")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, h.stderr.String())
	}
	got := strings.Fields(strings.TrimSpace(h.stdout.String()))
	if strings.Join(got, ",") != "1,3,4" {
		t.Errorf("collected IDs = %v, want [1 3 4] (workflow 9001, across two pages)", got)
	}
	// Two wire requests: page one, then the rel="next" page.
	if n := h.counting.count(); n != 2 {
		t.Errorf("wire requests = %d, want 2 (the crawl followed one next link)", n)
	}
}
