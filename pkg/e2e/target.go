package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	containerd "github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"cornus/pkg/deploy"
)

// Target is a deployment environment the harness drives cornus against: a
// Docker host or a kind-managed Kubernetes cluster. It supplies the environment
// `cornus serve` needs to use the matching deploy backend, and prepares built
// images so the target can run them.
type Target interface {
	Name() string
	// Setup provisions the target (e.g. create the kind cluster).
	Setup(ctx context.Context) error
	// Teardown releases the target (e.g. delete the kind cluster).
	Teardown(ctx context.Context) error
	// ServeEnv is appended to the environment of the `cornus serve` process.
	ServeEnv() []string
	// PrepareImage makes a freshly built+pushed image usable by the target.
	PrepareImage(ctx context.Context, ref string) error
}

// --- local (no deployment environment) --------------------------------------

// LocalTarget runs scenarios with no external deployment environment: the
// cornus server uses its default backend (never invoked unless the scenario
// deploys). Useful for build-only scenarios that exercise the build engine
// without Docker or kind.
type LocalTarget struct{}

func (t *LocalTarget) Name() string                   { return "local" }
func (t *LocalTarget) Setup(context.Context) error    { return nil }
func (t *LocalTarget) Teardown(context.Context) error { return nil }

// ServeEnv keeps the classic persistent-CAS registry. With no deploy backend
// set, the server defaults to dockerhost, where an unset CORNUS_REGISTRY_SOURCE
// now defaults to host-native re-export — but the registry scenarios exercise the
// CAS (push/catalog/persistence), so opt out explicitly. The host-native scenario
// sets its own value, which wins (serve(env=) is appended last).
func (t *LocalTarget) ServeEnv() []string                         { return []string{"CORNUS_REGISTRY_SOURCE=off"} }
func (t *LocalTarget) PrepareImage(context.Context, string) error { return nil }

// --- docker host ------------------------------------------------------------

// DockerTarget deploys via the dockerhost backend against the host's Docker
// daemon (DOCKER_HOST, default the local socket).
type DockerTarget struct {
	gateway string // default "bridge" network's gateway: how a mount-relay caretaker companion (default bridge network, no NetworkMode override) reaches a host-run server
}

func (t *DockerTarget) Name() string { return "docker" }

func (t *DockerTarget) Setup(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker not reachable (set DOCKER_HOST?): %v: %s", err, strings.TrimSpace(string(out)))
	}
	// The dockerhost backend's mount-relay caretaker companion (CORNUS_DOCKER_REMOTE,
	// pkg/deploy/dockerhost/mounts.go) runs on the default bridge network with no
	// NetworkMode override — it only needs outbound reachability to the cornus
	// server, which from inside that network is the bridge's own gateway IP, NOT
	// 127.0.0.1 (a bridge-network container has its own loopback). Mirrors the kind
	// gateway discovery below for KubeTarget. Best-effort: leaves gateway empty
	// (serve() then binds/advertises nothing extra) if docker can't be inspected.
	if g, err := exec.CommandContext(ctx, "docker", "network", "inspect", "bridge", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").CombinedOutput(); err == nil {
		for _, f := range strings.Fields(string(g)) {
			if strings.Contains(f, ".") && !strings.Contains(f, ":") {
				t.gateway = f
				break
			}
		}
	}
	return nil
}

// AdvertiseHost returns the address a mount-relay caretaker companion (default
// bridge network) can use to reach a host-run cornus server — the docker
// bridge gateway — or "" if it could not be discovered.
func (t *DockerTarget) AdvertiseHost() string { return t.gateway }

func (t *DockerTarget) Teardown(context.Context) error { return nil }

func (t *DockerTarget) ServeEnv() []string {
	// Scenarios exercise host bind mounts through the stateless deploy path
	// (e.g. deploy-config.star), which the backend default-denies. Permit any
	// absolute source for the test server; production stays default-deny.
	env := []string{
		"CORNUS_DEPLOY_BACKEND=dockerhost",
		"CORNUS_ALLOW_BIND_SOURCES=/",
		// The registry scenarios exercise the persistent CAS (push/catalog/
		// persistence); an unset source now defaults to host-native re-export on
		// dockerhost, so opt out. The host-native scenario overrides this (serve(env=)
		// wins).
		"CORNUS_REGISTRY_SOURCE=off",
	}
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		env = append(env, "DOCKER_HOST="+h)
	}
	return env
}

// PrepareImage is a no-op: the dockerhost backend pulls from cornus's registry
// on the same host.
func (t *DockerTarget) PrepareImage(context.Context, string) error { return nil }

// --- containerd host ----------------------------------------------------------

// ContainerdTarget deploys via the containerd backend against the host's
// containerd daemon. Zero values resolve like the backend does: Address from
// CORNUS_CONTAINERD_ADDRESS (default /run/containerd/containerd.sock),
// Namespace from CORNUS_CONTAINERD_NAMESPACE (default "cornus"); the CLI sets
// Namespace to "cornus-e2e" so test state never mixes with a production one.
type ContainerdTarget struct {
	Address   string
	Namespace string
}

func (t *ContainerdTarget) Name() string { return "containerd" }

func (t *ContainerdTarget) addr() string {
	if t.Address != "" {
		return t.Address
	}
	return containerdAddressFromEnv()
}

func (t *ContainerdTarget) ns() string {
	if t.Namespace != "" {
		return t.Namespace
	}
	if n := os.Getenv("CORNUS_CONTAINERD_NAMESPACE"); n != "" {
		return n
	}
	return "cornus"
}

// AdvertiseHost returns the address the containerd backend's mount-relay
// caretaker companion (CORNUS_CONTAINERD_REMOTE, pkg/deploy/containerdhost/
// mounts_linux.go) can use to reach a host-run cornus server. Unlike
// dockerhost's companion, this one runs in the HOST's own network namespace
// (withoutNamespace(NetworkNamespace) — it needs no per-container network
// identity), so the loopback address always works; no gateway discovery is
// needed.
func (t *ContainerdTarget) AdvertiseHost() string { return "127.0.0.1" }

// containerdAddressFromEnv resolves the daemon socket the way the backend does:
// CORNUS_CONTAINERD_ADDRESS, then the stock socket path.
func containerdAddressFromEnv() string {
	if a := os.Getenv("CORNUS_CONTAINERD_ADDRESS"); a != "" {
		return a
	}
	return "/run/containerd/containerd.sock"
}

// cniPluginDirs mirrors the backend's CNI plugin-dir resolution
// (pkg/deploy/containerdhost): CORNUS_CNI_BIN_DIR, then the standard CNI_PATH
// list, then /opt/cni/bin.
func cniPluginDirs() []string {
	if d := os.Getenv("CORNUS_CNI_BIN_DIR"); d != "" {
		return []string{d}
	}
	if p := os.Getenv("CNI_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	return []string{"/opt/cni/bin"}
}

// requiredCNIPlugins are the plugin binaries the containerd backend needs for
// its networking (bridge network + host port publishing).
var requiredCNIPlugins = []string{"bridge", "portmap", "host-local", "loopback"}

func (t *ContainerdTarget) Setup(ctx context.Context) error {
	addr := t.addr()
	client, err := containerd.New(addr, containerd.WithTimeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("containerd not reachable at %s: %w (is containerd running? the socket usually needs root; set CORNUS_CONTAINERD_ADDRESS)", addr, err)
	}
	defer client.Close()
	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := client.Version(vctx); err != nil {
		return fmt.Errorf("containerd at %s did not answer Version: %w (run as root or fix the socket permissions)", addr, err)
	}
	// The backend refuses to network workloads without the CNI reference
	// plugins, so fail here with the same actionable message instead of deep
	// inside the first deploy.
	dirs := cniPluginDirs()
	var missing []string
	for _, p := range requiredCNIPlugins {
		found := false
		for _, d := range dirs {
			if st, err := os.Stat(filepath.Join(d, p)); err == nil && !st.IsDir() {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing CNI plugins %s under %s (install the CNI plugins package, or point CORNUS_CNI_BIN_DIR/CNI_PATH at them)",
			strings.Join(missing, ", "), strings.Join(dirs, ":"))
	}
	return nil
}

// Teardown is best-effort cleanup: delete any leftover cornus-managed
// containers in the target namespace so a rerun starts clean. Errors are
// ignored — a scenario that tore itself down leaves nothing to do.
func (t *ContainerdTarget) Teardown(ctx context.Context) error {
	client, err := containerd.New(t.addr(), containerd.WithTimeout(5*time.Second))
	if err != nil {
		return nil
	}
	defer client.Close()
	nctx, cancel := context.WithTimeout(namespaces.WithNamespace(ctx, t.ns()), 30*time.Second)
	defer cancel()
	cs, err := client.Containers(nctx, fmt.Sprintf(`labels.%q==%q`, deploy.LabelManaged, "true"))
	if err != nil {
		return nil
	}
	for _, c := range cs {
		if task, err := c.Task(nctx, nil); err == nil {
			_, _ = task.Delete(nctx, containerd.WithProcessKill)
		}
		_ = c.Delete(nctx, containerd.WithSnapshotCleanup)
	}
	return nil
}

func (t *ContainerdTarget) ServeEnv() []string {
	// CORNUS_ALLOW_BIND_SOURCES: same rationale as the docker target — the
	// stateless host-bind scenarios need it; production stays default-deny.
	env := []string{
		"CORNUS_DEPLOY_BACKEND=containerd",
		"CORNUS_CONTAINERD_ADDRESS=" + t.addr(),
		"CORNUS_CONTAINERD_NAMESPACE=" + t.ns(),
		"CORNUS_BUILD_WORKER=containerd",
		"CORNUS_ALLOW_BIND_SOURCES=/",
		// Keep the classic CAS: an unset source now defaults to host-native
		// re-export on containerd too, but the registry scenarios exercise the CAS.
		"CORNUS_REGISTRY_SOURCE=off",
	}
	for _, k := range []string{"CORNUS_CNI_BIN_DIR", "CNI_PATH"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// PrepareImage is a no-op: the containerd backend pulls from cornus's registry
// on the same host (localhost refs are resolved over plain HTTP automatically).
func (t *ContainerdTarget) PrepareImage(context.Context, string) error { return nil }

// --- bare (daemonless OCI runtime) --------------------------------------------

// bareRuncStateRoot mirrors pkg/deploy/barehost's runcStateRoot: the tmpfs state
// root (`--root`) the bare backend hands the OCI runtime. Kept in sync by hand —
// the harness only needs it for best-effort Teardown cleanup, not correctness.
const bareRuncStateRoot = "/run/cornus/bare-runc"

// BareTarget deploys via the bare backend, which drives a low-level OCI runtime
// (runc/crun/youki) directly with NO container daemon — no dockerd, no
// containerd. Runtime resolves like the backend does: from Runtime, then
// CORNUS_BARE_RUNTIME, then "runc". Like the containerd target it needs the CNI
// reference plugins and root (for the overlay snapshotter mount, per-instance
// netns, and CNI); unlike it there is no daemon to reach.
type BareTarget struct {
	Runtime     string
	Snapshotter string
}

func (t *BareTarget) Name() string { return "bare" }

// runtimeBin resolves the OCI runtime binary the backend will drive.
func (t *BareTarget) runtimeBin() string {
	if t.Runtime != "" {
		return t.Runtime
	}
	return bareRuntimeFromEnv()
}

// bareRuntimeFromEnv resolves the OCI runtime binary the way the backend does:
// CORNUS_BARE_RUNTIME, then "runc".
func bareRuntimeFromEnv() string {
	if r := os.Getenv("CORNUS_BARE_RUNTIME"); r != "" {
		return r
	}
	return "runc"
}

// AdvertiseHost returns an address the bare backend's remote companion can dial
// to reach a host-run server. The companion shares the app instance's pinned
// netns, so it CANNOT reach the server on 127.0.0.1 (that is the netns's own
// loopback); it must use a routable host address. The CNI bridge's ipMasq lets
// the netns reach the harness host's primary IP, and serve() binds all
// interfaces, so the companion reaches the server there. Falls back to loopback
// if no routable address is found (a companion scenario then self-skips on the
// unreachable relay rather than the harness failing).
func (t *BareTarget) AdvertiseHost() string { return routableHostIP() }

// routableHostIP returns the harness host's primary non-loopback IPv4 address by
// asking the kernel which local address a UDP socket to a public IP would use (no
// packet is sent). Empty/"127.0.0.1" if none is found.
func routableHostIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua.IP != nil && !ua.IP.IsLoopback() {
		return ua.IP.String()
	}
	return "127.0.0.1"
}

func (t *BareTarget) Setup(ctx context.Context) error {
	// The backend shells out to the runtime binary (via go-runc), so it must be
	// present — fail here with the same actionable message the backend's New()
	// gives, instead of deep inside the first deploy.
	bin := t.runtimeBin()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("OCI runtime %q not found on PATH: %w (install runc/crun/youki/runsc or set CORNUS_BARE_RUNTIME)", bin, err)
	}
	// Rootless is out of scope for this milestone: the overlay snapshotter mount,
	// per-instance netns, and CNI plugins all need root.
	if os.Geteuid() != 0 {
		return fmt.Errorf("the bare target requires root (overlay snapshotter mount, network namespaces, CNI); rootless is not supported")
	}
	// Same CNI reference-plugin requirement as the containerd backend — the bare
	// backend reuses that networking. Fail with the same actionable message.
	dirs := cniPluginDirs()
	var missing []string
	for _, p := range requiredCNIPlugins {
		found := false
		for _, d := range dirs {
			if st, err := os.Stat(filepath.Join(d, p)); err == nil && !st.IsDir() {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing CNI plugins %s under %s (install the CNI plugins package, or point CORNUS_CNI_BIN_DIR/CNI_PATH at them)",
			strings.Join(missing, ", "), strings.Join(dirs, ":"))
	}
	return nil
}

// Teardown is best-effort cleanup: delete any leftover cornus-managed containers
// under the bare runtime state root so a rerun starts clean. There is no daemon
// to query, so it lists via the runtime CLI itself. Errors are ignored.
func (t *BareTarget) Teardown(ctx context.Context) error {
	bin := t.runtimeBin()
	out, err := exec.CommandContext(ctx, bin, "--root", bareRuncStateRoot, "list", "-q").Output()
	if err != nil {
		return nil
	}
	for _, id := range strings.Fields(string(out)) {
		if strings.HasPrefix(id, "cornus-") {
			_ = exec.CommandContext(ctx, bin, "--root", bareRuncStateRoot, "delete", "--force", id).Run()
		}
	}
	return nil
}

func (t *BareTarget) ServeEnv() []string {
	// CORNUS_ALLOW_BIND_SOURCES: same rationale as the docker/containerd targets —
	// the stateless host-bind scenarios need it; production stays default-deny.
	env := []string{
		"CORNUS_DEPLOY_BACKEND=bare",
		"CORNUS_BARE_RUNTIME=" + t.runtimeBin(),
		"CORNUS_ALLOW_BIND_SOURCES=/",
	}
	if t.Snapshotter != "" {
		env = append(env, "CORNUS_BARE_SNAPSHOTTER="+t.Snapshotter)
	} else if s := os.Getenv("CORNUS_BARE_SNAPSHOTTER"); s != "" {
		env = append(env, "CORNUS_BARE_SNAPSHOTTER="+s)
	}
	for _, k := range []string{"CORNUS_CNI_BIN_DIR", "CNI_PATH", "CORNUS_BARE_INSECURE_REGISTRIES"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// PrepareImage is a no-op: the bare backend pulls from cornus's registry on the
// same host into its own content store (localhost refs are plain-HTTP
// automatically). Unlike the containerd target it needs no image-store sharing —
// the build engine's default worker pushes to the registry and the backend pulls
// it back.
func (t *BareTarget) PrepareImage(context.Context, string) error { return nil }

// --- kind / kubernetes ------------------------------------------------------

// KubeTarget deploys via the kubernetes backend into a kind cluster. Built
// images are pulled from cornus's registry and loaded into the cluster's nodes
// ("kind load image-archive") so pods run them with imagePullPolicy IfNotPresent.
type KubeTarget struct {
	Cluster    string
	Namespace  string
	Keep       bool
	work       string
	kubeconfig string
	gateway    string // kind docker-network gateway: how in-cluster pods reach a host-run server
}

func (t *KubeTarget) Name() string { return "kube" }

func (t *KubeTarget) Setup(ctx context.Context) error {
	if t.Cluster == "" {
		t.Cluster = "cornus-e2e"
	}
	if t.Namespace == "" {
		t.Namespace = "cornus-e2e"
	}
	dir, err := os.MkdirTemp("", "cornus-e2e-kube-")
	if err != nil {
		return err
	}
	t.work = dir
	t.kubeconfig = filepath.Join(dir, "kubeconfig")
	// Remove the temp dir (and its 0600 kubeconfig) if Setup fails partway: the
	// caller (cmd/cornus-e2e) registers Teardown only after a successful Setup, so
	// without this an aborted Setup would orphan the dir for the process lifetime.
	success := false
	defer func() {
		if !success {
			os.RemoveAll(dir)
			t.work = ""
		}
	}()

	if !t.clusterExists(ctx) {
		if out, err := t.run(ctx, "kind", "create", "cluster", "--name", t.Cluster); err != nil {
			return fmt.Errorf("kind create cluster: %v: %s", err, out)
		}
	}
	out, err := t.run(ctx, "kind", "get", "kubeconfig", "--name", t.Cluster)
	if err != nil {
		return fmt.Errorf("kind get kubeconfig: %v: %s", err, out)
	}
	if err := os.WriteFile(t.kubeconfig, []byte(out), 0o600); err != nil {
		return err
	}
	// Ensure the namespace exists (ignore "already exists").
	_, _ = t.kubectl(ctx, "create", "namespace", t.Namespace)
	// Install Knative Serving into the cluster when asked (E2E_KNATIVE=1), so the
	// deploy-knative scenario can round-trip a real ksvc on this direct
	// `make e2e-kube` path — otherwise it self-skips. The containerized runner
	// installs it the same way via its entrypoint; both share install-knative.sh.
	if os.Getenv("E2E_KNATIVE") == "1" {
		if err := t.installKnative(ctx); err != nil {
			return fmt.Errorf("install knative: %w", err)
		}
	}
	// The kind docker-network gateway is how an in-cluster pod reaches a
	// host-run cornus server (for the mount-agent relay). The network is
	// dual-stack, so pick the IPv4 gateway (the server binds IPv4). Best-effort.
	if g, err := t.run(ctx, "docker", "network", "inspect", "kind", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}"); err == nil {
		fields := strings.Fields(g)
		// Prefer the IPv4 gateway (the routable one in typical kind/docker), but
		// never assume it exists: fall back to whatever gateway the network has
		// (e.g. an IPv6-only cluster). The server binds both families and the
		// advertised URL brackets IPv6, so either works.
		for _, f := range fields {
			if strings.Contains(f, ".") && !strings.Contains(f, ":") {
				t.gateway = f
				break
			}
		}
		if t.gateway == "" && len(fields) > 0 {
			t.gateway = fields[0]
		}
	}
	success = true
	return nil
}

// AdvertiseHost returns the address an in-cluster pod can use to reach a
// host-run cornus server (the kind docker-network gateway), or "" if unknown.
func (t *KubeTarget) AdvertiseHost() string { return t.gateway }

// NS returns the target namespace (for the harness pod_exec builtin).
func (t *KubeTarget) NS() string { return t.Namespace }

func (t *KubeTarget) Teardown(ctx context.Context) error {
	if t.Keep {
		return nil
	}
	if t.work != "" {
		defer os.RemoveAll(t.work)
	}
	if out, err := t.run(ctx, "kind", "delete", "cluster", "--name", t.Cluster); err != nil {
		return fmt.Errorf("kind delete cluster: %v: %s", err, out)
	}
	return nil
}

func (t *KubeTarget) ServeEnv() []string {
	return []string{
		"CORNUS_DEPLOY_BACKEND=kubernetes",
		"KUBECONFIG=" + t.kubeconfig,
		"CORNUS_K8S_NAMESPACE=" + t.Namespace,
		"CORNUS_K8S_IMAGE_PULL_POLICY=IfNotPresent",
		// The caretaker / net-redirect sidecars run the cornus binary, so they
		// use the cornus:e2e image regardless of the workload's app image.
		"CORNUS_K8S_SIDECAR_IMAGE=cornus:e2e",
	}
}

// PrepareImage pulls ref from cornus's registry to a tarball and loads it into
// the kind cluster's nodes.
func (t *KubeTarget) PrepareImage(ctx context.Context, ref string) error {
	img, err := crane.Pull(ref, crane.Insecure)
	if err != nil {
		return fmt.Errorf("crane pull %s: %w", ref, err)
	}
	tag, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		return err
	}
	archive := filepath.Join(t.work, "image.tar")
	if err := tarball.WriteToFile(archive, tag, img); err != nil {
		return fmt.Errorf("write image archive: %w", err)
	}
	if out, err := t.run(ctx, "kind", "load", "image-archive", archive, "--name", t.Cluster); err != nil {
		return fmt.Errorf("kind load image-archive: %v: %s", err, out)
	}
	return nil
}

// Kubeconfig returns the path to the cluster's kubeconfig (for the harness's
// kubectl builtin).
func (t *KubeTarget) Kubeconfig() string { return t.kubeconfig }

func (t *KubeTarget) clusterExists(ctx context.Context) bool {
	out, err := t.run(ctx, "kind", "get", "clusters")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == t.Cluster {
			return true
		}
	}
	return false
}

func (t *KubeTarget) kubectl(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--kubeconfig", t.kubeconfig, "-n", t.Namespace}, args...)
	return t.run(ctx, "kubectl", full...)
}

// installKnative runs the shared install script against this target's cluster,
// pointing kubectl at the target's kubeconfig. The script path is repo-relative
// (the harness runs from the repo root, as with the scenario paths it is given);
// it fetches upstream manifests, so it needs network access.
func (t *KubeTarget) installKnative(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "bash", "e2e/container/install-knative.sh")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+t.kubeconfig)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (t *KubeTarget) run(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// --- incus -------------------------------------------------------------------

// IncusTarget deploys via the incus backend against the host's Incus daemon,
// running cornus images as Incus OCI application containers. Socket/Project
// resolve like the backend does (CORNUS_INCUS_SOCKET / CORNUS_INCUS_PROJECT,
// then the incushost defaults). It drives the `incus` CLI for setup/teardown so
// the e2e package need not import the incus Go client.
type IncusTarget struct {
	Socket  string
	Project string
}

func (t *IncusTarget) Name() string { return "incus" }

func (t *IncusTarget) socket() string {
	if t.Socket != "" {
		return t.Socket
	}
	if s := os.Getenv("CORNUS_INCUS_SOCKET"); s != "" {
		return s
	}
	return "/var/lib/incus/unix.socket"
}

func (t *IncusTarget) project() string {
	if t.Project != "" {
		return t.Project
	}
	if p := os.Getenv("CORNUS_INCUS_PROJECT"); p != "" {
		return p
	}
	return "default"
}

// Setup verifies the Incus daemon is reachable, failing fast with an actionable
// message (the CapIncus preflight probe already checked incus/skopeo/umoci).
func (t *IncusTarget) Setup(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(cctx, "incus", "info").CombinedOutput(); err != nil {
		return fmt.Errorf("incus daemon not reachable: %v: %s (is incusd running and the socket accessible? usually root or the incus group)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AdvertiseHost returns the address the mount/egress caretaker companion uses to
// reach a host-run cornus server. Like the bare target's companion (which shares
// the app instance's network namespace), a routable host IP is used rather than
// loopback; refined when the companion path lands (Phase 2).
func (t *IncusTarget) AdvertiseHost() string { return routableHostIP() }

// Teardown best-effort deletes leftover cornus-managed instances so a rerun
// starts clean. Errors are ignored.
func (t *IncusTarget) Teardown(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "incus", "list", "--project", t.project(), "--format", "csv", "-c", "n").Output()
	if err != nil {
		return nil
	}
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, "cornus-") {
			_ = exec.CommandContext(ctx, "incus", "delete", "--project", t.project(), "--force", name).Run()
		}
	}
	return nil
}

func (t *IncusTarget) ServeEnv() []string {
	// CORNUS_ALLOW_BIND_SOURCES: same rationale as the other host targets — the
	// stateless host-bind scenarios need it; production stays default-deny.
	// CORNUS_INCUS_INSECURE_REGISTRIES lets Incus pull cornus's own registry over
	// plain HTTP (localhost is already treated as insecure by the backend).
	env := []string{
		"CORNUS_DEPLOY_BACKEND=incus",
		"CORNUS_INCUS_SOCKET=" + t.socket(),
		"CORNUS_INCUS_PROJECT=" + t.project(),
		"CORNUS_ALLOW_BIND_SOURCES=/",
		"CORNUS_REGISTRY_SOURCE=off",
	}
	if r := os.Getenv("CORNUS_INCUS_INSECURE_REGISTRIES"); r != "" {
		env = append(env, "CORNUS_INCUS_INSECURE_REGISTRIES="+r)
	}
	return env
}

// PrepareImage is a no-op when Incus pulls directly from cornus's registry
// (the Phase-0 assumption). If a live incusd proves it cannot pull the
// localhost/plain-HTTP ref, side-load the image into Incus's image store here
// (see .agents/docs/LTM/incus-backend.md), mirroring KubeTarget.PrepareImage.
func (t *IncusTarget) PrepareImage(context.Context, string) error { return nil }
