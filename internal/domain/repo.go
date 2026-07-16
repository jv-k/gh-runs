// Package domain holds the core types the tool reasons about. It performs no
// I/O and imports nothing internal, which is the one rule ADR-0011 fixes at the
// root of the tree.
package domain

import "fmt"

// RepoID identifies a repository, host-qualified as host/owner/name per
// ADR-0009. Host is a struct field from day one though 2.0.0 serves github.com
// alone, so adding GHES later is an additive change rather than a rekeying of
// every persisted entry.
type RepoID struct {
	Host  string
	Owner string
	Name  string
}

// String renders the identity as host/owner/name, the form every persisted key
// and the API base URL use.
func (r RepoID) String() string {
	return fmt.Sprintf("%s/%s/%s", r.Host, r.Owner, r.Name)
}
