package dispatch_test

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"

	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/ghclient"
	"github.com/jv-k/gh-runs/v2/internal/keys"
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

// countingRT counts the requests that reach the wire and records their paths. It sits directly above
// the cassette, so it counts what actually left rather than what the code believes it sent. It is
// how a claim about a request that was never made can be carried at all (ADR-0011's cassette note,
// mirrored from discovery's harness).
type countingRT struct {
	base  http.RoundTripper
	mu    sync.Mutex
	paths []string
}

func (c *countingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.paths = append(c.paths, req.URL.Path)
	c.mu.Unlock()
	return c.base.RoundTrip(req)
}

// countExact counts wire requests whose path was exactly path, so GET /repos/o/r is distinguishable
// from GET /repos/o/r/contents/..., which a substring check would conflate.
func (c *countingRT) countExact(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.paths {
		if p == path {
			n++
		}
	}
	return n
}

func (c *countingRT) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

// newCountedClient builds a ghclient over the named cassette with a counter between the two, and
// returns both so a test can assert over what left the process.
func newCountedClient(t *testing.T, cassetteName string) (*ghclient.Client, *countingRT) {
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
	counting := &countingRT{base: rec}
	client, err := ghclient.New(ghclient.Options{AuthToken: "dummy-fixed-token", Transport: counting})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client, counting
}

func newFetchClient(t *testing.T, cassetteName string) *ghclient.Client {
	t.Helper()
	client, _ := newCountedClient(t, cassetteName)
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

// TestClientFetchListsBranchesAndTags pins R24 and AC8 against the cassette: the picker set is the
// repository's branches followed by its tags, each labelled by kind, read from what the API actually
// said.
func TestClientFetchListsBranchesAndTags(t *testing.T) {
	fetch := dispatch.NewClientFetch(newFetchClient(t, "dispatch_contents"))

	refs, err := fetch.Refs(rid("o", "r"))
	if err != nil {
		t.Fatalf("Refs returned an error: %v", err)
	}
	want := []dispatch.Ref{
		{Name: "main"}, {Name: "release/1.2"}, {Name: "v2.0.0", IsTag: true},
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v (R24)", refs, want)
	}
	for i, w := range want {
		if refs[i] != w {
			t.Errorf("ref %d = %+v, want %+v (R24, AC8)", i, refs[i], w)
		}
	}
}

// TestClientFetchListsBranchesOnlyWithNoTags pins AC8's second half against the cassette: a
// repository whose tag listing is empty yields its branches and nothing claiming to be a tag.
func TestClientFetchListsBranchesOnlyWithNoTags(t *testing.T) {
	fetch := dispatch.NewClientFetch(newFetchClient(t, "dispatch_contents"))

	refs, err := fetch.Refs(rid("o", "untagged"))
	if err != nil {
		t.Fatalf("Refs returned an error: %v", err)
	}
	if len(refs) != 1 || refs[0] != (dispatch.Ref{Name: "main"}) {
		t.Fatalf("refs = %+v, want the branches alone (AC8)", refs)
	}
}

// TestOpeningAtTheDiscoveredDefaultBranchIssuesNoRepositoryRead is R23's Budget claim measured at
// the wire. The Workflows tab hands the pane the default branch discovery already carried, so the
// form's whole open costs one request, the Contents read R5 requires, and the GET /repos/{o}/{r} it
// used to spend per open never leaves the process. A test asserting a populated field could not tell
// those two worlds apart; a counter above the cassette can.
func TestOpeningAtTheDiscoveredDefaultBranchIssuesNoRepositoryRead(t *testing.T) {
	client, counting := newCountedClient(t, "dispatch_contents")
	fetch := dispatch.NewClientFetch(client)

	m := dispatch.New(dispatch.Options{Profile: keys.Standard, Fetch: fetch})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	// "trunk" is the discovered default branch, and it is deliberately not the cassette's "main":
	// a pane that resolved the branch by request would land on main and the ref line would say so.
	m, cmd := m.Open(dispatch.Target{
		Repo:     rid("o", "r"),
		Workflow: domain.Workflow{ID: 9001, Name: "Deployment", Path: ".github/workflows/deploy.yml", State: domain.StateActive},
		Eligible: true,
		Ref:      "trunk",
	})
	if cmd == nil {
		t.Fatal("Open issued no Cmd; the YAML must be fetched at the supplied ref (R2, R5)")
	}
	m, cmd = m.Update(cmd()) // the Contents read resolves into the form
	m = runCmd(m, cmd)       // and its environments follow, because the fixture declares one

	if n := counting.countExact("/repos/o/r"); n != 0 {
		t.Errorf("GET /repos/o/r was issued %d times; the default branch rides discovery, so R23 costs no request", n)
	}
	if n := counting.countExact("/repos/o/r/contents/.github/workflows/deploy.yml"); n != 1 {
		t.Errorf("the Contents read was issued %d times, want exactly 1 (R5, AC2)", n)
	}
	if n := counting.countExact("/repos/o/r/branches"); n != 0 {
		t.Errorf("opening the form listed branches %d times; R24 enumerates lazily", n)
	}
	if n := counting.count(); n != 2 {
		t.Errorf("opening the form cost %d wire requests, want 2 (the Contents read and the environments read): %v", n, counting.paths)
	}
	if !strings.Contains(m.View(), "Ref: trunk") {
		t.Errorf("the form is not on the discovered default branch (R23, R4): %q", m.View())
	}
}
