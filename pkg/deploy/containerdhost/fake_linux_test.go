//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"syscall"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/errdefs"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploy/internal/hostrun"
)

// fakeClient is the in-memory clientAPI double: real containerd semantics for
// the slice the backend exercises, no daemon. Interface embedding makes any
// unimplemented method a loud panic rather than silent misbehavior.
type fakeClient struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	pulled     []string
	created    []string
	pullErr    error
}

func newFakeClient() *fakeClient {
	return &fakeClient{containers: map[string]*fakeContainer{}}
}

// filterRe matches the two filter shapes the backend emits:
//
//	labels."key"=="value"   and   labels."key"~="re"
var filterRe = regexp.MustCompile(`^labels\."([^"]+)"(==|~=)"([^"]*)"$`)

func matchFilter(labels map[string]string, filter string) bool {
	m := filterRe.FindStringSubmatch(filter)
	if m == nil {
		panic(fmt.Sprintf("fakeClient: unsupported filter %q", filter))
	}
	v, ok := labels[m[1]]
	switch m[2] {
	case "==":
		return ok && v == m[3]
	case "~=":
		re := regexp.MustCompile(m[3])
		return ok && re.MatchString(v)
	}
	return false
}

func (f *fakeClient) Containers(ctx context.Context, filters ...string) ([]ctd.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ctd.Container
	for _, c := range f.containers {
		ok := true
		for _, flt := range filters {
			if !matchFilter(c.labels, flt) {
				ok = false
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

func (f *fakeClient) LoadContainer(ctx context.Context, id string) (ctd.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return nil, fmt.Errorf("container %q: %w", id, errdefs.ErrNotFound)
	}
	return c, nil
}

func (f *fakeClient) CreateContainer(ctx context.Context, id string, img ctd.Image, labels map[string]string, specOpts []oci.SpecOpts) (ctd.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.containers[id]; exists {
		return nil, fmt.Errorf("container %q: %w", id, errdefs.ErrAlreadyExists)
	}
	copied := map[string]string{}
	for k, v := range labels {
		copied[k] = v
	}
	c := &fakeContainer{client: f, id: id, image: img.Name(), labels: copied}
	f.containers[id] = c
	f.created = append(f.created, id)
	return c, nil
}

func (f *fakeClient) Pull(ctx context.Context, ref string, opts ...ctd.RemoteOpt) (ctd.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	f.pulled = append(f.pulled, ref)
	return &fakeImage{name: ref}, nil
}

func (f *fakeClient) GetImage(ctx context.Context, ref string) (ctd.Image, error) {
	return nil, fmt.Errorf("image %q: %w", ref, errdefs.ErrNotFound)
}

func (f *fakeClient) SnapshotService(name string) snapshots.Snapshotter {
	panic("fakeClient: SnapshotService not expected in this test")
}

func (f *fakeClient) Close() error { return nil }

// fakeImage implements the small part of ctd.Image the backend touches.
type fakeImage struct {
	ctd.Image // panic on anything unimplemented
	name      string
}

func (i *fakeImage) Name() string { return i.name }

// fakeContainer implements the ctd.Container slice the backend touches.
type fakeContainer struct {
	ctd.Container // panic on anything unimplemented
	client        *fakeClient
	id            string
	image         string
	labels        map[string]string
	task          *fakeTask
	deleted       bool
}

func (c *fakeContainer) ID() string { return c.id }

func (c *fakeContainer) Info(ctx context.Context, opts ...ctd.InfoOpts) (containers.Container, error) {
	return containers.Container{ID: c.id, Image: c.image, Labels: c.labels}, nil
}

func (c *fakeContainer) Labels(ctx context.Context) (map[string]string, error) {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.labels {
		out[k] = v
	}
	return out, nil
}

func (c *fakeContainer) SetLabels(ctx context.Context, l map[string]string) (map[string]string, error) {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	for k, v := range l {
		c.labels[k] = v
	}
	return c.labels, nil
}

func (c *fakeContainer) Task(ctx context.Context, attach cio.Attach) (ctd.Task, error) {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	if c.task == nil {
		return nil, fmt.Errorf("no task: %w", errdefs.ErrNotFound)
	}
	return c.task, nil
}

func (c *fakeContainer) NewTask(ctx context.Context, creator cio.Creator, opts ...ctd.NewTaskOpts) (ctd.Task, error) {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	if c.task != nil {
		return nil, fmt.Errorf("task exists: %w", errdefs.ErrAlreadyExists)
	}
	t := &fakeTask{container: c, status: ctd.Created, exitCh: make(chan ctd.ExitStatus, 1)}
	c.task = t
	return t, nil
}

func (c *fakeContainer) Spec(ctx context.Context) (*oci.Spec, error) {
	return &oci.Spec{
		Process: &specs.Process{Cwd: "/", Env: []string{"BASE=1"}},
		Linux:   &specs.Linux{Namespaces: []specs.LinuxNamespace{{Type: specs.NetworkNamespace, Path: c.labels[labelNetNS]}}},
	}, nil
}

func (c *fakeContainer) Update(ctx context.Context, opts ...ctd.UpdateContainerOpts) error {
	// The backend only updates the baked OCI spec; the fake records the call.
	return nil
}

func (c *fakeContainer) Delete(ctx context.Context, opts ...ctd.DeleteOpts) error {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	if c.task != nil {
		return fmt.Errorf("container has a task: %w", errdefs.ErrFailedPrecondition)
	}
	c.deleted = true
	delete(c.client.containers, c.id)
	return nil
}

// fakeTask implements the ctd.Task slice the backend touches.
type fakeTask struct {
	ctd.Task   // panic on anything unimplemented
	container  *fakeContainer
	status     ctd.ProcessStatus
	exitStatus uint32
	killed     []syscall.Signal
	exitCh     chan ctd.ExitStatus
}

func (t *fakeTask) Pid() uint32 { return 4242 }

func (t *fakeTask) Start(ctx context.Context) error {
	t.container.client.mu.Lock()
	defer t.container.client.mu.Unlock()
	t.status = ctd.Running
	return nil
}

func (t *fakeTask) Status(ctx context.Context) (ctd.Status, error) {
	t.container.client.mu.Lock()
	defer t.container.client.mu.Unlock()
	return ctd.Status{Status: t.status, ExitStatus: t.exitStatus}, nil
}

func (t *fakeTask) Wait(ctx context.Context) (<-chan ctd.ExitStatus, error) {
	return t.exitCh, nil
}

func (t *fakeTask) Kill(ctx context.Context, sig syscall.Signal, opts ...ctd.KillOpts) error {
	t.container.client.mu.Lock()
	defer t.container.client.mu.Unlock()
	t.killed = append(t.killed, sig)
	t.status = ctd.Stopped
	select {
	case t.exitCh <- ctd.ExitStatus{}:
	default:
	}
	return nil
}

func (t *fakeTask) Delete(ctx context.Context, opts ...ctd.ProcessDeleteOpts) (*ctd.ExitStatus, error) {
	t.container.client.mu.Lock()
	defer t.container.client.mu.Unlock()
	t.container.task = nil
	return &ctd.ExitStatus{}, nil
}

// fakeNet records network operations without touching the kernel. Each setup
// call hands out a fresh IP (10.4.0.9, .10, ...) so tests can tell replicas
// and repair-reallocations apart.
type fakeNet struct {
	mu          sync.Mutex
	ensured     [][]string
	setups      map[string][]api.PortMapping // id -> published ports
	setupCounts map[string]int               // id -> number of setup calls
	setupSeq    int
	teardowns   []string
	removed     []string
	// materialized models the on-disk conflist per network: ensureNetworks
	// creates it, removeNetwork reaps it, and setup fails when a network's
	// conflist is missing (the real cniManager's load reads conflistPath).
	materialized map[string]bool
}

func newFakeNet() *fakeNet {
	return &fakeNet{setups: map[string][]api.PortMapping{}, setupCounts: map[string]int{}, materialized: map[string]bool{}}
}

func (n *fakeNet) EnsureNetworks(names []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ensured = append(n.ensured, names)
	for _, nw := range names {
		n.materialized[nw] = true
	}
	return nil
}

func (n *fakeNet) Setup(ctx context.Context, id string, networks []string, ports []api.PortMapping) (hostrun.Attachment, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, nw := range networks {
		if !n.materialized[nw] {
			return hostrun.Attachment{}, fmt.Errorf("fakeNet: network %q has no conflist (was it ensured?)", nw)
		}
	}
	n.setups[id] = ports
	n.setupCounts[id]++
	n.setupSeq++
	ip := fmt.Sprintf("10.4.0.%d", 8+n.setupSeq)
	ips := map[string]string{}
	for _, nw := range networks {
		ips[nw] = ip
	}
	return hostrun.Attachment{Netns: "/run/cornus/netns/" + id, IP: ip, IPs: ips}, nil
}

func (n *fakeNet) teardownInstance(ctx context.Context, id string, labels map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.teardowns = append(n.teardowns, id)
}

func (n *fakeNet) RemoveNetwork(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.removed = append(n.removed, name)
	delete(n.materialized, name)
	return nil
}

// newTestBackend wires a Backend over the fakes with a permissive policy.
func newTestBackend(t interface {
	Helper()
	TempDir() string
	Fatalf(string, ...any)
}, f *fakeClient) (*Backend, *fakeNet) {
	t.Helper()
	b, err := NewWithClient(f, Config{DataDir: t.TempDir()}, WithPolicy(hostpolicy.Permissive()))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	fn := newFakeNet()
	b.net = fn
	return b, fn
}
