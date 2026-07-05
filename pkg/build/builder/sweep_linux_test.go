//go:build linux

package builder

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkTempDir creates dir <root>/<name> and sets its mtime to now-age.
func mkTempDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", p, err)
	return false
}

func TestSweepStaleTempDirs(t *testing.T) {
	root := t.TempDir()

	// Stale dirs of every swept prefix -> should be removed.
	oldAge := sweepStaleWindow + time.Hour
	staleP9 := mkTempDir(t, root, "cornus-9p-abc123", oldAge)
	staleBack := mkTempDir(t, root, "cornus-9pback-def456", oldAge)
	staleSSH := mkTempDir(t, root, "cornus-ssh-ghi789", oldAge)

	// Fresh dir (within window) -> a live peer's, must be kept.
	freshP9 := mkTempDir(t, root, "cornus-9p-fresh", time.Minute)

	// Unrelated dir -> never touched.
	other := mkTempDir(t, root, "some-other-dir", oldAge)

	// A file (not a dir) matching a prefix -> ignored (we only sweep dirs).
	otherFile := filepath.Join(root, "cornus-9p-file")
	if err := os.WriteFile(otherFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	stale := time.Now().Add(-oldAge)
	_ = os.Chtimes(otherFile, stale, stale)

	sweepStaleTempDirs(root)

	if exists(t, staleP9) {
		t.Errorf("stale cornus-9p dir not removed: %s", staleP9)
	}
	if exists(t, staleBack) {
		t.Errorf("stale cornus-9pback dir not removed: %s", staleBack)
	}
	if exists(t, staleSSH) {
		t.Errorf("stale cornus-ssh dir not removed: %s", staleSSH)
	}
	if !exists(t, freshP9) {
		t.Errorf("fresh (live-peer) dir was removed: %s", freshP9)
	}
	if !exists(t, other) {
		t.Errorf("unrelated dir was removed: %s", other)
	}
	if !exists(t, otherFile) {
		t.Errorf("prefix-matching file was removed: %s", otherFile)
	}
}

// TestSweepStaleTempDirsBoundary pins the exact mtime-age boundary using the
// injectable clock: a dir exactly at the window is kept, just past it is reaped.
func TestSweepStaleTempDirsBoundary(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// Just inside the window (kept). One second of slack absorbs the filesystem's
	// mtime truncation so the assertion is not flaky.
	inWindow := mkTempDir(t, root, "cornus-ssh-in", 0)
	mt := now.Add(-sweepStaleWindow + time.Second)
	_ = os.Chtimes(inWindow, mt, mt)

	pastWindow := mkTempDir(t, root, "cornus-ssh-past", 0)
	mt2 := now.Add(-sweepStaleWindow - time.Second)
	_ = os.Chtimes(pastWindow, mt2, mt2)

	sweepStaleTempDirsAt(root, now)

	if !exists(t, inWindow) {
		t.Errorf("dir just inside the staleness window was removed: %s", inWindow)
	}
	if exists(t, pastWindow) {
		t.Errorf("dir past the staleness window was not removed: %s", pastWindow)
	}
}

// TestSweepStaleTempDirsKeepsLiveSocket is the regression for the long-build
// hazard: a backing dir whose mtime is well past the staleness window but whose
// socket still has a live listener (an in-flight build > sweepStaleWindow) must
// NOT be reaped, while a stale dir whose socket has no listener (a dead process's
// leftover) must be. mtime alone cannot tell them apart; the socket probe can.
func TestSweepStaleTempDirsKeepsLiveSocket(t *testing.T) {
	// Use a short root so the unix socket path stays under the ~108-byte sun_path
	// limit (t.TempDir() paths embed the long test name).
	root, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	oldAge := sweepStaleWindow + time.Hour

	// Live: a stale-mtime dir whose socket still has an active listener.
	liveDir := mkTempDir(t, root, "cornus-ssh-live", oldAge)
	ln, err := net.Listen("unix", filepath.Join(liveDir, "agent.sock"))
	if err != nil {
		t.Fatalf("listen live: %v", err)
	}
	defer ln.Close()
	// Binding the socket bumped the dir mtime; force it back past the window.
	mt := time.Now().Add(-oldAge)
	if err := os.Chtimes(liveDir, mt, mt); err != nil {
		t.Fatalf("chtimes live: %v", err)
	}

	// Dead: a stale-mtime dir with a leftover socket file but no listener.
	deadDir := mkTempDir(t, root, "cornus-9p-dead", oldAge)
	addr, err := net.ResolveUnixAddr("unix", filepath.Join(deadDir, "ctx.sock"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	dl, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	// Keep the socket file on disk after closing so it looks like a crashed
	// process's remains: bound but with nothing listening.
	dl.SetUnlinkOnClose(false)
	dl.Close()
	if err := os.Chtimes(deadDir, mt, mt); err != nil {
		t.Fatalf("chtimes dead: %v", err)
	}

	sweepStaleTempDirs(root)

	if !exists(t, liveDir) {
		t.Errorf("stale dir with a live socket was removed: %s", liveDir)
	}
	if exists(t, deadDir) {
		t.Errorf("stale dir with a dead (unlistened) socket was not removed: %s", deadDir)
	}
}

func TestSweepStaleTempDirsMissingRoot(t *testing.T) {
	// Best-effort: an unreadable/missing root must not panic.
	sweepStaleTempDirs(filepath.Join(t.TempDir(), "does-not-exist"))
}
