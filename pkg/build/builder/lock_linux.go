//go:build linux

package builder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDataDir takes an exclusive, non-blocking advisory lock (flock) on the
// engine's data dir. Without it, a second cornus engine on the same dir
// hangs forever: BuildKit opens its boltdb cache with no lock timeout
// (bboltcachestorage.NewStore → bolt.Options{NoSync:true}), so the second
// process blocks indefinitely on the file lock with no output. This turns that
// into an immediate, actionable error.
//
// The returned file must stay open for the engine's lifetime; closing it (or
// process exit) releases the lock.
func lockDataDir(root string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(root, "engine.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("builder: open data-dir lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("builder: data dir %q is in use by another cornus process "+
				"(is `cornus serve` running against the same --data-dir?)", root)
		}
		return nil, fmt.Errorf("builder: lock data dir %q: %w", root, err)
	}
	return f, nil
}
