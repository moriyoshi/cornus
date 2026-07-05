//go:build linux

package builder

import (
	"strings"
	"testing"
)

// TestLockDataDirRejectsSecondHolder proves a second engine on the same data dir
// fails fast (instead of hanging on BuildKit's boltdb lock), and that the lock is
// released for reuse. flock conflicts across independent open descriptions, so
// the two acquisitions contend even within one process.
func TestLockDataDirRejectsSecondHolder(t *testing.T) {
	dir := t.TempDir()

	l1, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	if _, err := lockDataDir(dir); err == nil {
		t.Fatal("second lock succeeded; expected an in-use error, not a hang")
	} else if !strings.Contains(err.Error(), "in use") {
		t.Errorf("second lock error = %q, want an 'in use' message", err)
	}

	// Releasing the first lets a new engine acquire it.
	if err := l1.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	l2, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("re-lock after release: %v", err)
	}
	l2.Close()
}
