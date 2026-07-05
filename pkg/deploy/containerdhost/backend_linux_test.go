//go:build linux

package containerdhost

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/wire"
)

// TestRemoveVolume checks the deploy.VolumeRemover path: RemoveVolume removes the
// named volume's host directory, and removing an absent one is a no-op success.
func TestRemoveVolume(t *testing.T) {
	dataDir := t.TempDir()
	b := &Backend{dataDir: dataDir, vols: hostrun.NewVolumeStore(dataDir, "containerd", "containerd")}
	dir := b.namedVolumeDir("proj_cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := b.RemoveVolume(context.Background(), "proj_cache"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("named volume dir survived RemoveVolume (stat err = %v)", err)
	}
	if err := b.RemoveVolume(context.Background(), "gone"); err != nil {
		t.Fatalf("RemoveVolume of an absent volume should succeed, got %v", err)
	}
}

func TestConfigResolve(t *testing.T) {
	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "")
	t.Setenv("CONTAINERD_ADDRESS", "")
	t.Setenv("CORNUS_CONTAINERD_NAMESPACE", "")

	if _, err := (Config{}).resolve(); err == nil {
		t.Fatal("missing DataDir must error")
	}
	cfg, err := (Config{DataDir: "/data"}).resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Address != DefaultAddress || cfg.Namespace != DefaultNamespace {
		t.Fatalf("defaults = %+v", cfg)
	}

	t.Setenv("CONTAINERD_ADDRESS", "/std/sock")
	cfg, _ = (Config{DataDir: "/data"}).resolve()
	if cfg.Address != "/std/sock" {
		t.Fatalf("CONTAINERD_ADDRESS fallback = %q", cfg.Address)
	}
	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "/cornus/sock")
	t.Setenv("CORNUS_CONTAINERD_NAMESPACE", "myns")
	cfg, _ = (Config{DataDir: "/data"}).resolve()
	if cfg.Address != "/cornus/sock" || cfg.Namespace != "myns" {
		t.Fatalf("env resolution = %+v", cfg)
	}
	// Explicit config wins over env.
	cfg, _ = (Config{DataDir: "/data", Address: "/explicit", Namespace: "expl"}).resolve()
	if cfg.Address != "/explicit" || cfg.Namespace != "expl" {
		t.Fatalf("explicit config = %+v", cfg)
	}

	// Snapshotter: default empty (containerd's default), env-resolvable,
	// explicit config wins.
	if cfg.Snapshotter != "" {
		t.Fatalf("default Snapshotter = %q, want empty", cfg.Snapshotter)
	}
	t.Setenv("CORNUS_CONTAINERD_SNAPSHOTTER", "native")
	cfg, _ = (Config{DataDir: "/data"}).resolve()
	if cfg.Snapshotter != "native" {
		t.Fatalf("env Snapshotter = %q, want native", cfg.Snapshotter)
	}
	cfg, _ = (Config{DataDir: "/data", Snapshotter: "zfs"}).resolve()
	if cfg.Snapshotter != "zfs" {
		t.Fatalf("explicit Snapshotter = %q, want zfs", cfg.Snapshotter)
	}
}

func TestApplyCreatesReplicasAndPublishesOnce(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)

	st, err := b.Apply(context.Background(), api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/web:v1",
		Replicas: 2,
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("instances = %+v", st.Instances)
	}
	for _, in := range st.Instances {
		if !in.Running {
			t.Fatalf("instance %s not running: %+v", in.ID, in)
		}
	}
	if len(f.pulled) != 1 || f.pulled[0] != "localhost:5000/web:v1" {
		t.Fatalf("pulled = %v", f.pulled)
	}
	// Ports publish on replica 0 only (portmap DNATs to a single instance).
	if got := fn.setups["cornus-web-0"]; len(got) != 1 || got[0].Host != 8080 {
		t.Fatalf("replica 0 ports = %+v", got)
	}
	if got := fn.setups["cornus-web-1"]; len(got) != 0 {
		t.Fatalf("replica 1 must not publish ports: %+v", got)
	}
	// Ownership + data-plane labels.
	c := f.containers["cornus-web-0"]
	if c.labels[labelIP] != "10.4.0.9" || !strings.HasPrefix(c.labels[labelNetNS], "/run/cornus/netns/") {
		t.Fatalf("labels = %v", c.labels)
	}
	if c.labels[restart.PolicyLabel] != "unless-stopped" {
		t.Fatalf("restart policy label = %q", c.labels[restart.PolicyLabel])
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	spec := api.DeploySpec{Name: "web", Image: "img"}

	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if len(f.containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(f.containers))
	}
	// Recreate semantics: created twice, torn down once in between.
	if len(f.created) != 2 {
		t.Fatalf("created = %v", f.created)
	}
	if len(fn.teardowns) != 1 {
		t.Fatalf("teardowns = %v", fn.teardowns)
	}
}

func TestApplyEnforcesPolicy(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	b.policy = hostpolicy.Policy{} // default-deny

	_, err := b.Apply(context.Background(), api.DeploySpec{
		Name:   "web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/", Target: "/host"}},
	})
	if err == nil {
		t.Fatal("Apply should reject a root bind under default-deny")
	}
	if !strings.Contains(err.Error(), "not permitted by policy") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.pulled) != 0 || len(f.created) != 0 {
		t.Fatalf("denied spec must not touch the daemon: pulled=%v created=%v", f.pulled, f.created)
	}
}

func TestDeleteTearsDownAndReapsNetworks(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	spec := api.DeploySpec{
		Name:     "web",
		Image:    "img",
		Networks: []api.NetworkAttachment{{Name: "front"}},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("containers left: %v", f.containers)
	}
	if len(fn.teardowns) != 1 || fn.teardowns[0] != "cornus-web-0" {
		t.Fatalf("teardowns = %v", fn.teardowns)
	}
	// front had one member which is gone -> reaped.
	if len(fn.removed) != 1 || fn.removed[0] != "front" {
		t.Fatalf("removed networks = %v", fn.removed)
	}
}

func TestDeleteKeepsSharedNetwork(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	net := []api.NetworkAttachment{{Name: "shared"}}
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "a", Image: "img", Networks: net}); err != nil {
		t.Fatalf("Apply a: %v", err)
	}
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "b", Image: "img", Networks: net}); err != nil {
		t.Fatalf("Apply b: %v", err)
	}
	if err := b.Delete(context.Background(), "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fn.removed) != 0 {
		t.Fatalf("shared network reaped while b still uses it: %v", fn.removed)
	}
}

func TestStopStartLifecycle(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := f.containers["cornus-web-0"]

	if err := b.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c.labels[restart.ExplicitlyStoppedLabel] != "true" {
		t.Fatal("Stop must set the explicitly-stopped label (restart monitor would resurrect the task)")
	}
	if c.task != nil {
		t.Fatal("Stop must delete the task")
	}
	st, _ := b.Status(context.Background(), "web")
	if st.Instances[0].Running {
		t.Fatalf("stopped instance reported running: %+v", st.Instances[0])
	}

	if err := b.Start(context.Background(), "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.labels[restart.ExplicitlyStoppedLabel] != "false" {
		t.Fatal("Start must clear the explicitly-stopped label")
	}
	st, _ = b.Status(context.Background(), "web")
	if !st.Instances[0].Running {
		t.Fatalf("started instance not running: %+v", st.Instances[0])
	}

	// Start when already running is a no-op.
	if err := b.Start(context.Background(), "web"); err != nil {
		t.Fatalf("Start while running: %v", err)
	}
	if err := b.Restart(context.Background(), "web"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	st, _ = b.Status(context.Background(), "web")
	if !st.Instances[0].Running {
		t.Fatalf("restarted instance not running: %+v", st.Instances[0])
	}
}

// TestStatusExitCode confirms Status surfaces a stopped task's exit status so
// compose service_completed_successfully gating has a code to check; a running
// task leaves ExitCode nil, and health is always "" (containerd has no
// healthcheck engine).
func TestStatusExitCode(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "job", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := f.containers["cornus-job-0"]

	// Running task: no exit code, no health.
	st, _ := b.Status(context.Background(), "job")
	if len(st.Instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(st.Instances))
	}
	if st.Instances[0].ExitCode != nil {
		t.Fatalf("running instance ExitCode = %v, want nil", *st.Instances[0].ExitCode)
	}
	if st.Instances[0].Health != "" {
		t.Fatalf("Health = %q, want empty (containerd has no healthcheck)", st.Instances[0].Health)
	}

	// Task exited with a non-zero status.
	c.task.status = ctd.Stopped
	c.task.exitStatus = 42
	st, _ = b.Status(context.Background(), "job")
	inst := st.Instances[0]
	if inst.Running {
		t.Fatalf("exited instance reported running: %+v", inst)
	}
	if inst.ExitCode == nil || *inst.ExitCode != 42 {
		t.Fatalf("ExitCode = %v, want 42", inst.ExitCode)
	}
	if inst.Health != "" {
		t.Fatalf("Health = %q, want empty", inst.Health)
	}
}

func TestListGroupsByApp(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "a", Image: "img", Replicas: 2}); err != nil {
		t.Fatalf("Apply a: %v", err)
	}
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "b", Image: "img"}); err != nil {
		t.Fatalf("Apply b: %v", err)
	}
	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("List = %+v", got)
	}
	if len(got[0].Instances) != 2 || len(got[1].Instances) != 1 {
		t.Fatalf("instance counts = %d, %d", len(got[0].Instances), len(got[1].Instances))
	}
}

func TestForwardPortEchoes(t *testing.T) {
	// A local echo server stands in for the instance; the fake IP label points
	// at it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(conn, conn)
		_ = conn.Close() // EOF back to the bridge so its output side ends
	}()
	port := l.Addr().(*net.TCPAddr).Port

	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.containers["cornus-web-0"].labels[labelIP] = "127.0.0.1"

	client, remote := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", port, "tcp", remote) }()

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
}

// TestForwardPortUDPEchoes drives the udp branch: a local UDP echo server
// stands in for the instance (via the fake IP label), and ForwardPort bridges
// length-prefixed datagram frames on the tunnel to a connected UDP socket.
func TestForwardPortUDPEchoes(t *testing.T) {
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, src, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(buf[:n], src)
		}
	}()
	port := echo.LocalAddr().(*net.UDPAddr).Port

	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.containers["cornus-web-0"].labels[labelIP] = "127.0.0.1"

	client, remote := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", port, "udp", remote) }()

	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := wire.WriteDatagram(client, []byte("ping-udp")); err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadDatagram(client)
	if err != nil || string(got) != "ping-udp" {
		t.Fatalf("udp echo = %q, %v", got, err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatalf("ForwardPort: %v", err)
	}
}

func TestForwardPortRejectsUnknownProto(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	client, remote := net.Pipe()
	defer client.Close()
	if err := b.ForwardPort(context.Background(), "web", 53, "sctp", remote); err == nil ||
		!strings.Contains(err.Error(), "only tcp and udp") {
		t.Fatalf("sctp forward error = %v", err)
	}
}

// TestSupportsUDPPortForward pins the capability the server's port-forward
// handler probes before acking a udp tunnel.
func TestSupportsUDPPortForward(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if !b.SupportsUDPPortForward() {
		t.Fatal("containerd must advertise UDP port-forward support")
	}
}

func TestExecCreateNeedsRunningInstance(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.ExecCreate(context.Background(), "ghost", api.ExecConfig{Cmd: []string{"true"}}); err == nil {
		t.Fatal("exec against a missing deployment must error")
	}

	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	id, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"true"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if st, err := b.ExecInspect(context.Background(), id); err != nil || st.Running {
		t.Fatalf("fresh exec state = %+v, %v", st, err)
	}
	if _, err := b.ExecInspect(context.Background(), "exec-unknown"); err == nil {
		t.Fatal("unknown exec id must error")
	}

	if err := b.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"true"}}); err == nil {
		t.Fatal("exec against a stopped instance must error")
	}
}

func TestExecProcessSpec(t *testing.T) {
	base, err := f0().Spec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := execProcessSpec(base, api.ExecConfig{
		Cmd:        []string{"sh", "-c", "id"},
		Env:        []string{"EXTRA=1"},
		WorkingDir: "/tmp",
		User:       "1000:1000",
		Tty:        true,
	})
	if err != nil {
		t.Fatalf("execProcessSpec: %v", err)
	}
	if p.Args[0] != "sh" || !p.Terminal || p.Cwd != "/tmp" {
		t.Fatalf("process = %+v", p)
	}
	if p.User.UID != 1000 || p.User.GID != 1000 {
		t.Fatalf("user = %+v", p.User)
	}
	joined := strings.Join(p.Env, ",")
	if !strings.Contains(joined, "BASE=1") || !strings.Contains(joined, "EXTRA=1") {
		t.Fatalf("env = %v", p.Env)
	}
	if _, err := execProcessSpec(base, api.ExecConfig{Cmd: []string{"id"}, User: "alice"}); err == nil {
		t.Fatal("non-numeric user must error")
	}
}

// f0 builds a detached fake container for pure spec tests.
func f0() *fakeContainer {
	return &fakeContainer{client: newFakeClient(), id: "x", labels: map[string]string{}}
}

func TestInstanceMountsAndAnonReap(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	spec := api.DeploySpec{
		Name:   "web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/srv/data", Target: "/data", ReadOnly: true}},
		Volumes: []api.VolumeSpec{
			{Name: "shared", Target: "/var/lib/shared"},
			{Target: "/var/cache"}, // anonymous
		},
	}
	mounts, vols, err := b.instanceMounts(spec, 0)
	if err != nil {
		t.Fatalf("instanceMounts: %v", err)
	}
	if len(mounts) != 3 || len(vols) != 2 {
		t.Fatalf("mounts=%d vols=%d", len(mounts), len(vols))
	}
	if mounts[0].Source != "/srv/data" || mounts[0].Options[1] != "ro" {
		t.Fatalf("host bind = %+v", mounts[0])
	}
	if mounts[1].Source != b.namedVolumeDir("shared") {
		t.Fatalf("named volume source = %q", mounts[1].Source)
	}
	if !strings.HasPrefix(mounts[2].Source, b.anonVolumesDir("web")) {
		t.Fatalf("anon volume source = %q", mounts[2].Source)
	}
	// Replica index differentiates anonymous backings; named ones are shared.
	mounts1, _, _ := b.instanceMounts(spec, 1)
	if mounts1[2].Source == mounts[2].Source {
		t.Fatal("anon volume must be per-replica")
	}
	if mounts1[1].Source != mounts[1].Source {
		t.Fatal("named volume must be shared across replicas")
	}

	// Reaping removes anon backings, keeps named.
	b.reapAnonymousVolumes("web")
	if _, err := ioStat(mounts[2].Source); err == nil {
		t.Fatal("anon volume dir should be gone")
	}
	if _, err := ioStat(mounts[1].Source); err != nil {
		t.Fatal("named volume dir must survive")
	}

	// A volume without a target is rejected.
	if _, _, err := b.instanceMounts(api.DeploySpec{Name: "x", Volumes: []api.VolumeSpec{{}}}, 0); err == nil {
		t.Fatal("volume without target must error")
	}
}

func TestStatusStateMapping(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := f.containers["cornus-web-0"]
	for _, tc := range []struct {
		status ctd.ProcessStatus
		want   string
		run    bool
	}{
		{ctd.Running, "running", true},
		{ctd.Created, "created", false},
		{ctd.Paused, "paused", false},
		{ctd.Stopped, "exited", false},
	} {
		c.task.status = tc.status
		st, err := b.Status(context.Background(), "web")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.Instances[0].State != tc.want || st.Instances[0].Running != tc.run {
			t.Fatalf("status %s -> %+v, want state=%s running=%v", tc.status, st.Instances[0], tc.want, tc.run)
		}
	}
}

func ioStat(path string) (any, error) { return os.Stat(path) }

// TestLifecycleMissingDeployment asserts the cross-backend contract: Stop,
// Start, and Restart of a name with no instances error wrapping
// deploy.ErrNotFound, while Delete stays delete-if-exists.
func TestLifecycleMissingDeployment(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	ctx := context.Background()
	for verb, fn := range map[string]func(context.Context, string) error{
		"Stop":    b.Stop,
		"Start":   b.Start,
		"Restart": b.Restart,
	} {
		if err := fn(ctx, "ghost"); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("%s(ghost) = %v, want error wrapping deploy.ErrNotFound", verb, err)
		}
	}
	if err := b.Delete(ctx, "ghost"); err != nil {
		t.Errorf("Delete(ghost) = %v, want nil (delete-if-exists)", err)
	}
}

// TestApplyRecreateReensuresReapedNetwork guards the recreate path: Apply's
// Delete reaps a user network whose sole member it just removed (deleting the
// conflist and freeing the subnet); createInstance must re-materialize it
// before attaching, or CNI setup fails and the deployment is left deleted.
func TestApplyRecreateReensuresReapedNetwork(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	ctx := context.Background()
	spec := api.DeploySpec{
		Name:     "web",
		Image:    "img",
		Networks: []api.NetworkAttachment{{Name: "web-net"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Re-apply with the same spec (recreate). The intermediate Delete reaps
	// web-net; without a re-ensure the createInstance loop's setup would fail.
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("second Apply (recreate): %v", err)
	}
	if len(f.containers) != 1 {
		t.Fatalf("containers = %d, want 1 (recreate must leave the instance up)", len(f.containers))
	}
	if !fn.materialized["web-net"] {
		t.Fatal("web-net conflist not re-materialized after recreate")
	}
}

// TestApplyRecordsPortsOnPublishingReplicaOnly guards netns repair: only the
// replica that actually publishes host ports may record them in labelPorts, or
// a reboot repair re-installs conflicting host-port DNAT on the other replicas.
func TestApplyRecordsPortsOnPublishingReplicaOnly(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{
		Name:     "web",
		Image:    "img",
		Replicas: 2,
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := f.containers["cornus-web-0"].labels[labelPorts]; got == "" {
		t.Fatal("replica 0 must record its published ports")
	}
	if got := f.containers["cornus-web-1"].labels[labelPorts]; got != "" {
		t.Fatalf("replica 1 must not record published ports (repair would re-DNAT host:8080), got %q", got)
	}
}

// TestCopyRejectsStoppedInstance guards docker-cp against trusting a stopped
// task's recorded (and possibly kernel-recycled) PID: procRoot must require the
// task to be Running, not merely present.
func TestCopyRejectsStoppedInstance(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	c := &fakeContainer{
		client: f,
		id:     "cornus-app-0",
		image:  "img",
		labels: map[string]string{deploy.LabelManaged: "true", deploy.LabelApp: "app"},
	}
	// A task record that exists but whose init already exited (crash, not yet
	// reaped): runningTask returns it, but its Pid must not be trusted.
	c.task = &fakeTask{container: c, status: ctd.Stopped, exitCh: make(chan ctd.ExitStatus, 1)}
	f.containers[c.id] = c

	if _, err := b.StatPath(context.Background(), "app", "/etc/hostname"); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Fatalf("StatPath on stopped instance = %v, want a 'not running' error", err)
	}
	if err := b.CopyTo(context.Background(), "app", "/etc/", strings.NewReader(""), api.CopyToOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Fatalf("CopyTo into stopped instance = %v, want a 'not running' error", err)
	}
}
