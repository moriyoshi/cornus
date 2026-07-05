# Deploy backends

The cornus deploy engine applies a [deploy spec](/reference/deploy-spec) — a native `deploy.yaml`, or a Compose file / devcontainer translated into one — to one of **four interchangeable backends**. They all sit behind the same interface and are selected with the `CORNUS_DEPLOY_BACKEND` environment variable (env-only; there is no CLI flag).

| `CORNUS_DEPLOY_BACKEND` | Target | Networking | Notes |
| --- | --- | --- | --- |
| `dockerhost` (default) | Local Docker daemon | Docker networks | Needs the Docker socket (`/var/run/docker.sock`). |
| `containerd` | Bare containerd host, no dockerd | CNI bridge + portmap | Linux-only; needs root + CNI plugins. |
| `bare` | An OCI runtime CLI (runc/crun/youki) directly — **no daemon** | CNI bridge + portmap | Linux-only; needs root + an OCI-runtime binary + CNI plugins. Cornus owns image pull, supervision, and cgroups itself. |
| `kubernetes` / `k8s` | A Kubernetes cluster (client-go) | Deployments + Services | Server / in-cluster only; RBAC-scoped. |

The selection applies to both the server (`cornus serve`) and a local [`cornus deploy`](/cli/deploy) run **without** a `--server`. The one exception is `kubernetes`, which is server/in-cluster only — a local `cornus deploy` with `CORNUS_DEPLOY_BACKEND=kubernetes` falls back to `dockerhost` with a warning.

All three honor the same core spec fields (`name` / `image` / `replicas` / `restart` / `env` / `ports` / `mounts`), the client-local 9P bind mounts, Compose user networks, and published-port forwarding, so the same workflow moves across them unchanged. Where an individual spec field maps onto only some backends, the [deploy spec reference](/reference/deploy-spec) says so per field.

Privilege handling is **default-deny**: privileged containers and host bind mounts are refused unless explicitly allowed (`CORNUS_ALLOW_PRIVILEGED`, `CORNUS_ALLOW_BIND_SOURCES`). See [Security and authentication](/guides/security).

## `dockerhost` (default)

Runs workloads as containers on a local Docker daemon. It needs the Docker socket (`/var/run/docker.sock`, overridable with `CORNUS_DOCKER_SOCK`). This is the richest backend: it maps the widest set of spec fields directly onto Docker's create-time and host-config options, and Compose user networks become real Docker user-defined networks (libnetwork gives DNS and per-network isolation natively).

Under [host-native re-export](/reference/server-env-vars#reusing-a-local-image-store) (the default on this backend) it **skips the registry pull** for an image the daemon already has (bare or loopback-host refs), since pulling it would round-trip through cornus's registry back to the same daemon; external refs (e.g. `docker.io/...`) are still pulled normally.

**Client-local bind mounts** normally realize by kernel-9p-mounting the caller's export directly on the cornus **server's** own host — the single-host fast path, which assumes the server is co-located with the Docker daemon it drives. Setting `CORNUS_DOCKER_REMOTE=1` opts into a caretaker-sidecar path instead (the same mechanism the `kubernetes` backend always uses): a companion `cornus caretaker` container performs the kernel 9P mount itself, and a Docker-managed volume with `rshared`/`rslave` propagation relays it into the app container — so the mount works even when the server does not share a filesystem with the daemon (e.g. `DOCKER_HOST=tcp://...`). This needs `CORNUS_AGENT_IMAGE` set to a cornus-embedding image, exactly like the existing egress-companion path on this backend. See [server env vars](/reference/server-env-vars) for `CORNUS_DOCKER_REMOTE` and `CORNUS_AGENT_IMAGE`.

In remote mode this companion is **always created per instance**, sharing the app container's network namespace, whether or not the deploy uses `--mount` — it is a "remote companion," not just a mount relay. That is also what makes [`cornus port-forward`](/cli/port-forward) and [`cornus tunnel`](/cli/tunnel) work at all under `CORNUS_DOCKER_REMOTE=1`: without the companion, the server has no route to the instance's own network to bridge either one, so both reroute through the companion's shared netns instead of dialing the instance directly. The same companion also lets [`cornus exec --forward-agent`](/cli/exec) forward a local ssh-agent into an exec session on any remote-mode instance.

## `containerd`

`CORNUS_DEPLOY_BACKEND=containerd` runs workloads **natively on a bare containerd host — no dockerd** — implementing the full deploy interface directly against the containerd v1 client. It is **Linux-only** (elsewhere the backend returns an unsupported error) and, like `dockerhost`, works both for the server and for a local `cornus deploy` without a server.

It needs:

- the containerd socket (`CORNUS_CONTAINERD_ADDRESS`, default `/run/containerd/containerd.sock`; the standard `CONTAINERD_ADDRESS` is honored as a fallback),
- **root** (it creates network namespaces and runs CNI plugins), and
- the standard CNI plugins installed (`bridge`, `portmap`, `host-local`, `loopback`; discovered via `CORNUS_CNI_BIN_DIR`, `CNI_PATH`, or `/opt/cni/bin`).

Workloads live in the `cornus` containerd namespace (`CORNUS_CONTAINERD_NAMESPACE`); backend state (volumes, logs, CNI config) lives under `<DataDir>/containerd/`.

- **Networking** is a plain CNI bridge with host-port publishing via portmap. Each compose network gets its own `/24` carved from `CORNUS_CNI_SUBNET_BASE` (default `10.4`); published ports DNAT to replica 0 only. Inter-container name resolution works via hosts-file sync (nerdctl-style). UDP port mappings are supported (unlike the kubernetes backend).
- **Image pulls** decide plain-HTTP-vs-TLS themselves: `localhost` registries are plain-HTTP automatically, and `CORNUS_CONTAINERD_INSECURE_REGISTRIES` (comma-separated `host[:port]`) extends that to explicit hosts. `CORNUS_CONTAINERD_SNAPSHOTTER` overrides the rootfs snapshotter (set `native` on overlay-backed hosts such as docker-in-docker).
- **Logs** are kept under the data dir and rotated at `CORNUS_CONTAINERD_LOG_MAX_BYTES` (default 16 MiB, one old generation kept), and survive cornus restarts. **Restart policy** is delegated to containerd's restart-monitor plugin.

Pair it with the containerd **build worker** (`CORNUS_BUILD_WORKER=containerd`) so builds delegate execution, snapshots, and content to the same host containerd — a tagged build then lands in the host's image store directly, so a just-built image deploys without a registry round trip. Note the lazy build-context path (`--lazy` / `CORNUS_LAZY_BUILD`) is **not** supported on the containerd worker.

**Client-local bind mounts**, like `dockerhost`, default to a single-host kernel-9p fast path. `CORNUS_CONTAINERD_REMOTE=1` opts into the same caretaker-sidecar mechanism (a companion `cornus caretaker` container/task performs the kernel 9P mount, propagated into the app container via a shared host directory with `rshared`/`rslave` OCI mount options), needing `CORNUS_AGENT_IMAGE`. Unlike `dockerhost`, this does **not** add true remote-daemon support: containerd's client dialer only ever speaks to a local unix socket, so this backend is unconditionally co-located with the cornus server regardless of the flag — the sidecar mechanism is worth having anyway (it avoids the server itself needing kernel-mount privilege, and is the substrate future features can reuse), but it is not a path to a non-co-located containerd host.

As with `dockerhost`, `CORNUS_CONTAINERD_REMOTE=1` always creates this companion per instance (joining the app's pinned network namespace), with or without `--mount`, and for the same reason: it is what reroutes [`cornus port-forward`](/cli/port-forward)/[`cornus tunnel`](/cli/tunnel) and enables [`cornus exec --forward-agent`](/cli/exec) once `ForwardPort`'s normal direct-IP dial is in play — here that just avoids the server needing route/permission to dial into the CNI bridge network directly, distinct from the (unresolved) true-remote-daemon question above.

**Known gaps vs `dockerhost`:** attach is output-only, and healthchecks are ignored (with a warning). Rootless containerd is untested and unsupported for now.

## `bare`

`CORNUS_DEPLOY_BACKEND=bare` runs workloads **daemonlessly** — no dockerd *and* no containerd. Cornus drives a low-level **OCI runtime CLI** (`runc`, or `crun`/`youki`/`runsc` via `CORNUS_BARE_RUNTIME`) directly and owns everything a daemon otherwise provides: the image pull into an in-process content store, layer unpack + rootfs assembly, OCI `config.json` generation, **process supervision + restart policy**, cgroup lifecycle, and logging. It is effectively **cornus as its own Podman**. Like the other host backends it is **Linux-only** and works both for the server and a local `cornus deploy`. State lives under `<DataDir>/bare/`.

It needs:

- **root** (for the snapshotter mounts, network namespaces, CNI plugins, and the container cgroup),
- an **OCI-runtime binary** on `PATH` (`runc` default; validated at startup — a missing runtime fails fast with an actionable error), and
- the standard **CNI plugins** installed (`bridge`, `portmap`, `host-local`, `loopback`; discovered via `CORNUS_CNI_BIN_DIR`, `CNI_PATH`, or `/opt/cni/bin`).

Networking, hosts-file name resolution, and DataDir volumes behave **exactly as the `containerd` backend's** — the daemon-agnostic machinery is shared code (CNI bridge + portmap with a `/24` per compose network from `CORNUS_CNI_SUBNET_BASE`, published ports DNATed to replica 0, per-instance `/etc/hosts` sync, copy-when-empty volume seeding). In addition, an in-process resolver on the netns gateway answers guest DNS (disable with `CORNUS_BARE_DNS=false`). Image pulls decide plain-HTTP-vs-TLS themselves (`localhost` automatic, `CORNUS_BARE_INSECURE_REGISTRIES` extends it), and the rootfs snapshotter is overlay with a native fallback (`CORNUS_BARE_SNAPSHOTTER=native` on overlay-backed / docker-in-docker hosts).

What is unique to `bare` is that **cornus is the supervisor**. `runc create`/`start` returns immediately and runc's `/run` state is tmpfs, so cornus itself waits on each container's PID1 (via a pidfd), applies the restart policy (`no` / `on-failure[:N]` — which the containerd restart-monitor cannot express — / `always` / `unless-stopped`) with capped backoff, and relaunches. Two supervisor forms share that engine: an in-process one (default) and an opt-in **detached per-container shim** (`CORNUS_BARE_SHIM`, cornus's conmon analogue) that survives a cornus restart. A startup **reconcile** pass reattaches to survivors on a server restart and fully rebuilds workloads after a host reboot (the netns pins live on tmpfs, so a lost pin *is* the reboot signal). Per-instance state — image, snapshot, IPs, ports, restart policy, and desired-vs-observed status — is persisted as `<DataDir>/bare/records/<id>/record.json`, the store that replaces containerd's metadata DB.

Client-local bind mounts default to the same single-host kernel-9p fast path as the other host backends, with `CORNUS_BARE_REMOTE=1` opting into the caretaker-sidecar path (needs `CORNUS_AGENT_IMAGE`) — and, as on `dockerhost`/`containerd`, that companion is what reroutes [`cornus port-forward`](/cli/port-forward)/[`cornus tunnel`](/cli/tunnel) and enables [`cornus exec --forward-agent`](/cli/exec) in remote mode. The full optional-interface surface (`MountingBackend`, `EgressBackend`, `RemoteCapable`, volume removal) is implemented for parity with `containerd`.

**gVisor (`runsc`).** Setting `CORNUS_BARE_RUNTIME=runsc` runs each workload inside a gVisor sandbox. Because the sandbox owns the guest's cgroup accounting and filesystem, cornus adapts two operations automatically (detected from the runtime name; override with `CORNUS_BARE_STATS_SOURCE`): `cornus stats` reads the runtime's own metrics (`runsc events --stats`) instead of the host cgroup files, and `cornus cp` runs `tar` **inside** the container rather than through the host `/proc/<pid>/root`. Two caveats follow: `cornus cp` needs a `tar` binary in the image (scratch/distroless images cannot be copied), and per-container network counters are not reported (`cornus stats` shows zero network I/O). Everything else — supervision, restart policy, networking, volumes — is unchanged.

**Known gaps vs `dockerhost`:** attach is output-only and healthchecks are ignored (with a warning), as on `containerd`. Rootless is out of scope for now and errors clearly.

## `kubernetes` / `k8s`

`CORNUS_DEPLOY_BACKEND=kubernetes` (or `k8s`) deploys into a Kubernetes cluster using **client-go**, rendering each workload as a **Deployment** plus a **Service** for its published ports. It is **server / in-cluster only**: a local `cornus deploy` with this backend falls back to `dockerhost` with a warning. It is the backend the shipped Kubernetes manifests and Helm chart preset.

It is RBAC-scoped and namespaced (`CORNUS_K8S_NAMESPACE`), and it is the only backend that realises the advanced spec blocks — user networks via a pipeline of network drivers (`CORNUS_K8S_NET_DRIVER`: `services`, `bridge`/`ipvlan`/`macvlan` via Multus, `cilium`), the enforcing egress proxy, the per-pod caretaker DNS resolver, credential brokering, client-side egress relay, and the workload-to-workload [hub](/guides/hub) overlay. Rolling updates map onto the Deployment's `strategy.rollingUpdate`.

Because it deploys through the Kubernetes API rather than to the machine the CLI runs on, the kubernetes backend is what powers the [working with remote clusters](/guides/remote-clusters): a developer drives an in-cluster cornus server, and per-port forwarding or a SOCKS5 conduit brings the workload's ports back to the laptop.

`ForwardPort` (and so [`cornus port-forward`](/cli/port-forward)/[`cornus tunnel`](/cli/tunnel)) needs no companion sidecar here at all — it rides the Kubernetes API's own `pods/portforward` subresource directly. [`cornus exec --forward-agent`](/cli/exec) is supported too, but unlike the host backends' backend-wide remote mode it is **opt-in per deployment**: set `agentForward` in the [DeploySpec](/reference/deploy-spec) to fold an `AgentRelayRole` into the pod's caretaker (creating a minimal one if the pod has no other caretaker role). A deployment applied without it rejects `--forward-agent` with a clear error.

## Privilege posture

The backend that **runs workloads** and the in-process **build engine** have different privilege needs, and they are what determine how you run a Cornus server:

- A Cornus that **performs builds** needs elevation — the build engine runs runc + overlayfs + user namespaces. The registry and deploy subsystems on their own do not.
- The `dockerhost` backend needs the Docker socket; the `containerd` backend needs its socket, **root**, and CNI plugins; the `bare` backend needs **root**, an OCI-runtime binary, and CNI plugins (no daemon socket at all); the `kubernetes` backend runs in-cluster under RBAC.

```sh
# Simplest: run the container privileged (the shipped default).
#   compose: privileged: true   |   k8s: securityContext.privileged: true

# Rootless: run unprivileged with the prerequisites present, then:
cornus serve --rootless          # or CORNUS_ROOTLESS=1
```

Rootless needs `uidmap` (`newuidmap` / `newgidmap`), `rootlesskit`, and `slirp4netns` plus the appropriate `securityContext`. The image bundles `uidmap`. Some hosts (e.g. recent Ubuntu with `kernel.apparmor_restrict_unprivileged_userns=1`) need an AppArmor profile or a relaxed sysctl.

Note this is distinct from **workload** privilege, which is default-deny regardless of how the server runs: privileged containers and host bind mounts are refused unless explicitly allowed (`CORNUS_ALLOW_PRIVILEGED`, `CORNUS_ALLOW_BIND_SOURCES`; see [Security and authentication](/guides/security)).

## See also

- [`cornus deploy`](/cli/deploy) — the command that applies a spec.
- [Deploy spec reference](/reference/deploy-spec) — every field, and which backends honor it.
- [Server environment variables](/reference/server-env-vars) — `CORNUS_DEPLOY_BACKEND` and the per-backend knobs.
- [Working with remote clusters](/guides/remote-clusters) — driving the kubernetes backend from a laptop.
