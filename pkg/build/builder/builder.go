// Package builder is cornus's container build engine. It embeds the BuildKit
// solver in-process (no separate buildkitd daemon) and drives it over an
// in-memory gRPC loopback using BuildKit's own client, so Dockerfile builds,
// cache mounts (RUN --mount=type=cache), and secret mounts
// (RUN --mount=type=secret) all work exactly as they do with `docker buildx`.
package builder

import (
	"fmt"
	"io"
	"os"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets"
	"github.com/tonistiigi/fsutil"

	"cornus/pkg/build/internal/lazyctx"
)

// SolveInput is the lower-level build request used by both the local CLI build
// and the remote (9P/WebSocket) build. Its mounts and secret store are supplied
// by the caller: local directories for `cornus build`, or 9P-backed sources
// for a remote build. The interfaces are cross-platform, so this stays out of
// the linux-only build tag.
type SolveInput struct {
	// Target image reference.
	Target string
	// TargetStage is the Dockerfile multi-stage target (build.target); it maps to
	// the dockerfile frontend's "target" attr. Empty builds the final stage.
	TargetStage string
	// DockerfileName is the Dockerfile's basename within the "dockerfile" mount.
	DockerfileName string
	// BuildArgs are Dockerfile ARG values.
	BuildArgs map[string]string
	// Mounts are the build's local mounts: "context", "dockerfile", and one per
	// eagerly-synced named build context (RUN --mount=type=bind,from=<name>).
	Mounts map[string]fsutil.FS
	// LazyContexts are named build contexts served lazily via an oci-layout image
	// + the "stargz" remote snapshotter, so they are read on demand instead of
	// copied into a snapshot. Empty unless lazy builds are enabled.
	LazyContexts []*lazyctx.LazyContext
	// Secrets, if non-nil, serves RUN --mount=type=secret values.
	Secrets secrets.SecretStore
	// SSH, if non-nil, forwards SSH agents for RUN --mount=type=ssh.
	SSH      session.Attachable
	Push     bool
	Insecure bool
	NoCache  bool
	// Pull always attempts to pull a newer base image (build.pull); maps to the
	// "image-resolve-mode: pull" frontend attr.
	Pull bool
	// Labels are image labels applied to the built image (build.labels); map to
	// the "label:<k>=<v>" frontend attrs.
	Labels map[string]string
	// Platforms are the target build platforms (build.platforms); map to the
	// "platform" frontend attr (comma-joined). Multi-platform output also needs
	// the worker's emulators.
	Platforms []string
	// Tags are additional image references for the result (build.tags), added to
	// the image exporter's name list beyond Target.
	Tags []string
	// Network is the build-time network mode (build.network): default/none/host;
	// maps to the "force-network-mode" frontend attr.
	Network string
	// ExtraHosts adds custom /etc/hosts entries during the build (build.extra_hosts),
	// each "host:ip"; map to the "add-hosts" frontend attr.
	ExtraHosts []string
	// ShmSize sizes /dev/shm for RUN steps in bytes (build.shm_size); maps to the
	// "shm-size" frontend attr.
	ShmSize int64
	// CacheExports / CacheImports are the build-cache backends passed to the
	// solve (registry/local/inline).
	CacheExports []CacheOption
	CacheImports []CacheOption
	// DockerArchiveOutput, when non-nil, switches the exporter from a registry
	// push to a docker-archive (docker save format) streamed to the returned
	// writer. It is how docker-daemon re-export mode lands a build in the local
	// daemon: the server pipes the archive into POST /images/load. Push is ignored
	// when set, and Target/Tags are exported verbatim so the daemon tags them
	// exactly as they will be deployed.
	DockerArchiveOutput func(map[string]string) (io.WriteCloser, error)
}

// Config configures the build engine's worker and cache state.
type Config struct {
	// Root is the directory for the BuildKit worker, snapshots, and cache
	// databases (typically config.Config.CacheDir()).
	Root string
	// Rootless runs the runc executor in rootless mode (user namespaces).
	Rootless bool
	// Worker selects the BuildKit worker backend: WorkerRunc (the default,
	// self-contained runc executor + local snapshotter) or WorkerContainerd
	// (delegates execution, snapshots, and content to an external containerd
	// daemon). Empty resolves from CORNUS_BUILD_WORKER, defaulting to runc.
	Worker string
	// Containerd configures the containerd worker (used only when Worker is
	// WorkerContainerd). Empty fields resolve from the environment; see
	// ContainerdConfig.
	Containerd ContainerdConfig
}

// Worker backend names accepted by Config.Worker / CORNUS_BUILD_WORKER.
const (
	// WorkerRunc is the default self-contained worker: BuildKit's runc executor
	// with an in-process overlayfs/native snapshotter under Config.Root.
	WorkerRunc = "runc"
	// WorkerContainerd delegates execution, snapshots, content, and the image
	// store to an external containerd daemon. Built images tagged with a name
	// are recorded in containerd's image store. Incompatible with the lazy
	// build-context path (CORNUS_LAZY_BUILD / --lazy), which relies on the
	// runc worker's in-process snapshotter plumbing.
	WorkerContainerd = "containerd"
)

// ContainerdConfig locates the containerd daemon backing the containerd
// worker. Empty fields resolve from the environment in resolveWorkerConfig.
type ContainerdConfig struct {
	// Address is the containerd GRPC socket. Empty resolves from
	// CORNUS_CONTAINERD_ADDRESS, then the standard CONTAINERD_ADDRESS, then
	// defaults to /run/containerd/containerd.sock.
	Address string
	// Namespace is the containerd namespace the worker operates in. Empty
	// resolves from CORNUS_CONTAINERD_NAMESPACE, defaulting to "cornus".
	Namespace string
	// Snapshotter is the containerd snapshotter name. Empty resolves from
	// CORNUS_CONTAINERD_SNAPSHOTTER, defaulting to "overlayfs".
	Snapshotter string
}

// Environment knobs for worker selection and the containerd worker. The
// CORNUS_* variables win over the standard CONTAINERD_ADDRESS fallback.
const (
	buildWorkerEnv           = "CORNUS_BUILD_WORKER"
	containerdAddressEnv     = "CORNUS_CONTAINERD_ADDRESS"
	containerdAddressStdEnv  = "CONTAINERD_ADDRESS"
	containerdNamespaceEnv   = "CORNUS_CONTAINERD_NAMESPACE"
	containerdSnapshotterEnv = "CORNUS_CONTAINERD_SNAPSHOTTER"
)

// Defaults applied by resolveWorkerConfig when neither Config nor the
// environment provides a value.
const (
	defaultContainerdAddress     = "/run/containerd/containerd.sock"
	defaultContainerdNamespace   = "cornus"
	defaultContainerdSnapshotter = "overlayfs"
)

// resolveWorkerConfig fills Config's worker-selection fields from the
// environment and defaults, and validates the worker name. Explicit Config
// values win over the environment; empty environment variables count as unset.
// It is cross-platform (pure env reads) so both the linux engine and the stub
// build compile against it.
func resolveWorkerConfig(cfg Config) (Config, error) {
	if cfg.Worker == "" {
		cfg.Worker = os.Getenv(buildWorkerEnv)
	}
	if cfg.Worker == "" {
		cfg.Worker = WorkerRunc
	}
	switch cfg.Worker {
	case WorkerRunc, WorkerContainerd:
	default:
		return Config{}, fmt.Errorf("builder: unknown build worker %q (set Config.Worker or %s to %q or %q)", cfg.Worker, buildWorkerEnv, WorkerRunc, WorkerContainerd)
	}
	if cfg.Containerd.Address == "" {
		cfg.Containerd.Address = os.Getenv(containerdAddressEnv)
	}
	if cfg.Containerd.Address == "" {
		cfg.Containerd.Address = os.Getenv(containerdAddressStdEnv)
	}
	if cfg.Containerd.Address == "" {
		cfg.Containerd.Address = defaultContainerdAddress
	}
	if cfg.Containerd.Namespace == "" {
		cfg.Containerd.Namespace = os.Getenv(containerdNamespaceEnv)
	}
	if cfg.Containerd.Namespace == "" {
		cfg.Containerd.Namespace = defaultContainerdNamespace
	}
	if cfg.Containerd.Snapshotter == "" {
		cfg.Containerd.Snapshotter = os.Getenv(containerdSnapshotterEnv)
	}
	if cfg.Containerd.Snapshotter == "" {
		cfg.Containerd.Snapshotter = defaultContainerdSnapshotter
	}
	return cfg, nil
}

// CacheOption is a build-cache import/export backend entry, mapped 1:1 to a
// BuildKit CacheOptionsEntry. Type is the backend ("registry", "local",
// "inline"); Attrs carries backend params (e.g. "ref", "registry.insecure").
type CacheOption struct {
	Type  string
	Attrs map[string]string
}

// SecretSource maps a build-time secret id to a file providing its value,
// consumed by `RUN --mount=type=secret,id=<ID>`.
type SecretSource struct {
	ID   string
	Path string
}

// SSHSource maps an SSH agent id to the local agent socket forwarded for
// `RUN --mount=type=ssh,id=<ID>`.
type SSHSource struct {
	ID     string
	Socket string
}

// Request describes a single image build.
type Request struct {
	// ContextDir is the build context (directory sent to the build).
	ContextDir string
	// Dockerfile is the Dockerfile path relative to ContextDir.
	Dockerfile string
	// Target image reference, e.g. "localhost:5000/app:v1".
	Target string
	// TargetStage is the Dockerfile multi-stage target stage (build.target).
	// Empty builds the final stage.
	TargetStage string
	// BuildArgs are Dockerfile ARG values.
	BuildArgs map[string]string
	// Secrets are build-time secret mounts.
	Secrets []SecretSource
	// NamedContexts are additional build contexts (name -> local directory) for
	// RUN --mount=type=bind,from=<name>.
	NamedContexts map[string]string
	// SSH are SSH agents forwarded for RUN --mount=type=ssh.
	SSH []SSHSource
	// NoCache disables the build cache for this build.
	NoCache bool
	// Lazy serves NamedContexts on demand over the lazy path (oci-layout + the
	// "stargz" remote snapshotter) instead of eagerly syncing them. Also enabled
	// server-wide by CORNUS_LAZY_BUILD.
	Lazy bool
	// CacheExports / CacheImports are build-cache backends (buildx --cache-to /
	// --cache-from), e.g. {Type: "registry", Attrs: {"ref": "host/app:cache"}}.
	CacheExports []CacheOption
	CacheImports []CacheOption
	// Push pushes the resulting image to its registry (Target's registry).
	Push bool
	// Insecure allows pushing to an HTTP (non-TLS) registry, as cornus's
	// own registry is by default.
	Insecure bool
	// DockerArchiveOutput mirrors SolveInput.DockerArchiveOutput: when non-nil the
	// build is exported as a docker-archive to the returned writer (docker-daemon
	// re-export mode) instead of pushed to a registry.
	DockerArchiveOutput func(map[string]string) (io.WriteCloser, error)
}

// Result is the outcome of a successful build.
type Result struct {
	// ImageDigest is the digest of the produced image manifest, if reported.
	ImageDigest string
}
