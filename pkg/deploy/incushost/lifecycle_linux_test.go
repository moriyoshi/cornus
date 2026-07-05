//go:build linux

package incushost

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
)

// TestApplyEnforcesTheHostPrivilegePolicyBeforeTouchingTheDaemon pins the
// security gate every host backend shares: cornus's API is unauthenticated by
// default, so a spec asking for a privileged container or an unlisted host bind
// must be refused, and refused BEFORE anything is created (a partially applied
// deployment would leave the earlier replicas running).
func TestApplyEnforcesTheHostPrivilegePolicyBeforeTouchingTheDaemon(t *testing.T) {
	f := newFakeConn()
	// The zero policy is default-deny, which is what a server-side backend gets.
	b := &Backend{conn: f, project: "default", execs: newExecRegistry()}

	_, err := b.Apply(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1", Privileged: true,
	})
	if err == nil || !strings.Contains(err.Error(), "privileged") {
		t.Fatalf("privileged spec under the default policy: got %v", err)
	}
	if len(f.insts) != 0 {
		t.Fatalf("a denied spec must not create instances, got %v", f.insts)
	}

	_, err = b.Apply(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1",
		Mounts: []api.Mount{{Source: "/", Target: "/host"}},
	})
	if err == nil || !strings.Contains(err.Error(), "bind source") {
		t.Fatalf("host bind under the default policy: got %v", err)
	}
	if len(f.insts) != 0 {
		t.Fatalf("a denied spec must not create instances, got %v", f.insts)
	}

	// The permissive policy (local CLI) lets the same spec through.
	permissive := &Backend{conn: f, policy: hostpolicy.Permissive(), project: "default", execs: newExecRegistry()}
	if _, err := permissive.Apply(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1", Privileged: true,
	}); err != nil {
		t.Fatalf("permissive policy should allow a privileged spec: %v", err)
	}
	if f.insts["cornus-web-0"].Config["security.privileged"] != "true" {
		t.Fatalf("privileged flag not applied: %v", f.insts["cornus-web-0"].Config)
	}
}

// TestDaemonListingFailuresSurfaceRatherThanLookingEmpty pins the most dangerous
// possible mishandling in this backend: if a failed instance listing were
// treated as "no instances", Status would report a healthy deployment as gone,
// Delete would silently succeed without deleting anything, and Apply would
// recreate on top of live instances. Every path that lists must propagate.
func TestDaemonListingFailuresSurfaceRatherThanLookingEmpty(t *testing.T) {
	boom := errors.New("incusd: connection refused")
	f := newFakeConn()
	f.listErr = boom
	b := testBackend(f)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Status", func() error { _, err := b.Status(ctx, "web"); return err }},
		{"List", func() error { _, err := b.List(ctx); return err }},
		{"Delete", func() error { return b.Delete(ctx, "web") }},
		{"Start", func() error { return b.Start(ctx, "web") }},
		{"Stop", func() error { return b.Stop(ctx, "web") }},
		{"Restart", func() error { return b.Restart(ctx, "web") }},
		{"Apply", func() error {
			_, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"})
			return err
		}},
		{"Logs", func() error { return b.Logs(ctx, "web", api.LogOptions{}, new(bytes.Buffer)) }},
		{"ExecCreate", func() error { _, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh"}}); return err }},
		{"StatPath", func() error { _, err := b.StatPath(ctx, "web", "/etc/hi"); return err }},
		{"SampleMetrics", func() error { _, err := b.SampleMetrics(ctx, "web", 0); return err }},
	} {
		err := tc.call()
		if !errors.Is(err, boom) {
			t.Errorf("%s: want the daemon error, got %v", tc.name, err)
		}
		if errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("%s: a daemon failure must not be reported as not-found", tc.name)
		}
	}
}

// TestApplyStopsInstancesBeforeDeletingThem pins the recreate ordering: Incus
// refuses to delete a running instance, so Apply's teardown must stop first.
// (The stop is best-effort — an already-stopped instance errors, and that error
// is deliberately ignored.)
func TestApplyStopsInstancesBeforeDeletingThem(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	rec := &recordingConn{fakeConn: f}
	b.conn = rec

	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 1}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	rec.calls = nil
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	want := []string{"stop cornus-web-0", "delete cornus-web-0"}
	if len(rec.calls) < 2 || rec.calls[0] != want[0] || rec.calls[1] != want[1] {
		t.Fatalf("teardown order = %v, want %v first", rec.calls, want)
	}
}

// recordingConn logs the lifecycle calls made through it, so ordering contracts
// can be asserted.
type recordingConn struct {
	*fakeConn
	calls []string
}

func (c *recordingConn) SetInstanceState(name, action string, force bool, timeout int) error {
	c.calls = append(c.calls, action+" "+name)
	return c.fakeConn.SetInstanceState(name, action, force, timeout)
}

func (c *recordingConn) DeleteInstance(name string) error {
	c.calls = append(c.calls, "delete "+name)
	return c.fakeConn.DeleteInstance(name)
}

// TestCopyToChownsToRootUnlessArchiveIsRequested pins docker cp's ownership
// rule: by default extracted entries land owned by root, and only `--archive`
// preserves the uid/gid recorded in the tar.
func TestCopyToChownsToRootUnlessArchiveIsRequested(t *testing.T) {
	hdr := &tar.Header{Uid: 1000, Gid: 2000}
	if uid, gid := ownerFor(hdr, false); uid != 0 || gid != 0 {
		t.Errorf("default cp owner = %d:%d, want 0:0", uid, gid)
	}
	if uid, gid := ownerFor(hdr, true); uid != 1000 || gid != 2000 {
		t.Errorf("--archive owner = %d:%d, want 1000:2000", uid, gid)
	}
}

// TestCopyToPreservesSymlinksAsSymlinks pins that a symlink in the archive is
// created as a symlink inside the instance (with its target as the content),
// not materialised as a regular file holding the target's path.
func TestCopyToPreservesSymlinksAsSymlinks(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/real/target", Mode: 0o777,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	tw.Close()

	if err := b.CopyTo(context.Background(), "web", "/dest", &buf, api.CopyToOptions{}); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	n := f.files[id]["/dest/link"]
	if n == nil || n.typ != "symlink" || string(n.content) != "/real/target" {
		t.Fatalf("/dest/link = %+v, want a symlink to /real/target", n)
	}
}

// TestCopyFromPacksSymlinksWithTheirTarget pins the reverse direction: a symlink
// inside the instance packs as a tar symlink entry with an empty body, so the
// archive round-trips instead of inlining the link target as file content.
func TestCopyFromPacksSymlinksWithTheirTarget(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/d", "directory", 0o755, nil, "link")
	f.seedFile(id, "/d/link", "symlink", 0o777, []byte("/real/target"))

	var buf bytes.Buffer
	if _, err := b.CopyFrom(context.Background(), "web", "/d", &buf); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	tr := tar.NewReader(&buf)
	var seen bool
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name != "d/link" {
			continue
		}
		seen = true
		if hdr.Typeflag != tar.TypeSymlink {
			t.Errorf("typeflag = %q, want symlink", hdr.Typeflag)
		}
		if hdr.Linkname != "/real/target" {
			t.Errorf("linkname = %q", hdr.Linkname)
		}
		if hdr.Size != 0 {
			t.Errorf("a symlink entry must have no body, size = %d", hdr.Size)
		}
	}
	if !seen {
		t.Fatal("symlink entry missing from the archive")
	}
}

// TestStatPathReportsDirectoriesAsDirectories pins the mode translation the cp
// contract depends on: the type bit has to come from Incus's file-response type,
// since the permission bits alone cannot say what a path is.
func TestStatPathReportsDirectoriesAsDirectories(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/d", "directory", 0o755, nil)
	f.seedFile(id, "/l", "symlink", 0o777, []byte("/d"))

	st, err := b.StatPath(context.Background(), "web", "/d")
	if err != nil {
		t.Fatalf("StatPath: %v", err)
	}
	if os.FileMode(st.Mode)&os.ModeDir == 0 {
		t.Fatalf("directory mode = %v, want the directory bit set", os.FileMode(st.Mode))
	}
	if os.FileMode(st.Mode).Perm() != 0o755 {
		t.Errorf("directory permissions = %v, want 0755", os.FileMode(st.Mode).Perm())
	}
	if st.Size != 0 {
		t.Errorf("a directory has no measured size, got %d", st.Size)
	}

	lst, err := b.StatPath(context.Background(), "web", "/l")
	if err != nil {
		t.Fatalf("StatPath(symlink): %v", err)
	}
	if os.FileMode(lst.Mode)&os.ModeSymlink == 0 {
		t.Errorf("symlink mode = %v, want the symlink bit set", os.FileMode(lst.Mode))
	}
}

// TestCopyAbortsOnADaemonFailureMidTransfer pins that a failure part-way through
// a copy is reported, in both directions. Silently continuing would hand the
// caller a truncated archive (or a partially populated destination) that looks
// like a successful copy.
func TestCopyAbortsOnADaemonFailureMidTransfer(t *testing.T) {
	boom := errors.New("incusd: storage pool offline")

	// Out of the instance: the directory stats fine, a child read fails.
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/d", "directory", 0o755, nil, "a")
	f.seedFile(id, "/d/a", "file", 0o644, []byte("A"))
	f.fileErrs = map[string]error{"/d/a": boom}
	if _, err := b.CopyFrom(context.Background(), "web", "/d", new(bytes.Buffer)); !errors.Is(err, boom) {
		t.Fatalf("CopyFrom: want the daemon error, got %v", err)
	}

	// Into the instance: the write of an entry fails.
	f2 := newFakeConn()
	b2 := testBackend(f2)
	applyOne(t, b2, f2, "web")
	f2.fileErrs = map[string]error{"/dest/f": boom}
	var ar bytes.Buffer
	tw := tar.NewWriter(&ar)
	writeTar(t, tw, "f", tar.TypeReg, 0o644, "X")
	tw.Close()
	if err := b2.CopyTo(context.Background(), "web", "/dest", &ar, api.CopyToOptions{}); !errors.Is(err, boom) {
		t.Fatalf("CopyTo: want the daemon error, got %v", err)
	}
}
