# Deploy Backends Synthesis

## Summary

Cornus has five `deploy.Backend` implementations: dockerhost, containerdhost, barehost,
kubernetes, and incushost. One documented contract on `pkg/deploy/deploy.go` binds their
lifecycle, stream, volume, port, and error behavior while leaving runtime-specific state and
capabilities explicit. A cross-backend audit established that contract and extracted shared
sentinels and helpers (`ErrNotFound`, `ParseSince`, `Bridge`, `hostpolicy`). Barehost and
containerdhost additionally share daemon-agnostic Linux machinery in `hostrun`. Read this first
when touching any backend, then drill into the per-backend source documents.

## Included Documents

| Document | Focus |
|----------|-------|
| [deploy-backend-contract.md](./deploy-backend-contract.md) | The cross-backend contract, the audit's divergences and fixes, dockerhost-specific bugs (replica ports, `rm -v`) |
| [containerd-backend.md](./containerd-backend.md) | containerdhost internals: CNI, hosts-file sync, log shim, restart monitor, build worker |
| [kubernetes-backend.md](./kubernetes-backend.md) | k8s backend: object mapping, annotation lifecycle, PVCs, conflict retry, exec limits |
| See also: [kubernetes-deploy-synthesis.md](./kubernetes-deploy-synthesis.md) | DeploySpec -> k8s object shapes, netdriver fabrics, sidecar injection (the k8s deep dive) |
| [barehost-backend.md](./barehost-backend.md) | Daemonless OCI lifecycle, recovery, supervision, CNI, companions, and Stats |
| [hostrun-shared-runtime.md](./hostrun-shared-runtime.md) | Shared OCI spec, CNI, hosts, volume, netns, and Stats machinery |
| [incus-backend.md](./incus-backend.md) | Incus application-container mapping, lifecycle, data plane, and E2E target |
| [in-container-server-mode.md](./in-container-server-mode.md) | Host-path translation, preflight, Docker topology, and containerd constraints |
| See also: [port-forwarding.md](./port-forwarding.md) | `ForwardPort` and port-forward plumbing across backends |

## Stable Knowledge

### The shared contract (`pkg/deploy/deploy.go`)

- `deploy.ErrNotFound`: Stop/Start/Restart on a missing name MUST wrap it on every
  backend (never nil, never a raw backend error); `handleDeployAction` maps it to 404
  via `errors.Is`. `Delete` stays delete-if-exists (no error for missing).
- `deploy.ParseSince` (`pkg/deploy/since.go`): the one `--since` grammar (docker
  `GetTimestamp`: unix[.nanos] / RFC3339 / durations-ago; `"0"` = epoch). Never
  hand-parse; garbage input must error, not silently return all logs.
- Non-TTY Logs, exec, and attach output MUST be stdcopy-framed on every backend —
  clients demux unconditionally. kubernetes was the violator (fixed via `muxWriters`).
- Entrypoint/Command semantics are docker semantics everywhere: `spec.Command` is args
  to the image ENTRYPOINT; only `spec.Entrypoint` overrides the entrypoint. On k8s
  this means `spec.Command` -> container `Args` always, never container `Command`.
- Host-port publishing with `replicas>1` is replica-0-only on host/runtime backends;
  Kubernetes Services are per-deployment.
- `Delete` reaps anonymous volumes on all backends (`docker rm -v` parity, promised in
  `pkg/api/deploy.go`); named volumes survive.
- State vocabulary is documented, not normalized. Only `running` and the `Running` boolean are
  portable; callers must not compare backend-specific transitional or stopped state strings.
- Fields a backend cannot honor get a per-field `slog.Warn`, never a silent drop and
  never `sh -c` emulation (containers may lack a shell). Examples: k8s
  `warnUnsupportedRestart` and exec Env/WorkingDir/User/Privileged; containerd network
  `Driver`/`DriverOpts`.
- Shared helpers include `pkg/deploy/hostpolicy` for privilege and bind-source gating,
  `deploy.Bridge` for half-close stdio splicing, and `pkg/deploy/internal/hostrun` for
  daemon-agnostic Linux runtime functions. Do not force backend-specific lifecycle into them.
- Backend selection is centralized in `localBackend()` and the server's
  `defaultBackendFactory`. Supported selectors include `containerd`, `bare`, `kubernetes`, and
  `incus`; local-command behavior that cannot realize Kubernetes remains explicitly separate.

### Per-backend identity

- **dockerhost** (`pkg/deploy/dockerhost/`): Docker Engine API over the socket;
  recreate-on-Apply model. Its notable fixed bugs: multi-replica published ports
  reused one `createBody` with the same `PortBindings` ("port already allocated" —
  now replica 0 publishes, replicas 1+ get a `PortBindings`-less copy), and
  `containerRemove` lacked `v=1` (leaked anonymous volumes).
- **containerdhost** (`pkg/deploy/containerdhost/`, Linux-only, non-Linux stub returns
  `ErrUnsupported`): bare containerd in the `cornus` namespace
  (`CORNUS_CONTAINERD_ADDRESS`/`_NAMESPACE`). Networking is nerdctl-style CNI bridge
  (persisted /24 allocator, named netns pinned under `/run/cornus/netns`, portmap on
  replica 0); DNS is hosts-file sync bind-mounted at `/etc/hosts` (no resolver);
  logging is a binary log shim URI re-entering cornus (JSON lines via `logfmt`,
  rename rotation at `startTask` only); restart policy delegates to containerd's
  restart monitor via `runtime/restart` labels, with a one-shot `ensureReconciled`
  netns repair at startup. `CORNUS_CONTAINERD_SNAPSHOTTER` matters on overlay-backed
  roots (dind). Pairs with the `CORNUS_BUILD_WORKER=containerd` build worker for
  registry-round-trip-free build-then-deploy.
- **barehost** (`pkg/deploy/barehost/`, Linux-only): daemonless OCI execution with an OCI runtime,
  Cornus-owned records, snapshot and bundle state, CNI, hosts/DNS management, and restart
  supervision. Reconcile recovers desired workloads after server or host restart. The optional
  detached `cornus bare-shim` can own runtime lifecycle through server downtime, but in-process
  supervision remains the default. Shared OCI/CNI/volume/Stats machinery comes from `hostrun`;
  barehost keeps record, recovery, logging, and supervisor policy.
- **kubernetes** (`pkg/deploy/kubernetes/`): DeploySpec -> Deployment (+ ClusterIP
  Service only when ports are published), annotation-driven lifecycle — Stop scales
  to 0 saving the count in `cornus.dev/replicas`, Start restores it, Restart stamps
  `cornus.dev/restartedAt`; Delete is one foreground-propagation Deployment delete and
  k8s GC reclaims owner-ref'd Service/PVCs. All lifecycle mutations go through
  `updateDeployment` (`retry.RetryOnConflict`) because the deployment controller
  writes concurrently. Config: in-cluster or kubeconfig; `CORNUS_K8S_NAMESPACE`,
  `CORNUS_K8S_IMAGE_PULL_POLICY`. See kubernetes-deploy-synthesis.md for object
  shapes, PVC seeding, netdriver fabrics, and sidecar injection.
- **incushost** (`pkg/deploy/incushost/`, Linux-only): Incus application containers through a
  narrow `incusConn` seam. Metadata uses `user.cornus.*`, Apply recreates instances, and proxy
  devices publish ports on replica 0. The file API, console logs, websocket exec, and instance-IP
  forwarding implement the data plane; attach remains unsupported. The client is pinned at
  `github.com/lxc/incus/v6` v6.18.0 because newer versions conflict with the current containerd
  runtime-spec dependency.

### Shared host runtime and in-container topology

- `pkg/deploy/internal/hostrun` is Linux-only and intentionally daemon-client-free. Barehost and
  containerdhost share OCI spec construction, netns checks, managed hosts files, volume seeding,
  CNI, and Docker-compatible Stats projection while retaining different lifecycle and recovery.
- When the server itself runs in a container, every path interpreted by the host runtime must be
  translated through the proven host mapping. `pkg/hostenv` separates container detection from
  path divergence, and `pkg/hostcheck` fails configurations that would silently bind empty paths.
- Dockerhost in-container mode is supported with the daemon socket and an `rshared` data bind;
  the setup wizard and `server-in-container.star` cover it. Containerd path translation and
  content-addressed log-shim staging exist, but complete operation still requires host networking,
  host-visible runtime paths, and CNI plugins inside the server image.

### Known asymmetries (by documented design)

Stopped replica counts, restart-after-stop, copy on stopped workloads, log fidelity, attach, and
restart-policy expressiveness differ by backend and are documented on the interface and source
documents. Localhost image refs are also runtime-relative: containerd may use its local store,
dockerhost requires daemon-visible content, barehost owns a separate store, Incus resolves from
its daemon host, and Kubernetes means the node. Kubernetes pods remain `Always`; backends that
cannot express a requested field warn rather than fabricating parity.

## Operational Guidance

- A behavior change to one backend is a contract question: either change all five or
  document the divergence on the `Backend` interface (as the state vocabulary is).
  The interface doc comment in `pkg/deploy/deploy.go` is the contract's home.
- Server-side stream errors: `pkg/server/deploy.go` and
  `pkg/dockerproxy/containers.go` use a lazy-header writer — 200 flushes on the
  backend's first write, so pre-output errors map to real statuses (404/501/400/500).
  Do not touch the attach/wait flush-header-early protocol (docker run depends on it).
- Per-backend fakes model their control seams: Docker Engine wire API, containerd client and
  network manager, bare runtime/records, Kubernetes fake clientset, and `incusConn`. Assert the
  lifecycle semantics each fake can represent; Kubernetes fakes do not run GC or controllers,
  containerd's local content store omits persistent labels, and an Incus fake does not prove
  daemon-side OCI tooling.
- Local gate for any Go change: `gofmt -l`, `go build ./...`, `go vet ./...`,
  `go test ./...` (or the focused `go test ./pkg/deploy/...`).
- E2E is target-specific: dockerhost uses the dind container runner, containerd uses
  `make e2e-containerd`, barehost uses the privileged bare subset, Kubernetes uses kind, and
  Incus uses a live incusd with skopeo and umoci. The Docker server-in-container scenario is the
  topology proof for host-path translation. Live runs remain essential because runtime resource
  allocation, mount propagation, controllers, and daemon-side tools are outside unit-fake scope.

## Files

- `pkg/deploy/deploy.go` — `Backend` interface + documented contract, `ErrNotFound`
- `pkg/deploy/since.go` — `ParseSince`; `pkg/deploy/bridge.go` — `Bridge`
- `pkg/deploy/hostpolicy/policy.go` — shared privilege policy
- `pkg/deploy/dockerhost/dockerhost.go` — dockerhost backend
- `pkg/deploy/containerdhost/` — containerd backend (`backend_linux.go`,
  `network_linux.go`, `hosts_linux.go`, `logs_linux.go`, `reconcile_linux.go`, ...;
  pure subpackages `logfmt/`, `tarcopy/`)
- `pkg/deploy/barehost/` - daemonless backend, persistent records, recovery, and shim
- `pkg/deploy/internal/hostrun/` - shared OCI, CNI, hosts, volumes, netns, and Stats code
- `pkg/deploy/incushost/` - Incus backend and narrow client seam
- `pkg/hostenv/` and `pkg/hostcheck/` - in-container detection, host-path mapping, and preflight
- `pkg/deploy/kubernetes/kubernetes.go` — k8s backend (`updateDeployment`,
  `muxWriters`, `warnUnsupportedRestart`)
- `pkg/api/deploy.go` — `DeploySpec` doc contract (`Replicas`, `Command`, `rm -v`
  parity); `pkg/server/deploy.go`, `pkg/dockerproxy/containers.go` — lazy-header
  stream errors; `cmd/cornus/commands.go` — `localBackend()`

## Tests

- Shared: `pkg/deploy/since_test.go`, `bridge_test.go`, `hostpolicy/policy_test.go`.
- dockerhost: `dockerhost_test.go` — port-lifecycle fake (multi-replica port bug
  regresses loudly), anonymous-volume reaping, ErrNotFound.
- containerd: fake-based orchestration/CNI/logfmt/tarcopy/stats/hosts/reconcile
  suites; root-gated `TestBuildAndPushContainerdWorker`; live
  `e2e/scenarios/lifecycle-restart.star` (restart-monitor resurrection + sticky stop)
  via `make e2e-containerd`.
- barehost: root-free record/recovery/shim/cgroup tests plus the privileged bare E2E subsets.
- incushost: fake-connection lifecycle, mapping, cp/log/exec/forward tests plus the live Incus
  target.
- in-container: hostenv/hostcheck/path-mapping unit tests and `server-in-container.star` against a
  real Docker daemon.
- kubernetes: `kubernetes_test.go` — `TestLifecycleRetriesOnConflict` (409 reactor),
  `TestLifecycleMissingDeployment` (ErrNotFound), `TestApplyEntrypoint`,
  `TestManagedResourcesOwnedByDeployment`, framing/exec tests; live kind runs of
  `deploy-shape.star`, `deploy-volumes.star`, `lifecycle.star`.

## Pitfalls

- Unit fakes that don't model the daemon's resource lifecycle hide real bugs: the
  dockerhost fake accepted duplicate `PortBindings` for months, masking a live
  "port already allocated" failure. Fake the lifecycle (allocate/conflict/release),
  not just the wire shapes.
- k8s: never bare Get -> Update on a Deployment — the controller writes concurrently
  and it 409s under load (surfaced as 500). Route through `updateDeployment`.
- containerd: `Stop` MUST set the `explicitly-stopped=true` restart label BEFORE
  killing the task, or the restart monitor resurrects it within a reconcile tick;
  conversely Restart-after-stop resurrection is a host-backend behavior k8s does not
  share.
- Silently ignoring an unparsable `--since` returns ALL logs (the original k8s bug);
  silently mapping `spec.Command` to k8s `Command` drops the image ENTRYPOINT for
  every compose `command:` (silent docker -> k8s behavior change). Warn or error;
  never drop.
- Skipping stdcopy framing on a raw exec/attach stream corrupts client demuxing even
  when output "looks fine" in manual tests.
- containerd hosts-file updates must be in-place block rewrites — a rename detaches
  the live `/etc/hosts` bind mount; log rotation is only safe at `startTask`.
- `ConfigureDefaultRegistries` attaches no Authorizer — hand-built containerd
  resolvers need `docker.NewDockerAuthorizer()` or anonymous Hub pulls 401; normalize
  short names with `reference.ParseDockerRef` before the pull path.
- barehost recovery must release stale CNI allocation before rebuilding a vanished netns, and
  companions need graceful teardown before runtime deletion.
- Incus upgrades are coupled to containerd's runtime-spec version; treat the client pin as an API
  and license-audit boundary.
