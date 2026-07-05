// Package incushost implements cornus's deploy.Backend against an Incus daemon
// (https://linuxcontainers.org/incus/). It deploys OCI images as Incus
// application containers (Incus 6.3+ can run OCI images directly, given skopeo
// and umoci on the daemon host), sitting alongside the dockerhost, containerd,
// bare, and kubernetes backends behind the same deploy.Backend interface.
//
// It is selected with CORNUS_DEPLOY_BACKEND=incus and talks to the daemon over
// its REST API (the local unix socket by default) using the official Go client
// github.com/lxc/incus/v6/client.
//
// Like containerdhost and barehost the implementation lives behind
// //go:build linux (Incus only runs on Linux); New returns ErrUnsupported
// elsewhere. Everything a unit test needs is behind the incusConn seam
// (backend_linux.go), which a fake replaces so the default `go test ./...` path
// never needs a live incusd.
package incushost

import (
	"fmt"
	"os"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// Config configures the backend. Empty fields resolve from the environment.
type Config struct {
	// DataDir is cornus's data directory. The incus backend keeps almost no
	// local state of its own (the daemon owns instances and their storage), but
	// the field is carried for parity with the other host backends and any future
	// per-instance bookkeeping under <DataDir>/incus/.
	DataDir string
	// Socket is the incus daemon unix socket. Empty resolves CORNUS_INCUS_SOCKET,
	// then defaults to DefaultSocket.
	Socket string
	// Project is the incus project instances are created in. Empty resolves
	// CORNUS_INCUS_PROJECT, then defaults to "default".
	Project string
	// Pool is the incus storage pool this backend creates custom volumes in: a
	// deployment's managed volumes (DeploySpec.Volumes, see volumes_linux.go) and,
	// in remote mode, the companion's shared agent volume (companion_linux.go).
	// Empty resolves CORNUS_INCUS_STORAGE_POOL, then defaults to DefaultPool. A
	// deployment with neither still touches no pool: its instances take their root
	// disk from the project's profile as before.
	Pool string
}

// DefaultSocket is the incus daemon unix socket used when none is configured.
const DefaultSocket = "/var/lib/incus/unix.socket"

// DefaultProject is the incus project used when none is configured.
const DefaultProject = "default"

// DefaultPool is the incus storage pool used when none is configured — the
// pool name `incus admin init` creates.
const DefaultPool = "default"

// resolve fills empty Config fields from the environment and defaults.
func (c Config) resolve() Config {
	if c.Socket == "" {
		c.Socket = os.Getenv("CORNUS_INCUS_SOCKET")
	}
	if c.Socket == "" {
		c.Socket = DefaultSocket
	}
	if c.Project == "" {
		c.Project = os.Getenv("CORNUS_INCUS_PROJECT")
	}
	if c.Project == "" {
		c.Project = DefaultProject
	}
	if c.Pool == "" {
		c.Pool = os.Getenv("CORNUS_INCUS_STORAGE_POOL")
	}
	if c.Pool == "" {
		c.Pool = DefaultPool
	}
	return c
}

// Option configures the backend.
type Option func(*options)

type options struct {
	policy     hostpolicy.Policy
	remote     bool
	agentImage string
	companions *remotecompanion.Registry
	creds      deploy.RegistryCredentials
}

// WithPolicy sets the host-privilege policy enforced at Apply time. Without it
// the zero policy applies: default-deny (no privileged containers, no host
// binds). Shared with the other host backends via pkg/deploy/hostpolicy.
func WithPolicy(p hostpolicy.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithRemote turns on the always-on caretaker-companion path
// (CORNUS_INCUS_REMOTE), mirroring containerdhost.WithRemote /
// dockerhost.WithRemote. Like containerdhost's (and unlike dockerhost's) it does
// not make the daemon reachable from another host — New only ever dials a local
// unix socket — it changes how client-local features are realized: every replica
// gains a companion instance whose caretaker relays port-forward traffic and
// ssh-agent connections, for a server that cannot route to the instance itself
// (e.g. a containerized cornus with its own netns). See companion_linux.go.
func WithRemote(remote bool) Option {
	return func(o *options) { o.remote = remote }
}

// WithAgentImage sets the cornus-embedding image (CORNUS_AGENT_IMAGE) the
// caretaker companion runs. Required in remote mode.
func WithAgentImage(image string) Option {
	return func(o *options) { o.agentImage = image }
}

// WithRegistryCredentials installs the per-create image pull credential
// resolver. A fresh short-lived credential is resolved for every instance post.
func WithRegistryCredentials(creds deploy.RegistryCredentials) Option {
	return func(o *options) { o.creds = creds }
}

// WithCompanionRegistry sets the server's per-instance companion-connection
// registry (pkg/remotecompanion) so ForwardPort reroutes through a remote-mode
// instance's companion instead of dialing the instance's own IP.
func WithCompanionRegistry(r *remotecompanion.Registry) Option {
	return func(o *options) { o.companions = r }
}

// instanceName names replica i of a deployment. Incus instance names must be a
// valid DNS label (<=63 chars, [a-z0-9-], no leading/trailing dash); the
// cornus-<app>-<i> shape used by the other host backends satisfies that for
// conformant app names. Callers pass already-validated deployment names.
func instanceName(app string, i int) string { return fmt.Sprintf("cornus-%s-%d", app, i) }

// Config keys stamped on every managed instance, in Incus's user.* metadata
// namespace (arbitrary user keys must be user.*-prefixed). deploy.LabelManaged /
// deploy.LabelApp and the cornus.origin.* keys are stored the same way, each
// prefixed with configKeyPrefix; originFromConfig / originToConfig translate the
// origin set.
const configKeyPrefix = "user."
