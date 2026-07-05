package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/observability"
	"cornus/pkg/server"
	"cornus/pkg/storage"
)

// ServeCmd runs the unified cornus server.
type ServeCmd struct {
	Addr       string `kong:"name='addr',default=':5000',help='HTTP listen address for /v2/* and /.cornus/v1/*.',env='CORNUS_ADDR'"`
	Rootless   bool   `kong:"name='rootless',help='Run the build engine in rootless mode (user namespaces).',env='CORNUS_ROOTLESS'"`
	BuilderURL string `kong:"name='builder-url',help='Delegate builds to an upstream cornus builder (e.g. ws://127.0.0.1:5099) instead of building in-process. For hosts where the in-process engine cannot run: BuildKit needs mount(2)/CAP_SYS_ADMIN, so an unprivileged server fails every build.',env='CORNUS_BUILDER_URL'"`

	BuilderAuto      bool   `kong:"name='builder-auto',default='true',negatable,help='When the in-process build engine cannot run (mount(2) not permitted) and no --builder-url is set, start a privileged cornus builder container and delegate builds to it. Only engages where builds would otherwise fail outright.',env='CORNUS_BUILDER_AUTO'"`
	BuilderImage     string `kong:"name='builder-image',help='Pin a published cornus image for the auto-started builder container. Default: build a throwaway image containing this running binary, so the builder is exactly this cornus.',env='CORNUS_BUILDER_IMAGE'"`
	BuilderBaseImage string `kong:"name='builder-base-image',help='Base image for the self-built builder image (default: matches the host distribution).',env='CORNUS_BUILDER_BASE_IMAGE'"`
	Storage          string `kong:"name='storage',help='Registry persistence backend: a path, file://, mem://, or s3://bucket?region=&endpoint=&path_style=. Defaults to the data dir.',env='CORNUS_STORAGE'"`
	OTel             bool   `kong:"name='otel',help='Enable OpenTelemetry (traces/metrics/logs) via the standard OTEL_* env. Also enabled implicitly when any OTEL_* exporter/endpoint env var is set.',env='CORNUS_OTEL'"`
	TLSCert          string `kong:"name='tls-cert',help='PEM certificate file; serve HTTPS when set together with --tls-key.',env='CORNUS_TLS_CERT'"`
	TLSKey           string `kong:"name='tls-key',help='PEM private-key file; serve HTTPS when set together with --tls-cert.',env='CORNUS_TLS_KEY'"`
	TLSClientCA      string `kong:"name='tls-client-ca',help='PEM CA bundle to verify client certificates (mTLS). A verified cert CommonName becomes the caller identity; presenting a cert stays optional.',env='CORNUS_TLS_CLIENT_CA'"`

	FileCache          bool   `kong:"name='file-cache',help='Enable the server-side per-file block cache for on-demand remote file reads over 9P (immutable build contexts, and deploy mounts flagged immutable).',env='CORNUS_FILE_CACHE'"`
	FileCacheChunkSize int64  `kong:"name='file-cache-chunk-size',default='1048576',help='Block cache chunk size in bytes (default 1 MiB, matching the kernel-9p mount msize).',env='CORNUS_FILE_CACHE_CHUNK_SIZE'"`
	FileCacheMaxBytes  int64  `kong:"name='file-cache-max-bytes',help='Soft on-disk size cap for the block cache in bytes, enforced by GC pruning (0 = no cap).',env='CORNUS_FILE_CACHE_MAX_BYTES'"`
	FileCacheDir       string `kong:"name='file-cache-dir',help='Directory for the block cache backing files. REQUIRED when --file-cache is set (no default): point it at a dedicated volume so the cache does not share the data-dir volume.',env='CORNUS_FILE_CACHE_DIR'"`
}

// resolveFileCacheDir resolves the configured block-cache directory: empty stays
// empty (the cache is disabled or unset), an absolute path is used verbatim, and
// a relative path roots at the data dir so the cache lands under it by default.
func resolveFileCacheDir(dataDir, dir string) string {
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(dataDir, dir)
}

// Run starts the server and blocks until interrupted.
func (c *ServeCmd) Run(cli *CLI) error {
	cfg := cli.resolveConfig()
	cfg.HTTPAddr = c.Addr
	cfg.Rootless = c.Rootless
	cfg.BuilderURL = c.BuilderURL
	cfg.BuilderAuto = c.BuilderAuto
	cfg.BuilderImage = c.BuilderImage
	cfg.BuilderBaseImage = c.BuilderBaseImage
	cfg.StorageURL = c.Storage
	cfg.FileCacheEnabled = c.FileCache
	cfg.FileCacheChunkSize = c.FileCacheChunkSize
	cfg.FileCacheMaxBytes = c.FileCacheMaxBytes
	// A relative --file-cache-dir roots at the data dir; an absolute path is used
	// verbatim (e.g. a dedicated volume mount).
	cfg.FileCacheDir = resolveFileCacheDir(cfg.DataDir, c.FileCacheDir)

	// The block cache directory is mandatory when the cache is enabled: it has no
	// default, so operators must place it on an explicit (typically dedicated)
	// volume rather than silently sharing the data-dir volume.
	if cfg.FileCacheEnabled && cfg.FileCacheDir == "" {
		return fmt.Errorf("--file-cache requires --file-cache-dir (CORNUS_FILE_CACHE_DIR)")
	}

	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("preparing data dir: %w", err)
	}

	// In a host-native re-export mode (CORNUS_REGISTRY_SOURCE=host-native, or the
	// default on a host backend) with no explicit --storage, the local
	// Docker/containerd store is authoritative and the registry keeps NO content
	// store at all: it serves reads straight from that store and rejects writes. An
	// explicit --storage keeps a CAS as the primary layer with the re-export source
	// filling misses. Resolved fail-closed (same validation server.New performs).
	pureReexport, err := server.RegistryKeepsNoContentStore(cfg)
	if err != nil {
		return err
	}

	// The --otel flag is a convenience alias for CORNUS_OTEL; export it so the
	// env-driven observability.Enabled gate (used here and in the SDK) agrees.
	if c.OTel {
		os.Setenv("CORNUS_OTEL", "1")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Install OpenTelemetry before constructing the server so its instruments
	// bind to the real providers. A no-op unless telemetry is enabled (--otel /
	// CORNUS_OTEL / any OTEL_* exporter env); see pkg/observability.
	otelProviders, err := observability.Setup(ctx, observability.Options{
		ServiceName:    "cornus",
		ServiceVersion: version,
	})
	if err != nil {
		return fmt.Errorf("setting up observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = otelProviders.Shutdown(shutdownCtx)
	}()

	// Pure re-export keeps no content store (nil); otherwise open the configured
	// or default CAS backend.
	var st *storage.Backend
	if !pureReexport {
		st, err = storage.Open(ctx, cfg.StorageRef(), cfg.UploadsDir())
		if err != nil {
			return fmt.Errorf("opening storage: %w", err)
		}
		defer st.Close()
	}

	srv, err := server.New(cfg, st)
	if err != nil {
		return fmt.Errorf("initializing server: %w", err)
	}
	srv.Version = version // advertised via /.cornus/v1/info for the client skew check
	srv.TLSCertFile = c.TLSCert
	srv.TLSKeyFile = c.TLSKey
	srv.TLSClientCAFile = c.TLSClientCA

	log := logging.FromContext(ctx)
	if observability.Enabled() {
		log.InfoContext(ctx, "observability enabled (OpenTelemetry)")
	}
	storageDesc := cfg.StorageRef()
	if pureReexport {
		storageDesc = "none (host-native re-export)"
	}
	if srv.TLSCertFile != "" && srv.TLSKeyFile != "" {
		log.InfoContext(ctx, "cornus serving", "addr", cfg.HTTPAddr, "storage", storageDesc, "tls", true)
	} else {
		log.InfoContext(ctx, "cornus serving", "addr", cfg.HTTPAddr, "storage", storageDesc)
	}
	return srv.Run(ctx)
}
