// Package config holds cornus's runtime configuration and the on-disk
// layout shared by the registry, build engine, and deploy engine.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// Config is the fully-resolved runtime configuration for a cornus server.
type Config struct {
	// DataDir is the root of all persistent state: the registry CAS and the
	// build cache. It is mounted as a volume (Docker) or a PVC (Kubernetes).
	DataDir string
	// HTTPAddr is the listen address for the unified HTTP server that serves
	// /v2/* (registry) and /.cornus/v1/* (build, deploy).
	HTTPAddr string
	// Rootless runs the in-process BuildKit solver in rootless mode, using
	// user namespaces instead of requiring root.
	Rootless bool
	// BuilderURL delegates builds to an upstream cornus builder instead of
	// running them in-process. It exists for hosts where the in-process BuildKit
	// engine cannot run at all: BuildKit mounts every snapshot, and mount(2)
	// needs CAP_SYS_ADMIN, so an unprivileged `cornus serve` fails every build.
	// Pointing this at a privileged cornus (typically a local container) lets an
	// unprivileged server serve builds. Empty keeps the in-process engine.
	BuilderURL string
	// BuilderAuto lets the server start a containerized builder by itself when
	// the in-process engine cannot run (mount(2) is not permitted) and no
	// BuilderURL is set. It only engages where builds would otherwise fail
	// outright, so it cannot change a host that already builds successfully.
	BuilderAuto bool
	// BuilderImage pins a published cornus image for the auto-started builder
	// container. Empty — the default — builds a throwaway image containing the
	// running binary, so the builder is byte-identical to this server.
	BuilderImage string
	// BuilderBaseImage is the base for that self-built image. Empty matches the
	// host distribution, which matters because a locally built cornus is usually
	// dynamically linked against the host libc.
	BuilderBaseImage string
	// StorageURL selects the registry persistence backend. Empty means the
	// default filesystem layout under DataDir. Examples: "mem://",
	// "s3://bucket?region=us-east-1&endpoint=...&path_style=true".
	StorageURL string

	// TLSClientCAFile is a PEM CA bundle used to verify client certificates
	// (mTLS). It lives here, rather than only on Server, because a configured
	// client-cert CA is an AUTHENTICATION method: it turns auth on by itself, and
	// the authenticator is built inside server.New. The Server field of the same
	// name (which the TLS listener reads in Run) is populated from this. Setting
	// one without the other is the bug this field exists to prevent: the flag form
	// used to configure certificate verification while leaving the auth layer
	// believing mTLS was off, because that layer re-read the env var directly.
	TLSClientCAFile string

	// FileCacheEnabled turns on the server-side per-file block cache for on-demand
	// remote file reads over 9P (see pkg/blockcache, pkg/wire ServeCachingProxy).
	// When off, the kernel-9p mount paths blindly pipe frames as before.
	FileCacheEnabled bool
	// FileCacheChunkSize is the fixed cache chunk size in bytes. Zero selects
	// blockcache.DefaultChunkSize (1 MiB, matching the kernel-9p mount msize).
	FileCacheChunkSize int64
	// FileCacheMaxBytes is the soft on-disk size cap enforced by GC pruning; zero
	// disables the size cap (TTL pruning still applies).
	FileCacheMaxBytes int64
	// FileCacheDir is the directory holding the block cache's backing files. It is
	// mandatory when FileCacheEnabled and has no default: operators always place
	// the cache on an explicit (typically dedicated) volume rather than silently
	// sharing the data-dir volume that holds the registry CAS and build cache.
	FileCacheDir string

	// ObsEnabled turns on the built-in observability store (pkg/obsstore): a
	// durable, queryable home for a *workload's* logs, traces, and metrics, so a
	// user gets them without standing up an external OTLP backend first. It is
	// separate from the CORNUS_OTEL gate, which controls cornus's telemetry
	// about *itself*.
	//
	// Enabling it here is necessary but not sufficient: the store only exists in
	// a binary built with the `imbh` tag. A build without it reports the store as
	// not compiled in rather than silently recording nothing.
	ObsEnabled bool
	// ObsDir holds the observability database. Empty derives ObservabilityDir()
	// under DataDir. Unlike the block cache this has a default, because the store
	// is server-owned state of the same kind as the registry CAS rather than an
	// operator-placed cache.
	ObsDir string
	// ObsRetention drops recorded telemetry older than this. Zero keeps
	// everything, bounded only by ObsMaxBytes.
	ObsRetention time.Duration
	// ObsMaxBytes bounds the store's on-disk size. Zero is unbounded.
	ObsMaxBytes int64
	// ObsRecordLogs makes the server record every managed workload's
	// stdout/stderr into the store, so `compose logs` can answer after the
	// container is gone and an uninstrumented app gets searchable logs for free.
	// It costs one follow-stream per workload, which is the reason it is a
	// separate switch from ObsEnabled rather than implied by it.
	ObsRecordLogs bool
	// ObsRecordMetrics makes the server sample every managed workload's CPU,
	// memory, network, and disk on a timer and record the readings, so resource
	// usage has a history instead of only a live view.
	//
	// A separate switch from ObsRecordLogs because the costs differ in kind: logs
	// hold a standing stream per workload, metrics make a bounded call per replica
	// per interval. It is also NOT gated on ObsEnabled — a server configured only
	// to forward upstream has as much use for workload metrics as one keeping them.
	ObsRecordMetrics bool
	// ObsMetricsInterval is how often each replica is sampled. Zero means the
	// built-in default. Shorter buys resolution at a proportional cost in stored
	// datapoints and backend calls.
	ObsMetricsInterval time.Duration

	// ObsExportEndpoint forwards everything the server receives on to an upstream
	// OTLP/HTTP backend, in addition to storing it. It is what lets cornus be an
	// aggregation point rather than a destination: workloads export once, to
	// cornus, and the backend credential and route live on the server instead of
	// in every deploy spec.
	//
	// It is INDEPENDENT of ObsEnabled. With a store, cornus keeps a local copy and
	// forwards; without one, cornus is a pure telemetry gateway — which is still
	// useful, because a workload reaching cornus over the caretaker mux can then
	// land spans in a backend it has no route to itself.
	ObsExportEndpoint string
	// ObsExportHeaders are static headers added to every forwarded request, e.g.
	// the upstream's auth token.
	ObsExportHeaders map[string]string
	// ObsExportInsecure disables TLS verification toward the upstream.
	ObsExportInsecure bool
}

// Observability store defaults. Retention is a week because that is the window
// in which someone actually investigates an incident; the size cap is what stops
// a chatty workload from filling the data volume before retention ever applies.
const (
	// DefaultObsMetricsInterval matches the Prometheus scrape convention, so a
	// recorded series has the resolution a rate() over a 1m or 5m window expects.
	DefaultObsMetricsInterval = 15 * time.Second
	DefaultObsRetention       = 7 * 24 * time.Hour
	DefaultObsMaxBytes        = 512 << 20 // 512 MiB
)

// StorageRef returns the storage reference passed to storage.Open: the explicit
// StorageURL, or the filesystem DataDir when unset.
func (c Config) StorageRef() string {
	if c.StorageURL != "" {
		return c.StorageURL
	}
	return c.DataDir
}

// DefaultDataDir resolves the default data directory, honoring CORNUS_DATA
// then XDG_DATA_HOME, then ~/.local/share, then a working-directory fallback.
func DefaultDataDir() string {
	if d := os.Getenv("CORNUS_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "cornus")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "cornus")
	}
	return filepath.Join(".", "cornus-data")
}

// BlobsDir is the content-addressable blob store root.
func (c Config) BlobsDir() string { return filepath.Join(c.DataDir, "blobs") }

// ReposDir holds per-repository tag and manifest indexes.
func (c Config) ReposDir() string { return filepath.Join(c.DataDir, "repos") }

// UploadsDir holds in-progress (chunked) blob uploads.
func (c Config) UploadsDir() string { return filepath.Join(c.DataDir, "uploads") }

// CacheDir holds the BuildKit build cache and worker state.
func (c Config) CacheDir() string { return filepath.Join(c.DataDir, "buildkit") }

// MountsDir holds per-session kernel-9p mountpoints for deploy-attach
// client-local bind mounts (see pkg/deploywire).
func (c Config) MountsDir() string { return filepath.Join(c.DataDir, "mounts") }

// ObservabilityDir is the built-in observability store's database directory: the
// explicit ObsDir, or a default under DataDir.
//
// It is server-owned state that never leaves the server process, so — unlike the
// mountpoints under MountsDir — it is never handed to a container runtime and
// needs no host-path translation when the server itself runs in a container (see
// pkg/hostenv).
func (c Config) ObservabilityDir() string {
	if c.ObsDir != "" {
		return c.ObsDir
	}
	return filepath.Join(c.DataDir, "observability")
}

// EnsureDirs creates all data directories if they do not already exist. The block
// cache directory (FileCacheDir) is created only when the cache is enabled — it is
// an explicit, operator-provided path (often a separate volume), not derived from
// DataDir.
func (c Config) EnsureDirs() error {
	dirs := []string{c.DataDir, c.BlobsDir(), c.ReposDir(), c.UploadsDir(), c.CacheDir(), c.MountsDir()}
	if c.FileCacheEnabled && c.FileCacheDir != "" {
		dirs = append(dirs, c.FileCacheDir)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
