package buildwire

import (
	"context"
	gofs "io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hugelgupf/p9/p9"
	"github.com/tonistiigi/fsutil"

	"cornus/pkg/build/buildprog"
	"cornus/pkg/wire"
)

// rawClient connects a p9 client directly to an attacher over an in-memory pipe,
// so tests can issue the kind of Twalk/Topen/Tcreate a hostile build server
// would — bypassing the honest p9FS adapter, which sanitizes paths itself.
func rawClient(t *testing.T, attacher p9.Attacher) *p9.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := p9.NewServer(attacher)
	go func() { _ = srv.Handle(c1, c1) }()
	cl, err := p9.NewClient(c2)
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	t.Cleanup(func() {
		_ = cl.Close()
		_ = c1.Close()
		_ = c2.Close()
	})
	return cl
}

func confinedContext(t *testing.T, dir string) *p9.Client {
	t.Helper()
	attacher, err := buildAttacher(ServeOpts{ContextDir: dir, DockerfileDir: dir})
	if err != nil {
		t.Fatalf("buildAttacher: %v", err)
	}
	return rawClient(t, attacher)
}

func walk(t *testing.T, cl *p9.Client, names ...string) (p9.File, error) {
	t.Helper()
	root, err := cl.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	_, f, err := root.Walk(names)
	return f, err
}

func TestConfinedRejectsDotDotTraversal(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "a.txt", "ok")
	cl := confinedContext(t, ctxDir)

	cases := [][]string{
		{"context", ".."},
		{"context", "..", "..", "etc", "passwd"},
		{"context", "sub", "..", ".."},
	}
	for _, names := range cases {
		if f, err := walk(t, cl, names...); err == nil {
			f.Close()
			t.Errorf("walk %v: expected error, got success", names)
		}
	}

	// The legitimate path still works.
	f, err := walk(t, cl, "context", "a.txt")
	if err != nil {
		t.Fatalf("walk a.txt: %v", err)
	}
	f.Close()
}

func TestConfinedSymlinks(t *testing.T) {
	ctxDir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Escaping symlinks: one to an outside dir, one to an outside file.
	if err := os.Symlink(outside, filepath.Join(ctxDir, "evildir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ctxDir, "evilfile")); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside the context.
	mustWrite(t, ctxDir, "inside.txt", "inside-data")
	if err := os.Symlink("inside.txt", filepath.Join(ctxDir, "good")); err != nil {
		t.Fatal(err)
	}
	cl := confinedContext(t, ctxDir)

	// Escaping symlinks are transmitted as symlinks (Walk + Readlink succeed) —
	// this matches docker, and is harmless: the link only resolves container-side.
	for _, name := range []string{"evildir", "evilfile", "good"} {
		f, err := walk(t, cl, "context", name)
		if err != nil {
			t.Fatalf("walk %s: %v", name, err)
		}
		if _, err := f.Readlink(); err != nil {
			t.Errorf("readlink %s: %v", name, err)
		}
		f.Close()
	}

	// ...but the caller's files are NOT exfiltrated: you cannot walk *through* an
	// escaping symlink to reach a file outside the export.
	if f, err := walk(t, cl, "context", "evildir", "secret"); err == nil {
		f.Close()
		t.Error("walk through escaping symlink: expected denial, got success")
	}

	// The in-context symlink reports its real target.
	f, err := walk(t, cl, "context", "good")
	if err != nil {
		t.Fatalf("walk good: %v", err)
	}
	if target, _ := f.Readlink(); target != "inside.txt" {
		t.Errorf("in-context symlink target = %q, want %q", target, "inside.txt")
	}
	f.Close()
}

func TestConfinedIsReadOnly(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "a.txt", "ok")
	cl := confinedContext(t, ctxDir)

	dir, err := walk(t, cl, "context")
	if err != nil {
		t.Fatalf("walk context: %v", err)
	}
	defer dir.Close()

	if _, _, _, err := dir.Create("new.txt", p9.WriteOnly, 0o644, 0, 0); err == nil {
		t.Error("Create: expected denial, got success")
	}
	if err := dir.UnlinkAt("a.txt", 0); err == nil {
		t.Error("UnlinkAt: expected denial, got success")
	}
	if _, err := dir.Mkdir("d", 0o755, 0, 0); err == nil {
		t.Error("Mkdir succeeded")
	}
	// Nothing was written to disk.
	if _, err := os.Stat(filepath.Join(ctxDir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new.txt should not exist: stat err = %v", err)
	}

	// Opening a file for writing is refused.
	f, err := walk(t, cl, "context", "a.txt")
	if err != nil {
		t.Fatalf("walk a.txt: %v", err)
	}
	defer f.Close()
	if _, _, err := f.Open(p9.WriteOnly); err == nil {
		t.Error("Open(WriteOnly): expected denial, got success")
	}
}

// TestSymlinkTargetOverWire proves p9FS carries a symlink's target (Linkname)
// across the wire, so fsutil materializes it as a real symlink rather than a
// broken/empty one.
func TestSymlinkTargetOverWire(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "inside.txt", "data")
	if err := os.Symlink("inside.txt", filepath.Join(ctxDir, "good")); err != nil {
		t.Fatal(err)
	}

	url := testServer(t, func(s *ServerSession) {
		pw := s.Progress()
		err := s.Mounts()["context"].Walk(context.Background(), "", func(p string, d gofs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type()&gofs.ModeSymlink != 0 {
				if de, ok := d.(*fsutil.DirEntryInfo); ok && de.Stat != nil {
					pw.Log("symlink:%s->%s\n", p, de.Stat.Linkname)
				}
			}
			return nil
		})
		if err != nil {
			pw.Log("walk-error:%v\n", err)
		}
		_ = s.Done(&Result{}, nil)
	})

	var progress strings.Builder
	if _, err := Serve(context.Background(), url, BuildSpec{},
		ServeOpts{ContextDir: ctxDir, DockerfileDir: ctxDir}, buildprog.NewSink(&progress, buildprog.Plain, false), nil, wire.ClientTransport{}); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(progress.String(), "symlink:good->inside.txt") {
		t.Errorf("symlink target not transmitted; got:\n%s", progress.String())
	}
}

// TestDockerignoreNamedContext checks that each named build context honors its
// own .dockerignore, independently of the main context's patterns.
func TestDockerignoreNamedContext(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "main.txt", "main")
	// The main context ignores *.txt — this must NOT affect the named context.
	mustWrite(t, ctxDir, ".dockerignore", "*.skip\n")

	dataDir := t.TempDir()
	mustWrite(t, dataDir, "keep.bin", "keep")
	mustWrite(t, dataDir, "drop.skip", "drop")
	mustWrite(t, dataDir, "vendor/lib.a", "vendored")
	mustWrite(t, dataDir, ".dockerignore", "*.skip\nvendor\n")

	url := testServer(t, func(s *ServerSession) {
		pw := s.Progress()
		for k := range readFS(t, s.Mounts()["context"]) {
			pw.Log("ctx:%s\n", k)
		}
		for k := range readFS(t, s.Mounts()["data"]) {
			pw.Log("data:%s\n", k)
		}
		_ = s.Done(&Result{ImageDigest: "sha256:test"}, nil)
	})

	var progress strings.Builder
	if _, err := Serve(context.Background(), url, BuildSpec{NamedContexts: []string{"data"}},
		ServeOpts{ContextDir: ctxDir, DockerfileDir: ctxDir, NamedContexts: map[string]string{"data": dataDir}},
		buildprog.NewSink(&progress, buildprog.Plain, false), nil, wire.ClientTransport{}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	got := progress.String()
	for _, want := range []string{"ctx:main.txt", "data:keep.bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in export; got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"data:drop.skip", "data:vendor/lib.a"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("named-context ignored path leaked: %q; got:\n%s", unwanted, got)
		}
	}
}

func TestDockerignoreFiltersExport(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "keep.txt", "keep")
	mustWrite(t, ctxDir, "skip.log", "noisy")
	mustWrite(t, ctxDir, "secret.env", "API_KEY=xyz")
	mustWrite(t, ctxDir, "sub/keep2.txt", "keep2")
	mustWrite(t, ctxDir, "node_modules/lib.js", "vendored")
	mustWrite(t, ctxDir, ".dockerignore", "*.log\nsecret.env\nnode_modules\n")

	url := testServer(t, func(s *ServerSession) {
		pw := s.Progress()
		for k := range readFS(t, s.Mounts()["context"]) {
			pw.Log("have:%s\n", k)
		}
		_ = s.Done(&Result{ImageDigest: "sha256:test"}, nil)
	})

	var progress strings.Builder
	if _, err := Serve(context.Background(), url, BuildSpec{DockerfileName: "Dockerfile"},
		ServeOpts{ContextDir: ctxDir, DockerfileDir: ctxDir, DockerfileName: "Dockerfile"},
		buildprog.NewSink(&progress, buildprog.Plain, false), nil, wire.ClientTransport{}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	got := progress.String()
	for _, want := range []string{"have:keep.txt", "have:sub/keep2.txt", "have:.dockerignore"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in export; got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"have:skip.log", "have:secret.env", "have:node_modules/lib.js"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("ignored path leaked: %q; got:\n%s", unwanted, got)
		}
	}
}
