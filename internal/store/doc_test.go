package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonboulle/clockwork"

	"github.com/jv-k/gh-runs/v2/internal/store"
)

// doc is a store-local stand-in for whatever a caller persists. The store must
// not know discovery's record shape (ADR-0011: store does not import discovery),
// so the primitive is generic over any JSON value and this test proves it with a
// type of its own rather than discovery's.
type doc struct {
	Key     string `json:"key"`
	HasRuns bool   `json:"has_runs"`
}

// docFile returns the on-disk path SaveDoc writes name to, replicating the store's
// name hashing so a test that seeds a corrupt or wrong-schema file writes it where
// LoadDoc looks. It pins the traversal-safe on-disk layout: a document's filename is
// the hash of its name, so no name can carry a path separator onto disk.
func docFile(dir, name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(dir, "docs", hex.EncodeToString(sum[:])+".json")
}

// TestDocPersistsAcrossInstances proves the named-document primitive local-store
// R2 requires for discovery's results. A value a writer saves is read back by a
// second Transport over the same directory, which is the cross-session
// persistence a cold start reloads (repo-discovery R19, local-store AC7): the
// second instance never held the value in memory, so a successful read is a read
// from disk and not from a shared object.
func TestDocPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	clk := clockwork.NewFakeClock()

	want := []doc{
		{Key: "github.com/jv-k/gh-runs", HasRuns: true},
		{Key: "github.com/cli/cli", HasRuns: false},
	}

	// The first Transport takes the write lock and persists the document. base is
	// nil because this primitive touches no network: SaveDoc and LoadDoc are disk
	// only.
	writer := store.NewTransport(nil, dir, clk)
	writer.SaveDoc("discovery", want)

	// A second Transport over the same directory does not get the lock, so it is a
	// reader (local-store R21). It still reads the persisted document, which is the
	// degraded-reader path a Feed takes while a Purge holds the lock (R23).
	reader := store.NewTransport(nil, dir, clk)
	var got []doc
	if !reader.LoadDoc("discovery", &got) {
		t.Fatal("LoadDoc reported no document; the writer's SaveDoc did not reach disk")
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLoadDocAbsentCorruptAndWrongSchema pins the three miss cases local-store
// R12 and R13 demand: an absent document, a corrupt one, and one whose schema
// version the binary does not recognise all read as "no document" rather than
// failing, because the store is derived state that is always safe to rebuild (a
// re-probe). A launch must never fail on a bad store, and an unknown schema is
// discarded rather than migrated in place or read optimistically.
func TestLoadDocAbsentCorruptAndWrongSchema(t *testing.T) {
	dir := t.TempDir()
	clk := clockwork.NewFakeClock()
	tr := store.NewTransport(nil, dir, clk)

	var got []doc
	if tr.LoadDoc("never-written", &got) {
		t.Error("LoadDoc reported a document that was never written")
	}

	// A corrupt file reads as absent. Its on-disk name is the hash of the document
	// name (the traversal-safe layout), so the test seeds it at that hashed path.
	docPath := docFile(dir, "corrupt")
	if err := os.MkdirAll(filepath.Dir(docPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tr.LoadDoc("corrupt", &got) {
		t.Error("LoadDoc read a corrupt document instead of discarding it (R13)")
	}

	// A document whose envelope schema the binary does not recognise reads as
	// absent, so it is discarded rather than migrated in place (R12).
	wrongSchema := docFile(dir, "future")
	if err := os.WriteFile(wrongSchema, []byte(`{"schema":999,"payload":[{"key":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tr.LoadDoc("future", &got) {
		t.Error("LoadDoc read a document with an unrecognised schema version (R12)")
	}
}

// TestSaveDocNameCannotTraverse pins the document primitive's traversal safety. A
// document name carrying ".." and path separators must not escape the docs
// subdirectory, matching store.go's entry-key discipline where a key is hashed to
// hex so a filename can never traverse (store.go's key). The name is hashed to a
// safe basename, so the escape is impossible by construction rather than caught by
// a check, and the document still round-trips by the same name.
func TestSaveDocNameCannotTraverse(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	clk := clockwork.NewFakeClock()
	tr := store.NewTransport(nil, storeDir, clk)

	// A name climbing out of docs/ and the store dir. From storeDir/docs, "../../escape"
	// would clean to root/escape.json, the sentinel a traversal creates.
	evil := "../../escape"
	tr.SaveDoc(evil, []doc{{Key: "pwned"}})

	sentinel := filepath.Join(root, "escape.json")
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("SaveDoc wrote outside the store dir to %s; a traversal name escaped docs/", sentinel)
	}

	// The document still round-trips by the same name, hashed to a safe basename, so
	// the fix closes the escape without dropping the primitive's function.
	var got []doc
	if !tr.LoadDoc(evil, &got) {
		t.Fatal("a traversal-shaped name did not round-trip through SaveDoc/LoadDoc")
	}
	if len(got) != 1 || got[0].Key != "pwned" {
		t.Errorf("round-tripped %v, want the saved document back", got)
	}

	// Nothing landed outside docs/: the only document file is a hashed child of docs/,
	// and no stray file appeared at the store root.
	inDocs, _ := filepath.Glob(filepath.Join(storeDir, "docs", "*.json"))
	if len(inDocs) != 1 {
		t.Fatalf("docs/ holds %d document files, want exactly one hashed document", len(inDocs))
	}
	stray, _ := filepath.Glob(filepath.Join(storeDir, "*.json"))
	if len(stray) != 0 {
		t.Errorf("store root holds %d stray files, want none (documents live only under docs/)", len(stray))
	}
}

// TestReaderDoesNotWriteDoc pins local-store R21: a process that did not take the
// lock reads the store but never writes it. A reader's SaveDoc is a no-op, so its
// value never overwrites the writer's on disk.
func TestReaderDoesNotWriteDoc(t *testing.T) {
	dir := t.TempDir()
	clk := clockwork.NewFakeClock()

	writer := store.NewTransport(nil, dir, clk)
	writer.SaveDoc("discovery", []doc{{Key: "github.com/jv-k/gh-runs", HasRuns: true}})

	reader := store.NewTransport(nil, dir, clk)
	reader.SaveDoc("discovery", []doc{{Key: "github.com/other/repo", HasRuns: false}})

	// A third instance reads what is on disk. The reader's write must not have
	// landed, so the writer's record is what survives.
	verify := store.NewTransport(nil, dir, clk)
	var got []doc
	if !verify.LoadDoc("discovery", &got) {
		t.Fatal("LoadDoc reported no document")
	}
	if len(got) != 1 || got[0].Key != "github.com/jv-k/gh-runs" {
		t.Errorf("on-disk document = %+v, want only the writer's record; a reader wrote to the store", got)
	}
}
