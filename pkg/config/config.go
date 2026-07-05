// Package config holds cornus's runtime configuration and the on-disk
// layout shared by the registry, build engine, and deploy engine.
package config

import (
	"os"
	"path/filepath"
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
}

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
