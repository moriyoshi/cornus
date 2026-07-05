//go:build windows

package conduithost

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// acquireLease takes the accept lease at path, or reports ErrLeaseHeld.
//
// LOCKFILE_FAIL_IMMEDIATELY is the direct equivalent of flock's LOCK_NB, and is
// wanted for the same reason: a follower must be able to give up and retry rather
// than park uninterruptibly for the whole lifetime of another process's hosting.
//
// Windows releases file locks when the handle closes, and closes every handle when
// a process terminates however it terminates, so the death-detection property the
// lease depends on holds here too.
func acquireLease(path string) (*lease, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, ^uint32(0), ^uint32(0), ol)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrLeaseHeld
		}
		return nil, err
	}
	return &lease{f: f}, nil
}

func (l *lease) release() {
	if l == nil || l.f == nil {
		return
	}
	h := windows.Handle(l.f.Fd())
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(h, 0, ^uint32(0), ^uint32(0), ol)
	_ = l.f.Close()
	l.f = nil
}
