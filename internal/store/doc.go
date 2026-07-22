package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// docSchemaVersion versions the named-document envelope independently of the HTTP
// entry schema, so the two on-disk formats evolve without either forcing the
// other's discard (local-store R12). A document carrying any other version reads
// as a miss and is rebuilt, exactly as an unrecognised entry is.
const docSchemaVersion = 1

// docsSubdir holds the named documents, one JSON file each, in a subdirectory of
// the store's own directory. It is deliberately not the top level: the entry
// eviction and invalidation sweeps glob t.dir/*.json non-recursively (store.go),
// so a document at the top level would be summed toward the 50 MB bound, decoded
// as an entry (it is not one), taken to have the zero last-revalidated time, and
// evicted before any real entry under disk pressure. A subdirectory keeps
// discovery's results out of that machinery entirely.
const docsSubdir = "docs"

// docEnvelope wraps a persisted value with its schema version. The payload is
// held as raw JSON so the envelope decodes without knowing the value's type,
// which is what lets the store persist discovery's records without importing
// discovery (ADR-0011).
type docEnvelope struct {
	Schema  int             `json:"schema"`
	Payload json.RawMessage `json:"payload"`
}

// docFileName maps a document name to its on-disk basename, hashing the name to hex
// exactly as the entry key does (store.go's key). This is what keeps a document name
// from traversing: a hex string is a single path component with no separator and no
// dot-dot, so filepath.Join with it can never escape the docs subdirectory, whatever
// the caller passed. The name is caller-supplied (repo-discovery's docName today, a
// per-repository key tomorrow), so it is disciplined at the primitive rather than at
// every future caller. Hashing keeps the store's on-disk filenames uniformly safe,
// the same reason the entry cache hashes its keys.
func docFileName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:]) + ".json"
}

// SaveDoc persists v as a named JSON document under the store's docs
// subdirectory, the primitive local-store R2 requires for discovery's results
// (the classification and the recorded capability, repo-discovery R19). It is
// writer-gated and best-effort, exactly like the entry save it sits beside: a
// degraded reader writes nothing (R21, R23), and a write failure costs a future
// cold start its speed and nothing else (R11), so it is swallowed rather than
// surfaced. The document is host-qualified by its content, never by v, so the
// primitive itself imposes no identity scheme and stays general.
func (t *Transport) SaveDoc(name string, v any) {
	if t.dir == "" || !t.writer {
		return
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	data, err := json.Marshal(docEnvelope{Schema: docSchemaVersion, Payload: payload})
	if err != nil {
		return
	}
	dir := filepath.Join(t.dir, docsSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// Serialise with the entry writes: SaveDoc and a poll's persist may run at
	// once, and the atomic rename must not race an eviction sweep (store.go's
	// writeMu comment). The write itself lands by rename, so a reader in this
	// process or another never sees a half-written document (AC18).
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = writeFileAtomic(dir, filepath.Join(dir, docFileName(name)), data)
}

// LoadDoc reads the named document into v and reports whether a usable one was
// found. A missing, unreadable, corrupt or wrong-schema document reads as absent
// rather than as an error, so a cold start over a bad store rebuilds by
// re-probing and never fails a launch (local-store R11, R13). A reader loads on
// the same terms as a writer: the degraded-reader path a Feed takes while a Purge
// holds the lock still paints discovery's results from disk (R23).
func (t *Transport) LoadDoc(name string, v any) bool {
	if t.dir == "" {
		return false
	}
	data, err := readFile(filepath.Join(t.dir, docsSubdir, docFileName(name)))
	if err != nil {
		return false
	}
	var env docEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return false
	}
	if env.Schema != docSchemaVersion {
		return false
	}
	if err := json.Unmarshal(env.Payload, v); err != nil {
		return false
	}
	return true
}
