# Client-Local Bind Mounts for Remote Deploy (dockerhost + kubernetes)

## Summary

`cornus deploy --server <url> --local-mount SRC:DST[:ro]` runs a container on a remote Cornus host while bind-mounting directories that live on the caller's machine, streamed over the existing 9P-over-WebSocket transport. The mount lives exactly as long as the client's deploy-attach session (disconnect → teardown), scoping the feature to dev/inner-loop use. Both the dockerhost backend (kernel-9p mount on the server host) and the kubernetes backend (privileged native-sidecar 9P mount inside the pod, never on the node host) are supported, read-write included. dockerhost and containerdhost also have an opt-in remote-mode path (`CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`) that realizes the mount via a companion container/task instead of a server-host kernel mount, for when the Cornus server is not co-located with the daemon it drives — see "Mount relay via a caretaker companion (dockerhost/containerdhost remote mode)" below and [[remote-companion-and-agent-forwarding]] for how that companion later grew port-forward and ssh-agent-relay roles too.

## Key Facts

- Transport: long-lived WebSocket at `/.cornus/v1/deploy/attach`; client sends `DeployAttachSpec{Spec, LocalMounts[]{Index, Name, ReadOnly}}` and serves its local dirs over 9P via `buildwire.Serve9PBacking`.
- Lifetime is the whole point: the build path already had the mechanism (`tagLazy9P` lazy bind mounts); deploy only added a session that outlives one build. Control-stream drop → server tears the deployment down (a container on a dead 9P mount only gets EIO).
- dockerhost path: `MountManager` kernel-9p-mounts on the server host under `<DataDir>/mounts/<session>/<name>` and rewrites `Spec.Mounts[i].Source` before `backend.Apply` — the dockerhost backend and `api/deploy.go` stay unaware.
- kubernetes path: optional `deploy.MountingBackend` interface (`ApplyWithMounts`) + `deploy.AttachMount`; per mount an emptyDir + privileged native sidecar (`initContainer` + `restartPolicy: Always`, k8s >= 1.29) kernel-9p-mounts with `Bidirectional` propagation; the app container gets a `HostToContainer` volumeMount gated by a `startupProbe` (`cornus mountcheck`).
- The NAT'd caller is unreachable from pods, so the Cornus server is the relay: sessions are registered by a random id and the sidecar (`cornus mount-agent`) dials back through the server, bridged to a fresh 9P backing stream on the caller.
- The kubernetes backend never generates `hostPath` volumes from bind mounts; stateless `Apply` rejects bind-mount specs outright with an error pointing to the deploy-attach path.
- Read-write mounts work end to end: a writable confined 9P attacher (`internal/buildwire/writablefs.go`) keeps writes jailed to the export root; a pod write propagates back to the caller's local dir.
- Config: `CORNUS_ADVERTISE_URL` (in-cluster URL the sidecar dials; required for the k8s path; IPv6 must be bracketed) and `CORNUS_AGENT_IMAGE` (mount-agent image, defaults to the app image).
- Per-mount RX/TX byte metrics (`cornus.mount.io.bytes`, `caretaker.mount.io.bytes`) via `wire.MeteredConn`; `internal/wire`/`internal/deploywire` remain OTel-free (callback-based hooks).
- Transport evolution: the original per-mount `/.cornus/v1/deploy/mount/{session}/{name}` relay and `runMount` were later superseded by the unified caretaker transport — everything rides `/.cornus/v1/caretaker/attach` (`runMountStream` / `relayMountMuxed`).
- A 9P mount root must be a directory. For a client-local single-file source, the client exports its
  parent directory and records the basename in `deploywire.LocalMount.Subpath`; dockerhost rewrites
  the runtime source to `<mountpoint>/<Subpath>` after mounting the parent.
- Mount names (`m<i>`) are preserved end to end by the caller-assigned `Name`, never re-derived from
  a dense loop index on the server/backend side — this is what keeps client and server/caretaker
  agreeing on which mount is which even when non-local sources are interspersed among local ones in
  `spec.Mounts` (see "Sparse mount-name indexing" below).
- `pkg/caretaker/caretaker.go` logs the mount lifecycle at info level (`caretaker: starting`,
  `caretaker: connected`, `caretaker mount: attaching` / `live` (with `took=`) / `detaching` / `failed`
  (with `error=`)) so a stuck or reset mount is attributable to a specific mount name from the pod's
  own logs in real time, without needing tracing enabled. Verified live on real kube: a multi-mount
  pod's `cornus-caretaker` container logs all `attaching` lines concurrently (one per mount goroutine,
  confirming per-mount fan-out), each followed by its own `live` once that mount completes.
- dockerhost/containerdhost also support realizing a client-local mount through a caretaker companion
  container/task instead of a server-host kernel mount, gated behind `CORNUS_DOCKER_REMOTE`/
  `CORNUS_CONTAINERD_REMOTE` — see "Mount relay via a caretaker companion" below.

## Details

### Design seam

The seam is in the wire, not in `api.Mount`. The caller classifies host-path mounts and sends `DeployAttachSpec` with a `LocalMounts` list referencing mounts by index; the server realizes each local mount and rewrites the mount source before calling `backend.Apply`. This kept the dockerhost backend and existing tests untouched — the signal the split is right. Kubernetes genuinely needs backend awareness, hence the optional `MountingBackend` interface rather than bending the base `Backend`.

`handleDeployAttach` dispatch:

- no local mounts → plain `Apply`
- backend implements `MountingBackend` (kubernetes) → sidecar injection via `ApplyWithMounts`
- dockerhost → host `MountManager` (kernel mount + source rewrite)
- anything else → clear error (never silent empty-dir binds)

The transport reuses buildwire rather than extracting an `internal/wire` package up front: `Serve9PBacking`/`Backing9PSocket` were already exported, and extraction would have meant moving the security-critical `confinedfs.go` and splitting the BuildKit-coupled half of `ninep_backing.go`. Thin exported wrappers were added instead (`internal/buildwire/export.go`: `Dial`/`Accept`/`OpenTagged`/`AcceptTagged`/`TagControl`, later `OpenBacking`/`Pipe`/`AcceptConn`/`DialConn`); `internal/deploywire` imports buildwire. (An `internal/wire` package did later materialize for `MeteredConn`.)

### dockerhost path

- `MountManager.Prepare` kernel-9p-mounts each local mount onto `<DataDir>/mounts/<session>/<name>` (config: `MountsDir` in `internal/config/config.go`) and rewrites `Spec.Mounts[i].Source`; the caller's spec is never mutated.
- Teardown order matters: remove containers first (releases the bind), then `unix.Unmount` with `MNT_DETACH` fallback on EBUSY, then socket cleanup + `RemoveAll`.
- Preflight `CanMountLocal`: Linux + euid 0 + `9p` in `/proc/filesystems`. It cannot detect the mount-propagation-to-daemon trap (see ARCHITECTURE "Privilege posture": run the server in the host mount namespace, or bind `<DataDir>/mounts` `rshared`).
- The mount step is an injectable package var (`mountFn`) so handler/rewrite tests run unprivileged; `mountFn` takes a `readOnly` arg.
- The server route for `/.cornus/v1/deploy/attach` is more specific than `/.cornus/v1/deploy/`, so Go 1.22 mux precedence wins.

### kubernetes path (sidecar 9P, no host-namespace mount)

The Pass-1 mechanism was single-host; on k8s the mountpoint would exist only on the server's node, so a pod scheduled elsewhere would get a hostPath to nothing, and there is no way to kernel-mount "before the container starts" from outside a pod. Hard requirements: support k8s, never mount on the node host, use a live sidecar 9P mount. Full writeup: `.agents/docs/K8S_LIVE_MOUNT_DESIGN.md`.

Per client-local mount, `deploymentWithMounts` injects into the pod template:

| Piece | Role |
|---|---|
| shared `emptyDir` | mount propagation medium |
| privileged native sidecar (`initContainer` + `restartPolicy: Always`, k8s >= 1.29; built against k8s.io v0.32) running `cornus mount-agent` | kernel-9p-mounts onto the emptyDir with `Bidirectional` propagation |
| sidecar `startupProbe` running `cornus mountcheck` (self-contained via `/proc/self/mountinfo`, no util-linux) | gates the app container until the mount is live — restores the "files present before the entrypoint" guarantee, the analogue of dockerhost's synchronous `mount(2)`-before-start |
| app-container volumeMount at the target | `HostToContainer` propagation, `ReadOnly` per the mount's flag |

Rendezvous/relay: `handleDeployAttach` registers the attach session by a random id; the sidecar dialed `GET /.cornus/v1/deploy/mount/{session}/{name}` and the server bridged it to a fresh `'L'` backing stream on the caller — the exact transport dockerhost's kernel mount uses, just terminating in the pod. (Later unified onto `/.cornus/v1/caretaker/attach`.)

Privilege is scoped to the sidecar (needs `Bidirectional` propagation + `mount(2)`), not the node host and not the app container.

Operator verdict: not needed for the core flow — Cornus creates the Deployment, so it injects the sidecar directly at Apply time; no CRD, mutating webhook, or reconcile loop. An operator would only add robustness (reconnect after a server restart, GC of orphaned mount-Deployments) — deferred.

Volume copy-up (managed volumes, not client-local mounts): a `cornus-volinit-<i>` initContainer
(`volumePopulateContainer`, `pkg/deploy/kubernetes/kubernetes.go:586-609`) seeds a PVC from the app
image's baked content at the target, mirroring Docker's copy-up, only when the PVC is still empty. It
mounts the PVC at a SCRATCH path, not the target, and runs from the app image — never mounting a
client-local bind — so it cannot become a massive remote-9P copy even when a client-local bind covers
the same parent path (e.g. both a bind and a managed volume nested under `/app`): the volinit
initContainer is neither source nor destination for the remote mount. It can still be slow for a
large already-populated volume (a local `cp -a`); an OTel span was requested but deferred, since the
copy runs out-of-process in an app-image initContainer — a span would have to be synthesized by the
server from the initContainer's `terminated.startedAt`/`finishedAt`, not emitted from a Go `cp` call.

### Sparse mount-name indexing (investigated, coverage gap remains)

A reported hang ("`cornus: error: mount m2 at /cornus/mount/2: connection reset by peer`") was traced
to an OLDER build where the client names each client-local mount `m<i>` by its index in the FULL
`spec.Mounts` list, SKIPPING non-local entries — so a non-local source interspersed among local binds
makes the local names sparse (e.g. `m0, m1, m3, m4`). A server/caretaker side that re-indexed mount
roles DENSELY would then ask the client for a name it never served (`m2`), and the client's
`serveOne9P` closes the stream on the unknown name — the caretaker's `Mount9P` sees this as
`connection reset by peer`. The current tree already avoids this: the mount `Name` is preserved
end to end (`Name: lm.Name` in `pkg/server/deploy_attach.go`; the kube backend uses `m.Name` for the
backing role in `pkg/deploy/kubernetes/kubernetes.go`), and the dense loop index only ever feeds the
k8s volume name and sidecar path, never the backing name — so both sides stay sparse-consistent.
`TestCaretakerAttachMultiplexesMounts` covers a 2-mount multiplex.

Important nuance confirmed by actually reproducing the report live on kind: a Compose anonymous
`type: volume` cannot trigger this bug at all, because Compose routes it into `spec.Volumes` (a PVC),
never into `spec.Mounts` — there is no sparse index to mismatch. The genuine trigger needs a
NON-LOCAL source INSIDE `spec.Mounts` (a named/bare-name volume interspersed among local binds, as
arrives via the raw `deploy`/docker `-v` path), which is not reachable via Compose. Coverage gap:
`e2e/scenarios/compose-mounts-multi.star` (below) guards the nested-multi-mount topology but is
NOT a sparse-index regression guard — no scenario yet drives the raw deploy/deploy-attach path with a
non-local source interleaved between local binds to actually re-trigger the sparse-index condition.

No hostPath, ever:

- `deployment()` does not generate `hostPath` volumes from `spec.Mounts`.
- Stateless `Apply` rejects any spec with bind mounts with an error pointing to `cornus deploy --server --local-mount` (or docker/compose via cornus daemon docker) — so `cornus compose up` fails loud on k8s instead of creating a broken node-local hostPath.
- `ApplyWithMounts` rejects any mount lacking a client-local 9P backing (would otherwise be silently dropped); `deploymentWithMounts` builds the base with no mounts at all.

### Read-write mounts

Initially read-only (the confined 9P export was read-only; `MountManager.Prepare` forced `ReadOnly=true`, the client rejected non-`:ro` mounts). Read-write closed the gap:

- `internal/buildwire/writablefs.go`: a writable confined 9P attacher mirroring `confinedfs.go`'s containment (shared `guard`: reject `..` via `validComponent`, deny walking through / opening escaping symlinks via `within`/`confinedFollow`) but embedding `templatefs.NoopFile` instead of `templatefs.ReadOnlyFile`, delegating Create/WriteAt/Mkdir/Symlink/Link/UnlinkAt/RenameAt/SetAttr/FSync to the inner `localfs` node after a policy check (create-family ops require a single non-traversing name under an already-confined parent). Writes stay jailed to the export root.
- Plumbing end to end: `Serve9PBacking` gained `writable map[string]bool` (build path passes nil = read-only); `serveOne9P` picks `writableConfinedAttacher` vs the counted read-only one; `deploywire.Serve` builds the writable set from `spec.LocalMounts[].ReadOnly`; `MountManager` kernel-mounts rw when not ro; `client.DeployAttach` forwards each mount's `ReadOnly`; the k8s app-container volumeMount uses `m.ReadOnly`; the mount-agent already honored `--read-only`.

### Single-file sources

Compose file-backed `configs:` and `secrets:` become bind mounts whose source is one file. Passing
that source directly to kernel 9P fails with `ENOTDIR`, because the exported mount root must be a
directory. `Client.DeployAttach` stats every local source. A directory is exported unchanged; a file
exports `filepath.Dir(source)` and sends `filepath.Base(source)` as `LocalMount.Subpath`.
`MountManager.Prepare` mounts the directory and rewrites only the runtime source to the file below
the mountpoint.

The Kubernetes sidecar realization cannot mount one file at an arbitrary rootfs target through its
shared `emptyDir` propagation design. `rejectFileMounts` therefore fails fast on sidecar/attachment
paths instead of silently presenting a directory. Safe same-host direct bind detection remains a
separate follow-up; until then, kube scenarios for file-backed configs/secrets skip with the explicit
limitation.

### Mount relay via a caretaker companion (dockerhost/containerdhost remote mode)

Client-local bind mounts on dockerhost/containerdhost had always kernel-9p-mounted on the SERVER's
own host (`applyWithHostMounts` in `pkg/server/deploy_attach.go`) — correct only when the server is
co-located with the daemon it drives. An opt-in path now mirrors kubernetes' sidecar approach with
plain Docker/containerd primitives instead of a pod:

- dockerhost's `ApplyWithMounts` (`pkg/deploy/dockerhost/mounts.go`, implementing
  `deploy.MountingBackend`, the same interface kubernetes already implements) creates one
  Docker-managed volume PER (replica, mount) pair — required, since sharing one volume's source path
  across replicas would let a mount event from one replica's caretaker propagate into a different
  replica's app container. It inspects each volume's daemon-host `Mountpoint` (new
  `engineClient.volumeInspect`) and binds that same path twice: into the app container with `rslave`
  propagation, and into a dedicated `Privileged` `cornus-<name>-mount-<i>` companion container with
  `rshared` — the caretaker's own kernel 9P mount inside its `rshared` view propagates into the app
  container's `rslave` view. This is the same standard Linux shared-subtree mechanism Kubernetes'
  `HostToContainer`/`Bidirectional` propagation uses, realized with plain Docker primitives. The
  cornus SERVER itself never opens the volume's host path — only Engine API calls are needed, which
  already work against a non-co-located daemon (the same thing `DOCKER_HOST=tcp://...` support
  already proved). Unlike the pre-existing egress companion (which shares the app instance's netns),
  the mount-only companion carries no `NetworkMode` override at all — it only needs outbound
  reachability to the server, not to intercept or share the app's network stack.
- containerdhost's equivalent (`pkg/deploy/containerdhost/mounts_linux.go`) is the same trick via OCI
  mount `Options` (`rshared`/`rslave` directly on the mount spec, a plain host directory under
  `<DataDir>/containerd/caretaker-mounts/...`, no volume abstraction needed) and a companion task
  running in the HOST's own network namespace (`withoutNamespace(specs.NetworkNamespace)`) rather than
  a CNI attachment, since it only needs outbound reachability and host networking is simpler than a
  throwaway CNI attachment for a role that never receives inbound traffic. This does **not** achieve
  true remote-daemon support for containerd:
  containerd's vendored client dialer is hard-coded to a local unix socket on Linux, so this backend
  stays unconditionally co-located with the server regardless of the flag — the sidecar mechanism is
  still worth having, since it avoids the server needing kernel-mount privilege itself.
- Gating: a `deploy.RemoteCapable` marker interface (`Remote() bool`), read once at backend
  construction (`CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` in `pkg/server/server.go`). Without
  this, the unconditional type-assertion `backend.(deploy.MountingBackend)` in the mounts-dispatch
  branch would always succeed the moment dockerhost/containerdhost implemented it, silently stealing
  every client-local-mount deploy away from the existing, working `applyWithHostMounts` fast path —
  daemon co-location cannot be auto-detected. A backend that doesn't implement `RemoteCapable` at all
  (kubernetes, which has no host-mount fallback to protect) is treated as always eligible.
- E2E: `e2e/scenarios/deploy-mounts-sidecar-docker.star` / `deploy-mounts-sidecar-containerd.star`
  (kube-only pattern adapted for the two host backends); needed `pkg/e2e/target.go`'s
  `DockerTarget`/`ContainerdTarget` to gain an `AdvertiseHost()` method (`DockerTarget` discovers the
  default bridge network's gateway; `ContainerdTarget` returns `127.0.0.1` since its companion uses
  host networking). Both self-skip without `CORNUS_AGENT_IMAGE` and are `EXTRA_CHECK_SCENARIOS`-only
  (need privileged Docker/containerd plus a prebuilt agent image).

This mount-only companion was later superseded/extended by a unified always-on remote companion that
also shares the app instance's network namespace unconditionally in remote mode and gains
port-forward and ssh-agent-relay roles — see [[remote-companion-and-agent-forwarding]] for that
fuller evolution; this document covers only the mount-relay mechanism itself.

### Metrics

`wire.MeteredConn` (`internal/wire/metered.go`) wraps a `net.Conn` with `OnRead`/`OnWrite` byte-count callbacks — callback-based so `internal/wire` and `internal/deploywire` stay OTel-free (verified with `go list -deps`); light client binaries don't link OTel. It embeds the `net.Conn` interface (not a concrete type) so `io.Copy` in `pipe` cannot shortcut via `ReadFrom`/`WriteTo`.

Counters: `direction=rx` = bytes into the container, `tx` = bytes out, keyed by mount `name` (bounded cardinality: one series per mount x direction per process). Wrapped data paths:

- caretaker `runMountStream` (pod) → `caretaker.mount.io.bytes` via `meterMountStream`
- server `relayMountMuxed` on `/.cornus/v1/caretaker/attach` → `cornus.mount.io.bytes` via `s.meterMountConn`
- server dockerhost deploy-attach → `cornus.mount.io.bytes` via `MountManager.SetMeter(s.mountMeter)` → `wire.Backing9PSocketMetered` (plain `Backing9PSocket` delegates with nil hooks; buildwire callers unaffected). Topology (accepted conn = container side) is encoded in `tunnel9P` so callers only think in rx/tx.

Instruments live in the server `instruments` struct and the caretaker `ctMetrics`, built from the global meter (no-op and zero-cost when telemetry is off). Deferred: per-file RX/TX (high cardinality), hub-egress bytes (`hub.go` `wire.Pipe`), build-context 9P bytes (`DirServer.ReadBytes`).

### CLI

`cmd/cornus/commands.go`: `cornus deploy --server <url> --local-mount SRC:DST[:ro]` runs foreground with streaming and a Ctrl-C graceful `down`. `cmd/cornus/mountagent.go` adds the `mount-agent` and `mountcheck` subcommands. `CORNUS_AGENT_IMAGE` defaults to the app image because the Cornus image's ENTRYPOINT is `cornus`, so the sidecar just sets `Args: ["mount-agent", ...]`.

### Known constraints / deferred

- Single-replica attach/relay server: the session registry is in-memory in one process; a sidecar relay landing on another replica would not find the session. A shared/routed registry (possibly operator-owned) is future work.
- Privileged sidecar required for `Bidirectional` propagation.
- Deferred Pass 2: `cmd/cornus/daemon.go` + `internal/dockerproxy`, a Docker Engine API subset so stock `docker`/`docker compose` (via `DOCKER_HOST`) drive this transport.

## Files

- `internal/deploywire/` — `spec.go`, `serve.go`, `attach.go`, `backing.go`, `backing_linux.go`, `backing_other.go`, `preflight.go`, `preflight_linux.go`, `preflight_other.go`; exports `Mount9P`/`Unmount9P`, `ServerSession.AllowsMount`
- `internal/server/deploy_attach.go` — attach handler + backend dispatch (sidecar vs host vs error)
- `internal/server/mount_relay.go` — session registry + mount relay (originally `/.cornus/v1/deploy/mount/`, later unified onto `/.cornus/v1/caretaker/attach`)
- `internal/server/server.go` — `/.cornus/v1/deploy/attach` route
- `internal/buildwire/export.go` — buildwire wrappers (`Dial`, `Accept`, `OpenTagged`, `AcceptTagged`, `TagControl`, `OpenBacking`, `Pipe`, `AcceptConn`, `DialConn`)
- `internal/buildwire/writablefs.go` — writable confined 9P attacher
- `internal/wire/metered.go` — `MeteredConn`, `Backing9PSocketMetered`
- `internal/deploy/deploy.go` — `MountingBackend`, `AttachMount`
- `internal/deploy/kubernetes/` — `ApplyWithMounts`, `deploymentWithMounts`, shared `applyDeployment`, bind-mount rejection in `Apply`
- `internal/client/client.go` — `DeployAttach`, host-path classification, `wsAttachURL(base, path)`
- `internal/config/config.go` — `MountsDir`
- `cmd/cornus/commands.go` — `--server` / `--local-mount` flags
- `cmd/cornus/mountagent.go` — `mount-agent` + `mountcheck` subcommands
- `e2e/scenarios/deploy-mounts.star` + `internal/e2e` harness (`deploy_attach`, `attach_stop`, `pod_exec`)
- `.agents/docs/K8S_LIVE_MOUNT_DESIGN.md` — full k8s design writeup
- `pkg/deploy/dockerhost/mounts.go` — dockerhost `ApplyWithMounts`, per-(replica,mount) volume,
  `engineClient.volumeInspect`, mount-only companion container
- `pkg/deploy/containerdhost/mounts_linux.go` — containerdhost equivalent (OCI mount `Options`,
  host-netns companion task)
- `pkg/deploy/deploy.go` — `RemoteCapable` marker interface (`Remote() bool`)
- `pkg/e2e/target.go` — `DockerTarget`/`ContainerdTarget.AdvertiseHost()`
- `e2e/scenarios/deploy-mounts-sidecar-docker.star`, `deploy-mounts-sidecar-containerd.star`,
  `compose-mounts-multi.star` (+ fixtures), `deploy-mounts-multi.star`
- `pkg/caretaker/caretaker.go` — mount-lifecycle info logging (`Run`, `runCaretakerConn`,
  `runMountStream`)

## Test Coverage

All hermetic (no cluster, root, or network) unless noted:

- `MountManagerRewrite` — fake mount; only the local mount is rewritten, caller spec unmutated
- server `DeployAttachLifecycle` — real handler + fakeBackend over WebSocket: apply observed, disconnect tears down, mux routing of `attach` proven; `DeployAttachNotTreatedAsName`; fakeBackend is mutex-safe (attach handler runs concurrently, `-race` covered)
- root-gated `MountManagerKernelMount` — real `unix.Mount`, reads a file back through the mountpoint
- `TestApplyWithMountsInjectsSidecar` — fake clientset; asserts emptyDir, privileged native sidecar with Bidirectional + startupProbe + relay args, app `HostToContainer` mount, no hostPath
- `TestApplyRejectsBindMounts` — bind-mount spec rejected, no Deployment created; `TestApplyCreatesDeploymentAndService` expects no hostPath volumeMount
- `TestMountRelayServesCallerExport` — in-process: fake MountingBackend registers the session, test plays the pod's mount-agent, real p9 client over the relay reads the caller's file back (registry + relay + confined export, no root); `TestMountRelayUnknownSession`
- `TestWritableConfinedWritesStayInRoot` / `TestWritableConfinedRejectsEscape` — raw-p9-client harness; `Create("..")`, `Walk("..")`, escaping-symlink open for write all denied
- `TestMountManagerFileSubpath` proves parent-directory mounting plus file-source rewrite;
  `TestRejectFileMounts` proves the Kubernetes guard.
- Metrics: `internal/wire` MeteredConn count + nil-passthrough; `internal/server` `TestMountBytesMetered` (full `/.cornus/v1/caretaker/attach` 9P read through a `ManualReader`, asserts `cornus.mount.io.bytes{direction=rx}` >= payload); `internal/caretaker` `TestMeterMountStreamCounts`
- Kube E2E on kind: `e2e/scenarios/deploy-mounts.star` (kube-gated; parse-covered in `go test` via `TestScenariosParse`/`TestPredeclaredNamesInSync`) — deploys the Cornus image itself as the app (image doubles as mount-agent image), `pod_exec cat /data/marker` asserts ro content, and the rw case verifies a pod `printf > /data/frompod` propagates back to the caller's dir. Passed on a real kind cluster; the in-pod kernel mount + native-sidecar ordering can only be verified there.
- `e2e/scenarios/compose-mounts-multi.star` — nested multi-mount guard: four client-local binds with
  NESTED targets under `/app`, an interspersed anonymous `type: volume`, mixed ro/rw; fails closed if
  any mount stalls, reads each nested marker back. Verified GREEN on real kind. This is a
  nested-multi-mount guard, NOT a sparse-index guard (see "Sparse mount-name indexing" above) —
  `e2e/scenarios/deploy-mounts-multi.star` is the deploy-attach-tier companion (kube-only, written but
  not yet run live).
- `e2e/scenarios/deploy-mounts-sidecar-docker.star` / `deploy-mounts-sidecar-containerd.star` — the
  companion-mount path on dockerhost/containerdhost, `EXTRA_CHECK_SCENARIOS`-only (need
  `CORNUS_AGENT_IMAGE` plus privileged Docker/containerd; self-skip otherwise). The docker variant has
  been run live; the containerd variant is check-parsed only in this environment.

## Pitfalls

- Never assume IPv4. The kind docker network is dual-stack; picking `(index .IPAM.Config 0).Gateway` returned the IPv6 gateway, producing an unbracketed `ws://fc00:...::1:port` URL against an IPv4-only bind → sidecar CrashLoopBackOff with "network is unreachable". Fix is family-agnostic: bind `:port` (all interfaces, both families), build URLs with `net.JoinHostPort` (brackets IPv6), prefer the IPv4 gateway but fall back to any. Operators must bracket IPv6 in `CORNUS_ADVERTISE_URL`.
- Old docker CLI vs new daemon: debian's docker.io client (API 1.41) is rejected by daemon v29 (min API 1.44); the E2E tooling container uses a downloaded static docker CLI (27.5.1).
- hostPath is never a valid realization of a bind mount on kubernetes: it is node-local, non-portable, unsafe, and "works" until multi-node scheduling breaks it silently. Reject loudly instead.
- Preflight cannot detect the mount-propagation trap: if the Cornus server runs in a private mount namespace, kernel mounts under `<DataDir>/mounts` are invisible to the container runtime. Run in the host mount ns or bind the mounts dir `rshared`.
- dockerhost teardown must remove containers before unmounting (the bind holds the mount busy); use `MNT_DETACH` fallback on EBUSY.
- The deploy lives only as long as the client's WebSocket — this is by design (dev/inner-loop), not a bug; a container bound to a dead 9P mount only gets EIO, so the server proactively tears down on disconnect.
- The E2E's host-server/kind-gateway topology does not exercise the real in-cluster path (pods reaching the server via Service DNS).
- Historical naming: the per-mount `/.cornus/v1/deploy/mount/{session}/{name}` relay and the plan-era `handleMountRelay`/`relayMountStream` names were superseded by the unified `/.cornus/v1/caretaker/attach` transport.
- A file cannot be the root of a kernel 9P mount. Export its parent and use `Subpath`; do not weaken
  the Kubernetes guard until the backend can realize an arbitrary file target without `hostPath`.
- A dockerhost/containerdhost anonymous `type: volume` in Compose can never reproduce the sparse
  mount-name bug (it routes into `spec.Volumes`, not `spec.Mounts`); don't mistake a green
  `compose-mounts-multi.star` run for coverage of the sparse-index path — that needs a raw
  deploy/deploy-attach spec with a non-local source interleaved among local binds.
- Adding a `Remote() bool` method to a widely-embedded test fake (rather than a dedicated wrapper)
  can silently flip unrelated pre-existing tests' backend-dispatch decisions via Go method
  promotion — mirror the existing `fakeRemoteMountingBackend` precedent (a dedicated wrapper) instead
  of adding the method to a shared fake.
