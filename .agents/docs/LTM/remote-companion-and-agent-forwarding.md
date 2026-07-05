# Remote companion, port-forward rerouting, and SSH-agent forwarding

## Summary

Cornus's remote-mode host backends (dockerhost, containerdhost) run one always-on, privileged
"companion" container/task per app instance, sharing the app's network namespace, whenever
`CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` is set. The companion started as a mount-only
helper (see [[client-local-mounts-deploy]] for that baseline) and was generalized this arc into a
unified substrate for three capabilities that all need a live presence inside (or beside) the
workload's network/filesystem namespace: client-local mounts, `cornus port-forward`/`cornus tunnel`
reaching an unpublished port, and `cornus exec --forward-agent` relaying a caller's local
`ssh-agent` into a running container. Kubernetes reached the same SSH-agent-forwarding capability
through a materially different mechanism: a per-deployment opt-in (`DeploySpec.AgentForward`)
folded into whichever caretaker sidecar a workload already has, rather than an always-on companion.

## Key Facts

- `pkg/remotecompanion` is a new neutral registry package (`Registry.Put/Get/Remove`, keyed by
  `InstanceKey(name, replica) = "name/replica"`) that breaks an import cycle: `pkg/server` needs to
  look up "the live caretaker connection for instance X" but that registry is populated by the
  server's own HTTP handler, and both deploy backends already import `pkg/server`-adjacent code.
- The caretaker declares its instance via a new `?instance=` query parameter on the existing
  `GET /.cornus/v1/caretaker/attach` dial (`Config.Instance`) — deliberately not a new control-stream
  message, since the control stream's `hub.Registration`/`hub.CatalogUpdate` pair is hardcoded, not a
  generic envelope.
- `PortForwardRole` is the first caretaker capability where the **server** opens the stream (every
  prior role — mount, credential, egress — is caretaker-initiated).
- `AgentRelayRole` forwards a caller's local `ssh-agent` into a running container so a process
  inside it can use `$SSH_AUTH_SOCK` — this is a **different** mechanism from `cornus tunnel
  --forward-agent` (see [[public-tunnels]]), which forwards the caller's agent to the server
  process itself for the ssh tunnel backend's own outbound handshake.
- dockerhost/containerdhost gate all of this behind one backend-wide flag
  (`CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`) and one always-on companion per instance;
  kubernetes instead gates SSH-agent forwarding per-deployment via `DeploySpec.AgentForward`
  (`x-cornus-agent-forward` in Compose), folded into whatever caretaker sidecar the deployment
  already needs (or a minimal standalone one if it needs none otherwise).
- `pkg/supervisor` (promoted from `cmd/cornus/internal/supervisor`) is now the shared restart-tree
  primitive for both the server's periodic GC loop and every per-connection/per-role unit inside
  `pkg/caretaker` — a caretaker's mount/credential/egress/hub roles are now independently
  restartable instead of sharing one all-or-nothing errgroup.

## Details

### `pkg/supervisor` promotion and adoption

`pkg/caretaker` originally had **no** internal restart facility: any role erroring, or the
connection dropping, made the whole `cornus caretaker` process exit non-zero, relying entirely on
Kubernetes/a container restart policy to notice and restart it, with no in-process state preserved.
The actual reusable facility — a small, already-generic in-process supervisor tree (`Service`/
`ServiceFunc`, a `Policy` of `RemoveOnExit` or `Restart` with capped exponential backoff, per-child
panic recovery) — existed only under `cmd/cornus/internal/supervisor`, used solely by the CLI's
client-agent daemon, and being under an `internal/` path was not importable from `pkg/server` at
all. It was moved to `pkg/supervisor` (pure relocation: `git mv` + import-path updates at the three
existing call sites, zero API changes).

**Server adoption.** The periodic GC loop (`pkg/server/gcschedule.go`) is now a supervised
`AddSystem(..., Restart)` child of a new `Server.sup *supervisor.Supervisor`, constructed in `New`
(not `Run`) specifically so tests that build a bare `Server{}` without calling `Run` still get a
working `sup`; `supCancel`/`sup.Wait()` in `closeResources` replace the old dedicated
`gcStop`/`gcStopOnce`/`gcDone` channel trio. Bug caught during this: `gcRunning` (the no-overlap
guard) was reset to `false` via a plain statement *after* the GC run returned, not a `defer` — fine
under the old bare-goroutine model (a panic crashed the whole process anyway) but under
`supervisor.Restart` a panic mid-run is recovered and the loop relaunched fresh, so `gcRunning`
would have stayed `true` forever after any panic, permanently wedging every future tick. Fixed with
`defer s.gcRunning.Store(false)` in the extracted `runGCTick` helper, caught by writing
`TestPeriodicGCSupervisedAcrossPanic` before it could ship as a live bug.

**Caretaker adoption**, per explicit follow-up direction ("bring the supervisor to caretaker too, as
it should handle multiple miniservices in a single process"). `Run` now builds one top-level
`supervisor.Supervisor`, registering each server connection (`runCaretakerConn`) and each of
proxy/DNS/docker as an independently-restarting child — a connection to one server dying no longer
takes down a pod's connection to a *different* server or its other roles. Inside
`runCaretakerConn`, a connection-scoped `connSup` registers each mount/credential/egress/hub-bundle
role as its own supervised child: a role erroring reopens its own tagged stream over the *same*
still-live session, without disturbing siblings — previously all roles on one connection shared a
single errgroup, so any one role's error tore down every other role on that connection too. Session
death is detected via `*yamux.Session.CloseChan()` (present in the vendored
`hashicorp/yamux@v0.1.2/session.go`) in a `select` against `ctx.Done()`, so ordinary pod teardown
(context cancelled) returns cleanly with no restart, while a genuine connection drop returns an
error that makes the *outer* supervisor redial with backoff. `runHubRoleBundle` groups the hub
sub-roles (ingress delivery, reach listeners, dynamic-reach watch) as one supervised unit rather
than one child per listener — a deliberate, documented scoping choice.

A second latent bug, in the caretaker restructuring itself: the first version of
`runHubRoleBundle`, for a hub role with only `Register` set (no `Reach`, no ingress-delivery
targets, no dynamic reach — a normal, common configuration), built an errgroup with **zero**
goroutines. `g.Wait()` on an empty errgroup returns immediately, so the bundle "completed" a few
microseconds after starting and was busy-restarted forever at increasing backoff — functionally
harmless (registration is a one-shot action that already happened before the bundle starts) but
wasteful and noisy. `TestCaretakerRoleIsolation` (`pkg/server/caretaker_supervisor_test.go`)
surfaced this via the supervisor's own restart log lines. Fixed by adding a permanent
`g.Go(func() error { <-gctx.Done(); return nil })` placeholder to the bundle, mirroring what the old
connection-wide "hold the connection open even with no streams" goroutine used to do, rescoped to
the hub bundle specifically.

**Reusable testing pattern.** Root/privileged operations (an actual kernel 9P mount) can't run in
this environment, so the caretaker isolation tests deliberately used **credential**-role failure (a
`CredentialRole` with `Deliver: [{Kind: "file", ...}]` pointing at a `Name` with no registered
source on the server — an unknown session just closes the stream, a fast deterministic failure) as
the "broken sibling role" instead of a mount role, proven alongside a real working hub
`Register`+echo round-trip on the *same* connection. General lesson: pick the cheapest
reliably-failing role for a fault-injection test rather than the one the scenario is nominally
"about".

### Always-on unified remote companion (dockerhost/containerdhost)

The per-instance companion — previously created only when a deploy declared `--mount`, and never
sharing the app's netns (see [[client-local-mounts-deploy]] for that baseline) — is now **unified
and always-on** whenever `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` is set, regardless of
`--mount`, and always shares the app instance's network namespace (`NetworkMode: container:<app>`
on dockerhost; joins the pinned netns on containerd, replacing the old
`withoutNamespace(NetworkNamespace)`/host-netns choice). This costs one extra always-running
privileged sidecar per instance in remote mode even for mount-less deployments, in exchange for
`ForwardPort`/tunnel/exec-agent-forwarding all working uniformly through one mechanism. The design
was confirmed via `AskUserQuestion` before implementation. Kubernetes' `ForwardPort` needed no
change (it already routes through the Kubernetes API's `pods/portforward`, with no co-location
problem); wiring `AgentRelayRole` into kubernetes' own caretaker was explicitly scoped out of this
step and closed separately (see "Kubernetes `AgentForward`" below).

`pkg/remotecompanion.Registry` is the neutral lookup package described in Key Facts, imported by
`pkg/server` and both host deploy backends. `s.remoteCompanions` (this instance's caretaker mux,
read by `ForwardPort`) and `s.execAgentChannels` (this instance's currently-registered `cornus exec
--forward-agent` client channel, read by `relayAgentMuxed`) are two independent `Registry`
instances on `Server`.

**`PortForwardRole`.** Every prior caretaker role is caretaker-initiated (the caretaker calls
`OpenStream`, the server accepts); port-forward reroutes the other way — an external `cornus
port-forward`/`cornus tunnel` caller arrives at the server, which must reach into the companion's
shared netns. Since yamux sessions are fully bidirectional regardless of which side dialed the
underlying transport, the server calls `wire.OpenPortForward(mux, port, proto)` (new tag `'F'`,
writes `port\nproto\n`) on the registered mux; the caretaker runs a new accept loop it never had
before (`runPortForwardAccept`, `wire.AcceptTagged(sess)` in a loop) to receive it, dial
`127.0.0.1:port` (reachable because of the shared netns), and splice. `dockerhost`/
`containerdhost`'s `ForwardPort` branches on `b.remote`: unchanged direct-dial when false,
`forwardPortViaCompanion` (registry lookup + `wire.OpenPortForward` + splice) when true.

**`AgentRelayRole`.** Caretaker-initiated, like egress. Listens on a fixed unix socket path
(`remotecompanion.AgentSocketPath`, inside `remotecompanion.AgentScratchDir`) and, per accepted
local connection (a process inside the app container touching `$SSH_AUTH_SOCK`), opens a
`TagAgentRelay` stream (new tag `'A'`) to the server with no header — the server already knows the
instance from which connection this arrived on. `relayAgentMuxed` looks up
`s.execAgentChannels.Get(instance)`; if nothing is registered it closes the stream immediately (the
same fast-fail-on-unregistered pattern as credential relay — the correct failure mode, matching
what real `ssh -A` does when nothing is forwarding). `cornus exec --forward-agent`/`cornus compose
exec --forward-agent` open a new endpoint, `GET /.cornus/v1/deploy/{name}/exec-agent-channel`, as a
yamux **client** session (`cl.ExecAgentChannel`) before the exec starts, registering it under
`instance="name/0"` (exec always targets the first instance); the server accepts it as a yamux
**server** session (`wire.Accept`) and blocks on `<-sess.CloseChan()` until the CLI disconnects.
Each relayed stream on the client side gets a fresh dial of the real local agent (`sshagent.Dial`,
extracted from `cmd/cornus/tunnel.go`'s `dialLocalAgent` into a new shared
`cmd/cornus/internal/sshagent` package used by both `tunnel.go` and the new exec code) and
`wire.Pipe`-spliced. `ExecConfig` gained `ForwardAgent bool`; the server injects
`SSH_AUTH_SOCK=<AgentSocketPath>` into the exec's `Env` when set and rejects it outright (before any
caretaker involvement) unless the backend is `deploy.RemoteCapable` and `Remote()`.

### Kubernetes `AgentForward` (a different gating model)

Unlike the host backends' single unconditional always-on companion, kubernetes assembles its
caretaker `Config` from several call sites (`injectHub`, `injectDNS`, `injectDocker`,
`deploymentWithAttachments`'s mounts/credentials/egress path) gated on which specific features a
workload requests — a plain deployment with none of those gets **no** caretaker sidecar at all.
Making the sidecar always-on for every kubernetes deployment just to support `--forward-agent` would
be a materially more expensive default than the host backends' story, so kubernetes instead got a
new, independent, per-deployment opt-in toggle.

`DeploySpec.AgentForward bool` (`pkg/api/deploy.go`) — a plain top-level toggle, matching
`Privileged`/`ReadOnly`'s convention rather than a nested spec struct, since there is nothing to
configure beyond on/off. A new `addAgentForwardRole` helper (`pkg/deploy/kubernetes/kubernetes.go`)
folds an `AgentRelayRole` into whichever caretaker `cfg` the caller already has in hand: it sets
`cfg.Instance = remotecompanion.InstanceKey(spec.Name, 0)` and `cfg.AgentRelay`, plus a shared
`emptyDir` (`agentSocketVolume`) mounted at the fixed `remotecompanion.AgentScratchDir` path into
both the app container and the caretaker (the kubernetes analogue of the host backends'
`rshared`/`rslave` propagated bind for the same fixed path — a plain shared `emptyDir` suffices
since native sidecar containers already share nothing else needing the propagation trick). Called
from all four existing caretaker-assembly sites (`injectDNS`, `injectHub`, `injectDocker`,
`deploymentWithAttachments`) so `AgentForward` folds into whatever caretaker those already create,
plus a new fifth path (`injectAgentForward`, modeled on `injectDocker`'s "standalone, unprivileged,
no special sidecar needs" shape) for when `AgentForward` is the *only* reason a caretaker exists —
wired as the fourth branch of `deployment()`'s Hub/DNS/Docker/(now)AgentForward dispatch chain.
`base.AgentForward = false` was added to `deploymentWithAttachments`'s existing list of fields
cleared on its inner `b.deployment(ctx, base)` call, so the inner call does not also try to inject a
second, redundant caretaker.

Compose exposure: `Service.AgentForward bool` (`pkg/compose/types.go`, tag
`x-cornus-agent-forward`) — a plain bool extension mirroring `Privileged`'s precedent (not
`Egress`/`Ingress`'s struct-wrapping pattern, since there is nothing to configure and no
project-level default to inherit); `translateService` copies it straight onto `spec.AgentForward`;
added to `supportedServiceFields`.

**Bug caught while wiring this: `caretakerConfigEnv`'s `serverBound` gate was missing the two
newest server-bound roles.** `caretakerConfigEnv` decides whether to stamp `cfg.Token` (or a
`CORNUS_TOKEN` secretKeyRef) onto a caretaker's env based on a `serverBound` bool — originally
`len(Mounts) > 0 || len(Credentials) > 0 || Hub != nil || Egress != nil`. `PortForward`/`AgentRelay`
dial the exact same authenticated `/.cornus/v1/caretaker/attach` endpoint (confirmed via
`runCaretakerConn`/`groupByServer` in `pkg/caretaker/caretaker.go`, which already bucket them like
the other server-bound roles) but had never been added to this gate — harmless before this session
since kubernetes never set either field, but a real bug the instant `addAgentForwardRole` started
setting `cfg.AgentRelay` on kubernetes: with server auth enabled, an agent-forward-only caretaker
would have dialed the attach endpoint with no token and been rejected, silently breaking the
feature it exists for. Fixed by adding `cfg.PortForward != nil || cfg.AgentRelay != nil` to the
`serverBound` condition (the `PortForward` half is currently a no-op on kubernetes, which never sets
that field, but fixing both together pre-empts the identical bug the day kubernetes' `ForwardPort`
ever gets rerouted the same way). Caught by re-reading `caretakerConfigEnv` line by line, not by a
failing test — the existing tests only exercised mounts/hub/dns/proxy configs.

**Server-side gating: a new `deploy.AgentForwardCapable` interface, checked alongside
`RemoteCapable`.** `handleDeployExecCreate`/`handleDeployExecAgentChannel`
(`pkg/server/deploy_exec.go`) previously hard-gated `ForwardAgent`/the exec-agent-channel dial on
`backend.(deploy.RemoteCapable) && Remote()` alone — correct for the host backends' backend-wide
mode, wrong for kubernetes' per-deployment toggle, since the server only has the backend and a
deployment name in hand, not the `DeploySpec` that was originally applied. Rather than threading the
applied spec through generically (no existing mechanism does that across backends), kubernetes
answers the question itself: a new optional `deploy.AgentForwardCapable` interface
(`pkg/deploy/deploy.go`) with one method, `AgentForwardEnabled(ctx, name) (bool, error)`.
Kubernetes's implementation reads a `cornus.dev/agent-forward` annotation stamped onto the
Deployment object in `deployment()` (set from `spec.AgentForward` on every `Apply`, following the
`cornus.dev/replicas`/`cornus.dev/restartedAt` annotation-naming convention) rather than decoding
the caretaker's own config JSON, which would need a live pod plus a redundant JSON unmarshal. A
shared `agentForwardAllowed(ctx, backend, name)` helper in `deploy_exec.go` tries `RemoteCapable`
first, then falls back to `AgentForwardCapable`, so either backend family's gate is satisfied by one
call site. dockerhost/containerdhost implement neither interface differently than before (still
only `RemoteCapable`) — additive, byte-for-byte unchanged behavior for them.

Checked, not a bug: `applyDeployment` does a full object `Update` (not a merge patch), so the
annotations set in `deployment()` on every `Apply` — including the new `cornus.dev/agent-forward`
one — fully replace whatever annotations previously lived on the Deployment, same as before this
change. `cornus.dev/replicas`/`cornus.dev/restartedAt` are written later via `updateDeployment`'s
Get-then-mutate-then-Update against the *current* live object (`Stop`/`Start`/`Restart`), not
through `deployment()`'s from-scratch construction, so a subsequent `Apply` call already wiped
those before this change too — a pre-existing quirk, out of scope here.

### Five bugs found across this arc

1. **`firstInstanceID`/`firstInstance` never filtered out companion containers.** A pre-existing
   latent bug (present since the egress companion was introduced), low-probability before
   (companions were opt-in-rare) but load-bearing the moment companions became always-on in remote
   mode: `Logs`/`Attach`/`ExecCreate`/`Stats`/`ForwardPort` all resolve "the first instance" through
   this helper, so a companion sorted first by the daemon would silently hijack every one of those
   operations. Fixed by skipping `isCompanion` containers in both backends' equivalents.
2. **`deploy.Bridge`'s half-close silently no-ops on a yamux stream.** `Bridge`'s "client stdin EOF
   -> half-close the remote write side" branch type-asserts for a `CloseWrite() error` method, which
   `*yamux.Stream` does not implement. Using `Bridge` for the companion-relay path would have meant
   the relay stream never tore down when the external caller closed their end, leaking it until the
   companion's own upstream connection happened to die for an unrelated reason. Caught by a
   self-written unit test (`TestForwardPortViaCompanion`) that closed the caller side and asserted
   `ForwardPort` returned — it hung instead. Fixed by widening `wire.Pipe`'s parameter type from
   `net.Conn` to `io.ReadWriteCloser` (backward-compatible; every existing caller already passes a
   `net.Conn`) and using it instead of `Bridge` for the companion-relay path — a port-forward tunnel
   has no stdin/stdout asymmetry to preserve, so the plain symmetric "close both on either end"
   semantics is the semantically correct choice here, not just a workaround.
3. **`PortForwardRole`'s accept loop had no way to unblock on ordinary shutdown.**
   `runPortForwardAccept` blocks in `sess.AcceptStream()` on the shared pod-scoped session, which
   `runCaretakerConn`'s existing `defer sess.Close()` doesn't reach until after `connSup.Wait()`
   returns, which itself waits for `runPortForwardAccept` to return — a deadlock on ordinary pod
   teardown. Found while writing this role's own unit test. Fixed by adding
   `go func() { <-ctx.Done(); sess.Close() }()` in `runCaretakerConn` right after the dial succeeds,
   instead of relying on the deferred close — safe because `yamux.Session.Close()` is idempotent,
   and review confirmed no other role touches `sess`/its own opened streams after `<-ctx.Done()`
   fires.
4. **The agent-relay socket only existed inside the app container when the deploy also used
   `--mount`.** Root cause: the companion's per-replica scratch volume (the shared-propagation
   mechanism) was, at first, only ever provisioned by `ApplyWithMounts` for actual `--mount` roles —
   a plain `Apply()` (remote mode, no mounts) left `cm.binds` empty, so `AgentRelayRole`'s listening
   socket was created inside a filesystem region nothing shared with the app at all. Discovered
   because this sandbox had a working, privileged-capable Docker daemon and the new scenarios were
   actually run (not just syntax-checked) — `deploy-remote-exec-agent-docker.star` failed on first
   real run with "Error connecting to agent: No such file or directory". Fixed by giving the
   agent-relay socket its own dedicated per-replica scratch volume/host-dir
   (`remotecompanion.AgentScratchDir`, a Docker volume on dockerhost / a host directory on
   containerd, `rshared` in the companion / `rslave` in the app), provisioned unconditionally in
   `apply()`'s tail whenever `b.remote`, independent of any `--mount` volumes — and since Docker
   mounts can't be added to an already-created container, this had to move *before* the per-replica
   create loop (previously mounts only needed to be ready before the companion was created, which
   happens after). This is a concrete argument for actually running E2E scenarios against a real
   daemon when the environment allows it, not only syntax-checking them: three bugs were caught by
   code review/unit tests before any scenario ran, but this fourth one was only visible by executing
   the real companion/socket/propagation chain end to end — no unit test exercised the "no `--mount`
   at all" + "agent-relay must still work" combination together.
5. **Test-suite-only bug: a shared test fake's embedders are effectively part of its public
   contract.** Adding a `Remote() bool` method directly to the widely-embedded `fakeBackend` (for a
   new exec-agent-channel test's convenience) silently made `fakeMountingBackend` — which embeds
   `fakeBackend` and is used by several pre-existing, unrelated mount-relay tests — implement
   `deploy.RemoteCapable` via Go's method promotion, flipping `useSidecarMounts`'s dispatch decision
   for those tests from "sidecar path" (no `RemoteCapable` at all -> defaults true) to "co-located
   path" (`RemoteCapable` with `Remote()==false`), silently breaking `TestMountBytesMetered`/
   `TestMountRelayLocalFastPath` (both started timing out, only visible running the full
   `pkg/server` package rather than the new tests in isolation). Fixed by reverting that addition
   and using a dedicated wrapper (`fakeRemoteBackend`, embeds `fakeBackend` + its own `Remote()`)
   instead — mirroring the existing `fakeRemoteMountingBackend` precedent in
   `deploy_attach_test.go`.

## Files

- `pkg/remotecompanion/` — neutral `Registry` (put/get/remove by `InstanceKey`), `AgentSocketPath`,
  `AgentScratchDir`.
- `pkg/supervisor/` — promoted from `cmd/cornus/internal/supervisor`; `Service`/`ServiceFunc`,
  `Policy` (`RemoveOnExit`/`Restart` with capped exponential backoff), `AddSystem`, panic recovery.
- `pkg/deploy/dockerhost/` — `ForwardPort` (branches on `b.remote`), `forwardPortViaCompanion`,
  `isCompanion`/`firstInstanceID` fix.
- `pkg/deploy/containerdhost/` — mirror of the dockerhost companion mechanism over OCI mount
  options and a pinned netns join.
- `pkg/caretaker/caretaker.go` — `runCaretakerConn`, `connSup`, `runPortForwardAccept`,
  `runHubRoleBundle`, the top-level `Run` supervisor tree.
- `pkg/server/deploy_exec.go` — `handleDeployExecCreate`, `handleDeployExecAgentChannel`,
  `agentForwardAllowed`, `relayAgentMuxed`.
- `pkg/server/gcschedule.go` — `runGCTick`, `Server.sup`.
- `pkg/server/caretaker_attach.go` — `caretakerConfigEnv`, the `serverBound` gate.
- `pkg/deploy/deploy.go` — `RemoteCapable`, `AgentForwardCapable` interfaces.
- `pkg/deploy/kubernetes/kubernetes.go` — `addAgentForwardRole`, `injectAgentForward`,
  `deployment()`'s dispatch chain, `AgentForwardEnabled`, `cornus.dev/agent-forward` annotation.
- `pkg/api/deploy.go` — `DeploySpec.AgentForward`, `ExecConfig.ForwardAgent`.
- `pkg/compose/types.go`, `pkg/compose/project.go` — `Service.AgentForward` /
  `x-cornus-agent-forward`, `translateService`.
- `cmd/cornus/internal/sshagent/` — shared `sshagent.Dial`, extracted from
  `cmd/cornus/tunnel.go`'s `dialLocalAgent`.
- `pkg/wire/` — `OpenPortForward`/`AcceptTagged` (tag `'F'`), `TagAgentRelay` (tag `'A'`),
  `Pipe` (widened to `io.ReadWriteCloser`).
- `e2e/scenarios/deploy-remote-portforward-docker.star`, `e2e/scenarios/deploy-remote-exec-agent.star`
  (branches per `TARGET`: docker/containerd/kube).
- `pkg/e2e/harness.go` — `bDeploy`'s `agent_forward?` kwarg.

## Test Coverage

- `pkg/server/caretaker_supervisor_test.go`: `TestCaretakerRoleIsolation` (credential-role failure
  as fault injection, proven alongside a real hub round-trip on the same connection),
  `TestPeriodicGCSupervisedAcrossPanic`.
- `TestForwardPortViaCompanion` (companion-relay teardown on caller close).
- `pkg/deploy/kubernetes/agentforward_test.go`: `TestAgentForwardAloneGetsMinimalCaretaker`,
  `TestNoAgentForwardNoCaretaker`, `TestAgentForwardFoldsIntoHubCaretaker`,
  `TestAgentForwardEnabled`.
- `pkg/server/exec_agent_channel_test.go`: `fakeAgentForwardBackend` (a dedicated wrapper, not a
  method added to the shared `fakeBackend`); tests proving exec-create accepts `ForwardAgent` and
  injects `SSH_AUTH_SOCK` when `AgentForwardEnabled` is true, rejects it (400, not 500) when false,
  and that the exec-agent-channel endpoint's upgrade succeeds through the `AgentForwardCapable`
  path too.
- `pkg/compose/agentforward_test.go`: opted-in and not-opted-in Compose translation.
- E2E: `e2e/scenarios/deploy-remote-exec-agent.star` was run for real against both a live kind
  cluster (`E2E_TARGETS=kube`) and a live Docker daemon (`E2E_TARGETS=docker`, via
  `CORNUS_AGENT_IMAGE` auto-provisioning in the containerized runner's entrypoint), including the
  kube branch's negative case (an `agent_forward`-less deployment must reject `--forward-agent`).
  `deploy-remote-portforward-docker.star` was similarly run for real. The containerd branch of
  `deploy-remote-exec-agent.star` exists but is **not** run automatically — it needs a manual
  `ctr images import` step this sandbox has no containerd host for, the same limitation as
  `deploy-mounts-sidecar-containerd.star`. **Note:** `cmd/cornus-e2e --target kube` (kind-backed)
  has existed all along and is wired into CI (`e2e.yml`) as one of three regular E2E targets,
  already backing dozens of pre-existing scenarios — an earlier verification note in this arc's
  provenance incorrectly claimed no kube E2E target existed at all; that claim was wrong and should
  not be repeated.
- Full `gofmt -l` / `go build ./...` / `go vet ./...` / `go test ./... -count=1` gate is clean
  throughout this arc (67 packages report `ok`). `docs:build` (VitePress, English only) has no dead
  links.

## Pitfalls

- Don't add a method directly to the shared `fakeBackend` test fixture — its embedders
  (`fakeMountingBackend` and others) inherit it via Go method promotion, which can silently flip
  interface-satisfaction-based dispatch decisions in unrelated, pre-existing tests. Use a dedicated
  wrapper (`fakeRemoteBackend`, `fakeRemoteMountingBackend`, `fakeAgentForwardBackend`) instead.
  This has happened twice in this codebase now.
- `deploy.Bridge` does not half-close correctly over a `*yamux.Stream` (no `CloseWrite() error`
  method) — use `wire.Pipe` (accepts `io.ReadWriteCloser`) for any new relay that doesn't need
  `Bridge`'s stdin/stdout half-close asymmetry.
- Any caretaker accept loop that blocks on the shared pod-scoped `sess` (not a stream it opened
  itself, not a listener) needs an explicit `<-ctx.Done()` -> `sess.Close()` goroutine — the
  existing `defer sess.Close()` in `runCaretakerConn` fires too late (after the supervisor's
  `Wait()` already needs the loop to have returned).
- A companion/sidecar's scratch volume for a new capability must be provisioned independent of any
  feature-specific volume (e.g. `--mount`'s volumes) if that capability is meant to work even when
  the feature-specific path is absent — and on Docker, such provisioning must happen *before* the
  per-replica container-create loop, since mounts cannot be added to an already-created container.
- When adding a new server-bound caretaker role field, check `caretakerConfigEnv`'s `serverBound`
  gate — it is a hand-maintained boolean expression, not derived from the `Config` struct's shape,
  and has already missed two new roles once (`PortForward`, `AgentRelay`).
- `cornus tunnel --forward-agent` and `cornus exec --forward-agent` are unrelated mechanisms despite
  the similar flag name — the former forwards to the server process for its own outbound SSH
  handshake (see [[public-tunnels]]); the latter forwards into a running workload container. Do not
  conflate them when reading code or docs.
