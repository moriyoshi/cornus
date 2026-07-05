// Package api holds cornus's shared request/response types used by both the
// HTTP server and the CLI.
package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// DeploySpec is the declarative description of a workload cornus deploys. It
// is applied imperatively: one spec in, the backend converges actual state to
// it (create or recreate).
type DeploySpec struct {
	// Name uniquely identifies the deployment; managed resources are labeled
	// with it for idempotent apply/delete.
	Name string `json:"name" yaml:"name"`
	// Image is the image reference to run, ideally digest-pinned.
	Image string `json:"image" yaml:"image"`
	// Command overrides the image's default command (Docker CMD): it supplies
	// the arguments to the image's ENTRYPOINT, which stays in effect — docker
	// create semantics on every backend (kubernetes carries it in the
	// container's Args so the image ENTRYPOINT is preserved).
	Command []string `json:"command,omitempty" yaml:"command,omitempty"`
	// Entrypoint overrides the image's default entrypoint (Docker ENTRYPOINT /
	// Kubernetes container command). When set, Command supplies the arguments to
	// it, mirroring Docker's create-time semantics; empty keeps the image default.
	Entrypoint []string `json:"entrypoint,omitempty" yaml:"entrypoint,omitempty"`
	// Env are environment variables (KEY=VALUE applied from the map).
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Ports maps host ports to container ports.
	Ports []PortMapping `json:"ports,omitempty" yaml:"ports,omitempty"`
	// Mounts bind host paths or volumes into the container.
	Mounts []Mount `json:"mounts,omitempty" yaml:"mounts,omitempty"`
	// Volumes are managed (non-bind) volumes mounted into the container. Unlike
	// Mounts (which bind an existing host source), each entry is a container-only
	// target that the backend provisions storage for: a dynamically-provisioned
	// PersistentVolumeClaim on kubernetes, a Docker anonymous volume on dockerhost.
	Volumes []VolumeSpec `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	// Networks are the user-defined networks this workload joins (Compose
	// `networks:`). They carry name-resolution and isolation intent: on
	// dockerhost each becomes a Docker user-defined network the container
	// attaches to with its aliases (libnetwork gives DNS and per-network
	// isolation natively); on kubernetes they are realised by a pipeline of
	// network providers selected by Driver (see pkg/deploy/kubernetes/
	// netdriver). Empty means the workload only has default connectivity —
	// no user network, exactly as before this field existed.
	Networks []NetworkAttachment `json:"networks,omitempty" yaml:"networks,omitempty"`
	// Proxy, when set, requests a userspace ENFORCING egress proxy for the
	// workload (kubernetes only; the dockerhost backend gets isolation from
	// libnetwork and ignores it). All of the app's outbound TCP is redirected
	// into a sidecar that permits a connection only if its destination resolves
	// to one of the Allow peers (services sharing a proxy network) — real L4
	// isolation on a flat pod network, independent of the cluster CNI. The
	// caller (compose plan) computes Allow from the network topology.
	Proxy *ProxySpec `json:"proxy,omitempty" yaml:"proxy,omitempty"`
	// DNS, when set, requests a per-pod caretaker DNS resolver (kubernetes only):
	// the pod's resolver is pointed at the sidecar, which answers the given
	// Records authoritatively and forwards everything else to the cluster DNS.
	// Used by Multus network modes where peers must resolve to their user-network
	// (secondary) IPs — which CoreDNS never publishes — or where the pod is
	// detached from the cluster network and CoreDNS is unreachable.
	DNS *DNSSpec `json:"dns,omitempty" yaml:"dns,omitempty"`
	// Hub, when set, joins the workload to the cornus server's workload-to-
	// workload overlay (kubernetes only): the caretaker registers the services this
	// workload Exports and, for each service it Imports, funnels the app's dial of
	// that name (via a synthetic-IP DNS record) through the hub. See
	// .agents/docs/ARCHITECTURE.md ("Workload-to-workload hub").
	Hub *HubSpec `json:"hub,omitempty" yaml:"hub,omitempty"`
	// Docker, when set, exposes a Docker Engine API endpoint to the workload on a
	// pod-loopback address (kubernetes only): the caretaker runs a Docker-API proxy
	// that translates `docker` / `docker compose` operations into deploys on the
	// same cornus server, and injects DOCKER_HOST into the app container. This grants
	// the workload deploy-engine access to the managed stack, so it is opt-in and
	// requires the server to have provisioned a dedicated client-scoped token Secret
	// (CORNUS_CLIENT_TOKEN_SECRET). See DockerSpec.
	Docker *DockerSpec `json:"docker,omitempty" yaml:"docker,omitempty"`
	// Telemetry, when set, runs an OpenTelemetry Collector agent inside the pod's
	// caretaker sidecar (on every backend, via the companion caretaker on the host
	// backends): the app sends OTLP to a pod-loopback receiver and the Collector
	// batches/limits and exports it to a configured external OTLP backend. The
	// backend automatically injects OTEL_EXPORTER_OTLP_ENDPOINT (+ related OTEL_*)
	// into the app container so a workload's SDK ships to the sidecar with no
	// app-side config. See TelemetrySpec.
	Telemetry *TelemetrySpec `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`
	// Credentials, when set, brokers short-lived IaaS / API credentials into the
	// workload that are minted on the CLIENT (the developer's machine) and
	// delivered on demand through the cornus server and the caretaker sidecar —
	// so a secret never lives in the image or in this spec in plaintext. Each
	// source names a client-side backend that mints the credential and one or
	// more provider-agnostic deliveries (a generic HTTP endpoint, a file, or an
	// optional cloud-metadata adapter such as AWS IMDS). Kubernetes realizes it
	// via a caretaker credential role; host backends via a companion caretaker
	// (other backends warn and ignore). See CredentialSpec.
	Credentials *CredentialSpec `json:"credentials,omitempty" yaml:"credentials,omitempty"`
	// Restart is the restart policy: "no", "always", "on-failure", or
	// "unless-stopped". Defaults to "unless-stopped". Compose's
	// `deploy.restart_policy.condition` (none->"no", on-failure->"on-failure",
	// any->"always") is authoritative over the service-level `restart:` and, when
	// set, is what the planner writes here.
	Restart string `json:"restart,omitempty" yaml:"restart,omitempty"`
	// RestartMaxAttempts caps the number of restart attempts for an "on-failure"
	// policy (compose `deploy.restart_policy.max_attempts`). dockerhost maps it to
	// RestartPolicy.MaximumRetryCount; kubernetes (a Deployment always restarts its
	// pods) and containerd (its restart monitor takes only a policy word) cannot
	// bound the attempt count and ignore it. Zero leaves the backend default
	// (unlimited retries).
	RestartMaxAttempts int `json:"restartMaxAttempts,omitempty" yaml:"restartMaxAttempts,omitempty"`
	// Replicas is the desired number of instances, honored by every backend: the
	// Deployment replica count on kubernetes; N containers on dockerhost and
	// containerd, where published host ports go to replica 0 only.
	Replicas int `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	// Privileged runs the container with full privileges (Docker --privileged /
	// Kubernetes securityContext.privileged). Opt-in and off by default; needed
	// for workloads that themselves manage containers or mounts (e.g. an
	// in-container dockerd, the cornus build engine, or kind).
	Privileged bool `json:"privileged,omitempty" yaml:"privileged,omitempty"`
	// Healthcheck, when set, configures a container health probe. On dockerhost it
	// becomes the Docker container healthcheck; on kubernetes it becomes an exec
	// livenessProbe (and readinessProbe). See Healthcheck.
	Healthcheck *Healthcheck `json:"healthcheck,omitempty" yaml:"healthcheck,omitempty"`
	// Resources, when set, caps the workload's CPU and memory (limits) and/or
	// requests a guaranteed floor (reservations). On dockerhost the limits map to
	// HostConfig NanoCpus/Memory and a memory reservation to MemoryReservation; on
	// kubernetes limits map to resources.limits and reservations to
	// resources.requests. See Resources.
	Resources *Resources `json:"resources,omitempty" yaml:"resources,omitempty"`
	// UpdateConfig, when set, describes the rolling-update strategy (compose
	// `deploy.update_config`). Only kubernetes realises it — as the Deployment's
	// strategy.rollingUpdate. The host backends recreate a single instance and
	// have no rolling-update concept, so they ignore it. See UpdateConfig.
	UpdateConfig *UpdateConfig `json:"updateConfig,omitempty" yaml:"updateConfig,omitempty"`
	// User is the user (and optional group) the container process runs as
	// (compose `user`, Docker create `User`): "uid", "uid:gid", "user", or
	// "user:group". dockerhost sets Config.User; containerd parses numeric
	// uid[:gid] into OCI Process.User (a plain name best-effort via
	// Process.User.Username); kubernetes maps a NUMERIC uid[:gid] to
	// securityContext.runAsUser/runAsGroup and cannot express a username.
	User string `json:"user,omitempty" yaml:"user,omitempty"`
	// WorkingDir is the container's working directory (compose `working_dir`,
	// Docker `WorkingDir`). dockerhost Config.WorkingDir; kubernetes
	// container.WorkingDir; containerd OCI Process.Cwd.
	WorkingDir string `json:"workingDir,omitempty" yaml:"workingDir,omitempty"`
	// Hostname is the container hostname (compose `hostname`, Docker `Hostname`).
	// dockerhost Config.Hostname; kubernetes pod Hostname; containerd OCI
	// hostname (overriding the instance-id default).
	Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	// Labels are user metadata applied to the workload (compose `labels`). On
	// dockerhost they merge into the container Config.Labels; on kubernetes they
	// become pod-template ANNOTATIONS (not labels — compose label values do not
	// satisfy Kubernetes label syntax); on containerd they merge into the
	// container labels. cornus's own management labels always win on a key clash.
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	// Origin records the lineage of the deployment: the project it belongs to
	// and the client host / OS user / directory / git repo it was spawned from.
	// The client attests every field except Subject; the server always
	// overwrites Subject with the authenticated identity of the request (a
	// client-supplied Subject is discarded), so claimed origin and verified
	// identity stay distinct. Persisted per-backend as `cornus.origin.*` labels
	// (dockerhost/containerd), record fields (bare), or annotations (kubernetes),
	// and reported back on DeployStatus.
	Origin *Origin `json:"origin,omitempty" yaml:"origin,omitempty"`
	// StopSignal is the signal used to stop the main process (compose
	// `stop_signal`, Docker `StopSignal`), e.g. "SIGTERM". dockerhost
	// Config.StopSignal; kubernetes has no per-container stop signal and ignores
	// it; containerd ignores it (no stock create-time field).
	StopSignal string `json:"stopSignal,omitempty" yaml:"stopSignal,omitempty"`
	// StopGracePeriod is how long to wait for the main process to exit after the
	// stop signal before killing it (compose `stop_grace_period`), a Go duration
	// string ("10s", "1m30s"). dockerhost Config.StopTimeout (rounded to
	// seconds); kubernetes pod TerminationGracePeriodSeconds; containerd has no
	// stock create-time field and ignores it. Empty leaves the backend default.
	StopGracePeriod string `json:"stopGracePeriod,omitempty" yaml:"stopGracePeriod,omitempty"`
	// Init, when non-nil, requests (true) or declines (false) an init process
	// reaping zombies as PID 1 (compose `init`, Docker HostConfig.Init). nil
	// leaves the backend default. dockerhost HostConfig.Init; kubernetes has no
	// direct tini equivalent and ignores it; containerd ignores it.
	Init *bool `json:"init,omitempty" yaml:"init,omitempty"`
	// TTY allocates a pseudo-TTY for the container (compose `tty`, Docker `Tty`).
	// dockerhost Config.Tty; kubernetes container.TTY; containerd OCI
	// Process.Terminal.
	TTY bool `json:"tty,omitempty" yaml:"tty,omitempty"`
	// StdinOpen keeps the container's stdin open (compose `stdin_open`, Docker
	// `OpenStdin`). dockerhost Config.OpenStdin; kubernetes container.Stdin;
	// containerd has no persistent-stdin OCI flag and ignores it.
	StdinOpen bool `json:"stdinOpen,omitempty" yaml:"stdinOpen,omitempty"`
	// ReadOnly mounts the container's root filesystem read-only (compose
	// `read_only`, Docker HostConfig.ReadonlyRootfs). dockerhost
	// HostConfig.ReadonlyRootfs; kubernetes securityContext.readOnlyRootFilesystem;
	// containerd OCI Root.Readonly.
	ReadOnly bool `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
	// CapAdd / CapDrop add or drop Linux capabilities (compose `cap_add` /
	// `cap_drop`, Docker HostConfig.CapAdd/CapDrop). dockerhost sets them
	// verbatim; kubernetes maps them to the container
	// securityContext.capabilities.add/drop; containerd adds/removes them on the
	// OCI process capability sets (oci.WithAddedCapabilities/WithDroppedCapabilities).
	CapAdd  []string `json:"capAdd,omitempty" yaml:"capAdd,omitempty"`
	CapDrop []string `json:"capDrop,omitempty" yaml:"capDrop,omitempty"`
	// SecurityOpt is the container security options list (compose `security_opt`,
	// Docker HostConfig.SecurityOpt). dockerhost passes each entry through
	// verbatim. kubernetes and containerd map the well-known ones best-effort:
	// `no-new-privileges[:true]` -> securityContext.allowPrivilegeEscalation=false
	// / OCI Process.NoNewPrivileges; `label=...` (SELinux) -> best-effort; and
	// `seccomp=...`/`apparmor=...` are not mapped (they need profile objects) and
	// are warned about.
	SecurityOpt []string `json:"securityOpt,omitempty" yaml:"securityOpt,omitempty"`
	// GroupAdd adds supplementary groups the container process joins (compose
	// `group_add`, Docker HostConfig.GroupAdd): a group name or a numeric GID.
	// dockerhost sets it verbatim (names and GIDs). kubernetes
	// securityContext.supplementalGroups and containerd OCI
	// Process.User.AdditionalGids accept NUMERIC GIDs only — a group name is
	// skipped there with a warning.
	GroupAdd []string `json:"groupAdd,omitempty" yaml:"groupAdd,omitempty"`
	// Sysctls sets namespaced kernel parameters (compose `sysctls`, Docker
	// HostConfig.Sysctls). dockerhost HostConfig.Sysctls; kubernetes pod
	// securityContext.sysctls; containerd OCI Linux.Sysctl.
	Sysctls map[string]string `json:"sysctls,omitempty" yaml:"sysctls,omitempty"`
	// ExtraHosts adds custom /etc/hosts entries as "host:ip" strings (compose
	// `extra_hosts`, Docker HostConfig.ExtraHosts). dockerhost
	// HostConfig.ExtraHosts; kubernetes pod HostAliases (hostnames grouped by
	// IP); containerd has no native extra-hosts mechanism and ignores it (it
	// would need an /etc/hosts bind mount).
	ExtraHosts []string `json:"extraHosts,omitempty" yaml:"extraHosts,omitempty"`
	// DNSServers are custom nameservers (compose `dns`). NOTE: distinct from the
	// DNS *DNSSpec caretaker-resolver field above. dockerhost HostConfig.Dns;
	// kubernetes pod DNSConfig.Nameservers (with DNSPolicy None when any of
	// DNSServers/DNSSearch/DNSOptions is set); containerd has no native
	// resolv.conf control and ignores it.
	DNSServers []string `json:"dnsServers,omitempty" yaml:"dnsServers,omitempty"`
	// DNSSearch are custom DNS search domains (compose `dns_search`). dockerhost
	// HostConfig.DnsSearch; kubernetes pod DNSConfig.Searches; containerd ignores it.
	DNSSearch []string `json:"dnsSearch,omitempty" yaml:"dnsSearch,omitempty"`
	// DNSOptions are custom resolver options (compose `dns_opt`), each "name" or
	// "name:value". dockerhost HostConfig.DnsOptions; kubernetes pod
	// DNSConfig.Options ({Name,Value} split on ':'); containerd ignores it.
	DNSOptions []string `json:"dnsOptions,omitempty" yaml:"dnsOptions,omitempty"`
	// Ulimits sets per-resource rlimits on the container process (compose
	// `ulimits`). dockerhost HostConfig.Ulimits; containerd OCI Process.Rlimits
	// (Name upper-cased and "RLIMIT_"-prefixed); kubernetes has no native ulimit
	// mechanism and ignores it.
	Ulimits []Ulimit `json:"ulimits,omitempty" yaml:"ulimits,omitempty"`
	// Tmpfs mounts a tmpfs at each entry (compose `tmpfs`). Each entry is a
	// container path with optional ":"-separated mount options (e.g.
	// "/run:size=64m"). dockerhost HostConfig.Tmpfs (path -> options); containerd
	// adds an OCI tmpfs mount per path (options carried through); kubernetes maps
	// each to an emptyDir{Medium: Memory} volume + mount (size options approximated
	// via SizeLimit / dropped).
	Tmpfs []string `json:"tmpfs,omitempty" yaml:"tmpfs,omitempty"`
	// Devices maps host devices into the container (compose `devices`), each
	// "host:container[:perms]" (perms default "rwm"). dockerhost
	// HostConfig.Devices; containerd OCI Linux.Devices + cgroup rule (oci.WithDevices);
	// kubernetes has no native host-device mapping (needs device plugins) and ignores it.
	Devices []string `json:"devices,omitempty" yaml:"devices,omitempty"`
	// ShmSize is the size of the container's /dev/shm in bytes (compose
	// `shm_size`). dockerhost HostConfig.ShmSize; containerd sets the /dev/shm
	// OCI mount's size= option; kubernetes an emptyDir{Medium: Memory, SizeLimit}
	// mounted at /dev/shm. Zero leaves the backend default.
	ShmSize int64 `json:"shmSize,omitempty" yaml:"shmSize,omitempty"`
	// PIDMode sets the container's PID namespace mode (compose `pid`), e.g.
	// "host", "service:foo", "container:...". dockerhost HostConfig.PidMode;
	// kubernetes maps "host" to pod HostPID (other forms ignored with a warning);
	// containerd maps "host" to leaving the host PID namespace (other forms ignored).
	PIDMode string `json:"pidMode,omitempty" yaml:"pidMode,omitempty"`
	// IPCMode sets the container's IPC namespace mode (compose `ipc`), e.g.
	// "host", "shareable", "service:...", "none". dockerhost HostConfig.IpcMode;
	// kubernetes maps "host" to pod HostIPC (other forms ignored with a warning);
	// containerd maps "host" to leaving the host IPC namespace (other forms ignored).
	IPCMode string `json:"ipcMode,omitempty" yaml:"ipcMode,omitempty"`
	// Egress, when set, routes the workload's OUTBOUND traffic through a
	// client-side vantage point instead of egressing from wherever the runtime
	// sits — for air-gapped clusters, VPN/corporate-proxy/SASE networks where the
	// sanctioned egress path lives on the caller's side. See EgressSpec.
	Egress *EgressSpec `json:"egress,omitempty" yaml:"egress,omitempty"`
	// Ingress, when set, requests a public HTTP(S) Ingress fronting the workload's
	// ClusterIP Service (kubernetes only; dockerhost/containerd log a warning and
	// ignore it). The host may be given explicitly or auto-derived from a
	// server-side base domain plus the deployment name. See IngressSpec.
	Ingress *IngressSpec `json:"ingress,omitempty" yaml:"ingress,omitempty"`
	// Knative, when set with Enabled, deploys the workload as a Knative Serving
	// Service — a serverless, request-driven deployment with autoscaling and
	// scale-to-zero. Realised only by the kubernetes backend, and only on a
	// cluster that serves the serving.knative.dev API (the backend then emits a
	// serving.knative.dev/v1 Service instead of a Deployment+Service+Ingress).
	// Everywhere else — a plain kubernetes cluster, or the dockerhost/containerd/
	// bare backends — it is warned about and ignored, and the workload runs as an
	// ordinary container without autoscaling. Populated by the Knative descriptor
	// loader (pkg/knative) from a ksvc manifest, or set directly on a native spec.
	// See KnativeSpec.
	Knative *KnativeSpec `json:"knative,omitempty" yaml:"knative,omitempty"`
	// AgentForward, when set, wires a caretaker AgentRelayRole for this
	// deployment (kubernetes only), so `cornus exec --forward-agent` /
	// `cornus compose exec --forward-agent` can relay a caller's local
	// ssh-agent into an exec session run in this workload. Opt-in and off by
	// default: unlike dockerhost/containerdhost (gated by the backend-wide
	// CORNUS_DOCKER_REMOTE / CORNUS_CONTAINERD_REMOTE, which already runs a
	// per-instance companion for every deployment), kubernetes has no such
	// always-on companion, and running one for every deployment just for this
	// would be wasteful — so each deployment opts in individually here. When
	// none of Hub/DNS/Docker already place a caretaker sidecar in the pod, this
	// alone causes a minimal one to be added. dockerhost/containerd ignore this
	// field (see deploy.RemoteCapable instead).
	AgentForward bool `json:"agentForward,omitempty" yaml:"agentForward,omitempty"`
}

// Ulimit is one process resource limit (compose `ulimits`). Name is the bare
// limit name ("nofile", "nproc"); Soft and Hard are the soft and hard bounds.
// Compose's shorthand (a bare integer) sets Soft == Hard.
type Ulimit struct {
	Name string `json:"name" yaml:"name"`
	Soft int64  `json:"soft,omitempty" yaml:"soft,omitempty"`
	Hard int64  `json:"hard,omitempty" yaml:"hard,omitempty"`
}

// Healthcheck describes a container health probe, modelled on Docker's
// healthcheck. Test is the command in Docker's CMD form: the first element is
// "CMD" (exec the rest), "CMD-SHELL" (run the single string via the shell), or
// "NONE" (disable any inherited healthcheck). Interval, Timeout and StartPeriod
// are Go duration strings ("30s", "1m30s"); an empty value leaves the backend
// default. Retries is the number of consecutive failures before unhealthy.
type Healthcheck struct {
	Test        []string `json:"test,omitempty" yaml:"test,omitempty"`
	Interval    string   `json:"interval,omitempty" yaml:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty" yaml:"startPeriod,omitempty"`
	// StartInterval is the probe interval DURING the start period (compose
	// `healthcheck.start_interval`): a Go duration string. Backends that support
	// it probe this often until the container is healthy or StartPeriod elapses,
	// then fall back to Interval. Empty leaves the backend default.
	StartInterval string `json:"startInterval,omitempty" yaml:"startInterval,omitempty"`
	Retries       int    `json:"retries,omitempty" yaml:"retries,omitempty"`
}

// Disabled reports whether the healthcheck explicitly disables probing
// (Docker's "NONE" test, from compose `healthcheck.disable: true`), so a backend
// can skip emitting a probe rather than run an empty command.
func (h *Healthcheck) Disabled() bool {
	return len(h.Test) > 0 && h.Test[0] == "NONE"
}

// Resources caps a workload's compute (the *Limit fields) and/or reserves a
// guaranteed floor (the Reserved* fields, from compose
// `deploy.resources.reservations`). CPULimit is a fractional core count (e.g.
// 0.5 = half a core), mapped to Docker NanoCpus and to a kubernetes CPU quantity
// in millicores. MemoryLimit is a byte count, mapped to Docker Memory and to a
// kubernetes memory quantity. ReservedCPU / ReservedMemory are the reservation
// analogues: on kubernetes they become resources.requests (cpu/memory); on
// dockerhost only ReservedMemory maps (HostConfig.MemoryReservation) — Docker
// has no CPU reservation (only shares and hard limits) so ReservedCPU is a
// no-op there; containerd (OCI) has no request/reservation concept and ignores
// both. A zero field means "unset on that axis".
type Resources struct {
	CPULimit       float64 `json:"cpuLimit,omitempty" yaml:"cpuLimit,omitempty"`
	MemoryLimit    int64   `json:"memoryLimit,omitempty" yaml:"memoryLimit,omitempty"`
	ReservedCPU    float64 `json:"reservedCpu,omitempty" yaml:"reservedCpu,omitempty"`
	ReservedMemory int64   `json:"reservedMemory,omitempty" yaml:"reservedMemory,omitempty"`
}

// UpdateConfig is the rolling-update strategy from compose
// `deploy.update_config`. Parallelism is how many instances to update at once
// (0 => the backend's default of 1); Order is "stop-first" (default: take an old
// instance down before bringing a new one up) or "start-first" (surge a new
// instance up before removing the old). Only kubernetes maps it, onto the
// Deployment strategy.rollingUpdate: Parallelism sizes maxUnavailable
// (stop-first) or maxSurge (start-first). The other compose update_config knobs
// (delay, monitor, max_failure_ratio) are swarm rollout-timing concepts a
// Deployment cannot express and are dropped at translate time.
type UpdateConfig struct {
	Parallelism int    `json:"parallelism,omitempty" yaml:"parallelism,omitempty"`
	Order       string `json:"order,omitempty" yaml:"order,omitempty"`
}

// PortMapping maps a host port to a container port.
type PortMapping struct {
	Host      int    `json:"host" yaml:"host"`
	Container int    `json:"container" yaml:"container"`
	Protocol  string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // tcp (default) or udp
	// HostIP restricts the host-side publish to a specific host interface
	// (compose `127.0.0.1:8080:80`). Empty binds all interfaces (0.0.0.0), as
	// before. Honored by the host backends; k8s Services have no equivalent.
	HostIP string `json:"hostIP,omitempty" yaml:"hostIP,omitempty"`
}

// Mount binds a host source into the container at Target.
type Mount struct {
	Source   string `json:"source" yaml:"source"`
	Target   string `json:"target" yaml:"target"`
	ReadOnly bool   `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
	// SELinux requests an SELinux relabel of the bind source (compose `:z`/`:Z`):
	// "z" shares the content among containers, "Z" makes it private. Empty means
	// no relabel. Applied by the dockerhost backend; containerd/k8s do not relabel.
	SELinux string `json:"selinux,omitempty" yaml:"selinux,omitempty"`
	// Immutable marks a client-local bind mount whose contents will not change for
	// the deployment's lifetime, so the server may serve its 9P reads from the
	// per-file block cache. Only honored together with ReadOnly (the cache never
	// serves writable mounts). Ignored for non-local (server-host) mounts.
	Immutable bool `json:"immutable,omitempty" yaml:"immutable,omitempty"`
	// AsyncCache marks a client-local WRITABLE bind mount for the async,
	// cache-coherent block protocol: the container mounts cache=mmap (writes are
	// absorbed by its page cache and flushed asynchronously via writeback) and the server keeps a
	// coherent read cache in front of the mount. For write-intensive workloads
	// (databases). Mutually exclusive with ReadOnly/Immutable; requires a single
	// writer (replicas == 1). Ignored for non-local (server-host) mounts.
	AsyncCache bool `json:"asyncCache,omitempty" yaml:"asyncCache,omitempty"`
}

// VolumeSpec is a managed (non-bind) volume mounted into the container. On
// kubernetes it is realised as a dynamically-provisioned PersistentVolumeClaim;
// on dockerhost as a Docker anonymous/named volume. Distinct from a bind Mount.
//
// On first start the volume is seeded with whatever the image ships at Target,
// matching Docker's volume behaviour: dockerhost gets this from the daemon; the
// kubernetes backend adds a populate initContainer that copies the image content
// into the freshly provisioned (empty) PVC, and skips the copy once the volume
// holds data so subsequent starts preserve writes.
//
// Name distinguishes the two Compose volume flavours:
//   - Name == "": an ANONYMOUS volume. Storage is private to this deployment and
//     ephemeral — reaped when the deployment is deleted (like `docker rm -v`): a
//     per-deployment PVC owned by the Deployment on kubernetes, a Docker
//     anonymous volume on dockerhost.
//   - Name != "": a NAMED volume. A shared, project-scoped store whose lifecycle
//     is independent of any one deployment — multiple deployments naming the same
//     Name share the same backing store, and it SURVIVES `cornus delete` of any
//     one of them (Docker named-volume semantics; a persistent, un-owned PVC on
//     kubernetes). The caller supplies the already project-scoped logical name
//     (e.g. "myproj_cache"); the kubernetes backend maps it to a valid, stable
//     claim name.
type VolumeSpec struct {
	Name         string `json:"name,omitempty" yaml:"name,omitempty"`                 // set => shared/persistent named volume; empty => anonymous
	Target       string `json:"target" yaml:"target"`                                 // container mount path (required)
	Size         string `json:"size,omitempty" yaml:"size,omitempty"`                 // e.g. "1Gi"; default "1Gi"
	StorageClass string `json:"storageClass,omitempty" yaml:"storageClass,omitempty"` // empty => cluster default class
	ReadOnly     bool   `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
	// Driver / DriverOpts select and parameterize the volume plugin for a NAMED
	// volume (compose top-level `volumes.<name>.driver` / `driver_opts`).
	// dockerhost passes them to `POST /volumes/create` (Docker's own volume
	// drivers: "local", "nfs" via local-driver opts, third-party plugins).
	// kubernetes has no docker-volume-driver concept (storage is chosen by
	// StorageClass) and ignores them; containerd backs volumes with plain host
	// dirs and ignores them.
	Driver     string            `json:"driver,omitempty" yaml:"driver,omitempty"`
	DriverOpts map[string]string `json:"driverOpts,omitempty" yaml:"driverOpts,omitempty"`
	// Labels are user metadata applied to a NAMED volume (compose top-level
	// `volumes.<name>.labels`). dockerhost sets them on the created volume;
	// kubernetes copies them onto the PVC's labels (cornus management labels win);
	// containerd ignores them (host-dir backings have no metadata store).
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// NetworkAttachment is one membership of a workload in a user-defined network,
// modelled on Docker/Compose user-network semantics: a member is reachable by
// its service name (and by any Aliases) from other members of the SAME network,
// and — where the backend's fabric supports it — is isolated from members of
// networks it does not join.
//
// Name is the project-scoped network resource name (e.g. "myproj_frontend");
// the caller resolves compose `name:`/`external:` overrides before setting it.
//
// Driver selects how the kubernetes backend realises the network. Empty takes
// the backend default (CORNUS_K8S_NET_DRIVER, itself defaulting to
// "services"). Recognised drivers name a provider pipeline: "services" (DNS
// only, works on any cluster), "bridge"/"ipvlan"/"macvlan" (Multus CNI
// attachments), "cilium". The dockerhost backend passes Driver straight
// through to Docker's own network drivers (empty => Docker's default bridge
// driver). DriverOpts are opaque per-network knobs forwarded to the driver
// (compose `driver_opts`: master interface, subnet, mtu, ...).
//
// Default marks the DETACHED-primary mode on kubernetes: the network replaces
// the pod's primary interface (Multus default-network) instead of being added
// as an overlaid secondary. At most one attachment may set it. dockerhost
// ignores Default — the first network is the container's primary either way.
//
// IP, when set, pins the workload's IPv4 address on this network, in CIDR form
// (e.g. "10.222.14.7/24"). The compose planner assigns one deterministically per
// service on every Multus-realised network (see pkg/compose usernet.go) so the
// peer records handed to the caretaker DNS role match the pod's secondary
// interface; the kubernetes Multus provider then renders the network's
// NetworkAttachmentDefinition with the `static` IPAM plugin and carries the
// address in the pod's network-selection annotation. Ignored for detached
// (Default) attachments and by non-Multus fabrics; dockerhost ignores it too
// (libnetwork's own IPAM addresses and resolves members natively).
type NetworkAttachment struct {
	Name       string            `json:"name" yaml:"name"`
	Driver     string            `json:"driver,omitempty" yaml:"driver,omitempty"`
	DriverOpts map[string]string `json:"driverOpts,omitempty" yaml:"driverOpts,omitempty"`
	Aliases    []string          `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Default    bool              `json:"default,omitempty" yaml:"default,omitempty"`
	IP         string            `json:"ip,omitempty" yaml:"ip,omitempty"`
	// Subnet / Gateway / IPRange carry the network's IPAM configuration (compose
	// top-level `networks.<name>.ipam.config[0]` subnet/gateway/ip_range).
	// dockerhost forwards them as the network's `IPAM.Config` entry; the
	// kubernetes Multus netdriver uses Subnet as the host-local IPAM subnet when
	// no `driver_opts.subnet` is given (Gateway/IPRange are not expressed by the
	// generated conflist); containerd (auto-allocated /24 bridges) ignores them.
	Subnet  string `json:"subnet,omitempty" yaml:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	IPRange string `json:"ipRange,omitempty" yaml:"ipRange,omitempty"`
	// Internal restricts the network to intra-network traffic with no external
	// egress (compose `networks.<name>.internal`); Attachable allows standalone
	// containers to join a swarm-scoped network (compose `attachable`);
	// EnableIPv6 turns on IPv6 addressing (compose `enable_ipv6`). dockerhost maps
	// each to the matching field on `POST /networks/create`; kubernetes/containerd
	// have no equivalent on their fabrics and ignore them.
	Internal   bool `json:"internal,omitempty" yaml:"internal,omitempty"`
	Attachable bool `json:"attachable,omitempty" yaml:"attachable,omitempty"`
	EnableIPv6 bool `json:"enableIPv6,omitempty" yaml:"enableIPv6,omitempty"`
	// Labels are user metadata applied to the network (compose
	// `networks.<name>.labels`). dockerhost sets them on the created network
	// (cornus management labels always win); kubernetes/containerd ignore them.
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	// IPv6 / MAC pin this member's per-network IPv6 address and MAC address
	// (compose service `networks.<name>.ipv6_address` / `mac_address`). dockerhost
	// carries them in the endpoint settings (IPAMConfig.IPv6Address / MacAddress);
	// kubernetes/containerd cannot express a per-member endpoint address here and
	// ignore them. Priority orders network attachment (compose `priority`): the
	// highest-priority network is joined first and its gateway becomes the
	// container's default route. dockerhost sorts attachments by it (primary =
	// highest); kubernetes/containerd ignore it.
	IPv6     string `json:"ipv6,omitempty" yaml:"ipv6,omitempty"`
	MAC      string `json:"mac,omitempty" yaml:"mac,omitempty"`
	Priority int    `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// ProxySpec configures the userspace egress proxy for a workload. Allow is the
// set of peer service names the workload may reach (services sharing a proxy
// network). Mode selects how isolation is realised:
//
//   - "" / "enforcing" (default): ALL of the app's outbound TCP is redirected
//     into the sidecar (nftables, via a NET_ADMIN init container), which
//     resolves Allow into the set of permitted destination IPs and drops
//     everything else — so a connection to a service NOT on a shared network is
//     dropped even if the app resolved its IP directly. Real L4 isolation.
//     ListenPort is the port the sidecar listens on for redirected traffic
//     (default applied by the backend when zero).
//
//   - "cooperative": no privilege — the backend points each Allow peer's DNS
//     name at a distinct loopback address the sidecar listens on (hostAliases),
//     and the sidecar forwards each declared peer port to the peer's real
//     Service. Soft isolation: an app that dials a raw pod IP (rather than the
//     service name) bypasses it. Ports carries, per Allow peer, the container
//     ports to proxy (the peer's `ports:`/`expose:` — the sidecar can only
//     intercept ports it knows to listen on).
type ProxySpec struct {
	Mode       string           `json:"mode,omitempty" yaml:"mode,omitempty"`
	Allow      []string         `json:"allow,omitempty" yaml:"allow,omitempty"`
	Ports      map[string][]int `json:"ports,omitempty" yaml:"ports,omitempty"`
	ListenPort int              `json:"listenPort,omitempty" yaml:"listenPort,omitempty"`
}

// EgressSpec routes a workload's OUTBOUND traffic through a client-side vantage
// point. It has three modes of increasing transparency:
//
//   - "env": propagate proxy environment variables (HTTP_PROXY/HTTPS_PROXY/
//     NO_PROXY/ALL_PROXY) into the container. Works on every backend and needs no
//     sidecar, but only helps when the container's network can actually reach the
//     proxy (it does NOT by itself relay traffic to the client). Proxies carries
//     the explicit values; when empty the client resolves its own OS proxy
//     settings at deploy time and fills them in.
//
//   - "proxy": the caretaker runs an HTTP CONNECT + SOCKS5 forward proxy on
//     loopback and the app's proxy env vars point at it; each proxied connection
//     is relayed back through the cornus server to the egress terminus (the client
//     or a gateway), which performs the real dial. Kubernetes now; host backends
//     via a companion caretaker.
//
//   - "transparent": all of the app's outbound TCP is captured by an nftables
//     redirect (NET_ADMIN init container) and relayed the same way, so apps that
//     ignore proxy env vars are covered too. Kubernetes now.
//
// Routing is per destination (see Rules / Script): each flow is sent to one of
// four routes — "client" (relay to the client-side network), "gateway" (relay to
// a durable egress-gateway node, for --detach), "cluster" (egress directly from
// the workload's own network, no relay) or "deny" (drop). This is the
// allowlist/denylist surface. Default applies to unmatched destinations and
// defaults to "cluster", so enabling egress never silently diverts in-cluster
// traffic — the caller opts destinations OUT to the client/gateway.
type EgressSpec struct {
	// Mode is "env", "proxy", or "transparent" (see the type doc). Empty is
	// treated as "env".
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Gateway would name a DISTINCT egress-gateway node for destinations routed to
	// "gateway". It is reserved for a future release and MUST be empty today: the
	// "gateway" route currently egresses through the cornus server itself (the server
	// IS the gateway). Validate rejects a non-empty value rather than silently
	// ignoring it. Empty means the server is the gateway.
	Gateway string `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	// Proxies (mode "env") are the explicit proxy variables to inject
	// (HTTP_PROXY, HTTPS_PROXY, NO_PROXY, ALL_PROXY and their lowercase forms).
	// Empty asks the client to resolve its own OS proxy configuration at deploy
	// time and populate them.
	Proxies map[string]string `json:"proxies,omitempty" yaml:"proxies,omitempty"`
	// Rules is the declarative routing policy: an ordered list evaluated
	// first-match-wins, falling back to Default. Superseded by Script when Script
	// is set.
	Rules []EgressRule `json:"rules,omitempty" yaml:"rules,omitempty"`
	// Script is an OPTIONAL PAC-style JavaScript program (a FindProxyForURL
	// function) that decides the route per destination. When set it supersedes
	// Rules. Evaluated by a sandboxed pure-Go JS engine; a DIRECT return maps to
	// "cluster", "PROXY client"/"PROXY gateway" to the relay routes, "DENY" to a
	// drop, and no match to Default.
	Script string `json:"script,omitempty" yaml:"script,omitempty"`
	// Default is the route for destinations no rule/script matches: "cluster"
	// (default), "client", "gateway", or "deny".
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// ListenPort is the caretaker proxy's listen port (modes "proxy" and
	// "transparent"); zero lets the backend apply its default.
	ListenPort int `json:"listenPort,omitempty" yaml:"listenPort,omitempty"`
}

// NeedsRelay reports whether the egress mode tunnels the workload's traffic back
// through the client (or a gateway), which requires a live deploy-attach session:
// the "proxy" and "transparent" modes do; "env" (proxy-var propagation) does not.
// The client uses it to decide whether a service must hold a session rather than
// deploy statelessly, and the server to route the deploy through the caretaker path.
func (e *EgressSpec) NeedsRelay() bool {
	if e == nil {
		return false
	}
	switch e.Mode {
	case "proxy", "transparent":
		return true
	}
	return false
}

// Validate rejects an EgressSpec whose values are syntactically accepted but not
// yet implemented, so a deploy fails fast at the boundary instead of silently
// mis-routing. Every path that turns user input into an EgressSpec — the CLI,
// compose, and each backend (as defense in depth) — calls it. A nil spec is valid.
func (e *EgressSpec) Validate() error {
	if e == nil {
		return nil
	}
	// A distinct gateway node is reserved for a future release: today the "gateway"
	// route egresses through the cornus server itself, so a separate Gateway URL
	// would do nothing. Reject it rather than accept a value with no effect.
	if strings.TrimSpace(e.Gateway) != "" {
		return fmt.Errorf("egress: a distinct gateway URL is not yet supported (the server itself is the gateway); remove gateway=%q", e.Gateway)
	}
	return nil
}

// IngressSpec requests a public HTTP(S) Ingress fronting the workload's ClusterIP
// Service (kubernetes only; dockerhost/containerd warn and ignore it). It requires
// the spec to publish at least one port — that Service is the Ingress backend.
//
// The distinctive feature is AUTOMATIC host derivation: when Host is empty, the
// kubernetes backend derives "<name>.<CORNUS_INGRESS_DOMAIN>" from a server-side
// base domain, so a per-PR preview deploy gets a public URL with no per-deployment
// host wiring. An explicit Host overrides the derived one.
type IngressSpec struct {
	// Enabled turns the ingress on. It lets the compose form be a bare
	// `x-cornus-ingress: {}` that enables ingress with every field defaulted; a
	// non-empty Host implies Enabled as well.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Hosts are the external hostnames the ingress routes; each becomes its own
	// Ingress rule (and shares one TLS entry) fronting the same Service. The special
	// token "@" maps to the apex — the base domain itself (Domain below, or the
	// server default), with no "<name>." prefix, following the DNS-zone convention.
	// Empty asks the kubernetes backend to derive a single "<name>.<domain>" host
	// from the base domain; a deploy with neither an explicit host nor any base
	// domain is rejected by the backend.
	Hosts []string `json:"hosts,omitempty" yaml:"hosts,omitempty"`
	// Domain is a CLIENT override of the base domain used to auto-derive the single
	// host when Hosts is empty. Empty falls back to the server default
	// (CORNUS_INGRESS_DOMAIN, ultimately a Helm value). A server MAY enforce a policy
	// that every resolved host stay within the server's configured domain, in which
	// case an out-of-domain override (or explicit host) is rejected.
	Domain string `json:"domain,omitempty" yaml:"domain,omitempty"`
	// Subdomain is the label(s) the backend prefixes to the base domain when
	// auto-deriving the host (Hosts empty), so the derived host is
	// "<Subdomain>.<domain>". It may carry multiple dot-separated labels. The compose
	// translator sets it to "<service>.<project>" so different projects get distinct,
	// per-project hostnames from the same base domain rather than colliding on a flat
	// name. Empty falls back to the deployment name. The backend sanitizes each label
	// to a DNS-1123 fragment, so raw compose service/project names are safe here.
	Subdomain string `json:"subdomain,omitempty" yaml:"subdomain,omitempty"`
	// Path is the HTTP path prefix to route (default "/").
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// PathType is the Kubernetes path match type: "Prefix" (default), "Exact", or
	// "ImplementationSpecific".
	PathType string `json:"pathType,omitempty" yaml:"pathType,omitempty"`
	// Port is the container port the ingress routes to. Zero uses the first
	// published port; a non-zero value must match one of the spec's published ports.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`
	// ClassName sets the IngressClassName. Empty falls back to the server default
	// (CORNUS_INGRESS_CLASS) and then to the cluster's default IngressClass.
	ClassName string `json:"className,omitempty" yaml:"className,omitempty"`
	// Annotations are merged verbatim onto the Ingress object, for controller-
	// specific knobs (rewrite targets, body size, etc.).
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	// TLS, when set, requests HTTPS for Host; nil serves plain HTTP.
	TLS *IngressTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// IngressTLS configures HTTPS for the ingress host.
type IngressTLS struct {
	// SecretName names an existing TLS secret. Empty defaults to "<name>-tls",
	// which cert-manager provisions when ClusterIssuer (or the server default) is set.
	SecretName string `json:"secretName,omitempty" yaml:"secretName,omitempty"`
	// ClusterIssuer sets the cert-manager.io/cluster-issuer annotation so
	// cert-manager provisions the certificate. Empty falls back to the server
	// default (CORNUS_INGRESS_TLS_ISSUER).
	ClusterIssuer string `json:"clusterIssuer,omitempty" yaml:"clusterIssuer,omitempty"`
}

// validIngressPathTypes is the set of Kubernetes Ingress path-match types.
var validIngressPathTypes = map[string]bool{
	"Prefix":                 true,
	"Exact":                  true,
	"ImplementationSpecific": true,
}

// Validate checks the syntactically-decidable parts of an IngressSpec so a deploy
// fails fast at the boundary (CLI, compose, and the backend as defense in depth).
// A nil spec is valid. Host presence and TLS-issuer resolution depend on
// server-side configuration and are therefore enforced by the kubernetes backend,
// not here.
func (in *IngressSpec) Validate() error {
	if in == nil {
		return nil
	}
	for _, h := range in.Hosts {
		h = strings.TrimSpace(h)
		if h == "@" {
			continue // apex placeholder; the backend resolves it to the base domain
		}
		if !isDNSName(h) {
			return fmt.Errorf("ingress: host %q is not a valid DNS name", h)
		}
	}
	if d := strings.TrimSpace(in.Domain); d != "" && !isDNSName(d) {
		return fmt.Errorf("ingress: domain %q is not a valid DNS name", in.Domain)
	}
	if in.PathType != "" && !validIngressPathTypes[in.PathType] {
		return fmt.Errorf("ingress: pathType %q must be one of Prefix, Exact, ImplementationSpecific", in.PathType)
	}
	if in.Port < 0 {
		return fmt.Errorf("ingress: port %d must not be negative", in.Port)
	}
	return nil
}

// isDNSName reports whether s is a plausible DNS hostname: dot-separated labels of
// [a-z0-9-] (case-insensitive), each 1-63 chars, not edged with '-'.
func isDNSName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// KnativeSpec configures a workload as a Knative Serving Service — a
// serverless, request-driven deployment with autoscaling and scale-to-zero.
// It is realised only by the kubernetes backend, and only when the target
// cluster serves the serving.knative.dev API: the backend then emits a
// serving.knative.dev/v1 Service (a "ksvc") instead of a Deployment+Service, so
// Knative's autoscaler owns the replica count and its Route owns exposure. On a
// cluster without Knative, and on the dockerhost/containerd/bare backends, the
// block is warned about and ignored — the workload still runs as an ordinary
// container, just without autoscaling. The Knative descriptor loader
// (pkg/knative) sets this from a ksvc manifest; a native DeploySpec may also set
// it directly.
type KnativeSpec struct {
	// Enabled marks this workload as a Knative Service; a bare compose/JSON `{}`
	// enables it with every field defaulted.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// MinScale is the autoscaling floor (autoscaling.knative.dev/minScale). Nil
	// (or 0) permits scale-to-zero — the Knative default — so an idle service
	// costs nothing.
	MinScale *int `json:"minScale,omitempty" yaml:"minScale,omitempty"`
	// MaxScale is the autoscaling ceiling (autoscaling.knative.dev/maxScale). Nil
	// (or 0) means unlimited, the Knative default.
	MaxScale *int `json:"maxScale,omitempty" yaml:"maxScale,omitempty"`
	// Target is the autoscaling target per replica (autoscaling.knative.dev/
	// target): concurrent requests for the concurrency metric, requests-per-second
	// for rps. Nil leaves the Knative default.
	Target *int `json:"target,omitempty" yaml:"target,omitempty"`
	// Concurrency is the hard limit on simultaneous requests a single replica
	// handles (revision spec.containerConcurrency). Nil (or 0) means unlimited.
	Concurrency *int `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	// Class selects the autoscaler (autoscaling.knative.dev/class): "kpa" (the
	// Knative Pod Autoscaler, default) or "hpa" (the Kubernetes HPA). Empty uses
	// the cluster default.
	Class string `json:"class,omitempty" yaml:"class,omitempty"`
	// Metric is the scaling metric (autoscaling.knative.dev/metric):
	// "concurrency" (default), "rps", or "cpu" (hpa only). Empty uses the default.
	Metric string `json:"metric,omitempty" yaml:"metric,omitempty"`
	// TimeoutSeconds bounds the duration of a single request (revision
	// spec.timeoutSeconds). Nil leaves the Knative default (300s).
	TimeoutSeconds *int `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	// Port is the single container port Knative routes to. A Knative revision
	// exposes exactly one port; zero uses the first published port, and a non-zero
	// value must match one of the spec's published ports. Additional published
	// ports are dropped with a warning.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`
	// Annotations are merged verbatim onto the revision template
	// (spec.template.metadata.annotations), for autoscaling knobs beyond the
	// fields above (e.g. autoscaling.knative.dev/window, .../scale-down-delay).
	// The fields above take precedence over a colliding key here.
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// validKnativeClasses and validKnativeMetrics are the accepted autoscaler class
// and metric words.
var (
	validKnativeClasses = map[string]bool{"kpa": true, "hpa": true}
	validKnativeMetrics = map[string]bool{"concurrency": true, "rps": true, "cpu": true}
)

// Validate checks the syntactically-decidable parts of a KnativeSpec so a deploy
// fails fast at the boundary (CLI, the descriptor loader, and the backend as
// defense in depth). A nil spec is valid. Whether the target cluster actually
// serves Knative is a runtime property enforced by the kubernetes backend, not
// here.
func (kn *KnativeSpec) Validate() error {
	if kn == nil {
		return nil
	}
	if kn.Class != "" && !validKnativeClasses[kn.Class] {
		return fmt.Errorf("knative: class %q must be one of kpa, hpa", kn.Class)
	}
	if kn.Metric != "" && !validKnativeMetrics[kn.Metric] {
		return fmt.Errorf("knative: metric %q must be one of concurrency, rps, cpu", kn.Metric)
	}
	if kn.Metric == "cpu" && kn.Class != "hpa" {
		return fmt.Errorf("knative: metric %q requires class hpa", kn.Metric)
	}
	if kn.MinScale != nil && *kn.MinScale < 0 {
		return fmt.Errorf("knative: minScale %d must not be negative", *kn.MinScale)
	}
	if kn.MaxScale != nil && *kn.MaxScale < 0 {
		return fmt.Errorf("knative: maxScale %d must not be negative", *kn.MaxScale)
	}
	if kn.MinScale != nil && kn.MaxScale != nil && *kn.MaxScale != 0 && *kn.MinScale > *kn.MaxScale {
		return fmt.Errorf("knative: minScale %d must not exceed maxScale %d", *kn.MinScale, *kn.MaxScale)
	}
	if kn.Target != nil && *kn.Target < 0 {
		return fmt.Errorf("knative: target %d must not be negative", *kn.Target)
	}
	if kn.Concurrency != nil && *kn.Concurrency < 0 {
		return fmt.Errorf("knative: concurrency %d must not be negative", *kn.Concurrency)
	}
	if kn.TimeoutSeconds != nil && *kn.TimeoutSeconds < 0 {
		return fmt.Errorf("knative: timeoutSeconds %d must not be negative", *kn.TimeoutSeconds)
	}
	if kn.Port < 0 {
		return fmt.Errorf("knative: port %d must not be negative", kn.Port)
	}
	return nil
}

// EgressRule maps a destination to a route. Pattern matches the destination host
// (glob, e.g. "*.internal"), a CIDR (e.g. "10.0.0.0/8"), and/or an explicit port
// (e.g. "api.example.com:443", "10.0.0.0/8:5432"); an empty host or port part
// matches any. Route is one of "client", "gateway", "cluster", or "deny". An
// allow entry routes matching traffic to a network; a "deny" entry blocks it.
type EgressRule struct {
	Pattern string `json:"pattern" yaml:"pattern"`
	Route   string `json:"route" yaml:"route"`
}

// DNSSpec configures the per-pod caretaker DNS resolver. Records maps a peer
// service name to the IPv4 address the pod should resolve it to (typically the
// peer's user-network / Multus-secondary address). Everything not in Records is
// forwarded to the cluster DNS. The kubernetes backend fills in the upstream
// (the kube-dns ClusterIP) and the pod's namespace search domain, and points the
// pod's dnsConfig at the sidecar.
//
// RequireUserNet marks Records that point at Multus SECONDARY (user-network)
// addresses pinned via NetworkAttachment.IP — the compose planner sets it. When
// the cluster cannot realise the Multus fabric (no NAD CRD: the netdriver falls
// back to services-only DNS), those addresses would never exist, so the
// kubernetes backend skips the DNS caretaker entirely and name resolution
// degrades gracefully to the cluster DNS instead of resolving peers to
// unreachable IPs. An explicit DNSSpec (RequireUserNet false) always injects.
type DNSSpec struct {
	Records        map[string]string `json:"records,omitempty" yaml:"records,omitempty"`
	RequireUserNet bool              `json:"requireUserNet,omitempty" yaml:"requireUserNet,omitempty"`
}

// DockerSpec configures the caretaker's Docker-API endpoint (kubernetes only). The
// caretaker binds a Docker Engine API proxy on a pod-loopback endpoint and the
// backend injects DOCKER_HOST into the app container so stock `docker` /
// `docker compose` drive the same cornus server that manages the pod's own stack.
//
// Transport selects the endpoint form: "tcp" (default) binds 127.0.0.1:Port;
// "unix" binds a socket at SocketPath on a shared volume; "both" binds both (and
// DOCKER_HOST then points at the TCP endpoint). Enabling this requires the server
// to have a client-scoped token Secret configured (CORNUS_CLIENT_TOKEN_SECRET) —
// the caretaker's own attach-scoped token cannot drive the client deploy API.
type DockerSpec struct {
	// Transport is "" / "tcp" (default), "unix", or "both".
	Transport string `json:"transport,omitempty" yaml:"transport,omitempty"`
	// Port is the loopback TCP port for the tcp / both transports (default 2375).
	Port int `json:"port,omitempty" yaml:"port,omitempty"`
	// SocketPath is the unix socket path for the unix / both transports (default
	// /cornus/docker/docker.sock, on a shared emptyDir).
	SocketPath string `json:"socketPath,omitempty" yaml:"socketPath,omitempty"`
	// EnvVar overrides the environment variable used to advertise the endpoint to
	// the app container (default DOCKER_HOST).
	EnvVar string `json:"envVar,omitempty" yaml:"envVar,omitempty"`
}

// TelemetrySpec configures the embedded OpenTelemetry Collector agent the
// caretaker runs for a workload. The Collector receives the app's OTLP telemetry
// on a pod-loopback endpoint and exports it directly to an external OTLP backend
// (Endpoint). The deploy backend auto-wires the app container to the receiver by
// injecting OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_PROTOCOL (and, when
// the user has not set them, OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES).
//
// The Collector must be compiled into the caretaker image (the release image is
// built with -tags otelcol); a deploy targeting a caretaker built without it
// fails its readiness probe with an actionable error rather than silently
// dropping telemetry.
type TelemetrySpec struct {
	// Enabled turns the Collector on. It lets the compose form be a bare
	// `x-cornus-telemetry: {}` (enabled, all defaults); a non-empty Endpoint
	// implies Enabled as well.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Endpoint is the external OTLP backend the Collector exports to (host:port for
	// grpc, URL for http/protobuf). Required when enabled unless a server default
	// is configured.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	// Protocol selects the exporter and the receiver endpoint advertised to the app:
	// "" / "grpc" (default) or "http/protobuf" ("http" is accepted as an alias).
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	// Headers are static headers added to every export request (e.g. an auth
	// token). Sensitive values are projected from a Secret by the backend rather
	// than embedded as a pod-spec literal.
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	// Insecure disables transport security to the export backend (plaintext / no
	// server-cert verification). Off by default.
	Insecure bool `json:"insecure,omitempty" yaml:"insecure,omitempty"`
	// Signals restricts which pipelines the Collector builds: any of "traces",
	// "metrics", "logs". Empty means all three.
	Signals []string `json:"signals,omitempty" yaml:"signals,omitempty"`
	// ServiceName overrides the OTEL_SERVICE_NAME injected into the app container.
	// Empty defaults to the deployment name. A user-set OTEL_SERVICE_NAME in the
	// spec's Env always wins over this.
	ServiceName string `json:"serviceName,omitempty" yaml:"serviceName,omitempty"`
	// ResourceAttributes are extra key=value resource attributes injected as
	// OTEL_RESOURCE_ATTRIBUTES (merged with cornus-derived defaults); a user-set
	// OTEL_RESOURCE_ATTRIBUTES in the spec's Env wins.
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty" yaml:"resourceAttributes,omitempty"`
	// GRPCPort / HTTPPort are the OTLP receiver loopback ports inside the pod
	// (default 4317 / 4318). Zero uses the default.
	GRPCPort int `json:"grpcPort,omitempty" yaml:"grpcPort,omitempty"`
	HTTPPort int `json:"httpPort,omitempty" yaml:"httpPort,omitempty"`
	// Debug additionally wires the Collector's debug exporter (verbose stdout) into
	// every pipeline, for troubleshooting.
	Debug bool `json:"debug,omitempty" yaml:"debug,omitempty"`
}

// validTelemetryProtocols is the set of accepted OTLP exporter protocols.
var validTelemetryProtocols = map[string]bool{
	"":              true, // defaults to grpc
	"grpc":          true,
	"http":          true, // alias for http/protobuf
	"http/protobuf": true,
}

// validTelemetrySignals is the set of pipelines the embedded Collector can build.
var validTelemetrySignals = map[string]bool{
	"traces":  true,
	"metrics": true,
	"logs":    true,
}

// Active reports whether the spec requests the Collector: an explicit Enabled, or
// an Endpoint that implies it. A nil spec is inactive.
func (t *TelemetrySpec) Active() bool {
	return t != nil && (t.Enabled || strings.TrimSpace(t.Endpoint) != "")
}

// Validate checks the syntactically-decidable parts of a TelemetrySpec so a
// deploy fails fast at the boundary (CLI, compose, and each backend as
// defense-in-depth). A nil/inactive spec is valid.
func (t *TelemetrySpec) Validate() error {
	if !t.Active() {
		return nil
	}
	if strings.TrimSpace(t.Endpoint) == "" {
		return fmt.Errorf("telemetry: endpoint is required (the OTLP backend to export to)")
	}
	if !validTelemetryProtocols[t.Protocol] {
		return fmt.Errorf("telemetry: protocol %q must be one of grpc, http/protobuf", t.Protocol)
	}
	for _, s := range t.Signals {
		if !validTelemetrySignals[s] {
			return fmt.Errorf("telemetry: signal %q must be one of traces, metrics, logs", s)
		}
	}
	if t.GRPCPort < 0 || t.GRPCPort > 65535 {
		return fmt.Errorf("telemetry: grpcPort %d out of range", t.GRPCPort)
	}
	if t.HTTPPort < 0 || t.HTTPPort > 65535 {
		return fmt.Errorf("telemetry: httpPort %d out of range", t.HTTPPort)
	}
	return nil
}

// HubSpec requests workload-to-workload overlay membership. Identity is the policy
// identity (defaults to the deployment name). Export lists services this workload
// hosts; Import lists services it reaches (the backend allocates a synthetic
// loopback IP per import and wires a DNS record + caretaker Reach listener to it).
// ImportDynamic additionally opts the workload into dynamic import discovery.
type HubSpec struct {
	Identity      string            `json:"identity,omitempty" yaml:"identity,omitempty"`
	Export        []HubExport       `json:"export,omitempty" yaml:"export,omitempty"`
	Import        []HubImport       `json:"import,omitempty" yaml:"import,omitempty"`
	ImportDynamic *HubImportDynamic `json:"importDynamic,omitempty" yaml:"importDynamic,omitempty"`
}

// HubExport is one service this workload hosts on the overlay. Deliver requests
// ingress delivery (the hub relays to this pod, which dials Port on localhost) so
// the service need not be reachable from the hub; otherwise the hub dials the
// workload's cluster Service directly. Protocol is "tcp" (default, empty) or
// "udp"; UDP datagrams ride the byte-agnostic hub relay length-prefix framed.
type HubExport struct {
	Name     string `json:"name" yaml:"name"`
	Port     int    `json:"port" yaml:"port"`
	Deliver  bool   `json:"deliver,omitempty" yaml:"deliver,omitempty"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // "tcp" (default) or "udp"
}

// HubImport is one service this workload reaches through the overlay, on the given
// ports. Protocol is "tcp" (default, empty) or "udp"; a UDP import binds a
// datagram Reach listener that frames each datagram over the hub.
type HubImport struct {
	Name     string `json:"name" yaml:"name"`
	Ports    []int  `json:"ports" yaml:"ports"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // "tcp" (default) or "udp"
}

// HubImportDynamic opts a workload into dynamic import discovery: instead of (or
// in addition to) a static Import list, the caretaker subscribes to hub catalog
// pushes and binds a loopback listener at the deterministic synthetic IP of EVERY
// cataloged service (excluding this workload's own exports and its static
// imports) on the given ports, adding listeners as services appear and closing
// them as they vanish. No DNS records are wired for dynamic imports — the names
// are unknown at deploy time. Ports is the shared port set bound per discovered
// service; Protocol is "tcp" (default, empty) or "udp".
type HubImportDynamic struct {
	Ports    []int  `json:"ports" yaml:"ports"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // "tcp" (default) or "udp"
}

// CredentialSpec brokers client-sourced credentials into a workload. Each entry
// of Sources is one credential the container can retrieve on demand; the secret
// value is minted on the client (never carried in this spec) and delivered
// through the cornus server and the caretaker sidecar.
type CredentialSpec struct {
	Sources []CredentialSource `json:"sources,omitempty" yaml:"sources,omitempty"`
}

// CredentialSource is one brokered credential: a client-side backend that mints
// it, plus zero or more provider-agnostic ways the container consumes it. The
// core model is deliberately cloud-neutral — cloud specifics live only inside
// the named Backend (client) and Provider/Format (delivery) plug-ins.
type CredentialSource struct {
	// Name is the logical credential name. It is the backing name the caretaker
	// requests over the relay and doubles as the capability key (a workload may
	// only fetch a source its own deploy-attach session declared), and the
	// default file basename / endpoint path segment.
	Name string `json:"name" yaml:"name"`
	// Backend names the CLIENT-side source backend that mints the credential
	// (e.g. "aws-sts", "static", "exec"). It runs on the caller's machine, using
	// the caller's own cloud/API credentials.
	Backend string `json:"backend" yaml:"backend"`
	// Config carries non-secret backend configuration (e.g. role_arn, duration,
	// region for aws-sts; a literal or file path for static). It must never hold
	// the secret itself — the secret is produced by the backend at fetch time.
	Config map[string]string `json:"config,omitempty" yaml:"config,omitempty"`
	// TTL is a client-side cache/refresh hint (a Go duration string). Empty uses
	// the backend's own default (or the credential's own expiry when it has one).
	TTL string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	// Deliver lists how the container consumes the credential. An empty list is
	// valid (the source is fetchable but not surfaced); typically one endpoint
	// and/or one file.
	Deliver []CredentialDelivery `json:"deliver,omitempty" yaml:"deliver,omitempty"`
}

// CredentialDelivery is one provider-agnostic way to surface a credential to the
// container. Kind is the discriminator; the remaining fields are kind-specific
// and default to cloud-neutral values so a workload needs no cloud vocabulary.
type CredentialDelivery struct {
	// Kind is "endpoint" (an HTTP metadata server / auth-injecting proxy), "file"
	// (materialize to a path in a shared volume), or "env" (inject into the app
	// container's environment). Empty defaults to "endpoint".
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Provider (endpoint kind) names the delivery provider. "" / "generic" serves
	// the cornus-native JSON contract (GET /credentials/<name> ->
	// {"values":{...},"expiration":"..."}); "aws-imds" and future adapters render
	// the same credential in a cloud SDK's expected shape.
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	// WellKnown (endpoint kind) binds the provider's canonical/link-local address
	// (e.g. AWS 169.254.169.254) inside the pod netns so SDKs that hardcode it
	// work with no env injection. It needs NET_ADMIN on the caretaker; when false
	// the endpoint binds loopback and is advertised to the app via env vars.
	WellKnown bool `json:"wellKnown,omitempty" yaml:"wellKnown,omitempty"`
	// Upstream (endpoint kind, auth-proxy providers) overrides the vendor API the
	// proxy forwards to — an Anthropic-/OpenAI-compatible gateway (on-prem proxy,
	// Azure OpenAI) or a test mock. Empty uses the provider's real default
	// (https://api.anthropic.com / https://api.openai.com). Non-secret.
	Upstream string `json:"upstream,omitempty" yaml:"upstream,omitempty"`

	// Path (file kind) is the container path to materialize the credential to.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// Format (file kind) is the render format: "" / "json" (the neutral
	// {values,expiration} object), "env" (KEY=VALUE lines), "raw" (a single
	// value), or "aws-credentials" (an ini profile).
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	// EnvVar (env kind) is the app-container environment variable to set. The
	// value is fetched from the client ONCE at deploy time and materialized into
	// a Kubernetes Secret referenced via secretKeyRef — so it is not a pod-spec
	// literal, but it IS static (no runtime refresh) and lives in etcd. This is
	// the convenient-but-weaker option for SDKs that cannot override a base URL;
	// prefer the proxy or file delivery for short-lived / never-materialized
	// credentials.
	EnvVar string `json:"envVar,omitempty" yaml:"envVar,omitempty"`
	// ValueKey (env kind) selects which credential Values key supplies the env
	// value; empty tries "value" then "token".
	ValueKey string `json:"valueKey,omitempty" yaml:"valueKey,omitempty"`
}

// Origin records the lineage of a deployment — where it came from. Every field
// but Subject is client-attested (the caller reports its own hostname, OS user,
// launch directory, project, and git repo); Subject is stamped by the server
// from the authenticated request identity and cannot be forged by the client.
// All fields are best-effort: a field the client could not determine (or the
// server could not authenticate) is simply empty.
type Origin struct {
	// Project is the owning project — the Compose project name for a compose
	// deploy, or the `--project` value for a raw `cornus deploy -f`. Empty when
	// the deployment has no project context.
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// Host is the client machine hostname the deploy was spawned from
	// (client-attested).
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
	// User is the client OS user that spawned the deploy (client-attested).
	User string `json:"user,omitempty" yaml:"user,omitempty"`
	// Directory is the absolute client-side directory the deploy was launched
	// from — the Compose file's directory, or the working directory for a raw
	// `deploy -f` (client-attested).
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
	// Git describes the git repository the origin Directory belongs to, when it
	// is one. Nil when the directory is not a git working tree.
	Git *GitOrigin `json:"git,omitempty" yaml:"git,omitempty"`
	// Subject is the authenticated identity of the deploy request (the bearer
	// token's JWT subject). SERVER-STAMPED: the server overwrites whatever the
	// client sent, and it is empty when auth is disabled. Never trust a
	// client-supplied value here.
	Subject string `json:"subject,omitempty" yaml:"subject,omitempty"`
}

// GitOrigin captures the git provenance of an Origin.Directory (client-attested,
// best-effort).
type GitOrigin struct {
	// Remote is the `origin` remote URL, empty when there is no such remote.
	Remote string `json:"remote,omitempty" yaml:"remote,omitempty"`
	// Branch is the checked-out branch, empty on a detached HEAD.
	Branch string `json:"branch,omitempty" yaml:"branch,omitempty"`
	// Commit is the full HEAD commit SHA.
	Commit string `json:"commit,omitempty" yaml:"commit,omitempty"`
	// Dirty is true when the working tree had uncommitted changes at deploy time.
	Dirty bool `json:"dirty,omitempty" yaml:"dirty,omitempty"`
}

// DeployStatus reports the observed state of a deployment.
type DeployStatus struct {
	Name      string           `json:"name"`
	Image     string           `json:"image"`
	Instances []InstanceStatus `json:"instances"`
	Backend   string           `json:"backend"`
	// Origin is the deployment's recorded lineage, read back from the backend
	// (labels / annotations / record). Nil when the deployment carries no origin
	// metadata (e.g. deployed before this was recorded).
	Origin *Origin `json:"origin,omitempty" yaml:"origin,omitempty"`
	// URL, when non-empty, is the address the workload is reachable at, reported
	// by a backend that fronts it with a router of its own — the kubernetes
	// backend surfaces a Knative Service's status.url here. Empty on backends and
	// workloads with no such address.
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
}

// InstanceStatus is the state of a single running instance.
type InstanceStatus struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Running bool   `json:"running"`
	// Health is the container health state when the workload defines a healthcheck,
	// in Docker's vocabulary: "healthy", "unhealthy", or "starting". Empty means the
	// backend cannot report health (no healthcheck, or a backend without a health
	// concept). Compose `depends_on: {condition: service_healthy}` gates on "healthy".
	Health string `json:"health,omitempty" yaml:"health,omitempty"`
	// ExitCode is the exit status of the instance's main process once it has
	// terminated; nil while running or when the backend cannot report it. Compose
	// `depends_on: {condition: service_completed_successfully}` gates on a 0 ExitCode.
	ExitCode *int `json:"exitCode,omitempty" yaml:"exitCode,omitempty"`
	// Message is an optional human-readable diagnostic for an instance that is NOT
	// running — e.g. a container's Waiting reason ("CrashLoopBackOff",
	// "ImagePullBackOff") or a scheduling failure. Empty when the instance is
	// running or the backend has nothing to report. The deploy-attach readiness
	// wait streams it so a wedged workload is reported instead of hanging silently.
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// LogOptions controls a deploy.Backend.Logs stream (docker logs semantics).
//
// Stdout and Stderr select which streams to include; when NEITHER is set both
// default to true (stream everything) — use Streams to resolve the effective
// selection. Tail is "all" or a decimal count of trailing lines ("" means all).
// Since is an optional lower time bound (Unix seconds or an RFC3339 timestamp,
// per the backend). Follow keeps the stream open, tailing new output until the
// caller's context is cancelled.
type LogOptions struct {
	Follow     bool
	Tail       string
	Stdout     bool
	Stderr     bool
	Timestamps bool
	Since      string
	// Until, when set, suppresses log lines with a timestamp at or after it
	// (compose logs --until). Same grammar as Since (deploy.ParseSince). The
	// kubernetes backend cannot honor it (pods/log has no "until") and warns.
	Until string
}

// Streams returns the effective (stdout, stderr) selection: both true when
// neither flag is set, otherwise the flags as given.
func (o LogOptions) Streams() (stdout, stderr bool) {
	if !o.Stdout && !o.Stderr {
		return true, true
	}
	return o.Stdout, o.Stderr
}

// StatsOptions controls a deploy.Backend.Stats stream (docker stats semantics).
// When Stream is true the backend keeps emitting one Docker-format stats JSON
// object per interval; when false a single object is returned then the stream
// ends (docker's --no-stream / ?stream=0).
type StatsOptions struct {
	Stream bool
}

// ExecConfig describes an exec to create inside a deployment's instance (docker
// exec / POST /containers/{id}/exec). Cmd is the command to run; the Attach*
// flags and Tty mirror Docker's exec-create body. The backend runs the exec in
// the deployment's first instance.
type ExecConfig struct {
	Cmd          []string
	Tty          bool
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
	Env          []string
	WorkingDir   string
	User         string
	Privileged   bool
	// ForwardAgent forwards the caller's local ssh-agent into the exec'd
	// process's environment (SSH_AUTH_SOCK), relayed through a caretaker (see
	// pkg/caretaker's AgentRelayRole). Only supported when the backend is
	// deploy.RemoteCapable and Remote() (dockerhost/containerdhost), or the
	// backend implements deploy.AgentForwardCapable and the target deployment
	// opted in (kubernetes — see DeploySpec.AgentForward) — the server rejects
	// it otherwise. Like ssh -A, only use this against a cornus server you
	// trust.
	ForwardAgent bool
}

// ExecStartConfig controls starting a previously-created exec (docker exec
// start / POST /exec/{id}/start). Tty must match the exec's create-time Tty;
// Detach runs the exec without attaching a stream.
type ExecStartConfig struct {
	Tty    bool
	Detach bool
}

// ExecState reports an exec's status (docker exec inspect / GET /exec/{id}/json).
type ExecState struct {
	Running  bool
	ExitCode int
	Pid      int
}

// AttachConfig controls attaching to a running deployment instance (docker
// attach / POST /containers/{id}/attach). Stream keeps the connection open for
// live IO; Stdin/Stdout/Stderr select which streams to attach; Logs replays
// prior output before streaming.
type AttachConfig struct {
	Stream bool
	Stdin  bool
	Stdout bool
	Stderr bool
	Logs   bool
}

// PortForwardConfig is the newline-JSON preamble a caller writes as the first
// frame on a port-forward WS tunnel (WS /.cornus/v1/deploy/{name}/portforward). Port is
// the container port to reach inside the deployment's first instance; Protocol is
// "tcp" (the default when empty) or "udp". A tcp tunnel carries the raw byte
// stream of one connection; a udp tunnel carries length-prefixed datagrams (the
// wire package's WriteDatagram framing) for one client flow, and the server
// answers the preamble with a PortForwardAck before any frames flow. UDP is not
// supported on the kubernetes backend, whose port-forward subresource is
// TCP-only; its ack reports the rejection.
type PortForwardConfig struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol,omitempty"`
}

// PortForwardAck is the newline-JSON acknowledgement the server writes back on a
// port-forward tunnel whose PortForwardConfig requested Protocol "udp", before
// any datagram frames flow. An empty Error means the backend accepted the UDP
// forward and framed datagrams follow; a non-empty Error means the backend
// cannot forward UDP (e.g. kubernetes) and the server closes the tunnel. TCP
// tunnels have no ack — their wire format is unchanged, so old clients and
// servers interoperate on tcp.
type PortForwardAck struct {
	Error string `json:"error,omitempty"`
}

// PathStat mirrors Docker's container.PathStat: metadata about a path inside a
// container. It is carried on the container archive endpoint in the
// X-Docker-Container-Path-Stat response header as base64-encoded JSON. Mode
// holds os.FileMode bits exactly as Docker encodes them and is passed through
// unchanged (kept a uint32 so no re-interpretation happens in transit).
type PathStat struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	Mtime      string `json:"mtime"`
	LinkTarget string `json:"linkTarget"`
}

// CopyToOptions controls extracting a tar into a container path (docker cp into
// a container), mirroring Docker's archive PUT query params.
type CopyToOptions struct {
	NoOverwriteDirNonDir bool
	CopyUIDGID           bool
}

// PathStatHeader is the HTTP header Docker uses to carry a PathStat (base64 JSON)
// on the container archive endpoint. The same header name is used end to end so
// the value round-trips unchanged from the Docker host through cornus to the
// docker CLI.
const PathStatHeader = "X-Docker-Container-Path-Stat"

// StreamErrorTrailer is the HTTP TRAILER the server sets on a streaming deploy
// endpoint (logs, stats, archive GET) when the backend fails AFTER the first
// body byte has been sent. At that point the 200 status is already committed
// and there is no in-band error channel, so without the trailer a mid-stream
// failure looks like a clean EOF — the client cannot distinguish truncation
// from completion. The trailer carries the sanitized backend error message; it
// is absent (empty) on success. Clients read it from resp.Trailer after
// draining the body to EOF (Go's http client populates response trailers only
// then). Trailers ride HTTP/1.1 chunked encoding, which these streams always
// use (no Content-Length, flushed per write).
const StreamErrorTrailer = "X-Cornus-Stream-Error"

// ServerInfo is the server's self-description returned by GET /.cornus/v1/info. It lets a
// client learn the registry address the server's deploy targets can actually pull
// from — which, on a real cluster, differs from the client's own control-plane
// endpoint (a port-forward's localhost, an ingress host, etc.). RegistryHost is the
// "host[:port]" a deploy pull ref should carry; RegistryScheme is "http" or "https"
// so the client (and, for host-level backends, the resolver) knows whether the
// registry speaks plain HTTP. Both are empty when the server cannot determine its
// own advertised address, in which case the client falls back to its endpoint host.
type ServerInfo struct {
	RegistryHost   string `json:"registry_host,omitempty"`
	RegistryScheme string `json:"registry_scheme,omitempty"`
	// Ingress, when non-nil, describes the cluster's HTTP(S) ingress front door so a
	// client can reach a deployment's ingress host through the SOCKS5 conduit: the
	// base domain and class the server derives Ingress objects from, plus (when the
	// server could discover it) the in-cluster controller Service a native
	// passthrough tunnels to. Empty/nil when the backend cannot introspect an ingress
	// (non-kubernetes, or no controller found), in which case a client falls back to
	// client-side emulation.
	Ingress *IngressInfo `json:"ingress,omitempty"`
}

// IngressInfo is the server's advertised ingress facts, reported inside ServerInfo.
// Domain and Class come from the server's ingress defaults (CORNUS_INGRESS_DOMAIN /
// CORNUS_INGRESS_CLASS); Controller, when non-nil, names the discovered ingress
// controller Service a client can port-forward to for native passthrough.
type IngressInfo struct {
	Domain     string             `json:"domain,omitempty"`
	Class      string             `json:"class,omitempty"`
	Controller *IngressController `json:"controller,omitempty"`
}

// IngressController names the cluster's ingress controller Service and its HTTP and
// HTTPS ports, discovered server-side, so a client's native SOCKS5 passthrough can
// port-forward to it and let the real controller do Host/path routing and TLS.
type IngressController struct {
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	HTTPPort  int    `json:"http_port,omitempty"`
	HTTPSPort int    `json:"https_port,omitempty"`
}

// EncodePathStat marshals a PathStat into the base64-JSON form Docker places in
// the X-Docker-Container-Path-Stat header.
func EncodePathStat(st PathStat) (string, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecodePathStat parses a base64-JSON X-Docker-Container-Path-Stat header value
// into a PathStat. An empty value yields a zero PathStat.
func DecodePathStat(s string) (PathStat, error) {
	var st PathStat
	if s == "" {
		return st, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}
