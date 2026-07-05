//go:build linux

package deploywire

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/wire"
)

// TestMountManagerKernelMount exercises the real kernel-9p mount + spec rewrite
// end to end: the caller serves a directory over 9P, MountManager mounts it and
// rewrites the spec's mount source, and we read the file back through the
// mountpoint. Needs root + the 9p kernel module (privileged host); skipped
// otherwise.
func TestMountManagerKernelMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root + kernel 9p (privileged host)")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("DEPLOY-9P"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
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
		s, _ := yamux.Server(conn, cfg)
		srvCh <- s
	}()
	cConn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	callerSess, err := yamux.Client(cConn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	serverSess := <-srvCh
	if serverSess == nil {
		t.Fatal("server session not established")
	}

	go wire.Serve9PBacking(callerSess, map[string]string{"m0": dir}, nil, nil, nil)

	mm := NewMountManager(t.TempDir())
	defer mm.Teardown()
	spec := DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "x",
			Image:  "y",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data"}},
		},
		LocalMounts: []LocalMount{{Index: 0, Name: "m0"}},
	}
	out, err := mm.Prepare(serverSess, spec)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(out.Mounts[0].Source, "marker"))
	if err != nil {
		t.Fatalf("read via kernel-9p mount: %v", err)
	}
	if string(got) != "DEPLOY-9P" {
		t.Errorf("content = %q, want DEPLOY-9P", got)
	}
}
