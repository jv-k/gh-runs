// Package discovery establishes which of the account's repositories have Runs, so
// the Feed polls the ~26 that matter rather than all ~163, and records what the
// token may do in each so every destructive action can be gated without spending
// a request to find out (repo-discovery Purpose).
//
// There is no cross-repository Run query anywhere in GitHub's API (ADR-0003), and
// no repository field reveals Actions usage (repo-discovery R2), so discovery
// enumerates the account's repositories and probes each one's Run list. A
// repository is classified as having Runs if and only if that list is non-empty
// (R3), never from total_count, which a filtered listing inflates past the silent
// 1,000 cap (R4, ADR-0005). The classification and the free capability data (R7)
// persist through the local-store (R19, local-store R2), so a cold start paints
// from disk and then revalidates for free (R12).
//
// Every request discovery issues goes through ghclient's Request and therefore
// through the store-then-governor transport chain (ADR-0012), so each probe is
// accounted by the governor (R17) and revalidated by the store (R12). Discovery
// imports domain, clock, ghclient, store and governor, and never scheduler or tui
// (ADR-0011). It reaches the transport only through the Requester and Store seams
// below, which a test fills with a cassette-backed client and a real store over a
// temp directory (R20), and a fake for the orchestration properties (R16).
package discovery

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jv-k/gh-runs/v2/internal/clock"
	"github.com/jv-k/gh-runs/v2/internal/domain"
	"github.com/jv-k/gh-runs/v2/internal/governor"
)

// githubHost is the one host 2.0.0 discovers (ADR-0009). Every enumerated
// repository is qualified with it, and a repository resolving to any other host
// is rejected explicitly rather than silently attributed to it (R18, AC14).
const githubHost = "github.com"

// docName is the store document holding discovery's persisted results (the
// classification and the recorded capability, local-store R2). One document holds
// the whole set, loaded in a single read on a cold start.
const docName = "discovery"

// enumeratePath is the first page of the account's repository list. R1 names the
// affiliations and the type explicitly rather than inheriting the API default, so
// the reference 163 and its two-page cost stay described by this string
// (ADR-0020). The probe that classifies each repository is unfiltered and carries
// no query at all (R4, AC5), so it is built per repository rather than here.
const enumeratePath = "user/repos?per_page=100&affiliation=owner,collaborator,organization_member&type=all"

// hourlyTier is the fixed re-probe cadence for a repository with no persisted
// ETag (R11, ADR-0020). It is a constant no setting alters, which bounds the
// expensive case at ~163 unconditional requests per hour whatever the fast tier
// is set to.
const hourlyTier = time.Hour

// probeConcurrency bounds discovery's probe fan-out (R16, AC13). The number is
// the tool's, never the user's, and no setting alters it. It matches ADR-0018's
// process-wide wire bound of 10; the canonical long-term home for that bound is
// the transport-chain limiter ADR-0018 places innermost, and when that limiter
// lands this local bound becomes redundant. Until then discovery bounds its own
// burst so AC13 holds at this stage.
const probeConcurrency = 10

// Requester issues a request through the transport chain and returns the response
// for the caller to read and close. It is exactly ghclient.Client's surface
// (ADR-0012: Request, never Get or Do, so the response headers survive), narrowed
// to what discovery uses. A cassette-backed ghclient.Client fills it in the
// fidelity tests; a counting or gated fake fills it where the property under test
// is orchestration rather than the wire (R16, R20).
type Requester interface {
	Request(method, path string, body io.Reader) (*http.Response, error)
}

// Store persists and reloads discovery's results across sessions (local-store
// R2). *store.Transport satisfies it. It is narrowed to the two document calls
// discovery makes, so a test injects the real store over a temp directory and
// proves the restart economy (AC7) against the true persistence path rather than
// a fake.
type Store interface {
	SaveDoc(name string, v any)
	LoadDoc(name string, v any) bool
}

// Budget reports the account's rate limiting so a burst stops firing into an
// exhausted limit rather than hammering it (R17, ADR-0018: a rate-limited read
// rides the exhaustion path). *governor.Governor satisfies it. It is optional: a
// nil Budget disables the check, which is what the orchestration fakes pass.
type Budget interface {
	Readout() governor.Readout
}

// Options carries discovery's seams and its one tunable, the fast-tier interval.
// main.go fills them: the client and store are the transport chain and the
// local-store it already assembles, the clock is the injected one, Refresh is
// config.DiscoveryRefreshMinutes as a Duration (discovery may not import config,
// ADR-0011, so it takes the resolved value), and Current is the fast-path
// resolver (R14).
type Options struct {
	Client  Requester
	Store   Store
	Budget  Budget
	Clock   clock.Clock
	Refresh time.Duration
	Current func() (domain.RepoID, error)
}

// Discovery is the stateful engine. It holds the classified set and the recorded
// capability, updated by a pass, a re-probe, the fast path and a reload, and read
// by consumers through the query methods. It is safe for concurrent use: a pass
// fans out probes that mutate the set as they return (R15), so the set is guarded
// throughout.
type Discovery struct {
	opts Options

	mu      sync.Mutex
	records map[string]Record    // keyed by RepoID.String(); the classified set
	probed  map[string]time.Time // per repo: the injected-clock instant of its last probe
	etagged map[string]bool      // per repo: whether its last probe carried an ETag (the fast tier, R12)
}

// New returns a Discovery over opts. It reads nothing and issues no request: a
// caller reloads the persisted set with Reload and runs a pass with Pass.
func New(opts Options) *Discovery {
	return &Discovery{
		opts:    opts,
		records: make(map[string]Record),
		probed:  make(map[string]time.Time),
		etagged: make(map[string]bool),
	}
}

// Record is discovery's persisted per-repository result: the classification
// (HasRuns, R3) and the recorded capability (the permissions object, archived and
// disabled, R7), keyed host-qualified (R18). It is local-store R2's record shape,
// defined here because store may not import discovery (ADR-0011) and persisted
// through the store's document primitive. The key is the three identity fields
// rather than a bare owner/name, so no persisted entry can exist without a host
// (AC14).
type Record struct {
	Host        string             `json:"host"`
	Owner       string             `json:"owner"`
	Name        string             `json:"name"`
	Permissions domain.Permissions `json:"permissions"`
	Archived    bool               `json:"archived"`
	Disabled    bool               `json:"disabled"`
	HasRuns     bool               `json:"has_runs"`

	// Known is true once enumeration or adoption has recorded this repository's
	// capability. A repository painted by the fast path (R14) before enumeration
	// returns is Known: false, so its Capability reads not-yet-known and its
	// destructive actions stay disabled, never inferred from the fact that its Runs
	// listed (R8, AC8).
	Known bool `json:"known"`

	// Adopted is true for a repository admitted for the session by R22's fast-path
	// adoption rather than by enumeration. Its classification, capability and ETags
	// persist like any other's so revalidation stays free, but Reload does not
	// re-admit it: only a launch inside the repository does, so the Feed never
	// accretes past clones a session was not launched in (R22, ADR-0020).
	Adopted bool `json:"adopted"`
}

// ID is the record's host-qualified identity (R18).
func (r Record) ID() domain.RepoID {
	return domain.RepoID{Host: r.Host, Owner: r.Owner, Name: r.Name}
}

// Repo reconstructs the domain repository the capability is derived from, so a
// consumer reads permissions, archived and disabled through one type (R7).
func (r Record) Repo() domain.Repo {
	return domain.Repo{
		ID:          r.ID(),
		Permissions: r.Permissions,
		Archived:    r.Archived,
		Disabled:    r.Disabled,
	}
}

// Capability is the recorded tri-state for this repository (R8). A record whose
// capability is not yet Known reads not-yet-known whatever its (empty) permissions
// say, so the fast path's Runs listing never counts as evidence the token may
// delete there (R8, AC8). A Known record derives permitted or refused from its
// permissions and archived flag. An archived repository is refused, and Permanent
// reports that the refusal can never lift (R9).
func (r Record) Capability() domain.Capability {
	if !r.Known {
		return domain.CapabilityUnknown
	}
	return r.Repo().Capability()
}

// Permanent reports that this repository's refusal is permanent: it is archived,
// so no retry, token change or elevation will ever make it writable (R9, AC6). A
// consumer distinguishes a permanently read-only repository from a merely refused
// one by this flag, which is why the archived bit is recorded rather than folded
// into the tri-state.
func (r Record) Permanent() bool {
	return r.Archived
}

// recordFrom builds a Record from an enumerated repository and its classification.
// The capability data rides along with the enumeration at no extra request (R7,
// AC7), so this allocates nothing on the wire.
func recordFrom(id domain.RepoID, repo apiRepo, hasRuns bool) Record {
	return Record{
		Host:        id.Host,
		Owner:       id.Owner,
		Name:        id.Name,
		Permissions: repo.Permissions,
		Archived:    repo.Archived,
		Disabled:    repo.Disabled,
		HasRuns:     hasRuns,
		Known:       true, // enumeration carries the capability, so it is known (R7)
	}
}

// PollSet is the repositories classified as having Runs, the ~26 the Feed polls
// (R13). It is never the ~163-repository probe set: a scheduler that inherited the
// probe set would exceed the secondary limit outright (R13). The order is
// unspecified; a consumer that needs one sorts.
func (d *Discovery) PollSet() []domain.RepoID {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]domain.RepoID, 0)
	for _, r := range d.records {
		if r.HasRuns {
			ids = append(ids, r.ID())
		}
	}
	return ids
}

// Records returns a copy of the whole classified set, both the poll set and the
// repositories with no Runs, so a consumer can gate destructive actions on the
// capability of any enumerated repository (R7).
func (d *Discovery) Records() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, 0, len(d.records))
	for _, r := range d.records {
		out = append(out, r)
	}
	return out
}

// Capability reports the recorded tri-state for id (R8). A repository discovery
// has not enumerated reads as not-yet-known, so a consumer keeps its destructive
// actions disabled until enumeration returns (R8, AC8): the fast-path repository
// reads not-yet-known between its Runs listing and enumeration completing, and its
// capability is never inferred from the fact that its Runs listed.
func (d *Discovery) Capability(id domain.RepoID) domain.Capability {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.records[id.String()]
	if !ok {
		return domain.CapabilityUnknown
	}
	return r.Capability()
}

// put stores or replaces a record and, when a probe produced it, records the
// probe's clock instant and whether it carried an ETag, which the two-tier
// refresh cadence reads (R11, R12). A record admitted without a probe (a reload,
// or the fast-path repository before its own probe) leaves the timing untouched.
func (d *Discovery) put(r Record) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[r.ID().String()] = r
}

// putProbed stores a probed record together with the probe's timing, under one
// lock so a reader never sees the record without its cadence bookkeeping.
func (d *Discovery) putProbed(r Record, now time.Time, hasETag bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := r.ID().String()
	d.records[key] = r
	d.probed[key] = now
	d.etagged[key] = hasETag
}

// newRepoID host-qualifies a repository and rejects any host but github.com
// (R18, AC14). No RepoID can be built without a host, and a repository resolving
// elsewhere contributes no entry and returns an error naming the host, rather
// than being silently attributed to github.com.
func newRepoID(host, owner, name string) (domain.RepoID, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = githubHost
	}
	if !strings.EqualFold(host, githubHost) {
		return domain.RepoID{}, &UnsupportedHostError{Host: host}
	}
	return domain.RepoID{Host: githubHost, Owner: owner, Name: name}, nil
}

// UnsupportedHostError reports a repository resolving to a host 2.0.0 does not
// serve (R18, AC14). It names the host and claims nothing about what the host is,
// the neutral rejection ADR-0009 settled on: a message that makes no class claim
// cannot make a false one.
type UnsupportedHostError struct {
	Host string
}

func (e *UnsupportedHostError) Error() string {
	return "repository host " + e.Host + " is not supported; gh-runs 2.0.0 serves github.com only"
}
