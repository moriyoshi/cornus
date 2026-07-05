//go:build linux

package wire

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// TestRepro9PWritableSharedDir reproduces the "shared bind-mounted volume" bug:
// two independent writable confined 9P exports of the SAME host directory, each
// kernel-9P-mounted (as the deploy caretaker does), with one mount running a
// sed -i-style rename loop over a file while the other mount concurrently
// reads/writes the same tree — mirroring the frontend (next dev) + teaser
// (sed loop) scenario.
//
// Gated behind CORNUS_REPRO_9P=1 and requires CAP_SYS_ADMIN (run inside a
// privileged container as root). It is a diagnostic reproduction, not part of
// the normal go test gate.
func TestRepro9PWritableSharedDir(t *testing.T) {
	if os.Getenv("CORNUS_REPRO_9P") == "" {
		t.Skip("set CORNUS_REPRO_9P=1 (needs CAP_SYS_ADMIN) to run the kernel-9p reproduction")
	}

	host := t.TempDir()
	appDir := filepath.Join(host, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(appDir, "page.tsx")
	if err := os.WriteFile(page, []byte("<>{(0)}</>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// serve starts a writable confined 9P export of host on a fresh unix socket
	// and kernel-mounts it at a fresh mountpoint; returns the mountpoint.
	serve := func(tag string) string {
		sockDir := t.TempDir()
		sock := filepath.Join(sockDir, "9p.sock")
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("[%s] listen: %v", tag, err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				att, err := writableConfinedAttacher(host)
				if err != nil {
					t.Errorf("[%s] attacher: %v", tag, err)
					c.Close()
					continue
				}
				go func() { _ = p9.NewServer(att).Handle(c, c) }()
			}
		}()
		mnt := t.TempDir()
		opts := "trans=unix,version=9p2000.L,msize=1048576"
		if err := unix.Mount(sock, mnt, "9p", 0, opts); err != nil {
			t.Fatalf("[%s] kernel 9p mount: %v", tag, err)
		}
		t.Cleanup(func() {
			if err := unix.Unmount(mnt, 0); err != nil {
				_ = unix.Unmount(mnt, unix.MNT_DETACH)
			}
			l.Close()
		})
		return mnt
	}

	teaserMnt := serve("teaser")
	frontendMnt := serve("frontend")

	const iters = 400
	var wg sync.WaitGroup
	var sedErrs, readErrs, writeErrs int64

	// teaser: sed -i loop — write a temp file, rename it over page.tsx.
	wg.Add(1)
	go func() {
		defer wg.Done()
		dir := filepath.Join(teaserMnt, "app")
		for i := 0; i < iters; i++ {
			tmp := filepath.Join(dir, fmt.Sprintf("sed%06d", i))
			if err := os.WriteFile(tmp, []byte(fmt.Sprintf("<>{(%d)}</>\n", i)), 0o644); err != nil {
				atomic.AddInt64(&sedErrs, 1)
				t.Logf("teaser write temp: %v", err)
				continue
			}
			if err := os.Rename(tmp, filepath.Join(dir, "page.tsx")); err != nil {
				atomic.AddInt64(&sedErrs, 1)
				t.Logf("teaser rename (sed -i): %v", err)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// frontend: next-dev-style — repeatedly read page.tsx (watcher) and write a
	// lockfile into .next (create+write) on the shared tree.
	wg.Add(1)
	go func() {
		defer wg.Done()
		p := filepath.Join(frontendMnt, "app", "page.tsx")
		nextDir := filepath.Join(frontendMnt, ".next")
		_ = os.MkdirAll(nextDir, 0o755)
		lock := filepath.Join(nextDir, "cache.lock")
		for i := 0; i < iters; i++ {
			if _, err := os.ReadFile(p); err != nil {
				atomic.AddInt64(&readErrs, 1)
				t.Logf("frontend read page.tsx: %v", err)
			}
			f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				atomic.AddInt64(&writeErrs, 1)
				t.Logf("frontend open lockfile: %v", err)
			} else {
				if _, err := f.WriteAt([]byte("lock"), 0); err != nil {
					atomic.AddInt64(&writeErrs, 1)
					t.Logf("frontend write lockfile: %v", err)
				}
				f.Close()
			}
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
	t.Logf("results: sedErrs=%d readErrs=%d writeErrs=%d", sedErrs, readErrs, writeErrs)
	if sedErrs+readErrs+writeErrs > 0 {
		t.Fatalf("REPRODUCED: sedErrs=%d readErrs=%d writeErrs=%d", sedErrs, readErrs, writeErrs)
	}

	// Turbopack (next dev) opens .next/dev/lock and flock()s it. Over the real
	// kernel 9P client this is a Tlock; before the export delegated Lock it failed
	// with ENOSYS ("Function not implemented"). Reproduce that exact operation.
	lock := filepath.Join(frontendMnt, ".next", "dev", "lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lockfile: %v", err)
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("flock lockfile (the turbopack failure): %v", err)
	}
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("flock unlock: %v", err)
	}
	t.Logf("flock over kernel-9p: OK")
}
