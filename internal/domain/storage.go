package domain

import "time"

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
