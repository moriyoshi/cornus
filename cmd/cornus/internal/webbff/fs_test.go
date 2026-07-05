package webbff

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
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

// explorerServerWithVolumes is explorerServer with an arbitrary `volumes:` block, for
// the safeguard tests that need a bind of a real host path.
func explorerServerWithVolumes(t *testing.T, upstream *httptest.Server, volumes string) (s *Server, projectDir string) {
	t.Helper()
	projectDir = t.TempDir()
	composePath := filepath.Join(projectDir, "compose.yaml")
	composeYAML := "services:\n  web:\n    image: example/web:1\n    volumes:\n" + volumes
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
	return s, projectDir
}

// rootIDForPath returns the id of the local root anchored at real, or "" when the
// explorer declined to expose it.
func rootIDForPath(s *Server, real string) string {
	for _, r := range s.localRoots {
		if r.Real == real {
			return r.ID
		}
	}
	return ""
}

// TestExplorerRefusesPseudoFilesystemRoots is the H14 regression. Before the fix,
// buildLocalRoots exposed EVERY external bind source that stats as a directory, so the
// standard monitoring idiom `- /proc:/host/proc:ro` produced a browsable — and writable —
// /proc root on a surface with no authentication, putting every local process's
// environment one GET away.
//
// The second subtest is the one that matters for the mechanism: /proc mounted at a name
// that says nothing about /proc. A path denylist passes the first and fails this.
func TestExplorerRefusesPseudoFilesystemRoots(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc on this host")
	}
	for _, tc := range []struct {
		name    string
		volumes string
	}{
		{"named /proc", "      - /proc:/host/proc:ro\n"},
		{"disguised", "      - /proc:/mnt/p:ro\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil), tc.volumes)

			if id := rootIDForPath(s, "/proc"); id != "" {
				t.Fatalf("/proc is exposed as browsable root %q — this is the bug", id)
			}
			// The refusal is explained, not silent: a root that merely vanishes reads
			// as a defect to whoever wrote the compose file.
			var roots fsRoots
			doJSON(t, s, "GET", "/.cornus/web/fs/roots", &roots)
			found := false
			for _, r := range roots.Refused {
				if r.Path == "/proc" {
					found = true
					if !strings.Contains(r.Reason, "pseudo-filesystem") {
						t.Errorf("reason should name the cause, got %q", r.Reason)
					}
				}
			}
			if !found {
				t.Errorf("refusal not reported to the UI: %+v", roots.Refused)
			}
			// And it is not reachable by id either, for anyone who guesses one.
			for _, id := range []string{"mount0", "mount1"} {
				rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root="+id+"&path=self", "")
				if rec.Code == http.StatusOK {
					t.Fatalf("root %q served a /proc path: %s", id, rec.Body.String())
				}
			}
		})
	}
}

// TestExplorerRefusesFilesystemRootBind covers the other tier-2 rule: `- /:/host` would
// anchor confinement at the whole machine, which is the same as having none.
func TestExplorerRefusesFilesystemRootBind(t *testing.T) {
	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil), "      - /:/host\n")
	if id := rootIDForPath(s, "/"); id != "" {
		t.Fatalf("/ is exposed as browsable root %q", id)
	}
}

// TestExplorerHonoursReadOnlyBind is the second half of H14. buildLocalRoots dropped
// m.ReadOnly entirely, so a `:ro` bind source — the mode the container is held to by the
// kernel — was served writably by the explorer.
func TestExplorerHonoursReadOnlyBind(t *testing.T) {
	roDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(roDir, "keep.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil),
		fmt.Sprintf("      - %s:/ro:ro\n", roDir))
	id := rootIDForPath(s, roDir)
	if id == "" {
		t.Fatal("a read-only bind source should still be BROWSABLE, just not writable")
	}
	// Reading is fine.
	if rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root="+id+"&path=", ""); rec.Code != http.StatusOK {
		t.Fatalf("listing a read-only root: %d %s", rec.Code, rec.Body.String())
	}
	// Every mutation is refused, and the file on disk is untouched.
	for _, m := range []struct{ method, path, body string }{
		{"PUT", "/.cornus/web/fs/content?source=local&root=" + id + "&path=keep.txt", "clobbered"},
		{"POST", "/.cornus/web/fs/mkdir?source=local&root=" + id + "&path=sub", ""},
		{"POST", "/.cornus/web/fs/rename?source=local&root=" + id + "&path=keep.txt", `{"to":"moved.txt"}`},
		{"DELETE", "/.cornus/web/fs?source=local&root=" + id + "&path=keep.txt", ""},
		{"POST", "/.cornus/web/fs/upload?source=local&root=" + id + "&path=&name=keep.txt", "clobbered"},
	} {
		if rec := doReq(t, s, m.method, m.path, m.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", m.method, m.path, rec.Code)
		}
	}
	if b, err := os.ReadFile(filepath.Join(roDir, "keep.txt")); err != nil || string(b) != "original" {
		t.Fatalf("read-only root was mutated: %q %v", b, err)
	}
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

	// fsopFn, when nil, makes every structured filesystem operation report
	// unsupported — which is what a backend with no operator does, and therefore the
	// only safe default for a fake. A fake that answered "yes" would route tests down
	// a path no daemon in the default suite provides, and every relay assertion would
	// pass for the wrong reason.
	fsopFn func(api.FSOpRequest) (api.FSOpResponse, error)
	// fsopCalls records what the BFF asked the operator to do, in order.
	fsopCalls []api.FSOpRequest
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

func (f *fakeContainerFS) FSOp(_ context.Context, _ string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	f.fsopCalls = append(f.fsopCalls, req)
	if f.fsopFn == nil {
		return api.FSOpUnsupported("this fake has no operator"), client.ErrFSOpUnsupported
	}
	if body != nil {
		io.Copy(io.Discard, body)
	}
	_ = out
	return f.fsopFn(req)
}

// Exec applies the caller's capture limit exactly as execCapture does. A fake that
// ignored it would be more permissive than the real thing, and every test about
// truncation would pass for the wrong reason.
func (f *fakeContainerFS) Exec(_ context.Context, _, workdir string, cmd []string, limit int) (ExecResult, error) {
	res, err := f.execFn(workdir, cmd)
	if len(res.Stdout) > limit {
		res.Stdout = res.Stdout[:limit]
	}
	if len(res.Stderr) > limit {
		res.Stderr = res.Stderr[:limit]
	}
	return res, err
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

// execInspectServer serves only GET /.cornus/v1/deploy/exec/{id}/json, the one call
// inspectExit makes. states is replayed in order, the last entry repeating; a nil
// entry is a 500, which is the "ExecInspect failed" case.
func execInspectServer(t *testing.T, states []*api.ExecState) (*client.Client, *int) {
	t.Helper()
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.cornus/v1/deploy/exec/{id}/json", func(w http.ResponseWriter, _ *http.Request) {
		i := calls
		calls++
		if i >= len(states) {
			i = len(states) - 1
		}
		if states[i] == nil {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(states[i])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return client.New(srv.URL), &calls
}

// TestInspectExitDistinguishesUnknownFromZero pins the H4 contract directly: an exit
// status that could not be read must NOT come back as 0/success. Before the fix the
// ExecInspect error was swallowed and every one of these returned (0, _) with the
// caller free to read it as success.
func TestInspectExitDistinguishesUnknownFromZero(t *testing.T) {
	t.Run("settled", func(t *testing.T) {
		cl, calls := execInspectServer(t, []*api.ExecState{{Running: false, ExitCode: 3}})
		code, known := inspectExit(t.Context(), cl, "e1")
		if !known || code != 3 {
			t.Fatalf("settled exec: got (%d, %v), want (3, true)", code, known)
		}
		if *calls != 1 {
			t.Errorf("a settled status should be read once, not %d times", *calls)
		}
	})
	t.Run("inspect fails", func(t *testing.T) {
		cl, _ := execInspectServer(t, []*api.ExecState{nil})
		if code, known := inspectExit(t.Context(), cl, "e1"); known {
			t.Fatalf("unreadable status reported as known (code %d) — this is the bug", code)
		}
	})
	t.Run("never settles", func(t *testing.T) {
		cl, _ := execInspectServer(t, []*api.ExecState{{Running: true}})
		if code, known := inspectExit(t.Context(), cl, "e1"); known {
			t.Fatalf("still-running exec reported as known (code %d)", code)
		}
	})
	t.Run("settles after a moment", func(t *testing.T) {
		// The realistic docker case: Running immediately after the stream closes,
		// then the real status. Trusting the first answer loses the exit code.
		cl, _ := execInspectServer(t, []*api.ExecState{
			{Running: true}, {Running: true}, {Running: false, ExitCode: 7},
		})
		code, known := inspectExit(t.Context(), cl, "e1")
		if !known || code != 7 {
			t.Fatalf("late-settling exec: got (%d, %v), want (7, true)", code, known)
		}
	})
}

// TestExplorerContainerMutationRefusesUnknownExit is the consequence that matters: a
// mutation whose status could not be read must fail, because callers sequence
// destructive work behind it (a move deletes its source only if the copy reported
// success). ExitKnown is false here by omission, which is exactly how a real
// unreadable status arrives.
func TestExplorerContainerMutationRefusesUnknownExit(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	ran := 0
	s.cfs = &fakeContainerFS{execFn: func(_ string, _ []string) (ExecResult, error) {
		ran++
		return ExecResult{}, nil // command ran; status unreadable
	}}

	rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=container&workload=proj-web&path=/app/f.txt", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("delete with unreadable exit status: got %d, want 502 — a zero exit code we never read is not success", rec.Code)
	}
	if ran != 1 {
		t.Errorf("expected the command to have been attempted once, got %d", ran)
	}
	if !strings.Contains(rec.Body.String(), "exit status unavailable") {
		t.Errorf("error should name the cause, got %q", rec.Body.String())
	}
}

func TestExplorerContainerListing(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	ts := fmt.Sprintf("%d", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	// Names with a space and a newline must survive the NUL framing.
	fake := &fakeContainerFS{
		execFn: func(workdir string, cmd []string) (ExecResult, error) {
			// The directory travels as an ARGUMENT, never as a working directory:
			// kubernetes cannot express one for an exec, so a listing that depended
			// on it answered with the image's default workdir under the requested
			// path's name. This assertion used to require workdir == "/app", which
			// pinned the mechanism that was broken rather than the contract.
			if workdir != "" {
				t.Errorf("the listing must not request an exec working directory, got %q", workdir)
			}
			if len(cmd) < 5 || cmd[0] != "/bin/sh" || cmd[len(cmd)-1] != "/app" {
				t.Errorf("the directory must be the last argv element: %q", cmd)
			}
			out := nulRec("d", "4096", ts, "755", "", "bin") +
				nulRec("f", "12", ts, "644", "", "a b.txt") +
				nulRec("f", "3", ts, "600", "", "line\nname") +
				nulRec("l", "0", ts, "777", "/etc/hosts", "hosts")
			return ExecResult{Stdout: out, ExitKnown: true}, nil
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
			return ExecResult{ExitCode: 1, ExitKnown: true, Stderr: "ls: /nope: No such file or directory"}, nil
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
			return ExecResult{ExitCode: 0, ExitKnown: true}, nil
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
		execFn: func(workdir string, cmd []string) (ExecResult, error) {
			if workdir != "" {
				t.Errorf("the listing must not request an exec working directory, got %q", workdir)
			}
			if cmd[len(cmd)-1] != "/app" {
				t.Errorf("workload sub-path not resolved: %q", cmd)
			}
			return ExecResult{Stdout: nulRec("f", "3", "0", "644", "", "hello.txt"), ExitKnown: true}, nil
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

// TestExplorerCopyDirectoryLocal copies a directory TREE within the local mount: the
// nested files arrive, the source is untouched, and the destination lands inside an
// existing directory under its source basename (`cp -r dir other/`).
func TestExplorerCopyDirectoryLocal(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	mk := func(rel, content string) {
		t.Helper()
		full := filepath.Join(projectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("tree/top.txt", "top")
	mk("tree/sub/deep.txt", "deep")
	mk("tree/sub/deeper/leaf.txt", "leaf")
	if err := os.MkdirAll(filepath.Join(projectDir, "dest"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/tree", `{"to":"project/dest"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("copy tree: %d %s", rec.Code, rec.Body.String())
	}
	for rel, want := range map[string]string{
		"dest/tree/top.txt":             "top",
		"dest/tree/sub/deep.txt":        "deep",
		"dest/tree/sub/deeper/leaf.txt": "leaf",
	} {
		b, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("%s: got %q %v, want %q", rel, b, err, want)
		}
	}
	// The source tree is left intact.
	if _, err := os.Stat(filepath.Join(projectDir, "tree", "sub", "deeper", "leaf.txt")); err != nil {
		t.Errorf("source tree damaged by copy: %v", err)
	}
}

// TestExplorerCopyDirectoryRefusals guards the two shapes that cannot work: a directory
// into its own subtree (the walk would recurse into what it is writing) and a bare mount
// root (no basename to land under).
func TestExplorerCopyDirectoryRefusals(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.MkdirAll(filepath.Join(projectDir, "tree", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The message matters as much as the code: without the self-copy guard the walk
	// still stops, but only after the depth cap has written 32 nested copies, and it
	// says "too deep" rather than what is actually wrong.
	cases := []struct{ name, path, to, want string }{
		{"into itself", "project/tree", "project/tree", "into itself"},
		{"into its own subtree", "project/tree", "project/tree/sub", "into itself"},
		{"a mount root", "project", "project/tree", "mount root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path="+tc.path, `{"to":"`+tc.to+`"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body %q, want it to mention %q", rec.Body.String(), tc.want)
			}
			// Nothing was written on the way to the refusal.
			if _, err := os.Stat(filepath.Join(projectDir, "tree", "tree")); err == nil {
				t.Errorf("the refused copy still created %s", filepath.Join("tree", "tree"))
			}
		})
	}
}

// TestExplorerCopyDirectorySkipsOddSymlinks copies a tree holding a symlink to a
// DIRECTORY and a dangling one. Neither can ride through the read/write path, so both
// are named in the response instead of failing the whole copy — and the regular files
// beside them still arrive.
func TestExplorerCopyDirectorySkipsOddSymlinks(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.MkdirAll(filepath.Join(projectDir, "tree", "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "tree", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(projectDir, "tree", "to-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("nowhere", filepath.Join(projectDir, "tree", "dangling")); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/tree", `{"to":"project/copy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result  string   `json:"result"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Skipped) != 2 {
		t.Errorf("skipped: got %v, want the two odd symlinks", got.Skipped)
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "copy", "keep.txt")); err != nil || string(b) != "keep" {
		t.Errorf("regular file beside the symlinks: %q %v", b, err)
	}
}

// TestExplorerContainerSymlinkIsNotAnEmptyFile is the H7 regression. tarcopy.Pack Lstats
// and emits a TypeSymlink header with NO BODY, and singleTarEntry rejected only TypeDir —
// so io.ReadAll returned zero bytes with no error and every container symlink copied as
// an empty regular file. Nothing failed, so nothing was recorded in `skipped` either,
// which is what made it dangerous once a move started deleting sources.
func TestExplorerContainerSymlinkIsNotAnEmptyFile(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	// Exactly what docker cp / tarcopy produce for a symlink: a header, no body.
	if err := tw.WriteHeader(&tar.Header{
		Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/hosts", Mode: 0o777,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	s.cfs = &fakeContainerFS{
		stat:     api.PathStat{Name: "link", Size: 0, Mode: uint32(os.ModeSymlink | 0o777)},
		copyFrom: tarBuf.Bytes(),
		execFn:   func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}
	rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/link", "")
	if rec.Code == http.StatusOK && rec.Body.Len() == 0 {
		t.Fatal("a container symlink read as an empty file with no error — this is the bug")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("symlink read: got %d, want 400", rec.Code)
	}
}

// TestExplorerContainerPutRefusesKindMismatch is the H6 regression: an empty
// CopyToOptions lets the extractor os.RemoveAll an existing DIRECTORY when a
// non-directory entry lands on its name, so writing a file over a same-named folder wiped
// the tree without confirmation or error.
func TestExplorerContainerPutRefusesKindMismatch(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	fake := &fakeContainerFS{
		// The destination exists and is a DIRECTORY.
		stat:   api.PathStat{Name: "data", Mode: uint32(os.ModeDir | 0o755)},
		execFn: func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}
	s.cfs = fake

	rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/data", "clobber")
	if rec.Code != http.StatusConflict {
		t.Fatalf("writing a file over a directory: got %d, want 409", rec.Code)
	}
	if fake.copyToBuf.Len() != 0 {
		t.Error("the refusal must happen before anything is sent to the daemon")
	}
}

// TestExplorerContainerPutSetsNoOverwriteDirNonDir pins the belt to the braces: the
// pre-check above is BFF-side, but barehost's gVisor tar-exec path and incus ignore the
// option, so both have to be in place.
func TestExplorerContainerPutSetsNoOverwriteDirNonDir(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	var gotOpts api.CopyToOptions
	fake := &optCapturingFS{
		fakeContainerFS: fakeContainerFS{
			statErr: errors.New("not found"),
			execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
		},
		opts: &gotOpts,
	}
	s.cfs = fake
	if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=container&workload=proj-web&path=/app/new.txt", "hi"); rec.Code != http.StatusOK {
		t.Fatalf("write: %d %s", rec.Code, rec.Body.String())
	}
	if !gotOpts.NoOverwriteDirNonDir {
		t.Error("CopyTo was issued without NoOverwriteDirNonDir")
	}
}

// optCapturingFS records the CopyToOptions the BFF chose.
type optCapturingFS struct {
	fakeContainerFS
	opts *api.CopyToOptions
}

func (f *optCapturingFS) CopyTo(ctx context.Context, name, path string, r io.Reader, o api.CopyToOptions) error {
	*f.opts = o
	return f.fakeContainerFS.CopyTo(ctx, name, path, r, o)
}

// TestLocalRootDeclared covers `cornus web --local-root`: a directory the Compose
// project never mentions becomes a browsable, writable root, and `:ro` on it is
// honoured by the WRITE path rather than merely reported in the switcher.
func TestLocalRootDeclared(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	scratch := t.TempDir()
	readonly := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.LocalRoots = []LocalRootSpec{
		{Label: "scratch", Path: scratch},
		{Path: readonly, ReadOnly: true},
	}
	if err := s.loadProject(); err != nil {
		t.Fatalf("loadProject: %v", err)
	}

	var roots fsRoots
	doJSON(t, s, "GET", "/.cornus/web/fs/roots", &roots)
	byID := map[string]fsRoot{}
	for _, r := range roots.Roots {
		byID[r.ID] = r
	}
	realScratch, _ := filepath.EvalSymlinks(scratch)
	if got, ok := byID["local0"]; !ok || got.Path != realScratch || got.Label != "scratch" || got.ReadOnly {
		t.Fatalf("local0 = %+v (present=%v), want {scratch %s rw}", got, ok, realScratch)
	}
	realRO, _ := filepath.EvalSymlinks(readonly)
	// An unlabelled root is named by its directory, not left blank.
	if got, ok := byID["local1"]; !ok || got.Path != realRO || !got.ReadOnly || got.Label != filepath.Base(realRO) {
		t.Fatalf("local1 = %+v (present=%v), want {%s %s ro}", got, ok, filepath.Base(realRO), realRO)
	}

	// The declared root really serves its contents.
	rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root=local0&path=", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "note.txt") {
		t.Errorf("listing local0: %d %s", rec.Code, rec.Body.String())
	}

	// :ro is a real refusal, not a label. This is the assertion that would catch a
	// root wired in with its mode dropped — which is exactly the bug the bind-mount
	// roots already had once.
	rec = doReq(t, s, "PUT", "/.cornus/web/fs/content?source=local&root=local1&path=x.txt", "nope")
	if rec.Code != http.StatusForbidden {
		t.Errorf("write to a :ro local root: got %d, want 403", rec.Code)
	}
}

// TestLocalRootWithoutComposeProject is the case the flag mostly exists for: no
// compose file anywhere, just a directory to browse. loadProject returns early
// when it finds no files, and the root set used to be built only on the path it
// returns early FROM — so the flag parsed, validated, and then did nothing.
func TestLocalRootWithoutComposeProject(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "solo.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A working directory with no compose file, so discovery finds nothing.
	empty := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	s, err := New(
		Config{LocalRoots: []LocalRootSpec{{Path: scratch}}},
		client.New(upstream.URL),
		upstream.URL,
		&clientconn.Resolver{},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	if name, _ := s.Project(); name != "" {
		t.Fatalf("a project was loaded (%q); this test needs the no-project path", name)
	}
	var roots fsRoots
	doJSON(t, s, "GET", "/.cornus/web/fs/roots", &roots)
	if len(roots.Roots) != 1 || roots.Roots[0].ID != "local0" {
		t.Fatalf("roots = %+v, want exactly the declared local0", roots.Roots)
	}
	rec := doReq(t, s, "GET", "/.cornus/web/fs?source=local&root=local0&path=", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "solo.txt") {
		t.Errorf("listing local0 with no project: %d %s", rec.Code, rec.Body.String())
	}
}
