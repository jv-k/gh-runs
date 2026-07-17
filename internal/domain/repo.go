package domain

// RepoID identifies a repository as host/owner/name (ADR-0009).
type RepoID struct {
	Host  string
	Owner string
	Name  string
}

func (r RepoID) String() string { return r.Host + "/" + r.Owner + "/" + r.Name }

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
