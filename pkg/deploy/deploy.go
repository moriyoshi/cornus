// Package deploy is cornus's imperative deployment engine. A Backend converges
// a host's actual state to a declarative DeploySpec. Backends are pluggable:
// the dockerhost backend ships first; a kubernetes backend can be added behind
// the same interface.
package deploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"cornus/pkg/api"
)

// ErrNotFound reports that a named deployment does not exist on the backend.
// Stop, Start, and Restart wrap it (via fmt.Errorf's %w) when asked to act on
// an unknown name, so errors.Is(err, ErrNotFound) is the portable test across
// backends. Delete is exempt: it is delete-if-exists and succeeds on a missing
// name.
var ErrNotFound = errors.New("deployment not found")

// Labels applied to all cornus-managed resources.
const (
	LabelManaged = "cornus.managed"
	LabelApp     = "cornus.app"
	// LabelVolume records a named volume's logical name on its (shared,
	// un-owned) backing PVC, so it is discoverable without being tied to any one
	// deployment.
	LabelVolume = "cornus.volume"
)

// Origin lineage keys stamped on every cornus-managed instance so a
// deployment's provenance (project, client host/user/dir, git repo, and the
// authenticated subject) survives on the backend and can be read back on
// List/Status. They are container labels on dockerhost/containerd, record
// fields on bare, and object annotations on kubernetes — the values (paths,
// URLs, subjects) do not satisfy Kubernetes label syntax. Use originToLabels /
// originFromLabels for the translation so all backends stay consistent.
const (
	LabelOriginProject   = "cornus.origin.project"
	LabelOriginHost      = "cornus.origin.host"
	LabelOriginUser      = "cornus.origin.user"
	LabelOriginDir       = "cornus.origin.dir"
	LabelOriginGitRemote = "cornus.origin.git.remote"
	LabelOriginGitBranch = "cornus.origin.git.branch"
	LabelOriginGitCommit = "cornus.origin.git.commit"
	LabelOriginGitDirty  = "cornus.origin.git.dirty"
	LabelOriginSubject   = "cornus.origin.subject"
)

// OriginToLabels renders an api.Origin as the flat cornus.origin.* key/value set
// a backend stamps onto a managed instance. Empty fields are omitted; a nil
// origin (or one with nothing set) yields nil, so backends can merge the result
// unconditionally without writing empty keys.
func OriginToLabels(o *api.Origin) map[string]string {
	if o == nil {
		return nil
	}
	m := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	put(LabelOriginProject, o.Project)
	put(LabelOriginHost, o.Host)
	put(LabelOriginUser, o.User)
	put(LabelOriginDir, o.Directory)
	put(LabelOriginSubject, o.Subject)
	if o.Git != nil {
		put(LabelOriginGitRemote, o.Git.Remote)
		put(LabelOriginGitBranch, o.Git.Branch)
		put(LabelOriginGitCommit, o.Git.Commit)
		if o.Git.Dirty {
			m[LabelOriginGitDirty] = "true"
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// OriginFromLabels reconstructs an api.Origin from the cornus.origin.* keys read
// off a managed instance's labels/annotations. It returns nil when none are
// present, so a deployment with no recorded origin reports a nil DeployStatus.Origin.
func OriginFromLabels(m map[string]string) *api.Origin {
	if len(m) == 0 {
		return nil
	}
	o := &api.Origin{
		Project:   m[LabelOriginProject],
		Host:      m[LabelOriginHost],
		User:      m[LabelOriginUser],
		Directory: m[LabelOriginDir],
		Subject:   m[LabelOriginSubject],
	}
	git := &api.GitOrigin{
		Remote: m[LabelOriginGitRemote],
		Branch: m[LabelOriginGitBranch],
		Commit: m[LabelOriginGitCommit],
		Dirty:  m[LabelOriginGitDirty] == "true",
	}
	if git.Remote != "" || git.Branch != "" || git.Commit != "" || git.Dirty {
		o.Git = git
	}
	if o.Project == "" && o.Host == "" && o.User == "" && o.Directory == "" && o.Subject == "" && o.Git == nil {
		return nil
	}
	return o
}

// RegistryAdvertiser is an optional Backend capability: a backend that can
// describe the registry address its deploy targets pull from implements it (the
// kubernetes backend, by introspecting its own Service). The server consults it
// for GET /.cornus/v1/info when CORNUS_ADVERTISE_REGISTRY is unset, so a client can tag
// and pull images by an address the cluster's nodes — not just the client's
// control-plane endpoint — can reach. A backend that cannot introspect (dockerhost,
// containerd) simply does not implement it; the type assertion then falls through
// to the endpoint-host fallback. An empty ServerInfo return means "could not
// determine", distinct from an error.
type RegistryAdvertiser interface {
	AdvertisedRegistry(ctx context.Context) (api.ServerInfo, error)
}

// IngressAdvertiser is an optional Backend capability: a backend that can describe
// the cluster's HTTP(S) ingress front door implements it (the kubernetes backend,
// by reporting its ingress defaults and discovering the controller Service). The
// server consults it for GET /.cornus/v1/info so a client can decide how to reach a
// workload's ingress through the SOCKS5 conduit — a native tunnel to the real
// controller, or client-side emulation. A backend with no ingress (dockerhost,
// containerd) simply does not implement it. A nil *api.IngressInfo return means "no
// ingress to advertise", distinct from an error.
type IngressAdvertiser interface {
	AdvertisedIngress(ctx context.Context) (*api.IngressInfo, error)
}

// VolumeRemover is an optional Backend capability: removing a named,
// project-scoped volume by its logical name — the VolumeSpec.Name a Compose
// project assigns (e.g. "myproj_cache"), which each backend maps to its own
// concrete identifier (a Docker volume name, a PVC, a host directory). Compose
// `down --volumes` calls it once per named volume, after the deployments are
// gone. Delete-if-exists semantics, like Backend.Delete: removing a volume that
// does not exist is a no-op success, NOT an error. Backends that cannot remove
// volumes simply do not implement it; the server then answers 501 Not
// Implemented and the client reports volume removal as unsupported.
type VolumeRemover interface {
	RemoveVolume(ctx context.Context, name string) error
}

// Backend deploys workloads onto a target (Docker host, Kubernetes, ...).
type Backend interface {
	// Name identifies the backend (e.g. "dockerhost", "kubernetes").
	Name() string
	// Apply converges the target to spec, creating or recreating as needed.
	Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error)
	// Status reports the observed state of the named deployment. A name with no
	// live resources is not an error: the status simply has no Instances.
	//
	// InstanceStatus.State carries each backend's own vocabulary, NOT a
	// normalized enum. The guaranteed common subset is exactly "running" (with
	// InstanceStatus.Running == true) for a live instance; portable callers must
	// branch on the Running bool and treat other State strings as display text.
	// Per backend:
	//
	//   - dockerhost: Docker's container states passed through verbatim —
	//     created, restarting, running, removing, paused, exited, dead.
	//   - containerd: created, running, paused, exited (an unknown task status
	//     passes through verbatim; a container with no task reports exited).
	//   - kubernetes: running (ready replica) or pending (not ready), reported
	//     on fabricated per-replica instance IDs "<name>-<i>" — the Deployment's
	//     ready count is projected onto replica slots, so no per-pod states
	//     (e.g. CrashLoopBackOff) are surfaced.
	Status(ctx context.Context, name string) (api.DeployStatus, error)
	// List reports all cornus-managed deployments. Instance states follow the
	// same per-backend vocabulary as Status.
	List(ctx context.Context) ([]api.DeployStatus, error)
	// Delete removes the named deployment. Delete-if-exists semantics: removing
	// a name that does not exist is a no-op success, NOT an ErrNotFound.
	Delete(ctx context.Context, name string) error
	// Start starts a stopped deployment's instances. If no deployment with the
	// name exists, it returns an error wrapping ErrNotFound.
	Start(ctx context.Context, name string) error
	// Stop stops a deployment's instances without removing them. If no
	// deployment with the name exists, it returns an error wrapping ErrNotFound.
	Stop(ctx context.Context, name string) error
	// Restart restarts a deployment's instances. If no deployment with the name
	// exists, it returns an error wrapping ErrNotFound.
	Restart(ctx context.Context, name string) error
	// Logs streams logs for the named deployment's instance(s) to w until ctx is
	// done (Follow) or the stream ends. opts controls follow/tail/streams/
	// timestamps/since. opts.Since accepts the ParseSince grammar on every
	// backend (Unix seconds[.nanos], RFC3339, or a duration relative to now); a
	// malformed value is an error, never silently ignored.
	//
	// Framing contract: implementations MUST write stdcopy-multiplexed frames
	// (Docker's 8-byte per-chunk stream header) so a caller can demultiplex
	// stdout/stderr regardless of backend. The dockerhost backend passes
	// Docker's already-framed non-TTY log bytes through unchanged; backends with
	// an unframed stream (kubernetes) wrap it in stdcopy stdout framing.
	//
	// If a deployment has multiple instances, the first is streamed (a
	// documented limitation; multi-instance log fan-in is not implemented).
	Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error
	// Stats streams Docker-format container metrics for the named deployment to w
	// (docker stats semantics). Each write is one Docker stats JSON object; when
	// opts.Stream is false a single object is written then the stream ends. The
	// bytes are Docker's own stats format, passed through unchanged so the docker
	// CLI can parse them. If a deployment has multiple instances, the first is
	// streamed (a documented limitation). Backends without a metrics source
	// return a not-supported error.
	Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error
	// StatPath returns metadata for path inside the named deployment's first
	// instance (docker cp / archive HEAD semantics).
	StatPath(ctx context.Context, name, path string) (api.PathStat, error)
	// CopyFrom writes a tar of path (from the named deployment's first instance)
	// to w and returns the path's stat (docker cp from container / archive GET
	// semantics). The tar bytes are Docker's own archive format, passed through
	// unchanged.
	CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error)
	// CopyTo extracts the tar read from r into path inside the named deployment's
	// first instance (docker cp into container / archive PUT semantics).
	CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error
	// ExecCreate creates an exec in the named deployment's first instance (docker
	// exec create) and returns the backend's opaque exec id. It does not start the
	// exec; ExecStart runs it.
	ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (execID string, err error)
	// ExecStart runs a previously-created exec and bridges conn to the exec's raw
	// bidirectional stdio stream (stdin from conn to the process, process output
	// back to conn). It returns when either side of the stream closes; conn is the
	// caller's transport (already stripped of any protocol preamble). For a non-TTY
	// exec the process output is Docker's stdcopy-multiplexed stream, passed
	// through unchanged.
	ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig, conn io.ReadWriteCloser) error
	// ExecInspect reports an exec's state (docker exec inspect).
	ExecInspect(ctx context.Context, execID string) (api.ExecState, error)
	// ExecResize resizes the TTY of a running exec to height rows by width
	// columns (docker exec resize). It is an out-of-band control-plane call,
	// separate from the ExecStart stdio stream.
	ExecResize(ctx context.Context, execID string, height, width uint) error
	// Attach bridges conn to the named deployment's first instance's raw
	// bidirectional stdio stream (docker attach). It returns when either side
	// closes. cfg selects which streams to attach and whether to replay prior logs.
	Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error
	// ForwardPort bridges conn to a port inside the named deployment's first
	// instance (kubectl port-forward / docker -p, but tunneled through the server
	// so it works against a remote backend and reaches unpublished ports). proto
	// is "tcp" (the default when empty) or "udp". For tcp, conn carries the raw
	// byte stream of one connection. For udp, conn carries length-prefixed
	// datagrams (wire.WriteDatagram framing) for one client flow, bridged to a
	// connected UDP socket toward the workload; the kubernetes backend rejects
	// udp (its port-forward subresource is TCP-only). It returns when either
	// side of the stream closes.
	ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error
	// Close releases backend resources.
	Close() error
}

// AttachMount describes a client-local bind mount to be realized live inside the
// workload (e.g. a Kubernetes pod sidecar), streamed over 9P from the caller via
// the cornus server relay — without mounting on any host namespace. Backends
// that can do this implement MountingBackend.
type AttachMount struct {
	Target     string // path inside the app container
	ReadOnly   bool
	AsyncCache bool   // writable, cache-coherent block-proxy mount (cache=mmap)
	Session    string // deploy-attach session id, for the relay endpoint
	Name       string // 9P backing name the caller exports
	RelayURL   string // cornus server URL the sidecar dials for the 9P stream
	AgentImage string // image running `cornus mount-agent` (the cornus image)
}

// MountingBackend is an optional Backend extension for realizing client-local
// bind mounts natively (inside the workload) instead of on a host namespace. The
// kubernetes backend implements it unconditionally; dockerhost/containerdhost
// implement it too (a caretaker-sidecar path for "remote" deployments, see
// RemoteCapable), alongside their normal host-mount fast path.
type MountingBackend interface {
	Backend
	// ApplyWithMounts converges to spec, wiring each AttachMount as a live 9P
	// mount inside the workload. spec.Mounts still lists every mount (including
	// the attach targets); the backend must not treat an attach target as an
	// ordinary host path.
	ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []AttachMount) (api.DeployStatus, error)
}

// RemoteCapable is an optional Backend extension: a host backend (dockerhost,
// containerd) that can ALSO realize client-local mounts via a caretaker
// sidecar (implementing MountingBackend), in addition to its normal co-located
// fast path. Remote reports whether THIS backend instance has been explicitly
// configured for that mode — there is no reliable way to detect daemon
// co-location automatically, so it is always an explicit operator opt-in
// (e.g. CORNUS_DOCKER_REMOTE), never inferred. The server consults it to
// choose between the two paths; a backend that does not implement
// RemoteCapable (kubernetes, which has no host-mount fallback anyway) is
// always treated as if Remote() were true.
type RemoteCapable interface {
	Remote() bool
}

// AgentForwardCapable is an optional Backend extension for backends where
// ssh-agent forwarding into an exec session (api.ExecConfig.ForwardAgent) is
// gated per-DEPLOYMENT rather than by a backend-wide mode: kubernetes has no
// Remote()/co-location concept (RemoteCapable), and unconditionally running a
// caretaker sidecar for every deployment just for this would be wasteful, so
// it is opt-in via api.DeploySpec.AgentForward instead. AgentForwardEnabled
// reports whether name's currently-applied spec had it set, so the server can
// fail an exec request fast instead of injecting a SSH_AUTH_SOCK that goes
// nowhere. Backends that gate on RemoteCapable/Remote() instead (dockerhost,
// containerdhost) do not need this interface.
type AgentForwardCapable interface {
	AgentForwardEnabled(ctx context.Context, name string) (bool, error)
}

// AttachCredential describes one client-sourced credential to be delivered live
// inside the workload (e.g. via a Kubernetes caretaker sidecar), fetched on
// demand from the caller through the cornus server relay. Like AttachMount it
// carries the session/relay coordinates the sidecar dials; Deliver carries the
// provider-agnostic delivery list from the spec (endpoints and/or files).
type AttachCredential struct {
	Name       string                   // credential name (relay backing + capability key)
	Session    string                   // deploy-attach session id, for the relay endpoint
	RelayURL   string                   // cornus server URL the sidecar dials for the credential stream
	AgentImage string                   // image running `cornus caretaker` (the cornus image)
	TTL        string                   // client-side cache/refresh hint (Go duration); "" = default
	Deliver    []api.CredentialDelivery // runtime deliveries (endpoints/files) served by the caretaker
	// EnvVars are env-kind deliveries the SERVER already resolved: it fetched the
	// value from the client once at deploy time (env is fixed at container start).
	// The backend materializes these into a Secret + secretKeyRef on the app
	// container — never a pod-spec literal.
	EnvVars []CredentialEnvVar
}

// CredentialEnvVar is one deploy-time-resolved env-kind delivery: the app-container
// variable Var set to Value (fetched from the client).
type CredentialEnvVar struct {
	Var   string
	Value string
}

// AttachEgress describes client-side egress to realize inside the workload: the
// caretaker runs a forward proxy (or transparent redirect) that relays the app's
// outbound connections back through the cornus server to the client (or a gateway),
// per the routing policy in Spec. Like AttachMount/AttachCredential it carries the
// session/relay coordinates the caretaker dials. Nil means no egress relay.
type AttachEgress struct {
	Session    string          // deploy-attach session id, presented on each egress relay stream
	RelayURL   string          // cornus server URL the caretaker dials for the egress stream
	AgentImage string          // image running `cornus caretaker` (the cornus image)
	Spec       *api.EgressSpec // mode / listen port / routing policy
}

// AttachingBackend is a superset of MountingBackend that also realizes
// client-sourced credentials (AttachCredential) and client-side egress
// (AttachEgress) inside the workload, in the SAME per-pod caretaker as the mounts.
// Backends supporting these implement it; the server prefers it when a session
// declares credential sources or an egress relay. The kubernetes backend implements
// both, and its ApplyWithMounts delegates here with no attachments, so existing
// mount-only callers are unaffected.
type AttachingBackend interface {
	MountingBackend
	ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []AttachMount, creds []AttachCredential, egress *AttachEgress) (api.DeployStatus, error)
}

// EgressBackend is an optional Backend extension for realizing client-side egress
// (AttachEgress) WITHOUT the full mount/credential caretaker machinery — the host
// backends (dockerhost, containerd) implement it to run a companion caretaker
// container sharing the workload's network namespace. The kubernetes backend does
// NOT implement it (its AttachingBackend folds egress into the one pod caretaker);
// the server prefers AttachingBackend and falls back to this for an egress-only
// deploy on a backend that has no AttachingBackend.
type EgressBackend interface {
	Backend
	ApplyWithEgress(ctx context.Context, spec api.DeploySpec, egress *AttachEgress) (api.DeployStatus, error)
}

// RestartPolicy returns the effective restart policy string for a spec.
func RestartPolicy(spec api.DeploySpec) string {
	if spec.Restart == "" {
		return "unless-stopped"
	}
	return spec.Restart
}

// IsOneShot reports whether a spec describes a run-to-completion (one-shot / init)
// workload rather than a long-lived service: its restart policy is "no" or
// "on-failure". Such a workload is expected to exit; a backend must not restart it
// unconditionally (the kubernetes backend deploys it as a Job, not a Deployment),
// and the deploy-attach readiness wait treats a clean exit as success rather than
// waiting for it to stay Running. "always"/"unless-stopped" (the default) are
// long-lived and return false.
func IsOneShot(spec api.DeploySpec) bool {
	p := RestartPolicy(spec)
	// "on-failure:N" normally arrives already split into Restart="on-failure" +
	// RestartMaxAttempts (see compose), but tolerate the raw short form too.
	return p == "no" || p == "on-failure" || strings.HasPrefix(p, "on-failure:")
}

// Replicas returns the effective replica count (at least 1).
func Replicas(spec api.DeploySpec) int {
	if spec.Replicas <= 0 {
		return 1
	}
	return spec.Replicas
}

// StopGracePeriodSeconds parses spec.StopGracePeriod (a Go duration string like
// "10s"/"1m30s") into whole seconds for the backends that express a stop grace
// as an integer-seconds field (Docker StopTimeout, Kubernetes
// TerminationGracePeriodSeconds). ok is false when unset or unparseable, so a
// caller leaves the backend default. A sub-second value rounds up to 1 so a
// non-empty grace is never truncated to "kill immediately".
func StopGracePeriodSeconds(spec api.DeploySpec) (seconds int, ok bool) {
	if spec.StopGracePeriod == "" {
		return 0, false
	}
	d, err := time.ParseDuration(spec.StopGracePeriod)
	if err != nil || d < 0 {
		return 0, false
	}
	secs := int(d / time.Second)
	if secs == 0 && d > 0 {
		secs = 1
	}
	return secs, true
}
