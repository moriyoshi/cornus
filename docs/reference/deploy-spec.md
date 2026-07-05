# Deploy spec reference

The **deploy spec** is the declarative description of a workload cornus runs. It is the YAML (or JSON) document you pass to [`cornus deploy -f`](/cli/deploy). It is applied *imperatively*: one spec goes in, and the selected [deploy backend](/reference/deploy-backends) converges actual state to it (creating or recreating the workload).

A Compose file or devcontainer is translated into this same spec internally, so every field here is also reachable through [`cornus compose`](/cli/compose). The four backends — `dockerhost` (default), `containerd`, `bare`, and `kubernetes` — sit behind one interface and honor the same spec, but not every field maps onto every backend. Where the source records a per-backend behavior, it is called out in the field's description.

The canonical source of truth is [`pkg/.cornus/v1/deploy.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/.cornus/v1/deploy.go).

## Example

A reasonably complete spec, showing the common fields plus a few nested blocks:

```yaml
name: web
image: localhost:5000/web@sha256:1c2d...   # digest-pinned is ideal
replicas: 2
restart: unless-stopped

command: ["--port", "8080"]                 # args to the image ENTRYPOINT
env:
  LOG_LEVEL: info
  DATABASE_URL: postgres://db:5432/app

ports:
  - host: 8080
    container: 80
  - host: 127.0.0.1:5432                     # see hostIP below
    hostIP: 127.0.0.1
    container: 5432

mounts:
  - source: /srv/data
    target: /data
    readOnly: true

volumes:
  - name: web_cache                          # named => shared/persistent
    target: /var/cache
    size: 2Gi

networks:
  - name: myproj_frontend
    aliases: [web, frontend]

resources:
  cpuLimit: 0.5                              # half a core
  memoryLimit: 268435456                     # 256 MiB, in bytes
  reservedMemory: 134217728                  # 128 MiB floor

healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost/healthz"]
  interval: 30s
  timeout: 5s
  retries: 3

labels:
  app.kubernetes.io/part-of: myproj
```

## Top-level fields (`DeploySpec`)

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Uniquely identifies the deployment; managed resources are labeled with it for idempotent apply/delete. |
| `image` | string | yes | — | Image reference to run, ideally digest-pinned. |
| `command` | []string | no | image `CMD` | Overrides the image's default command (Docker `CMD`): the arguments to the image `ENTRYPOINT`, which stays in effect. On kubernetes it is carried in the container's `Args` so the image entrypoint is preserved. |
| `entrypoint` | []string | no | image `ENTRYPOINT` | Overrides the image entrypoint (Docker `ENTRYPOINT` / Kubernetes container `command`). When set, `command` supplies its arguments; empty keeps the image default. |
| `env` | map[string]string | no | — | Environment variables, applied as `KEY=VALUE` from the map. |
| `ports` | [][PortMapping](#portmapping) | no | — | Maps host ports to container ports. |
| `mounts` | [][Mount](#mount) | no | — | Bind host paths into the container. |
| `volumes` | [][VolumeSpec](#volumespec) | no | — | Managed (non-bind) volumes the backend provisions storage for. |
| `networks` | [][NetworkAttachment](#networkattachment) | no | — | User-defined networks this workload joins (Compose `networks:`). Empty means default connectivity only. |
| `proxy` | [ProxySpec](#proxyspec) | no | — | Requests a userspace enforcing egress proxy. **kubernetes only** (dockerhost gets isolation from libnetwork and ignores it). |
| `dns` | [DNSSpec](#dnsspec) | no | — | Requests a per-pod caretaker DNS resolver. **kubernetes only.** |
| `hub` | [HubSpec](#hubspec) | no | — | Joins the workload to the server's workload-to-workload overlay. **kubernetes only.** See [the workload hub](/topics/hub). |
| `docker` | [DockerSpec](#dockerspec) | no | — | Exposes a Docker Engine API endpoint to the workload. **kubernetes only.** Requires `CORNUS_CLIENT_TOKEN_SECRET` on the server. |
| `credentials` | [CredentialSpec](#credentialspec) | no | — | Brokers short-lived client-minted credentials into the workload. Realized on **kubernetes**; host backends via a companion caretaker; other backends warn and ignore. See [Credential brokering](/topics/credentials). |
| `restart` | string | no | `unless-stopped` | Restart policy: `no`, `always`, `on-failure`, or `unless-stopped`. |
| `restartMaxAttempts` | int | no | `0` (backend default, unlimited) | Caps restart attempts for an `on-failure` policy. **dockerhost only** (kubernetes and containerd cannot bound the count and ignore it). |
| `replicas` | int | no | backend default | Desired number of instances. Honored by every backend; on host backends published host ports go to replica 0 only. |
| `privileged` | bool | no | `false` | Runs with full privileges (Docker `--privileged` / Kubernetes `securityContext.privileged`). Opt-in; see [Auth and TLS](/topics/auth-and-tls) for the default-deny posture. |
| `healthcheck` | [Healthcheck](#healthcheck) | no | — | Container health probe. |
| `resources` | [Resources](#resources) | no | — | CPU/memory limits and reservations. |
| `updateConfig` | [UpdateConfig](#updateconfig) | no | — | Rolling-update strategy. **kubernetes only** (host backends recreate a single instance and ignore it). |
| `user` | string | no | image default | User (and optional group) the process runs as: `uid`, `uid:gid`, `user`, or `user:group`. kubernetes maps a **numeric** `uid[:gid]` only and cannot express a username. |
| `workingDir` | string | no | image default | Container working directory (compose `working_dir`). |
| `hostname` | string | no | backend default | Container hostname (compose `hostname`). |
| `labels` | map[string]string | no | — | User metadata. On kubernetes they become pod-template **annotations** (not labels); cornus's own management labels always win on a key clash. |
| `origin` | [Origin](#origin) | no | — | Workload **lineage**: the project it belongs to and the client host / user / directory / git repo it was spawned from. The CLI populates it automatically; the server stamps the authenticated subject. Reported back on status/list. |
| `stopSignal` | string | no | image default | Signal used to stop the main process, e.g. `SIGTERM`. dockerhost only; kubernetes and containerd ignore it. |
| `stopGracePeriod` | string | no | backend default | How long to wait after the stop signal before killing, as a Go duration (`10s`, `1m30s`). containerd ignores it. |
| `init` | bool (nullable) | no | backend default | `true` requests / `false` declines a PID-1 init reaping zombies (compose `init`). dockerhost only; kubernetes and containerd ignore it. |
| `tty` | bool | no | `false` | Allocates a pseudo-TTY (compose `tty`). |
| `stdinOpen` | bool | no | `false` | Keeps the container's stdin open (compose `stdin_open`). containerd ignores it. |
| `readOnly` | bool | no | `false` | Mounts the root filesystem read-only (compose `read_only`). |
| `capAdd` | []string | no | — | Add Linux capabilities (compose `cap_add`). |
| `capDrop` | []string | no | — | Drop Linux capabilities (compose `cap_drop`). |
| `securityOpt` | []string | no | — | Security options (compose `security_opt`). dockerhost passes them verbatim; kubernetes/containerd map only the well-known ones (`no-new-privileges`, `label=`) and warn on `seccomp=`/`apparmor=`. |
| `groupAdd` | []string | no | — | Supplementary groups (compose `group_add`). kubernetes/containerd accept **numeric GIDs only** and skip names with a warning. |
| `sysctls` | map[string]string | no | — | Namespaced kernel parameters (compose `sysctls`). |
| `extraHosts` | []string | no | — | Custom `/etc/hosts` entries as `host:ip` (compose `extra_hosts`). containerd ignores it. |
| `dnsServers` | []string | no | — | Custom nameservers (compose `dns`). Distinct from the `dns` caretaker field. containerd ignores it. |
| `dnsSearch` | []string | no | — | Custom DNS search domains (compose `dns_search`). containerd ignores it. |
| `dnsOptions` | []string | no | — | Custom resolver options (compose `dns_opt`), each `name` or `name:value`. containerd ignores it. |
| `ulimits` | [][Ulimit](#ulimit) | no | — | Per-resource rlimits (compose `ulimits`). kubernetes ignores it. |
| `tmpfs` | []string | no | — | tmpfs mounts, each a container path with optional `:`-separated options (e.g. `/run:size=64m`). |
| `devices` | []string | no | — | Host device mappings (compose `devices`), each `host:container[:perms]` (perms default `rwm`). kubernetes ignores it. |
| `shmSize` | int64 | no | `0` (backend default) | Size of `/dev/shm` in bytes (compose `shm_size`). |
| `pidMode` | string | no | backend default | PID namespace mode (compose `pid`), e.g. `host`. kubernetes/containerd map only `host`. |
| `ipcMode` | string | no | backend default | IPC namespace mode (compose `ipc`), e.g. `host`. kubernetes/containerd map only `host`. |
| `egress` | [EgressSpec](#egressspec) | no | — | Routes outbound traffic through a client-side vantage point. See [Client-side egress](/topics/egress). |
| `ingress` | [IngressSpec](#ingressspec) | no | — | Requests a public HTTP(S) Ingress fronting the workload's published port. **kubernetes** only (host backends warn and ignore). See [Ingress](/topics/ingress). |
| `knative` | [KnativeSpec](#knativespec) | no | — | Deploys the workload as a Knative Serving Service (serverless, autoscaling, scale-to-zero). Realized only on a **kubernetes** backend whose cluster serves `serving.knative.dev`; elsewhere it is warned about and ignored (the workload runs as an ordinary container). Usually populated by the `serving.knative.dev/v1` descriptor loader — see [`cornus deploy`](/cli/deploy). |
| `agentForward` | bool | no | `false` | Wires a caretaker `AgentRelayRole` for this deployment so `cornus exec --forward-agent` / `cornus compose exec --forward-agent` can relay a local ssh-agent into an exec session. **kubernetes only**, opt-in per deployment (dockerhost/containerdhost gate this instead on the backend-wide `CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE`, which already runs a per-instance companion for every deployment). Compose services set it with `x-cornus-agent-forward: true`. |
| `telemetry` | [TelemetrySpec](#telemetryspec) | no | — | Runs an embedded OpenTelemetry Collector in the caretaker and auto-wires the workload's `OTEL_*` env to it. All backends. Compose: `x-cornus-telemetry:` (service or project level); CLI: `--telemetry-*`. See [Observability](/guides/observability#workload-telemetry). |

::: tip
`restart` maps from Compose's `deploy.restart_policy.condition` (`none`→`no`, `on-failure`→`on-failure`, `any`→`always`), which is authoritative over the service-level `restart:` when the planner writes the spec.
:::

## Nested types

### Origin

The workload's lineage (`origin`) — where the deployment came from. The CLI fills every field but `subject` from the client environment (`cornus deploy` records the working directory; `cornus compose` records the project name and the Compose file's directory); the server overwrites `subject` with the authenticated request identity and **discards any client-supplied value**, so claimed origin and verified identity stay separate. All fields are best-effort. It is persisted per backend as `cornus.origin.*` container labels (dockerhost / containerd), record fields (bare), or object annotations (kubernetes), and reported back on [`cornus deploy`](/cli/deploy) / status / list.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `project` | string | no | — | Owning project — the Compose project name, or `cornus deploy --project`. |
| `host` | string | no | — | Client machine hostname the deploy was spawned from (client-attested). |
| `user` | string | no | — | Client OS user that spawned the deploy (client-attested). |
| `directory` | string | no | — | Absolute client-side directory the deploy was launched from (client-attested). |
| `git` | [GitOrigin](#gitorigin) | no | — | Git provenance of `directory`, when it is a repository. |
| `subject` | string | no | — | **Server-stamped** authenticated identity (JWT subject). Any value sent by the client is ignored; empty when auth is disabled. |

#### GitOrigin

Git provenance of the origin `directory` (`origin.git`), client-attested and best-effort.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `remote` | string | no | — | The `origin` remote URL. |
| `branch` | string | no | — | Checked-out branch (empty on a detached HEAD). |
| `commit` | string | no | — | Full HEAD commit SHA. |
| `dirty` | bool | no | `false` | The working tree had uncommitted changes at deploy time. |

### PortMapping

Maps a host port to a container port (`ports[]`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `host` | int | yes | — | Host port to publish. |
| `container` | int | yes | — | Container port to reach. |
| `protocol` | string | no | `tcp` | `tcp` or `udp`. |
| `hostIP` | string | no | `0.0.0.0` (all interfaces) | Restricts the host-side publish to a specific interface (compose `127.0.0.1:8080:80`). Honored by the host backends; kubernetes Services have no equivalent. |

### Mount

Binds a host source into the container (`mounts[]`). Distinct from a managed [`volumes`](#volumespec) entry.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `source` | string | yes | — | Host path to bind. |
| `target` | string | yes | — | Container path to mount it at. |
| `readOnly` | bool | no | `false` | Mount read-only. |
| `selinux` | string | no | — | SELinux relabel (compose `:z`/`:Z`): `z` shares the content among containers, `Z` makes it private. Applied by dockerhost; containerd/kubernetes do not relabel. |
| `immutable` | bool | no | `false` | Client-local, read-only mount whose contents remain unchanged for the deployment lifetime. Enables the server per-file cache. Ignored for server-host mounts. |
| `asyncCache` | bool | no | `false` | Client-local writable mount using the cache-coherent block protocol. Requires one replica and cannot combine with `readOnly` or `immutable`. Ignored for server-host mounts. |

### VolumeSpec

A managed (non-bind) volume mounted into the container (`volumes[]`). On kubernetes it becomes a dynamically-provisioned PersistentVolumeClaim; on dockerhost a Docker anonymous/named volume. On first start the volume is seeded with whatever the image ships at `target` (Docker volume semantics); subsequent starts preserve writes.

The `name` field selects the two Compose volume flavours:

- **Anonymous** (`name` empty): storage is private to this deployment and ephemeral — reaped when the deployment is deleted (like `docker rm -v`).
- **Named** (`name` set): a shared, project-scoped store whose lifecycle is independent of any one deployment; it **survives** `cornus delete` of any single deployment that uses it. Supply the already project-scoped logical name (e.g. `myproj_cache`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | no | anonymous | Set => shared/persistent named volume; empty => anonymous. |
| `target` | string | yes | — | Container mount path. |
| `size` | string | no | `1Gi` | Requested size, e.g. `1Gi`. |
| `storageClass` | string | no | cluster default class | Kubernetes StorageClass for the PVC. |
| `readOnly` | bool | no | `false` | Mount read-only. |
| `driver` | string | no | Docker default (`local`) | Volume plugin for a **named** volume (compose `driver`). dockerhost only; kubernetes/containerd ignore it. |
| `driverOpts` | map[string]string | no | — | Opaque driver options (compose `driver_opts`). dockerhost only. |
| `labels` | map[string]string | no | — | User metadata on a **named** volume. dockerhost sets them; kubernetes copies them onto the PVC (management labels win); containerd ignores them. |

### NetworkAttachment

One membership of a workload in a user-defined network (`networks[]`), modelled on Docker/Compose user-network semantics: a member is reachable by its service name (and any aliases) from other members of the **same** network, and — where the fabric supports it — isolated from networks it does not join.

`driver` selects how the kubernetes backend realises the network; empty takes the backend default (`CORNUS_K8S_NET_DRIVER`, itself defaulting to `services`). Recognised kubernetes drivers: `services` (DNS only, any cluster), `bridge`/`ipvlan`/`macvlan` (Multus CNI), `cilium`. The dockerhost backend passes `driver` straight through to Docker's own network drivers.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Project-scoped network resource name (e.g. `myproj_frontend`). |
| `driver` | string | no | `services` (kubernetes) / Docker bridge | Realisation driver (see above). |
| `driverOpts` | map[string]string | no | — | Opaque per-network knobs forwarded to the driver (compose `driver_opts`). |
| `aliases` | []string | no | — | Extra DNS names for this member on the network. |
| `default` | bool | no | `false` | Detached-primary mode on kubernetes: replaces the pod's primary interface (Multus default-network). At most one attachment may set it. dockerhost ignores it. |
| `ip` | string | no | — | Pins the member's IPv4 address on this network, in CIDR form (e.g. `10.222.14.7/24`). Multus-realised networks only; dockerhost ignores it (libnetwork addresses natively). |
| `subnet` | string | no | — | Network IPAM subnet (compose `ipam.config[0].subnet`). dockerhost and the Multus netdriver use it; containerd ignores it. |
| `gateway` | string | no | — | Network IPAM gateway. dockerhost only. |
| `ipRange` | string | no | — | Network IPAM IP range. dockerhost only. |
| `internal` | bool | no | `false` | Restricts the network to intra-network traffic with no external egress (compose `internal`). dockerhost only. |
| `attachable` | bool | no | `false` | Allows standalone containers to join a swarm-scoped network (compose `attachable`). dockerhost only. |
| `enableIPv6` | bool | no | `false` | Turns on IPv6 addressing (compose `enable_ipv6`). dockerhost only. |
| `labels` | map[string]string | no | — | User metadata on the network. dockerhost only (management labels win). |
| `ipv6` | string | no | — | Pins this member's per-network IPv6 address (compose `ipv6_address`). dockerhost only. |
| `mac` | string | no | — | Pins this member's MAC address (compose `mac_address`). dockerhost only. |
| `priority` | int | no | `0` | Orders network attachment (compose `priority`): highest-priority network is joined first and its gateway becomes the default route. dockerhost only. |

### ProxySpec

Configures the userspace egress proxy for a workload (`proxy`). **kubernetes only.** `allow` is the set of peer service names the workload may reach (services sharing a proxy network).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `mode` | string | no | `enforcing` | `enforcing` (all outbound TCP redirected to an nftables sidecar that permits only destinations resolving to an `allow` peer — real L4 isolation) or `cooperative` (soft isolation: each `allow` peer's DNS name points at a loopback address the sidecar forwards; bypassed by dialing a raw pod IP). |
| `allow` | []string | no | — | Peer service names the workload may reach. |
| `ports` | map[string][]int | no | — | Cooperative mode: per `allow` peer, the container ports to proxy. |
| `listenPort` | int | no | backend default | Port the sidecar listens on for redirected traffic. |

### DNSSpec

Configures the per-pod caretaker DNS resolver (`dns`). **kubernetes only.** `records` maps a peer service name to the IPv4 address the pod should resolve it to (typically the peer's user-network / Multus-secondary address). Everything not in `records` is forwarded to the cluster DNS.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `records` | map[string]string | no | — | Peer service name → IPv4 address to resolve it to. |
| `requireUserNet` | bool | no | `false` | Marks records that point at Multus secondary addresses. When the cluster cannot realise the Multus fabric, the backend skips the DNS caretaker entirely and resolution degrades to the cluster DNS. |

### DockerSpec

Configures the caretaker's Docker Engine API endpoint (`docker`). **kubernetes only.** The caretaker binds a Docker-API proxy on a pod-loopback endpoint and injects `DOCKER_HOST` so stock `docker` / `docker compose` drive the same cornus server that manages the pod's own stack. Requires a client-scoped token Secret on the server (`CORNUS_CLIENT_TOKEN_SECRET`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `transport` | string | no | `tcp` | `tcp` (binds `127.0.0.1:port`), `unix` (binds a socket at `socketPath`), or `both` (`DOCKER_HOST` then points at the TCP endpoint). |
| `port` | int | no | `2375` | Loopback TCP port for the `tcp` / `both` transports. |
| `socketPath` | string | no | `/cornus/docker/docker.sock` | Unix socket path for the `unix` / `both` transports (on a shared emptyDir). |
| `envVar` | string | no | `DOCKER_HOST` | Environment variable used to advertise the endpoint to the app container. |

### TelemetrySpec

Runs an embedded OpenTelemetry Collector in the caretaker (compose `x-cornus-telemetry:`, service or project level; CLI `--telemetry-*`). The app sends OTLP to a pod-loopback receiver and the Collector exports it to `endpoint`; the backend injects the workload's `OTEL_*` env automatically. All backends. See [Observability](/guides/observability#workload-telemetry). Requires the collector-enabled image (`-tags otelcol`, set in the released image).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | Turns telemetry on. A non-empty `endpoint` implies it (a bare `x-cornus-telemetry: {}` needs an endpoint too). |
| `endpoint` | string | yes | — | The external OTLP backend to export to (`host:port` for grpc, URL for http/protobuf). |
| `protocol` | string | no | `grpc` | Exporter protocol: `grpc` or `http/protobuf` (also selects the receiver port advertised to the app: 4317 vs 4318). |
| `headers` | map[string]string | no | — | Static export headers (e.g. an auth token). On **kubernetes** projected via a Deployment-owned Secret + `secretKeyRef`, so no value appears in the pod spec. |
| `insecure` | bool | no | `false` | Disable transport security to the backend (plaintext / no cert verification). |
| `signals` | []string | no | all | Restrict pipelines to `traces`, `metrics`, and/or `logs`. |
| `serviceName` | string | no | deployment name | Override `OTEL_SERVICE_NAME` injected into the app (a user-set env wins). |
| `resourceAttributes` | map[string]string | no | — | Extra `OTEL_RESOURCE_ATTRIBUTES` merged with cornus-derived defaults (a user-set env wins). |
| `grpcPort` / `httpPort` | int | no | `4317` / `4318` | OTLP receiver loopback ports inside the pod. |
| `debug` | bool | no | `false` | Also log collected telemetry to the collector stdout (troubleshooting). |

### HubSpec

Requests workload-to-workload overlay membership (`hub`). **kubernetes only.** See [the workload hub](/topics/hub).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `identity` | string | no | deployment name | Policy identity. |
| `export` | [][HubExport](#hubexport-hubimport-hubimportdynamic) | no | — | Services this workload hosts on the overlay. |
| `import` | [][HubImport](#hubexport-hubimport-hubimportdynamic) | no | — | Services this workload reaches through the overlay. |
| `importDynamic` | [HubImportDynamic](#hubexport-hubimport-hubimportdynamic) | no | — | Opts the workload into dynamic import discovery. |

#### HubExport / HubImport / HubImportDynamic

**`HubExport`** — one service this workload hosts on the overlay:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Service name on the overlay. |
| `port` | int | yes | — | Port the service listens on. |
| `deliver` | bool | no | `false` | Requests ingress delivery (the hub relays to this pod, which dials `port` on localhost) so the service need not be reachable from the hub. |
| `protocol` | string | no | `tcp` | `tcp` or `udp`. |

**`HubImport`** — one service this workload reaches through the overlay:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Service name to reach. |
| `ports` | []int | yes | — | Ports to bind a loopback listener for. |
| `protocol` | string | no | `tcp` | `tcp` or `udp`. |

**`HubImportDynamic`** — subscribes to hub catalog pushes and binds a loopback listener at the synthetic IP of **every** cataloged service (excluding this workload's own exports and static imports), adding/closing listeners as services appear and vanish. No DNS records are wired (names are unknown at deploy time):

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `ports` | []int | yes | — | Shared port set bound per discovered service. |
| `protocol` | string | no | `tcp` | `tcp` or `udp`. |

### CredentialSpec

Brokers client-sourced credentials into a workload (`credentials`). The secret value is minted on the client (never carried in this spec) and delivered through the cornus server and the caretaker sidecar. Realized on **kubernetes** over a foreground `cornus deploy --server` session; host backends via a companion caretaker; `--detach` and other backends reject/ignore it. See [Credential brokering](/topics/credentials).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `sources` | [][CredentialSource](#credentialsource) | no | — | Each entry is one credential the container can retrieve on demand. |

#### CredentialSource

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Logical credential name. Doubles as the capability key and default file basename / endpoint path segment. |
| `backend` | string | yes | — | Client-side backend that mints the credential (e.g. `aws-sts`, `static`, `exec`). Runs on the caller's machine with the caller's own cloud/API credentials. |
| `config` | map[string]string | no | — | Non-secret backend configuration (e.g. `role_arn`, `duration`, `region`). Must never hold the secret itself. |
| `ttl` | string | no | backend default | Client-side cache/refresh hint, a Go duration string. |
| `deliver` | [][CredentialDelivery](#credentialdelivery) | no | — | How the container consumes the credential. Empty is valid (fetchable but not surfaced). |

#### CredentialDelivery

One provider-agnostic way to surface a credential to the container.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `kind` | string | no | `endpoint` | `endpoint` (an HTTP metadata server / auth-injecting proxy), `file` (materialize to a path in a shared volume), or `env` (inject into the app container's environment). |
| `provider` | string | no | `generic` | **endpoint kind.** `generic` serves the cornus-native JSON contract (`GET /credentials/<name>`); `aws-imds` and future adapters render the same credential in a cloud SDK's expected shape. |
| `wellKnown` | bool | no | `false` | **endpoint kind.** Binds the provider's canonical link-local address (e.g. AWS `169.254.169.254`, IMDSv2) inside the pod netns. Needs `NET_ADMIN`; when false the endpoint binds loopback and is advertised via an injected env var (for `aws-imds`, `AWS_CONTAINER_CREDENTIALS_FULL_URI` — the ECS container-credentials endpoint). |
| `upstream` | string | no | provider default | **endpoint kind, auth-proxy providers.** Overrides the vendor API the proxy forwards to (e.g. an Anthropic-/OpenAI-compatible gateway). Non-secret. |
| `path` | string | no | — | **file kind.** Container path to materialize the credential to. |
| `format` | string | no | `json` | **file kind.** `json` (the neutral `{values,expiration}` object), `env` (`KEY=VALUE` lines), `raw` (a single value), or `aws-credentials` (an ini profile). |
| `envVar` | string | no | — | **env kind.** App-container environment variable to set. Fetched once at deploy time into a Kubernetes Secret (`secretKeyRef`) — static, no runtime refresh, lives in etcd. Prefer proxy/file delivery for short-lived credentials. |
| `valueKey` | string | no | `value` then `token` | **env kind.** Which credential values key supplies the env value. |

### Healthcheck

A container health probe (`healthcheck`), modelled on Docker's healthcheck. On dockerhost it becomes the Docker container healthcheck; on kubernetes an exec liveness (and readiness) probe. `test` uses Docker's `CMD` form: first element is `CMD` (exec the rest), `CMD-SHELL` (run the single string via the shell), or `NONE` (disable any inherited healthcheck).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `test` | []string | no | — | Probe command in Docker `CMD` form (see above). |
| `interval` | string | no | backend default | Probe interval, a Go duration string (`30s`). |
| `timeout` | string | no | backend default | Per-probe timeout, a Go duration string. |
| `startPeriod` | string | no | backend default | Grace period before failures count, a Go duration string. |
| `startInterval` | string | no | backend default | Probe interval **during** the start period (compose `start_interval`). |
| `retries` | int | no | backend default | Consecutive failures before unhealthy. |

::: warning containerd
The containerd backend ignores healthchecks (with a warning).
:::

### Resources

Caps a workload's compute (the `*Limit` fields) and/or reserves a guaranteed floor (the `reserved*` fields, from compose `deploy.resources.reservations`). A zero field means "unset on that axis".

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `cpuLimit` | float64 | no | `0` (unset) | Fractional core count (e.g. `0.5` = half a core). Docker `NanoCpus`; kubernetes CPU quantity in millicores. |
| `memoryLimit` | int64 | no | `0` (unset) | Byte count. Docker `Memory`; kubernetes memory quantity. |
| `reservedCpu` | float64 | no | `0` (unset) | Reservation floor. kubernetes `resources.requests.cpu`; **no-op on dockerhost** (Docker has no CPU reservation); containerd ignores it. |
| `reservedMemory` | int64 | no | `0` (unset) | Reservation floor. kubernetes `resources.requests.memory`; dockerhost `MemoryReservation`; containerd ignores it. |

### UpdateConfig

The rolling-update strategy (`updateConfig`, from compose `deploy.update_config`). **Only kubernetes maps it**, onto the Deployment `strategy.rollingUpdate`. The other compose knobs (`delay`, `monitor`, `max_failure_ratio`) are swarm concepts a Deployment cannot express and are dropped at translate time.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `parallelism` | int | no | `0` (backend default of 1) | How many instances to update at once. Sizes `maxUnavailable` (stop-first) or `maxSurge` (start-first). |
| `order` | string | no | `stop-first` | `stop-first` (take an old instance down before bringing a new one up) or `start-first` (surge a new instance up before removing the old). |

### Ulimit

One process resource limit (`ulimits[]`, compose `ulimits`). Compose's shorthand (a bare integer) sets `soft == hard`. dockerhost `HostConfig.Ulimits`; containerd OCI `Process.Rlimits`; kubernetes ignores it.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Bare limit name (`nofile`, `nproc`). |
| `soft` | int64 | no | — | Soft bound. |
| `hard` | int64 | no | — | Hard bound. |

### EgressSpec

Routes a workload's **outbound** traffic through a client-side vantage point (`egress`) — for air-gapped clusters or VPN/corporate-proxy/SASE networks where the sanctioned egress path lives on the caller's side. See [Client-side egress](/topics/egress).

Routing is per destination: each flow is sent to one of four routes — `client` (relay to the client-side network), `gateway` (relay to a durable egress-gateway node, for `--detach`), `cluster` (egress directly, no relay), or `deny` (drop). `default` applies to unmatched destinations and defaults to `cluster`, so enabling egress never silently diverts in-cluster traffic — you opt destinations **out** to the client/gateway.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `mode` | string | no | `env` | `env` (propagate `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`ALL_PROXY` into the container — every backend, no relay), `proxy` (caretaker runs an HTTP CONNECT + SOCKS5 forward proxy relayed back through the server — kubernetes now, host backends via a companion caretaker), or `transparent` (all outbound TCP captured by an nftables redirect and relayed — kubernetes now). |
| `gateway` | string | no | — | **Reserved; must be empty today.** The `gateway` route currently egresses through the cornus server itself; a non-empty value is rejected by validation. |
| `proxies` | map[string]string | no | client-resolved | Mode `env`: explicit proxy variables to inject. Empty asks the client to resolve its own OS proxy configuration at deploy time. |
| `rules` | [][EgressRule](#egressrule) | no | — | Declarative routing policy: an ordered list, first-match-wins, falling back to `default`. Superseded by `script`. |
| `script` | string | no | — | Optional PAC-style JavaScript (`FindProxyForURL`) that decides the route per destination. When set it supersedes `rules`: `DIRECT`→`cluster`, `PROXY client`/`PROXY gateway`→relay routes, `DENY`→drop, no match→`default`. |
| `default` | string | no | `cluster` | Route for destinations no rule/script matches: `cluster`, `client`, `gateway`, or `deny`. |
| `listenPort` | int | no | backend default | Caretaker proxy's listen port (modes `proxy` and `transparent`). |

Modes `proxy` and `transparent` tunnel traffic back through the client and therefore require a live deploy-attach session (they cannot be used with a stateless `--detach` deploy); `env` does not.

#### EgressRule

Maps a destination to a route (`egress.rules[]`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `pattern` | string | yes | — | Matches the destination host (glob, e.g. `*.internal`), a CIDR (e.g. `10.0.0.0/8`), and/or an explicit port (e.g. `api.example.com:443`, `10.0.0.0/8:5432`). An empty host or port part matches any. |
| `route` | string | yes | — | One of `client`, `gateway`, `cluster`, or `deny`. |

### IngressSpec

Requests a public HTTP(S) Ingress fronting the workload's `ClusterIP` Service (`ingress`). **Kubernetes-backend only** — the spec must publish at least one port (that Service is the Ingress backend); `dockerhost` / `containerd` warn and ignore it. See [Ingress](/topics/ingress).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | Turns ingress on. A non-empty `hosts` (or the Compose `host:`) implies `enabled`; a bare `x-cornus-ingress: {}` enables it with every field defaulted. |
| `hosts` | []string | no | derived | External hostnames; each becomes its own Ingress rule sharing one TLS entry. `@` maps to the apex (the base domain itself, no `<name>.` prefix). Empty derives a single `<subdomain>.<domain>` host; neither a host nor a base domain is rejected. |
| `domain` | string | no | `CORNUS_INGRESS_DOMAIN` | Client override of the base domain used to auto-derive the host when `hosts` is empty. A server may enforce that resolved hosts stay within its domain (`CORNUS_INGRESS_ENFORCE_DOMAIN`). |
| `subdomain` | string | no | deployment name | Label(s) prefixed to the base domain when auto-deriving (`<subdomain>.<domain>`). The Compose translator sets `<service>.<project>`. Sanitized to DNS-1123. |
| `path` | string | no | `/` | HTTP path prefix to route. |
| `pathType` | string | no | `Prefix` | Kubernetes path match type: `Prefix`, `Exact`, or `ImplementationSpecific`. |
| `port` | int | no | first published | Container port the ingress routes to. Non-zero must match one of the spec's published ports. |
| `className` | string | no | `CORNUS_INGRESS_CLASS`, then cluster default | `IngressClassName` for the Ingress. |
| `annotations` | map[string]string | no | — | Merged verbatim onto the Ingress object, for controller-specific knobs. |
| `tls` | [IngressTLS](#ingresstls) | no | — | When set, requests HTTPS for the host(s); omit for plain HTTP. |

#### IngressTLS

Configures HTTPS for the ingress host(s) (`ingress.tls`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `secretName` | string | no | `<name>-tls` | Existing TLS secret to serve. The default is provisioned by cert-manager when `clusterIssuer` (or the server default) is set. |
| `clusterIssuer` | string | no | `CORNUS_INGRESS_TLS_ISSUER` | Sets the `cert-manager.io/cluster-issuer` annotation so cert-manager provisions the certificate. |

### KnativeSpec

Deploys the workload as a Knative Serving Service (`knative`). Realized only on a **kubernetes** backend whose cluster serves `serving.knative.dev` — the backend then emits a `serving.knative.dev/v1` Service instead of a Deployment plus Service, so Knative owns autoscaling, scale-to-zero, and the Route. On a plain cluster or the `dockerhost` / `containerd` / `bare` backends it is warned about and ignored. Most often set by the Knative descriptor loader when you `cornus deploy -f service.yaml`; see [`cornus deploy`](/cli/deploy).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `enabled` | bool | no | `false` | Marks the workload as a Knative Service. A bare `{}` enables it with every field defaulted. |
| `minScale` | int | no | `0` | Autoscaling floor (`autoscaling.knative.dev/minScale`). `0` permits scale-to-zero. |
| `maxScale` | int | no | `0` | Autoscaling ceiling (`autoscaling.knative.dev/maxScale`). `0` means unlimited. |
| `target` | int | no | — | Autoscaling target per replica (`autoscaling.knative.dev/target`): concurrent requests, or requests-per-second for the `rps` metric. |
| `concurrency` | int | no | `0` | Hard limit on simultaneous requests per replica (revision `containerConcurrency`). `0` means unlimited. |
| `class` | string | no | cluster default | Autoscaler class: `kpa` (Knative Pod Autoscaler) or `hpa`. |
| `metric` | string | no | `concurrency` | Scaling metric: `concurrency`, `rps`, or `cpu` (`cpu` requires `class: hpa`). |
| `timeoutSeconds` | int | no | `300` | Maximum duration of a single request (revision `timeoutSeconds`). |
| `port` | int | no | first published | The single container port Knative routes to. Non-zero must match one of the published ports. |
| `annotations` | map[string]string | no | — | Merged onto the revision template for autoscaling knobs beyond the fields above (the fields win on a collision). |

## See also

- [`cornus deploy`](/cli/deploy) — the command that applies a spec.
- [Deploy backends](/reference/deploy-backends) — how `dockerhost`, `containerd`, `bare`, and `kubernetes` realise these fields.
- [Client-side egress](/topics/egress) — the `egress` block in depth.
- [Ingress](/topics/ingress) — the `ingress` block in depth.
- [Credential brokering](/topics/credentials) — the `credentials` block in depth.
- [The workload hub](/topics/hub) — the `hub` block and the workload-to-workload overlay.
