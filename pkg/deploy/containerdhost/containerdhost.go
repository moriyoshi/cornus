// Package containerdhost implements cornus's deploy.Backend natively against a
// containerd daemon — no dockerd required. It speaks the containerd v1 client
// API in a dedicated namespace, realizes networking with CNI bridge + portmap
// plugins (each instance gets its own netns and IP; host ports publish via
// portmap; service names and network aliases resolve between instances through
// a synced, bind-mounted /etc/hosts — see hosts_linux.go), persists logs
// through a `cornus containerd-log-shim` binary log URI
// so they survive cornus restarts, and delegates restart policy to containerd's
// restart-monitor plugin via containerd.io/restart labels.
//
// Linux-only: the implementation lives behind //go:build linux, and New returns
// ErrUnsupported elsewhere (mirroring pkg/build/builder).
package containerdhost

import (
	"fmt"
	"os"

	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// Config configures the backend. Empty fields resolve from the environment.
type Config struct {
	// DataDir is cornus's data directory; the backend keeps volumes, logs, and
	// CNI state under <DataDir>/containerd/.
	DataDir string
	// Address is the containerd socket. Empty resolves CORNUS_CONTAINERD_ADDRESS,
	// then the standard CONTAINERD_ADDRESS, then /run/containerd/containerd.sock.
	Address string
	// Namespace is the containerd namespace cornus manages. Empty resolves
	// CORNUS_CONTAINERD_NAMESPACE, default "cornus". The build engine's
	// containerd worker defaults to the same namespace so built images are
	// directly runnable without a registry round-trip.
	Namespace string
	// Snapshotter names the containerd snapshotter used for image unpack and
	// container rootfs snapshots. Empty resolves CORNUS_CONTAINERD_SNAPSHOTTER,
	// then containerd's compile-time default (overlayfs on linux). Set it to
	// "native" when the containerd root itself sits on an overlay filesystem
	// (docker-in-docker, nested containers): the kernel rejects overlay-upon-
	// overlay mounts, which surfaces as "failed to mount rootfs component:
	// invalid argument" at task start.
	Snapshotter string
}

// DefaultAddress is the stock containerd socket path.
const DefaultAddress = "/run/containerd/containerd.sock"

// DefaultNamespace is the containerd namespace cornus uses when none is set.
const DefaultNamespace = "cornus"

// resolve fills empty Config fields from the environment and defaults.
func (c Config) resolve() (Config, error) {
	if c.Address == "" {
		c.Address = os.Getenv("CORNUS_CONTAINERD_ADDRESS")
	}
	if c.Address == "" {
		c.Address = os.Getenv("CONTAINERD_ADDRESS")
	}
	if c.Address == "" {
		c.Address = DefaultAddress
	}
	if c.Namespace == "" {
		c.Namespace = os.Getenv("CORNUS_CONTAINERD_NAMESPACE")
	}
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace
	}
	if c.Snapshotter == "" {
		c.Snapshotter = os.Getenv("CORNUS_CONTAINERD_SNAPSHOTTER")
	}
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("containerd: Config.DataDir is required")
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
// the zero policy applies: default-deny (no privileged containers, no host binds).
func WithPolicy(p hostpolicy.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithRemote opts this Backend into the caretaker-sidecar mount-relay path
// (ApplyWithMounts, mounts_linux.go) instead of the default co-located fast
// path (applyWithHostMounts, in pkg/server). Unlike dockerhost, this does NOT
// make containerd itself reachable from a different host — its client dialer
// is hard-coded to a local unix socket — so this only changes how client-local
// mounts are realized on an otherwise still-co-located daemon (e.g. to avoid
// the server needing a privileged kernel mount of its own). See
// CORNUS_CONTAINERD_REMOTE.
func WithRemote(remote bool) Option {
	return func(o *options) { o.remote = remote }
}

// WithAgentImage sets the cornus-embedding image (CORNUS_AGENT_IMAGE) used for
// the always-on remote-companion sidecar in remote mode (see
// mounts_linux.go). Consulted only when WithRemote(true) is also set;
// ApplyWithMounts/ApplyWithEgress take their own AgentImage per call instead
// (unaffected).
func WithAgentImage(image string) Option {
	return func(o *options) { o.agentImage = image }
}

// WithCompanionRegistry sets the server's per-instance companion-connection
// registry (pkg/remotecompanion), so ForwardPort can look up a remote-mode
// instance's companion connection instead of dialing it directly. Required
// for WithRemote(true) to make ForwardPort/cornus tunnel/cornus port-forward
// work; nil (the zero value) makes ForwardPort always error in remote mode.
func WithCompanionRegistry(r *remotecompanion.Registry) Option {
	return func(o *options) { o.companions = r }
}

// instanceName names replica i of a deployment, mirroring the dockerhost
// convention. It is also the containerd container ID.
func instanceName(app string, i int) string { return fmt.Sprintf("cornus-%s-%d", app, i) }

// Labels recorded on every managed container, beyond deploy.LabelManaged and
// deploy.LabelApp.
const (
	// labelNetworks records the instance's user-defined network memberships
	// (comma-joined) so Delete can garbage-collect networks whose last member
	// is gone without inspecting every container.
	labelNetworks = "cornus.networks"
	// labelIP records the instance's primary CNI-assigned IP for Status and
	// ForwardPort.
	labelIP = "cornus.ip"
	// labelNetNS records the instance's named-netns bind-mount path so
	// Start/Delete can find it across cornus restarts.
	labelNetNS = "cornus.netns"
	// labelPorts records the instance's published port mappings as JSON so CNI
	// teardown can release the portmap rules without the original spec.
	labelPorts = "cornus.ports"
	// labelNetIPs records the instance's per-network CNI-assigned IPs as JSON
	// (network -> IP) so the hosts-file sync can publish the right address on
	// each shared network.
	labelNetIPs = "cornus.netips"
	// labelAliases records the spec's per-network aliases as JSON (network ->
	// aliases) so the hosts-file sync can rebuild the name map without the
	// original spec.
	labelAliases = "cornus.aliases"
)
