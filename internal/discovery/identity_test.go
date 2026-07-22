package discovery_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/discovery"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// TestReloadRejectsNonGitHubHost is AC14 at the persistence seam. A persisted
// record whose host is not github.com is rejected on reload and contributes no
// entry, rather than being silently attributed to github.com. It is the one place
// a foreign host could re-enter the tool across sessions, so the rejection lives
// where the record is read back.
func TestReloadRejectsNonGitHubHost(t *testing.T) {
	dir := t.TempDir()

	// Persist one github.com record and one on another host, directly through the
	// store, then reload through discovery.
	writer := newDocStore(t, dir)
	writer.SaveDoc("discovery", []discovery.Record{
		{Host: "github.com", Owner: "jv-k", Name: "keep", HasRuns: true, Known: true, Permissions: domain.Permissions{Push: true}},
		{Host: "ghe.example.com", Owner: "corp", Name: "drop", HasRuns: true, Known: true, Permissions: domain.Permissions{Push: true}},
	})

	d := discovery.New(discovery.Options{Store: newDocStore(t, dir), Clock: clockwork.NewFakeClock()})
	n := d.Reload()
	if n != 1 {
		t.Errorf("Reload admitted %d records, want 1 (the foreign host is rejected)", n)
	}
	if got := pollSetKeys(d); strings.Join(got, ",") != "github.com/jv-k/keep" {
		t.Errorf("poll set = %v, want only github.com/jv-k/keep", got)
	}
	if got := d.Capability(domain.RepoID{Host: "ghe.example.com", Owner: "corp", Name: "drop"}); got != domain.CapabilityUnknown {
		t.Errorf("foreign-host capability = %v, want not-yet-known (it contributes no entry)", got)
	}
}

// TestReloadRejectsInvalidRepoName is the charset hardening at the persistence
// seam. A persisted record whose owner or name is outside GitHub's identifier
// charset (a traversal-shaped name here) is rejected on reload and contributes no
// entry, so a name that could reach a request URL path or a filesystem key never
// re-enters the tool across sessions. It is the analogue of the foreign-host
// rejection, at the same seam newRepoID guards.
func TestReloadRejectsInvalidRepoName(t *testing.T) {
	dir := t.TempDir()

	writer := newDocStore(t, dir)
	writer.SaveDoc("discovery", []discovery.Record{
		{Host: "github.com", Owner: "jv-k", Name: "keep", HasRuns: true, Known: true, Permissions: domain.Permissions{Push: true}},
		{Host: "github.com", Owner: "jv-k", Name: "../../evil", HasRuns: true, Known: true, Permissions: domain.Permissions{Push: true}},
	})

	d := discovery.New(discovery.Options{Store: newDocStore(t, dir), Clock: clockwork.NewFakeClock()})
	if n := d.Reload(); n != 1 {
		t.Errorf("Reload admitted %d records, want 1 (the path-unsafe name is rejected)", n)
	}
	if got := pollSetKeys(d); strings.Join(got, ",") != "github.com/jv-k/keep" {
		t.Errorf("poll set = %v, want only github.com/jv-k/keep", got)
	}
}

// TestRecordIDRoutesThroughTheValidatedConstructor pins the identity consolidation: a
// Record's ID() is built through newRepoID, discovery's one validated construction door,
// rather than a hand-assembled struct literal (security review). A canonical record
// round-trips unchanged, and a host-less record (an older persisted entry) defaults to
// github.com exactly as newRepoID defaults it, so it keys canonically rather than under an
// empty host.
func TestRecordIDRoutesThroughTheValidatedConstructor(t *testing.T) {
	full := discovery.Record{Host: "github.com", Owner: "jv-k", Name: "gh-runs"}
	if got := full.ID().String(); got != "github.com/jv-k/gh-runs" {
		t.Errorf("ID() = %q, want github.com/jv-k/gh-runs", got)
	}
	hostless := discovery.Record{Owner: "jv-k", Name: "gh-runs"}
	if got := hostless.ID().String(); got != "github.com/jv-k/gh-runs" {
		t.Errorf("host-less ID() = %q, want github.com/jv-k/gh-runs (newRepoID's host default)", got)
	}
}

// TestFastPathRejectsNonGitHubHost is R18 at the fast path. A resolver that yields
// a host other than github.com is rejected explicitly, with the neutral message
// ADR-0009 settled on, rather than probed as though it were github.com.
func TestFastPathRejectsNonGitHubHost(t *testing.T) {
	foreign := domain.RepoID{Host: "gitlab.com", Owner: "me", Name: "proj"}
	h := newHarness(t, "fastpath", "", withCurrent(func() (domain.RepoID, error) {
		return foreign, nil
	}))

	_, resolved, err := h.disc.FastPath(context.Background(), nil)
	if resolved {
		t.Error("FastPath admitted a non-github.com repository")
	}
	var uhe *discovery.UnsupportedHostError
	if !errors.As(err, &uhe) {
		t.Fatalf("FastPath error = %v, want an UnsupportedHostError", err)
	}
	if !strings.Contains(uhe.Error(), "gitlab.com") || !strings.Contains(uhe.Error(), "github.com") {
		t.Errorf("rejection message %q must name the host and state github.com support", uhe.Error())
	}
	if n := h.counting.count(); n != 0 {
		t.Errorf("a foreign host caused %d requests, want 0 (rejected before any probe)", n)
	}
}

// exhaustBudget is a Budget stuck reporting exhaustion, standing in for the
// governor after a rate-limit classification (ADR-0018). It lets the burst's
// exhaustion behaviour be tested without provoking a real rate limit, which PRD
// risk R4 forbids.
type exhaustBudget struct{}

func (exhaustBudget) Readout() governor.Readout { return governor.Readout{Exhausted: true} }

// countingProbeFake serves enumeration and probes without gating, counting the
// probes so a test can prove the burst issued none.
type countingProbeFake struct {
	enumBody string
	probes   int
}

func (c *countingProbeFake) Request(method, path string, body io.Reader) (*http.Response, error) {
	if strings.Contains(path, "/actions/runs") {
		c.probes++
		return fakeResp(hasRunsBody), nil
	}
	return fakeResp(c.enumBody), nil
}

// TestBurstStopsWhenBudgetExhausted is R17 and ADR-0018's read-exhaustion path. A
// discovery burst does not keep firing probes into a limit that names the whole
// token: when the governor reports exhaustion, the pass stops launching probes.
// The account is still enumerated, so the tool knows what it would probe once the
// limit resets.
func TestBurstStopsWhenBudgetExhausted(t *testing.T) {
	fake := &countingProbeFake{enumBody: enumBody(8)}
	d := discovery.New(discovery.Options{
		Client: fake,
		Budget: exhaustBudget{},
		Clock:  clockwork.NewFakeClock(),
	})

	if err := d.Pass(context.Background(), nil); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if fake.probes != 0 {
		t.Errorf("burst issued %d probes while exhausted, want 0 (R17: subject to the governor's backoff)", fake.probes)
	}
	if got := len(d.PollSet()); got != 0 {
		t.Errorf("poll set = %d, want 0 (nothing was probed)", got)
	}
}
