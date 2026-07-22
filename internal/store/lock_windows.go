//go:build windows

package store

import (
	"os"

	"golang.org/x/sys/windows"
)

// flockTry takes a non-blocking exclusive lock via LockFileEx, the Windows
// equivalent of flock(2)'s LOCK_EX|LOCK_NB (local-store R22). LOCKFILE_EXCLUSIVE_LOCK
// is the write lock; LOCKFILE_FAIL_IMMEDIATELY makes it non-blocking, returning at
// once on contention rather than waiting. The kernel releases the lock when the
// holding process exits, so, as on unix, there is no stale-lock case to detect.
// 2.0.0 ships Windows, so this limb is required rather than optional, and it uses
// golang.org/x/sys/windows, already in the module graph, so no new module pin
// arrives with it (ADR-0013).
func flockTry(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, // reserved, must be zero
		1, // lock one byte, low word
		0, // lock one byte, high word
		ol,
	)
}
