package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/storage"
)

// fakeBackend is an in-memory deploy.Backend for exercising the HTTP API. It is
// mutex-guarded because the deploy-attach handler runs in its own goroutine
// while the test observes state (its teardown Delete happens after disconnect).
type fakeBackend struct {
	mu      sync.Mutex
	applied map[string]api.DeploySpec
	deleted []string
	actions []string
	archive []byte // last tar received by CopyTo, replayed by CopyFrom
	// execOutput, when set, makes ExecStart/Attach write these bytes to conn
	// IMMEDIATELY (independent of any stdin) then return — modelling real Docker,
	// whose stdout does not depend on stdin. Empty means echo behaviour.
	execOutput string
	// resizeH, resizeW record the last dimensions passed to ExecResize.
	resizeH, resizeW uint
	// fwdPort, fwdProto record the last ForwardPort target.
	fwdPort  int
	fwdProto string
	// logsErr / statsErr, when set, are returned by Logs/Stats. With
	// logsPartialWrite / statsPartialWrite, the method first writes its normal
	// output and THEN returns the error, modelling a backend that fails
	// mid-stream.
	logsErr           error
	statsErr          error
	logsPartialWrite  bool
	statsPartialWrite bool
	// actionErr, when set, is returned by Start/Stop/Restart (modelling e.g. a
	// backend's deploy.ErrNotFound for a missing name).
	actionErr error
	// statErr / copyFromErr, when set, are returned by StatPath / CopyFrom
	// before any output (modelling a missing deployment or path on the archive
	// endpoints). With copyFromPartialWrite, CopyFrom first streams the archive
	// bytes and THEN returns copyFromErr (a mid-tar failure).
	statErr              error
	copyFromErr          error
	copyFromPartialWrite bool
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Close() error { return nil }

func (f *fakeBackend) Apply(_ context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	f.mu.Lock()
	if f.applied == nil {
		f.applied = map[string]api.DeploySpec{}
	}
	f.applied[spec.Name] = spec
	f.mu.Unlock()
	return api.DeployStatus{Name: spec.Name, Image: spec.Image, Backend: "fake",
		Instances: []api.InstanceStatus{{ID: "x", State: "running", Running: true}}}, nil
}

func (f *fakeBackend) Status(_ context.Context, name string) (api.DeployStatus, error) {
	f.mu.Lock()
	spec := f.applied[name]
	f.mu.Unlock()
	return api.DeployStatus{Name: name, Image: spec.Image, Backend: "fake"}, nil
}

func (f *fakeBackend) List(_ context.Context) ([]api.DeployStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.DeployStatus
	for name, spec := range f.applied {
		out = append(out, api.DeployStatus{Name: name, Image: spec.Image, Backend: "fake"})
	}
	return out, nil
}

func (f *fakeBackend) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, name)
	delete(f.applied, name)
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Start(_ context.Context, name string) error {
	f.mu.Lock()
	f.actions = append(f.actions, "start:"+name)
	f.mu.Unlock()
	return f.actionErr
}

func (f *fakeBackend) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	f.actions = append(f.actions, "stop:"+name)
	f.mu.Unlock()
	return f.actionErr
}

func (f *fakeBackend) Restart(_ context.Context, name string) error {
	f.mu.Lock()
	f.actions = append(f.actions, "restart:"+name)
	f.mu.Unlock()
	return f.actionErr
}

// Logs writes a single stdcopy-framed stdout line for the deployment, honoring
// the Backend.Logs framing contract.
func (f *fakeBackend) Logs(_ context.Context, name string, _ api.LogOptions, w io.Writer) error {
	if f.logsErr != nil && !f.logsPartialWrite {
		return f.logsErr
	}
	if _, err := stdcopy.NewStdWriter(w, stdcopy.Stdout).Write([]byte("hello from " + name + "\n")); err != nil {
		return err
	}
	return f.logsErr
}

// Stats writes a single canned Docker-format stats object for the deployment.
func (f *fakeBackend) Stats(_ context.Context, name string, _ api.StatsOptions, w io.Writer) error {
	if f.statsErr != nil && !f.statsPartialWrite {
		return f.statsErr
	}
	if _, err := io.WriteString(w, `{"name":"`+name+`","read":"now"}`); err != nil {
		return err
	}
	return f.statsErr
}

func (f *fakeBackend) StatPath(_ context.Context, _, path string) (api.PathStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statErr != nil {
		return api.PathStat{}, f.statErr
	}
	return api.PathStat{Name: path, Size: int64(len(f.archive)), Mode: 0o644}, nil
}

func (f *fakeBackend) CopyFrom(_ context.Context, _, path string, w io.Writer) (api.PathStat, error) {
	f.mu.Lock()
	b := f.archive
	err := f.copyFromErr
	partial := f.copyFromPartialWrite
	f.mu.Unlock()
	if err != nil && !partial {
		return api.PathStat{}, err
	}
	if _, werr := w.Write(b); werr != nil {
		return api.PathStat{}, werr
	}
	if err != nil {
		return api.PathStat{}, err
	}
	return api.PathStat{Name: path, Size: int64(len(b)), Mode: 0o644}, nil
}

func (f *fakeBackend) CopyTo(_ context.Context, _, _ string, r io.Reader, _ api.CopyToOptions) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.archive = b
	f.mu.Unlock()
	return nil
}

// ExecCreate returns a canned exec id for the deployment.
func (f *fakeBackend) ExecCreate(_ context.Context, name string, _ api.ExecConfig) (string, error) {
	return "exec-" + name, nil
}

// ExecStart either emits canned output independent of stdin (when execOutput is
// set, modelling real Docker) or echoes conn back to itself (default). Either
// way it proves the WS preamble + tunnel wiring end to end.
func (f *fakeBackend) ExecStart(_ context.Context, _ string, _ api.ExecStartConfig, conn io.ReadWriteCloser) error {
	return f.streamExec(conn)
}

// ExecInspect returns a canned running state.
func (f *fakeBackend) ExecInspect(_ context.Context, _ string) (api.ExecState, error) {
	return api.ExecState{Running: true, ExitCode: 0, Pid: 4242}, nil
}

// ExecResize records the last requested TTY dimensions so a later phase's test
// can assert the out-of-band resize path reached the backend.
func (f *fakeBackend) ExecResize(_ context.Context, _ string, height, width uint) error {
	f.mu.Lock()
	f.resizeH, f.resizeW = height, width
	f.mu.Unlock()
	return nil
}

// Attach behaves like ExecStart.
func (f *fakeBackend) Attach(_ context.Context, _ string, _ api.AttachConfig, conn io.ReadWriteCloser) error {
	return f.streamExec(conn)
}

// ForwardPort records the target port/proto and echoes the tunnel, so a test can
// assert the preamble was decoded and the bytes round-trip through the bridge.
func (f *fakeBackend) ForwardPort(_ context.Context, _ string, port int, proto string, conn io.ReadWriteCloser) error {
	f.mu.Lock()
	f.fwdPort, f.fwdProto = port, proto
	f.mu.Unlock()
	_, err := io.Copy(conn, conn)
	conn.Close()
	return err
}

func (f *fakeBackend) streamExec(conn io.ReadWriteCloser) error {
	if f.execOutput != "" {
		_, err := io.WriteString(conn, f.execOutput)
		conn.Close()
		return err
	}
	_, err := io.Copy(conn, conn)
	conn.Close()
	return err
}

// hasApplied and wasDeleted are lock-safe accessors for concurrent observation.
func (f *fakeBackend) hasApplied(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.applied[name]
	return ok
}

func (f *fakeBackend) wasDeleted(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.deleted {
		if n == name {
			return true
		}
	}
	return false
}

func newTestServer(t *testing.T, backend deploy.Backend) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dir}, st)
	if err != nil {
		t.Fatal(err)
	}
	s.newBackend = func() (deploy.Backend, error) { return backend, nil }
	return httptest.NewServer(s.Handler())
}

func TestDeployAPI(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	// Apply.
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d", resp.StatusCode)
	}
	var st api.DeployStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Name != "web" || len(st.Instances) != 1 {
		t.Fatalf("apply result = %+v", st)
	}

	// List.
	resp, _ = http.Get(srv.URL + "/.cornus/v1/deploy")
	var list []api.DeployStatus
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("list = %+v", list)
	}

	// Status.
	resp, _ = http.Get(srv.URL + "/.cornus/v1/deploy/web")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}

	// Delete.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/.cornus/v1/deploy/web", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if len(fb.deleted) == 0 || fb.deleted[len(fb.deleted)-1] != "web" {
		t.Fatalf("deleted = %v", fb.deleted)
	}
}

// TestDeployDiscardsClientSubject confirms a client cannot forge the origin
// Subject: on an unauthenticated request the server clears whatever the client
// sent, while client-attested fields (project, host) are preserved.
func TestDeployDiscardsClientSubject(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Origin: &api.Origin{
			Project: "proj",
			Host:    "laptop",
			Subject: "forged-admin", // a client attempt to attest identity
		},
	}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	fb.mu.Lock()
	applied := fb.applied["web"]
	fb.mu.Unlock()
	if applied.Origin == nil {
		t.Fatal("applied spec has no origin")
	}
	if applied.Origin.Subject != "" {
		t.Errorf("Subject = %q, want empty (client value discarded on an unauthenticated request)", applied.Origin.Subject)
	}
	if applied.Origin.Project != "proj" || applied.Origin.Host != "laptop" {
		t.Errorf("client-attested fields not preserved: %+v", applied.Origin)
	}
}

// TestStampOriginSubject unit-tests the pure stamping helper: an authenticated
// identity overwrites any client value; an unauthenticated request with no
// origin stays nil.
func TestStampOriginSubject(t *testing.T) {
	// Authenticated: overwrite whatever the client sent.
	spec := api.DeploySpec{Origin: &api.Origin{Subject: "forged", Project: "p"}}
	stampOriginSubject(&spec, "user:real")
	if spec.Origin.Subject != "user:real" {
		t.Errorf("Subject = %q, want user:real", spec.Origin.Subject)
	}
	if spec.Origin.Project != "p" {
		t.Errorf("Project = %q, want preserved", spec.Origin.Project)
	}

	// Authenticated but no client origin: a minimal origin carrying the subject.
	spec = api.DeploySpec{}
	stampOriginSubject(&spec, "user:real")
	if spec.Origin == nil || spec.Origin.Subject != "user:real" {
		t.Errorf("Origin = %+v, want subject-only origin", spec.Origin)
	}

	// Unauthenticated, no client origin: stays nil.
	spec = api.DeploySpec{}
	stampOriginSubject(&spec, "")
	if spec.Origin != nil {
		t.Errorf("Origin = %+v, want nil", spec.Origin)
	}
}

// volBackend is a fakeBackend that also implements deploy.VolumeRemover, for the
// DELETE /.cornus/v1/volume/{name} path.
type volBackend struct {
	*fakeBackend
	removed []string
}

func (v *volBackend) RemoveVolume(_ context.Context, name string) error {
	v.removed = append(v.removed, name)
	return nil
}

// TestDeleteVolume covers DELETE /.cornus/v1/volume/{name}: a backend that implements
// deploy.VolumeRemover returns 204 and records the name; missing name is 400 and
// a non-DELETE method is 405.
func TestDeleteVolume(t *testing.T) {
	vb := &volBackend{fakeBackend: &fakeBackend{}}
	srv := newTestServer(t, vb)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/.cornus/v1/volume/proj_cache", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete volume status = %d", resp.StatusCode)
	}
	if len(vb.removed) != 1 || vb.removed[0] != "proj_cache" {
		t.Fatalf("removed = %v, want [proj_cache]", vb.removed)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/.cornus/v1/volume/", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing-name status = %d, want 400", resp.StatusCode)
	}

	resp, _ = http.Get(srv.URL + "/.cornus/v1/volume/proj_cache")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}

// TestDeleteVolumeUnsupported covers a backend that does NOT implement
// deploy.VolumeRemover: DELETE /.cornus/v1/volume/{name} answers 501 so the client can
// report volume removal as unsupported.
func TestDeleteVolumeUnsupported(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/.cornus/v1/volume/proj_cache", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unsupported status = %d, want 501", resp.StatusCode)
	}
}

func TestDeployLifecycleActions(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	for _, action := range []string{"start", "stop", "restart"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/.cornus/v1/deploy/web/"+action, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s status = %d", action, resp.StatusCode)
		}
	}
	want := []string{"start:web", "stop:web", "restart:web"}
	if len(fb.actions) != 3 || fb.actions[0] != want[0] || fb.actions[2] != want[2] {
		t.Fatalf("actions = %v, want %v", fb.actions, want)
	}

	// Unknown action -> 404.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/.cornus/v1/deploy/web/frobnicate", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action status = %d, want 404", resp.StatusCode)
	}
}

func TestDeployLogs(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/logs?follow=1&tail=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.docker.raw-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, resp.Body); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if got := stdout.String(); got != "hello from web\n" {
		t.Fatalf("logs = %q", got)
	}
	// A clean stream carries no stream-error trailer value.
	if got := resp.Trailer.Get(api.StreamErrorTrailer); got != "" {
		t.Fatalf("stream-error trailer on success = %q, want empty", got)
	}
}

func TestDeployStats(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/stats?stream=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != `{"name":"web","read":"now"}` {
		t.Fatalf("stats = %q", got)
	}
}

// TestDeployLogsBackendErrorBeforeOutput confirms a backend error raised
// before any log byte is written becomes a real error response (status mapped
// from the error message, JSON error body) instead of an empty 200.
func TestDeployLogsBackendErrorBeforeOutput(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want int
	}{
		{"not found", `dockerhost: no instances for deployment "web"`, http.StatusNotFound},
		{"invalid since", `containerd: invalid since value "bogus" (want Unix seconds or RFC3339 timestamp)`, http.StatusBadRequest},
		{"unclassified", "backend exploded", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBackend{logsErr: errors.New(tc.err)}
			srv := newTestServer(t, fb)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/logs")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("logs status = %d, want %d", resp.StatusCode, tc.want)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type = %q", ct)
			}
			var e map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if e["error"] != tc.err {
				t.Fatalf("error = %q, want %q", e["error"], tc.err)
			}
			// The pre-first-byte path sends a real error response; the
			// stream-error trailer belongs to committed 200 streams only and
			// must not be advertised here.
			if h := resp.Header.Get("Trailer"); h != "" {
				t.Fatalf("Trailer header on error response = %q, want empty", h)
			}
		})
	}
}

// TestDeployStatsBackendErrorBeforeOutput confirms an unsupported-stats backend
// (the kubernetes backend) yields a 501 with the message rather than an empty
// 200.
func TestDeployStatsBackendErrorBeforeOutput(t *testing.T) {
	msg := "stats not supported on the kubernetes backend (needs metrics-server); use kubectl top"
	fb := &fakeBackend{statsErr: errors.New(msg)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/stats?stream=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("stats status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
	var e map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if e["error"] != msg {
		t.Fatalf("error = %q, want %q", e["error"], msg)
	}
}

// TestDeployLogsMidStreamBackendError confirms a backend error AFTER output has
// begun leaves the committed 200 and the partial body intact, and surfaces the
// error out of band in the X-Cornus-Stream-Error trailer (the status can no
// longer change once the body has begun), so a client can tell truncation from
// a clean EOF.
func TestDeployLogsMidStreamBackendError(t *testing.T) {
	fb := &fakeBackend{logsErr: errors.New("stream broke"), logsPartialWrite: true}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.docker.raw-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, resp.Body); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if got := stdout.String(); got != "hello from web\n" {
		t.Fatalf("partial logs = %q", got)
	}
	// The trailer is populated only after the body reached EOF (StdCopy above).
	if got := resp.Trailer.Get(api.StreamErrorTrailer); got != "stream broke" {
		t.Fatalf("stream-error trailer = %q, want %q", got, "stream broke")
	}
}

// TestDeployStatsMidStreamBackendError confirms the same trailer convention on
// the stats stream: partial output, committed 200, error in the trailer.
func TestDeployStatsMidStreamBackendError(t *testing.T) {
	fb := &fakeBackend{statsErr: errors.New("stats collector died"), statsPartialWrite: true}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != `{"name":"web","read":"now"}` {
		t.Fatalf("partial stats = %q", got)
	}
	if got := resp.Trailer.Get(api.StreamErrorTrailer); got != "stats collector died" {
		t.Fatalf("stream-error trailer = %q, want %q", got, "stats collector died")
	}
}

// TestDeployArchiveGetMidStreamError confirms an archive GET whose CopyFrom
// fails after tar bytes have flowed keeps the 200 + partial tar and carries the
// error in the X-Cornus-Stream-Error trailer (the tar is truncated, and the
// client must not treat it as complete).
func TestDeployArchiveGetMidStreamError(t *testing.T) {
	fb := &fakeBackend{
		archive:              []byte("TAR-PREFIX"),
		copyFromErr:          errors.New("container vanished mid-copy"),
		copyFromPartialWrite: true,
	}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/archive?path=/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive get status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, []byte("TAR-PREFIX")) {
		t.Fatalf("partial tar = %q", body)
	}
	if got := resp.Trailer.Get(api.StreamErrorTrailer); got != "container vanished mid-copy" {
		t.Fatalf("stream-error trailer = %q, want %q", got, "container vanished mid-copy")
	}
}

func TestDeployArchive(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	// PUT a tar payload into the container archive.
	payload := []byte("TAR-BYTES")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/.cornus/v1/deploy/web/archive?path=/data", bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive put status = %d", resp.StatusCode)
	}

	// HEAD returns the path stat header (no body).
	req, _ = http.NewRequest(http.MethodHead, srv.URL+"/.cornus/v1/deploy/web/archive?path=/data", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	st, err := api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil {
		t.Fatalf("decode stat header: %v", err)
	}
	if st.Name != "/data" || st.Size != int64(len(payload)) {
		t.Fatalf("head stat = %+v", st)
	}

	// GET streams the tar back and carries the stat header.
	resp, err = http.Get(srv.URL + "/.cornus/v1/deploy/web/archive?path=/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive get status = %d", resp.StatusCode)
	}
	st, err = api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil {
		t.Fatalf("decode get stat header: %v", err)
	}
	if st.Name != "/data" {
		t.Fatalf("get stat = %+v", st)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("archive get body = %q, want %q", body, payload)
	}
}

// TestDeployArchiveStatErrorStatus confirms an archive GET/HEAD whose StatPath
// fails is a classified error response — deploy.ErrNotFound and fs.ErrNotExist
// (a missing cp path) map to 404, an unsupported backend to 501 — not a 500 (or
// the GET's former flushed empty 200).
func TestDeployArchiveStatErrorStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"missing deployment", fmt.Errorf("fake: deployment %q: %w", "ghost", deploy.ErrNotFound), http.StatusNotFound},
		{"missing path", fmt.Errorf("lstat /proc/42/root/nope: %w", fs.ErrNotExist), http.StatusNotFound},
		{"unsupported backend", errors.New("cp/archive not supported on the kubernetes backend; use kubectl cp"), http.StatusNotImplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBackend{statErr: tc.err}
			srv := newTestServer(t, fb)
			defer srv.Close()

			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req, _ := http.NewRequest(method, srv.URL+"/.cornus/v1/deploy/web/archive?path=/nope", nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != tc.want {
					t.Fatalf("%s archive status = %d, want %d", method, resp.StatusCode, tc.want)
				}
			}
		})
	}
}

// TestDeployArchiveGetCopyErrorBeforeBytes confirms a CopyFrom error raised
// before the first tar byte (the stat succeeded, then the copy failed) becomes
// a real error response — the 200 header is deferred until the backend's first
// output byte — with the stat header withdrawn.
func TestDeployArchiveGetCopyErrorBeforeBytes(t *testing.T) {
	fb := &fakeBackend{copyFromErr: errors.New("backend exploded")}
	srv := newTestServer(t, fb)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/deploy/web/archive?path=/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("archive get status = %d, want 500", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if h := resp.Header.Get(api.PathStatHeader); h != "" {
		t.Fatalf("stat header on error response = %q, want empty", h)
	}
	var e map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if e["error"] != "backend exploded" {
		t.Fatalf("error = %q", e["error"])
	}
}

// TestDeployStreamErrorTrailerEndToEnd drives the real pkg/client against the
// real server over a live httptest connection (trailers only round-trip over a
// real network conn): a backend that fails after partial output makes the
// client's Logs/Stats/CopyFrom return BOTH the partial bytes and an error
// carrying the backend message, while a healthy backend returns no error.
func TestDeployStreamErrorTrailerEndToEnd(t *testing.T) {
	ctx := context.Background()

	t.Run("logs partial then error", func(t *testing.T) {
		fb := &fakeBackend{logsErr: errors.New("stream broke"), logsPartialWrite: true}
		srv := newTestServer(t, fb)
		defer srv.Close()

		var buf bytes.Buffer
		err := client.New(srv.URL).Logs(ctx, "web", api.LogOptions{}, &buf)
		if err == nil {
			t.Fatal("Logs after mid-stream backend error = nil, want error")
		}
		if !strings.Contains(err.Error(), "stream broke") || !strings.Contains(err.Error(), "partial output") {
			t.Fatalf("Logs error = %q, want the backend message flagged as partial output", err)
		}
		var stdout bytes.Buffer
		if _, err := stdcopy.StdCopy(&stdout, io.Discard, &buf); err != nil {
			t.Fatalf("StdCopy: %v", err)
		}
		if got := stdout.String(); got != "hello from web\n" {
			t.Fatalf("partial logs = %q", got)
		}
	})

	t.Run("logs success has no error", func(t *testing.T) {
		fb := &fakeBackend{}
		srv := newTestServer(t, fb)
		defer srv.Close()

		var buf bytes.Buffer
		if err := client.New(srv.URL).Logs(ctx, "web", api.LogOptions{}, &buf); err != nil {
			t.Fatalf("Logs on clean stream = %v, want nil", err)
		}
	})

	t.Run("stats partial then error", func(t *testing.T) {
		fb := &fakeBackend{statsErr: errors.New("stats collector died"), statsPartialWrite: true}
		srv := newTestServer(t, fb)
		defer srv.Close()

		var buf bytes.Buffer
		err := client.New(srv.URL).Stats(ctx, "web", api.StatsOptions{Stream: true}, &buf)
		if err == nil {
			t.Fatal("Stats after mid-stream backend error = nil, want error")
		}
		if !strings.Contains(err.Error(), "stats collector died") {
			t.Fatalf("Stats error = %q, want the backend message", err)
		}
		if got := buf.String(); got != `{"name":"web","read":"now"}` {
			t.Fatalf("partial stats = %q", got)
		}
	})

	t.Run("copyfrom partial then error", func(t *testing.T) {
		fb := &fakeBackend{
			archive:              []byte("TAR-PREFIX"),
			copyFromErr:          errors.New("container vanished mid-copy"),
			copyFromPartialWrite: true,
		}
		srv := newTestServer(t, fb)
		defer srv.Close()

		var buf bytes.Buffer
		_, err := client.New(srv.URL).CopyFrom(ctx, "web", "/data", &buf)
		if err == nil {
			t.Fatal("CopyFrom after mid-stream backend error = nil, want error")
		}
		if !strings.Contains(err.Error(), "container vanished mid-copy") {
			t.Fatalf("CopyFrom error = %q, want the backend message", err)
		}
		if got := buf.String(); got != "TAR-PREFIX" {
			t.Fatalf("partial tar = %q", got)
		}
	})
}

// TestSanitizeStreamError checks the trailer value sanitizer: control bytes
// (CR/LF above all — a field value must be a single line) become spaces, the
// value is trimmed and length-capped without splitting a rune, and an
// all-control message still yields a non-empty placeholder.
func TestSanitizeStreamError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "backend exploded", "backend exploded"},
		{"newlines flattened", "line one\r\nline two\ttabbed", "line one  line two tabbed"},
		{"empty after strip", "\r\n\x00", "stream error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeStreamError(errors.New(tc.in)); got != tc.want {
				t.Fatalf("sanitizeStreamError(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Length cap: a multi-byte rune straddling the cap is dropped whole.
	long := strings.Repeat("x", maxStreamErrorLen-1) + "ézzz" // é = 2 bytes, starts at cap-1
	got := sanitizeStreamError(errors.New(long))
	if len(got) > maxStreamErrorLen {
		t.Fatalf("sanitized length = %d, want <= %d", len(got), maxStreamErrorLen)
	}
	if got != strings.Repeat("x", maxStreamErrorLen-1) {
		t.Fatalf("cap did not fall back to the rune boundary: tail = %q", got[len(got)-8:])
	}
}

// TestDeployActionErrNotFoundIs404 asserts that a lifecycle action on a
// missing deployment — every backend wraps deploy.ErrNotFound — maps to a 404,
// not a 500 (errors.Is classification in streamErrStatus).
func TestDeployActionErrNotFoundIs404(t *testing.T) {
	fb := &fakeBackend{actionErr: fmt.Errorf("fake: deployment %q: %w", "ghost", deploy.ErrNotFound)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	for _, action := range []string{"start", "stop", "restart"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/.cornus/v1/deploy/ghost/"+action, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s of missing deployment: status = %d, want 404", action, resp.StatusCode)
		}
	}
}
