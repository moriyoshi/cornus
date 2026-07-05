# Podman libpod REST API: measured findings (Phase 0 spike)

Measured against a **live Podman 5.8.2** (`quay.io/podman/stable`, linux/arm64)
running rootful inside a privileged Docker container with `--cgroupns=host` and a
writable `/sys/fs/cgroup`. Everything below is an observation from that daemon,
not a source reading. Where it contradicts the earlier source-derived research,
the measurement wins and the contradiction is called out.

Purpose: gate Phase 2 of the podman-backend plan (a `podmanEngine` alongside the
Docker `engineClient` in `pkg/deploy/dockerhost`).

## Reproducing

```sh
docker run -d --name podman-spike --privileged --cgroupns=host \
  --device /dev/fuse -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  quay.io/podman/stable:latest sleep infinity
docker exec -d podman-spike sh -c 'podman system service --time=0 unix:///tmp/podman.sock'
docker exec podman-spike curl -s --unix-socket /tmp/podman.sock http://d/v5.0.0/libpod/info
```

Two environment traps cost time and will cost it again:

- The image's `/etc/containers/containers.conf` ships `cgroups="disabled"`, so
  containers are created with `Cgroups=disabled` and **every stats call fails**
  with `this container does not have a cgroup` — on the compat endpoint too, so
  it is not a libpod defect. Create with `"cgroups_mode":"enabled"` (or the CLI's
  `--cgroups=enabled`).
- `touch /sys/fs/cgroup/x` fails with EPERM even when cgroups are fully writable;
  cgroupfs only accepts **directories**. Probe with `mkdir`, not `touch`.

## The headline: far more Docker-compatible than expected

**Four of the four `deploy.Backend` Docker-format obligations are satisfied by
libpod byte-for-byte, with one exception in stats.** The plan budgeted for
translation layers that turn out to be unnecessary.

| Obligation | Measured | Work needed |
|---|---|---|
| `Logs` stdcopy framing | **stdcopy-framed, byte-identical to compat** | none — pass through |
| `ExecStart` non-TTY framing | **stdcopy-framed** | none — pass through |
| `Attach` framing | **stdcopy-framed** | none — pass through |
| `CopyFrom`/`CopyTo`/`StatPath` | **`X-Docker-Container-Path-Stat` header + real tar** | none — pass through |
| `Stats` JSON | Docker-shaped **except `Id` vs `id`**, and no `networks` | small remap, see below |

Raw evidence for the framing, from `/v5.0.0/libpod/containers/logtest/logs`:

```
00000000  01 00 00 00 00 00 00 09  4f 55 54 2d 4c 49 4e 45  |........OUT-LINE|
00000010  0a 02 00 00 00 00 00 00  09 45 52 52 2d 4c 49 4e  |.........ERR-LIN|
00000020  45 0a                                             |E.|
```

`01`=stdout / `02`=stderr, 3 reserved bytes, big-endian uint32 length — Docker's
8-byte header exactly. The compat endpoint returns the identical bytes. Exec and
attach produce the same shape.

`cp` was flagged in the plan as *"the one Docker-format obligation with no in-tree
fallback"* and the highest remaining risk, because `containerdhost/tarcopy` packs
from a local rootfs a socket-based backend does not have. It is a non-issue:
`HEAD .../archive` returns the same base64 `X-Docker-Container-Path-Stat` header
cornus already parses (`{name,size,mode,mtime,isDir,linkTarget}`), `GET` returns a
valid tar, `PUT` returns 200.

## Stats: the one real divergence

`GET /v5.0.0/libpod/containers/{name}/stats?stream=false` returns Docker's stats
object — `cpu_stats.cpu_usage.{total_usage,usage_in_kernelmode,usage_in_usermode}`,
`system_cpu_usage`, `online_cpus`, `throttling_data`, `precpu_stats`,
`memory_stats`, `blkio_stats`, `pids_stats`, `num_procs`, `read`/`preread`.

Two differences from real Docker, both measured:

| | real Docker / compat | libpod |
|---|---|---|
| container id key | `id` | **`Id`** |
| `networks` | present | **absent** |
| `memory_stats` sub-keys | `usage`, `limit`, `stats{…}` | `usage`, `limit` only |

`pkg/deploy/internal/hostrun/stats.go`'s `DockerStats.ID` is tagged `json:"id"`,
and real Docker emits `id` (verified against the host's own Docker 29.2.1). So
**`podmanEngine` must not pass stats through verbatim** — decode and re-emit
(`hostrun.ToDockerStats`, or at minimum remap `Id`→`id`). Passing through would
blank the id for every consumer, silently.

The missing `networks` key means per-interface counters are unavailable from this
endpoint; `hostrun`'s `workingSet()` also has no `memory_stats.stats` to work
from and will fall back to raw usage.

## `dns_enabled` — the trap, confirmed and its consequence proven

`POST /v5.0.0/libpod/networks/create` with `{"name":"nodns","driver":"bridge"}`
returns `dns_enabled = False`. The same create through the **compat** endpoint
yields `dns_enabled = true`. The consequence, measured end to end:

```
network=nodns    resolve peer-nodns    -> NXDOMAIN
network=withdns  resolve peer-withdns  -> 10.89.1.2  peer-withdns.dns.podman …
```

Cornus's compose user networks depend entirely on container-name resolution, so
**every libpod network create must send `"dns_enabled": true`**. Note what this
implies for the test: a structural assertion that the network was created passes
in *both* cases. The regression test has to assert at the DNS lookup.

## Corrections to the earlier source-derived research

- **Container label filters use Docker syntax, not split syntax.** Measured on
  `GET /v5.0.0/libpod/containers/json`: `filters={"label":["cornus.app=demo"]}`
  → 1 match; `filters={"label":["cornus.app","demo"]}` → 0 matches. The split
  form the research described applies to `/libpod/images/json`, whose handler
  branches on `IsLibpodRequest` — it is image-specific. **Cornus's existing
  container label-filter code carries over unchanged.**
- Logs/exec/attach framing was listed as unknown-and-likely-needing-a-`stdcopy`
  wrap. It needs none.

## Confirmed as researched

| Claim | Measured |
|---|---|
| libpod routes are version-prefixed | `/libpod/info` → **404**; `/v5.0.0/libpod/info` → **200** |
| `/libpod/_ping` is unversioned and carries the version | `Libpod-Api-Version: 5.8.2`, `Api-Version: 1.44` |
| rootless detection field | `host.security.rootless` present (`False` here — rootful) |
| other info fields | `host.networkBackend=netavark`, `host.cgroupVersion=v2`, `host.rootlessNetworkCmd=pasta`, `version.*` PascalCase |
| compat API version in 5.8.x | **1.44** (matches the 3y8m-frozen-at-1.41 story) |
| `images/{name}/exists` | **204** present / **404** absent |
| image export default format | **OCI layout** (`blobs/sha256/…`); `?format=docker-archive` gives `<hash>.json` + `<layer>/layer.tar` + `VERSION` — so Phase 5 must pass it explicitly for go-containerregistry's `tarball` |
| already-connected connect | libpod **500**, compat **403**, and **200 (silent no-op)** when the container is stopped |
| exec create body | Docker-style PascalCase (`AttachStdout`, `Cmd`, `Tty`), returns `{"Id":…}`; exec inspect returns `{CanRemove, ContainerID, ExitCode, ID, …}` |

The already-connected 500 carries a clean discriminator in the response body's
`cause` field — literally `"network is already connected"` — which is a better
match target than the long `message`. Still prefer inspecting first; 500 is
otherwise indistinguishable from a genuine failure.

Status-code inventory worth pinning: container create **201**, start **204**,
libpod network create **200** (compat **201**), archive PUT **200**.

## Not measured

- **Rootless `rshared`/`rslave` mount propagation surviving into the app
  container** under rootless + fuse-overlayfs. The spike ran rootful; this needs a
  rootless podman. It gates the mount-relay companion only — **port-forward is
  unaffected**, needing just the shared netns.
- Anything about `ssh://` endpoints (Phase 6).

## Verdict

**Go.** The streaming surface is a pass-through, `cp` is a pass-through, and the
only translation needed anywhere is a one-field stats remap. The residual risks
are `dns_enabled` (mitigated by an explicit field plus a DNS-level test) and
rootless mount propagation (unmeasured, and it degrades a capability rather than
breaking the backend).
