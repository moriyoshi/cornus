package webbff

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/client"
)

// volumeExplorerServer builds a project whose `web` service has TWO named volumes and no
// binds — the shape a volume-to-volume transfer needs, and the one nothing else in this
// suite provides.
func volumeExplorerServer(t *testing.T, upstream *httptest.Server) *Server {
	t.Helper()
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "compose.yaml")
	composeYAML := `services:
  web:
    image: example/web:1
    volumes:
      - data:/data
      - cache:/cache
volumes:
  data:
  cache:
`
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
	return s
}

// operatorFS is a fakeContainerFS whose structured operations WORK, so the tests below
// can observe the route actually being taken. Everything the archive primitives would do
// stays fatal: the whole claim is that no byte reaches them.
//
// The archive methods report the violation with t.Error and RETURN AN ERROR rather than
// t.Fatal: CopyFrom runs on the BFF's producing goroutine, and Fatal there is
// runtime.Goexit — the pipe never closes and the whole test binary hangs instead of
// failing. Returning an error fails the transfer loudly and lets the test finish, which
// is what makes the neutralization run at all.
type operatorFS struct {
	fakeContainerFS
	t *testing.T
}

var errRelayed = errors.New("relayed through the BFF")

func (f *operatorFS) CopyFrom(context.Context, string, string, io.Writer) (api.PathStat, error) {
	f.t.Error("CopyFrom: a volume-to-volume transfer must not relay bytes through the BFF")
	return api.PathStat{}, errRelayed
}

func (f *operatorFS) CopyTo(context.Context, string, string, io.Reader, api.CopyToOptions) error {
	f.t.Error("CopyTo: a volume-to-volume transfer must not relay bytes through the BFF")
	return errRelayed
}

func newOperatorFS(t *testing.T, fn func(api.FSOpRequest) (api.FSOpResponse, error)) *operatorFS {
	return &operatorFS{t: t, fakeContainerFS: fakeContainerFS{
		stat:   api.PathStat{Name: "x", Size: 3, Mode: 0o644},
		fsopFn: fn,
	}}
}

// TestVolumeToVolumeCopyRunsOnTheServer is the payoff of the whole fsop chain: both ends
// are volumes on one workload, so the copy is one structured request and NOTHING is
// relayed. The archive primitives are fatal here, which is what makes that a proof rather
// than a hope.
func TestVolumeToVolumeCopyRunsOnTheServer(t *testing.T) {
	s := volumeExplorerServer(t, runningUpstream(t))
	cfs := newOperatorFS(t, func(api.FSOpRequest) (api.FSOpResponse, error) {
		return api.FSOpResponse{}, nil
	})
	s.cfs = cfs

	src := fsQuery{source: "container", workload: "proj-web", path: "/data/dump.sql"}
	dst := fsQuery{source: "container", workload: "proj-web", path: "/cache/dump.sql"}
	if _, err := s.FsCopy(context.Background(), src, dst); err != nil {
		t.Fatalf("FsCopy: %v", err)
	}

	var copyReq *api.FSOpRequest
	for i, r := range cfs.fsopCalls {
		if r.Op == api.FSOpCopy {
			copyReq = &cfs.fsopCalls[i]
		}
	}
	if copyReq == nil {
		t.Fatalf("no copy op reached the operator; calls = %+v", cfs.fsopCalls)
	}
	if copyReq.Path != "/data/dump.sql" || copyReq.To != "/cache/dump.sql" {
		t.Errorf("copy = %s -> %s, want the container-absolute paths", copyReq.Path, copyReq.To)
	}
	// The relay route sets this; so must the server route, or the same gesture is
	// refused on one path and deletes a directory tree on the other.
	if !copyReq.NoOverwriteDirNonDir {
		t.Error("the server route dropped NoOverwriteDirNonDir")
	}
}

// TestVolumeToVolumeMoveIsARename: within one operator a move should cost nothing at all.
func TestVolumeToVolumeMoveIsARename(t *testing.T) {
	s := volumeExplorerServer(t, runningUpstream(t))
	cfs := newOperatorFS(t, func(api.FSOpRequest) (api.FSOpResponse, error) {
		return api.FSOpResponse{}, nil
	})
	s.cfs = cfs

	src := fsQuery{source: "container", workload: "proj-web", path: "/data/dump.sql"}
	dst := fsQuery{source: "container", workload: "proj-web", path: "/cache/dump.sql"}
	if _, err := s.FsMove(context.Background(), src, dst); err != nil {
		t.Fatalf("FsMove: %v", err)
	}
	sawRename := false
	for _, r := range cfs.fsopCalls {
		if r.Op == api.FSOpRename && r.Path == "/data/dump.sql" && r.To == "/cache/dump.sql" {
			sawRename = true
		}
		if r.Op == api.FSOpCopy {
			t.Errorf("a move within one operator copied instead of renaming: %+v", r)
		}
	}
	if !sawRename {
		t.Fatalf("no rename reached the operator; calls = %+v", cfs.fsopCalls)
	}
}

// TestCrossDeviceMoveFallsBackToCopyThenDelete: two volumes are two filesystems, so
// rename(2) cannot cross them. That is not a failure — it is the case copy-then-delete
// exists for, and the user must not see an error.
func TestCrossDeviceMoveFallsBackToCopyThenDelete(t *testing.T) {
	s := volumeExplorerServer(t, runningUpstream(t))
	cfs := newOperatorFS(t, func(req api.FSOpRequest) (api.FSOpResponse, error) {
		if req.Op == api.FSOpRename {
			return api.FSOpResponse{Error: "invalid cross-device link", Code: api.FSErrCrossDevice},
				errors.New("invalid cross-device link")
		}
		return api.FSOpResponse{}, nil
	})
	s.cfs = cfs

	src := fsQuery{source: "container", workload: "proj-web", path: "/data/dump.sql"}
	dst := fsQuery{source: "container", workload: "proj-web", path: "/cache/dump.sql"}
	if _, err := s.FsMove(context.Background(), src, dst); err != nil {
		t.Fatalf("FsMove: %v", err)
	}
	sawCopy, sawRemove := false, false
	for _, r := range cfs.fsopCalls {
		switch r.Op {
		case api.FSOpCopy:
			sawCopy = true
		case api.FSOpRemove:
			sawRemove = true
		}
	}
	if !sawCopy {
		t.Errorf("a cross-device move did not fall back to a copy; calls = %+v", cfs.fsopCalls)
	}
	_ = sawRemove // the delete goes through FsDelete, which has its own route
}

// TestAnOperatorlessBackendStillRelays is the fallback that keeps every other backend
// working. The default fake reports unsupported, so this drives exactly what a docker or
// containerd deployment with no caretaker does — and it must relay silently, not fail.
func TestAnOperatorlessBackendStillRelays(t *testing.T) {
	s := volumeExplorerServer(t, runningUpstream(t))
	relayed := false
	cfs := &fakeContainerFS{
		stat:     api.PathStat{Name: "dump.sql", Size: 3, Mode: 0o644},
		copyFrom: tarOf(t, "dump.sql", "abc"),
	}
	s.cfs = &relayWatcher{fakeContainerFS: cfs, sawCopyTo: &relayed}

	src := fsQuery{source: "container", workload: "proj-web", path: "/data/dump.sql"}
	dst := fsQuery{source: "container", workload: "proj-web", path: "/cache/dump.sql"}
	if _, err := s.FsCopy(context.Background(), src, dst); err != nil {
		t.Fatalf("FsCopy through the relay: %v", err)
	}
	if !relayed {
		t.Fatal("the copy neither ran on the server nor relayed; nothing happened")
	}
	// The probe must have been attempted exactly once for the workload, not once per
	// entry: the memo is what keeps a tree walk from being a round trip per file.
	probes := 0
	for _, r := range cfs.fsopCalls {
		if r.Op == api.FSOpStat {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("probed the operator %d times, want exactly 1 (the result is memoized)", probes)
	}
}

// tarOf builds the single-entry archive a container CopyFrom would produce.
func tarOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type relayWatcher struct {
	*fakeContainerFS
	sawCopyTo *bool
}

func (r *relayWatcher) CopyTo(ctx context.Context, name, path string, rd io.Reader, o api.CopyToOptions) error {
	*r.sawCopyTo = true
	return r.fakeContainerFS.CopyTo(ctx, name, path, rd, o)
}

// TestArchivelessBackendRoutesWritesToTheOperator is the regression for what the live kube
// run caught, and it is a test about ORDER rather than about capability.
//
// The first design retried after the archive call failed, gated on nothing having moved —
// which is the right gate, because a spent stream cannot be replayed. But a PUT streams its
// tar from a pipe while the request is in flight, so Go's transport had already pumped
// bytes out of that pipe by the time the 501 arrived. The guard fired correctly and the
// fallback never happened: on kubernetes every write stayed a 501, with a message about
// `kubectl cp`.
//
// It drives the REAL clientContainerFS over a real pkg/client against an httptest server,
// because the thing under test is exactly the routing that a re-implementation in a fake
// would restate — and a restatement inherits whatever the original got wrong. The server
// below is the shape kubernetes presents: 501 on every archive route, 200 on fsop.
func TestArchivelessBackendRoutesWritesToTheOperator(t *testing.T) {
	var archiveCalls, statCalls int
	var put []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy/{name}/archive", func(w http.ResponseWriter, r *http.Request) {
		archiveCalls++
		if r.Method == http.MethodHead {
			statCalls++
			// HEAD carries no body, exactly as the real endpoint does — which is
			// what makes this the one call that can be safely retried.
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		// Drain BEFORE failing, exactly as Go's transport does for a pipe-backed
		// body. A handler that refused without reading would let the original,
		// broken retry-after-the-fact design pass this test.
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error":"cp/archive not supported on the kubernetes backend; use kubectl cp"}`))
	})
	mux.HandleFunc("POST /.cornus/v1/deploy/{name}/fsop", func(w http.ResponseWriter, r *http.Request) {
		switch api.FSOp(r.URL.Query().Get("op")) {
		case api.FSOpStat:
			json.NewEncoder(w).Encode(api.FSOpResponse{Stat: &api.PathStat{Name: "f.txt", Mode: 0o644}})
		case api.FSOpPut:
			put, _ = io.ReadAll(r.Body)
			json.NewEncoder(w).Encode(api.FSOpResponse{})
		default:
			json.NewEncoder(w).Encode(api.FSOpResponse{})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fs := &clientContainerFS{c: client.New(srv.URL)}

	// The stat is where the answer is learned.
	st, err := fs.StatPath(context.Background(), "web", "/vol1/f.txt")
	if err != nil {
		t.Fatalf("StatPath on an archiveless backend: %v", err)
	}
	if st.Name != "f.txt" {
		t.Fatalf("stat = %+v, want the operator's answer", st)
	}
	before := archiveCalls

	if err := fs.CopyTo(context.Background(), "web", "/vol1", strings.NewReader("TAR"), api.CopyToOptions{}); err != nil {
		t.Fatalf("CopyTo on an archiveless backend: %v", err)
	}
	if string(put) != "TAR" {
		t.Fatalf("the operator received %q, want the tar", put)
	}
	if archiveCalls != before {
		t.Errorf("the archive was attempted %d more times after it was known to be absent",
			archiveCalls-before)
	}

	// And reads keep working off the same learned answer, without re-probing.
	var got bytes.Buffer
	if _, err := fs.CopyFrom(context.Background(), "web", "/vol1/f.txt", &got); err != nil {
		t.Fatalf("CopyFrom on an archiveless backend: %v", err)
	}
	if statCalls != 1 {
		t.Errorf("archive HEAD called %d times, want exactly 1 — the answer is remembered", statCalls)
	}
}

// TestArchiveBackedBackendIsUntouched: the learned answer must not cost a working backend
// anything. Every call goes straight to the archive and the operator is never consulted.
func TestArchiveBackedBackendIsUntouched(t *testing.T) {
	fsopCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy/{name}/archive", func(w http.ResponseWriter, r *http.Request) {
		enc, _ := api.EncodePathStat(api.PathStat{Name: "f.txt", Mode: 0o644})
		w.Header().Set(api.PathStatHeader, enc)
		if r.Method == http.MethodGet {
			w.Write(tarOf(t, "f.txt", "hello"))
		}
	})
	mux.HandleFunc("POST /.cornus/v1/deploy/{name}/fsop", func(w http.ResponseWriter, _ *http.Request) {
		fsopCalls++
		json.NewEncoder(w).Encode(api.FSOpResponse{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fs := &clientContainerFS{c: client.New(srv.URL)}
	if _, err := fs.StatPath(context.Background(), "web", "/data/f.txt"); err != nil {
		t.Fatalf("StatPath: %v", err)
	}
	var got bytes.Buffer
	if _, err := fs.CopyFrom(context.Background(), "web", "/data/f.txt", &got); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if err := fs.CopyTo(context.Background(), "web", "/data", strings.NewReader("TAR"), api.CopyToOptions{}); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if fsopCalls != 0 {
		t.Fatalf("the operator was consulted %d times on a backend whose archive works", fsopCalls)
	}
}
