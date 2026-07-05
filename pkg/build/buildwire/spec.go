// Package buildwire carries a remote cornus build over a single WebSocket:
// the byte stream is yamux-multiplexed into a control channel (the build spec +
// streamed progress) and a 9P channel over which the caller serves its own
// directories and secrets to the build engine on demand. The caller is the 9P
// server (it exports its local files); the cornus server is the 9P client.
package buildwire

import (
	"cornus/pkg/build/buildprog"
	"cornus/pkg/build/internal/lazyctx"
)

// LazySpec is a named build context the caller serves lazily: the caller
// computes its content identity (LayerDigest) and per-file content digests
// locally, the server serves the files on demand over 9P (tagLazy9P), and seeds
// BuildKit's contenthash from Digests so the RUN cache-key scan is skipped.
type LazySpec struct {
	Name        string               `json:"name"`
	LayerDigest string               `json:"layerDigest"`
	LayerSize   int64                `json:"layerSize"`
	Digests     []lazyctx.FileDigest `json:"digests"`
}

// BuildSpec is sent by the caller over the control stream to start a build.
type BuildSpec struct {
	// LazyContexts are named contexts served on demand over 9P (see LazySpec),
	// instead of eagerly synced. The caller exports their dirs via Serve9PBacking.
	LazyContexts []LazySpec `json:"lazyContexts,omitempty"`
	// Target image reference, e.g. "localhost:5000/app:v1".
	Target string `json:"target"`
	// TargetStage is the Dockerfile multi-stage target stage (build.target); it
	// maps to the dockerfile frontend's "target" attr. Empty builds the final stage.
	TargetStage string `json:"targetStage,omitempty"`
	// DockerfileName is the Dockerfile's basename within the exported
	// "dockerfile" tree (e.g. "Dockerfile").
	DockerfileName string `json:"dockerfile"`
	// BuildArgs are Dockerfile ARG values.
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// NamedContexts are the names of additional build contexts the caller
	// exports under /ctx/<name> (for RUN --mount=type=bind,from=<name>).
	NamedContexts []string `json:"namedContexts,omitempty"`
	// SecretIDs are the ids the caller exports under /secrets/<id>.
	SecretIDs []string `json:"secretIds,omitempty"`
	// SSHIDs are the SSH agent ids the caller forwards (RUN --mount=type=ssh).
	SSHIDs []string `json:"sshIds,omitempty"`
	// CacheExports / CacheImports are the build-cache backends (buildx
	// --cache-to / --cache-from) the remote build should use.
	CacheExports []CacheOption `json:"cacheExports,omitempty"`
	CacheImports []CacheOption `json:"cacheImports,omitempty"`
	Push         bool          `json:"push"`
	Insecure     bool          `json:"insecure"`
	NoCache      bool          `json:"noCache"`
	// Pull always attempts to pull a newer base image (build.pull); maps to the
	// frontend "image-resolve-mode: pull" attr.
	Pull bool `json:"pull,omitempty"`
	// Labels are image labels applied to the built image (build.labels); map to
	// the "label:<k>=<v>" frontend attrs.
	Labels map[string]string `json:"labels,omitempty"`
	// Platforms are the target build platforms (build.platforms); map to the
	// "platform" frontend attr (comma-joined).
	Platforms []string `json:"platforms,omitempty"`
	// Tags are additional image references for the result (build.tags), added to
	// the image exporter's name list beyond Target.
	Tags []string `json:"tags,omitempty"`
	// Network is the build-time network mode (build.network): default/none/host;
	// maps to the "force-network-mode" frontend attr.
	Network string `json:"network,omitempty"`
	// ExtraHosts adds custom /etc/hosts entries during the build (build.extra_hosts),
	// each "host:ip"; map to the "add-hosts" frontend attr.
	ExtraHosts []string `json:"extraHosts,omitempty"`
	// ShmSize sizes /dev/shm for RUN steps in bytes (build.shm_size); maps to the
	// "shm-size" frontend attr.
	ShmSize int64 `json:"shmSize,omitempty"`
}

// CacheOption is a build-cache backend entry carried over the wire, mirroring
// builder.CacheOption / a BuildKit CacheOptionsEntry.
type CacheOption struct {
	Type  string            `json:"type"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Result is the outcome of a remote build.
type Result struct {
	ImageDigest string `json:"imageDigest,omitempty"`
}

// controlMsg is a server→caller progress/result frame on the control stream.
// Event carries a structured build-progress update (the caller renders it); Done
// with Digest/Err is the terminal result frame.
type controlMsg struct {
	Event  *buildprog.Event `json:"event,omitempty"`
	Done   bool             `json:"done,omitempty"`
	Digest string           `json:"digest,omitempty"`
	Err    string           `json:"error,omitempty"`
}

// Stream tags identify the build-specific yamux streams. The eager 9P export is
// opened by the caller at startup alongside wire.TagControl; SSH streams are
// opened by the server on demand. The control tag and the lazy-9P backing tag
// live in pkg/wire (wire.TagControl and the transport's 'L' backing).
const (
	tagP9  = '9'
	tagSSH = 'S'
	// tagLazy9P mirrors wire's private lazy-9P backing tag ('L'): the build
	// server opens an 'L'-tagged stream per on-demand lazy-context read. The
	// caller runs a single accept loop (serveCallerStreams) that must route both
	// SSH and lazy-9P streams, so it needs the tag value here to dispatch by it.
	tagLazy9P = 'L'
)
