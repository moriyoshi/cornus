# In-Container Server Mode (dockerhost / containerd / bare / incus)

Status: **implemented for dockerhost, containerd and incus; bare has one
consequence and no way to detect it** (updated 2026-08-06).

## Summary

Running the cornus server in a container against a host Docker/containerd runtime
is now supported for dockerhost. Containerd path handling and content-addressed
log-shim staging are implemented, while a complete container installation still
requires host networking and available CNI plugins.

`pkg/hostenv` separates being in a container from proven host-path divergence;
`pkg/hostcheck` makes the silently empty-bind case fail loudly. Dockerhost
translates minted 9P paths and policy prefixes, and the setup wizard plus
`server-in-container.star` cover the supported Docker topology.

## Historical Proposal Baseline

The inventory below preserves the pre-implementation analysis and phase rationale;
the current status is summarized above and in `## Implemented State`.


- The README prints a `docker run ... -v /var/run/docker.sock:...` line ("Plain
  Docker, no Kubernetes"), but nothing in the codebase treats that as supported.
- No "am I in a container" detection, no host/container path translation, no
  preflight. The one known sharp edge is a prose warning in `ARCHITECTURE.md`
  ("Running with the right privileges"): bind `<DataDir>/mounts` from the host
  with `rshared`, "otherwise the container silently binds an empty directory".
- `cornus setup` (`cmd/cornus/internal/setupwiz`) offers, for docker/containerd
  hosts, only the **SSH** scenario, and its artifact is a `cornus.service` systemd
  unit — server-as-host-process. There is no container-install scenario and no
  compose/`docker run` artifact.
- The E2E harness always starts `cornus serve` as a **host process** per target
  (`pkg/e2e/target.go`: `LocalTarget`/`DockerTarget`/`ContainerdTarget`/
  `BareTarget` only contribute `ServeEnv()`). Even the containerized runner
  (`e2e/container/`) runs the server as a process next to an in-container dockerd,
  which is co-location, not this mode.
- The existing escape hatch `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`
  (`pkg/server/server.go` `defaultBackendFactory`) was designed for a *remote*
  daemon. It solves a strict superset of some problems (mounts via a companion
  sidecar, port-forward/tunnel/exec-agent rerouting) at the cost of a per-instance
  sidecar for every deployment, and solves none of the containerd host-path
  problems.

## Failure inventory

### `dockerhost` — server in container, host daemon socket

| # | Area | What breaks | Where |
| - | ---- | ----------- | ----- |
| D1 | Client-local mounts (9P fast path) | Server 9P-mounts under `<DataDir>/mounts/<session>` inside its own mount namespace and hands that path to the daemon as a bind source. The daemon resolves it on the **host**, so without an `rshared` bind of `<DataDir>` the workload gets an empty dir. Silent. | `pkg/server/deploy_attach.go` `applyWithHostMounts`, `pkg/deploywire` MountManager |
| D2 | Path identity | Even *with* a bind, the container path and the host path may differ (`-v /srv/cornus:/var/lib/cornus`), and nothing translates. | same |
| D3 | `ForwardPort` / `cornus tunnel` / `cornus port-forward` | Non-remote path dials the container IP directly (`dockerhost.go:657-679`). A server on the default bridge cannot reach a workload on a user-defined network. Fails at connect time, not at deploy time. **FIXED 2026-08-05 — see `## D3: routing to a workload's network` below.** | `pkg/deploy/dockerhost/dockerhost.go` |
| D4 | Registry / advertise reachability | Workload pulls and caretaker dial-backs need a host-reachable URL. `CORNUS_ADVERTISE_URL` and the advertised registry host are operator-supplied with no in-container-aware default; `localhost:5000` inside the server container is not the daemon's `localhost`. | `pkg/server/auth.go` info, `resolveRegistrySource` |
| D5 | User bind mounts in a spec | Sources are host paths from the daemon's view — arguably *correct* docker-in-docker semantics — but the host-policy allowlist (`CORNUS_ALLOW_BIND_SOURCES`) is then evaluated against paths the server cannot see. Needs a documented decision, not code. | `pkg/deploy/dockerhost/policy.go` |
| D6 | Build engine | Unchanged: needs `--privileged` or the rootless stack. Already documented. | — |

Not broken: volumes (dockerhost uses daemon-managed volumes, no server-side host
path — `engine.go` `volumeEnsure`/mountSpec), logs, exec, stats, egress and
telemetry companions (all Engine API).

### `containerd` — server in container, host containerd socket

Everything above, plus hard blockers:

| # | Area | What breaks | Where |
| - | ---- | ----------- | ----- |
| C1 | **Log shim binary URI** | The log URI is `binary://<os.Executable()>?containerd-log-shim=<path>`. containerd's shim runs **on the host** and execs that path; the server's in-container `/usr/local/bin/cornus` does not exist there. Every task's IO setup fails, and the path is also persisted in a `containerd.io/restart` label, so it must stay valid across restarts and upgrades. | `pkg/deploy/containerdhost/logs_linux.go` `logURI` |
| C2 | **netns creation** | The backend pins netns under `/run/cornus/netns` and runs CNI in it. If the server container has its own network namespace, the veth lands in the container's netns, not the host's; and the path must be visible to the shim. | `pkg/deploy/internal/hostrun/network_linux.go` (`netnsDir`, `Setup`) |
| C3 | Volume backings | `<DataDir>/containerd/volumes/...` dirs are created by the server and passed to containerd as OCI bind sources, resolved on the host. | `pkg/deploy/internal/hostrun/volumes_linux.go` |
| C4 | Managed `/etc/hosts` | Same: a server-written file under `<DataDir>` bind-mounted into the container. | `pkg/deploy/internal/hostrun/hosts_linux.go`, `containerdhost/lifecycle_linux.go:253` |
| C5 | Log file path | `b.logPath(id)` is passed to the host-side shim as its output path. | `containerdhost/logs_linux.go` |
| C6 | CNI plugin binaries | `/opt/cni/bin` (or `CORNUS_CNI_BIN_DIR`) is exec'd by the server process, so it must exist **inside** the container. Conf dir is server-only, fine. | `hostrun/network_linux.go` `pluginDirs` |
| C7 | Host stats | `SystemCPUUsage`/`HostMemTotal` read `/proc` — correct only without lxcfs-style masking. | `hostrun/stats_linux.go` |

`barehost` shares C2/C3/C4/C6 by construction (same `hostrun` package). The plan
deliberately excludes it; the mechanism is written so it can adopt it later
without a second design. In practice bare needs none of it while its OCI runtime
stays cornus's own child — it inherits cornus's mount namespace, so a
containerized bare server runs workloads inside that same container
(`hostcheck.sharesMountNamespace`). `CORNUS_BARE_SHIM` is what would end that, and
it is off with four prerequisites outstanding.

**`incushost` does NOT share those rows — measured 2026-07-31, correcting the
earlier claim.** It imports `hostrun` for `StreamStats` only (C7), and none of the
four apply to it:

- C2 netns: incusd owns instance networking; incushost pins no netns. Its only
  mentions of netns are comments explaining that its companion is a SIBLING
  INSTANCE rather than a netns-sharing sidecar.
- C3 volume backings: incus custom-storage-pool volume NAMES, not server-created
  host directories.
- C4 managed `/etc/hosts`: incusd generates one from the instance name and
  bind-mounts it over the rootfs on every start — which is exactly why this
  backend warns that `hostname` and `extraHosts` are ignored. cornus writes none.
- C6 CNI: incus uses its own bridge and `proxy` devices; there is no CNI at all.

What incushost hands incusd is a USER's bind source (a host path by definition —
the daemon opens it, so translating it would BE the bug), a storage-pool volume
name, or tmpfs. `b.dataDir` is carried for parity and builds no path. So there is
nothing to translate, and `hostcheck` correspondingly does not run its data-dir
check for incus (`handsDataDirToRuntime`); it used to, and told incus operators
that bind-mounting the data dir would make client-local mounts available — advice
that cannot help, since incus is not a `MountingBackend` at all.

## Design

Two new primitives, both small, plus one policy decision.

### `pkg/hostenv` — self-location and path translation

New package (no build tag; a `_linux` file for the mountinfo bits):

```go
type Env struct {
    InContainer bool
    Runtime     string   // "docker", "containerd", "unknown"
    SelfID      string   // own container ID when discoverable
    HostNetwork bool     // own netns == host netns
    HostPID     bool
}

type Mapper interface {
    // ToHost translates a path in this process's mount namespace to the path the
    // host runtime must be given. Returns ok=false when the path is not
    // host-visible at all, so callers fail loudly instead of binding nothing.
    ToHost(p string) (string, bool)
    // Propagation reports the mount propagation of the mount backing p
    // ("shared", "slave", "private"), for the 9P precondition.
    Propagation(p string) string
}
```

Sources, in priority order:

1. **Explicit** — `CORNUS_HOST_PATH_MAP="/var/lib/cornus=/srv/cornus,/run/cornus=/run/cornus"`.
   Canonical, always available, the only thing E2E and non-docker runtimes need.
2. **Self-inspection (docker)** — resolve own container ID from
   `/proc/self/mountinfo` / `/proc/self/cgroup`, then `GET /containers/<id>/json`
   over the already-configured Engine socket, and build the map from `Mounts[]`
   (`Source` -> `Destination`). This is what testcontainers/compose-in-docker do,
   and `pkg/deploy/dockerhost/engine.go` already has the client and a fake for
   tests. `HostNetwork` comes from `HostConfig.NetworkMode`.
3. **Detection only** — `/.dockerenv`, `/proc/1/cgroup`, `/proc/self/ns/net` vs
   `/proc/1/ns/net` (needs `--pid=host`) for `HostNetwork`; enough to emit a good
   error when no map is available.

Explicit override always wins so an operator can correct a bad guess, and so the
containerd case (where self-inspection may be impossible) is never a dead end.

### A preflight that fails loudly

`cornus serve` gains a startup preflight (and a `cornus daemon preflight`
subcommand plus a field on `/.cornus/v1/info`, so `cornus setup`'s verify step and
the web UI can show it):

- in-container? runtime? socket reachable?
- `<DataDir>` host-visible (mapper hit) and, for the 9P path, propagation
  `shared`?
- for containerd: host netns? `/run/cornus` host-visible and shared? CNI plugins
  present **inside** the container? log-shim staging dir host-visible?
- advertise URL set and syntactically host-reachable?

Policy: **warn** for capabilities that are merely unavailable (9P mounts), **fail
to start** for a configuration that would silently corrupt (containerd with a
non-host netns, or a `<DataDir>` the runtime cannot see). The current failure mode
— an empty bind mount, no message — is the thing worth deleting.

### Threading the mapper through

One rule: **every path that leaves cornus and is interpreted by the host runtime
goes through `Mapper.ToHost`.** The chokepoints are few:

- `pkg/server/deploy_attach.go` `applyWithHostMounts` — translate rewritten bind
  sources (D1/D2).
- `pkg/deploy/internal/hostrun` — `OCIBindMount`, `VolumeStore.InstanceMounts`,
  `HostsStore.Path`, `CNIManager` netns paths (C2/C3/C4). Give the constructors an
  optional `Mapper` rather than sprinkling calls at each use site.
- `pkg/deploy/containerdhost/logs_linux.go` — `logURI` and `logPath` (C1/C5).
- `pkg/deploy/dockerhost/policy.go` — decide and document that bind sources are
  host paths (D5).

For **C1** specifically, `os.Executable()` is replaced by a *staged* binary: at
startup, if in-container, copy the running binary to
`<DataDir>/bin/cornus-<contenthash>` (content-addressed, so an upgraded server
does not invalidate the `containerd.io/restart` labels of containers created by
the old one), and use `ToHost` of that path in the URI. GC unreferenced staged
binaries during the existing reconcile pass.

### Runtime requirements, stated up front

Documented and preflighted rather than worked around:

- **dockerhost**: socket bind; `<DataDir>` bind with `rshared` if client-local
  mounts are wanted; `--network host` (or remote mode) if `port-forward`/`tunnel`
  are wanted; `--privileged`/rootless stack if builds are wanted.
- **containerd**: socket bind; `--network host`; `--pid=host`; `/run/cornus` and
  `<DataDir>` bind with `rshared`; CNI plugins inside the image; root.

`--network host` for containerd is not a workaround that can be avoided — CNI must
attach veths to the host's bridge. Stating it beats half-supporting it.

## Phased delivery

Each phase is independently shippable and independently verifiable.

**Phase 1 — `pkg/hostenv` + preflight (no behavior change).**
Detection, mapper, mountinfo propagation, `cornus daemon preflight`, info field,
startup log line. Pure addition; unit-testable with fixture `mountinfo`/`cgroup`
files and the existing dockerhost fake Engine API.

**Phase 2 — dockerhost in-container becomes real.**
D1/D2 (translate 9P bind sources, hard error when not host-visible), D4 (derive
the advertise/registry default from the detected env: published port + bridge
gateway, matching what `pkg/e2e/target.go` `DockerTarget.AdvertiseHost` already
computes for tests), D3 (detect unreachable instance IP and return an error naming
the two fixes: host networking, or `CORNUS_DOCKER_REMOTE`), D5 (document; add the
host-path caveat to the policy error text). D3 was later solved outright rather
than explained — see `## D3: routing to a workload's network`.

**Phase 3 — containerd in-container.**
C1 (staged log-shim binary), C2 (host-netns requirement + preflight), C3/C4/C5
(mapper through `hostrun` + `logPath`), C6 (ship CNI plugins in the image, or
preflight their absence with an install hint), C7 (note only).

**Phase 4 — packaging and UX.**
- Image: keep the current `Dockerfile`; add the CNI plugins to a
  `cornus:*-containerd` variant or an install script, whichever the license audit
  prefers.
- `cornus setup`: two new scenarios, "Docker host (server in a container)" and
  "containerd host (server in a container)", whose artifact is a ready-to-run
  `compose.yaml` / `docker run` line with the exact binds — the analogue of the
  existing systemd-unit artifact (`cmd/cornus/internal/setupwiz/artifacts.go`).
- Docs: a new `docs/guides/` page plus `docs/reference/server-env-vars.md` entries
  for `CORNUS_HOST_PATH_MAP` and friends; update the README's Plain-Docker variant
  to the real, complete invocation; update `ARCHITECTURE.md` "Running with the
  right privileges" from prose warning to a documented mode. Translations for
  `docs/ja` and `docs/zh` per the `translate-documents` skill.

**Phase 5 — E2E proof.**
The mode is only real if it is exercised. Add a target *wrapper* to `pkg/e2e`
that, instead of spawning `cornus serve` as a process, starts the `cornus:e2e`
image as a container with the socket and binds, then runs the existing scenario
set unchanged against it:

- `docker-inctr` — against the containerized runner's dind daemon.
- `containerd-inctr` — against the runner's standalone containerd.

This reuses `e2e/container/entrypoint.sh`'s existing daemons; the new surface is a
`ServerLauncher` seam in the harness (today the server start is implicit in the
target contract) plus two Makefile `SCENARIOS` entries. It also gives the
regression coverage for C1/C2, which no unit test can reach.

## Open questions

1. **Is `--network host` acceptable for the documented containerd mode?** If not,
   the alternative is entering the host netns via `/proc/1/ns/net` with
   `--pid=host` + `CAP_SYS_ADMIN` for the CNI/netns operations only, which is more
   code and more surprising. Recommendation: require host networking, revisit if
   an operator objects.
2. **Should dockerhost in-container just *be* remote mode?** Remote mode already
   solves D1 and D3 correctly via the companion sidecar. The cost is a sidecar per
   instance always. Recommendation: keep the fast path as the default and make
   remote mode the documented fallback the preflight points at — but if Phase 2
   turns out to be more than a few hundred lines, flipping the default for
   in-container installs is the cheaper answer and should be reconsidered then.
3. **`barehost`/`incushost`**: out of scope here. This originally read "the
   `hostrun` mapper from Phase 3 covers most of their exposure", which the
   2026-07-31 measurement contradicts for incus — it has no such exposure to cover.
   The follow-up TODO entry this recommended was filed, worked, and closed as
   premise-wrong; see the C-row note above.
4. **Self-management**: nothing today stops a deploy from targeting the cornus
   container itself. Not a new problem in this mode, but it becomes reachable —
   worth a label-based guard in the GC/reconcile pass.

## D3: routing to a workload's network (2026-08-05)

D3 was closed by making cornus join the network rather than by documenting a
requirement. Phase 2 had only shipped the diagnostic (`WithIsolatedNetwork` ->
`unreachableHint`), which named `--network host` and `CORNUS_DOCKER_REMOTE`; the
default `docker run` install still could not port-forward to anything with
`networks:` in its spec.

**The mechanism is in `pkg/deploy/dockerhost/selfnet.go`.** Apply attaches this
cornus's own container to each network it ensures, before the workload containers
exist; `reapNetwork` detaches during delete-time GC. Three guards, all
load-bearing: `isolatedNetwork` (we have a netns of our own), non-remote (remote
mode routes through a companion and its daemon need not be local), and a
CONFIRMED self id from `hostenv` — a guessed id would attach an unrelated
container to the workload's network.

**Measured on Docker 29.2.1, because each of these decided a design point:**

| Probe | Result | What it settled |
| ----- | ------ | --------------- |
| container-on-bridge -> container-on-user-network | UNREACHABLE | the bug is real and is docker's isolation chains, not cornus |
| host -> the same address | reachable | why every host-process target passes and only this topology fails |
| `docker network connect <usernet> <ctr-on-bridge>` | default route UNCHANGED | the self-attach does not re-home cornus's own egress |
| `docker network connect <internal-net> <ctr>` | default route UNCHANGED | an `internal: true` network cannot strand the server either |
| `docker network connect bridge <ctr-on-usernet>` | **default route MOVED** | why the default bridge is the one attachment that stays manual |
| repeat connect | 403 `endpoint ... already exists` | must read as success, or every re-apply warns |
| disconnect when not attached | 500 `is not connected to network` | must read as success, or GC logs noise |

**Two halves, both required.** Joining alone is not enough: `pickNetworkIP`
preferred the default bridge "which the server host can route to" — true for a
host process and exactly backwards here, since the bridge is frequently the one
network a workload is on that a containerized server cannot see. `containerIP`
now takes a `reachable` set (nil = host view = historical behaviour) and returns
"no route" rather than an address that will time out.

**GC would have silently broken.** cornus's own endpoint keeps a network
non-empty, so `networkInspect` returning a COUNT would have left a network and an
endpoint behind on every deploy/delete cycle. It returns member ids now, and
`reapNetwork` leaves before it counts.

**Not fixed, deliberately:** a workload with no `networks:` at all lands on the
default bridge. Per the probe above, joining that would move cornus's default
route, so this case keeps the pre-existing remedies and the pre-existing hint.

### The follow-on landmines the self-attach creates (swept 2026-08-05)

An endpoint is state, and state has a lifecycle. Three consequences, each probed
rather than reasoned about:

| Probe | Result | Consequence |
| ----- | ------ | ----------- |
| `docker restart` the server | endpoint SURVIVES | restarts are fine |
| `docker rm` + `docker run` (the upgrade) | endpoint **GONE**, nothing rejoins | every existing deployment unreachable, with an error blaming the netns and pointing at `--network host` when the remedy was "re-apply" |
| server joins first, then fill a `/29` | 5 replicas fit where 6 did; 6th fails at **START** with `no available IPv4 addresses on this network's address pools` | cornus is an invisible extra tenant of the user's own subnet |
| a STOPPED container's `NetworkSettings.Networks` | still lists every network, each with an EMPTY address | "not running" is indistinguishable from "no route" unless unaddressed endpoints are dropped |

Fixes, in the same order:

- **`instanceIP` joins on demand.** ForwardPort is the single dial chokepoint (the
  emulated ingress front door and `cornus tunnel` both reach workloads through
  `deploy.PortForwardDialer` -> `Backend.ForwardPort`), so one retry there covers
  every server-originated path — and also covers deployments created before this
  mechanism existed. Live-verified: post-upgrade port-forward went 000 -> 200,
  with one INFO line naming the rejoin.
- **`addressPoolHint`** appends cornus's own contribution to the daemon's
  address-pool error, gated on that exact wording AND on cornus actually holding
  an endpoint, so it cannot editorialize on an unrelated start failure.
- **`containerNetworks` returns only ADDRESSED networks**, so a merely-stopped
  workload does not drag the server onto networks it has no reason to be on, once
  per failed forward.

Residual, stated rather than fixed:

- A companion that dials the server by CONTAINER NAME will not resolve between a
  server upgrade and the next apply-or-forward. The attachment-bearing companions
  (client mounts, egress) die with the server and return via a fresh Apply, which
  rejoins; a long-lived telemetry sidecar usually has a host-address advertise
  URL. Not worth a startup reconcile pass until something is observed to need it.
- With TWO cornus servers on one daemon, `reapNetwork` excludes only its OWN
  endpoint, so a network the other server is on is not reaped. Judged correct: the
  other server may still be serving it.
- `containerIP` reads IPv4 only, never `GlobalIPv6Address`. Unreachable through
  cornus today — `NetworkAttachment` cannot express `ipv4: false`, so cornus
  cannot create an IPv6-only network — and only an external one could trip it.
### The same class of bug on a HOST-process server (swept 2026-08-05)

Not in-container mode at all, but found by asking the same question of the default
topology, and it lives in the same code. `pickNetworkIP`'s premise — the host can
route to every docker network — is false for one family:

| Probe | Result | Consequence |
| ----- | ------ | ----------- |
| host -> a container on an `internal: true` network | **REACHABLE** | no special case needed; `internal` blocks the network's egress, not the host's access |
| host -> a container on a **macvlan** network | **UNREACHABLE** | kernel-level: a macvlan child cannot talk to its own parent interface |
| a workload on macvlan AND bridge | address chosen by network NAME | which of the two addresses ForwardPort dialed was decided alphabetically |

`pkg/deploy/dockerhost/hostisolation.go` demotes macvlan/ipvlan-driven networks when
the workload has another address, and errors immediately naming the cause when it
does not. Live: with networks named `mv-a-lan` (macvlan) and `mv-z-br` (bridge),
pre-fix gave `dial container 192.168.10.240:80: connect: no route to host`, post-fix
status 200 from the bridge address. **The first live attempt used `mv-lan`/`mv-br`,
where the bridge sorts first — it returned 200 on both builds and proved nothing.**
The adversarial naming is what makes the reproduction evidence.

Separately, `unreachableHint` was gated on being containerized, so a host-process
server pointed at `DOCKER_HOST=tcp://other-machine` without `CORNUS_DOCKER_REMOTE=1`
got a bare timeout and no pointer to the setting that exists for it. It now covers
that topology, and stays silent in remote mode.

Cost discipline worth reusing: `ForwardPort` runs once per CONNECTION, so `containerIP`
was split into `containerAddresses` (one inspect, raw) + `selectIP` (pure) rather than
adding a second inspect to learn the networks. Driver lookups are cached for the
backend's lifetime. A test counts inspects across ten forwards (10 container, <=2
network) so neither can regress silently.

- Checked and NOT a problem: `network_mode: host`/`container:` is not expressible
  in `api.DeploySpec` at all, so the case space really is "default bridge or
  user-defined networks"; the hub overlay's synthetic IPs are kubernetes-only and
  never reach this backend; `endpointConfigFor` deliberately does not wire the
  IPv4 pin, so there is no interaction with a static `ipv4_address`; and Docker
  reserves the gateway address, so a pinned `gateway:` cannot be stolen by the
  server's endpoint.

**Evidence.** `selfnet_test.go` against the fake daemon (which now models the 403
and the 500); three separate neutralizations, each caught by the intended test.
Live: a containerized server + a compose project with `networks:`, port-forward
returning 200 post-fix and hanging pre-fix, plus re-apply with no warning and
`compose down` reaping the network. E2E: the network section of
`server-in-container.star`, which fails on a pre-fix server image with
`"bridge" does not contain "inctrnet_appnet"`.

### The same question asked of containerd, bare and incus (swept 2026-08-06)

The dockerhost sweep established the premise worth auditing: **"the server can route
to the workload"** is an assumption, and it is worth asking each backend how it earns
it. The three remaining host backends answer in two distinct ways, and neither
resembles dockerhost.

| Backend | Who builds the workload network | Reachable from a containerized server? | Failure shape |
| ------- | ------------------------------- | -------------------------------------- | ------------- |
| `containerd` | cornus, via `hostrun.CNIManager` (plugins forked as cornus's children) | **yes, by construction** — the bridge is in cornus's own netns | published ports DNAT'd in the wrong netns; the netns PIN PATH is invisible to a host containerd (loud) |
| `bare` | same shared `CNIManager` | **yes, by construction** — and runc is cornus's child, so the pin path is visible too | published ports DNAT'd in the wrong netns (silent) |
| `incus` | incusd, on its own bridge in the HOST netns | **no** — and cornus cannot join it either | bare dial failure, previously with no explanation |

Three findings, all fixed:

1. **`hostcheck`'s netns check was gated on path translation.** `hostNetworkCheck` sat
   inside `if in.Env.Translating`, which made it unreachable in the configuration the
   guide recommends: bind the data dir at the *same* path on both sides, no
   `CORNUS_HOST_PATH_MAP` is needed, nothing translates, and a containerized containerd
   server was told nothing at all about its network namespace. The two questions are
   unrelated — CNI builds the bridge in cornus's netns whatever its paths mean — so the
   check now hangs off `InContainer` alone.
2. **The check named containerd only, but `bare` shares the same CNIManager.**
   hostrun's own doc says "Shared by both host backends"; only one was checked. `bare`
   was the better-hidden of the two, because `sharesMountNamespace` excuses it from
   every *other* check here (its OCI runtime is cornus's own child, so its paths cannot
   diverge) — so a containerized bare server produced **no host-environment output
   whatsoever** while silently DNAT'ing its published ports inside its own container.
   Confirmed by the neutralization, whose failure output is the entire check list: two
   OK lines.
3. **incus had the dockerhost bug with none of the dockerhost machinery.** An instance
   sits on incusd's bridge in the host netns; a containerized cornus has no route, and —
   unlike docker — no way to get one, because a cornus container is not an incus
   instance. The pre-fix error was `incus: dial instance 127.0.0.1:39145: ... connection
   refused`, indistinguishable from "the workload is not listening yet".
   `WithIsolatedNetwork` now appends the cause and names `CORNUS_INCUS_REMOTE=1`, which
   is the only remedy besides host networking. The backend's own ForwardPort comment had
   *already stated* this topology as the reason remote mode exists — the knowledge was
   in the tree, just not on the path where an operator would meet it.

Plus one determinism defect, the direct analogue of the macvlan tie-break above:
`incushost.pickIPv4` **ranged over the state's per-interface map**, and Go randomizes
map iteration. One addressed NIC hides it; two or more — a profile adding a bridged
NIC, or a macvlan/routed NIC beside eth0 — returned a different address on different
calls. Where the two differ in reachability that is a port-forward that works about
half the time, which is materially harder to diagnose than one that never works. Now
sorted by interface name, which also puts `eth0` first for free. The pre-existing
`TestPickIPv4` could not have caught it: its map had exactly one addressed NIC, so it
passed under every iteration order.

- Checked and NOT a problem: containerd/bare have no macvlan analogue — every network
  they realize is a bridge conflist cornus generated, and `UnsupportedNetworkFeatures`
  already warns that a spec's `driver` is ignored, so the case cannot arise. Their
  recorded instance address is refreshed by the reboot-recovery re-Setup
  (`repairNetns` / containerd's netns repair both persist the fresh `IP`/`NetIPs`), so
  there is no stale-address analogue of dockerhost's dropped endpoints. incus has no
  remote-daemon case either: `New` calls `ConnectIncusUnix` and nothing else, so
  `overTCP` has no counterpart. `ForwardPort` is the sole workload dial in all three
  backends (grepped for `DialContext`/`net.Dial`), so it remains the single chokepoint.
- Stated limit on the evidence: the netns location of the portmap DNAT rules is derived
  from how CNI works (the plugin is cornus's forked child, so it inherits cornus's
  netns), **not measured** on a split-netns bare setup. The containerized E2E runner
  shares one netns between harness, cornus and workloads, which is precisely why its
  containerd leg passes — so it cannot host that measurement as built.

**Evidence.** Four new tests; four neutralizations, each caught by the intended test
with the intended diagnostic and none via a compile error. The determinism test asserts
across 400 iterations of a four-NIC map, so a surviving map range would have to pick
eth0 first 400 times running — it failed on iteration 0.

## Related documents

- [deploy-backends-synthesis.md](./deploy-backends-synthesis.md) — the backend
  contract the affected code paths implement.
- [containerd-backend.md](./containerd-backend.md) — the log shim, CNI, and netns
  machinery behind C1/C2/C6.
- [hostrun-shared-runtime.md](./hostrun-shared-runtime.md) — the shared runtime
  package behind C2/C3/C4.
- [remote-companion-and-agent-forwarding.md](./remote-companion-and-agent-forwarding.md)
  — `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`, the existing escape hatch.
- [client-local-mounts-deploy.md](./client-local-mounts-deploy.md) — the 9P mount
  path behind D1.
- [setup-wizard.md](./setup-wizard.md) — the wizard that Phase 4 extends.

## Implemented State

An explicit `CORNUS_HOST_PATH_MAP` always wins. Otherwise Docker self-inspection
confirms the current container and its mounts; symlinked destinations such as
`/var/run/docker.sock` and `/run/docker.sock` are normalized. Without evidence,
the mapper remains identity so the all-in-one E2E runner is not misclassified.

Startup preflight, `cornus daemon preflight`, and `/.cornus/v1/info` expose the
verdict. Containerd translates OCI mount crossings and stages a host-visible
content-addressed log shim because persisted restart labels can outlive a server
upgrade. The setup artifact uses socket and `rshared` data-dir binds, explicit
`CORNUS_DATA`, and matching profile settings.

Since 2026-08-05 a containerized dockerhost server also routes to workloads on
user-defined networks by attaching its own container to them (D3, below), so the
default `docker run` install needs neither host networking nor remote mode for
port-forward, tunnels or caretaker dial-backs.

Remaining work is privileged confirmation of host-process mount unwind.
(Barehost/incushost path translation was retired 2026-07-31: incus has no
cornus-built path to translate, and bare inherits cornus's mount namespace until
`CORNUS_BARE_SHIM` changes that — see the C-row note above.)

## containerd and incus finished (2026-08-06)

Both are now self-configuring, and every claim below was MEASURED against live
daemons in the all-in-one runner image, not derived.

**containerd.** `containerdhost.SelfInspector` gives hostenv a second Inspector.
The container id comes from `/proc/self/cgroup`, whose containerd default
`/<namespace>/<id>` shape also names the namespace to load from, so the lookup is
exact rather than a search (a namespace search remains as the fallback, ordered
deterministically). The path map and the network mode come from that container's
OCI spec: **absence** of a `network` entry in `linux.namespaces` is how OCI spells
"shares the host's netns" — a present entry, with or without a Path, never is.

Four findings that changed the design, all of them measured:

1. **hostenv could not have found the id.** Its miners match 64-hex ids and the
   docker/CRI cgroup spellings; a containerd container is an arbitrary name
   (`ctr run ... myserver`). Hence `Options.ExtraSelfIDs`, supplied by the backend.
2. **`looksContainerized` misses a `ctr` container entirely** — no `/.dockerenv`,
   no `/run/.containerenv`, no `/containers/<64hex>` bind (containerd binds the
   host's own `/etc/hosts`), and an unreadable cgroup path. Gating self-inspection
   on it would have left the whole path dead on real hosts. So a CONFIRMED
   self-inspection is now itself evidence of containerization. In the dind runner
   the marker fires anyway, for the wrong reason — the OUTER container's docker id
   leaks in through the bind-mounted `/etc/hosts` — which is worse than not firing,
   because it looks like it works.
3. **`spec.Hostname` is empty**; a containerd container inherits the host's
   hostname. So Hostname is neither evidence nor a route to the id, unlike docker's
   12-hex short id.
4. **The default spec binds `/etc/hosts` and `/etc/resolv.conf` from the host**, and
   those destinations are mount points in EVERY container. Reporting them would let
   `confirmSelf` confirm any candidate id at all, defeating the guard that exists to
   stop a confident wrong path map. `reportableMounts` drops them.

Two consequences became checks rather than discoveries. The netns pin is
translated where it enters an OCI spec (four sites) and its visibility is a FATAL
preflight check: `/run` is container-private, and a file created at
`/run/cornus/netns` inside a `ctr`-run cornus is invisible from containerd's own
mount namespace, so without the bind every deploy fails — after the image pull and
after the previous healthy deployment has been torn down. And a PROVEN isolated
netns is fatal, which was the deferred half of the 2026-08-06 routing sweep;
`CORNUS_HOST_NETWORK` is the operator's override, and the only answer available to
bare, which has no daemon to ask.

Packaging needed one thing the plan missed and measurement found: the released
image needs **iptables**, not just the CNI plugins. The generated bridge conflist
sets `ipMasq` and portmap realizes `ports:`, and both shell out to it — measured as
`failed to locate iptables: exec: "iptables": executable file not found in $PATH`
from inside the bridge plugin.

**incus.** Nothing to translate (confirmed again: zero `os.Open`/`filepath.Join` in
the package, and `b.dataDir` is stored but never read), so the work was routing and
recognition. `/dev/incus/sock` is the containerization marker — without it a cornus
inside an instance reported "cornus runs on the host". An instance's hostname IS its
name, so `GetInstance(hostname)` is the whole self-inspection.

The payoff is that **cornus running AS an incus instance is the one containerized
incus topology with no routing problem**: it sits on incusd's bridge alongside its
workloads. `WithSelfInstance` suppresses the unreachable hint there, which would
otherwise blame a topology that is not the cause. Measured: the incusd socket must
be exposed to such an instance with a **proxy** device, not a disk device — a disk
device is idmap-shifted to `nobody` and cornus gets `connect: permission denied`;
with a proxy device `GET /.cornus/v1/deploy` returns 200.

Two warnings were also removed as unactionable: `unverifiedPathsCheck` no longer
fires for a backend that hands the runtime no cornus-built path (it was the ENTIRE
preflight output a containerized incus server produced, with a remedy that changed
nothing), and the dead `mountBindPrefixes` carve-out for incus is gone since incus
is not a `MountingBackend`.
