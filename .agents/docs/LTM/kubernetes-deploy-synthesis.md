# Kubernetes Deploy Synthesis (backend, pod mutation, network fabrics)

## Summary

How Cornus turns a `DeploySpec` into Kubernetes objects and everything it injects into the pod on
the way: the imperative client-go backend (Deployment/Job/Service/PVC, annotation lifecycle,
conflict-retried mutation, ownerReference GC, one-shot restart values mapped to a Kubernetes Job),
the netdriver provider pipeline for compose user networks (incl. Multus static IPs + overlaid DNS),
and the sidecar/init-container assembly sites (caretaker, net-redirect, volume populate, sidecar
image self-discovery). Merged because pod mutation, labels, and GC are one design surface — a
change to any injection site or label scheme touches all three source documents.

## Included Documents

| Document | Focus |
|----------|-------|
| [kubernetes-backend.md](./kubernetes-backend.md) | Object mapping, lifecycle annotations, managed PVCs, ownerRef GC |
| [user-networks-and-caretaker.md](./user-networks-and-caretaker.md) | netdriver provider pipeline, proxy/DNS injection, dind validation (also feeds the caretaker synthesis) |
| [client-local-mounts-deploy.md](./client-local-mounts-deploy.md) | k8s facet: `ApplyWithMounts` sidecar injection, no-hostPath rule (also feeds the caretaker synthesis) |
| [client-side-egress.md](./client-side-egress.md) | Kubernetes egress role injection, detached-network guard, and relay lifecycle |

Current package root: `pkg/deploy/kubernetes/` (older journal-era text says
`internal/deploy/kubernetes`). For the cross-backend `deploy.Backend` contract shared with
dockerhost/containerd (framing, lifecycle verbs, argv semantics), see
[deploy-backends-synthesis.md](./deploy-backends-synthesis.md).

## Stable Knowledge

### Object mapping and lifecycle

- `DeploySpec` -> one Deployment (+ ClusterIP Service only when ports are published; `service()`
  returns nil for port-less deploys). Deliberately NOT an operator: no CRD, no reconcile loop —
  Cornus creates the objects, so it mutates them directly at Apply time.
- Docker create semantics for argv: `DeploySpec.Entrypoint` -> container `Command` (only an
  explicit Entrypoint overrides the image ENTRYPOINT); `DeploySpec.Command` -> container `Args`
  ALWAYS — so a command-only spec (compose `command:`) preserves the image ENTRYPOINT.
- Lifecycle is annotation-driven: Stop scales to 0 and saves the count in `cornus.dev/replicas`;
  Start restores it; Restart stamps `cornus.dev/restartedAt` on the pod template. Delete is a
  single foreground-propagation Deployment delete; Service and PVCs carry ownerReferences
  (`Controller` + `BlockOwnerDeletion` true) and are reclaimed by k8s GC — interrupted or
  out-of-band deletes cannot orphan them. Deployment-first ordering is safe (pods tolerate a
  briefly-missing PVC).
- `cornus.dev` is the project's real domain and the canonical API-group/annotation/label
  prefix everywhere (the `pkg/kubehub` CRD group, the two annotations above, RBAC
  `apiGroups` for `hubendpoints`) — it replaced an earlier `cornus.io` prefix that the
  project does not own. The rename was a clean sweep with no compatibility shim; the CRD
  group and the RBAC `apiGroups` entry must always agree, since a chart-vs-manifest
  mismatch here silently breaks RBAC for the CRD.
- Deploy-attach readiness is real workload readiness, not object creation: `awaitReady`
  (`pkg/server/deploy_attach.go`) polls `backend.Status` every second until all desired
  instances run (or, for a one-shot, exit cleanly), streams deduplicated non-terminal
  diagnostics, and tears the workload down on a five-minute timeout or client
  cancellation. Kubernetes' `statusOf` feeds this via `api.InstanceStatus.Message`
  (`name: reason: message`), checking init/sidecar container state (CrashLoopBackOff,
  ImagePullBackOff, ErrImagePull, InvalidImageName, CreateContainerConfigError,
  CreateContainerError, RunContainerError) before app-container state, then a non-zero
  terminated container, then Unschedulable — otherwise a failed caretaker sidecar hides
  behind the app container's innocuous `PodInitializing` status.
- Every Deployment mutation goes through `updateDeployment`, which wraps Get -> mutate -> Update
  in `retry.RetryOnConflict(retry.DefaultRetry, ...)` — the deployment controller writes to the
  Deployment concurrently, so a bare Get -> Update races into 409 Conflict (a
  stop -> start -> restart sequence reliably tripped it).
- Restart policy: `deploy.IsOneShot(spec)` (restart `no`/`on-failure`, tolerant of the
  raw `on-failure:N` form) routes a workload to a `batch/v1` Job instead of a
  Deployment (`pkg/deploy/kubernetes/job.go`) — restartPolicy `Never`/`OnFailure`,
  `backoffLimit` from `RestartMaxAttempts`; a Job's pod template is immutable, so
  re-apply deletes and recreates it. Every other restart value (unset, `always`,
  `unless-stopped`) still runs as a Deployment with restartPolicy `Always`
  (`unless-stopped` counts as honored — Stop scales to zero). `warnUnsupportedRestart`
  is gone; one-shots are honored, not warned. Stop/Start/Restart remain
  Deployment-only. See [[kubernetes-backend]] for the full mechanism and the root
  cause (a permanent caretaker-mount CrashLoopBackOff once a one-shot's deploy-attach
  session ended and the always-restarting Deployment kept reconnecting a dead mount
  session).
- Non-TTY exec AND attach output is stdcopy-framed via `muxWriters` (the `deploy.Backend`
  framing contract — clients demux unconditionally). `ExecCreate` warns per-field for
  Env/WorkingDir/User/Privileged (`PodExecOptions` cannot carry them; no `sh -c` emulation,
  containers may lack a shell); ExecInspect reports Running = started && !done and Pid stays 0.
- Managed volumes: anonymous -> PVC `<name>-vol-<i>` (RWO, default 1Gi, cluster-default
  StorageClass), owner-ref'd (ephemeral, `docker rm -v` parity); named -> one shared PVC
  `namedPVCName(logical)` with NO owner-ref (survives `cornus delete` of any sharer), labelled
  `cornus.managed=true` + `cornus.volume=<name>`. A `cornus-volinit-<i>` initContainer seeds a
  fresh PVC from image content at the target (copy-only-when-empty; needs `/bin/sh` + `cp`/`ls`
  in the image).
- Config: in-cluster config or kubeconfig fallback; `CORNUS_K8S_NAMESPACE`,
  `CORNUS_K8S_IMAGE_PULL_POLICY` (the E2E kube target sets `IfNotPresent` for `kind load`ed
  images).
- Helm supplies `CORNUS_ADVERTISE_URL` by default as the server Service FQDN and Service
  port: `ws{s}://<fullname>.<namespace>.svc:<service.port>`. `advertise.enabled`
  (default true) omits it when false; `advertise.url` overrides it. Relay clients must
  use the Service port, not the container target port — distinct from the
  node-reachable `registry.advertiseHost`.

### Hard rules

- NEVER generate `hostPath` from a bind mount: stateless `Apply` rejects bind-mount specs with an
  error pointing at deploy-attach; `ApplyWithMounts` rejects mounts lacking a client-local 9P
  backing. Client-local mounts are realized as a privileged native sidecar
  (`initContainer` + `restartPolicy: Always`, k8s >= 1.29) kernel-9p-mounting an emptyDir with
  `Bidirectional` propagation, gated by a `cornus mountcheck` startupProbe.
- `checkPrivilege` (default-deny `Privileged`, opt-in `CORNUS_ALLOW_PRIVILEGED`) applies only to
  the USER spec — Cornus's own privileged sidecars come from separate container specs.
- Sidecar image resolution (`sidecarImageFor`): explicit `CORNUS_K8S_SIDECAR_IMAGE`
  override -> self-discovery of the server's own running image via its Pod
  (`discoverSelfImage`, reading the container named `cornus`) -> the workload's app
  image as a last resort. The app-image fallback alone silently breaks every sidecar
  on a non-cornus workload image (no `cornus` binary to exec); self-discovery closes
  that gap without any new env var or Helm value. See [[kubernetes-backend]].
- Every injected sidecar (caretaker, net-redirect, mount-agent — all seven
  construction sites) sets `Command: []string{"cornus"}` explicitly instead of relying
  on the image ENTRYPOINT — belt-and-suspenders alongside the image-resolution order
  above, since a sidecar running under the app-image fallback would otherwise execute
  the app's own entrypoint. `TestApplyWithMountsInjectsSidecar` pins this.
- ONE caretaker sidecar per pod: `deploymentWithMounts` folds proxy/DNS/hub roles into the
  privileged mount caretaker when both are present; conflicting combinations (dns+proxy,
  hub+proxy) are rejected. The caretaker token is stamped only onto server-bound configs
  (mounts or hub), never DNS/proxy-only sidecars.

### Network fabrics (netdriver)

- `pipelineFor(driver, net)` in `pkg/deploy/kubernetes/internal/netdriver/`: providers are pure
  builders (NetworkScoped/WorkloadScoped/MutatePod/Requires); the Engine does all I/O. New
  fabrics are data, not control flow (the cilium provider was ~50 lines).
- Matrix: `services` (headless Service per alias — DNS baseline, any cluster); `bridge`/`ipvlan`/
  `macvlan` (Multus NAD, needs the NAD CRD); `policy` (shared Ingress-only NetworkPolicy, emitted
  unconditionally); `cilium` (CNP, needs the CRD); `driver_opts: {proxy: "true"}` (+
  `mode: cooperative`) mutate the pod instead. Capability fallback-to-services with a warning, or
  hard error under `CORNUS_K8S_NET_STRICT`.
- Multus static IPAM: plan-time deterministic IPs (`pkg/compose/usernet.go` — sha256 of the
  resource name onto the subnet's host range, salted-probe collision handling; compose
  `ipv4_address` is an explicit override; dynamic host-local fallback for `replicas > 1`). The
  NAD renders `static` IPAM plus the `ips` capability, and the pod annotation upgrades from the
  name form to the Multus JSON selection form carrying the pinned IPs. BOTH annotation forms
  (`k8s.v1.cni.cncf.io/networks` and `v1.multus-cni.io/default-network`) must be
  namespace-qualified `<ns>/<nad>` — Multus resolves an unqualified reference in ITS configured
  default namespace (kube-system), not the pod's. Pinned specs use the Recreate deployment
  strategy (a rolling update would briefly run two pods claiming the same static IP).
- Detached mode (`networks[].default: true`, at most one): the user network IS the pod's primary
  interface — no net1, no caretaker, direct-IP data path; rejected in combination with
  attach-mounts (the 9P relay rides the cluster network).
- Overlaid-mode DNS: CoreDNS never publishes Multus secondary IPs, so the caretaker dns role
  serves peer SECONDARY IPs (the pinned static addresses), driven by
  `api.DNSSpec.RequireUserNet`; on non-Multus clusters this degrades gracefully to plain
  services DNS.
- Membership is stamped as `cornus.net/<netLabel>` pod labels; shared objects (NAD/NetworkPolicy/
  CNP) are reaped by mark-and-sweep GC from `Backend.Delete`, which must SKIP Deployments with a
  non-nil DeletionTimestamp (foreground deletion lingers Terminating — fakes hide this).
- The enforcing proxy's allow-list is computed at COMPOSE-PLAN time (`applyProxyPolicy` sees the
  whole topology); runtime denial is by destination IP via `SO_ORIGINAL_DST`. Capture is
  `cornus net-redirect` programming nftables directly over netlink (no nft binary needed).

### E2E integration

- Kube-only assertions live in `deploy-shape.star` (object shapes via `kubectl -o jsonpath`),
  `deploy-volumes.star` (PVC provision/seed/persist/GC), `deploy-named-volume.star`,
  `deploy-mounts.star`, `deploy-oneshot.star` (`restart: no` -> Job, `restartPolicy: Never`,
  no Deployment, live 9P mount inside the Job pod), and the `deploy-network`/`netpolicy`/`proxy`/
  `dns`/`multus`/`cilium` scenarios. The dind runner (`make e2e-container`) is THE validation vehicle
  — every real bug in this area passed unit fakes and was caught only live.
- Multus fabric validation matrix (all live-validated in dind except cross-node macvlan):

  | Row | Fabric | Scenario | Gate |
  |---|---|---|---|
  | A' | bridge, overlaid, static IPs | `deploy-multus.star`, `ftp-usernet.star` | `E2E_MULTUS=1` |
  | b | ipvlan, overlaid | `deploy-multus-ipvlan.star` | `E2E_MULTUS_IPVLAN=1` |
  | c | macvlan, overlaid | `deploy-multus-macvlan.star` | `E2E_MULTUS_MACVLAN=1` |
  | D | detached (primary) | `deploy-multus-detached.star` | `E2E_MULTUS_DETACHED=1` |

  Row c asserts POD-TO-POD only — macvlan slave-to-parent is impossible by kernel semantics;
  cross-node macvlan is environment-sensitive and permanently gated. Row D is driven via
  `cornus deploy --detach` + `networks[].default: true` and flushed out two real bugs:
  `pkg/client.New` must normalize `ws://`/`wss://` bases for plain HTTP calls
  (`TestClientWSBaseNormalized`), and the `default-network` annotation must be
  namespace-qualified.

## Operational Guidance

- Funnel all Deployment creation through `applyDeployment` (both plain and with-mounts builders)
  so membership labels, ownerRefs, and mutation hooks apply uniformly.
- Adding a network fabric: write a Provider, add a `pipelineFor` entry, add its GVR to the GC
  loop. Nothing else changes.
- Unit-test wiring, not behavior the fake clientset cannot model: assert ownerReference
  Kind/Name/Controller (UIDs are empty in fakes), never expect the GC cascade or foreground
  deletion; leave those to kind E2E.
- kubectl jsonpath cannot address dotted annotation keys — dump `{.metadata.annotations}` and
  substring-match.

## Files

- `pkg/deploy/kubernetes/kubernetes.go` — backend, `applyDeployment`, `updateDeployment`
  (conflict retry), `muxWriters`, the Entrypoint -> Command /
  Command -> Args mapping, `deploymentWithMounts`, `injectProxy`/`injectDNS`/`injectHub`,
  `checkPrivilege`, `caretakerConfigEnv`, `volumePopulateContainer`, `namedPVCName`,
  `sidecarImageFor`/`discoverSelfImage`, `applyDependents`
- `pkg/deploy/kubernetes/job.go` — one-shot Job mapping: `applyWorkload`, `applyJob`,
  `jobFromDeployment`, `jobStatus`/`statusOfJob`/`representativePod`/`fillInstanceFromPod`
- `pkg/deploy/deploy.go` — `IsOneShot(spec)`, shared by the server and this backend
- `pkg/deploy/kubernetes/internal/netdriver/` — Provider/Engine, `pipelineFor`, capability
  detection, mark-and-sweep GC, services/multus/networkpolicy/cilium providers; `multus.go`
  (static-IPAM NAD, JSON selection-form annotation, namespace-qualified `<ns>/<nad>` refs)
- `pkg/compose/usernet.go` — plan-time deterministic IP allocator, `ipv4_address` override
- `pkg/api/deploy.go` — `DeploySpec`, `VolumeSpec`, `NetworkAttachment`, `ProxySpec`, `DNSSpec`,
  `HubSpec`
- `pkg/deploy/deploy.go` — `Backend`, `MountingBackend`, `AttachMount`
- `cmd/cornus/netredirect_linux.go` — nftables-over-netlink redirect
- `e2e/scenarios/deploy-*.star` — kube scenarios; `e2e/container/` — dind runner

## Tests

- Fake-clientset: object mapping, lifecycle, `TestLifecycleRetriesOnConflict` (a reactor 409s
  every first Update; all three verbs must retry and land), `TestApplyEntrypoint`,
  `TestManagedResourcesOwnedByDeployment`, `TestAnonymousVolumePopulateInitContainer`,
  `TestNamedVolumeSharedAndPersistent`, `TestApplyRejectsBindMounts`,
  `TestApplyWithMountsInjectsSidecar`, `TestApplyPrivileged`, `muxWriters` framing + exec
  lifecycle tests, proxy/DNS/hub injection tests, netdriver provider + GC tests
  (`TestGCIgnoresTerminatingDeployment`).
- Live kind: `deploy-volumes.star`, `deploy-shape.star`, `deploy-mounts.star`,
  `deploy-oneshot.star` (re-run alongside `deploy-mounts.star`/`deploy-mounts-multi.star`
  to confirm no regression), the network scenario family incl. the `deploy-multus-*.star`
  matrix rows, `deploy-netpolicy-enforce.star` (kindnet DOES enforce NetworkPolicy);
  `lifecycle.star`'s stop -> start -> restart is the live regression for the conflict retry.
- `TestSidecarImageDiscoveredFromOwnPod`/`EnvOverrideWins`/`FallsBackToAppImage` (image
  resolution order); `TestExecCommand`/updated `TestExecCreateWarnsUnsupportedFields`
  (exec `Env` honored via `env(1)` wrapping, no longer warned).
- `TestStatusOfSurfacesWedgeDiagnostics` plus `pkg/server/deploy_await_test.go`'s
  short-circuit, wait-then-ready, crash-loop-timeout, and cancellation cases
  (`awaitReady`); live kind `deploy-crashloop.star` asserts a deliberately failing pod
  reports a CrashLoopBackOff diagnostic with zero running instances; `deploy-mounts.star`
  asserts the mount caretaker's command is `cornus`.

## Pitfalls

- The fake clientset runs no GC and leaves UIDs empty — assert wiring, not cascades.
- Foreground-deletion GC race: skip Terminating Deployments in reference-counting GC or shared
  network objects are never reaped (live-only bug).
- Named PVCs must not carry a Deployment owner-ref, or `cornus delete` reaps them; anonymous PVCs
  must. Shared RWO PVCs require co-located pods — fine on single-node kind only.
- Never do a bare Get -> Update on the Deployment (409 under controller writes — route through
  `updateDeployment`), and never map `spec.Command` to container `Command` (it overrides the
  image ENTRYPOINT and silently changes compose `command:` behavior vs docker).
- kind ships a minimal CNI plugin set; Multus needs plugins staged onto nodes and a readiness
  canary (DaemonSet Ready != able to attach; a pod created in the window never self-heals).
- Unqualified Multus network references resolve in Multus's OWN default namespace (kube-system),
  hanging pods in ContainerCreating with NoNetworkFound — always emit `<ns>/<nad>`.
- CoreDNS never publishes Multus secondary IPs — overlaid name resolution rides the primary
  network unless the caretaker DNS role serves the pinned secondary IPs (the static-IPAM +
  `DNSSpec.RequireUserNet` path, matrix row A').
- macvlan slave-to-parent traffic is impossible by kernel semantics — assert pod-to-pod only;
  pinned static IPs require the Recreate strategy.
- Same-namespace E2E scenarios must use distinct headless-Service alias names (bare-alias
  collisions race create/cascade-delete across scenarios).
- PVC seeding and the mount-agent path require a "full enough" image (`/bin/sh`, `cp`, `ls`);
  `FROM scratch` images cannot use them.
- `sidecarImageFor` falling back all the way to the app image (no override, no
  discoverable self image) silently breaks every sidecar on a non-cornus workload —
  prefer relying on/fixing self-discovery over adding another manual override.
- A Job's pod template is immutable: re-apply must delete-and-recreate, not `Update()`
  in place like a Deployment.
- `Backend.Delete` always attempts a Job delete alongside the Deployment delete (it
  doesn't track which kind a workload is), so the `batch`/`jobs` RBAC grant is required
  for EVERY workload delete on this backend, not only one-shot/Job ones — as is
  `persistentvolumeclaims` for every volume-bearing deploy. Both were absent entirely
  (not just under-verbed) in every shipped Role manifest until fixed; an already-running
  in-cluster cornus needs its Role re-applied/patched to pick either up.
- Stop/Start/Restart are Deployment-only; do not expect them on a one-shot Job.
- Never reintroduce `cornus.io` as the API-group/annotation/label prefix — the project
  does not own that domain; `cornus.dev` is canonical everywhere and the CRD group must
  always agree with the RBAC `apiGroups` entry for it.
- A failed caretaker/init sidecar hides behind the app container's innocuous
  `PodInitializing` status unless `statusOf` is asked to look at init/sidecar state
  first — do not read only the app container's status when diagnosing a wedged deploy.

## Client-Side Egress On Kubernetes

The Kubernetes facet of `client-side-egress.md` is injected through the same deployment assembly that handles mounts, DNS, and hub roles. Relay egress requires the cluster network for `/.cornus/v1/caretaker/attach`; reject it on a `Default` detached-primary network, while environment-only egress remains valid.

The egress role must fold into the one caretaker container and is incompatible with enforcing proxy. Its transparent redirect exempts loopback and captures TCP only, preserving caretaker DNS and hub loopback traffic. Use `ApplyWithAttachments` for relay-mode policy so the session and sidecar are created together.
