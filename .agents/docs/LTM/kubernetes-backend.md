# Kubernetes deploy backend

## Summary

The Kubernetes backend (`pkg/deploy/kubernetes/kubernetes.go`; originally
`internal/deploy/kubernetes`) implements Cornus's imperative deploy engine on client-go:
a `DeploySpec` becomes a Deployment (plus a ClusterIP Service when ports are published),
managed volumes become dynamically provisioned PVCs seeded from image content on first
start, and cleanup is Kubernetes-native via ownerReferences + foreground GC. It is
deliberately NOT an operator — no CRD, no controller — just robust imperative client-go.

## Key Facts

- Deps: `k8s.io/{client-go,api,apimachinery}` (introduced at v0.32.1, upgraded cleanly
  from transitive v0.26; no diamond).
- Object mapping: `DeploySpec` -> Deployment (replicas, container image/command/env/ports,
  hostPath bind mounts) + ClusterIP Service; `service()` returns nil when
  `len(Ports)==0`, so no Service object exists for port-less deploys.
- Entrypoint/Command mapping (docker semantics): container `Command` is set only from
  `DeploySpec.Entrypoint`; `DeploySpec.Command` always maps to container `Args`, so a
  command-only spec (compose `command:`) preserves the image ENTRYPOINT instead of
  overriding it.
- Lifecycle: `Stop` scales to 0 and remembers the prior count in the
  `cornus.dev/replicas` annotation (`replicasAnnotation`); `Start` restores it;
  `Restart` stamps `cornus.dev/restartedAt` (`restartAnnotation`) on the pod template to
  force a rollout.
- The `cornus.dev` prefix (the CRD group in `pkg/kubehub`, the two annotations above,
  and the CRD's RBAC `apiGroups` entry) is the project's actual domain. It replaced an
  earlier `cornus.io` prefix, which the project does not own; the rename was clean with
  no compatibility shim (annotations/CRD group are not data that persists across the
  rename boundary). The CRD group and the RBAC `apiGroups` for `hubendpoints` must
  always agree — the rename also fixed a pre-existing chart-vs-manifest drift where the
  Helm chart still said `cornus.io` while the plain manifest had moved on.
- All lifecycle mutations go through the shared `updateDeployment` helper —
  Get -> mutate -> Update wrapped in `retry.RetryOnConflict(retry.DefaultRetry, ...)` —
  because the deployment controller writes to the Deployment concurrently and a bare
  Get -> Update races into 409 Conflict.
- Restart policy: `deploy.IsOneShot(spec)` (`pkg/deploy/deploy.go` — restart `no`/
  `on-failure`, tolerant of the raw `on-failure:N` form) routes a workload to a
  Kubernetes Job (`pkg/deploy/kubernetes/job.go`) instead of a Deployment; every other
  restart value (unset, `always`, `unless-stopped`) stays a Deployment with
  restartPolicy `Always` (`unless-stopped` counts as honored because Stop scales to
  zero). `warnUnsupportedRestart` is gone — one-shot restart values are now honored,
  not warned. Stop/Start/Restart remain Deployment-only (not wired for Jobs); all
  three wrap `deploy.ErrNotFound` for a missing name. See "One-shot workloads deploy
  as a Kubernetes Job" below.
- Sidecar image resolution (`sidecarImageFor`) order: explicit
  `CORNUS_K8S_SIDECAR_IMAGE` override -> self-discovery of the server's OWN running
  image via its Pod (`discoverSelfImage`) -> the workload's app image as a last
  resort. See "Sidecar image self-discovery" below.
- Every injected sidecar (caretaker, net-redirect, mount-agent — all seven
  construction sites) sets `Command: []string{"cornus"}` explicitly rather than
  relying on the image ENTRYPOINT. This is belt-and-suspenders alongside sidecar image
  resolution: when `sidecarImageFor` ends up on the app-image fallback, the sidecar
  would otherwise run the app image's own entrypoint instead of `cornus`.
  `TestApplyWithMountsInjectsSidecar` asserts the mount caretaker's command so an
  E2E-only image-entrypoint assumption can't conceal a regression.
- Deploy-attach readiness is real workload readiness, not object creation:
  `handleDeployAttach` (`pkg/server/deploy_attach.go`) calls `awaitReady` instead of
  sending `Ready:true` immediately. It polls `backend.Status` at `readyPollInterval`
  (one second) until every desired instance is running, streams deduplicated
  non-terminal diagnostics, and tears the workload down on the five-minute
  `readyTimeout` or client cancellation. `statusOf` surfaces
  `api.InstanceStatus.Message` (`name: reason: message`): init/sidecar containers are
  checked first for CrashLoopBackOff / ImagePullBackOff / ErrImagePull /
  InvalidImageName / CreateContainerConfigError / CreateContainerError /
  RunContainerError, then a non-zero-exit terminated container, then an Unschedulable
  condition — otherwise a failed caretaker sidecar hides behind the app container's
  innocuous `PodInitializing` status.
- Non-TTY exec AND attach output is stdcopy-framed via `muxWriters` (the
  `deploy.Backend` framing contract); `ExecCreate` warns per-field for
  WorkingDir/User/Privileged (not honorable via `PodExecOptions`) but now honors `Env`
  by wrapping the command as `env KEY=VALUE ... cmd...` (`execCommand`) — the
  `pods/exec` subresource has no native per-exec env parameter, so this is the only
  way to pass `-e` values on this backend, at the cost of argv/`ps` visibility inside
  the pod for the process's lifetime (documented in `docs/cli/compose.md`, not a
  cornus-side gap: dockerhost/containerdhost use native, non-argv exec-env
  mechanisms and don't have this exposure). ExecInspect reports Running =
  started && !done and Pid stays 0 (documented).
- Config: in-cluster config or local kubeconfig fallback (works against kind from a dev
  machine). Namespace via `CORNUS_K8S_NAMESPACE`; image pull policy via
  `CORNUS_K8S_IMAGE_PULL_POLICY` (Always / IfNotPresent / ...) — the E2E kube target sets
  `IfNotPresent` so pods run `kind load`ed images without an in-cluster registry.
- Managed volumes: `api.VolumeSpec{Target,Size,StorageClass,ReadOnly}` on
  `DeploySpec.Volumes` (`pkg/api/deploy.go`) -> PVC `<name>-vol-<i>` (RWO, default size
  1Gi, nil `StorageClassName` => cluster default StorageClass), mounted at the target.
- First-start seeding: a `cornus-volinit-<i>` initContainer copies the image's baked
  content into a fresh (empty) PVC, matching Docker's seeding of fresh
  anonymous/named volumes.
- Cleanup: `Delete` is a single
  `Deployments().Delete(..., PropagationPolicy=Foreground)`; the Service and PVCs carry
  an ownerReference to the Deployment and are reclaimed by Kubernetes GC. Interrupted or
  out-of-band deletes (`kubectl delete deployment`) can no longer orphan them.
- Plain (non-sidecar) bind mounts are rejected by `Apply` on this backend.
- Pod-subresource RBAC: server-proxied logs/exec/attach/port-forward need grants on
  pod SUBRESOURCES, which are distinct from the parent `pods` grant in Kubernetes RBAC
  — `pods/log` (get); `pods/exec` + `pods/attach` + `pods/portforward` (get, create).
  These rules ship in every RBAC manifest (`deploy/k8s/cornus.yaml`, helm
  `rbac.yaml`, the incluster e2e manifests). An already-running in-cluster cornus must
  re-apply the manifest (or have its Role patched) before the server path works.
- Direct-to-pod is preferred over the server proxy for logs and port-forward: the
  client uses the developer's kubeconfig credentials (`pkg/kubelogs`, `pkg/kubefwd`)
  and only falls back to the server proxy when the direct path fails. exec/attach have
  no direct path, so the server proxy (RBAC-gated) is their only route. See
  [[port-forwarding]] and [[auth-and-security]].
- Cluster-side reconcile reporting: `cornus compose up` polls `Client.Status` after
  `Deploy` and prints per-instance state changes until every instance is running,
  because the deploy backends (kube especially) return `DeployStatus` the instant
  objects are created — before any pod is scheduled.
- Healthcheck / probes: only the kube backend maps a healthcheck to pod probes;
  containerd ignores Healthcheck. Cross-backend divergence, documented not silent.

## Details

### Object shapes and lifecycle

A `DeploySpec` maps to one Deployment carrying the container image, command, env,
containerPorts, and hostPath bind mounts, plus a ClusterIP Service only when ports are
published. Lifecycle is annotation-driven rather than object-deleting:

| Operation | Mechanism |
|-----------|-----------|
| Stop | scale replicas to 0; save prior count in `cornus.dev/replicas` annotation |
| Start | restore replicas from the `cornus.dev/replicas` annotation |
| Restart | stamp `cornus.dev/restartedAt` on the pod-template annotations (forces rollout) |
| Delete | delete the Deployment with `PropagationPolicy=Foreground`; GC cascades to owned Service + PVCs |

Stop/Start/Restart all mutate the live Deployment, which the deployment controller is
concurrently writing (status, revision annotations). A bare Get -> Update therefore
races into 409 Conflict (surfaced to clients as a 500) — a stop -> start -> restart
sequence reliably tripped it. All three verbs share the `updateDeployment` helper,
which wraps Get -> mutate -> Update in
`retry.RetryOnConflict(retry.DefaultRetry, ...)`.

### Entrypoint/Command mapping (docker semantics)

`DeploySpec.Entrypoint` maps to container `Command` and `DeploySpec.Command` always
maps to container `Args`:

| DeploySpec | Pod container |
|-----------|---------------|
| Entrypoint set | `Command=spec.Entrypoint`, `Args=spec.Command` |
| Entrypoint unset | `Command` unset (image ENTRYPOINT applies), `Args=spec.Command` |

This matches Docker, where Cmd is args to the image ENTRYPOINT and only an explicit
Entrypoint overrides it. Two historical bugs shaped this: create-time `Entrypoint`
was once dropped entirely (the devcontainer CLI creates with
`Entrypoint=["/bin/sh"] Cmd=["-c", <keepalive>]`, which degenerated to argv
`["-c", ...]`), and command-only specs once set k8s `Command=spec.Command`, which
silently dropped the image ENTRYPOINT — compose `command:` is exactly that shape, so
docker -> k8s moves changed behavior. For contrast: dockerhost maps
`spec.Entrypoint` to Docker's `Entrypoint` create slot and `spec.Command` stays Cmd.

The contract phrased operationally: `command` replaces the image's args (k8s `Args`),
while `entrypoint` replaces the image ENTRYPOINT (k8s `Command`). Supplying `entrypoint`
requires plumbing it end to end — compose `Service` carries an `entrypoint:` field
mapped to `spec.Entrypoint` in `translateService` (was silently unsupported), and the
E2E `deploy`/`deploy_attach` builtins gained an `entrypoint?` kwarg
(`pkg/e2e/harness.go`). Devcontainer's `overrideCommand` keep-alive is emitted as
`Entrypoint` (not `Command`) so it actually runs on ENTRYPOINT-bearing images, matching
`@devcontainers/cli` (`Entrypoint=["/bin/sh"]` + keepalive).

Mistaking the mapping is easy: kube-only E2E scenarios once set `command` on
ENTRYPOINT-bearing images expecting REPLACEMENT, so `cornus:e2e`
(ENTRYPOINT `["cornus"]`) with `command=["sleep","3600"]` ran `cornus sleep 3600` and
crashed; `hashicorp/http-echo` and `alpine/socat` doubled their binary. The correct
fixture form is `entrypoint=["sleep"], command=["3600"]`, and hub exporters drop the
redundant leading binary from `command`. deploy-shape asserts BOTH directions
(`Entrypoint -> .command`, `Command -> .args`).

### Restart policy

One-shot restart values (`no`, `on-failure[:N]`) now deploy as a Kubernetes Job
instead of a Deployment — see "One-shot workloads deploy as a Kubernetes Job" below
for the full mechanism. Every other value (unset, `always`, `unless-stopped`) still
runs as a Deployment with restartPolicy `Always`; `unless-stopped` counts as honored
because `Stop` scales to zero. `warnUnsupportedRestart` (which used to warn instead of
honoring `no`/`on-failure[:N]`) has been removed as obsolete. dockerhost/containerd
honor all four restart values natively; the cross-backend divergence is now only in
*mechanism* (Job vs. native restart policy), not in fidelity.

### Exec / attach framing and ExecCreate limits

Non-TTY exec AND attach output is stdcopy-multiplexed via `muxWriters` (stdout/stderr
frames over the bridged conn), satisfying the `deploy.Backend` framing contract that
the backend's own Logs already met. `ExecCreate` cannot honor WorkingDir/User/
Privileged through `PodExecOptions` and warns per-field instead of dropping them
silently; deliberately no `sh -c` wrapping is used to emulate them, because containers
may lack a shell. `Env` IS honored (no longer warned): `execCommand(cfg)` wraps the
command as `env KEY=VALUE ... cmd...` when `cfg.Env` is set — `env(1)` applies the
variables then `exec()`s the real command directly, so there is no shell parsing or
quoting hazard, and `env` is present in essentially every image that ships a shell.
This is the ONLY way to plumb exec-time env vars through `pods/exec`, which has no
native env parameter at all (a genuine Kubernetes API constraint, not a cornus
oversight) — the tradeoff is that the values are visible via `ps`/
`/proc/<pid>/cmdline` inside the pod to anyone with exec access, for the life of that
process; dockerhost (Docker's native exec-create `Env` field) and containerdhost (the
OCI runtime spec's `Process.Env`) have no such exposure. Documented with a `::: warning`
callout in `docs/cli/compose.md` (+ ja/zh) rather than fixed in code, since there is no
native alternative on this backend. ExecInspect lifecycle: Running =
started && !done (it previously reported Running before start), and Pid stays 0
(unknowable through the exec API; documented).

### Managed volumes -> dynamic PVCs

compose `translateService` (`pkg/compose`) routes anonymous volumes (`Source==""`,
`Target!=""`) into `spec.Volumes` (bind mounts go to `Mounts`; named-volume support was
added later and is covered elsewhere — see `e2e/scenarios/deploy-named-volume.star`).
Per volume `i`, `applyDeployment` creates PVC `<name>-vol-<i>` (accessMode RWO; size
from `VolumeSpec.Size`, default 1Gi; `StorageClassName` nil unless
`VolumeSpec.StorageClass` is set, so the cluster default class applies) and wires the pod
`volumes[]` / `volumeMounts[]` at `VolumeSpec.Target`. The PVCs are ephemeral —
lifetime-bound to the deployment, mirroring `docker rm -v`.

For contrast, the dockerhost backend gets the same semantics for free via
`HostConfig.Mounts` `{Type:volume, Source:"", Target, ReadOnly}` (the daemon
auto-provisions and seeds the volume).

### First-start PVC population from image content

Docker seeds a fresh volume with whatever the image ships at the mount path; a bare PVC
mounts empty, so images expecting baked content at the volume target (config, seed data)
would see an empty directory. Fix: `deployment()` emits one populate initContainer per
managed volume via the helper `volumePopulateContainer` (name `cornus-volinit-<i>`).
It runs the app image itself and mounts the SAME PVC (`vol-<i>`) at a SCRATCH path
(`/cornus/volinit/<i>`, not the target — so the image's baked content at the target stays
visible inside the init container), then runs:

```sh
if [ -d <target> ] && [ -z "$(ls -A <scratch>)" ]; then cp -a <target>/. <scratch>/; fi
```

The empty-check makes it copy-only-on-first-start: on restart the PVC already holds
data, the copy is skipped, and user writes persist (Docker parity). Paths are
single-quoted via `shellQuote`. It is a regular init container (runs to completion
before the app); in the mounts path `deploymentWithMounts` appends its native sidecars
after these, so both coexist. Requirement: the image must contain `/bin/sh` + `cp`/`ls`
— the same "full enough image" assumption as the mount-agent path (documented on the
helper and on `VolumeSpec`).

### ownerReferences + GC-based cleanup

Design decision: "robust GC-based cleanup" was chosen over a full operator. The original
`Delete` removed Deployment -> Service -> PVCs as three separate calls, which could
orphan the Service or PVCs if interrupted (crash/timeout) or if the Deployment was
deleted out-of-band. Now:

- `applyDeployment` creates/updates the Deployment FIRST (to obtain its UID), then stamps
  the Service and each managed-volume PVC with an ownerReference to the Deployment
  (`deploymentOwnerRef`; `Controller` and `BlockOwnerDeletion` both true). Deployment-first
  ordering is safe because a pod tolerates a briefly-missing PVC (stays Pending until the
  claim appears).
- `Delete` collapses to a single Deployment delete with `PropagationPolicy=Foreground`;
  Kubernetes GC reclaims the owned Service + PVCs. Cleanup is tied to ownership, not to
  Cornus's imperative call sequence.

### E2E integration

The kube E2E target of the Starlark harness (see the e2e-harness LTM doc for the harness
itself) creates/destroys a kind cluster, writes its kubeconfig, sets the kube serve-env,
and `PrepareImage` crane-pulls the built image and `kind load`s it so pods run it without
an in-cluster registry. Kube-relevant harness features:

- `deploy(volumes=["target[:size[:class]]"])` kwarg drives managed-volume deploys.
- `deploy(expect_fail=True)` (mirroring `build`'s) logs and returns None on an expected
  failure instead of aborting — used to assert the bind-mount rejection.
- `assert_contains` accepts an optional `msg?` argument (parity with
  `assert_eq`/`assert_true`).

`e2e/scenarios/deploy-shape.star` (kube-only) reads the Deployment/Service back with
`kubectl -o jsonpath` and asserts the generated object shape: container command
(`sleep 3600`), env (`FOO=bar`), containerPort (80 from `8080:80`),
`imagePullPolicy=IfNotPresent`; Service present with published ports and ABSENT without;
`stop` -> replicas 0 + remembered `cornus.dev/replicas:1`, `start` -> restored to 1,
`restart` -> `cornus.dev/restartedAt` stamped; and the negative that a plain bind mount is
rejected by `Apply`.

`e2e/scenarios/deploy-volumes.star` (kube-only) was validated on a live kind cluster
(privileged host-network container with the host docker socket, per the
`privileged-build-tests-via-docker` memory): the PVC provisions against kind's
`standard` local-path StorageClass, binds, mounts (pod Running), is writable, data
persists across a pod restart (PVC reattaches), and `cornus delete` reclaims it via the
GC cascade. Its `volseed` deployment mounts a volume OVER alpine's `/etc` and asserts
`/etc/alpine-release` is visible through the fresh PVC, proving the populate
initContainer copied image content before the app started.

### Pod-subresource RBAC for server-proxied streaming ops

The in-cluster deploy backend originally granted `pods` with the core verbs but NOT
the pod SUBRESOURCES. In Kubernetes RBAC a subresource is a distinct grant from its
parent resource, so with only `pods` the server's ServiceAccount is forbidden from the
operations it proxies:

| Subresource | Verb | Backend call | Client op that fails |
|-------------|------|--------------|----------------------|
| `pods/log` | get | `Backend.Logs` via `Pods().GetLogs()` | `compose logs` |
| `pods/portforward` | get, create | `Backend.ForwardPort` SPDY dial | `port-forward` |
| `pods/exec` | get, create | `Backend.ExecStart` | `exec` |
| `pods/attach` | get, create | `Backend` attach | attach |

The server-side code paths (`pkg/server/deploy_exec.go`,
`pkg/deploy/kubernetes` ForwardPort/Logs/exec) were correct all along — they just
never had permission. The fix added these rules to EVERY shipped RBAC manifest:
`deploy/k8s/cornus.yaml`, `deploy/helm/cornus/templates/rbac.yaml`, and the two e2e
in-cluster manifests (`e2e/scenarios/incluster-cornus*.yaml`). There is no
golden-manifest test asserting RBAC, so this is validated by hand + live E2E.

Operational note: an existing in-cluster cornus does NOT pick up the new grants
automatically — it must re-apply the manifest (or have its Role patched) for the
server-proxy path to start working. See [[auth-and-security]].

Two more resources were later found missing from the same RBAC manifests, both
resource-absent (not just under-verbed) the same way pod subresources were:

- `persistentvolumeclaims` — absent entirely, so ANY volume-bearing deploy
  (`spec.Volumes` managed PVCs) failed at reconcile the moment `pvcs.Create(...)`
  ran. Fixed by adding the full verb set (`get, list, watch, create, update, patch,
  delete`) to `deploy/helm/cornus/templates/rbac.yaml` and `deploy/k8s/cornus.yaml`
  (matching the existing `services`/`pods` grant shape).
- `batch`/`jobs` — needed once one-shot workloads started deploying as Jobs (see
  below), because `Backend.Delete` unconditionally attempts to delete BOTH the
  Deployment and the Job for any given name (it does not track which kind a workload
  actually is) — so this 403s on **every** workload delete/`compose down` on
  kubernetes, not only one-shot ones, once the Job code path shipped without a
  matching RBAC update. Fixed by adding a `batch`/`jobs` rule (same verb set as
  `apps/deployments`) to all four Role manifests: `e2e/scenarios/incluster-cornus.yaml`,
  `incluster-cornus-auth.yaml`, `deploy/k8s/cornus.yaml`, and
  `deploy/helm/cornus/templates/rbac.yaml`.

Same operational caveat applies to both: an already-running in-cluster cornus needs
its Role re-applied/patched before either grant takes effect.

### Direct-to-pod vs server-proxy for logs and port-forward

Logs and port-forward each have two routes, and the CLIENT-side direct-to-pod route is
preferred so the operation runs with the DEVELOPER's kubeconfig credentials instead of
the cornus server's ServiceAccount (which is RBAC-gated per above):

- Logs: `pkg/kubelogs` `Open(ctx, Options)` loads the developer kubeconfig via
  `kubeclient.Load`, selects the pod by the `cornus.app` label
  (`kubeclient.FirstPod`: first Running, else first found), builds `PodLogOptions`
  from `api.LogOptions` (reusing `deploy.ParseSince`), and streams. Every setup
  failure surfaces BEFORE any bytes flow, so `composecli.streamServiceLogs` can fall
  back to the server proxy safely; once the copy starts it never falls back (would
  duplicate output). The kube stream is unframed (both streams already folded into the
  pod log) so it is written straight to the stdout writer, no stdcopy.
- Port-forward: `pkg/kubefwd` `New(kubeContext, namespace) *Dialer` satisfies
  `portfwd.Dialer`, forwarding straight to the pod over the pods/portforward SPDY
  subresource. It rejects UDP (subresource is TCP-only), lazily loads+caches the
  kubeconfig (load error cached too), resolves the pod via `kubeclient.FirstPod`, and
  opens a fresh SPDY connection per call (error+data stream pair, mirroring
  client-go's `PortForwarder.handleConnection`). The data stream is wrapped as a
  `net.Conn` (`podConn`) that closes the owning SPDY connection on Close.
  `kubefwd.Fallback{Primary, Secondary}` tries direct then proxy, firing the fallback
  only on a pre-traffic error (never on `ctx.Err()`, never after bytes flow).

`clientconn.Conn.Dialer()` returns `kubefwd.Fallback{direct, proxy}` for a cluster
profile (`KubeCluster{KubeContext, Namespace}` set, populated in `Resolve` from the
profile's `PortForward`/`KubeAuth` block) or the plain proxy client otherwise. It is
wired into every client-side forward site: `cornus port-forward`, `deploy` remote
foreground (`runRemote`), and compose up foreground (`rt.forwardDialer`). The detached
`up -d` supervisor is covered too — `spawnDaemon` passes
`--kube-context`/`--kube-namespace` to `daemon mounts` and `runDaemon` rebuilds the
same fallback dialer, so direct-pod forwarding survives into the background helper.

exec and attach have NO direct-to-pod client path, so for them the RBAC-gated server
proxy is the only route — which is why the pod-subresource grants above still matter
even after logs/port-forward went direct. See [[port-forwarding]].

### Cluster-side reconcile reporting in `cornus compose up`

`POST /.cornus/v1/deploy` (`handleDeployCollection` -> `backend.Apply`) returns a single JSON
`DeployStatus` the instant objects are created; the kube `applyDeployment` returns
`b.Status()` immediately after Create/Update with no watch/poll/wait, so `statusOf`
reports `ReadyReplicas == 0` before any pod is scheduled and the user saw `0/1 running`
with no further progress. There is no wait anywhere on the deploy path, and even the
streaming attach path emits one `Event{Ready:true}` right after `Apply` whose "ready"
is not real readiness.

Fix is CLIENT-side only (no server/backend/protocol/api-type changes):
`cmd/cornus/internal/composecli/reconcile.go` `reportReconcile` polls `Client.Status`
(the existing `GET /.cornus/v1/deploy/{name}` primitive returning per-instance
`InstanceStatus{ID,State,Running}`) every 500ms after `Deploy`, printing one line per
instance whenever its state CHANGES (`web  web-0: pending` -> `web  web-0: running`)
until every instance is running, ctx is cancelled, or a 120s bound elapses. The bound
is non-fatal (notes giving up, returns last status so the up still holds ports/mounts).
An empty instance set (backends report it before any container exists) counts as "not
yet running" so the poll keeps going. Wired into both mount-free branches
(`runForeground`, `upDetached`), with a post-wait `ctx.Err()` bail for clean Ctrl-C
teardown. A narrow `statusPoller` interface (compile-time asserted against
`*client.Client`) is the seam. The client poll was chosen over a kube Pod watch + new
streaming endpoint because it is uniform across kube/docker/containerd with zero
protocol changes; only `running`/`pending` are portable today, so richer per-pod
phases (ContainerCreating, CrashLoopBackOff, image-pull errors) would need
`InstanceStatus`/`statusOf` enriched.

## Sidecar image self-discovery

`sidecarImageFor` resolves the image used for every injected sidecar (caretaker,
net-redirect, mount-agent — all pinned to the `cornus` entrypoint/`Command`, see
"2026-07-13 Caretaker And Readiness Updates" above). Its ORIGINAL fallback, when
`CORNUS_K8S_SIDECAR_IMAGE` was unset, was the workload's own app image — which only
works when the app image happens to ship the `cornus` binary + iptables. For any real
workload (nginx, postgres, ...) the sidecar tries to `exec cornus` inside an image
with no such binary and never starts, silently breaking client-local mounts / egress /
user-networks. This was never caught by E2E because every kube scenario sets
`CORNUS_K8S_SIDECAR_IMAGE=cornus:e2e` (`pkg/e2e/target.go`), i.e. always exercises the
override path, against deploy workloads that are themselves the cornus image.

Fixed resolution order: explicit `CORNUS_K8S_SIDECAR_IMAGE` override -> the server's
OWN running image, discovered from its own Pod (`discoverSelfImage`) -> the app image
as a last resort. Discovery reads the server's own Pod (name from `POD_NAME`, else the
hostname — the same source `server.go` already trusts for the replica id; namespace
from `POD_NAMESPACE`, else the backend/release namespace where the server Pod lives,
RBAC already granting `pods get`) and takes the image of the container named `cornus`.
Runs once at backend construction (`NewWithClients`) under a 5s timeout so a slow API
server cannot wedge startup; on any failure it returns `""` and the app-image fallback
stands. This is strictly more correct than wiring the image via a Helm value because it
always reflects the *actually running* image (survives `kubectl set image`), and needs
no new env var or downward-API field (the container image is not exposed via the
downward API). No manifest/chart change required.

Tests: `TestSidecarImageDiscoveredFromOwnPod` (a non-cornus app image still yields the
discovered cornus image — the regression guard), `TestSidecarImageEnvOverrideWins`,
`TestSidecarImageFallsBackToAppImage` in `pkg/deploy/kubernetes/kubernetes_test.go`.
Coverage gap: no E2E yet deploys a genuinely non-cornus workload (e.g. a stock image)
with a client-local mount on a cluster where the server's own image is discoverable —
that would exercise the discovery path end to end rather than always the override; the
kube E2E target would need to stop force-setting `CORNUS_K8S_SIDECAR_IMAGE` for such a
scenario.

## One-shot workloads deploy as a Kubernetes Job

Root cause chain (a real production/multi-tenant bug, found via a live cluster
report, not just an E2E gap): the kube backend originally mapped EVERY workload to a
Deployment, which always restarts its pods. A compose `restart: no`/`on-failure`
one-shot/init service (e.g. `kenall-init`) is expected to run once and exit — as a
Deployment it was restarted forever. Each restart's caretaker reconnects to mount a
client-local bind whose deploy-attach session had already ended (the client's job
finished and its session closed after the first, legitimate run), so
`relayMountMuxed` misses on the now-gone session, `relayMountRemote` resets instantly
on a non-distributed hub, and the pod's caretaker exits — a permanent
`CrashLoopBackOff`. This was confirmed NOT to be a readiness race: the client serves
the 9P backings (`deploywire.Serve` -> `ServeBackings`) before the pod's caretaker
connects on first deploy; the resets happen on the RESTARTS, after the session is
already gone.

Fix: run-to-completion specs now deploy as a `batch/v1` Job.

- `deploy.IsOneShot(spec)` (`pkg/deploy/deploy.go`) is the one shared truth: restart
  `no`/`on-failure`, tolerant of the raw `on-failure:N` short form. Used by both the
  server and the kube backend.
- `pkg/deploy/kubernetes/job.go` (new): `applyWorkload` funnels every apply path
  (plain/mounts/creds/egress), mutates the network template once, then routes
  one-shots to `applyJob(jobFromDeployment(...))` and everything else to
  `applyDeployment`. Job pod `restartPolicy` = `Never` (`restart: no`) / `OnFailure`
  (`restart: on-failure`); `backoffLimit` = `RestartMaxAttempts` (default 6) for
  `on-failure`, 0 for `no`; `Completions`/`Parallelism` = 1; keeps the `cornus.app`
  selector label so exec/logs/status resolve its pods same as a Deployment.
  `jobStatus`/`statusOfJob`/`representativePod`/`fillInstanceFromPod` derive
  instances from the Job's pod (no ready-replica counter — Jobs don't have one). A
  Job's pod template is IMMUTABLE, so re-apply REPLACES the Job (waits for the old one
  to clear first, then creates the new one) rather than updating in place like a
  Deployment.
- `kubernetes.go`: extracted the shared dependents (Service/PVCs/Ingress/network) into
  `applyDependents`, called from both the Job and Deployment paths; `Status` falls
  back to the Job path on Deployment-NotFound; `Delete` deletes the Job too (see the
  `batch/jobs` RBAC note above for the resulting RBAC requirement on EVERY delete, not
  just one-shot ones); `warnUnsupportedRestart` removed as obsolete (one-shots are now
  honored, not warned).
- Server side (`pkg/server/deploy_attach.go`): `awaitReady` takes the spec and, for a
  one-shot, treats an instance that has terminated cleanly (exit 0) as ready —
  `allRunning` replaced by `allReady(st, oneShot)` — so a fast init that finishes
  before the first poll is not mistaken for a hang.
- E2E: `e2e/scenarios/deploy-oneshot.star` (kube-only) asserts `restart: no` becomes a
  Job with `restartPolicy: Never`, no Deployment object, and a live 9P mount inside the
  Job pod. Verified on a real kind cluster alongside `deploy-mounts.star`/
  `deploy-mounts-multi.star` (unregressed).

Limitation (documented, not a bug): Stop/Start/Restart remain Deployment-only — a
one-shot is not a meaningful stop/start target and these verbs are not wired for Jobs.
The `logMountReset` server-side WARN (see [[caretaker-transport-and-hub-synthesis]])
complements this fix by making any residual stale-session mount reset diagnosable even
outside the one-shot/Job scenario.

## Files

- `/home/moriyoshi/src/chimpose/pkg/deploy/kubernetes/kubernetes.go` — backend: object
  mapping, annotations (`replicasAnnotation`, `restartAnnotation`), `applyDeployment`,
  `deploymentOwnerRef`, `volumePopulateContainer`, `imagePullPolicy`, namespace/env
  handling. (Journal entries predate the `internal/` -> `pkg/` move.)
- `/home/moriyoshi/src/chimpose/pkg/api/deploy.go` — `api.VolumeSpec`,
  `DeploySpec.Volumes`.
- `/home/moriyoshi/src/chimpose/pkg/compose` — `translateService` volume routing.
- `/home/moriyoshi/src/chimpose/pkg/e2e/harness.go` — `deploy()` kwargs
  (`volumes=`, `expect_fail=`), `assert_contains` msg parity.
- `/home/moriyoshi/src/chimpose/e2e/scenarios/deploy.star`,
  `deploy-volumes.star`, `deploy-shape.star` — kube deploy scenarios.
- `/home/moriyoshi/src/cornus/pkg/deploy/kubernetes/kubernetes.go` — current home
  (post-rename): `updateDeployment` (conflict retry), `warnUnsupportedRestart`,
  `muxWriters`, the Entrypoint -> Command / Command -> Args mapping, ExecCreate
  warnings, ExecInspect lifecycle.
- `/home/moriyoshi/src/cornus/pkg/api/deploy.go` — `DeploySpec.Entrypoint []string`
  (empty keeps the image default, matching Docker); `Command` doc states the uniform
  args-to-ENTRYPOINT contract.
- `/home/moriyoshi/src/cornus/pkg/kubelogs/` — `Open(ctx, Options)` direct-to-pod log
  streaming with the developer kubeconfig.
- `/home/moriyoshi/src/cornus/pkg/kubefwd/` — `New`/`Dialer`/`podConn`/`Fallback`:
  direct-to-pod SPDY port-forward with server-proxy fallback.
- `/home/moriyoshi/src/cornus/pkg/kubeclient/` — shared `Load` + `FirstPod`
  (`cornus.app`-label pod resolver, wraps `deploy.ErrNotFound`).
- `/home/moriyoshi/src/cornus/cmd/cornus/internal/composecli/reconcile.go` —
  `reportReconcile`, `statusPoller`; `streamServiceLogs`/`forwardDialer` seams and the
  `daemon mounts` `--kube-context`/`--kube-namespace` plumbing in `commands.go`.
- `/home/moriyoshi/src/cornus/cmd/cornus/internal/clientconn/clientconn.go` —
  `Conn.KubeCluster{KubeContext,Namespace}`, `Resolve` population, `Conn.Dialer()`.
- `/home/moriyoshi/src/cornus/deploy/k8s/cornus.yaml`,
  `/home/moriyoshi/src/cornus/deploy/helm/cornus/templates/rbac.yaml`,
  `/home/moriyoshi/src/cornus/e2e/scenarios/incluster-cornus*.yaml` — pod-subresource
  RBAC rules (`pods/log` get; `pods/exec`+`pods/attach`+`pods/portforward` get,create).
- `/home/moriyoshi/src/cornus/pkg/compose` — `Service.entrypoint` field and its
  `translateService` mapping to `spec.Entrypoint`.
- `/home/moriyoshi/src/cornus/pkg/e2e/harness.go` — `entrypoint?` kwarg on `deploy`
  and `deploy_attach`.
- `/home/moriyoshi/src/cornus/pkg/deploy/kubernetes/job.go` — one-shot Job mapping:
  `applyWorkload`, `applyJob`, `jobFromDeployment`, `jobStatus`/`statusOfJob`/
  `representativePod`/`fillInstanceFromPod`, `applyDependents`.
- `/home/moriyoshi/src/cornus/pkg/deploy/deploy.go` — `IsOneShot(spec)`.
- `/home/moriyoshi/src/cornus/pkg/server/deploy_attach.go` — `awaitReady`/`allReady`:
  polls `backend.Status` (`readyPollInterval`) until every instance is running (or, for
  a one-shot, cleanly exited), streams deduplicated diagnostics, tears down on
  `readyTimeout` or cancellation; `pkg/server/deploy_await_test.go` covers it.
- `/home/moriyoshi/src/cornus/e2e/scenarios/deploy-oneshot.star` — kube-only one-shot
  Job E2E.
- `/home/moriyoshi/src/cornus/deploy/k8s/cornus.yaml`,
  `deploy/helm/cornus/templates/rbac.yaml`,
  `e2e/scenarios/incluster-cornus{,-auth}.yaml` — `persistentvolumeclaims` and
  `batch`/`jobs` RBAC rules.

## Test Coverage

- Fake-clientset unit tests (`pkg/deploy/kubernetes/kubernetes_test.go`): create
  Deployment+Service, lifecycle, not-found status; `TestAnonymousVolumeCreatesPVCAndMount`,
  `TestVolumeStorageClassAndSize`; `TestManagedResourcesOwnedByDeployment` (successor of
  `TestDeleteRemovesPVC`) asserts the ownership WIRING — Kind/Name/Controller of the
  ownerReference, not UID, which the fake clientset leaves empty — plus that Delete
  removes the Deployment; `TestAnonymousVolumePopulateInitContainer` asserts one init
  container per volume, running the app image, mounted at a scratch path != target, with
  the idempotent `ls -A`/`cp -a` script.
- compose: `TestAnonymousVolume`; dockerhost: `TestToCreateBodyAnonymousVolume`.
- `TestLifecycleRetriesOnConflict`: a fake-clientset reactor rejects every first
  Update per call with a 409; all three lifecycle verbs must retry and land.
- `TestApplyEntrypoint` (kubernetes) and `TestToCreateBodyEntrypoint` (dockerhost)
  cover the `DeploySpec.Entrypoint` mapping; further unit tests cover the
  Command -> Args mapping, `muxWriters` framing, and exec lifecycle in
  `pkg/deploy/kubernetes/kubernetes_test.go`.
- Live kind E2E: `deploy-volumes.star` (PVC provision/persist/seed/GC-reclaim) and
  `deploy-shape.star` (six object-shape checks) both pass on a real kind cluster;
  `lifecycle.star`'s stop -> start -> restart sequence is the live regression for
  the conflict retry.
- `pkg/kubelogs` fake-clientset: label selection, no-pods `ErrNotFound`, bad `since`.
- `pkg/kubefwd`: UDP rejected before load; pod resolved by label; no-pod
  `ErrNotFound`; load + load-error caching; `Fallback` primary-success/secondary/
  no-fallback-on-cancel (stubbed load + dialPod seams).
- `composecli`: direct-path logs (proxy never contacted) and fallback-on-Open-error
  (proxy used); `reconcile_test.go` scripted-fake transitions (state printed once per
  change; transient Status errors do not abort), timeout path, cancelled-ctx prompt
  return, empty->running convergence, and an end-to-end pass driving the reporter
  through a real `*client.Client` against an `httptest` server.
- RBAC change carries no Go test (no golden-manifest assertion); YAML validated by
  hand + live E2E.
- `TestSidecarImageDiscoveredFromOwnPod`, `TestSidecarImageEnvOverrideWins`,
  `TestSidecarImageFallsBackToAppImage` (sidecar image resolution order).
- `TestExecCommand` (env(1) wrapping pass-through and wrapping), updated
  `TestExecCreateWarnsUnsupportedFields` (Env no longer warns).
- One-shot Jobs: fake-clientset tests in `pkg/deploy/kubernetes/job.go`'s test file
  covering `restartPolicy`/`backoffLimit` derivation, Job-vs-Deployment routing, and
  `Status` falling back to the Job on Deployment-NotFound; live kind:
  `deploy-oneshot.star` plus a no-regression re-run of `deploy-mounts.star`/
  `deploy-mounts-multi.star`.
- `TestStatusOfSurfacesWedgeDiagnostics` (init/sidecar failure reasons surfaced via
  `InstanceStatus.Message` ahead of app-container state) plus
  `pkg/server/deploy_await_test.go`'s short-circuit, wait-then-ready,
  crash-loop-timeout, and cancellation cases for `awaitReady`; `TestApplyWithMountsInjectsSidecar`
  (mount caretaker `Command: ["cornus"]`). Live kind: kube-only `deploy-crashloop.star`
  asserts a deliberately failing pod reports a CrashLoopBackOff diagnostic with zero
  running instances.

## Pitfalls

- The fake clientset runs NO GC controller — never write a unit test expecting the
  ownerReference cascade to delete dependents; assert the ownership wiring instead and
  leave the cascade to the live kind E2E.
- The fake clientset leaves object UIDs empty, so ownership assertions must match on
  Kind/Name/Controller, not UID.
- `cornus-e2e --check` (Starlark parse) does NOT catch builtin arity or format errors —
  only resolve errors. Arity bugs (e.g. the missing `assert_contains` msg arg) surface
  only at runtime on a cluster.
- kubectl jsonpath's bracketed dotted-key form (`['cornus.dev/replicas']`) returns empty;
  dump the whole `{.metadata.annotations}` map (kubectl emits it as JSON) and
  substring-match `"cornus.dev/replicas":"1"` instead.
- PVC seeding and the mount-agent path both require `/bin/sh` + `cp`/`ls` in the app
  image; scratch-from-`FROM scratch` images cannot use managed-volume seeding.
- Managed PVCs are ephemeral by design (deleted with the deployment via GC); do not use
  them for data that must outlive `cornus delete`.
- Never do a bare Get -> Update on the Deployment: the deployment controller writes
  concurrently and the update 409s under load. Route every mutation through
  `updateDeployment` (`retry.RetryOnConflict`).
- Never map `spec.Command` to k8s container `Command` — that overrides the image
  ENTRYPOINT and silently changes compose `command:` behavior versus docker. Command
  belongs in `Args`; only `spec.Entrypoint` sets `Command`.
- `PodExecOptions` cannot carry exec Env/WorkingDir/User/Privileged; warn per-field
  and do NOT emulate via `sh -c` (containers may lack a shell). Exec Pid is
  unknowable (stays 0).
- Non-TTY exec/attach output must be stdcopy-framed (`muxWriters`) even though the
  transport is a raw bridged stream — clients demux unconditionally.
- Pod subresources are DISTINCT RBAC grants from `pods`; granting `pods` alone leaves
  server-proxied logs/exec/attach/port-forward forbidden. An already-running in-cluster
  cornus will NOT pick up new grants — the manifest must be re-applied (or the Role
  patched). Symptom is a subresource-forbidden error, not a code bug.
- Direct-to-pod logs/port-forward must surface every setup failure BEFORE any bytes
  flow, otherwise the fallback to the server proxy would duplicate output — never fall
  back once the copy/traffic has started, and never fall back on `ctx.Err()`.
- The deploy `DeployStatus` (and the attach `Event{Ready:true}`) reports "ready" the
  instant objects are created, NOT real pod readiness — do not trust it as
  convergence. Poll `Client.Status` (`GET /.cornus/v1/deploy/{name}`) for actual per-instance
  state; treat an empty instance set as "not yet running", not as done.
- `sidecarImageFor` falling back to the app image (no override, no discoverable self
  image) silently breaks every sidecar on a non-cornus workload image — always prefer
  fixing/relying on self-discovery over adding yet another manual override knob.
- `pods/exec` has NO native env parameter — any exec-time env vars on this backend
  MUST go through the `env(1)`-wrapping argv trick, which means they are visible via
  `ps`/`/proc/<pid>/cmdline` to anyone with exec access; this is a Kubernetes API
  limitation to document, not something further code can close.
- A Job's pod template is immutable — re-apply must delete-and-recreate (waiting for
  the old Job to clear) rather than update in place; do not try to `Update()` a Job
  the way `updateDeployment` does for Deployments.
- `Backend.Delete` always attempts a Job delete alongside the Deployment delete
  (it doesn't track which kind a workload is) — the `batch`/`jobs` RBAC grant is
  therefore required for **every** workload delete on this backend, not only
  one-shot/Job ones; missing it 403s `compose down`/delete universally.
- Stop/Start/Restart are Deployment-only; do not expect them to operate on a one-shot
  Job — this is a documented limitation, not an oversight to "fix" reflexively.
- Never reintroduce `cornus.io` as the API-group/annotation/label prefix — the project
  does not own that domain. `cornus.dev` is canonical everywhere (the `pkg/kubehub` CRD
  group, `cornus.dev/replicas`/`cornus.dev/restartedAt` annotations, RBAC `apiGroups`
  for `hubendpoints`); a chart-vs-manifest mismatch between the two silently breaks
  RBAC for the CRD.
- `awaitReady`'s readyPollInterval/readyTimeout are package variables specifically so
  tests can shorten them — do not hardcode a new sleep/timeout when adding a case to
  `deploy_await_test.go`.
