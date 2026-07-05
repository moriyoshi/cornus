# In-Container Server Mode (dockerhost / containerd)

Status: **implemented for dockerhost; containerd path translation is implemented
with networking/CNI limitations** (updated 2026-07-26).

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
| D3 | `ForwardPort` / `cornus tunnel` / `cornus port-forward` | Non-remote path dials the container IP directly (`dockerhost.go:657-679`). A server on the default bridge cannot reach a workload on a user-defined network. Fails at connect time, not at deploy time. | `pkg/deploy/dockerhost/dockerhost.go` |
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

`barehost` and `incushost` share C2/C3/C4/C6 by construction (same `hostrun`
package). The plan deliberately excludes them; the mechanism is written so they
can adopt it later without a second design.

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
host-path caveat to the policy error text).

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
3. **`barehost`/`incushost`**: out of scope, but the `hostrun` mapper from Phase 3
   covers most of their exposure. Worth a follow-up TODO entry rather than scope
   creep.
4. **Self-management**: nothing today stops a deploy from targeting the cornus
   container itself. Not a new problem in this mode, but it becomes reachable —
   worth a label-based guard in the GC/reconcile pass.

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

Remaining work is containerd host networking/CNI packaging, barehost/incushost
path translation, and privileged confirmation of host-process mount unwind.
