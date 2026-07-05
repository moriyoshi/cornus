//go:build linux

package wire

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hashicorp/yamux"
)

// TestNinePBackingKernelMount mounts kernel-9p over the lazy-bind backing
// transport (server unix socket -> 'L' yamux stream -> caller confined p9 server)
// and reads a file, proving the build server can serve a lazy context on demand
// from the caller across the multiplexed link. Needs root + the 9p kernel module
// (privileged host); skipped otherwise.
func TestNinePBackingKernelMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root + kernel 9p (privileged host)")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("9P-OVER-WS"), 0o644); err != nil {
		t.Fatal(err)
	}

	// yamux pair over a TCP loopback (stands in for the WebSocket).
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	srvCh := make(chan *yamux.Session, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			srvCh <- nil
			return
		}
		s, _ := yamux.Server(conn, yamuxConfig())
		srvCh <- s
	}()
	cConn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	callerSess, err := yamux.Client(cConn, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	serverSess := <-srvCh
	if serverSess == nil {
		t.Fatal("server session not established")
	}

	// Caller serves the confined 9P export on demand.
	go Serve9PBacking(callerSess, map[string]string{"data": dir}, nil, nil, nil)

	// Server side: a unix socket the kernel-9p client mounts.
	sock, cleanup, err := Backing9PSocket(serverSess, "data")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	mp := t.TempDir()
	if out, err := exec.Command("mount", "-t", "9p", sock, mp,
		"-o", "trans=unix,version=9p2000.L,msize=1048576,ro").CombinedOutput(); err != nil {
		t.Fatalf("mount 9p: %v: %s", err, out)
	}
	defer exec.Command("umount", mp).Run()

	got, err := os.ReadFile(filepath.Join(mp, "marker"))
	if err != nil {
		t.Fatalf("read over 9p backing: %v", err)
	}
	if string(got) != "9P-OVER-WS" {
		t.Errorf("content = %q, want 9P-OVER-WS", got)
	}
}
