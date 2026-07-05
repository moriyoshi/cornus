package wire

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

func writableClient(t *testing.T, dir string) *p9.Client {
	t.Helper()
	attacher, err := writableConfinedAttacher(dir)
	if err != nil {
		t.Fatalf("writableConfinedAttacher: %v", err)
	}
	return rawClient(t, attacher)
}

// TestWritableConfinedWritesStayInRoot proves the writable export actually writes
// (create/write/mkdir) into the exported directory.
func TestWritableConfinedWritesStayInRoot(t *testing.T) {
	dir := t.TempDir()
	cl := writableClient(t, dir)

	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err := root.Create("out.txt", p9.WriteOnly, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteAt([]byte("hello-rw"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	if got, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(got) != "hello-rw" {
		t.Fatalf("on disk = %q (%v), want hello-rw", got, err)
	}

	root2, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root2.Mkdir("sub", 0o755, 0, 0); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "sub")); err != nil || !fi.IsDir() {
		t.Fatalf("subdir not created: %v", err)
	}
}

// TestWritableConfinedHonorsLock proves the writable export delegates advisory
// file locking to localfs instead of inheriting NoopFile's NotLockable stub,
// which returned ENOSYS. A writable client-local bind mount must honor locks:
// workloads that acquire a POSIX/flock lock on a file in the mount (e.g.
// Next.js/Turbopack's .next/dev/lock) crash on the first lock ("Function not
// implemented") without this.
//
// This is exercised white-box against the server-side File rather than through
// the p9 Go client: that client's clientFile.Lock omits the fid on the tlock it
// sends, so it cannot drive a lock at all (the real client is the Linux kernel
// 9P client, which sets the fid). Calling the export's Lock directly reproduces
// the exact path a Tlock takes on the server.
func TestWritableConfinedHonorsLock(t *testing.T) {
	dir := t.TempDir()
	attacher, err := writableConfinedAttacher(dir)
	if err != nil {
		t.Fatalf("writableConfinedAttacher: %v", err)
	}
	root, err := attacher.Attach()
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, _, _, err := root.Create("lock", p9.ReadWrite, 0o644, 0, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	// Acquire then release a write lock: both must succeed, not fail with ENOSYS
	// (which NoopFile's NotLockable stub returned before Lock was delegated).
	st, err := f.Lock(1, p9.WriteLock, 0, 0, 0, "test")
	if err != nil {
		t.Fatalf("lock (acquire): %v", err)
	}
	if st != p9.LockStatusOK {
		t.Fatalf("lock (acquire) status = %v, want OK", st)
	}
	if st, err := f.Lock(1, p9.Unlock, 0, 0, 0, "test"); err != nil || st != p9.LockStatusOK {
		t.Fatalf("lock (release) status=%v err=%v, want OK/nil", st, err)
	}
}

// TestWritableConfinedAppendWrites proves a file opened with O_APPEND can be
// written: localfs opens the host fd append-mode and then serves writes via
// pwrite (WriteAt), which Go refuses on an O_APPEND fd, so without stripping
// O_APPEND every append write fails with EIO. A 9P client positions each append
// write at end-of-file, which the test mirrors by writing at increasing offsets.
func TestWritableConfinedAppendWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cl := writableClient(t, dir)

	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	_, f, err := root.Walk([]string{"app.log"})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	defer f.Close()
	// Open append-mode (O_WRONLY|O_APPEND), as a log writer does.
	if _, _, err := f.Open(p9.WriteOnly | p9.OpenFlags(unix.O_APPEND)); err != nil {
		t.Fatalf("open append: %v", err)
	}
	// A 9P client sends each O_APPEND write at the current EOF offset.
	if _, err := f.WriteAt([]byte("second\n"), int64(len("first\n"))); err != nil {
		t.Fatalf("append write (would be EIO without the O_APPEND strip): %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "app.log")); err != nil || string(got) != "first\nsecond\n" {
		t.Fatalf("on disk = %q (%v), want \"first\\nsecond\\n\"", got, err)
	}
}

// TestWritableConfinedRejectsEscape proves the write path keeps the same jail as
// the read-only export: no ".." traversal and no writing through a symlink that
// escapes the export root.
func TestWritableConfinedRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	cl := writableClient(t, dir)

	if root, err := cl.Attach(""); err == nil {
		if _, _, _, err := root.Create("..", p9.WriteOnly, 0o644, 0, 0); err == nil {
			t.Error(`Create("..") should be denied`)
		}
	}
	if root, err := cl.Attach(""); err == nil {
		if _, _, err := root.Walk([]string{".."}); err == nil {
			t.Error("Walk(..) should be denied")
		}
	}

	// A symlink inside the export pointing outside it: reaching it (as a link) is
	// allowed, but opening it to write through must be denied.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "esc")); err != nil {
		t.Fatal(err)
	}
	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	_, lf, err := root.Walk([]string{"esc"})
	if err != nil {
		t.Fatalf("walk to symlink: %v", err)
	}
	if _, _, err := lf.Open(p9.WriteOnly); err == nil {
		t.Error("opening an escaping symlink for write should be denied")
	}
}

// TestWritableConfinedSetAttrRejectsEscape proves SetAttr(size) cannot truncate a
// file outside the export root through a symlink fid. localfs.SetAttr uses
// os.Truncate, which follows a final-component symlink, so without confinement a
// hostile client could zero out arbitrary files reachable via an in-root symlink.
func TestWritableConfinedSetAttrRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	cl := writableClient(t, dir)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("keepme"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "esc")); err != nil {
		t.Fatal(err)
	}

	root, err := cl.Attach("")
	if err != nil {
		t.Fatal(err)
	}
	_, lf, err := root.Walk([]string{"esc"})
	if err != nil {
		t.Fatalf("walk to symlink: %v", err)
	}
	err = lf.SetAttr(p9.SetAttrMask{Size: true}, p9.SetAttr{Size: 0})
	if err == nil {
		t.Error("SetAttr(size=0) through an escaping symlink should be denied")
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != "keepme" {
		t.Fatalf("outside file = %q (%v), want it untouched (keepme)", got, err)
	}
}
