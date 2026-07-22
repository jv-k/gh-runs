package dispatch_test

import (
	"net/http"
	"net/url"
	"testing"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/tui/dispatch"
)

// contentsMatcher matches a live request against a taped one on method, URL path and the empty
// If-None-Match header, the header-matched shape the tree pins go-vcr v4 for (CLAUDE.md). The path
// disambiguates the Contents read from the environments read, and matching the path rather than the
// full URL is robust to how go-gh encodes the ref query.
func contentsMatcher(r *http.Request, i cassette.Request) bool {
	iu, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	if r.Method != i.Method || r.URL.Path != iu.Path {
		return false
	}
	return r.Header.Get("If-None-Match") == i.Headers.Get("If-None-Match")
}

func newFetchClient(t *testing.T, cassetteName string) *ghclient.Client {
	t.Helper()
	rec, err := recorder.New("testdata/"+cassetteName,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(contentsMatcher),
	)
	if err != nil {
		t.Fatalf("open cassette %s: %v", cassetteName, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("stop recorder %s: %v", cassetteName, err)
		}
	})
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: rec})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

// TestClientFetchResolvesDefaultBranch pins R23 against the cassette: the repository read returns the
// default branch the form defaults the ref picker to.
func TestClientFetchResolvesDefaultBranch(t *testing.T) {
	fetch := dispatch.NewClientFetch(newFetchClient(t, "dispatch_contents"))

	branch, err := fetch.DefaultBranch(rid("o", "r"))
	if err != nil {
		t.Fatalf("DefaultBranch returned an error: %v", err)
	}
	if branch != "main" {
		t.Errorf("default branch = %q, want main (R23)", branch)
	}
}

// TestClientFetchDecodesYAMLAtRef pins R5 against a cassette: the Contents read hits the file at the
// target ref, decodes the base64 body, and the result parses into the schema the form paints. It is
// exercised against what the API actually said with no live network.
func TestClientFetchDecodesYAMLAtRef(t *testing.T) {
	fetch := dispatch.NewClientFetch(newFetchClient(t, "dispatch_contents"))

	data, err := fetch.WorkflowYAML(rid("o", "r"), ".github/workflows/deploy.yml", "main")
	if err != nil {
		t.Fatalf("WorkflowYAML returned an error: %v", err)
	}
	form, err := dispatch.ParseForm(data)
	if err != nil {
		t.Fatalf("the decoded YAML did not parse (R5): %v", err)
	}
	if !form.Dispatchable || len(form.Inputs) != 2 {
		t.Fatalf("decoded form = dispatchable %v with %d inputs, want true/2 (R5)", form.Dispatchable, len(form.Inputs))
	}
	if !form.HasEnvironmentInput() {
		t.Errorf("the decoded YAML declares an environment input; HasEnvironmentInput must be true (R7)")
	}
}

// TestClientFetchReadsEnvironments pins R7 against the same cassette: the environments read returns
// the repository's environment names, which populate the environment selects.
func TestClientFetchReadsEnvironments(t *testing.T) {
	fetch := dispatch.NewClientFetch(newFetchClient(t, "dispatch_contents"))

	envs, err := fetch.Environments(rid("o", "r"))
	if err != nil {
		t.Fatalf("Environments returned an error: %v", err)
	}
	want := []string{"production", "staging"}
	if len(envs) != len(want) {
		t.Fatalf("environments = %v, want %v (R7)", envs, want)
	}
	for i, e := range want {
		if envs[i] != e {
			t.Errorf("environment %d = %q, want %q (R7)", i, envs[i], e)
		}
	}
}
