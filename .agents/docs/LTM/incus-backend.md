# The incus deploy backend

## Summary

`pkg/deploy/incushost` is cornus's fifth `deploy.Backend`
(`CORNUS_DEPLOY_BACKEND=incus`): it deploys OCI images as
[Incus](https://linuxcontainers.org/incus/) application containers (Incus 6.3+
OCI support) via the official Go client `github.com/lxc/incus/v6/client`. It sits
beside dockerhost/containerd/bare/kubernetes behind the same interface; nothing
above the backend (server, CLI, web UI, wire protocol) changed. Linux-only
(`//go:build linux` with a `!linux` `ErrUnsupported` stub), mirroring
containerdhost/barehost. This document is the canonical reference (it absorbed
the former standalone `INCUS_BACKEND_PLAN.md`).

## Key Facts

- **Selection + env.** `defaultBackendFactory` (`pkg/server/server.go`) and
  `localBackend` (`cmd/cornus/commands.go`) each gained an `incus` case. Knobs:
  `CORNUS_INCUS_SOCKET` (default `/var/lib/incus/unix.socket`),
  `CORNUS_INCUS_PROJECT` (default `default`), `CORNUS_INCUS_REMOTE` (companion
  opt-in, Phase 2), `CORNUS_INCUS_INSECURE_REGISTRIES`. The two advertise-mirror
  selectors (`advertisedRegistry`/`advertisedIngress`) only special-case
  kubernetes, so incus correctly falls through — no change needed there.
- **The `incusConn` seam.** `incus.InstanceServer` is ~100 methods returning
  async `Operation`s; the backend talks through a narrow `incusConn` interface
  (`backend_linux.go`) whose methods return already-waited plain values (the
  `realConn` adapter runs `Operation.Wait`). Exec is the sole method returning the
  live `Operation` (it needs the control-channel websocket). A fake `incusConn`
  drives every unit test, so `go test ./...` needs no live incusd.
- **Not-found.** `incusapi.StatusErrorCheck(err, 404)` (`isIncusNotFound`) is the
  reliable missing-instance test; it maps to `deploy.ErrNotFound` (Stop/Start/
  Restart) or a no-op (delete-if-exists). Never string-match.
- **Identity/labels.** Arbitrary metadata must live under Incus's `user.*`
  namespace, so `cornus.managed`/`cornus.app`/`cornus.origin.*` are stored as
  `user.cornus.*` config keys (env as `environment.*`, limits as `limits.*`,
  restart as `boot.autorestart`). Instance names are `cornus-<app>-<i>`.
- **Recreate-on-Apply.** Apply tears down the app's instances (stop then delete —
  Incus refuses to delete a running instance) and creates `Replicas(spec)` fresh
  ones with `Start:true`. Published ports become Incus `proxy` devices attached to
  **replica 0 only** (cross-backend contract).
- **Version pin.** Incus `v6.19.0+` requires `runtime-spec v1.3.0`, which breaks
  the vendored `containerd v1.7.24` `oci` package. MVS settles on **incus
  `v6.18.0` + runtime-spec `v1.2.1`** (still OCI-capable). Bumping incus further
  requires bumping containerd first.

## Details

### OCI image source (the Phase-0 open question)

`imageSource` (`image_linux.go`) maps a cornus ref to
`InstanceSource{Type:"image", Protocol:"oci", Server:<registry-url>,
Alias:<repo:tag>}` — the API form of `incus remote add <r> <url> --protocol=oci`
+ `incus launch <r>:<alias>`. Incus flattens the OCI image with **skopeo +
umoci** on the DAEMON host, so both must be on its PATH (the `CapIncus` preflight
checks them). Localhost / `CORNUS_INCUS_INSECURE_REGISTRIES` hosts are addressed
over `http://`. **Whether incusd pulls cornus's own localhost/plain-HTTP
registry directly is unverified without a live daemon** — the Phase-0 spike. If it
cannot, `IncusTarget.PrepareImage` side-loads the image (like
`KubeTarget.PrepareImage`); the decision is isolated in `image_linux.go`.

### Data plane (Phase 2)

- **Stats** (`stats_linux.go`): `GetInstanceState` → `hostrun.StatsSample` →
  shared `hostrun.StreamStats` Docker-JSON encoder (same framing as
  containerd/bare). Incus reports no host-wide system CPU total, so the docker
  CLI's CPU% reads low/zero (documented); memory/pids/network are exact.
- **cp** (`copy_linux.go`): rides the instance file API via the seam, translating
  to/from Docker's archive tar (dir → `base/` + tree, file → `base`, per the
  tarcopy naming contract). The file API is lossy vs a real stat — no size (drained
  to measure) and no symlink target (read as content). A future refinement is the
  SFTP client (`GetInstanceFileSFTP`).
- **Logs** (`logs_linux.go`): the instance CONSOLE log (OCI PID-1 stdout/stderr),
  a single raw unframed PTY stream, wrapped in `stdcopy` stdout framing.
  `--since`/`--until` are validated with `deploy.ParseSince` (malformed = error)
  but cannot be honored (no timestamps); Follow/Tail/Timestamps are warned and
  ignored. There is no Incus source for per-line timestamps.
- **ForwardPort** (`forwardport_linux.go`): resolves the instance's global IPv4
  from `GetInstanceState().Network` (`pickIPv4`, skips `lo`) and splices via
  `deploy.Bridge` (tcp) / `wire.BridgeDatagramStream` (udp). udp works here
  (routable instance IP), unlike kubernetes.
- **Exec** (`exec_linux.go`): an in-memory `execRegistry`. ExecStart runs
  `ExecInstance` bridging conn⇄process (non-TTY output `stdcopy`-framed), waits,
  reads the exit code from operation `Metadata["return"]` (a JSON `float64`).
  ExecResize pushes `InstanceExecControl{Command:"window-resize"}` to the captured
  control websocket (pulls `github.com/gorilla/websocket` into the direct
  requires). `buildExecPost` maps only a numeric uid (Incus exec `User` is
  `uint32`). Pid stays 0 (Incus does not surface it — like kubernetes).
- **Attach**: a deliberate, documented not-supported error. Incus exposes a
  console (single PTY to PID 1), not docker-attach stream semantics for an OCI app
  container — callers use exec.

### E2E

`IncusTarget` (`pkg/e2e/target.go`, drives the `incus` CLI for setup/teardown),
`CapIncus` preflight probe (`incus` + skopeo + umoci + daemon reachability),
`--target incus` runner enum (`cmd/cornus-e2e/main.go`), `make e2e-incus` +
`SCENARIOS_INCUS` (deploy/deploy-stats/lifecycle/exec/deploy-portforward/compose/
compose-exec). `IncusTarget.AdvertiseHost` returns `routableHostIP()` pending the
companion path. All integration is E2E-gated behind a live incusd.

The **containerized runner** (`e2e/container/`) runs the incus target fully
green (7/7) on a real privileged Docker host. `entrypoint.sh`'s `start_incus`
launches incusd, version-gates (self-skip < 6.3), and initializes it with a
firewall/NAT-disabled bridge preseed (not `--minimal`), dispatched by an `incus`
case (`INCUS_SCENARIOS` mirrors `SCENARIOS_INCUS`). Run with
`make e2e-container E2E_TARGETS=incus`.

**The runner image was migrated Alpine `docker:dind` → `debian:bookworm-slim`**
so a current OCI-capable incus installs cleanly. The Alpine path is a dead end:
Alpine stable ships incus **6.0 LTS** (predates OCI support, added 6.3 → deploys
fail `Unsupported protocol: oci`), and pulling incus 7.x from Alpine **edge**
breaks `nft` via a non-reproducible `libnftables`/`libnftnl` symbol skew
(`Failed clearing nftables rules ... EOF`). Debian gives a consistent nftables +
in-repo skopeo/umoci, and the **Zabbly** apt repo gives current incus.

Debian-migration specifics (all learned by running it):
- **incus** from `pkgs.zabbly.com/incus/stable` (bookworm) → incus **7.2**.
  Zabbly installs the daemon at `/opt/incus/lib/systemd/incusd` (off PATH, run
  via a lib-setup wrapper); a thin exec-wrapper at `/usr/local/bin/incusd`
  exposes it (a symlink would break the wrapper's `$0`-relative lib lookup).
- **Docker-in-Docker** rebuilt from Docker's official Debian apt repo
  (docker-ce + containerd.io + runc) plus the dind BOOTSTRAP scripts
  (`dockerd-entrypoint.sh`/`dind`/`modprobe`) COPY-ed from `docker:27-dind`
  (arch-independent shell; the docker:dind *binaries* are musl-linked so not
  reused). Two gotchas: `docker-init` is at `/usr/libexec/docker/` on Debian
  (symlink it onto PATH — `dind` execs `docker-init`), and `VOLUME
  /var/lib/docker` must be declared (as docker:dind does) or the nested dockerd's
  overlayfs fails overlay-on-overlay. Verified the docker target still passes.
- **cornus's own registry pull.** incus pulls OCI via skopeo, which defaults to
  HTTPS and rejects cornus's plain-HTTP loopback registry (`server gave HTTP
  response to HTTPS client`) — the concrete Phase-0 answer. Fixed image-side with
  a `/etc/containers/registries.conf.d/` entry marking `127.0.0.1`/`localhost`
  insecure; a host-only entry matches ANY port (verified skopeo then dials
  `http://`), covering the harness's dynamic registry port. Public registries
  keep TLS.
- **Exec TTY resize** (backend fix, `exec_linux.go`): the client sends the
  terminal size around exec-create, racing the control-channel setup, so the
  size is remembered on the `execSession` and applied both as the initial
  `InstanceExecPost.Width/Height` at start and via a window-resize once the
  control channel connects.

Also runnable against a **host incusd ≥ 6.3** via `make e2e-incus`.

## Files

- `pkg/deploy/incushost/incushost.go` — Config/Option/resolve, `instanceName`,
  `configKeyPrefix`. `backend_other.go` — `!linux` stub.
- `backend_linux.go` — `incusConn` seam + `realConn` adapter, Backend struct,
  New, Name/Close/Remote, `isIncusNotFound`.
- `lifecycle_linux.go` / `status_linux.go` / `spec_linux.go` / `image_linux.go` —
  Apply/Start/Stop/Restart/Delete, Status/List, DeploySpec→InstancesPost, OCI
  image source.
- `stats_linux.go` / `copy_linux.go` / `logs_linux.go` / `forwardport_linux.go` /
  `exec_linux.go` — the data plane.
- `incushost_linux_test.go` — the fake `incusConn` (models instance lifecycle,
  config keys, proxy-device host-port conflicts, an in-memory FS, console logs)
  and ~27 unit tests.
- Wiring: `pkg/server/server.go`, `cmd/cornus/commands.go`, `pkg/e2e/target.go`,
  `pkg/e2e/preflight.go`, `cmd/cornus-e2e/main.go`, `Makefile`.

## Test Coverage

Unit (no live daemon): recreate Apply + replica-0-only ports, ErrNotFound
wrapping, delete-if-exists, Stop/Start, origin round-trip, List grouping,
host-port conflict, image-source mapping, cp (stat/file/recursive-dir/round-trip),
Logs stdcopy framing + malformed-since, pickIPv4, exec buildExecPost/lifecycle/
unknown-id/resize-noop, Attach not-supported. Integration (Apply against real
images, streaming exec/logs, cp, port-forward) is E2E-gated via `make e2e-incus`.

## Pitfalls

- The Incus file API carries no size and no symlink target — do not assume a cheap
  stat; StatPath drains the body.
- Console logs have no timestamps or stdout/stderr split — `--since`/Follow/Tail
  cannot be honored; warn per-field, never silently drop (contract).
- Delete must stop the instance first (Incus refuses to delete a running one).
- Restart policy maps to `boot.autorestart` (bool) — no attempt cap
  (`RestartMaxAttempts` inexpressible, like containerd). Exact crash-restart
  semantics for OCI containers are E2E-gated (why `lifecycle-restart` is not in
  `SCENARIOS_INCUS`).

## Deferred (blocked on a live incusd / companion)

`MountingBackend`/`EgressBackend` caretaker companion (client-local mounts,
client-side egress), `RemoteCapable` remote-companion realization, and the
Phase-0 OCI-registry-pull spike. Without the companion, incus simply does not
advertise those optional capabilities (like dockerhost without remote mode).
Also pending: rerun `audit-licenses` if the dependency set changes.
