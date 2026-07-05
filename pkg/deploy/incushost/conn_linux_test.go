//go:build linux

package incushost

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// fakeOp is an incus.Operation whose Wait outcome and result metadata the test
// dictates. Embedding the interface leaves every method the backend never calls
// as a nil panic, which is the point: it makes an unexpected call loud.
type fakeOp struct {
	incus.Operation
	meta    map[string]any
	waitErr error
	waited  int
}

func (o *fakeOp) Get() incusapi.Operation { return incusapi.Operation{Metadata: o.meta} }
func (o *fakeOp) Wait() error             { o.waited++; return o.waitErr }

// fakeSrv is an incus.InstanceServer standing in for the daemon client, so the
// realConn adapter's own logic (404 mapping, operation waiting, argument
// forwarding) is testable without an incusd. Only the methods realConn calls are
// implemented; anything else panics on the embedded nil interface.
type fakeSrv struct {
	incus.InstanceServer

	// Instances / Instance / InstanceState
	instances     []incusapi.Instance
	instancesType incusapi.InstanceType
	instancesErr  error
	instance      *incusapi.Instance
	instanceErr   error
	state         *incusapi.InstanceState
	stateErr      error

	// CreateInstance
	created   []incusapi.InstancesPost
	createOp  incus.Operation
	createErr error

	// UpdateInstanceState
	updatedName string
	updatedPut  incusapi.InstanceStatePut
	updateOp    incus.Operation
	updateErr   error

	// DeleteInstance
	deleted   []string
	deleteOp  incus.Operation
	deleteErr error

	// ExecInstance
	execName string
	execPost incusapi.InstanceExecPost
	execArgs *incus.InstanceExecArgs
	execOp   incus.Operation
	execErr  error

	// File API
	gotFileInst, gotFilePath string
	fileBody                 string
	fileResp                 *incus.InstanceFileResponse
	getFileErr               error
	putFileInst, putFilePath string
	putFileArgs              incus.InstanceFileArgs
	createFileErr            error

	// Console
	consoleInst string
	consoleBody string
	consoleErr  error

	disconnected int
}

func (s *fakeSrv) GetInstances(t incusapi.InstanceType) ([]incusapi.Instance, error) {
	s.instancesType = t
	return s.instances, s.instancesErr
}

func (s *fakeSrv) GetInstance(name string) (*incusapi.Instance, string, error) {
	return s.instance, "", s.instanceErr
}

func (s *fakeSrv) GetInstanceState(name string) (*incusapi.InstanceState, string, error) {
	return s.state, "", s.stateErr
}

func (s *fakeSrv) CreateInstance(req incusapi.InstancesPost) (incus.Operation, error) {
	s.created = append(s.created, req)
	return s.createOp, s.createErr
}

func (s *fakeSrv) UpdateInstanceState(name string, put incusapi.InstanceStatePut, etag string) (incus.Operation, error) {
	s.updatedName, s.updatedPut = name, put
	return s.updateOp, s.updateErr
}

func (s *fakeSrv) DeleteInstance(name string) (incus.Operation, error) {
	s.deleted = append(s.deleted, name)
	return s.deleteOp, s.deleteErr
}

func (s *fakeSrv) ExecInstance(name string, post incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	s.execName, s.execPost, s.execArgs = name, post, args
	return s.execOp, s.execErr
}

func (s *fakeSrv) GetInstanceFile(name, path string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	s.gotFileInst, s.gotFilePath = name, path
	if s.getFileErr != nil {
		return nil, nil, s.getFileErr
	}
	return io.NopCloser(strings.NewReader(s.fileBody)), s.fileResp, nil
}

func (s *fakeSrv) CreateInstanceFile(name, path string, args incus.InstanceFileArgs) error {
	s.putFileInst, s.putFilePath, s.putFileArgs = name, path, args
	return s.createFileErr
}

func (s *fakeSrv) GetInstanceConsoleLog(name string, _ *incus.InstanceConsoleLogArgs) (io.ReadCloser, error) {
	s.consoleInst = name
	if s.consoleErr != nil {
		return nil, s.consoleErr
	}
	return io.NopCloser(strings.NewReader(s.consoleBody)), nil
}

func (s *fakeSrv) Disconnect() { s.disconnected++ }

func notFound() error { return incusapi.StatusErrorf(404, "Instance not found") }

// TestRealConnListsContainersOnly pins that instance listing asks for
// containers: an Incus project can hold VMs too, and a VM must never be reported
// as a cornus deployment instance.
func TestRealConnListsContainersOnly(t *testing.T) {
	s := &fakeSrv{instances: []incusapi.Instance{{Name: "cornus-web-0"}}}
	c := &realConn{srv: s}
	got, err := c.Instances()
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if s.instancesType != incusapi.InstanceTypeContainer {
		t.Fatalf("listed instance type %q, want %q", s.instancesType, incusapi.InstanceTypeContainer)
	}
	if len(got) != 1 || got[0].Name != "cornus-web-0" {
		t.Fatalf("unexpected instances: %+v", got)
	}
	s.instancesErr = errors.New("daemon down")
	if _, err := c.Instances(); err == nil {
		t.Fatal("a listing failure must propagate, not be swallowed")
	}
}

// TestRealConnInstanceMissingIsNilNotError pins the seam's contract that a
// missing instance is (nil, nil) — the whole delete-if-exists / ErrNotFound
// mapping above it depends on 404 not surfacing as a generic error.
func TestRealConnInstanceMissingIsNilNotError(t *testing.T) {
	c := &realConn{srv: &fakeSrv{instanceErr: notFound()}}
	in, err := c.Instance("ghost")
	if err != nil || in != nil {
		t.Fatalf("missing instance: got (%v, %v), want (nil, nil)", in, err)
	}

	boom := errors.New("permission denied")
	c = &realConn{srv: &fakeSrv{instanceErr: boom}}
	if _, err := c.Instance("web"); !errors.Is(err, boom) {
		t.Fatalf("non-404 error must propagate, got %v", err)
	}

	c = &realConn{srv: &fakeSrv{instance: &incusapi.Instance{Name: "web"}}}
	in, err = c.Instance("web")
	if err != nil || in == nil || in.Name != "web" {
		t.Fatalf("existing instance: got (%v, %v)", in, err)
	}
}

// TestRealConnInstanceStateMissingIsNilNotError is the same contract for live
// state, which the stats and port-forward paths turn into deploy.ErrNotFound.
func TestRealConnInstanceStateMissingIsNilNotError(t *testing.T) {
	c := &realConn{srv: &fakeSrv{stateErr: notFound()}}
	st, err := c.InstanceState("ghost")
	if err != nil || st != nil {
		t.Fatalf("missing state: got (%v, %v), want (nil, nil)", st, err)
	}

	boom := errors.New("i/o timeout")
	c = &realConn{srv: &fakeSrv{stateErr: boom}}
	if _, err := c.InstanceState("web"); !errors.Is(err, boom) {
		t.Fatalf("non-404 error must propagate, got %v", err)
	}

	c = &realConn{srv: &fakeSrv{state: &incusapi.InstanceState{Status: "Running"}}}
	st, err = c.InstanceState("web")
	if err != nil || st == nil || st.Status != "Running" {
		t.Fatalf("existing state: got (%v, %v)", st, err)
	}
}

// TestRealConnCreateInstanceWaitsForCompletion pins that create is synchronous
// at the seam: Apply reports the deployment's status right after CreateInstance
// returns, so an un-awaited operation would report instances that do not exist
// yet. A failure to even start the operation must not be waited on.
func TestRealConnCreateInstanceWaitsForCompletion(t *testing.T) {
	op := &fakeOp{}
	s := &fakeSrv{createOp: op}
	c := &realConn{srv: s}
	if err := c.CreateInstance(incusapi.InstancesPost{Name: "cornus-web-0"}); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if op.waited != 1 {
		t.Fatalf("create waited %d times, want 1", op.waited)
	}
	if len(s.created) != 1 || s.created[0].Name != "cornus-web-0" {
		t.Fatalf("request not forwarded: %+v", s.created)
	}

	// A failure reported by the operation is the caller's error.
	failing := &fakeOp{waitErr: errors.New("no such image")}
	c = &realConn{srv: &fakeSrv{createOp: failing}}
	if err := c.CreateInstance(incusapi.InstancesPost{Name: "x"}); err == nil || !strings.Contains(err.Error(), "no such image") {
		t.Fatalf("operation failure must surface, got %v", err)
	}

	// A request that never became an operation must not be waited on.
	never := &fakeOp{}
	c = &realConn{srv: &fakeSrv{createOp: never, createErr: errors.New("bad request")}}
	if err := c.CreateInstance(incusapi.InstancesPost{Name: "x"}); err == nil {
		t.Fatal("want the request error")
	}
	if never.waited != 0 {
		t.Fatalf("waited on an operation that was never started (%d)", never.waited)
	}
}

// TestRealConnSetInstanceStateForwardsActionAndMapsMissing pins both halves of
// the lifecycle call: the action/force/timeout actually reach the daemon, and a
// 404 becomes deploy.ErrNotFound whether it is reported by the request or by the
// operation (Incus can race a delete between the two).
func TestRealConnSetInstanceStateForwardsActionAndMapsMissing(t *testing.T) {
	s := &fakeSrv{updateOp: &fakeOp{}}
	c := &realConn{srv: s}
	if err := c.SetInstanceState("cornus-web-0", "stop", true, 30); err != nil {
		t.Fatalf("SetInstanceState: %v", err)
	}
	if s.updatedName != "cornus-web-0" {
		t.Fatalf("acted on %q", s.updatedName)
	}
	if s.updatedPut.Action != "stop" || !s.updatedPut.Force || s.updatedPut.Timeout != 30 {
		t.Fatalf("state put not forwarded: %+v", s.updatedPut)
	}

	c = &realConn{srv: &fakeSrv{updateErr: notFound()}}
	if err := c.SetInstanceState("ghost", "start", false, 0); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("404 on request: want ErrNotFound, got %v", err)
	}

	c = &realConn{srv: &fakeSrv{updateOp: &fakeOp{waitErr: notFound()}}}
	if err := c.SetInstanceState("ghost", "start", false, 0); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("404 while waiting: want ErrNotFound, got %v", err)
	}

	c = &realConn{srv: &fakeSrv{updateOp: &fakeOp{waitErr: errors.New("instance is frozen")}}}
	err := c.SetInstanceState("web", "start", false, 0)
	if errors.Is(err, deploy.ErrNotFound) || err == nil {
		t.Fatalf("a non-404 failure must stay itself, got %v", err)
	}
}

// TestRealConnDeleteInstanceIsIfExists pins delete-if-exists at the seam: a
// missing instance is success whether the 404 arrives from the request or from
// the operation, while any other failure is reported.
func TestRealConnDeleteInstanceIsIfExists(t *testing.T) {
	op := &fakeOp{}
	s := &fakeSrv{deleteOp: op}
	c := &realConn{srv: s}
	if err := c.DeleteInstance("cornus-web-0"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if len(s.deleted) != 1 || s.deleted[0] != "cornus-web-0" || op.waited != 1 {
		t.Fatalf("delete not forwarded/awaited: %v waited=%d", s.deleted, op.waited)
	}

	never := &fakeOp{}
	c = &realConn{srv: &fakeSrv{deleteOp: never, deleteErr: notFound()}}
	if err := c.DeleteInstance("ghost"); err != nil {
		t.Fatalf("404 on request should be a no-op success, got %v", err)
	}
	if never.waited != 0 {
		t.Fatalf("waited on an operation that was never started (%d)", never.waited)
	}

	c = &realConn{srv: &fakeSrv{deleteOp: &fakeOp{waitErr: notFound()}}}
	if err := c.DeleteInstance("ghost"); err != nil {
		t.Fatalf("404 while waiting should be a no-op success, got %v", err)
	}

	c = &realConn{srv: &fakeSrv{deleteOp: &fakeOp{waitErr: errors.New("volume busy")}}}
	if err := c.DeleteInstance("web"); err == nil {
		t.Fatal("a non-404 delete failure must be reported")
	}
}

// TestRealConnExecReturnsLiveOperation pins the documented exception to the
// "already waited" seam rule: exec hands back the live operation (ExecStart
// needs its control channel and exit metadata) instead of waiting internally.
func TestRealConnExecReturnsLiveOperation(t *testing.T) {
	op := &fakeOp{}
	s := &fakeSrv{execOp: op}
	c := &realConn{srv: s}
	args := &incus.InstanceExecArgs{}
	got, err := c.Exec("cornus-web-0", incusapi.InstanceExecPost{Command: []string{"sh"}}, args)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got != incus.Operation(op) {
		t.Fatal("Exec must return the daemon's operation unwrapped")
	}
	if op.waited != 0 {
		t.Fatalf("Exec must not wait; waited %d", op.waited)
	}
	if s.execName != "cornus-web-0" || len(s.execPost.Command) != 1 || s.execArgs != args {
		t.Fatalf("exec request not forwarded: %q %+v", s.execName, s.execPost)
	}
}

// TestRealConnFileAndConsolePassThrough pins that the cp and logs paths address
// the instance and path they were given and hand back the daemon's stream
// untouched (the framing is applied a layer up, not here).
func TestRealConnFileAndConsolePassThrough(t *testing.T) {
	s := &fakeSrv{
		fileBody:    "hello",
		fileResp:    &incus.InstanceFileResponse{Type: "file", Mode: 0o644},
		consoleBody: "console line\n",
	}
	c := &realConn{srv: s}

	rc, resp, err := c.GetFile("cornus-web-0", "/etc/hi")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "hello" || resp.Type != "file" {
		t.Fatalf("GetFile returned %q / %+v", body, resp)
	}
	if s.gotFileInst != "cornus-web-0" || s.gotFilePath != "/etc/hi" {
		t.Fatalf("GetFile addressed %q %q", s.gotFileInst, s.gotFilePath)
	}

	if err := c.CreateFile("cornus-web-0", "/etc/out", incus.InstanceFileArgs{Type: "file", Mode: 0o600}); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if s.putFileInst != "cornus-web-0" || s.putFilePath != "/etc/out" || s.putFileArgs.Mode != 0o600 {
		t.Fatalf("CreateFile forwarded %q %q %+v", s.putFileInst, s.putFilePath, s.putFileArgs)
	}

	crc, err := c.ConsoleLog("cornus-web-0")
	if err != nil {
		t.Fatalf("ConsoleLog: %v", err)
	}
	cbody, _ := io.ReadAll(crc)
	crc.Close()
	if string(cbody) != "console line\n" || s.consoleInst != "cornus-web-0" {
		t.Fatalf("ConsoleLog returned %q for %q", cbody, s.consoleInst)
	}
}

// TestBackendCloseDisconnectsTheDaemonClient pins that closing the backend
// releases the daemon connection (a leaked unix socket per backend would
// accumulate across server-side backend churn), and that Name is the identifier
// the deploy layer and status output key off.
func TestBackendCloseDisconnectsTheDaemonClient(t *testing.T) {
	s := &fakeSrv{}
	b := &Backend{conn: &realConn{srv: s}}
	if b.Name() != "incus" {
		t.Fatalf("Name = %q, want incus", b.Name())
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.disconnected != 1 {
		t.Fatalf("Disconnect called %d times, want 1", s.disconnected)
	}
}

// TestNewReportsAnActionableErrorWhenTheSocketIsAbsent pins the operator-facing
// failure mode: an unreachable daemon must name the socket that was tried and
// the knob that changes it, not just bubble up "no such file or directory".
func TestNewReportsAnActionableErrorWhenTheSocketIsAbsent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nope.socket")
	_, err := New(Config{Socket: sock, Project: "default"})
	if err == nil {
		t.Fatal("connecting to a nonexistent socket must fail")
	}
	if !strings.Contains(err.Error(), sock) || !strings.Contains(err.Error(), "CORNUS_INCUS_SOCKET") {
		t.Fatalf("error should name the socket and the env knob, got: %v", err)
	}
}

// incusStub serves the handful of Incus REST endpoints New's handshake and a
// subsequent instance listing touch, over a unix socket in a temp dir (no
// daemon, no root, no network). It records the project query parameter so a test
// can prove the backend's calls are scoped to the configured project.
type incusStub struct {
	listedProjects []string
}

func (s *incusStub) serve(t *testing.T) string {
	t.Helper()
	// Keep the path short: a unix socket address is capped near 108 bytes.
	dir, err := os.MkdirTemp("", "incusstub")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	mux := http.NewServeMux()
	sync := func(w http.ResponseWriter, v any) {
		raw, _ := json.Marshal(v)
		_ = json.NewEncoder(w).Encode(incusapi.Response{
			Type: incusapi.SyncResponse, Status: "Success", StatusCode: 200, Metadata: raw,
		})
	}
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		sync(w, incusapi.Server{})
	})
	mux.HandleFunc("/1.0/instances", func(w http.ResponseWriter, r *http.Request) {
		s.listedProjects = append(s.listedProjects, r.URL.Query().Get("project"))
		sync(w, []incusapi.Instance{})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestNewScopesCallsToTheConfiguredProjectAndCarriesOptions drives the real
// Incus client against a canned REST stub on a unix socket to prove the two
// things New is responsible for: every subsequent call is scoped to the
// configured Incus project (instances of other projects must never be seen as
// cornus deployments), and the options are carried onto the backend.
func TestNewScopesCallsToTheConfiguredProjectAndCarriesOptions(t *testing.T) {
	stub := &incusStub{}
	sock := stub.serve(t)

	reg := remotecompanion.NewRegistry()
	got, err := New(
		Config{Socket: sock, Project: "cornus-proj", DataDir: "/var/lib/cornus"},
		WithPolicy(hostpolicy.Permissive()),
		WithRemote(true),
		WithAgentImage("localhost:5000/cornus:dev"),
		WithCompanionRegistry(reg),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, ok := got.(*Backend)
	if !ok {
		t.Fatalf("New returned %T", got)
	}
	t.Cleanup(func() { _ = b.Close() })

	if b.project != "cornus-proj" || b.dataDir != "/var/lib/cornus" {
		t.Fatalf("config not carried: project=%q dataDir=%q", b.project, b.dataDir)
	}
	if !b.remote || b.agentImage != "localhost:5000/cornus:dev" || b.companions != reg {
		t.Fatalf("options not carried: %+v", b)
	}
	if b.execs == nil {
		t.Fatal("exec registry must be initialised or ExecCreate panics")
	}

	// The connection is project-scoped: the listing lands with ?project=.
	if _, err := b.conn.Instances(); err != nil {
		t.Fatalf("Instances over the stub: %v", err)
	}
	if len(stub.listedProjects) != 1 || stub.listedProjects[0] != "cornus-proj" {
		t.Fatalf("instance listing was not project-scoped: %v", stub.listedProjects)
	}
}
