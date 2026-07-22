//go:build unix

package store

import (
	"os"
	"syscall"
)

// flockTry takes a non-blocking exclusive flock(2). The kernel releases it when
// the holding process exits by any means, SIGKILL included (local-store R22), so
// there is no stale-lock case to detect and the lock file needs no PID. A
// separate open of the same file, even within this process, is denied by flock,
// which is what lets one holder exclude another and makes AC18 provable in a
// single test process. It reaches flock through the standard library alone, so
// ADR-0013's pin set is unchanged on this limb.
func flockTry(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
