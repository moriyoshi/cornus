package webbff

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/client"
)

// ---- local source ----

// explorerServer builds a Server whose project has an in-tree bind mount (./html,
// reachable through the project root) and an EXTERNAL bind mount (a separate temp
// dir, exposed as its own root). It returns the project dir and the external dir.
func explorerServer(t *testing.T, upstream *httptest.Server) (s *Server, projectDir, sharedDir string) {
	t.Helper()
	projectDir = t.TempDir()
	sharedDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(projectDir, "compose.yaml")
	composeYAML := fmt.Sprintf(`services:
  web:
    image: example/web:1
    volumes:
      - ./html:/usr/share/nginx/html:ro
      - %s:/data
`, sharedDir)
	if err := os.WriteFile(composePath, []byte(composeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL),
		upstream.URL,
		&clientconn.Resolver{},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s, projectDir, sharedDir
}

// doReq drives one BFF request with an optional body and returns the recorder.
func doReq(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.routes(mux)
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, r))
	return rec
}

func TestExplorerLocalListing(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s, projectDir, _ := explorerServer(t, upstream)

	// project tree: a file, a subdir, and a symlink.
	if err := os.WriteFile(filepath.Join(projectDir, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("readme.txt", filepath.Join(projectDir, "link")); err != nil {
		t.Fatal(err)
	}

	var out fsListing
	rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=local&root=project&path=", &out)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	byName := map[string]fsEntry{}
	for _, e := range out.Entries {
		byName[e.Name] = e
	}
	// compose.yaml, html/, readme.txt, src/, link — dirs sort first.
	if out.Entries[0].Kind != "dir" {
		t.Errorf("dirs should sort first: %+v", out.Entries)
	}
	if e := byName["readme.txt"]; e.Kind != "file" || e.Size != 5 {
		t.Errorf("readme.txt entry: %+v", e)
	}
	if e := byName["src"]; e.Kind != "dir" {
		t.Errorf("src entry: %+v", e)
	}
	if e := byName["link"]; e.Kind != "symlink" || e.LinkTarget != "readme.txt" {
		t.Errorf("link entry: %+v", e)
	}
}

func TestExplorerLocalRoundTrip(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s, projectDir, _ := explorerServer(t, upstream)

	// write -> read
	if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=local&root=project&path=new.txt", "content"); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, "new.txt")); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=local&root=project&path=new.txt", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "content" {
		t.Fatalf("GET: %d %q", rec.Code, rec.Body.String())
	}

	// mkdir -> list shows it
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/mkdir?source=local&root=project&path=sub/deep", ""); rec.Code != http.StatusOK {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body.String())
	}
	if fi, err := os.Stat(filepath.Join(projectDir, "sub", "deep")); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}

	// upload (raw + name)
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/upload?source=local&root=project&path=sub&name=up.txt", "u"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "sub", "up.txt")); err != nil || string(b) != "u" {
		t.Fatalf("upload content: %q %v", b, err)
	}

	// rename
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/rename?source=local&root=project&path=new.txt", `{"to":"renamed.txt"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	var stat fsEntry
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs/stat?source=local&root=project&path=renamed.txt", &stat); rec.Code != http.StatusOK || stat.Name != "renamed.txt" {
		t.Fatalf("stat after rename: %d %+v", rec.Code, stat)
	}

	// delete
	if rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=local&root=project&path=renamed.txt", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, "renamed.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file not deleted: %v", err)
	}
	// cannot delete the root
	if rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=local&root=project&path=", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("delete root: got %d, want 400", rec.Code)
	}
}

func TestExplorerImageContentType(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s, projectDir, _ := explorerServer(t, upstream)
	if err := os.WriteFile(filepath.Join(projectDir, "logo.png"), []byte("\x89PNG\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An inline image read is served with the image MIME so the <img> viewer renders it.
	rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=virtual&path=project/logo.png", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("image read: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("image content-type: got %q, want image/png", ct)
	}
	// Non-image inline reads stay text/plain (the editor path).
	rec = doReq(t, s, "GET", "/.cornus/web/fs/content?source=virtual&path=project/readme.txt", "")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("text content-type: got %q, want text/plain", ct)
	}
}

func TestExplorerLocalConfinement(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s, projectDir, _ := explorerServer(t, upstream)

	// a symlink inside the root that points outside it
	if err := os.Symlink("/etc", filepath.Join(projectDir, "escape")); err != nil {
		t.Fatal(err)
	}

	// A path that resolves outside the root through an escaping symlink is refused
	// outright (403) for both read and write.
	for _, path := range []string{"escape/passwd", "escape"} {
		if rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root=project&path="+path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("read through escape %q: got %d, want 403", path, rec.Code)
		}
		if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=local&root=project&path="+path, "x"); rec.Code != http.StatusForbidden {
			t.Errorf("write through escape %q: got %d, want 403", path, rec.Code)
		}
	}

	// ".."/absolute spellings are neutralized by cleaning: they collapse to a path
	// INSIDE the root (here nonexistent), never escaping it, so a write cannot touch
	// /etc/passwd. It lands harmlessly in-root (404 for the missing parent).
	for _, path := range []string{"../../../etc/passwd", "/etc/passwd"} {
		if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=local&root=project&path="+path, "x"); rec.Code == http.StatusOK {
			t.Errorf("write %q unexpectedly succeeded", path)
		}
		if _, err := os.Stat("/etc/passwd.cornus-test"); err == nil {
			t.Fatal("escaped the root")
		}
	}

	// unknown root is a 400
	if rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root=bogus&path=", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown root: got %d, want 400", rec.Code)
	}
}

func TestExplorerRoots(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: true}}},
	}, nil)
	s, _, sharedDir := explorerServer(t, upstream)

	var roots fsRoots
	doJSON(t, s, "GET", "/.cornus/web/fs/roots", &roots)
	ids := map[string]fsRoot{}
	for _, r := range roots.Roots {
		ids[r.ID] = r
	}
	if _, ok := ids["project"]; !ok {
		t.Errorf("missing project root: %+v", roots.Roots)
	}
	// the external bind source is its own root; the in-tree ./html is NOT (reachable
	// through the project root).
	var mount fsRoot
	for _, r := range roots.Roots {
		if strings.HasPrefix(r.ID, "mount") {
			mount = r
		}
	}
	realShared, _ := filepath.EvalSymlinks(sharedDir)
	if mount.Path != realShared {
		t.Errorf("external mount root: got %+v, want path %s", mount, realShared)
	}
	for _, r := range roots.Roots {
		if strings.HasSuffix(r.Path, "/html") {
			t.Errorf("in-tree mount should not be a separate root: %+v", r)
		}
	}
	if len(roots.Workloads) != 1 || roots.Workloads[0].Name != "proj-web" || !roots.Workloads[0].Running {
		t.Errorf("workloads: %+v", roots.Workloads)
	}
}

// ---- container source ----

// fakeContainerFS is an in-memory containerFS for daemon-free container-source tests.
type fakeContainerFS struct {
	stat        api.PathStat
	statErr     error
	copyFrom    []byte // tar bytes returned by CopyFrom
	copyFromErr error
	execFn      func(workdir string, cmd []string) (ExecResult, error)

	copyToPath string       // last CopyTo destination dir
	copyToBuf  bytes.Buffer // last CopyTo archive
}

func (f *fakeContainerFS) StatPath(_ context.Context, _, _ string) (api.PathStat, error) {
	return f.stat, f.statErr
}
func (f *fakeContainerFS) CopyFrom(_ context.Context, _, _ string, w io.Writer) (api.PathStat, error) {
	if f.copyFromErr != nil {
		return api.PathStat{}, f.copyFromErr
	}
	_, err := w.Write(f.copyFrom)
	return f.stat, err
}
func (f *fakeContainerFS) CopyTo(_ context.Context, _, path string, r io.Reader, _ api.CopyToOptions) error {
	f.copyToPath = path
	f.copyToBuf.Reset()
	_, err := io.Copy(&f.copyToBuf, r)
	return err
}
func (f *fakeContainerFS) Exec(_ context.Context, _, workdir string, cmd []string) (ExecResult, error) {
	return f.execFn(workdir, cmd)
}

// runningUpstream fakes a server where proj-web is running (for ensureRunning).
func runningUpstream(t *testing.T) *httptest.Server {
	return fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: true}}},
	}, nil)
}

func nulRec(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f)
		b.WriteByte(0)
	}
	return b.String()
}

func TestExplorerContainerListing(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	ts := fmt.Sprintf("%d", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	// Names with a space and a newline must survive the NUL framing.
	fake := &fakeContainerFS{
		execFn: func(workdir string, cmd []string) (ExecResult, error) {
			if workdir != "/app" || len(cmd) < 3 || cmd[0] != "/bin/sh" {
				t.Errorf("unexpected exec: %q %q", workdir, cmd)
			}
			out := nulRec("d", "4096", ts, "755", "", "bin") +
				nulRec("f", "12", ts, "644", "", "a b.txt") +
				nulRec("f", "3", ts, "600", "", "line\nname") +
				nulRec("l", "0", ts, "777", "/etc/hosts", "hosts")
			return ExecResult{Stdout: out}, nil
		},
	}
	s.cfs = fake

	var out fsListing
	rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=container&workload=proj-web&path=/app", &out)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	byName := map[string]fsEntry{}
	for _, e := range out.Entries {
		byName[e.Name] = e
	}
	if e := byName["a b.txt"]; e.Kind != "file" || e.Size != 12 || e.Mode != "0644" {
		t.Errorf("space name: %+v", e)
	}
	if _, ok := byName["line\nname"]; !ok {
		t.Errorf("newline name lost: %+v", out.Entries)
	}
	if e := byName["hosts"]; e.Kind != "symlink" || e.LinkTarget != "/etc/hosts" {
		t.Errorf("symlink: %+v", e)
	}
	if out.Entries[0].Kind != "dir" {
		t.Errorf("dirs first: %+v", out.Entries)
	}
}

func TestExplorerContainerListingErrors(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	s.cfs = &fakeContainerFS{
		execFn: func(_ string, _ []string) (ExecResult, error) {
			return ExecResult{ExitCode: 1, Stderr: "ls: /nope: No such file or directory"}, nil
		},
	}
	if rec := doReq(t, s, "GET", "/.cornus/web/fs?source=container&workload=proj-web&path=/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing dir: got %d, want 404", rec.Code)
	}
	// workload not running -> 409
	s2, _, _ := explorerServer(t, fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: false}}},
	}, nil))
	s2.cfs = &fakeContainerFS{execFn: func(_ string, _ []string) (ExecResult, error) { return ExecResult{}, nil }}
	if rec := doReq(t, s2, "GET", "/.cornus/web/fs?source=container&workload=proj-web&path=/", ""); rec.Code != http.StatusConflict {
		t.Errorf("stopped workload: got %d, want 409", rec.Code)
	}
}

func TestExplorerContainerListingTarFallback(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	// A shell that will not start forces the recursive-tar-header fallback.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []struct {
		name string
		dir  bool
	}{
		{"app/", true}, {"app/top.txt", false}, {"app/sub/", true}, {"app/sub/deep.txt", false},
	} {
		h := &tar.Header{Name: e.name, Mode: 0o644, ModTime: time.Unix(1700000000, 0)}
		if e.dir {
			h.Typeflag = tar.TypeDir
		} else {
			h.Typeflag = tar.TypeReg
			h.Size = 2
		}
		tw.WriteHeader(h)
		if !e.dir {
			tw.Write([]byte("hi"))
		}
	}
	tw.Close()
	s.cfs = &fakeContainerFS{
		execFn:   func(_ string, _ []string) (ExecResult, error) { return ExecResult{}, errors.New("exec: no such file") },
		copyFrom: buf.Bytes(),
	}

	var out fsListing
	rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=container&workload=proj-web&path=/app", &out)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback list: %d %s", rec.Code, rec.Body.String())
	}
	names := []string{}
	for _, e := range out.Entries {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] != "sub" || names[1] != "top.txt" {
		t.Errorf("fallback should list only top-level entries: %v", names)
	}
}

func TestExplorerContainerReadWrite(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))

	// read: CopyFrom yields a single-file tar.
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	tw.WriteHeader(&tar.Header{Name: "f.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg})
	tw.Write([]byte("hello"))
	tw.Close()
	fake := &fakeContainerFS{
		stat:     api.PathStat{Name: "f.txt", Size: 5, Mode: 0o644},
		copyFrom: tarBuf.Bytes(),
		execFn:   func(_ string, _ []string) (ExecResult, error) { return ExecResult{}, nil },
	}
	s.cfs = fake
	rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/f.txt", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("container read: %d %q", rec.Code, rec.Body.String())
	}

	// write: CopyTo captures a one-entry tar dropped into the parent dir.
	fake.statErr = errors.New("not found") // new file -> default mode
	if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/w.txt", "data"); rec.Code != http.StatusOK {
		t.Fatalf("container write: %d %s", rec.Code, rec.Body.String())
	}
	if fake.copyToPath != "/app" {
		t.Errorf("CopyTo dir: got %q, want /app", fake.copyToPath)
	}
	tr := tar.NewReader(&fake.copyToBuf)
	h, err := tr.Next()
	if err != nil || h.Name != "w.txt" {
		t.Fatalf("uploaded header: %+v %v", h, err)
	}
	body, _ := io.ReadAll(tr)
	if string(body) != "data" {
		t.Errorf("uploaded body: %q", body)
	}

	// too large -> 413
	fake.statErr = nil
	fake.stat = api.PathStat{Size: maxEditableFileSize + 1, Mode: 0o644}
	if rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/big", ""); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("large read: got %d, want 413", rec.Code)
	}
}

func TestExplorerContainerRenameDelete(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	var gotCmds [][]string
	s.cfs = &fakeContainerFS{
		execFn: func(_ string, cmd []string) (ExecResult, error) {
			gotCmds = append(gotCmds, cmd)
			return ExecResult{ExitCode: 0}, nil
		},
	}
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/rename?source=container&workload=proj-web&path=/app/a", `{"to":"/app/b"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=container&workload=proj-web&path=/app/b&recursive=1", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if len(gotCmds) != 2 || gotCmds[0][0] != "mv" || gotCmds[1][0] != "rm" || gotCmds[1][1] != "-rf" {
		t.Errorf("exec commands: %v", gotCmds)
	}
}

// ensure the container source errors cleanly with no workload.
func TestExplorerContainerRequiresWorkload(t *testing.T) {
	s, _, _ := explorerServer(t, fakeCornusServer(t, nil, nil))
	if rec := doReq(t, s, "GET", "/.cornus/web/fs?source=container&path=/", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no workload: got %d, want 400", rec.Code)
	}
}

// ---- virtual namespace ----

// TestExplorerVirtualRoot lists the virtual root: local roots first, then workloads
// with their running state attached.
func TestExplorerVirtualRoot(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: true}}},
		{Name: "proj-db", Instances: []api.InstanceStatus{{Running: false}}},
	}, nil)
	s, _, _ := explorerServer(t, upstream)

	var out fsListing
	rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=", &out)
	if rec.Code != http.StatusOK {
		t.Fatalf("virtual root: %d %s", rec.Code, rec.Body.String())
	}
	if out.Source != "virtual" {
		t.Errorf("source: got %q", out.Source)
	}
	byName := map[string]fsEntry{}
	for _, e := range out.Entries {
		if e.Kind != "dir" {
			t.Errorf("mount %q should be a dir: %+v", e.Name, e)
		}
		byName[e.Name] = e
	}
	// The project root is a mount; local roots carry no running flag.
	if e, ok := byName["project"]; !ok || e.Running != nil {
		t.Errorf("project mount: %+v (ok=%v)", e, ok)
	}
	// Workloads carry running state.
	if e := byName["proj-web"]; e.Running == nil || !*e.Running {
		t.Errorf("proj-web should be running: %+v", e)
	}
	if e := byName["proj-db"]; e.Running == nil || *e.Running {
		t.Errorf("proj-db should be stopped: %+v", e)
	}
}

// TestExplorerVirtualNavigate proves a virtual path resolves onto the right mount:
// a local root by id and a workload by name.
func TestExplorerVirtualNavigate(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.WriteFile(filepath.Join(projectDir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfs = &fakeContainerFS{
		execFn: func(workdir string, _ []string) (ExecResult, error) {
			if workdir != "/app" {
				t.Errorf("workload sub-path not resolved: %q", workdir)
			}
			return ExecResult{Stdout: nulRec("f", "3", "0", "644", "", "hello.txt")}, nil
		},
	}

	// /project/... -> the local project root.
	var local fsListing
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=project", &local); rec.Code != http.StatusOK {
		t.Fatalf("local mount: %d %s", rec.Code, rec.Body.String())
	}
	if local.Source != "virtual" || local.Path != "project" {
		t.Errorf("echoed path: %+v", local)
	}
	found := false
	for _, e := range local.Entries {
		if e.Name == "readme.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("readme.txt missing from project mount: %+v", local.Entries)
	}

	// /proj-web/app -> the workload container filesystem at /app.
	var cont fsListing
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=proj-web/app", &cont); rec.Code != http.StatusOK {
		t.Fatalf("workload mount: %d %s", rec.Code, rec.Body.String())
	}
	if len(cont.Entries) != 1 || cont.Entries[0].Name != "hello.txt" {
		t.Errorf("workload listing: %+v", cont.Entries)
	}
	if cont.Path != "proj-web/app" {
		t.Errorf("echoed workload path: %q", cont.Path)
	}
}

// TestExplorerVirtualRoundTrip drives write/read/delete entirely through virtual
// paths against a local mount.
func TestExplorerVirtualRoundTrip(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))

	if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=virtual&path=project/v.txt", "hello"); rec.Code != http.StatusOK {
		t.Fatalf("virtual write: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "v.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file not written via virtual path: %q %v", b, err)
	}
	if rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=virtual&path=project/v.txt", ""); rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("virtual read: %d %q", rec.Code, rec.Body.String())
	}
	// Operations on the bare virtual root are rejected.
	if rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=virtual&path=", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("delete virtual root: got %d, want 400", rec.Code)
	}
	// A cross-mount rename is refused (that is what copy is for).
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/rename?source=virtual&path=project/v.txt", `{"to":"assets/v.txt"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("cross-mount rename: got %d, want 400", rec.Code)
	}
}

// TestExplorerCopyLocal copies a file within the local project mount.
func TestExplorerCopyLocal(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.WriteFile(filepath.Join(projectDir, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/src.txt", `{"to":"project/dst.txt"}`); rec.Code != http.StatusOK {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "dst.txt")); err != nil || string(b) != "payload" {
		t.Fatalf("copy target: %q %v", b, err)
	}
	// The source is left intact.
	if _, err := os.Stat(filepath.Join(projectDir, "src.txt")); err != nil {
		t.Errorf("source removed by copy: %v", err)
	}
}

// TestExplorerCopyLocalToContainer copies a local file into a workload, proving the
// virtual namespace spans sources.
func TestExplorerCopyLocalToContainer(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.WriteFile(filepath.Join(projectDir, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeContainerFS{
		statErr: errors.New("not found"), // dst does not exist -> plain file write
		execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{}, nil },
	}
	s.cfs = fake

	if rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/src.txt", `{"to":"proj-web/app/dst.txt"}`); rec.Code != http.StatusOK {
		t.Fatalf("cross-source copy: %d %s", rec.Code, rec.Body.String())
	}
	// The file was dropped into the workload's /app via a one-entry tar.
	if fake.copyToPath != "/app" {
		t.Errorf("CopyTo dir: got %q, want /app", fake.copyToPath)
	}
	tr := tar.NewReader(&fake.copyToBuf)
	h, err := tr.Next()
	if err != nil || h.Name != "dst.txt" {
		t.Fatalf("copied header: %+v %v", h, err)
	}
	if body, _ := io.ReadAll(tr); string(body) != "payload" {
		t.Errorf("copied body: %q", body)
	}
}
