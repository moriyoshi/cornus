//go:build unix

package conduithost

import (
	"os"
	"syscall"
)

// withLock runs fn while holding an exclusive flock on lockPath, so two processes
// racing to open the same port serialize: one binds and the other, re-checking
// inside the lock, finds the incumbent and joins it. Without this, both would find
// nothing, both would bind, and one would fail with a bare "address already in
// use" — the very outcome the rendezvous exists to prevent.
//
// Deliberately the same shape as agentproc.withLock
// (cmd/cornus/internal/agentproc/lock_unix.go), which serializes agent spawns for
// the same reason.
func withLock(lockPath string, fn func() error) error {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
