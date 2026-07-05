package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cornus/pkg/logging"
	"cornus/pkg/observability"
	"cornus/pkg/obsstore"
	"cornus/pkg/server"
	"cornus/pkg/storage"
)

// ServeCmd runs the unified cornus server.
type ServeCmd struct {
	Addr       string `kong:"name='addr',default=':5000',help='HTTP listen address for /v2/* and /.cornus/v1/*. Defaults to ALL interfaces, because a containerized caretaker (client-local 9P mounts, client-side egress, credential delivery, workload telemetry) dials BACK to the server and cannot reach a loopback-only bind. Note the API is unauthenticated unless you configure auth, and it can build, deploy, and exec — pass --addr 127.0.0.1:5000 (or CORNUS_ADDR=127.0.0.1:5000) to restrict it to this machine when you do not need workloads to reach it.',env='CORNUS_ADDR'"`
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

	// The built-in observability store. Distinct from --otel above, which is
	// cornus instrumenting ITSELF; these record a WORKLOAD's telemetry.
	// Tri-state on purpose: nil means "not specified", which resolves to
	// whether the store is compiled into this binary (see Run). A plain bool
	// could not tell an unset flag from an explicit --no-obs, and the released
	// binaries all carry the store, so "unset" has to mean ON there and OFF in a
	// stub dev build.
	Obs           *bool         `kong:"name='obs',negatable,help='Enable the built-in observability store: record deployed workloads\\' logs (and receive their OTLP traces/metrics) into a local database queryable with cornus observe. Defaults to on when the store is compiled into this binary (every released binary and image), off otherwise.',env='CORNUS_OBS'"`
	ObsDir        string        `kong:"name='obs-dir',help='Directory for the observability database (default: <data-dir>/observability).',env='CORNUS_OBS_DIR'"`
	ObsRetention  time.Duration `kong:"name='obs-retention',default='168h',help='Drop recorded telemetry older than this (0 = keep until the size cap applies). Rounded up to whole days.',env='CORNUS_OBS_RETENTION'"`
	ObsMaxBytes   int64         `kong:"name='obs-max-bytes',default='536870912',help='On-disk size cap for the observability store in bytes (0 = unbounded).',env='CORNUS_OBS_MAX_BYTES'"`
	ObsRecordLogs bool          `kong:"name='obs-record-logs',default='true',negatable,help='Record every managed workload\\'s stdout/stderr into the store, so logs survive the container and are searchable. Costs one follow-stream per workload.',env='CORNUS_OBS_RECORD_LOGS'"`

	ObsRecordMetrics   bool          `kong:"name='obs-record-metrics',default='true',negatable,help='Sample every managed workload\\'s CPU, memory, network and disk usage on a timer and record it, so resource usage has a history rather than only a live view. Works with --obs, with --obs-export-endpoint, or both.',env='CORNUS_OBS_RECORD_METRICS'"`
	ObsMetricsInterval time.Duration `kong:"name='obs-metrics-interval',default='15s',help='How often each workload replica is sampled for metrics. Shorter buys resolution at a proportional cost in stored datapoints and backend calls.',env='CORNUS_OBS_METRICS_INTERVAL'"`

	// Re-export. Independent of --obs: with a store cornus keeps a copy AND
	// forwards; without one it is a pure telemetry gateway, which is useful
	// because a workload reaching cornus over the caretaker mux can then land
	// telemetry in a backend it has no route to itself.
	ObsExportEndpoint string   `kong:"name='obs-export-endpoint',help='Forward received workload telemetry on to this upstream OTLP/HTTP backend, in addition to storing it. Works with or without --obs.',env='CORNUS_OBS_EXPORT_ENDPOINT'"`
	ObsExportHeader   []string `kong:"name='obs-export-header',sep='none',help='KEY=VALUE header added to every forwarded export (e.g. the upstream auth token). Repeatable.',env='CORNUS_OBS_EXPORT_HEADERS'"`
	ObsExportInsecure bool     `kong:"name='obs-export-insecure',help='Skip TLS verification toward the re-export upstream.',env='CORNUS_OBS_EXPORT_INSECURE'"`
}

// parseHeaderPairs turns repeated KEY=VALUE flags into a header map. A pair
// without '=' is rejected rather than ignored: a silently dropped auth header
// fails as an upstream 401 far from its cause.
func parseHeaderPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid header %q: want KEY=VALUE", kv)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// resolveDataRelativeDir resolves a configured directory against the data dir:
// empty stays empty (the feature is disabled or unset), an absolute path is used
// verbatim (e.g. a dedicated volume mount), and a relative path roots at the data
// dir so it lands under it by default. Shared by the block cache and the
// observability store, which want identical placement semantics.
func resolveDataRelativeDir(dataDir, dir string) string {
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(dataDir, dir)
}

// resolveObsEnabled decides whether the built-in observability store is on.
//
// An explicit --obs / --no-obs (flag non-nil) always wins, including --obs on a
// build that lacks the store: that caller gets openObsStore's loud "not compiled
// in" warning, which is the useful answer. Unspecified follows the BUILD — on
// wherever the store is linked in, which is every released binary and the
// published image, and off in a plain `go build` binary so a dev build does not
// nag about a feature nobody asked for.
func resolveObsEnabled(flag *bool, compiled bool) bool {
	if flag != nil {
		return *flag
	}
	return compiled
}

// loopbackOnlyAddr reports whether a listen address binds the loopback
// interface alone, so only this machine can reach the server.
//
// An empty host (":5000") or a wildcard ("0.0.0.0", "[::]") is every interface.
// Anything unparseable answers false: this only drives an advisory startup
// message, and a wrong "you are safe" reads far worse than a missing hint.
func loopbackOnlyAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// logListenScope explains the reach of the address just bound, so the loopback
// default is discoverable from the server's own output rather than only from
// --help.
//
// Containerized changes the advice, not the fact: a container's loopback is the
// container's own, so a published port (docker run -p) resolves to nothing and
// the failure is a silent connection refused from outside. That case warns; a
// host-run server merely mentions the flag once.
func logListenScope(ctx context.Context, log *slog.Logger, addr string, containerized bool) {
	if !loopbackOnlyAddr(addr) {
		return
	}
	if containerized {
		log.WarnContext(ctx, "listening on loopback only, but this cornus runs in a container: a published port (docker run -p / a Service) cannot reach it",
			"remedy", "pass --addr :5000 (CORNUS_ADDR=:5000) to bind every interface in the container")
		return
	}
	log.InfoContext(ctx, "listening on loopback only; clients on other hosts or in containers need --addr :5000 (CORNUS_ADDR=:5000)")
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
	// Set on the CONFIG, not on srv after New: a client-cert CA turns auth on, and
	// the authenticator is built inside New. Assigning it afterwards (as the two
	// serving certs below still do, since only Run reads those) left the auth
	// layer to re-read the env var and disagree with the flag.
	cfg.TLSClientCAFile = c.TLSClientCA
	cfg.FileCacheEnabled = c.FileCache
	cfg.FileCacheChunkSize = c.FileCacheChunkSize
	cfg.FileCacheMaxBytes = c.FileCacheMaxBytes
	// A relative --file-cache-dir roots at the data dir; an absolute path is used
	// verbatim (e.g. a dedicated volume mount).
	cfg.FileCacheDir = resolveDataRelativeDir(cfg.DataDir, c.FileCacheDir)
	cfg.ObsEnabled = resolveObsEnabled(c.Obs, obsstore.Compiled())
	// A relative --obs-dir roots at the data dir, matching --file-cache-dir.
	cfg.ObsDir = resolveDataRelativeDir(cfg.DataDir, c.ObsDir)
	cfg.ObsRetention = c.ObsRetention
	cfg.ObsMaxBytes = c.ObsMaxBytes
	cfg.ObsRecordLogs = c.ObsRecordLogs
	cfg.ObsRecordMetrics = c.ObsRecordMetrics
	cfg.ObsMetricsInterval = c.ObsMetricsInterval
	cfg.ObsExportEndpoint = c.ObsExportEndpoint
	cfg.ObsExportInsecure = c.ObsExportInsecure
	exportHeaders, err := parseHeaderPairs(c.ObsExportHeader)
	if err != nil {
		return fmt.Errorf("--obs-export-header: %w", err)
	}
	cfg.ObsExportHeaders = exportHeaders

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
		// Deliver cornus's own instruments to the built-in store as well. Turned
		// on by the same switch that records workload metrics, because a history
		// of what the workloads used is far less useful without what the server
		// running them used at the same moment. server.New registers the sink.
		InProcessMetrics:         cfg.ObsRecordMetrics && (cfg.ObsEnabled || cfg.ObsExportEndpoint != ""),
		InProcessMetricsInterval: cfg.ObsMetricsInterval,
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

	log := logging.FromContext(ctx)
	if observability.Enabled() {
		log.InfoContext(ctx, "observability enabled (OpenTelemetry)")
	}
	storageDesc := cfg.StorageRef()
	if pureReexport {
		storageDesc = "none (host-native re-export)"
	}
	// Announce only once the server is actually up. srv.Run binds its own
	// listener, so a bind failure (occupied port, bad address) surfaces as an
	// early Run error; srv.Ready() closes only after that bind succeeds. Select
	// on both so the banner is exact rather than time-bounded. Logging first
	// printed "cornus serving addr=:5000" immediately followed by "bind: address
	// already in use", which reads as if the server had started.
	runCh := make(chan error, 1)
	go func() { runCh <- srv.Run(ctx) }()
	select {
	case err := <-runCh:
		return err
	case <-srv.Ready():
	}
	if srv.TLSCertFile != "" && srv.TLSKeyFile != "" {
		log.InfoContext(ctx, "cornus serving", "addr", cfg.HTTPAddr, "storage", storageDesc, "tls", true)
	} else {
		log.InfoContext(ctx, "cornus serving", "addr", cfg.HTTPAddr, "storage", storageDesc)
	}
	logListenScope(ctx, log, cfg.HTTPAddr, srv.Containerized())
	return <-runCh
}
