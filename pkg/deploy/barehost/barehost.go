// Package barehost implements cornus's deploy.Backend natively against a
// low-level OCI runtime CLI (runc, crun, youki) — no container daemon at all:
// no dockerd, no containerd, no kubelet. It owns everything a daemon used to
// provide: pulling images into an in-process content store, unpacking layers
// and assembling a container rootfs via a snapshotter, generating the OCI
// runtime spec (config.json), supervising the container process and applying
// its restart policy through a detached per-container shim (cornus's own
// conmon analogue), managing cgroups, and persisting logs.
//
// Architecturally this makes cornus its own Podman: daemonless, driving
// runc/crun directly. It is selected with CORNUS_DEPLOY_BACKEND=bare; the
// runtime binary is CORNUS_BARE_RUNTIME (default "runc"). It is additive — the
// dockerhost, containerd, and kubernetes backends are unchanged.
//
// Linux-only: the implementation lives behind //go:build linux, and New returns
// ErrUnsupported elsewhere (mirroring pkg/deploy/containerdhost and
// pkg/build/builder).
package barehost

import (
	"fmt"
	"os"

	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// Config configures the backend. Empty fields resolve from the environment.
type Config struct {
	// DataDir is cornus's data directory; the backend keeps its content store,
	// snapshots, bundles, per-instance state, logs, CNI state, hosts files, and
	// volumes under <DataDir>/bare/.
	DataDir string
	// Runtime is the OCI runtime binary driven via go-runc. Empty resolves
	// CORNUS_BARE_RUNTIME, then defaults to "runc". Any runc-CLI-compatible
	// binary works: "runc", "crun", "youki", "runsc" (gVisor), or an absolute
	// path. A runtime whose basename is "runsc"/"gvisor" is treated as sandboxed
	// (see resolveSandboxed): Stats reads runtime-native metrics and copy runs an
	// in-container tar, since the guest's cgroup accounting and filesystem are not
	// visible on the host. CORNUS_BARE_STATS_SOURCE overrides that detection.
	Runtime string
	// Snapshotter selects the containerd snapshotter used for image unpack and
	// the container rootfs. Empty resolves CORNUS_BARE_SNAPSHOTTER, then picks
	// "overlayfs" when the kernel supports it, else "native". Set it to "native"
	// when the cornus data dir itself sits on an overlay filesystem
	// (docker-in-docker, nested containers): the kernel rejects overlay-upon-
	// overlay mounts.
	Snapshotter string
}

// DefaultRuntime is the OCI runtime binary used when none is configured.
const DefaultRuntime = "runc"

// contentNamespace is the containerd namespace applied to the context for every
// content-store and snapshotter operation. The containerd libraries the backend
// reuses (content/local, snapshotters) require a namespace for their label/GC
// bookkeeping even though there is no containerd daemon; a single fixed value
// keeps pull, unpack, and spec generation consistent. It deliberately matches
// containerdhost's default namespace so a same-host image is addressed the same
// way regardless of which native backend touched it.
const contentNamespace = "cornus"

// runcStateRoot is the go-runc state root (--root). It lives on tmpfs under
// /run so a host reboot wipes the runtime's "running" view — exactly the
// semantic the reboot-recovery reconcile relies on, paralleling the tmpfs netns
// pins under /run/cornus/netns.
const runcStateRoot = "/run/cornus/bare-runc"

// resolve fills empty Config fields from the environment and defaults.
func (c Config) resolve() (Config, error) {
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("bare: Config.DataDir is required")
	}
	if c.Runtime == "" {
		c.Runtime = os.Getenv("CORNUS_BARE_RUNTIME")
	}
	if c.Runtime == "" {
		c.Runtime = DefaultRuntime
	}
	if c.Snapshotter == "" {
		c.Snapshotter = os.Getenv("CORNUS_BARE_SNAPSHOTTER")
	}
	return c, nil
}

// Option configures the backend.
type Option func(*options)

type options struct {
	policy     hostpolicy.Policy
	remote     bool
	agentImage string
	companions *remotecompanion.Registry
}

// WithPolicy sets the host-privilege policy enforced at Apply time. Without it,
// the zero policy applies: default-deny (no privileged containers, no host
// binds). Shared with dockerhost/containerdhost via pkg/deploy/hostpolicy.
func WithPolicy(p hostpolicy.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithRemote opts this backend into the caretaker-companion mount-relay path
// (CORNUS_BARE_REMOTE) instead of the co-located kernel-9p fast path, mirroring
// containerdhost.WithRemote. Like containerd — and unlike dockerhost — this does
// not make the runtime reachable from another host; it only changes how
// client-local mounts / port-forward / exec-agent-forwarding are realized.
func WithRemote(remote bool) Option {
	return func(o *options) { o.remote = remote }
}

// WithAgentImage sets the cornus-embedding image (CORNUS_AGENT_IMAGE) used for
// the always-on remote companion in remote mode. Consulted only with
// WithRemote(true).
func WithAgentImage(image string) Option {
	return func(o *options) { o.agentImage = image }
}

// WithCompanionRegistry sets the server's per-instance companion-connection
// registry (pkg/remotecompanion) so ForwardPort can reroute through a
// remote-mode instance's companion. Required for WithRemote(true) to make
// ForwardPort / cornus tunnel / cornus port-forward work.
func WithCompanionRegistry(r *remotecompanion.Registry) Option {
	return func(o *options) { o.companions = r }
}

// instanceName names replica i of a deployment, mirroring the dockerhost and
// containerd convention. It is also the OCI runtime container ID.
func instanceName(app string, i int) string { return fmt.Sprintf("cornus-%s-%d", app, i) }

// Labels recorded on every managed instance's persisted record, beyond
// deploy.LabelManaged and deploy.LabelApp. Unlike containerd (which stores these
// as container labels in its metadata DB), the bare backend has no daemon store,
// so the equivalents live in the per-instance state record (state_linux.go).
const (
	// labelNetworks records the instance's user-defined network memberships.
	labelNetworks = "cornus.networks"
	// labelIP records the instance's primary CNI-assigned IP.
	labelIP = "cornus.ip"
	// labelNetNS records the instance's named-netns bind-mount path.
	labelNetNS = "cornus.netns"
	// labelPorts records the instance's published port mappings as JSON.
	labelPorts = "cornus.ports"
	// labelNetIPs records the instance's per-network CNI-assigned IPs as JSON.
	labelNetIPs = "cornus.netips"
	// labelAliases records the spec's per-network aliases as JSON.
	labelAliases = "cornus.aliases"
)
