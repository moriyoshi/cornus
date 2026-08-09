//go:build linux

package wire

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"
	"golang.org/x/sys/unix"
)

// The mount-mode A/B against a REAL kernel 9p client, which is the only place
// some of this is visible: the in-process harness drives a Go p9.Client, and the
// kernel's v9fs differs in the ways that decide this comparison — it issues reads
// at its own granularity, pipelines them, and (for the block mount) runs with
// cache=mmap, so reads go through the page cache with readahead rather than one
// synchronous request per read(2).
//
// Gated behind CORNUS_MOUNT_BENCH=1 and CAP_SYS_ADMIN, like the neighbouring
// kernel-9p reproduction. It exists so the docker E2E benchmark
// (e2e/benchmarks/bench-mount-modes.star) is not the only way to see a change on
// a real mount — that one needs the whole privileged runner image, where this is
// one `go test -c` plus a privileged container:
//
//	go test -c -o /tmp/wire.test ./pkg/wire/
//	docker run --rm --privileged -v /tmp:/w -e CORNUS_MOUNT_BENCH=1 \
//	  debian:trixie-slim /w/wire.test -test.run TestKernelMountModes -test.v

// mountModeCase is one leg of the comparison: how the server serves the mount,
// and the kernel cache mode production pairs with it.
type mountModeCase struct {
	name      string
	block     bool
	writeback bool // cache=mmap, as the :async block mount is mounted
	yamux     bool // carry the server<->caller hop on a yamux stream, as production does
}

var mountModeCases = []mountModeCase{
	// What production runs today: the raw splice with cache=none, the block
	// protocol with cache=mmap — both over a yamux stream.
	{"raw9p", false, false, true},
	{"block", true, true, true},
	// The same protocols under the OTHER cache mode, and without the mux, so a
	// difference can be attributed to one variable rather than to all three.
	{"raw9p-mmap", false, true, true},
	{"block-cachenone", true, false, true},
	{"raw9p-nomux", false, false, false},
	{"block-nomux", true, true, false},
}

// muxPair returns the two ends of one yamux stream over a socket pair — the
// server<->caller hop as production carries it (a tagged stream inside the
// session's WebSocket), with no emulated link in the way.
func muxPair(tb testing.TB) (net.Conn, net.Conn) {
	tb.Helper()
	a, b := sockPair(tb)
	srv, err := yamux.Client(a, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	cli, err := yamux.Server(b, yamuxConfig())
	if err != nil {
		tb.Fatal(err)
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := cli.Accept()
		ch <- res{c, err}
	}()
	s, err := srv.Open()
	if err != nil {
		tb.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		tb.Fatal(r.err)
	}
	tb.Cleanup(func() { srv.Close(); cli.Close() })
	return s, r.c
}

// serveKernelMount sets up one mount mode on a unix socket and mounts it at a
// fresh directory, returning the mountpoint.
func serveKernelMount(t *testing.T, c mountModeCase, exportDir string) string {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "cornus-mountbench-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sockDir, 0o711); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sockDir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			// The server<->caller hop, as production carries it: a yamux stream (the
			// tagged backing inside the session's WebSocket), or a bare socket pair
			// for the leg that isolates the mux.
			var rs1, rs2 net.Conn
			if c.yamux {
				rs1, rs2 = muxPair(t)
			} else {
				rs1, rs2 = sockPair(t)
			}
			go func() {
				if c.block {
					go ServeBlockServer(rs2, exportDir, defaultBlockChunk)
					ServeBlockProxy(conn, rs1, nil, "m")
					return
				}
				att, aerr := writableConfinedAttacher(exportDir)
				if aerr != nil {
					return
				}
				go func() { _ = p9.NewServer(att).Handle(rs2, rs2) }()
				pipe(conn, rs1)
			}()
		}
	}()

	mnt := filepath.Join(sockDir, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := "trans=unix,version=9p2000.L,msize=1048576"
	if c.writeback {
		opts += ",cache=mmap"
	}
	if err := unix.Mount(sock, mnt, "9p", 0, opts); err != nil {
		t.Fatalf("mount 9p (%s): %v", c.name, err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(mnt, 0); err != nil {
			_ = unix.Unmount(mnt, unix.MNT_DETACH)
		}
		l.Close()
		os.RemoveAll(sockDir)
	})
	return mnt
}

// TestKernelMountModes measures sequential write, sequential read and small
// fsync'd writes over a real kernel mount, for each mode. It reports rather than
// asserts: the point is the RATIO between the legs in one run, which no fixed
// threshold could stand in for across machines.
func TestKernelMountModes(t *testing.T) {
	if os.Getenv("CORNUS_MOUNT_BENCH") == "" {
		t.Skip("set CORNUS_MOUNT_BENCH=1 (needs CAP_SYS_ADMIN) to run the kernel mount benchmark")
	}
	if os.Geteuid() != 0 {
		t.Skip("kernel 9p mount needs root / CAP_SYS_ADMIN")
	}
	const total = 64 << 20
	buf := make([]byte, 1<<20)

	for _, c := range mountModeCases {
		t.Run(c.name, func(t *testing.T) {
			export := t.TempDir()
			mnt := serveKernelMount(t, c, export)

			// Sequential write, fsync'd — the dd conv=fsync shape.
			path := filepath.Join(mnt, "seq")
			f, err := os.Create(path)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			t0 := time.Now()
			for off := 0; off < total; off += len(buf) {
				if _, err := f.Write(buf); err != nil {
					t.Fatalf("write at %d: %v", off, err)
				}
			}
			if err := f.Sync(); err != nil {
				t.Fatalf("sync: %v", err)
			}
			wdt := time.Since(t0)
			f.Close()

			// Sequential read of a file written on the EXPORT side, so nothing about it
			// is in this mount's page cache. Reading back the file we just wrote would
			// measure the page cache under cache=mmap and the wire under cache=none —
			// two different things reported in one column.
			cold := make([]byte, total)
			for i := range cold {
				cold[i] = byte(i)
			}
			if err := os.WriteFile(filepath.Join(export, "cold"), cold, 0o644); err != nil {
				t.Fatal(err)
			}
			rf, err := os.Open(filepath.Join(mnt, "cold"))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t1 := time.Now()
			got := 0
			for {
				n, err := rf.Read(buf)
				got += n
				if n == 0 || err != nil {
					break
				}
			}
			rdt := time.Since(t1)
			rf.Close()
			if got != total {
				t.Fatalf("read back %d bytes, want %d", got, total)
			}

			// 100 small fsync'd writes: per-op round trips.
			const ops = 100
			t2 := time.Now()
			for i := 0; i < ops; i++ {
				wf, err := os.OpenFile(filepath.Join(mnt, "w"), os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatalf("open w: %v", err)
				}
				if _, err := wf.Write(buf[:4096]); err != nil {
					t.Fatalf("small write: %v", err)
				}
				if err := wf.Sync(); err != nil {
					t.Fatalf("small sync: %v", err)
				}
				wf.Close()
			}
			sdt := time.Since(t2)

			mbps := func(d time.Duration) string {
				return fmt.Sprintf("%.1f MB/s", float64(total)/(1<<20)/d.Seconds())
			}
			t.Logf("%-18s seq-write %-12s seq-read %-12s fsync %.3f ms/op",
				c.name, mbps(wdt), mbps(rdt), float64(sdt.Microseconds())/1000/ops)
		})
	}
}
