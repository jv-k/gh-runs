package store

import "os"

// lockName is the advisory write lock's filename, in the store's own directory
// (local-store R21, ADR-0017). Its contents are empty and stay empty: nothing
// reads it, so it needs no PID and no schema, which is what keeps staleness the
// kernel's problem rather than ours (R22).
const lockName = "store.lock"

// fileLock is an advisory, non-blocking, exclusive lock over a file. The kernel
// releases it when the holding process exits by any means, SIGKILL included, so
// there is no stale-lock case to detect (local-store R22). flock(2) on unix and
// LockFileEx on Windows both give those semantics; flockTry spells the platform
// half, and 2.0.0 ships both.
type fileLock struct {
	f *os.File
}

// acquireLock opens path and takes the exclusive lock without blocking. It
// returns (lock, true) on success and (nil, false) on contention or on any other
// failure, such as a filesystem that does not honour the lock, which a caller
// treats identically: degrade to a reader on the spot (local-store R21, R22). The
// file is created empty and is never written to.
func acquireLock(path string) (*fileLock, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := flockTry(f); err != nil {
		_ = f.Close()
		return nil, false
	}
	return &fileLock{f: f}, true
}

// release drops the lock by closing the file, which unlocks it. Production never
// calls it: a process holds the lock for its lifetime and the kernel releases it
// at exit (local-store R21). Tests call it to stand in for a holding process
// ending, so a later acquisition can prove the lock was freed (R22).
func (l *fileLock) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
