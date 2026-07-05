package webbff

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/client"
)

// redirectServer builds a project whose workload binds sharedDir at /data, with the
// deployment's Origin attesting that it came from THIS machine and THIS directory —
// without which the redirect correctly refuses (see originMatchesHere).
func redirectServer(t *testing.T) (s *Server, projectDir, sharedDir string) {
	t.Helper()
	projectDir, sharedDir = t.TempDir(), t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname")
	}
	composePath := filepath.Join(projectDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(fmt.Sprintf(
		"services:\n  web:\n    image: example/web:1\n    volumes:\n      - %s:/data\n", sharedDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	upstream := fakeCornusServer(t, []api.DeployStatus{{
		Name:      "proj-web",
		Instances: []api.InstanceStatus{{Running: true}},
		Origin:    &api.Origin{Host: host, Directory: real, Project: "proj"},
	}}, nil)
	s, err = New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL), upstream.URL, &clientconn.Resolver{},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s, projectDir, sharedDir
}

// fatalContainerFS fails the test if the explorer reaches for the daemon at all. It is
// the proof that a redirected path costs zero round trips — the entire point of the
// client site.
type fatalContainerFS struct{ t *testing.T }

func (f fatalContainerFS) StatPath(context.Context, string, string) (api.PathStat, error) {
	f.t.Fatal("StatPath: a bind-resolved path must not reach the daemon")
	return api.PathStat{}, nil
}
func (f fatalContainerFS) CopyFrom(context.Context, string, string, io.Writer) (api.PathStat, error) {
	f.t.Fatal("CopyFrom: a bind-resolved path must not reach the daemon")
	return api.PathStat{}, nil
}
func (f fatalContainerFS) CopyTo(context.Context, string, string, io.Reader, api.CopyToOptions) error {
	f.t.Fatal("CopyTo: a bind-resolved path must not reach the daemon")
	return nil
}
func (f fatalContainerFS) Exec(context.Context, string, string, []string, int) (ExecResult, error) {
	f.t.Fatal("Exec: a bind-resolved path must not reach the daemon")
	return ExecResult{}, nil
}

// FSOp is fatal for the same reason as its siblings, and it says something extra: when
// both ends of a transfer are on the developer's disk the planner must decide that
// WITHOUT asking anyone. planTransfer's first rule returns before it consults the
// server-op probe, so a probe reaching here means the ordering was lost.
func (f fatalContainerFS) FSOp(context.Context, string, api.FSOpRequest, io.Reader, io.Writer) (api.FSOpResponse, error) {
	f.t.Fatal("FSOp: a client-to-client transfer must not consult the server")
	return api.FSOpResponse{}, nil
}

// TestSiteRedirectsBindMountToClient is the headline behaviour: a container path that
// falls under a client-local bind mount resolves to the developer's own filesystem, so
// the bytes never travel to the daemon and back over 9P to the disk they started on.
func TestSiteRedirectsBindMountToClient(t *testing.T) {
	s, _, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	real, _ := filepath.EvalSymlinks(sharedDir)

	site, err := s.site(t.Context(), fsQuery{source: "virtual", path: "proj-web/data/sub/x.txt"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if site.kind != siteClient {
		t.Fatalf("kind = %v, want client (why: %s)", site.kind, site.why)
	}
	if site.root.Real != real {
		t.Errorf("anchored at %q, want the bind source %q", site.root.Real, real)
	}
	if site.path != "sub/x.txt" {
		t.Errorf("path = %q, want the path relative to the bind source", site.path)
	}
	// The container spelling survives, so degrading to a relay is a field selection.
	if site.containerPath != "/data/sub/x.txt" {
		t.Errorf("containerPath = %q", site.containerPath)
	}
}

// TestSiteKeepsUnmountedPathsInTheContainer is the paired negative. Without it the tests
// above pass vacuously for a resolver that redirects everything.
func TestSiteKeepsUnmountedPathsInTheContainer(t *testing.T) {
	s, _, _ := redirectServer(t)
	site, err := s.site(t.Context(), fsQuery{source: "virtual", path: "proj-web/etc/hosts"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if site.kind != siteContainer {
		t.Fatalf("kind = %v, want container (why: %s)", site.kind, site.why)
	}
	if site.path != "/etc/hosts" {
		t.Errorf("path = %q, want the container-absolute path", site.path)
	}
}

// TestSiteRefusesRedirectForForeignOrigin covers the guard that keeps a stale checkout
// from writing to the wrong directory: DeployStatus carries no mounts, so the table and
// the running workload are joined by name alone.
func TestSiteRefusesRedirectForForeignOrigin(t *testing.T) {
	projectDir, sharedDir := t.TempDir(), t.TempDir()
	composePath := filepath.Join(projectDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(fmt.Sprintf(
		"services:\n  web:\n    image: example/web:1\n    volumes:\n      - %s:/data\n", sharedDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deployed from somewhere else entirely.
	upstream := fakeCornusServer(t, []api.DeployStatus{{
		Name:      "proj-web",
		Instances: []api.InstanceStatus{{Running: true}},
		Origin:    &api.Origin{Host: "another-machine", Directory: "/elsewhere"},
	}}, nil)
	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL), upstream.URL, &clientconn.Resolver{},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	site, err := s.site(t.Context(), fsQuery{source: "virtual", path: "proj-web/data/x.txt"})
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if site.kind != siteContainer {
		t.Fatalf("a foreign deployment must not redirect onto this machine's disk; got %v (why: %s)",
			site.kind, site.why)
	}
}

// TestSiteRefusesRedirectForNestedVolume is the shadow case: the host directory has an
// ordinary (empty) cache/ in it while the container sees a volume, so resolving to the
// host would read — and write — the wrong bytes.
func TestSiteRefusesRedirectForNestedVolume(t *testing.T) {
	projectDir, sharedDir := t.TempDir(), t.TempDir()
	host, _ := os.Hostname()
	composePath := filepath.Join(projectDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(fmt.Sprintf(
		"services:\n  web:\n    image: example/web:1\n    volumes:\n      - %s:/data\n      - cache:/data/cache\n",
		sharedDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(projectDir)
	upstream := fakeCornusServer(t, []api.DeployStatus{{
		Name:      "proj-web",
		Instances: []api.InstanceStatus{{Running: true}},
		Origin:    &api.Origin{Host: host, Directory: real},
	}}, nil)
	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL), upstream.URL, &clientconn.Resolver{},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// The bind still redirects...
	if site, _ := s.site(t.Context(), fsQuery{source: "virtual", path: "proj-web/data/x"}); site.kind != siteClient {
		t.Errorf("/data/x should redirect, got %v (%s)", site.kind, site.why)
	}
	// ...but the nested volume does not.
	site, err := s.site(t.Context(), fsQuery{source: "virtual", path: "proj-web/data/cache/x"})
	if err != nil {
		t.Fatal(err)
	}
	if site.kind != siteServer {
		t.Fatalf("/data/cache/x is a volume, not the host directory; got %v (why: %s)",
			site.kind, site.why)
	}
}

// TestExplorerServesBindMountFromDisk is the feature end to end, through the real HTTP
// surface: read, list, write and delete of a bind-mounted container path all happen on
// the developer's own filesystem, with a containerFS that fails the test on contact.
//
// Before this, every one of these round-tripped to the daemon — and for a client-local
// bind the container's write then travelled back out over 9P to this very directory.
func TestExplorerServesBindMountFromDisk(t *testing.T) {
	s, _, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(sharedDir, "hello.txt"), []byte("on disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	// read
	rec := doReq(t, s, "GET", "/.cornus/web/fs/content?source=virtual&path=proj-web/data/hello.txt", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "on disk" {
		t.Fatalf("read: %d %q", rec.Code, rec.Body.String())
	}

	// list — labelled as the container path the caller asked about, not the mount
	var listing fsListing
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=proj-web/data", &listing); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "hello.txt" {
		t.Fatalf("listing: %+v", listing.Entries)
	}

	// write, and confirm it landed on the real host file rather than in a tar
	if rec := doReq(t, s, "PUT",
		"/.cornus/web/fs/content?source=virtual&path=proj-web/data/new.txt", "written"); rec.Code != http.StatusOK {
		t.Fatalf("write: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(sharedDir, "new.txt")); err != nil || string(b) != "written" {
		t.Fatalf("write did not reach the host file: %q %v", b, err)
	}

	// delete
	if rec := doReq(t, s, "DELETE", "/.cornus/web/fs?source=virtual&path=proj-web/data/new.txt", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete did not remove the host file: %v", err)
	}
}

// TestPlanTransferRouting is the whole routing rule in one table, with no server, no
// daemon and no filesystem — which is the point of keeping planTransfer pure. A
// misrouted operation is otherwise invisible: it does the wrong thing quickly.
func TestPlanTransferRouting(t *testing.T) {
	client := func() fsSite { return fsSite{kind: siteClient, path: "a.txt"} }
	// A container path that a bind resolved onto the developer's disk. It is a CLIENT
	// site that still remembers its workload, which is the case fsQuery identity used
	// to get wrong.
	bound := func(w string) fsSite {
		return fsSite{kind: siteClient, workload: w, path: "a.txt", containerPath: "/data/a.txt"}
	}
	container := func(w string) fsSite {
		return fsSite{kind: siteContainer, workload: w, path: "/etc/a", containerPath: "/etc/a"}
	}
	volume := func(w string) fsSite {
		return fsSite{kind: siteServer, workload: w, volume: "v", path: "/v/a", containerPath: "/v/a"}
	}
	always := func(string) bool { return true }
	never := func(string) bool { return false }

	for _, tc := range []struct {
		name       string
		op         fsOp
		src, dst   fsSite
		serverOps  func(string) bool
		wantWhere  execAt
		wantNative bool
	}{
		{"client copy", opCopy, client(), client(), never, execHere, false},
		{"client move is a rename", opMove, client(), client(), never, execHere, true},
		{"across local roots is still one filesystem", opMove, client(), bound("web"), never, execHere, true},
		{"a bind-resolved path is a client path", opCopy, bound("web"), bound("web"), never, execHere, false},

		{"volume to volume, same workload", opCopy, volume("web"), volume("web"), always, execServer, true},
		{"volume move, same workload", opMove, volume("web"), volume("web"), always, execServer, true},
		{"volume to volume without the capability relays", opCopy, volume("web"), volume("web"), never, execRelay, false},
		{"volume to volume across workloads relays", opCopy, volume("web"), volume("api"), always, execRelay, false},

		// An image-layer path is not server-side visible: no sidecar shares the app's
		// mount namespace, so an fsop cannot reach it however capable the backend is.
		{"container to container relays even with the capability", opCopy, container("web"), container("web"), always, execRelay, false},
		{"container to volume relays", opCopy, container("web"), volume("web"), always, execRelay, false},

		{"client to container relays", opCopy, client(), container("web"), always, execRelay, false},
		{"container to client relays", opMove, container("web"), client(), always, execRelay, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planTransfer(tc.op, tc.src, tc.dst, tc.serverOps)
			if p.where != tc.wantWhere {
				t.Errorf("where = %v, want %v (why: %s)", p.where, tc.wantWhere, p.why)
			}
			if p.native != tc.wantNative {
				t.Errorf("native = %v, want %v (why: %s)", p.native, tc.wantNative, p.why)
			}
			if p.why == "" {
				t.Error("every plan must say why it chose its route")
			}
		})
	}
}

// TestPlanTransferNeverExecsInTheContainer states the H13 rule as an assertion rather
// than a comment: there is no route that runs a command inside the app image, so no
// amount of capability can produce one.
func TestPlanTransferNeverExecsInTheContainer(t *testing.T) {
	sites := []fsSite{
		{kind: siteClient, path: "a"},
		{kind: siteContainer, workload: "web", path: "/a"},
		{kind: siteServer, workload: "web", volume: "v", path: "/a"},
	}
	for _, op := range []fsOp{opCopy, opMove} {
		for _, src := range sites {
			for _, dst := range sites {
				p := planTransfer(op, src, dst, func(string) bool { return true })
				switch p.where {
				case execHere, execServer, execRelay:
				default:
					t.Fatalf("%v %v->%v produced route %v", op, src.kind, dst.kind, p.where)
				}
			}
		}
	}
}

// inodeOf identifies a file by identity rather than by content, so a test can tell a
// rename from a copy-and-delete that happens to leave the same bytes.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode on this platform")
	}
	return st.Ino
}

// TestFsMoveWithinClientIsARename proves the fast path is actually fast: the file keeps
// its inode, so no bytes moved. A copy-then-delete would pass a content assertion and
// fail this one.
func TestFsMoveWithinClientIsARename(t *testing.T) {
	s, _, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	src := filepath.Join(sharedDir, "big.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := inodeOf(t, src)

	rec := doReq(t, s, "POST", "/.cornus/web/fs/move?source=virtual&path=proj-web/data/big.bin",
		`{"to":"proj-web/data/renamed.bin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
	if got := inodeOf(t, filepath.Join(sharedDir, "renamed.bin")); got != before {
		t.Errorf("inode changed (%d -> %d): the move copied instead of renaming", before, got)
	}
}

// TestFsMoveCrossesRootsInOneRequest covers the case the browser could not express: a
// move between two different mounts. FsRename refuses to cross, so this used to be a
// copy request plus a delete request.
func TestFsMoveCrossesRootsInOneRequest(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/move?source=virtual&path=project/note.txt",
		`{"to":"proj-web/data/note.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(sharedDir, "note.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("destination: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "note.txt")); !os.IsNotExist(err) {
		t.Errorf("source survived a move: %v", err)
	}
}

// TestFsMoveLandsInsideAnExistingDirectory pins `mv x dir/` semantics, applied exactly
// once — FsCopy applies the same adjustment, and doing it twice would nest.
func TestFsMoveLandsInsideAnExistingDirectory(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sharedDir, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/move?source=virtual&path=project/note.txt",
		`{"to":"proj-web/data/inbox"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "inbox", "note.txt")); err != nil {
		t.Fatalf("did not land inside the directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "inbox", "note.txt", "note.txt")); err == nil {
		t.Error("the destination adjustment was applied twice")
	}
}

// TestFsMoveRefusesReadOnlySource is the half that is easy to forget: a move REMOVES the
// source, so a read-only source must refuse just as a read-only destination does.
func TestFsMoveRefusesReadOnlySource(t *testing.T) {
	roDir, dstDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(roDir, "keep.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil),
		fmt.Sprintf("      - %s:/ro:ro\n      - %s:/rw\n", roDir, dstDir))
	roID, rwID := rootIDForPath(s, mustEval(t, roDir)), rootIDForPath(s, mustEval(t, dstDir))
	if roID == "" || rwID == "" {
		t.Fatalf("roots not built: ro=%q rw=%q", roID, rwID)
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/move?source=virtual&path="+roID+"/keep.txt",
		fmt.Sprintf(`{"to":%q}`, rwID+"/keep.txt"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("move off a read-only mount: got %d, want 403", rec.Code)
	}
	if b, err := os.ReadFile(filepath.Join(roDir, "keep.txt")); err != nil || string(b) != "original" {
		t.Fatalf("read-only source was disturbed: %q %v", b, err)
	}
	// The refusal must come BEFORE anything happens. Checking only the status code and
	// the surviving source is not enough: without the source gate the copy still runs
	// and only the delete fails, which looks identical from the outside while leaving a
	// half-finished move on disk.
	if _, err := os.Stat(filepath.Join(dstDir, "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("a refused move still wrote to the destination: %v", err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
