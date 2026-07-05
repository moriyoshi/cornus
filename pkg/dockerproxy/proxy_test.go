package dockerproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
)

// fakeAttacher records the specs it was asked to deploy and stays "connected"
// (blocking on ctx) until the proxy stops the session, mirroring a real
// deploy-attach.
type fakeAttacher struct {
	mu          sync.Mutex
	attached    []api.DeploySpec
	archives    map[string][]byte // deployment name -> last tar received by CopyTo
	execCreated map[string]string // exec id -> deployment name
	// output, when set, makes ExecStart/Attach emit these bytes IMMEDIATELY
	// (after outputDelay) independent of any stdin, then close — modelling real
	// Docker, whose stdout does not depend on stdin. Empty means echo behaviour.
	output      string
	outputDelay time.Duration

	// resizedExec records the last ExecResize call so tests can assert the
	// proxy forwarded the exec id and dimensions.
	resizedExec string
	resizedH    uint
	resizedW    uint

	// forwarded records the container ports PortForward tunnels were opened to.
	forwarded []int

	// selfExit, when non-nil, makes DeployAttach return (the workload exits on
	// its own) as soon as it is closed, instead of blocking until the proxy
	// cancels the session. It models a detached container whose process exits
	// while no client is blocked in wait.
	selfExit chan struct{}

	// logsErr / statsErr, when set, are returned by Logs/Stats. With
	// logsPartialWrite, Logs first writes its normal frames and THEN returns
	// logsErr, modelling a stream that fails after output has begun.
	logsErr          error
	statsErr         error
	logsPartialWrite bool

	// statErr / copyFromErr, when set, are returned by StatPath / CopyFrom
	// before any output (modelling a missing deployment or path on the archive
	// endpoints).
	statErr     error
	copyFromErr error

	// registryHost, when set, is the builtin registry the push handler reads
	// from (an in-process cornus registry in tests). It makes the fake satisfy
	// the registryClient capability.
	registryHost string
}

// registryClient capability (see push.go): the fake points the proxy at an
// in-process, plaintext, unauthenticated cornus registry.
func (f *fakeAttacher) Host() string                         { return f.registryHost }
func (f *fakeAttacher) RegistryToken() string                { return "" }
func (f *fakeAttacher) RegistrySecure() bool                 { return false }
func (f *fakeAttacher) RegistryTransport() http.RoundTripper { return http.DefaultTransport }

func (f *fakeAttacher) DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error {
	f.mu.Lock()
	f.attached = append(f.attached, spec)
	selfExit := f.selfExit
	f.mu.Unlock()
	events(deploywire.Event{Ready: true, Status: &api.DeployStatus{
		Name:      spec.Name,
		Image:     spec.Image,
		Instances: []api.InstanceStatus{{ID: "x", State: "running", Running: true}},
	}})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-selfExit:
		// The workload exited on its own; the session ends without a cancel.
		return nil
	}
}

func (f *fakeAttacher) Status(_ context.Context, name string) (api.DeployStatus, error) {
	return api.DeployStatus{Name: name}, nil
}

// Logs writes two stdcopy-framed lines (stdout + stderr) for the deployment,
// honoring the framing contract the proxy relies on.
func (f *fakeAttacher) Logs(_ context.Context, name string, _ api.LogOptions, w io.Writer) error {
	if f.logsErr != nil && !f.logsPartialWrite {
		return f.logsErr
	}
	if _, err := stdcopy.NewStdWriter(w, stdcopy.Stdout).Write([]byte("out: " + name + "\n")); err != nil {
		return err
	}
	if _, err := stdcopy.NewStdWriter(w, stdcopy.Stderr).Write([]byte("err: " + name + "\n")); err != nil {
		return err
	}
	return f.logsErr
}

// Stats writes a single canned Docker-format stats object for the deployment.
func (f *fakeAttacher) Stats(_ context.Context, name string, _ api.StatsOptions, w io.Writer) error {
	if f.statsErr != nil {
		return f.statsErr
	}
	_, err := io.WriteString(w, `{"name":"`+name+`","read":"now"}`)
	return err
}

func (f *fakeAttacher) StatPath(_ context.Context, name, path string) (api.PathStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statErr != nil {
		return api.PathStat{}, f.statErr
	}
	return api.PathStat{Name: path, Size: int64(len(f.archives[name])), Mode: 0o644}, nil
}

func (f *fakeAttacher) CopyFrom(_ context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	f.mu.Lock()
	b := f.archives[name]
	err := f.copyFromErr
	f.mu.Unlock()
	if err != nil {
		return api.PathStat{}, err
	}
	if _, err := w.Write(b); err != nil {
		return api.PathStat{}, err
	}
	return api.PathStat{Name: path, Size: int64(len(b)), Mode: 0o644}, nil
}

func (f *fakeAttacher) CopyTo(_ context.Context, name, _ string, r io.Reader, _ api.CopyToOptions) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.archives == nil {
		f.archives = map[string][]byte{}
	}
	f.archives[name] = b
	f.mu.Unlock()
	return nil
}

// ExecCreate records the deployment and returns a canned exec id.
func (f *fakeAttacher) ExecCreate(_ context.Context, name string, _ api.ExecConfig) (string, error) {
	f.mu.Lock()
	if f.execCreated == nil {
		f.execCreated = map[string]string{}
	}
	id := "exec-" + name
	f.execCreated[id] = name
	f.mu.Unlock()
	return id, nil
}

// ExecStart returns a net.Conn modelling the backend exec stream: either an
// immediate-output stream (when f.output is set) or a one-shot echo (default),
// which the proxy bridges to the hijacked docker CLI conn.
func (f *fakeAttacher) ExecStart(_ context.Context, _ string, _ api.ExecStartConfig) (net.Conn, error) {
	return f.streamConn(), nil
}

func (f *fakeAttacher) ExecInspect(_ context.Context, _ string) (api.ExecState, error) {
	return api.ExecState{Running: true, ExitCode: 7, Pid: 99}, nil
}

// ExecResize records the exec id and dimensions the proxy forwarded.
func (f *fakeAttacher) ExecResize(_ context.Context, execID string, height, width uint) error {
	f.mu.Lock()
	f.resizedExec = execID
	f.resizedH = height
	f.resizedW = width
	f.mu.Unlock()
	return nil
}

func (f *fakeAttacher) Attach(_ context.Context, _ string, _ api.AttachConfig) (net.Conn, error) {
	return f.streamConn(), nil
}

// PortForward records the dialed container port and returns an echoing tunnel.
func (f *fakeAttacher) PortForward(_ context.Context, name string, port int, proto string) (net.Conn, error) {
	f.mu.Lock()
	f.forwarded = append(f.forwarded, port)
	f.mu.Unlock()
	near, far := net.Pipe()
	go func() {
		defer far.Close()
		_, _ = io.Copy(far, far)
	}()
	return near, nil
}

func (f *fakeAttacher) streamConn() net.Conn {
	if f.output != "" {
		return outputConn(f.output, f.outputDelay)
	}
	return echoConn()
}

// echoConn returns a net.Conn that echoes one read of input back and then closes
// its side (a shell that exits after handling its input), so the bridge tears
// down cleanly once the echo is delivered.
func echoConn() net.Conn {
	a, b := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		if n, err := b.Read(buf); n > 0 && err == nil {
			_, _ = b.Write(buf[:n])
		}
		_ = b.Close()
	}()
	return a
}

// outputConn returns a net.Conn that writes payload (after delay) independent of
// any input, then closes — modelling Docker emitting stdout for a non-interactive
// exec whose stdin is empty. The delay lets a fast stdin-EOF race the output so
// the old full-close-on-either-EOF bridge would truncate it.
func outputConn(payload string, delay time.Duration) net.Conn {
	a, b := net.Pipe()
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = io.WriteString(b, payload)
		_ = b.Close()
	}()
	return a
}

func (f *fakeAttacher) specFor(name string) *api.DeploySpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.attached {
		if f.attached[i].Name == name {
			return &f.attached[i]
		}
	}
	return nil
}

func TestPingAndVersion(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()

	// Versioned path must route (prefix stripped) and set the negotiation header.
	resp, err := http.Get(srv.URL + "/v1.43/_ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Api-Version") == "" {
		t.Fatalf("ping status=%d api-version=%q", resp.StatusCode, resp.Header.Get("Api-Version"))
	}
}

func TestContainerRunLifecycle(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	// create (docker run buffers here).
	body := createRequest{
		Image: "localhost:5000/web:v1",
		Env:   []string{"LOG=info"},
		HostConfig: hostConfig{
			Binds:        []string{"/local/conf:/etc/app:ro"},
			PortBindings: map[string][]portBinding{"80/tcp": {{HostPort: "8080"}}},
		},
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/v1.43/containers/create?name=web", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.ID == "" {
		t.Fatal("no container id returned")
	}
	// No remote contact until start.
	if fa.specFor("web") != nil {
		t.Fatal("create must not deploy (buffer only)")
	}

	// start -> deploy-attach opens and the spec is what we translated.
	resp = do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start status = %d", resp.StatusCode)
	}
	spec := fa.specFor("web")
	if spec == nil {
		t.Fatal("start did not deploy")
	}
	if spec.Image != "localhost:5000/web:v1" || spec.Env["LOG"] != "info" {
		t.Errorf("deployed spec = %+v", spec)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != "/local/conf" || !spec.Mounts[0].ReadOnly {
		t.Errorf("deployed mounts = %+v", spec.Mounts)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].Host != 8080 {
		t.Errorf("deployed ports = %+v", spec.Ports)
	}

	// ps shows it running.
	resp = do(t, http.MethodGet, srv.URL+"/containers/json?all=1", nil)
	var list []containerSummary
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].State != "running" || list[0].Names[0] != "/web" {
		t.Fatalf("ps = %+v", list)
	}

	// inspect.
	resp = do(t, http.MethodGet, srv.URL+"/containers/web/json", nil)
	var cj containerJSON
	_ = json.NewDecoder(resp.Body).Decode(&cj)
	resp.Body.Close()
	if !cj.State.Running || cj.Config.Image != "localhost:5000/web:v1" {
		t.Fatalf("inspect = %+v", cj)
	}

	// rm tears the session down.
	resp = do(t, http.MethodDelete, srv.URL+"/containers/"+cr.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rm status = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, srv.URL+"/containers/web/json", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("inspect after rm = %d, want 404", resp.StatusCode)
	}
}

// TestContainerPortForward covers the docker -p publication: start opens a
// local listener per PortBinding tunneled through the attacher, stop releases
// it, and a restart re-publishes.
func TestContainerPortForward(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	// Pick a free local port for the host side of the binding.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", hostPort)

	b, _ := json.Marshal(createRequest{
		Image: "img",
		HostConfig: hostConfig{
			PortBindings: map[string][]portBinding{"80/tcp": {{HostPort: strconv.Itoa(hostPort)}}},
		},
	})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	roundTrip := func() {
		t.Helper()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		defer c.Close()
		if _, err := io.WriteString(c, "ping"); err != nil {
			t.Fatalf("write: %v", err)
		}
		buf := make([]byte, 4)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("echo = %q err=%v", buf, err)
		}
	}

	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	roundTrip()
	fa.mu.Lock()
	fwd := append([]int(nil), fa.forwarded...)
	fa.mu.Unlock()
	if len(fwd) != 1 || fwd[0] != 80 {
		t.Fatalf("forwarded ports = %v, want [80]", fwd)
	}

	// stop releases the local listener.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/stop", nil).Body.Close()
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("listener still accepting after stop")
	}

	// start again re-publishes.
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	roundTrip()

	// remove releases it for good.
	do(t, http.MethodDelete, srv.URL+"/containers/"+cr.ID, nil).Body.Close()
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("listener still accepting after rm")
	}
}

// TestLabelFilter confirms docker ps label filtering (used by compose discovery).
func TestLabelFilter(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	create := func(name string, labels map[string]string) string {
		b, _ := json.Marshal(createRequest{Image: "img", Labels: labels})
		resp, _ := http.Post(srv.URL+"/containers/create?name="+name, "application/json", bytes.NewReader(b))
		var cr createResponse
		_ = json.NewDecoder(resp.Body).Decode(&cr)
		resp.Body.Close()
		do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
		return cr.ID
	}
	create("a", map[string]string{"com.docker.compose.project": "proj1"})
	create("b", map[string]string{"com.docker.compose.project": "proj2"})

	resp := do(t, http.MethodGet, srv.URL+`/containers/json?filters={"label":["com.docker.compose.project=proj1"]}`, nil)
	var list []containerSummary
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Names[0] != "/a" {
		t.Fatalf("filtered ps = %+v", list)
	}

	// Modern docker CLIs (API >= 1.22) send the map-of-bools filter encoding;
	// the devcontainer CLI locates its container exclusively this way.
	resp = do(t, http.MethodGet, srv.URL+`/containers/json?filters={"label":{"com.docker.compose.project=proj2":true}}`, nil)
	list = nil
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Names[0] != "/b" {
		t.Fatalf("map-form filtered ps = %+v, want just /b", list)
	}

	// A map-form filter matching nothing must return an empty list, not all.
	resp = do(t, http.MethodGet, srv.URL+`/containers/json?filters={"label":{"com.docker.compose.project=nope":true}}`, nil)
	list = nil
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Fatalf("non-matching map-form filter ps = %+v, want empty", list)
	}
}

// TestContainerLogs confirms GET /containers/{id}/logs streams the backend's
// stdcopy-framed log frames through unchanged with the docker raw-stream type.
func TestContainerLogs(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	resp = do(t, http.MethodGet, srv.URL+"/containers/"+cr.ID+"/logs?stdout=1&stderr=1&follow=1", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.docker.raw-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Body); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if stdout.String() != "out: web\n" || stderr.String() != "err: web\n" {
		t.Fatalf("logs stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestContainerStats confirms GET /containers/{id}/stats streams the backend's
// Docker-format stats JSON through unchanged with an application/json type.
func TestContainerStats(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	resp = do(t, http.MethodGet, srv.URL+"/containers/"+cr.ID+"/stats?stream=0", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`"read":"now"`)) {
		t.Fatalf("stats body = %q", body)
	}
}

// createAndStart creates and starts a container named "web" on srv and returns
// its id (shared setup for the logs/stats error tests).
func createAndStart(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, err := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	return cr.ID
}

// TestContainerLogsBackendErrorBeforeOutput confirms a backend error raised
// before any log byte becomes a docker-shaped error response (status mapped
// from the error message) instead of an empty 200.
func TestContainerLogsBackendErrorBeforeOutput(t *testing.T) {
	// The attacher is a cornus server client, so its errors carry the server's
	// status text plus the backend message.
	msg := `404 Not Found: dockerhost: no instances for deployment "web"`
	fa := &fakeAttacher{logsErr: errors.New(msg)}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()
	id := createAndStart(t, srv)

	resp := do(t, http.MethodGet, srv.URL+"/containers/"+id+"/logs?stdout=1", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("logs status = %d, want 404", resp.StatusCode)
	}
	var e map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if e["message"] != msg {
		t.Fatalf("message = %q, want %q", e["message"], msg)
	}
}

// TestContainerStatsBackendErrorBeforeOutput confirms an unsupported-stats
// backend error (the kubernetes backend, relayed through the cornus server)
// yields a 501 docker-shaped error instead of an empty 200.
func TestContainerStatsBackendErrorBeforeOutput(t *testing.T) {
	msg := "501 Not Implemented: stats not supported on the kubernetes backend (needs metrics-server); use kubectl top"
	fa := &fakeAttacher{statsErr: errors.New(msg)}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()
	id := createAndStart(t, srv)

	resp := do(t, http.MethodGet, srv.URL+"/containers/"+id+"/stats?stream=0", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("stats status = %d, want 501", resp.StatusCode)
	}
	var e map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if e["message"] != msg {
		t.Fatalf("message = %q, want %q", e["message"], msg)
	}
}

// TestContainerLogsMidStreamBackendError confirms a backend error AFTER output
// has begun leaves the committed 200 and the partial frames intact.
func TestContainerLogsMidStreamBackendError(t *testing.T) {
	fa := &fakeAttacher{logsErr: errors.New("stream broke"), logsPartialWrite: true}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()
	id := createAndStart(t, srv)

	resp := do(t, http.MethodGet, srv.URL+"/containers/"+id+"/logs?stdout=1&stderr=1", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.docker.raw-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Body); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if stdout.String() != "out: web\n" || stderr.String() != "err: web\n" {
		t.Fatalf("partial logs stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestContainerArchive round-trips docker cp through the proxy: PUT a small tar,
// GET it back (checking the tar body and the X-Docker-Container-Path-Stat
// header), and HEAD to confirm the stat header alone.
func TestContainerArchive(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	// PUT a tar into /data.
	payload := []byte("TAR-BYTES")
	resp = do(t, http.MethodPut, srv.URL+"/containers/"+cr.ID+"/archive?path=/data", payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive put status = %d", resp.StatusCode)
	}

	// GET it back: body is the tar, header carries the stat.
	resp = do(t, http.MethodGet, srv.URL+"/containers/"+cr.ID+"/archive?path=/data", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive get status = %d", resp.StatusCode)
	}
	st, err := api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil || st.Name != "/data" {
		t.Fatalf("get stat header = %+v, err %v", st, err)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("archive get body = %q, want %q", got, payload)
	}

	// HEAD returns only the stat header.
	resp = do(t, http.MethodHead, srv.URL+"/containers/"+cr.ID+"/archive?path=/data", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive head status = %d", resp.StatusCode)
	}
	st, err = api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil || st.Name != "/data" || st.Size != int64(len(payload)) {
		t.Fatalf("head stat header = %+v, err %v", st, err)
	}
}

// TestContainerArchiveStatErrorStatus confirms an archive GET/HEAD whose
// StatPath fails yields a classified docker-shaped error: the usual attacher is
// a cornus server client, so a missing deployment/path arrives as relayed
// status text ("404 Not Found: ...") and an unsupported backend as "not
// supported" (501) — no more blanket 404, and no flushed empty 200 on GET.
func TestContainerArchiveStatErrorStatus(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want int
	}{
		{"missing path", `404 Not Found: lstat /proc/42/root/nope: no such file or directory`, http.StatusNotFound},
		{"unsupported backend", `501 Not Implemented: cp/archive not supported on the kubernetes backend; use kubectl cp`, http.StatusNotImplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeAttacher{statErr: errors.New(tc.err)}
			srv := httptest.NewServer(New(fa).Handler())
			defer srv.Close()
			id := createAndStart(t, srv)

			for _, method := range []string{http.MethodGet, http.MethodHead} {
				resp := do(t, method, srv.URL+"/containers/"+id+"/archive?path=/nope", nil)
				resp.Body.Close()
				if resp.StatusCode != tc.want {
					t.Fatalf("%s archive status = %d, want %d", method, resp.StatusCode, tc.want)
				}
			}
		})
	}
}

// TestContainerArchiveGetCopyErrorBeforeBytes confirms a CopyFrom error raised
// before the first tar byte (stat succeeded, copy failed) becomes a
// docker-shaped error response — the 200 header is deferred until the first
// output byte — with the stat header withdrawn.
func TestContainerArchiveGetCopyErrorBeforeBytes(t *testing.T) {
	fa := &fakeAttacher{copyFromErr: errors.New("backend exploded")}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()
	id := createAndStart(t, srv)

	resp := do(t, http.MethodGet, srv.URL+"/containers/"+id+"/archive?path=/data", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("archive get status = %d, want 500", resp.StatusCode)
	}
	if h := resp.Header.Get(api.PathStatHeader); h != "" {
		t.Fatalf("stat header on error response = %q, want empty", h)
	}
	var e map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if e["message"] != "backend exploded" {
		t.Fatalf("message = %q", e["message"])
	}
}

// TestExecTunnel drives a full docker exec through the proxy: create a container,
// create an exec, then raw-write POST /exec/{id}/start with the upgrade headers,
// read the 101 handshake, send a payload, and assert it echoes back (the fake
// exec stream echoes, so a round trip proves the hijack tunnel wiring).
func TestExecTunnel(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	// create + start a container.
	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	// exec create.
	eb, _ := json.Marshal(execConfigRequest{Cmd: []string{"sh"}, AttachStdin: true, AttachStdout: true, Tty: true})
	resp = do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/exec", eb)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("exec create status = %d", resp.StatusCode)
	}
	var er struct {
		Id string `json:"Id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()
	if er.Id == "" {
		t.Fatal("no exec id")
	}

	// exec-start over a raw hijacked connection.
	conn := rawDial(t, srv.URL)
	defer conn.Close()
	req := "POST /exec/" + er.Id + "/start HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		"Content-Length: 30\r\n\r\n" +
		`{"Detach":false,"Tty":true}  ` // padded to 30 bytes
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("exec start handshake = %q, want 101", status)
	}
	drainHeaders(t, br)

	// Payload round-trips through the echoing fake exec stream.
	if _, err := io.WriteString(conn, "hello-exec"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("hello-exec"))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "hello-exec" {
		t.Fatalf("exec echo = %q", got)
	}
}

// TestExecOutputSurvivesStdinEOF is the regression guard for the bridge policy:
// a non-interactive `docker exec` (no -i) sends no stdin and half-closes its
// write side immediately. The backend exec stream still emits output shortly
// after; the bridge must deliver that output in full rather than tearing the
// tunnel down on the instant stdin EOF. The 60ms output delay makes the stdin
// EOF win the race, so the old full-close-on-either-EOF bridge truncated the
// output and this test would fail against it.
func TestExecOutputSurvivesStdinEOF(t *testing.T) {
	fa := &fakeAttacher{output: "EXEC_MARKER", outputDelay: 60 * time.Millisecond}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	eb, _ := json.Marshal(execConfigRequest{Cmd: []string{"echo", "hi"}, AttachStdout: true})
	resp = do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/exec", eb)
	var er struct {
		Id string `json:"Id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()

	conn := rawDial(t, srv.URL)
	defer conn.Close()
	body := `{"Detach":false,"Tty":false}`
	req := "POST /exec/" + er.Id + "/start HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("exec start handshake = %q, want 101", status)
	}
	drainHeaders(t, br)

	// Simulate non-interactive stdin EOF: half-close our write side, send NO stdin.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}

	// The delayed output must still arrive in full.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := io.ReadAll(br)
	if !strings.Contains(string(got), "EXEC_MARKER") {
		t.Fatalf("exec output after stdin EOF = %q, want it to contain EXEC_MARKER", got)
	}
}

// TestExecInspect confirms GET /exec/{id}/json renders the backend exec state.
func TestExecInspect(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	resp := do(t, http.MethodGet, srv.URL+"/exec/exec-web/json", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exec inspect status = %d", resp.StatusCode)
	}
	var out struct {
		ID       string
		Running  bool
		ExitCode int
		Pid      int
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.ID != "exec-web" || !out.Running || out.ExitCode != 7 || out.Pid != 99 {
		t.Fatalf("exec inspect = %+v", out)
	}
}

// TestExecResize confirms POST /exec/{id}/resize?h=&w= parses the dimensions and
// forwards them to the backend attacher, returning 200.
func TestExecResize(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	resp := do(t, http.MethodPost, srv.URL+"/exec/exec-web/resize?h=40&w=100", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exec resize status = %d, want 200", resp.StatusCode)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.resizedExec != "exec-web" || fa.resizedH != 40 || fa.resizedW != 100 {
		t.Fatalf("ExecResize got (%q, %d, %d), want (exec-web, 40, 100)", fa.resizedExec, fa.resizedH, fa.resizedW)
	}
}

// TestAttachTunnel drives docker attach through the proxy: create+start, then
// raw-write POST /containers/{id}/attach with upgrade headers, read the 101, and
// assert a payload echoes back through the fake attach stream.
func TestAttachTunnel(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	conn := rawDial(t, srv.URL)
	defer conn.Close()
	req := "POST /containers/" + cr.ID + "/attach?stream=1&stdin=1&stdout=1&stderr=1 HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("attach handshake = %q, want 101", status)
	}
	drainHeaders(t, br)

	if _, err := io.WriteString(conn, "hello-attach"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("hello-attach"))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "hello-attach" {
		t.Fatalf("attach echo = %q", got)
	}
}

// rawDial opens a raw TCP connection to an httptest server URL.
func rawDial(t *testing.T, serverURL string) net.Conn {
	t.Helper()
	host := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// drainHeaders reads response header lines up to the blank line.
func drainHeaders(t *testing.T, br *bufio.Reader) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			return
		}
	}
}

func do(t *testing.T, method, url string, body []byte) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != nil {
		r, err = http.NewRequest(method, url, bytes.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestVolumePrune is the regression guard for the prune contract: POST
// /volumes/prune must not fall through the /volumes/ catch-all (handleVolumeItem)
// and 405, but return the docker prune shape with 200.
func TestVolumePrune(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()

	resp := do(t, http.MethodPost, srv.URL+"/v1.43/volumes/prune", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("volume prune status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		VolumesDeleted []string
		SpaceReclaimed int
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode prune body: %v", err)
	}
	if out.VolumesDeleted == nil {
		t.Fatalf("VolumesDeleted = nil, want []")
	}
}

// TestNetworkPrune mirrors TestVolumePrune for POST /networks/prune.
func TestNetworkPrune(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()

	resp := do(t, http.MethodPost, srv.URL+"/v1.43/networks/prune", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("network prune status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		NetworksDeleted []string
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode prune body: %v", err)
	}
	if out.NetworksDeleted == nil {
		t.Fatalf("NetworksDeleted = nil, want []")
	}
}

// TestExecRecordReclaimed is the regression guard for the exec-registry leak: a
// completed `docker exec` must not leave its record in execRegistry forever
// (health-check/probe loops would otherwise grow the map unboundedly).
func TestExecRecordReclaimed(t *testing.T) {
	fa := &fakeAttacher{}
	p := New(fa)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	b, _ := json.Marshal(createRequest{Image: "img"})
	resp, _ := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	eb, _ := json.Marshal(execConfigRequest{Cmd: []string{"sh"}, AttachStdin: true, AttachStdout: true, Tty: true})
	resp = do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/exec", eb)
	var er struct {
		Id string `json:"Id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()
	if er.Id == "" {
		t.Fatal("no exec id")
	}

	// Sanity: the record exists before the exec is started.
	p.execs.mu.Lock()
	n := len(p.execs.byID)
	p.execs.mu.Unlock()
	if n != 1 {
		t.Fatalf("execRegistry size before start = %d, want 1", n)
	}

	// Run the exec to completion over a raw hijacked connection.
	conn := rawDial(t, srv.URL)
	defer conn.Close()
	body := `{"Detach":false,"Tty":true}`
	req := "POST /exec/" + er.Id + "/start HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("exec start handshake = %q, want 101", status)
	}
	drainHeaders(t, br)
	if _, err := io.WriteString(conn, "hi"); err != nil {
		t.Fatal(err)
	}
	// Drain until the echo stream closes; bridge returns and the record is freed.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.ReadAll(br)

	// The delete runs in the server handler after bridge returns; poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.execs.mu.Lock()
		n = len(p.execs.byID)
		p.execs.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("execRegistry size after exec = %d, want 0 (record leaked)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
