# The domain types, and the Budget Readout

[ADR-0011](./0011-package-layout-and-dependency-direction.md) fixed the tree and gave `domain` a single line: Run, Workflow, Cache, Artifact, RepoID, no I/O. Sixteen feature documents then name fields, sort keys and enum members, and not one declares a struct. The argument that produced ADR-0011 applies one level down: sixteen implementers would agree on the vocabulary and each invent a different set of types. [BUILD-ORDER](../BUILD-ORDER.md) sharpens the gap into a schedule problem, because stages 1 and 2 begin when two types are agreed, the **Budget Readout** and the base `http.RoundTripper`. [ADR-0012](./0012-transport-chain-and-the-client-surface.md) fixed the second. Nothing fixed the first, or `domain` at all, which is stage 0's first package.

This ADR fixes the types. It adds no product decisions. Every struct below was measured against the live API on 2026-07-16 (Run 29516338954 on `cli/cli`, and gh 2.92.0 wherever the projection is compared), and every field list is the requirements' rather than the API's: the run object serves 35 keys, and `domain.Run` declares the ones a requirement reads. **A field the API serves and no requirement reads is not declared. Adding one later is a diff, not a decision.**

## Status, Conclusion and State are three distinct string types

```go
// Status is where a Run, Job or Step is in its lifecycle.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusWaiting    Status = "waiting"
	StatusRequested  Status = "requested"
	StatusPending    Status = "pending"
)

// Conclusion is how a Run, Job or Step ended. Its zero value means
// "not yet concluded": the API serves null until Status reaches completed.
type Conclusion string

const (
	ConclusionNone           Conclusion = ""
	ConclusionSuccess        Conclusion = "success"
	ConclusionFailure        Conclusion = "failure"
	ConclusionCancelled      Conclusion = "cancelled"
	ConclusionSkipped        Conclusion = "skipped"
	ConclusionTimedOut       Conclusion = "timed_out"
	ConclusionNeutral        Conclusion = "neutral"
	ConclusionActionRequired Conclusion = "action_required"
	ConclusionStale          Conclusion = "stale"
	ConclusionStartupFailure Conclusion = "startup_failure"
)

// State is a Workflow's lifecycle value. A Workflow has a State and
// never a Status or a Conclusion.
type State string

const (
	StateActive             State = "active"
	StateDisabledManually   State = "disabled_manually"
	StateDisabledInactivity State = "disabled_inactivity"
	StateDisabledFork       State = "disabled_fork"
	StateDeleted            State = "deleted"
)
```

**Three defined types, because the defining bug of the tools that came before can be made uncompilable.** [CONTEXT.md](../CONTEXT.md) calls conflating Status and Conclusion "the defining bug of the tools that came before this one". With distinct types, comparing a Status to a Conclusion is a compile error rather than a review comment. The one place the two legitimately meet is gh's permissive `-s` flag, whose 15 values are exactly the six Statuses plus the nine Conclusions ([cli-surface](../features/cli-surface/requirements.md) R4), and that parse belongs to the filter engine: one input, two typed predicates out, and no shared type anywhere.

**String-backed, never iota, because an unknown member must survive its own decoding.** [live-run-feed](../features/live-run-feed/requirements.md) R6 requires a Status or Conclusion the tool does not recognise to render verbatim, R6a forbids collapsing it to "unknown", and [workflow-management](../features/workflow-management/requirements.md) R3 requires the same of State. An integer enum destroys the unknown value at the decode boundary, which is the exact substitution R6 forbids. A string type carries it to the UI untouched. It follows that **nothing validates enum membership at decode time, deliberately.** Validation runs in the other direction only: [cli-surface](../features/cli-surface/requirements.md) R6 checks what a person typed against these constants before any request is built.

**Conclusion's zero value is the null, and there is no pointer.** `encoding/json` leaves a string empty where the payload says null, and [live-run-feed](../features/live-run-feed/requirements.md) R5's empty Conclusion cell is that `""` rendered. A `*Conclusion` would carry the same single fact and add a nil dereference to every row repaint.

## RepoID is host-qualified, and only ever stamped

```go
// RepoID identifies a repository as host/owner/name (ADR-0009).
type RepoID struct {
	Host  string
	Owner string
	Name  string
}

func (r RepoID) String() string { return r.Host + "/" + r.Owner + "/" + r.Name }
```

**It carries no JSON tags because no payload can populate it.** [ADR-0009](./0009-host-qualified-repo-identity.md) qualifies identity by host, and no GitHub payload states the host it was served from. The decoding caller knows which host it asked, so the caller stamps a `RepoID` onto every decoded object after decode. `String()` is the exact spelling [purge](../features/purge/requirements.md) R29's deletion log writes and R4's tuple carries.

```go
// Repo is one discovered repository: identity, permissions, and the two
// flags that gate destructive actions (repo-discovery R7).
type Repo struct {
	ID          RepoID      `json:"-"`
	Permissions Permissions `json:"permissions"`
	Archived    bool        `json:"archived"`
	Disabled    bool        `json:"disabled"`
}

// Permissions is the API's five-boolean permissions object, verbatim.
type Permissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// Capability is the recorded tri-state over a Repo's permissions
// (CONTEXT.md): what the token may do there, or that we do not yet know.
type Capability int

const (
	CapabilityUnknown Capability = iota
	CapabilityPermitted
	CapabilityRefused
)

// Capability derives the recorded value from an enumerated Repo
// (live-run-feed R17: push, and not archived).
func (r Repo) Capability() Capability {
	if r.Permissions.Push && !r.Archived {
		return CapabilityPermitted
	}
	return CapabilityRefused
}
```

**Capability's zero value is `CapabilityUnknown`, and that is the decision.** [CONTEXT.md](../CONTEXT.md) insists the tri-state is never a boolean and that not-yet-known "is not the absence of the first two". Making it the zero value makes a forgotten initialisation fail closed: a Capability nobody set keeps destructive actions disabled, which is [repo-discovery](../features/repo-discovery/requirements.md) R8's rule and [live-run-feed](../features/live-run-feed/requirements.md) R18's. `Capability` is an iota where the three enums above are strings because it is our word rather than the API's: no payload ever carries one, so there is no unknown member to preserve. The method returns only the two known values, because `Unknown` means "no enumerated Repo exists yet", and holding a `Repo` is already proof that this is not the case at hand. An archived repository's permanence ([repo-discovery](../features/repo-discovery/requirements.md) R9) is the Gate's message to render, not a fourth value.

## Run

```go
// Run is one invocation of a Workflow, and the central object of this
// tool. JSON tags are the API's names (ADR-0011): the gh-compatible
// spellings live in cli's projection and nowhere else.
type Run struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	DisplayTitle       string     `json:"display_title"`
	RunNumber          int        `json:"run_number"`
	RunAttempt         int        `json:"run_attempt"`
	Event              string     `json:"event"`
	Status             Status     `json:"status"`
	Conclusion         Conclusion `json:"conclusion"`
	WorkflowID         int64      `json:"workflow_id"`
	HeadBranch         string     `json:"head_branch"`
	HeadSHA            string     `json:"head_sha"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	RunStartedAt       time.Time  `json:"run_started_at"`
	PreviousAttemptURL string     `json:"previous_attempt_url"`
	HTMLURL            string     `json:"html_url"`
	Actor              User       `json:"actor"`
	TriggeringActor    User       `json:"triggering_actor"`

	// Stamped by the decoding caller, never decoded. See below.
	Repo         RepoID `json:"-"`
	WorkflowName string `json:"-"`
}

// User is an actor reduced to the one field a requirement reads.
type User struct {
	Login string `json:"login"`
}

// EffectiveStart is the Feed's sort key: run_started_at, falling back to
// created_at where the API served null (live-run-feed R8).
func (r Run) EffectiveStart() time.Time {
	if r.RunStartedAt.IsZero() {
		return r.CreatedAt
	}
	return r.RunStartedAt
}
```

**Every id in this file is `int64`, on measurement.** The reference Run's id is 29,516,338,954: eleven digits, and roughly fourteen times past `int32`'s ceiling. [live-run-feed](../features/live-run-feed/requirements.md) R4a already sizes its column to 11 digits from the same observation.

**`Repo` and `WorkflowName` are stamped, and the tags say so.** The host argument above covers `Repo`. `WorkflowName` is stamped because **the run object has no workflow-name key**, measured: all 35 keys were enumerated and none carries one, which is consistent with [cli-surface](../features/cli-surface/requirements.md)'s constraint that ruleset Runs surface no Workflow name. The name is resolved client-side by joining `WorkflowID` against the discovered Workflow list, the join happens where the fan-out already holds both sides, and a Run whose join finds nothing keeps the empty string honestly. The job object does carry a `workflow_name` key (measured), and it is no use here: the Feed needs the name before any Job is ever fetched.

**`EffectiveStart` lives on the type so the fallback exists exactly once.** The Feed sorts by it, while cli's `startedAt` column emits the raw field rather than the fallback, exactly as gh does. The filter engine's Created predicate reads `CreatedAt` instead, because the server's `created` parameter filters on `created_at` and the client half must agree with the field it pushes ([ADR-0016](./0016-the-filter-representation.md)). An earlier form of this sentence had the filter reading `EffectiveStart`, and ADR-0016 records why that was wrong. Without one home, the sort key's consumers reimplement R8's fallback and eventually disagree.

**`TriggeringActor` is declared beside `Actor` because an open question is answered by comparing them.** [notifications](../features/notifications/requirements.md) asks whether a re-run re-attributes a Run, and though that feature is deferred to 2.1 ([PRD](../PRD.md) Scope), the pair rides in every payload already and the domain carries the answer for when it is built.

## The projection: sixteen gh names, measured to their sources

[cli-surface](../features/cli-surface/requirements.md) R7 fixes gh's sixteen `--json` fields, and [ADR-0011](./0011-package-layout-and-dependency-direction.md) fixes where the projection lives: `cli`, never `domain`. Neither fixed the mapping. It was measured by requesting the same Run both ways (gh 2.92.0 and the raw API, Run 29516338954):

| gh field | Source on `domain.Run` |
|---|---|
| `attempt` | `RunAttempt` (`run_attempt`) |
| `conclusion` | `Conclusion` |
| `createdAt` | `CreatedAt` |
| `databaseId` | `ID` (`id`) |
| `displayTitle` | `DisplayTitle` |
| `event` | `Event` |
| `headBranch` | `HeadBranch` |
| `headSha` | `HeadSHA` |
| `name` | `Name` |
| `number` | `RunNumber` (`run_number`) |
| `startedAt` | `RunStartedAt`, raw, never `EffectiveStart` |
| `status` | `Status` |
| `updatedAt` | `UpdatedAt` |
| `url` | `HTMLURL` (`html_url`), see below |
| `workflowDatabaseId` | `WorkflowID` (`workflow_id`) |
| `workflowName` | `WorkflowName`, the stamped join |

**`url` is `html_url`, and the collision is why the row is called out.** The API serves a field literally named `url`, and it is the api.github.com address. gh's `url` is the browser one. Measured: gh emitted `https://github.com/cli/cli/actions/runs/29516338954` while the API's own `url` on the same Run read `https://api.github.com/repos/cli/cli/actions/runs/29516338954`. A projection written from the field names alone would ship the wrong URL and look right.

## Workflow, Job and Step

```go
// Workflow is an automation definition. Its Runs outlive it (CONTEXT.md).
type Workflow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Repo RepoID `json:"-"`
}

// Job is a unit of work within an Attempt, with its own Status and
// Conclusion (CONTEXT.md). Steps arrive inline at no extra request
// (run-detail, resolved open question 1).
type Job struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	RunAttempt  int        `json:"run_attempt"`
	Name        string     `json:"name"`
	Status      Status     `json:"status"`
	Conclusion  Conclusion `json:"conclusion"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
	Steps       []Step     `json:"steps"`
}

// Step is one command or Action invocation within a Job, in the
// measured shape: number, name, status, conclusion, started_at,
// completed_at.
type Step struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      Status     `json:"status"`
	Conclusion  Conclusion `json:"conclusion"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt time.Time  `json:"completed_at"`
}
```

Job and Step reuse `Status` and `Conclusion` because the API reuses the vocabulary, and [CONTEXT.md](../CONTEXT.md) already says so: a Run, Job or Step has a Status and a Conclusion, and only a Workflow has a State.

## Artifact and Cache, with the trap encoded

```go
// Artifact is a file bundle a Run uploaded. An expired one is a
// Tombstone (CONTEXT.md): the listing survives, the bytes are gone.
type Artifact struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	SizeInBytes int64     `json:"size_in_bytes"`
	Expired     bool      `json:"expired"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	Repo RepoID `json:"-"`
}

// Tombstone reports whether the bytes are already gone.
func (a Artifact) Tombstone() bool { return a.Expired }

// ReclaimableBytes is what deleting this Artifact actually recovers. A
// Tombstone still reports its original size_in_bytes, and believing that
// number is the measured mistake: 15.50 of cli/cli's 15.55 GB of
// Artifact bytes were tombstoned and reclaimed nothing (PRD).
func (a Artifact) ReclaimableBytes() int64 {
	if a.Expired {
		return 0
	}
	return a.SizeInBytes
}

// Cache is a keyed blob scoped to a repository and a ref, the thing
// Reclamation deletes. Never this tool's local-store (CONTEXT.md).
type Cache struct {
	ID             int64     `json:"id"`
	Key            string    `json:"key"`
	Ref            string    `json:"ref"`
	Version        string    `json:"version"`
	SizeInBytes    int64     `json:"size_in_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`

	Repo RepoID `json:"-"`
}
```

**`ReclaimableBytes` puts the canon's most expensive number where no consumer can miss it.** The [PRD](../PRD.md)'s constraints table carries the measurement: raw Artifact bytes exceed Cache bytes, and reclaimable Artifact bytes are ~220× smaller than the Caches'. [storage-reclamation](../features/storage-reclamation/requirements.md) leads with Caches on exactly that arithmetic. A view that sums `SizeInBytes` where it means `ReclaimableBytes` recreates the defect this method exists to retire, and now it has to do so by name.

## The Budget Readout is governor's, and it is four fields

```go
package governor

// Readout is the Budget Readout (CONTEXT.md): what the governor observed
// about the account's rate limiting at a moment. An observation, never a
// policy, and never the Budget, which is the input it is most easily
// confused with.
type Readout struct {
	Remaining int       // primary allowance left, from the last response's headers (R5)
	Reset     time.Time // the reset or resume instant. Zero when none is derivable (R9)
	Pressure  bool      // R8a's projection. Never true while burn is zero
	Exhausted bool      // authoritative for R9, live-run-feed R30, polling-scheduler R16.
	                    // Also true through a secondary-limit backoff (ADR-0018)
}
```

**It lives in `governor`, not `domain`.** It describes this tool's spending, not a GitHub Actions object, and [ADR-0012](./0012-transport-chain-and-the-client-surface.md) already rejected `domain` as the parking spot for a cross-package type whose vocabulary is not the domain's. Every consumer may import `governor` under [ADR-0011](./0011-package-layout-and-dependency-direction.md)'s table: `scheduler`, `discovery`, `ops`, `cli` and `tui` all carry the arrow already. `store` may not, and does not consume the Readout. Should [local-store](../features/local-store/requirements.md) open question 3 ever resolve toward persisting one, `main.go` mediates, exactly as it already does for the transport chain.

**Delivery is a pull, and the value is a copy.** `governor` exposes `Readout() Readout`, computed under its own lock and safe for any goroutine to call. [rate-governor](../features/rate-governor/requirements.md) R8's "publish to any component that asks" is a getter, asked. How the value then travels into tabs is the routing table's row ("the Budget Readout reaches every tab and pane") and the async model's business, not this ADR's.

**A zero `Reset` is R9 kept, not a bug.** R9 requires exhaustion reported without a time rather than with an invented one, and [live-run-feed](../features/live-run-feed/requirements.md) open question 9 already carries the fallback wording for exactly this case. `Reset.IsZero()` is that case's name in code.

**`Exhausted` covers both limits, an amendment [ADR-0018](./0018-the-fanout-concurrency-shape.md) made in place.** The struct was drafted as a primary-limit observation. ADR-0018 has a rate-limit classified response (a secondary 403 or 429) publish exhaustion with the `Retry-After` derived resume, so the field reads "the account may not issue requests right now, and `Reset` says when that changes", whichever limit said so. No field was added or retyped, and every consumer's behaviour is unchanged.

## The clock is an alias, not an abstraction

```go
package clock

import "github.com/jonboulle/clockwork"

// Clock is the injected clock five packages take (ADR-0011).
type Clock = clockwork.Clock

// Real returns the wall clock main.go injects in production.
func Real() Clock { return clockwork.NewRealClock() }
```

[ADR-0013](./0013-dependency-pins.md) pinned clockwork for its fake. Declaring an interface of our own on top of it would cost an adapter in every test that reaches for `clockwork.NewFakeClock`, to insulate five packages from a dependency the pin set already owns. The alias gives the importers one name and the tests the fake, unadapted.

## Considered Options

**Integer enums for Status, Conclusion and State.** Idiomatic and cheap to compare, and wrong here: decoding destroys the unknown member, and R6a's verbatim rendering is a requirement on exactly that member.

**Pointers for the nullable fields** (`*Conclusion`, `*time.Time`), which is google/go-github's style. The zero values already carry the null's entire meaning (`""` is unconcluded, `IsZero()` is unstarted), and every pointer adds a crash path to a TUI that repaints on every poll.

**Adopting google/go-github's structs outright.** A second client library imported for its types alone, all-pointer fields per the row above, and field sets that track its releases rather than our requirements.

**One shared type spanning Status and Conclusion**, which is what gh's `-s` flag spans. It would make the canon's defining bug a declared type. The flag's breadth is the filter engine's parsing problem, one input and two typed outputs, and no struct anywhere holds a value that might be either.

**The Readout in `domain`.** Covered above: wrong vocabulary, and the import table never needed the dodge.

**Resolving `Repo` and `WorkflowName` at render time instead of stamping.** Every consumer repeats the join: the Feed row, cli's projection, [purge](../features/purge/requirements.md) R30's inspect rows and R29's log line all want the same two answers. The fan-out holds both sides of the join at decode time, so it stamps once and the joins cannot drift apart.

## Consequences

**`domain` stays free of I/O.** Stamping is the decoding caller's act, and every method above is arithmetic over fields already held.

**Decode never rejects, and input validation never decodes.** An enum value the API invents flows to the UI verbatim (R6a), and a value a person typos is caught against the constants before the wire ([cli-surface](../features/cli-surface/requirements.md) R6). The two directions never share code, which is what keeps each honest.

**The two serialisations stay two.** Tags here are the API's. gh's sixteen spellings appear in `cli`'s projection and nowhere else, per [ADR-0011](./0011-package-layout-and-dependency-direction.md), and the mapping table above is that projection's spec.

**Fields are additive, and removals are decisions.** The run object serves 35 keys and this file declares the read ones. Adding a field a new requirement reads is a diff. Removing or retyping one is a change to a contract several packages compile against, and earns its way back through here.

**BUILD-ORDER's gate is open.** The "two types first" sentence is satisfied: the base `http.RoundTripper` was [ADR-0012](./0012-transport-chain-and-the-client-surface.md)'s, the Readout is now this one's, and stage 0's `domain` has contents rather than a name.

**What this ADR deliberately does not fix.** `ops`'s `Plan` and `Confirmed` structs are stage 9's, owed to [purge](../features/purge/requirements.md) R30 before that stage begins and buildable over these types when it arrives. The `tea.Msg` catalog and the fan-out's concurrency shape are the async model's, unwritten. The filter engine's representation is stage 5's. Each is a gap these types make expressible, and none is closed here.
