// Package ops owns every write in the product (ADR-0011, ADR-0019). It freezes a
// selection into a Plan, prices the confirmation friction R7 demands of that blast
// radius, validates the operator's answer into an unforgeable Confirmed, and executes
// it: the deletes paced by the governor's AIMD ramp on the wire, the deletion log
// written one line per attempt, the failure contract counted per Run.
//
// Execute is the only call that issues a DELETE and the only call that writes the
// deletion log, and that invariant is exact. Dispatch (dispatch.go) is the one other
// write entry: the workflow_dispatch POST that must return the Run it created
// (workflow-dispatch R16), which no Summary carries, and over which the canon prices no
// graduated confirmation, so it cannot ride Execute's frozen-set-to-Summary shape and
// sits beside it rather than distorting it. It is still ops's, still travels the one
// governed transport, and issues a POST rather than a DELETE, so the DELETE safety is
// untouched. ADR-0019 left dispatch's confirmation shape open for exactly this call.
//
// The safety properties are structural, not conventional. A caller cannot reach
// Execute without a Confirmed, and cannot build a Confirmed without ops.Confirm
// validating an explicit act against the Plan's friction, and cannot build a Plan
// except through ops.Plan, whose fields are unexported. So purge R9 (no path from
// a selection to a DELETE skips confirmation) and R29 (the deletion log gates the
// delete) are things the compiler refuses to let a tab break, rather than promises
// four call sites make.
//
// ops imports domain, clock, config, filter, ghlink and governor, and never store,
// scheduler or tui (ADR-0011). It reaches the transport only through the Requester
// seam, which main.go fills with a ghclient over the store-then-governor-then-limiter
// chain and a test fills with the same ghclient over a go-vcr cassette, so a Purge
// is exercised end to end against what the API actually said with no live DELETE
// (purge R26, R28).
package ops

import "github.com/jv-k/gh-runs/v2/internal/domain"

// Kind is the class of object an Item names. The values are exactly purge R29's
// kind column, so a deletion log line is a field copy (ADR-0019).
type Kind string

const (
	KindRun      Kind = "run"
	KindLog      Kind = "log"
	KindCache    Kind = "cache"
	KindArtifact Kind = "artifact"
	KindWorkflow Kind = "workflow"
)

// Operation is the verb a Plan was built for. Delete resolves its endpoint per
// Item Kind. Cancel, force-cancel and the two re-runs act on Runs alone
// (run-lifecycle R16, ADR-0019). Enable and disable act on a Workflow, and are the
// two reversible PUT writes the rate-governor names among its ten (rate-governor R2,
// workflow-management R5). All ten travel Execute, the one write path (ADR-0011).
type Operation string

const (
	OpDelete      Operation = "delete"
	OpCancel      Operation = "cancel"
	OpForceCancel Operation = "force-cancel"
	OpRerun       Operation = "rerun"
	OpRerunFailed Operation = "rerun-failed"
	OpEnable      Operation = "enable"
	OpDisable     Operation = "disable"
)

// SkipReason is why Execute will not attempt an Item. Stamped by Plan, and the
// vocabulary is purge R11's and R12's (ADR-0019).
type SkipReason string

const (
	SkipNone         SkipReason = ""
	SkipReadOnly     SkipReason = "repository is read-only"
	SkipArchived     SkipReason = "repository is archived"
	SkipNotCompleted SkipReason = "run is not completed"
	SkipDeleted      SkipReason = "workflow is deleted"
)

// Item is one member of a frozen set: purge R4's tuple (host, owner, repo, id),
// the Kind, and the domain object that is its display row (R30, AC22). Exactly one
// object pointer is set, by the constructor that copies the object in. The tuple
// fields ride beside the object rather than behind a type switch because the two
// consumers that want them bare, Execute's request building and R29's log line,
// are the two places a switch would otherwise repeat (ADR-0019).
type Item struct {
	Repo domain.RepoID
	Kind Kind
	ID   int64

	Run      *domain.Run
	Cache    *domain.Cache
	Artifact *domain.Artifact
	Workflow *domain.Workflow

	// Skip is stamped by Plan. A value a caller sets is overwritten (ADR-0019).
	Skip SkipReason
}

// RunItem freezes a Run into an Item. It copies the Run by value and stores a
// pointer to its own copy, because R5's freeze is a memory property: the Feed's
// projections are rewritten under every poll, so an Item pointing into a live
// slice is a frozen set in name only (ADR-0019). The tuple is derived from the
// object, so the pair cannot disagree.
func RunItem(r domain.Run) Item {
	return Item{Repo: r.Repo, Kind: KindRun, ID: r.ID, Run: &r}
}

// LogItem freezes a Run's logs into an Item: Kind "log", carrying the Run's own id
// (log-viewer R17). The Run rides along as the display row, exactly as it does for
// a Run deletion, so the inspect view renders one shape (ADR-0019).
func LogItem(r domain.Run) Item {
	return Item{Repo: r.Repo, Kind: KindLog, ID: r.ID, Run: &r}
}

// WorkflowItem freezes a Workflow into an Item: Kind "workflow", carrying the
// Workflow's own id, which the enable and disable endpoints address (workflow-management
// R5). The Workflow rides along as the display row and carries its State, which Plan
// reads to refuse a deleted Workflow (R9). Its Repo is the Workflow's owning repository,
// stamped at fetch, so the toggle resolves to the right repository even under all-repos
// (R0). The constructor copies the Workflow by value, the same freeze the other
// constructors make (ADR-0019).
func WorkflowItem(w domain.Workflow) Item {
	return Item{Repo: w.Repo, Kind: KindWorkflow, ID: w.ID, Workflow: &w}
}

// CacheItem freezes a Cache into an Item (storage-reclamation R17).
func CacheItem(c domain.Cache) Item {
	return Item{Repo: c.Repo, Kind: KindCache, ID: c.ID, Cache: &c}
}

// ArtifactItem freezes an Artifact into an Item (storage-reclamation R17).
func ArtifactItem(a domain.Artifact) Item {
	return Item{Repo: a.Repo, Kind: KindArtifact, ID: a.ID, Artifact: &a}
}
