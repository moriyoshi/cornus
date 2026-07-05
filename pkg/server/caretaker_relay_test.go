package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// TestCaretakerAttachMultiplexesMounts drives the multiplexed relay in-process: a
// caller attaches and serves TWO local dirs over 9P; the test then plays the
// pod's caretaker, opening ONE yamux connection to /.cornus/v1/caretaker/attach and one
// mount stream (session + name) per mount over it, and reads both files back over
// real 9P clients — proving a single connection carries all of a pod's mounts and
// that the per-mount session/name/auth bridge works. No root or kernel 9p needed.
func TestCaretakerAttachMultiplexesMounts(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "marker"), []byte("MOUNT-A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "marker"), []byte("MOUNT-B"), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "web",
			Image: "img",
			Mounts: []api.Mount{
				{Source: "/client/a", Target: "/a", ReadOnly: true},
				{Source: "/client/b", Target: "/b", ReadOnly: true},
			},
		},
		LocalMounts: []deploywire.LocalMount{
			{Index: 0, Name: "m0", ReadOnly: true},
			{Index: 1, Name: "m1", ReadOnly: true},
		},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as,
			map[string]string{"m0": dirA, "m1": dirB}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	if len(mounts) != 2 {
		t.Fatalf("want 2 attach mounts, got %d", len(mounts))
	}
	session := mounts[0].Session

	// Play the pod's caretaker: ONE multiplexed connection, one stream per mount.
	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	if got := readMarkerOverMux(t, mux, session, "m0"); got != "MOUNT-A" {
		t.Errorf("m0 over caretaker mux = %q, want MOUNT-A", got)
	}
	if got := readMarkerOverMux(t, mux, session, "m1"); got != "MOUNT-B" {
		t.Errorf("m1 over caretaker mux = %q, want MOUNT-B", got)
	}
}

// TestCaretakerUnifiedMount exercises the mount path on the UNIFIED pod-scoped
// endpoint (/.cornus/v1/caretaker/attach): the mount stream carries its deploy-attach
// session then name (two lines), so one session-independent connection serves the
// mount. Proves relayMountMuxed bridges to the caller's export.
func TestCaretakerUnifiedMount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("UNIFIED-9P"), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, map[string]string{"m0": dir}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	session := mounts[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial unified attach: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagMount)
	if err != nil {
		t.Fatalf("open mount stream: %v", err)
	}
	// Unified framing: session line then name line.
	if _, err := io.WriteString(stream, session+"\n"+"m0"+"\n"); err != nil {
		t.Fatalf("send session/name: %v", err)
	}
	p9c, err := p9.NewClient(stream)
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	root, err := p9c.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	_, f, err := root.Walk([]string{"marker"})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if _, _, err := f.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open: %v", err)
	}
	buf := make([]byte, 16)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "UNIFIED-9P" {
		t.Errorf("content over unified mount = %q, want UNIFIED-9P", buf[:n])
	}
}

// readMarkerOverMux opens one mount stream on the unified caretaker mux (session +
// name framing), runs a 9P client over it, and returns the export's "marker" file.
func readMarkerOverMux(t *testing.T, mux *yamux.Session, session, name string) string {
	t.Helper()
	stream, err := wire.OpenTagged(mux, wire.TagMount)
	if err != nil {
		t.Fatalf("open mount stream %s: %v", name, err)
	}
	if _, err := io.WriteString(stream, session+"\n"+name+"\n"); err != nil {
		t.Fatalf("send session/name %s: %v", name, err)
	}
	p9c, err := p9.NewClient(stream)
	if err != nil {
		t.Fatalf("p9 client %s: %v", name, err)
	}
	root, err := p9c.Attach("")
	if err != nil {
		t.Fatalf("attach %s: %v", name, err)
	}
	_, f, err := root.Walk([]string{"marker"})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	if _, _, err := f.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	buf := make([]byte, 16)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(buf[:n])
}

// TestCaretakerMountUnknownSession confirms a mount stream naming an unregistered
// session is closed (no bridge), rather than the connection being rejected — the
// pod-scoped endpoint accepts any connection and gates per stream.
func TestCaretakerMountUnknownSession(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mux, err := wire.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagMount)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()
	if _, err := io.WriteString(stream, "nope\nm0\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected the mount stream to be closed for an unknown session")
	}
}
