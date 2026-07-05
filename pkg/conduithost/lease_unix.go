//go:build unix

package conduithost

import (
	"errors"
	"os"
	"syscall"
)

// acquireLease takes the accept lease at path, or reports ErrLeaseHeld.
//
// Non-blocking, deliberately. A blocking flock blocks the OS THREAD, not just the
// goroutine, and nothing can cancel it — so a follower waiting for the lease would
// be unkillable, and a process shutting down would hang in it. That is tolerable
// for the create-or-join mutex, which is held across a few syscalls; it is not
// tolerable for a lease held for the entire lifetime of hosting. The caller retries
// instead, which is also what makes the wait cancellable.
func acquireLease(path string) (*lease, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLeaseHeld
		}
		return nil, err
	}
	return &lease{f: f}, nil
}

// release drops the lease. The kernel does this too when the process dies — by any
// means, SIGKILL included — which is the whole reason the accepting right is a
// kernel object rather than a record anyone has to maintain.
func (l *lease) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
