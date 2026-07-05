//go:build linux

package incushost

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
)

// fakeConn is an in-memory incusConn modelling the daemon's instance lifecycle:
// create/start/stop/delete, config-key storage, proxy (published-port) devices,
// and a host-port conflict (two live proxy devices on one host port is an
// error, as dockerd's port allocator would reject). It deliberately models the
// resource lifecycle so a lax fake cannot hide a real bug (the pitfall the
// dockerhost port fake once fell into).
type fakeConn struct {
	volumes map[string]map[string]string
	insts   map[string]*incusapi.Instance
	// files models a per-instance filesystem for the cp paths: instance -> path
	// -> node. Directories carry their child base names in entries.
	files map[string]map[string]*fnode
	// consoles models each instance's accumulated console log for Logs.
	consoles map[string][]byte
	// liveConsole, when set, is the byte stream a --follow console attach
	// delivers AFTER the snapshot; attachedConsole/consoleDetached record that
	// the attach and its teardown actually happened.
	liveConsole     []byte
	attachedConsole string
	consoleDetached atomic.Int32
	// states overrides the state InstanceState reports for an instance, so the
	// metrics/network paths can be driven with real counters. Instances without an
	// override report just their lifecycle status.
	states map[string]*incusapi.InstanceState
	// execFn, when set, handles Exec (the streaming exec path needs a live
	// operation and writes to the caller's stdio).
	execFn func(name string, req incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error)
	// listErr, when set, makes instance listing fail — modelling a daemon that is
	// down or refusing the project.
	listErr error
	// fileErrs makes the file API fail for specific paths, modelling a daemon or
	// storage failure part-way through a copy.
	fileErrs map[string]error
	// getFileFn, when set, serves GetFile instead of the seeded tree, so a test can
	// hand back a SYNTHETIC body — one bigger than any fixture we would keep in
	// memory, which is the only way to observe whether the copy path buffers.
	getFileFn func(inst, path string) (io.ReadCloser, *incus.InstanceFileResponse, error)
	// createErrs / volumeErr / volumeDeleteErr make the companion wiring's daemon
	// calls fail, so the unwinding an Apply does on a partial failure is testable.
	createErrs      map[string]error
	volumeErr       error
	volumeDeleteErr error
	// calls records the teardown calls in order, so a test can assert that
	// volumes are only reaped once nothing holds them.
	calls []string
}

// volumeNames lists the volumes the fake currently holds, for failure messages.
func (f *fakeConn) volumeNames() []string {
	out := make([]string, 0, len(f.volumes))
	for k := range f.volumes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type fnode struct {
	typ     string // "file" | "directory" | "symlink"
	mode    int
	content []byte
	entries []string // directory children (base names)
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		insts:    map[string]*incusapi.Instance{},
		files:    map[string]map[string]*fnode{},
		consoles: map[string][]byte{},
		states:   map[string]*incusapi.InstanceState{},
	}
}

// seedFile registers a filesystem node on an instance (test helper).
func (f *fakeConn) seedFile(inst, path, typ string, mode int, content []byte, entries ...string) {
	if f.files[inst] == nil {
		f.files[inst] = map[string]*fnode{}
	}
	f.files[inst][path] = &fnode{typ: typ, mode: mode, content: content, entries: entries}
}

func (f *fakeConn) Instances() ([]incusapi.Instance, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]incusapi.Instance, 0, len(f.insts))
	for _, in := range f.insts {
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeConn) Instance(name string) (*incusapi.Instance, error) {
	if in, ok := f.insts[name]; ok {
		cp := *in
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeConn) InstanceState(name string) (*incusapi.InstanceState, error) {
	in, ok := f.insts[name]
	if !ok {
		return nil, nil
	}
	if st, ok := f.states[name]; ok {
		return st, nil
	}
	return &incusapi.InstanceState{Status: in.Status, StatusCode: in.StatusCode}, nil
}

// hostPortsInUse returns the set of host ports published by live proxy devices,
// excluding instance except.
func (f *fakeConn) hostPortsInUse(except string) map[string]bool {
	used := map[string]bool{}
	for name, in := range f.insts {
		if name == except {
			continue
		}
		for _, dev := range in.Devices {
			if dev["type"] == "proxy" {
				used[listenPort(dev["listen"])] = true
			}
		}
	}
	return used
}

func listenPort(listen string) string {
	parts := strings.Split(listen, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func (f *fakeConn) CreateInstance(req incusapi.InstancesPost) error {
	if err := f.createErrs[req.Name]; err != nil {
		return err
	}
	if _, ok := f.insts[req.Name]; ok {
		return errors.New("fake: instance already exists")
	}
	used := f.hostPortsInUse("")
	for _, dev := range req.Devices {
		if dev["type"] == "proxy" {
			if p := listenPort(dev["listen"]); used[p] {
				return errors.New("fake: host port " + p + " already allocated")
			}
		}
	}
	status, code := "Stopped", incusapi.Stopped
	if req.Start {
		status, code = "Running", incusapi.Running
	}
	f.insts[req.Name] = &incusapi.Instance{
		Name:       req.Name,
		Status:     status,
		StatusCode: code,
		InstancePut: incusapi.InstancePut{
			Config:  req.Config,
			Devices: req.Devices,
		},
	}
	return nil
}

func (f *fakeConn) SetInstanceState(name, action string, force bool, timeout int) error {
	in, ok := f.insts[name]
	if !ok {
		return errors.New("fake: not found: " + api404(name))
	}
	switch action {
	case "start", "restart":
		in.Status, in.StatusCode = "Running", incusapi.Running
	case "stop":
		in.Status, in.StatusCode = "Stopped", incusapi.Stopped
	}
	return nil
}

func (f *fakeConn) DeleteInstance(name string) error {
	f.calls = append(f.calls, "DeleteInstance "+name)
	delete(f.insts, name)
	return nil
}

func (f *fakeConn) Exec(name string, req incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	if f.execFn != nil {
		return f.execFn(name, req, args)
	}
	return nil, errors.New("fake: exec unsupported")
}
func (f *fakeConn) GetFile(inst, path string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if err, ok := f.fileErrs[path]; ok {
		return nil, nil, err
	}
	if f.getFileFn != nil {
		return f.getFileFn(inst, path)
	}
	n, ok := f.files[inst][path]
	if !ok {
		return nil, nil, incusapi.StatusErrorf(404, "not found: %s", path)
	}
	return io.NopCloser(bytes.NewReader(n.content)), &incus.InstanceFileResponse{
		Type:    n.typ,
		Mode:    n.mode,
		Entries: n.entries,
	}, nil
}

func (f *fakeConn) CreateFile(inst, path string, args incus.InstanceFileArgs) error {
	if err, ok := f.fileErrs[path]; ok {
		return err
	}
	if f.files[inst] == nil {
		f.files[inst] = map[string]*fnode{}
	}
	var content []byte
	if args.Content != nil {
		b, _ := io.ReadAll(args.Content)
		content = b
	}
	f.files[inst][path] = &fnode{typ: args.Type, mode: args.Mode, content: content}
	return nil
}
func (f *fakeConn) ConsoleLog(inst string) (io.ReadCloser, error) {
	if c, ok := f.consoles[inst]; ok {
		return io.NopCloser(bytes.NewReader(c)), nil
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (f *fakeConn) Close() {}

// api404 is an arbitrary marker; the backend's not-found detection for lifecycle
// actions relies on the seam wrapping deploy.ErrNotFound, which the fake does
// via appInstanceNames (empty => ErrNotFound) rather than SetInstanceState.
func api404(name string) string { return name }

func testBackend(f *fakeConn) *Backend {
	return &Backend{conn: f, policy: hostpolicy.Permissive(), project: "default", execs: newExecRegistry()}
}

func TestApplyCreatesReplicasWithPortsOnReplicaZero(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/app:v1",
		Replicas: 2,
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
		Env:      map[string]string{"FOO": "bar"},
	}
	st, err := b.Apply(context.Background(), spec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(st.Instances))
	}
	if st.Backend != "incus" || st.Image != "localhost:5000/app:v1" {
		t.Fatalf("unexpected status meta: %+v", st)
	}
	for _, in := range st.Instances {
		if !in.Running || in.State != "running" {
			t.Fatalf("instance %s not running: %+v", in.ID, in)
		}
	}
	// Replica 0 has the proxy device; replica 1 has none (replica-0-only publish).
	r0 := f.insts["cornus-web-0"]
	r1 := f.insts["cornus-web-1"]
	if countProxy(r0) != 1 {
		t.Fatalf("replica 0 should have 1 proxy device, got %d", countProxy(r0))
	}
	if countProxy(r1) != 0 {
		t.Fatalf("replica 1 should have 0 proxy devices, got %d", countProxy(r1))
	}
	if r0.Config["environment.FOO"] != "bar" {
		t.Fatalf("env not stamped: %v", r0.Config)
	}
	if r0.Config[configKeyPrefix+deploy.LabelApp] != "web" || r0.Config[configKeyPrefix+deploy.LabelManaged] != "true" {
		t.Fatalf("ownership config keys missing: %v", r0.Config)
	}
}

func countProxy(in *incusapi.Instance) int {
	n := 0
	for _, dev := range in.Devices {
		if dev["type"] == "proxy" {
			n++
		}
	}
	return n
}

func TestApplyRecreatesInsteadOfDuplicating(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 2, Ports: []api.PortMapping{{Host: 8080, Container: 80}}}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// A second Apply must delete-then-create (recreate), not conflict on the
	// already-published host port and not leave 4 instances.
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if len(f.insts) != 2 {
		t.Fatalf("want 2 instances after recreate, got %d", len(f.insts))
	}
}

func TestLifecycleNotFoundWrapsErrNotFound(t *testing.T) {
	b := testBackend(newFakeConn())
	for _, fn := range []struct {
		name string
		call func() error
	}{
		{"Start", func() error { return b.Start(context.Background(), "ghost") }},
		{"Stop", func() error { return b.Stop(context.Background(), "ghost") }},
		{"Restart", func() error { return b.Restart(context.Background(), "ghost") }},
	} {
		if err := fn.call(); !errors.Is(err, deploy.ErrNotFound) {
			t.Fatalf("%s on missing name: want ErrNotFound, got %v", fn.name, err)
		}
	}
}

func TestDeleteIsIfExists(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	if err := b.Delete(context.Background(), "ghost"); err != nil {
		t.Fatalf("Delete of missing name should be nil, got %v", err)
	}
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 1}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.insts) != 0 {
		t.Fatalf("instances remain after delete: %d", len(f.insts))
	}
}

func TestStopStart(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 1}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st, _ := b.Status(context.Background(), "web")
	if st.Instances[0].Running {
		t.Fatalf("expected stopped after Stop")
	}
	if err := b.Start(context.Background(), "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, _ = b.Status(context.Background(), "web")
	if !st.Instances[0].Running {
		t.Fatalf("expected running after Start")
	}
}

func TestOriginRoundTrip(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/app:v1",
		Replicas: 1,
		Origin:   &api.Origin{Project: "proj", Host: "dev-box", User: "alice"},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Origin == nil || st.Origin.Project != "proj" || st.Origin.Host != "dev-box" || st.Origin.User != "alice" {
		t.Fatalf("origin not round-tripped: %+v", st.Origin)
	}
}

func TestListGroupsByApp(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "a", Image: "localhost:5000/appx:v1", Replicas: 2}); err != nil {
		t.Fatalf("Apply a: %v", err)
	}
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "b", Image: "localhost:5000/appy:v1", Replicas: 1}); err != nil {
		t.Fatalf("Apply b: %v", err)
	}
	list, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 deployments, got %d", len(list))
	}
	if list[0].Name != "a" || len(list[0].Instances) != 2 || list[1].Name != "b" || len(list[1].Instances) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestHostPortConflictAcrossApps(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "a", Image: "localhost:5000/appx:v1", Replicas: 1, Ports: []api.PortMapping{{Host: 8080, Container: 80}}}); err != nil {
		t.Fatalf("Apply a: %v", err)
	}
	// A different app publishing the same host port must fail (the fake models
	// the daemon's port allocator).
	_, err := b.Apply(context.Background(), api.DeploySpec{Name: "b", Image: "localhost:5000/appy:v1", Replicas: 1, Ports: []api.PortMapping{{Host: 8080, Container: 80}}})
	if err == nil {
		t.Fatalf("expected host-port conflict error, got nil")
	}
}

func TestStatsMissingDeploymentIsNotFound(t *testing.T) {
	b := testBackend(newFakeConn())
	err := b.Stats(context.Background(), "ghost", api.StatsOptions{}, io.Discard)
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("Stats on missing name: want ErrNotFound, got %v", err)
	}
}

func TestImageSource(t *testing.T) {
	cases := []struct {
		ref        string
		wantServer string
		wantAlias  string
	}{
		{"localhost:5000/app:v1", "http://localhost:5000", "app:v1"},
		{"127.0.0.1:5000/team/app:latest", "http://127.0.0.1:5000", "team/app:latest"},
		{"docker.io/library/nginx:1.27", "https://index.docker.io", "library/nginx:1.27"},
		{"ghcr.io/org/img:tag", "https://ghcr.io", "org/img:tag"},
	}
	for _, tc := range cases {
		src, err := imageSource(tc.ref, nil)
		if err != nil {
			t.Fatalf("imageSource(%q): %v", tc.ref, err)
		}
		if src.Protocol != "oci" || src.Type != "image" {
			t.Fatalf("imageSource(%q): unexpected type/protocol %+v", tc.ref, src)
		}
		if src.Server != tc.wantServer {
			t.Fatalf("imageSource(%q): server = %q, want %q", tc.ref, src.Server, tc.wantServer)
		}
		if src.Alias != tc.wantAlias {
			t.Fatalf("imageSource(%q): alias = %q, want %q", tc.ref, src.Alias, tc.wantAlias)
		}
	}
}

// applyOne deploys a single-replica app and returns its instance id.
func applyOne(t *testing.T, b *Backend, f *fakeConn, name string) string {
	t.Helper()
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: name, Image: "localhost:5000/app:v1", Replicas: 1}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return "cornus-" + name + "-0"
}

func TestStatPath(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/etc/hi", "file", 0o644, []byte("hello"))
	st, err := b.StatPath(context.Background(), "web", "/etc/hi")
	if err != nil {
		t.Fatalf("StatPath: %v", err)
	}
	if st.Name != "hi" || st.Size != 5 || st.Mode != uint32(os.FileMode(0o644)) {
		t.Fatalf("unexpected stat: %+v", st)
	}
}

func TestStatPathNotFound(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	_, err := b.StatPath(context.Background(), "web", "/missing")
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCopyFromFile(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/etc/hi", "file", 0o644, []byte("hello"))
	var buf bytes.Buffer
	st, err := b.CopyFrom(context.Background(), "web", "/etc/hi", &buf)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if st.Name != "hi" {
		t.Fatalf("stat name = %q", st.Name)
	}
	got := tarEntries(t, buf.Bytes())
	if len(got) != 1 || got["hi"] != "hello" {
		t.Fatalf("unexpected tar: %v", got)
	}
}

func TestCopyFromDirRecursive(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/d", "directory", 0o755, nil, "a", "sub")
	f.seedFile(id, "/d/a", "file", 0o644, []byte("A"))
	f.seedFile(id, "/d/sub", "directory", 0o755, nil, "b")
	f.seedFile(id, "/d/sub/b", "file", 0o600, []byte("B"))
	var buf bytes.Buffer
	if _, err := b.CopyFrom(context.Background(), "web", "/d", &buf); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	got := tarEntries(t, buf.Bytes())
	for _, name := range []string{"d/", "d/sub/"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing dir entry %q in %v", name, keys(got))
		}
	}
	if got["d/a"] != "A" || got["d/sub/b"] != "B" {
		t.Fatalf("unexpected file contents: %v", got)
	}
}

func TestCopyToRoundTrip(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTar(t, tw, "f", tar.TypeReg, 0o644, "X")
	writeTar(t, tw, "dir/", tar.TypeDir, 0o755, "")
	writeTar(t, tw, "dir/g", tar.TypeReg, 0o600, "Y")
	tw.Close()
	if err := b.CopyTo(context.Background(), "web", "/dest", &buf, api.CopyToOptions{}); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if n := f.files[id]["/dest/f"]; n == nil || string(n.content) != "X" || n.typ != "file" {
		t.Fatalf("/dest/f wrong: %+v", n)
	}
	if n := f.files[id]["/dest/dir"]; n == nil || n.typ != "directory" {
		t.Fatalf("/dest/dir wrong: %+v", n)
	}
	if n := f.files[id]["/dest/dir/g"]; n == nil || string(n.content) != "Y" {
		t.Fatalf("/dest/dir/g wrong: %+v", n)
	}
}

func tarEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		b, _ := io.ReadAll(tr)
		out[hdr.Name] = string(b)
	}
	return out
}

func writeTar(t *testing.T, tw *tar.Writer, name string, typ byte, mode int64, content string) {
	t.Helper()
	hdr := &tar.Header{Name: name, Typeflag: typ, Mode: mode, Size: int64(len(content))}
	if typ == tar.TypeDir {
		hdr.Size = 0
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if content != "" {
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLogsStdcopyFramed(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.consoles[id] = []byte("hello from console\n")
	var buf bytes.Buffer
	if err := b.Logs(context.Background(), "web", api.LogOptions{}, &buf); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	// Demultiplex the stdcopy stream and confirm the payload lands on stdout.
	var out, errb bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errb, &buf); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if out.String() != "hello from console\n" {
		t.Fatalf("unexpected stdout %q", out.String())
	}
}

func TestLogsRejectsMalformedSince(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	err := b.Logs(context.Background(), "web", api.LogOptions{Since: "not-a-time"}, io.Discard)
	if err == nil {
		t.Fatalf("expected error for malformed --since")
	}
}

func TestPickIPv4(t *testing.T) {
	net := map[string]incusapi.InstanceStateNetwork{
		"lo": {Addresses: []incusapi.InstanceStateNetworkAddress{
			{Family: "inet", Scope: "local", Address: "127.0.0.1"},
		}},
		"eth0": {Addresses: []incusapi.InstanceStateNetworkAddress{
			{Family: "inet6", Scope: "global", Address: "fd42::1"},
			{Family: "inet", Scope: "global", Address: "10.1.2.3"},
		}},
	}
	if got := pickIPv4(net); got != "10.1.2.3" {
		t.Fatalf("pickIPv4 = %q, want 10.1.2.3", got)
	}
	// No global IPv4 -> empty.
	if got := pickIPv4(map[string]incusapi.InstanceStateNetwork{
		"lo": {Addresses: []incusapi.InstanceStateNetworkAddress{{Family: "inet", Scope: "local", Address: "127.0.0.1"}}},
	}); got != "" {
		t.Fatalf("pickIPv4 loopback-only = %q, want empty", got)
	}
}

func TestBuildExecPost(t *testing.T) {
	post := buildExecPost(api.ExecConfig{
		Cmd:        []string{"sh", "-c", "echo hi"},
		Tty:        true,
		Env:        []string{"A=1", "B=two", "MALFORMED"},
		WorkingDir: "/work",
		User:       "1000",
	})
	if len(post.Command) != 3 || !post.Interactive || !post.WaitForWS {
		t.Fatalf("unexpected post: %+v", post)
	}
	if post.Cwd != "/work" || post.User != 1000 {
		t.Fatalf("cwd/user wrong: %+v", post)
	}
	if post.Environment["A"] != "1" || post.Environment["B"] != "two" {
		t.Fatalf("env wrong: %+v", post.Environment)
	}
	if _, ok := post.Environment["MALFORMED"]; ok {
		t.Fatalf("malformed env entry should be dropped: %+v", post.Environment)
	}
}

func TestExecCreateInspectLifecycle(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	id, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"true"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if id == "" {
		t.Fatalf("empty exec id")
	}
	// Created but not started → not running, exit 0.
	st, err := b.ExecInspect(context.Background(), id)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running || st.Pid != 0 {
		t.Fatalf("fresh exec should not be running: %+v", st)
	}
}

func TestExecCreateNoDeployment(t *testing.T) {
	b := testBackend(newFakeConn())
	_, err := b.ExecCreate(context.Background(), "ghost", api.ExecConfig{Cmd: []string{"true"}})
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestExecInspectUnknownID(t *testing.T) {
	b := testBackend(newFakeConn())
	if _, err := b.ExecInspect(context.Background(), "nope"); err == nil {
		t.Fatalf("expected error for unknown exec id")
	}
	// ExecResize on an unstarted/unknown session is a no-op or error, never panics.
	if err := b.ExecResize(context.Background(), "nope", 24, 80); err == nil {
		t.Fatalf("expected error for unknown exec id on resize")
	}
}

func TestExecResizeBeforeStartStoresSize(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	// Control websocket not yet set → resize sends nothing (no error)...
	if err := b.ExecResize(context.Background(), id, 24, 100); err != nil {
		t.Fatalf("resize before start should not error, got %v", err)
	}
	// ...but the requested size is remembered so ExecStart can seed the initial
	// PTY size (the fix for the window-resize race: height=24, width=100).
	sess, ok := b.execs.get(id)
	if !ok || sess.width != 100 || sess.height != 24 {
		t.Fatalf("resize should store size (w=100,h=24), got %+v", sess)
	}
}

func TestAttachNotSupported(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	err := b.Attach(context.Background(), "web", api.AttachConfig{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected not-supported error, got %v", err)
	}
}

// Ensure the numeric status codes we compare against match the client's.
func TestRunningStatusCode(t *testing.T) {
	if incusapi.Running != incusapi.StatusCode(103) {
		t.Fatalf("unexpected Running status code %d", int(incusapi.Running))
	}
}

// TestAgentForwardNotOffered guards the gap between Remote() and what this
// backend actually runs: CORNUS_INCUS_REMOTE records the operator's opt-in, but
// no caretaker companion is created at all, so nothing carries an AgentRelayRole
// and nothing listens on the SSH_AUTH_SOCK path an exec would be given. The
// server used to infer agent-forward support from Remote() alone, letting the
// failure surface inside the container ("Error connecting to agent: No such file
// or directory") instead of as a clean API rejection.
//
// If the companion ever lands here (see .agents/docs/JOURNAL.md, "unified
// companion caretaker", 2026-07-25), this test should start failing — that is
// the point.
// TestAgentForwardTracksRemoteMode pins ssh-agent forwarding to remote mode,
// which is exactly when this backend runs a companion.
//
// It previously asserted the opposite ("AgentForwardEnabled = true, but this
// backend creates no companion") and was correct at the time: incus had no
// companion at all, so offering an SSH_AUTH_SOCK would have pointed the workload
// at a socket nothing listened on. The companion landed on 2026-07-28 — it
// carries the caretaker AgentRelayRole and listens on
// remotecompanion.AgentSocketPath inside the shared agent volume the app
// instance also mounts — so the honest answer flipped with it.
//
// Both directions are asserted: a non-remote backend must still say NO, because
// it starts no companion and the old failure mode would return.
func TestAgentForwardTracksRemoteMode(t *testing.T) {
	remote := &Backend{remote: true}
	if !remote.Remote() {
		t.Fatal("Remote() = false, want true (the fixture sets remote)")
	}
	enabled, err := remote.AgentForwardEnabled(context.Background(), "web")
	if err != nil {
		t.Fatalf("AgentForwardEnabled(remote): %v", err)
	}
	if !enabled {
		t.Error("AgentForwardEnabled = false in remote mode, but the companion carries the agent relay")
	}

	local := &Backend{}
	if local.Remote() {
		t.Fatal("Remote() = true on the zero-value fixture")
	}
	enabled, err = local.AgentForwardEnabled(context.Background(), "web")
	if err != nil {
		t.Fatalf("AgentForwardEnabled(local): %v", err)
	}
	if enabled {
		t.Error("AgentForwardEnabled = true without remote mode, so no companion exists to relay to")
	}
}

// ConsoleAttach satisfies incusConn. By default it reports "no console", so an
// accidental attach in a test that did not opt in fails loudly instead of
// hanging on a nil stream. A test that IS exercising the follow path installs
// liveConsole: the returned function stands in for Incus's mirror — it feeds the
// scripted bytes to the caller's sink and then blocks on the sink's Read, which
// is exactly how the real one ends (see consoleSink).
func (f *fakeConn) ConsoleAttach(name string) (func(io.ReadWriteCloser) error, func(), error) {
	if f.liveConsole == nil {
		return nil, nil, fmt.Errorf("fakeConn: console not available for %s", name)
	}
	f.attachedConsole = name
	live := f.liveConsole
	stream := func(rwc io.ReadWriteCloser) error {
		if _, err := rwc.Write(live); err != nil {
			return err
		}
		// Incus's read mirror copies the sink into the console until it reports
		// EOF, which consoleSink does on ctx cancellation; mirror that so the
		// stream function returns exactly when the real one would.
		_, _ = io.Copy(io.Discard, rwc)
		return nil
	}
	// Counted atomically, and idempotently: the seam's contract is that stop may
	// be called from the deferred teardown and the ctx watcher at once.
	return stream, func() { f.consoleDetached.Add(1) }, nil
}

// CreateVolume / DeleteVolume satisfy incusConn and record what the companion
// wiring asked for, so a test can assert the agent volume was provisioned and
// reaped without a live Incus.
func (f *fakeConn) CreateVolume(pool, name string, config map[string]string) error {
	if f.volumeErr != nil {
		return f.volumeErr
	}
	if f.volumes == nil {
		f.volumes = map[string]map[string]string{}
	}
	f.volumes[pool+"/"+name] = config
	return nil
}

func (f *fakeConn) DeleteVolume(pool, name string) error {
	f.calls = append(f.calls, "DeleteVolume "+pool+"/"+name)
	if f.volumeDeleteErr != nil {
		return f.volumeDeleteErr
	}
	delete(f.volumes, pool+"/"+name)
	return nil
}

// TestCopyFromDoesNotBufferWholeFiles: the incus copy path used to io.ReadAll every file
// before packing it, so a copy out of an instance allocated the whole file in the SERVER's
// memory. That was survivable while the web explorer capped a transfer at 10 MB and
// stopped being so the moment that cap came off — the relay hands this backend whatever
// the user dragged.
//
// The body here is synthetic, so nothing but the copy path can account for the
// allocation.
func TestCopyFromDoesNotBufferWholeFiles(t *testing.T) {
	const size = 64 << 20
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.seedFile(id, "/big.bin", "file", 0o644, nil)
	f.getFileFn = func(_, path string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
		if path != "/big.bin" {
			return nil, nil, incusapi.StatusErrorf(404, "not found: %s", path)
		}
		return io.NopCloser(io.LimitReader(constReader('q'), size)), &incus.InstanceFileResponse{
			Type: "file", Mode: 0o644,
		}, nil
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	st, err := b.CopyFrom(context.Background(), "web", "/big.bin", countingWriter{})
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	runtime.ReadMemStats(&after)

	if st.Size != size {
		t.Fatalf("stat size = %d, wanted %d", st.Size, size)
	}
	// Generous: the streaming path allocates a few 32 KiB buffers, the buffering one
	// allocates the file (twice over, as the slice doubles).
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > size/4 {
		t.Errorf("copying a %d-byte file allocated %d bytes: it is being buffered whole",
			size, allocated)
	}
}

// constReader yields the same byte forever, so a large fixture costs nothing to model.
type constReader byte

func (c constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(c)
	}
	return len(p), nil
}

// countingWriter discards the tar so the TEST's own bookkeeping does not dominate the
// allocation it is measuring.
type countingWriter struct{}

func (countingWriter) Write(p []byte) (int, error) { return len(p), nil }
