# Deploy Backends Synthesis

## Summary

Cornus has three `deploy.Backend` implementations — dockerhost (Docker Engine API),
containerdhost (bare containerd), and kubernetes (client-go) — bound by one documented
contract on the interface in `pkg/deploy/deploy.go`. A method-by-method cross-backend
audit established what that contract guarantees, fixed the divergences, and extracted
shared sentinels and helpers (`ErrNotFound`, `ParseSince`, `Bridge`, `hostpolicy`).
Read this first when touching any backend; drill into the per-backend source docs for
implementation depth.

## Included Documents

| Document | Focus |
|----------|-------|
| [deploy-backend-contract.md](./deploy-backend-contract.md) | The cross-backend contract, the audit's divergences and fixes, dockerhost-specific bugs (replica ports, `rm -v`) |
| [containerd-backend.md](./containerd-backend.md) | containerdhost internals: CNI, hosts-file sync, log shim, restart monitor, build worker |
| [kubernetes-backend.md](./kubernetes-backend.md) | k8s backend: object mapping, annotation lifecycle, PVCs, conflict retry, exec limits |
| See also: [kubernetes-deploy-synthesis.md](./kubernetes-deploy-synthesis.md) | DeploySpec -> k8s object shapes, netdriver fabrics, sidecar injection (the k8s deep dive) |
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
- Host-port publishing with `replicas>1` is replica-0-only on both host backends (one
  DNAT target per host port); kubernetes Services are per-deployment anyway.
- `Delete` reaps anonymous volumes on all backends (`docker rm -v` parity, promised in
  `pkg/api/deploy.go`); named volumes survive.
- State vocabulary is documented, not normalized: docker 7 states / containerd 4 /
  kubernetes only `running|pending`. Only `running` (and the Running bool) is portable.
- Fields a backend cannot honor get a per-field `slog.Warn`, never a silent drop and
  never `sh -c` emulation (containers may lack a shell). Examples: k8s
  `warnUnsupportedRestart` and exec Env/WorkingDir/User/Privileged; containerd network
  `Driver`/`DriverOpts`.
- Shared helpers: `pkg/deploy/hostpolicy` (privileged/bind-mount gating, error text
  names the backend; used by both host backends, k8s has an equivalent path) and
  `deploy.Bridge` (`pkg/deploy/bridge.go`, half-close stdio splicer: stdin EOF ->
  CloseIO; used by dockerhost and containerd exec/attach).
- Backend selection: `localBackend()` (`cmd/cornus/commands.go`) honors
  `CORNUS_DEPLOY_BACKEND=containerd`, deliberately falls through to dockerhost for
  `kubernetes`, and warns on unrecognized values. The server side is
  `defaultBackendFactory`.

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
- **kubernetes** (`pkg/deploy/kubernetes/`): DeploySpec -> Deployment (+ ClusterIP
  Service only when ports are published), annotation-driven lifecycle — Stop scales
  to 0 saving the count in `cornus.dev/replicas`, Start restores it, Restart stamps
  `cornus.dev/restartedAt`; Delete is one foreground-propagation Deployment delete and
  k8s GC reclaims owner-ref'd Service/PVCs. All lifecycle mutations go through
  `updateDeployment` (`retry.RetryOnConflict`) because the deployment controller
  writes concurrently. Config: in-cluster or kubeconfig; `CORNUS_K8S_NAMESPACE`,
  `CORNUS_K8S_IMAGE_PULL_POLICY`. See kubernetes-deploy-synthesis.md for object
  shapes, PVC seeding, netdriver fabrics, and sidecar injection.

### Known asymmetries (by documented design)

Stopped shows `1/1 exited` on host backends vs `0/0` on k8s; Restart resurrects a
stopped deploy on host backends but not on scaled-to-zero k8s; cp on stopped
containers works only on dockerhost (containerd needs a running task, k8s
unsupported); localhost refs of unpushed images work on containerd (local-store
fallback), fail on dockerhost, and mean "the node" on k8s; restart policies: host
backends honor all four values, k8s pods are always `Always` (warned except
`unless-stopped`).

## Operational Guidance

- A behavior change to one backend is a contract question: either change all three or
  document the divergence on the `Backend` interface (as the state vocabulary is).
  The interface doc comment in `pkg/deploy/deploy.go` is the contract's home.
- Server-side stream errors: `pkg/server/deploy.go` and
  `pkg/dockerproxy/containers.go` use a lazy-header writer — 200 flushes on the
  backend's first write, so pre-output errors map to real statuses (404/501/400/500).
  Do not touch the attach/wait flush-header-early protocol (docker run depends on it).
- Per-backend unit fakes: dockerhost has a wire-API fake modeling dockerd's port
  lifecycle in `pkg/deploy/dockerhost/dockerhost_test.go`; containerd fakes the
  `clientAPI` seam (`client_linux.go`) and `networkManager` (stock `NewContainerOpts`
  are unfakeable); kubernetes uses the fake clientset (no GC, empty UIDs — assert
  wiring, not cascades).
- Local gate for any Go change: `gofmt -l`, `go build ./...`, `go vet ./...`,
  `go test ./...` (or the focused `go test ./pkg/deploy/...`).
- E2E (opt-in, Starlark harness): dockerhost runs under `make e2e-container` (dind);
  containerd under `make e2e-containerd` (`--target containerd`, `CapContainerd`
  preflight); kubernetes under the kube target (kind cluster,
  `imagePullPolicy=IfNotPresent`, images `kind load`ed). Live runs are the real
  validation — every serious bug in this area passed unit fakes first.

## Files

- `pkg/deploy/deploy.go` — `Backend` interface + documented contract, `ErrNotFound`
- `pkg/deploy/since.go` — `ParseSince`; `pkg/deploy/bridge.go` — `Bridge`
- `pkg/deploy/hostpolicy/policy.go` — shared privilege policy
- `pkg/deploy/dockerhost/dockerhost.go` — dockerhost backend
- `pkg/deploy/containerdhost/` — containerd backend (`backend_linux.go`,
  `network_linux.go`, `hosts_linux.go`, `logs_linux.go`, `reconcile_linux.go`, ...;
  pure subpackages `logfmt/`, `tarcopy/`)
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
