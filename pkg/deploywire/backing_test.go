package deploywire

import (
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
)

// TestMountManagerRewrite verifies that Prepare rewrites only the client-local
// mounts to server-side mountpoints (forced read-only), leaves other mounts
// untouched, and does not mutate the caller's spec. The kernel mount is faked so
// this runs unprivileged — the "dockerhost stays unaware" assertion lives here.
func TestMountManagerRewrite(t *testing.T) {
	orig := mountFn
	var mounted [][2]string
	mountFn = func(sock, mp string, readOnly, writeback bool) error {
		mounted = append(mounted, [2]string{sock, mp})
		return nil
	}
	defer func() { mountFn = orig }()

	// An idle yamux session: Backing9PSocket only touches it lazily on a socket
	// connection, which Prepare never triggers (the mount is faked).
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	server, err := yamux.Server(c1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := yamux.Client(c2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	base := t.TempDir()
	mm := NewMountManager(base)
	defer mm.Teardown()

	spec := DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "web",
			Image: "img",
			Mounts: []api.Mount{
				{Source: "/host/keep", Target: "/keep"},      // not client-local: passes through
				{Source: "/client/conf", Target: "/etc/app"}, // client-local: served over 9P
			},
		},
		LocalMounts: []LocalMount{{Index: 1, Name: "m1", ReadOnly: true}},
	}

	out, err := mm.Prepare(server, spec)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if out.Mounts[0].Source != "/host/keep" {
		t.Errorf("passthrough mount source = %q, want /host/keep", out.Mounts[0].Source)
	}
	got := out.Mounts[1].Source
	if !strings.HasPrefix(got, base+string(os.PathSeparator)) {
		t.Errorf("local mount source = %q, want a mountpoint under %q", got, base)
	}
	if !out.Mounts[1].ReadOnly {
		t.Error("local mount was not forced read-only")
	}
	if len(mounted) != 1 {
		t.Fatalf("kernel mount calls = %d, want 1", len(mounted))
	}
	if mounted[0][1] != got {
		t.Errorf("mounted at %q, but spec rewritten to %q", mounted[0][1], got)
	}
	if spec.Spec.Mounts[1].Source != "/client/conf" {
		t.Errorf("caller's spec was mutated: mount source = %q", spec.Spec.Mounts[1].Source)
	}
}

// TestMountManagerFileSubpath verifies that a file mount (LocalMount.Subpath set)
// is kernel-9p-mounted at its exported parent directory but rewritten to bind just
// the file within it — the Compose file-based config/secret path.
func TestMountManagerFileSubpath(t *testing.T) {
	orig := mountFn
	var mountedAt string
	mountFn = func(sock, mp string, readOnly, writeback bool) error {
		mountedAt = mp
		return nil
	}
	defer func() { mountFn = orig }()

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	server, err := yamux.Server(c1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := yamux.Client(c2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	base := t.TempDir()
	mm := NewMountManager(base)
	defer mm.Teardown()

	spec := DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "web",
			Image: "img",
			Mounts: []api.Mount{
				{Source: "/client/conf/app.conf", Target: "/app_cfg", ReadOnly: true},
			},
		},
		LocalMounts: []LocalMount{{Index: 0, Name: "m0", ReadOnly: true, Subpath: "app.conf"}},
	}

	out, err := mm.Prepare(server, spec)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The kernel mount is of the parent directory (the mountpoint), but the spec
	// binds the file within it.
	want := mountedAt + string(os.PathSeparator) + "app.conf"
	if out.Mounts[0].Source != want {
		t.Errorf("file mount source = %q, want %q (mountpoint %q + subpath)", out.Mounts[0].Source, want, mountedAt)
	}
	if !strings.HasPrefix(mountedAt, base+string(os.PathSeparator)) {
		t.Errorf("mounted at %q, want a mountpoint under %q", mountedAt, base)
	}
}
