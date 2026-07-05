package deploywire

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/yamux"

	"cornus/pkg/activity"
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

// mountManagerFixture is the shared setup of the tests above: a faked kernel
// mount and an idle yamux session Prepare never actually dials.
func mountManagerFixture(t *testing.T, onMount func(mp string)) (*yamux.Session, DeployAttachSpec) {
	t.Helper()
	orig := mountFn
	mountFn = func(sock, mp string, readOnly, writeback bool) error {
		if onMount != nil {
			onMount(mp)
		}
		return nil
	}
	t.Cleanup(func() { mountFn = orig })

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	server, err := yamux.Server(c1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	client, err := yamux.Client(c2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	_ = client

	spec := DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/local/src", Target: "/data"}},
		},
		LocalMounts: []LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	return server, spec
}

// A kernel mount outlives the process that made it and leaves nothing behind
// saying it was cornus's. The record is the only trace, so it must exist BEFORE
// the syscall — recording afterwards leaves exactly the window this is for.
func TestMountManagerRecordsWriteAhead(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	// Assert the ordering from inside the mount itself: at the moment the
	// syscall runs, the record must already be on disk.
	var recordedAtMountTime bool
	sess, spec := mountManagerFixture(t, func(mp string) {
		open, _ := activity.Unfinished(dir)
		for _, e := range open {
			if e.Kind == activity.KindMount9P && e.Target == mp {
				recordedAtMountTime = true
			}
		}
	})

	mm := NewMountManager(t.TempDir())
	mm.SetRecorder(rec)
	if _, err := mm.Prepare(sess, spec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !recordedAtMountTime {
		t.Fatal("the mount was made before its record hit disk; a crash in that window is untraceable")
	}

	open, _ := activity.Unfinished(dir)
	if len(open) != 1 || open[0].Attrs["deployment"] != "web" || open[0].Attrs["mount"] != "m0" {
		t.Fatalf("unfinished = %+v, want the mount with its deployment and name", open)
	}

	// Teardown closes it only after the unmount, so the record can never claim
	// the mountpoint is gone while it is still there.
	mm.Teardown()
	if left, _ := activity.Unfinished(dir); len(left) != 0 {
		t.Errorf("still unfinished after Teardown: %+v", left)
	}
}

// A mount that fails to attach never existed, so its record must be closed
// rather than left to be "recovered" by every future startup.
func TestMountManagerRecordsAFailedMount(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	sess, spec := mountManagerFixture(t, nil)
	orig := mountFn
	mountFn = func(sock, mp string, readOnly, writeback bool) error { return errors.New("EPERM") }
	defer func() { mountFn = orig }()

	mm := NewMountManager(t.TempDir())
	mm.SetRecorder(rec)
	if _, err := mm.Prepare(sess, spec); err == nil {
		t.Fatal("expected Prepare to fail")
	}
	mm.Teardown()

	if left, _ := activity.Unfinished(dir); len(left) != 0 {
		t.Errorf("a mount that never attached left an open record: %+v", left)
	}
	events, _ := activity.Read(dir)
	end := events[len(events)-1]
	if end.Status != activity.StatusError || !strings.Contains(end.Err, "EPERM") {
		t.Errorf("end = %+v, want the mount failure recorded", end)
	}
}

// Teardown must unmount BEFORE closing the record. The other order leaves a
// window in which the log claims the mountpoint is gone while it still exists —
// and a process dying in that window strands a mount with no open record
// pointing at it, which is the failure the recorder exists to prevent.
func TestMountManagerUnmountsBeforeClosingTheRecord(t *testing.T) {
	dir := t.TempDir()
	rec, err := activity.Open(dir, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	sess, spec := mountManagerFixture(t, nil)
	mm := NewMountManager(t.TempDir())
	mm.SetRecorder(rec)
	if _, err := mm.Prepare(sess, spec); err != nil {
		t.Fatal(err)
	}

	// At the moment of the unmount the record must still be open.
	var openAtUnmount bool
	origUnmount := unmountFn
	unmountFn = func(mp string) {
		open, _ := activity.Unfinished(dir)
		for _, e := range open {
			if e.Target == mp {
				openAtUnmount = true
			}
		}
	}
	defer func() { unmountFn = origUnmount }()

	mm.Teardown()
	if !openAtUnmount {
		t.Fatal("the record was closed before the unmount; the log would claim the mount was gone while it still existed")
	}
}
