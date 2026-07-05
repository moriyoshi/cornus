//go:build windows

package conduithost

import (
	"os"

	"golang.org/x/sys/windows"
)

// withLock runs fn while holding an exclusive lock on lockPath.
//
// Windows gets a real lock rather than the no-op agentproc settles for
// (cmd/cornus/internal/agentproc/lock_other.go), because the two situations are
// not alike. There the lock only serializes spawning a daemon that a ping
// re-check would catch anyway; here it serializes BINDING A PORT, and losing the
// race produces exactly the split-brain pair of conduits on one address that this
// package exists to make impossible. LockFileEx with LOCKFILE_EXCLUSIVE_LOCK is
// the direct equivalent of flock(LOCK_EX).
func withLock(lockPath string, fn func() error) error {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	// A zero length means "the whole file" only for the byte range given; lock a
	// fixed maximal range so two processes cannot pick disjoint ranges and both win.
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, ^uint32(0), ^uint32(0), ol); err != nil {
		return err
	}
	defer windows.UnlockFileEx(h, 0, ^uint32(0), ^uint32(0), ol)
	return fn()
}
