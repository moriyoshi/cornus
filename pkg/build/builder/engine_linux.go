//go:build linux

package builder

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/containerd/containerd/snapshots/overlay/overlayutils"
	dockerconfig "github.com/docker/cli/cli/config"
	units "github.com/docker/go-units"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/remotecache"
	inlineremotecache "github.com/moby/buildkit/cache/remotecache/inline"
	localremotecache "github.com/moby/buildkit/cache/remotecache/local"
	registryremotecache "github.com/moby/buildkit/cache/remotecache/registry"
	"github.com/moby/buildkit/client"
	bkconfig "github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/control"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/frontend"
	dockerfile "github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/frontend/gateway/forwarder"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/bboltcachestorage"
	"github.com/moby/buildkit/util/db/boltutil"
	"github.com/moby/buildkit/util/disk"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/resolver"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/runc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Engine is an in-process BuildKit build engine.
type Engine struct {
	client *client.Client
	server *grpc.Server
	lis    *bufconn.Listener
	// ctrl owns the BuildKit control plane and, transitively, the bbolt handles
	// opened in newController (cache.db, history.db) and the worker's snapshotter
	// metadata db. ctrl.Close() closes all of them; without it those handles (and
	// their exclusive file locks) leak, and the next engine construction on the
	// same data dir blocks forever in bboltcachestorage.NewStore (Timeout 0).
	ctrl     *control.Controller
	lock     *os.File // exclusive advisory lock on the data dir
	cacheMgr cache.Manager
	root     string // data dir (holds the managed type=local cache under localcache/)
	// remotes holds the lazy remote snapshotters so Close can release their
	// bookkeeping (bounds committed/views growth — see remotesnapshotter Leak note).
	// Stays empty on the containerd worker, whose snapshotter lives in the
	// containerd daemon; Close's releaseAll is a no-op then.
	remotes *snapshotterRegistry
	// workerKind is the resolved worker backend (WorkerRunc or WorkerContainerd).
	// The lazy build-context path is rejected per build when it is
	// WorkerContainerd (see Build / Solve).
	workerKind string
}

// New constructs the in-process BuildKit controller, serves it on an in-memory
// gRPC listener, and connects a BuildKit client to it over that loopback.
func New(cfg Config) (*Engine, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("builder: empty root")
	}
	cfg, err := resolveWorkerConfig(cfg)
	if err != nil {
		return nil, err
	}
	// The lazy build-context path (oci-layout + the in-process "stargz" remote
	// snapshotter) only exists on the runc worker's snapshotter plumbing. Fail
	// construction before dialing containerd so the conflict surfaces early.
	if cfg.Worker == WorkerContainerd && lazyBuildEnabled() {
		return nil, fmt.Errorf("builder: %s is not supported with the containerd build worker (%s=%s)", lazyBuildEnv, buildWorkerEnv, WorkerContainerd)
	}
	if err := os.MkdirAll(cfg.Root, 0o711); err != nil {
		return nil, fmt.Errorf("builder: create root: %w", err)
	}

	// Guard the data dir before touching BuildKit's boltdb (which would hang on a
	// held lock). Released if construction fails.
	lock, err := lockDataDir(cfg.Root)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()

	// Reap stale lazy/9P/ssh backing dirs a previously crashed engine left behind
	// (see sweep_linux.go). Best-effort and gated on mtime staleness so a
	// concurrently-running peer's live dirs are never touched.
	sweepStaleTempDirs(os.TempDir())

	reg := &snapshotterRegistry{}
	ctrl, cacheMgr, err := newController(context.Background(), cfg, reg)
	if err != nil {
		return nil, err
	}
	// ctrl owns the just-opened bbolt handles; close it on any later failure so
	// their exclusive file locks are released (else the next construction hangs).
	defer func() {
		if !ok {
			_ = ctrl.Close()
		}
	}()

	server := grpc.NewServer()
	ctrl.Register(server)

	lis := bufconn.Listen(16 << 20)
	go func() { _ = server.Serve(lis) }()

	c, err := client.New(context.Background(), "",
		client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		client.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		server.Stop()
		return nil, fmt.Errorf("builder: connect loopback client: %w", err)
	}
	ok = true
	return &Engine{client: c, server: server, lis: lis, ctrl: ctrl, lock: lock, cacheMgr: cacheMgr, root: cfg.Root, remotes: reg, workerKind: cfg.Worker}, nil
}

// PruneLocalCache removes stale managed type=local build caches under the
// engine's data Root (see the package-level PruneLocalCache).
func (e *Engine) PruneLocalCache(olderThan time.Duration) (freed int, err error) {
	return PruneLocalCache(e.root, olderThan)
}

// Close releases the engine's client and in-process server.
func (e *Engine) Close() error {
	if e.client != nil {
		_ = e.client.Close()
	}
	if e.server != nil {
		e.server.GracefulStop()
	}
	if e.ctrl != nil {
		// Closes cache.db, history.db, and the worker's snapshotter metadata db,
		// releasing their exclusive file locks. Do it after the server has stopped
		// so no in-flight request touches the stores mid-close.
		_ = e.ctrl.Close()
	}
	if e.lock != nil {
		_ = e.lock.Close() // release the data-dir lock
	}
	if e.remotes != nil {
		// Free any lazy committed/view entries an aborted build never Removed.
		e.remotes.releaseAll()
	}
	return nil
}

// snapshotterFactory selects overlayfs when the kernel supports it (it does on
// modern kernels) and falls back to the native snapshotter otherwise.
func snapshotterFactory(root string, reg *snapshotterRegistry) runc.SnapshotterFactory {
	var f runc.SnapshotterFactory
	if err := overlayutils.Supported(root); err == nil {
		f = runc.SnapshotterFactory{
			Name: "overlayfs",
			New: func(root string) (ctdsnapshot.Snapshotter, error) {
				return overlay.NewSnapshotter(root, overlay.AsynchronousRemove)
			},
		}
	} else {
		f = runc.SnapshotterFactory{Name: "native", New: native.NewSnapshotter}
	}
	// Optionally: (1) route through the "stargz"-named remote snapshotter for lazy
	// bind mounts (CORNUS_LAZY_BUILD), and (2) trace snapshotter calls
	// (CORNUS_SNAPSHOTTER_TRACE). Both inert by default. Trace is outermost so it
	// logs the remote snapshotter's calls too.
	return traceSnapshotterFactory(lazySnapshotterFactory(f, reg))
}

// newController assembles the BuildKit control.Controller in-process. It mirrors
// buildkitd's newController, dropping tracing/OTEL and the remote cache backends
// cornus does not use (gha/s3/azblob), keeping inline + local + registry.
func newController(ctx context.Context, cfg Config, reg *snapshotterRegistry) (*control.Controller, cache.Manager, error) {
	sm, err := session.NewManager()
	if err != nil {
		return nil, nil, err
	}

	// Assemble the worker backend: the self-contained runc worker by default, or
	// the containerd-delegating worker (engine_containerd_linux.go). Everything
	// downstream of base.NewWorker is worker-agnostic. The lazy snapshotter
	// registry only participates on the runc path.
	var wopt base.WorkerOpt
	switch cfg.Worker {
	case WorkerContainerd:
		wopt, err = containerdWorkerOpt(cfg)
	default:
		wopt, err = runcWorkerOpt(cfg, reg)
	}
	if err != nil {
		return nil, nil, err
	}
	// Neither runc.NewWorkerOpt nor the containerd worker sets GCPolicy, which
	// would let the snapshot/cache state grow without bound. Bound it the way
	// buildkitd does (see defaultGCPolicy).
	wopt.GCPolicy = defaultGCPolicy(cfg.Root)
	w, err := base.NewWorker(ctx, wopt)
	if err != nil {
		return nil, nil, fmt.Errorf("builder: new worker: %w", err)
	}
	wc := &worker.Controller{}
	if err := wc.Add(w); err != nil {
		_ = w.Close()
		return nil, nil, err
	}

	gw, err := gateway.NewGatewayFrontend(wc.Infos(), nil)
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	frontends := map[string]frontend.Frontend{
		"dockerfile.v0": forwarder.NewGatewayForwarder(wc.Infos(), dockerfile.Build),
		"gateway.v0":    gw,
	}

	cacheStorage, err := bboltcachestorage.NewStore(filepath.Join(cfg.Root, "cache.db"))
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	historyDB, err := boltutil.Open(filepath.Join(cfg.Root, "history.db"), 0o600, nil)
	if err != nil {
		_ = cacheStorage.Close()
		_ = w.Close()
		return nil, nil, err
	}

	// Registry cache resolver: default hosts (localhost defaults to plain HTTP;
	// non-local insecure registries via the `registry.insecure=true` cache attr),
	// with auth flowing through the session.
	cacheHosts := resolver.NewRegistryConfig(nil)

	ctrl, err := control.NewController(control.Opt{
		SessionManager:   sm,
		WorkerController: wc,
		Frontends:        frontends,
		CacheManager:     solver.NewCacheManager(ctx, "local", cacheStorage, worker.NewCacheResultStorage(wc)),
		CacheStore:       cacheStorage,
		HistoryDB:        historyDB,
		LeaseManager:     w.LeaseManager(),
		ContentStore:     w.ContentStore(),
		GarbageCollect:   w.GarbageCollect,
		ResolveCacheExporterFuncs: map[string]remotecache.ResolveCacheExporterFunc{
			"inline":   inlineremotecache.ResolveCacheExporterFunc(),
			"local":    localremotecache.ResolveCacheExporterFunc(sm),
			"registry": registryremotecache.ResolveCacheExporterFunc(sm, cacheHosts),
		},
		ResolveCacheImporterFuncs: map[string]remotecache.ResolveCacheImporterFunc{
			"local": localremotecache.ResolveCacheImporterFunc(sm),
			// The registry importer also consumes inline cache (embedded in an
			// image's config) when cache-from points at an image ref.
			"registry": registryremotecache.ResolveCacheImporterFunc(sm, w.ContentStore(), cacheHosts),
		},
	})
	if err != nil {
		_ = historyDB.Close()
		_ = cacheStorage.Close()
		_ = w.Close()
		return nil, nil, err
	}
	// The worker's cache manager is exposed so the lazy-context path can pre-seed
	// a layer's contenthash before Solve (skipping the RUN cache-key scan).
	return ctrl, w.CacheManager(), nil
}

// runcWorkerOpt builds the default self-contained worker: BuildKit's runc
// executor with an in-process snapshotter under cfg.Root. reg collects any
// lazy remote snapshotters so Close can release their bookkeeping.
func runcWorkerOpt(cfg Config, reg *snapshotterRegistry) (base.WorkerOpt, error) {
	processMode := oci.ProcessSandbox
	if cfg.Rootless {
		// Inside an unprivileged container we cannot unshare a fresh pidns.
		processMode = oci.NoProcessSandbox
	}

	wopt, err := runc.NewWorkerOpt(
		cfg.Root,
		snapshotterFactory(cfg.Root, reg),
		cfg.Rootless,
		processMode,
		nil, // labels
		nil, // idmap
		netproviders.Opt{Mode: "auto"},
		nil, // dns
		"",  // runc binary (auto-detect on PATH)
		"",  // apparmor profile
		false,
		nil, // parallelism semaphore
		"",  // trace socket
		"",  // default cgroup parent
	)
	if err != nil {
		return base.WorkerOpt{}, fmt.Errorf("builder: worker opt: %w", err)
	}
	return wopt, nil
}

// buildCacheKeepBytesEnv overrides the build cache's max-used-space budget. Its
// value is a byte count, accepting either a plain integer ("2147483648") or a
// human-readable size ("2GB", "512m"); see parseKeepBytes.
const buildCacheKeepBytesEnv = "CORNUS_BUILD_CACHE_KEEP_BYTES"

// parseKeepBytes parses the CORNUS_BUILD_CACHE_KEEP_BYTES value into a byte
// count. ok is false when the value is empty or unparsable (or non-positive), so
// callers fall back to buildkit's disk-derived default.
func parseKeepBytes(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	// units.RAMInBytes accepts both plain integers and suffixed sizes (KB/MB/GB…).
	n, err := units.RAMInBytes(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// defaultGCPolicy builds a bounded GC policy for the in-process worker, mirroring
// buildkitd's getGCPolicy/DefaultGCPolicy: it starts from buildkit's
// DetectDefaultGCCap sizing for the data dir's filesystem (reserved space up to
// 10GB, keep at least 20% free, cap used space at min(80%, 100GB)) and lets
// CORNUS_BUILD_CACHE_KEEP_BYTES override the max-used-space budget — the knob
// that actually caps cache growth (see cache.calculateKeepBytes, which seeds the
// keep target from MaxUsedSpace). runc.NewWorkerOpt leaves GCPolicy nil, so
// without this the snapshot/cache dir grows without limit.
func defaultGCPolicy(root string) []client.PruneInfo {
	dstat, _ := disk.GetDiskStat(root)
	cfg := bkconfig.DetectDefaultGCCap(dstat)
	if n, ok := parseKeepBytes(os.Getenv(buildCacheKeepBytesEnv)); ok {
		cfg.GCMaxUsedSpace = bkconfig.DiskSpace{Bytes: n}
	}
	policies := bkconfig.DefaultGCPolicy(cfg, dstat)
	out := make([]client.PruneInfo, 0, len(policies))
	for _, rule := range policies {
		out = append(out, client.PruneInfo{
			Filter:        rule.Filters,
			All:           rule.All,
			KeepDuration:  rule.KeepDuration.Duration,
			ReservedSpace: rule.ReservedSpace.AsBytes(dstat),
			MaxUsedSpace:  rule.MaxUsedSpace.AsBytes(dstat),
			MinFreeSpace:  rule.MinFreeSpace.AsBytes(dstat),
		})
	}
	return out
}

// defaultAuthProvider returns a session attachable that reads registry
// credentials from the Docker config, so pushes can authenticate.
func defaultAuthProvider() session.Attachable {
	return authprovider.NewDockerAuthProvider(dockerconfig.LoadDefaultConfigFile(os.Stderr), nil)
}

// buildSecretsStore builds a secret store serving the request's file-backed
// secret mounts.
func buildSecretsStore(srcSecrets []SecretSource) (secrets.SecretStore, error) {
	srcs := make([]secretsprovider.Source, 0, len(srcSecrets))
	for _, s := range srcSecrets {
		srcs = append(srcs, secretsprovider.Source{ID: s.ID, FilePath: s.Path})
	}
	return secretsprovider.NewStore(srcs)
}
