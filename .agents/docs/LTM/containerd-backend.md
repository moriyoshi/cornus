# Containerd backend (deploy) and containerd build worker

## Summary

Cornus can target a bare containerd host — no dockerd — on both subsystems: a full
`deploy.Backend` in `pkg/deploy/containerdhost` (selected via
`CORNUS_DEPLOY_BACKEND=containerd`) and a BuildKit containerd worker in the build
engine (selected via `CORNUS_BUILD_WORKER=containerd`). Both run against the
containerd v1.7.24 client already pinned via BuildKit (no module bumps, no
containerd/v2) in the `cornus` namespace, and pairing them (same namespace,
deliberately) gives registry-round-trip-free build-then-deploy.

## Key Facts

- Backend selection: `CORNUS_DEPLOY_BACKEND=containerd`; the socket and namespace come
  from `CORNUS_CONTAINERD_ADDRESS` / `CORNUS_CONTAINERD_NAMESPACE` (default namespace
  `cornus`). Linux-only; the non-Linux stub returns `ErrUnsupported` (`New` returns
  `deploy.Backend`, so the stub needs no 19-method boilerplate).
- Networking: nerdctl-style CNI bridge. Generated conflists under
  `<DataDir>/containerd/cni/conf/`, persisted /24 allocator
  (`CORNUS_CNI_SUBNET_BASE`, default 10.4), per-instance named netns pinned under
  `/run/cornus/netns` (survives cornus restarts), portmap for `ports:` on replica 0
  only, plugins discovered from `CORNUS_CNI_BIN_DIR`/`CNI_PATH`/`/opt/cni/bin` with an
  actionable missing-plugin error.
- Inter-container DNS: nerdctl-style hosts-file sync — a per-instance file under
  `<DataDir>/containerd/hosts/` bind-mounted at `/etc/hosts` carries deploy names and
  aliases (lowest live replica IP); hostname = instance ID.
- Logging: tasks log via a binary log shim URI
  (`binary:///<cornus>?containerd-log-shim=<path>`); JSON-line records via the pure
  `logfmt` subpackage. Rename-based rotation at `startTask` only, one `.1` generation,
  16 MiB default, `CORNUS_CONTAINERD_LOG_MAX_BYTES` override.
- Restart policy: containerd's restart monitor via `runtime/restart` labels. `Stop`
  sets `explicitly-stopped=true` before killing; `Start` clears it and repairs
  netns/CNI/spec after a host reboot. A one-shot startup reconcile
  (`ensureReconciled`) repairs dead netnses without starting tasks.
- Image pulls: refs normalized via `reference.ParseDockerRef`
  (`nginx` -> `docker.io/library/nginx:latest`); custom resolver with
  `docker.NewDockerAuthorizer()` (bearer-token flow), `MatchLocalhost` -> plain HTTP,
  `CORNUS_CONTAINERD_INSECURE_REGISTRIES` for more, local-store `GetImage` fallback
  when the registry is unreachable.
- Snapshotter: `CORNUS_CONTAINERD_SNAPSHOTTER` selects the snapshotter for
  pull/unpack/create/volume-seed (needed because overlay cannot stack on an
  overlay-backed root, e.g. dind).
- Build worker: `CORNUS_BUILD_WORKER=containerd` picks BuildKit's `worker/containerd`
  branch in `newWorkerOpt`; state under `<Root>/containerd-<snapshotter>/` (coexists
  with runc's dir); tagged builds land in the host containerd image store in addition
  to the registry push. Lazy builds are rejected with a clear error.
- E2E: `ContainerdTarget` + `CapContainerd` preflight, `--target containerd`,
  `make e2e-containerd`; ServeEnv also sets `CORNUS_BUILD_WORKER=containerd`.
- Shared code promoted while building this backend: `pkg/deploy/hostpolicy` (privilege
  policy, extracted from dockerhost) and `deploy.Bridge` (the half-close stdio splicer)
  — see the deploy-backend-contract LTM doc.

## Details

### Networking (CNI bridge) and its limits

The backend generates CNI conflists per user network under
`<DataDir>/containerd/cni/conf/` and allocates a /24 per network from a persisted
allocator rooted at `CORNUS_CNI_SUBNET_BASE` (default 10.4). Each instance gets a named
netns pinned under `/run/cornus/netns`, so netnses (and therefore container IPs)
survive cornus restarts. `ports:` mappings ride the CNI portmap plugin and are
published on replica 0 only — one DNAT target per host port, the cross-backend
convention. Plugin binaries are found via `CORNUS_CNI_BIN_DIR`, then `CNI_PATH`, then
`/opt/cni/bin`; a missing plugin produces an actionable error naming the search path.

There is no embedded DNS resolver. Specs using network `Aliases` with a non-bridge
`Driver` or `DriverOpts` get a per-deploy `slog.Warn` for the unsupported knobs
(aliases themselves are supported via hosts-file sync and are NOT part of the
warning). `ForwardPort` dials the IP recorded in the `cornus.ip` label.

### Name resolution: hosts-file sync

Instead of a resolver, the backend does nerdctl-style hosts-file sync: each instance
gets a hosts file under `<DataDir>/containerd/hosts/` bind-mounted at `/etc/hosts`,
containing a managed marker block that maps every deploy name and alias on the shared
network to the lowest live replica IP, plus `hostname = instance ID`. State is derived
from labels (`cornus.netips`, `cornus.aliases`), so it is restart-safe. Updates are
in-place block rewrites — a rename would detach the live bind mount.

### Logging: binary shim + rotation

Tasks are created with log URI `binary:///<cornus>?containerd-log-shim=<path>`. A
single query key is used because containerd's `NewBinaryCmd` passes query params as
unordered `key value` argv pairs — the key doubles as the hidden kong subcommand that
re-enters cornus as the shim. Records are JSON lines written by the pure
`logfmt` subpackage; `Logs` replays/follows the file into stdcopy frames (demux, tail,
since, timestamps all handled over partial/torn writes). The same URI is stored in the
`containerd.io/restart.loguri` label, so logging survives both cornus restarts and
restart-monitor-driven task restarts.

Rotation is rename-based and happens only at `startTask` — the sole point where no
shim process holds the fd. One generation (`<name>.log.1`), 16 MiB default cap,
`CORNUS_CONTAINERD_LOG_MAX_BYTES` override. The log reader concatenates `.1` + live
for backlog/tail and resets its follow offset when the live file shrinks. Residual
limitation: within one uninterrupted run (including restart-monitor resurrections) the
file can exceed the cap.

### Restart policy, Stop/Start, and startup reconcile

Restart handling delegates to containerd's restart monitor via `runtime/restart`
labels (`NewPolicy` accepts exactly cornus's four restart values). `Stop` must set
the `explicitly-stopped=true` label BEFORE killing the task, or the monitor
resurrects the container within its reconcile tick. `Start` clears the label and, if
the pinned netns died (host reboot), repairs netns + CNI + spec — rewriting the baked
OCI spec via `typeurl` + `Container.Update` (`repairNetns`).

`ensureReconciled` runs a one-shot startup netns reconcile: an nsfs-liveness statfs
check detects dead netnses and repairs them via the same `repairNetns` path. It never
starts tasks (the restart monitor owns resurrection) and skips `restart=no` and
explicitly-stopped instances.

### Volumes

DataDir-backed bind mounts: named volumes are shared and persistent, anonymous
volumes are per-replica and reaped on `Delete`. Seeding is copy-when-empty from a
snapshot **View** + `mount.WithTempMount` + continuity `fs.CopyDir`, matching the
docker/kubernetes seed-when-empty semantics.

### Data plane

- Exec: `task.Exec`; stdin EOF triggers `CloseIO` half-close — the `deploy.Bridge`
  semantics (shared helper in `pkg/deploy/bridge.go`). Exec TTY resizes that arrive
  before process start are buffered in the session and applied at start (the initial
  window-size message always races start; unbuffered they were dropped).
- Stats: task metrics (cgroup v1+v2) mapped to Docker StatsJSON by the pure
  `sampleFromMetrics` mapper, including `memory_stats.stats` (cg1 `total_*` keys /
  cg2 verbatim `memory.stat` — docker CLI MEM is correct), `networks` (parsed from
  `/proc/<task.Pid()>/net/dev`, no setns, `lo` excluded), and
  `blkio_stats.io_service_bytes_recursive` (cg1 passthrough / cg2 `io.stat`).
- docker-cp: via `/proc/<pid>/root` with **every** path passing through
  `fs.RootPath` (symlink confinement), implemented in the pure `tarcopy` subpackage.
  Requires a running task.
- Attach is output-only (the shim owns the fifos); healthchecks are ignored with a
  warning (nothing consumes them on this backend).

### Testability seams

`clientAPI` takes a structured `CreateContainer` (stock `NewContainerOpts`
dereference a concrete `*ctd.Client` and are unfakeable), and `networkManager`
abstracts CNI (netns creation needs root). Orchestration tests run against in-memory
fakes with interface-embedding panics for unexpected calls.

### Build worker

`CORNUS_BUILD_WORKER=containerd` selects BuildKit's `worker/containerd` branch in
`newWorkerOpt` (`pkg/build/builder/engine_containerd_linux.go`). Worker state lives
under `<Root>/containerd-<snapshotter>/` and coexists with the runc worker's dir; GC
policy and `engine.lock` are unchanged (bolt files stay under Root). The worker's
ImageStore means tagged builds land in the host containerd's image store in addition
to the registry push. Lazy builds are rejected (construction-time AND per-build) —
the stargz-named snapshotter wrapper is runc-factory plumbing. A pre-dial socket
probe makes a dead socket fail in ~0ms instead of the 5s dial timeout.

### Wiring

`defaultBackendFactory` gains `case "containerd"` with the same MountsDir carve-out
as dockerhost — the backend stays on the host-side 9P MountManager path, no
`MountingBackend`. The local `cornus deploy` CLI honors `CORNUS_DEPLOY_BACKEND` via
`localBackend()`.

### Bugs found on the first live containerd-in-dind runs

Four real bugs surfaced and were fixed when the containerd E2E leg first ran in dind:

1. Unnormalized short-name pulls — fixed by `reference.ParseDockerRef` at the single
   pull choke point.
2. The custom resolver had NO Authorizer (`ConfigureDefaultRegistries` does not attach
   one), so anonymous public-registry pulls died with a bare 401 — fixed with
   `docker.NewDockerAuthorizer()` (bearer-token flow).
3. The overlay snapshotter cannot stack on an overlay-backed root — added the
   `CORNUS_CONTAINERD_SNAPSHOTTER` knob (threaded through
   pull/unpack/create/volume-seed) plus /proc/mounts-based auto-detection in the E2E
   runner entrypoint (busybox `stat` reports UNKNOWN for overlayfs).
4. Exec TTY resizes arriving before process start were dropped — now buffered in the
   session and applied at start.

After the fixes, deploy/lifecycle/exec/compose all pass on containerd-in-dind.
`e2e/scenarios/lifecycle-restart.star` validates the restart-monitor semantics live
(boot count via a bind-mounted log; PID 1 is `sh` with a TERM trap): resurrection
after `kill 1`, and an explicit stop sticking past a monitor reconcile interval.

## Files

- `/home/moriyoshi/src/cornus/pkg/deploy/containerdhost/` — the backend:
  `backend_linux.go`, `client_linux.go` (clientAPI seam), `network_linux.go` (CNI +
  networkManager), `hosts_linux.go` (hosts-file sync), `spec_linux.go` (OCI spec +
  restart labels), `lifecycle_linux.go`, `reconcile_linux.go` (`ensureReconciled`),
  `logs_linux.go`, `stats_linux.go` (`sampleFromMetrics`), `exec_linux.go`,
  `copy_linux.go`, `image_linux.go` (pull normalization/resolver/authorizer),
  `volumes_linux.go`, `backend_other.go` (stub).
- `/home/moriyoshi/src/cornus/pkg/deploy/containerdhost/logfmt/` — JSON-line log
  record encoding (pure).
- `/home/moriyoshi/src/cornus/pkg/deploy/containerdhost/tarcopy/` — docker-cp tar
  semantics with `fs.RootPath` confinement (pure).
- `/home/moriyoshi/src/cornus/pkg/build/builder/engine_containerd_linux.go` — the
  containerd build worker branch.
- `/home/moriyoshi/src/cornus/pkg/deploy/hostpolicy/policy.go` and
  `/home/moriyoshi/src/cornus/pkg/deploy/bridge.go` — shared code promoted during
  this work.
- `/home/moriyoshi/src/cornus/e2e/scenarios/lifecycle-restart.star` — restart-monitor
  live validation.

## Test Coverage

- Unit (rootless, on fakes): logfmt (round-trip, partial chunks, torn tail,
  tail/since); tarcopy (docker-cp semantics, symlink confinement,
  NoOverwriteDirNonDir); CNI (allocator persistence/reuse, conflist goldens, plugin
  discovery/probe errors); spec translation (env sort, resources, netns, restart
  labels including invalid policy); lifecycle orchestration (replicas, publish-once,
  idempotent recreate, policy-before-daemon, network reap including
  shared-network keep, stop/start labels, status mapping); exec (process spec,
  registry errors); ForwardPort echo over `net.Pipe`; Logs (demux, tail, since,
  timestamps over partial runs, follow); stats mapper; hosts sync; reconcile; build
  worker (env resolution, lazy rejection, dead-socket fast-fail).
- Root+daemon-gated: `TestBuildAndPushContainerdWorker` (skips unprivileged; asserts
  registry pull-back AND `ImageService().Get` sees the tag).
- Live (containerd-in-dind): the containerd E2E leg (deploy/lifecycle/exec/compose +
  `lifecycle-restart.star`) is green; containerd is in the CI e2e matrix.

## Pitfalls

- `Stop` MUST set `explicitly-stopped=true` before killing the task, or the restart
  monitor resurrects the container within its reconcile tick.
- containerd's `NewBinaryCmd` passes log-URI query params as unordered `key value`
  argv pairs — keep the shim URI to a single query key (it doubles as the hidden kong
  subcommand).
- Hosts-file updates must be in-place block rewrites; a rename detaches the live
  `/etc/hosts` bind mount.
- Log rotation can only happen at `startTask` (no shim holds the fd then); a single
  uninterrupted run can exceed the size cap.
- `ConfigureDefaultRegistries` does NOT attach an Authorizer — a hand-built resolver
  needs `docker.NewDockerAuthorizer()` or anonymous Docker Hub pulls 401.
- Short image names must be normalized (`reference.ParseDockerRef`) before hitting
  the containerd pull path.
- Overlay snapshotter over an overlay-backed root (dind) fails; set
  `CORNUS_CONTAINERD_SNAPSHOTTER` (the E2E runner auto-detects via /proc/mounts —
  busybox `stat -f` cannot identify overlayfs).
- Exec TTY resize always races process start; buffer pre-start resizes or the
  initial window size is lost.
- docker-cp needs a running task (`/proc/<pid>/root`); attach is output-only;
  healthchecks are ignored (warned).
- Stock `NewContainerOpts` are unfakeable (concrete `*ctd.Client`); go through the
  `clientAPI` seam in tests.
