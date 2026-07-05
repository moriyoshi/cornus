# Server environment variables

This page lists the `CORNUS_*` environment variables read by [`cornus serve`](/cli/serve) and the server subsystems. Some correspond to `cornus serve` flags (noted below); most are env-only knobs read directly by the server, deploy backends, build engine, and tunnels.

::: info
This list is derived from the source tree (`grep 'CORNUS_[A-Z0-9_]+' pkg cmd`) and is meant as a practical reference. It may include a few internal or evolving knobs; the authoritative behavior always lives in the code. Test-only variables (`CORNUS_TEST_*`) are omitted. Client-side variables consumed by the CLI (not the server) are grouped separately at the end.
:::

## General / listener

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_ADDR` | `--addr` | `:5000` | HTTP listen address for `/v2/*` and `/.cornus/v1/*`. |
| `CORNUS_DATA` | — | platform data dir | The server data directory (registry filesystem store, uploads, backend state). |
| `CORNUS_ROOTLESS` | `--rootless` | off | Run the build engine in rootless mode (user namespaces). |
| `CORNUS_LOG_LEVEL` | — | `info` | Log verbosity (`debug`, `info`, `warn`, `error`). |
| `CORNUS_ADVERTISE_URL` | — | — | The cornus URL a mount-agent / caretaker sidecar dials back to. Required for client-local mounts on the kubernetes backend, and on `dockerhost`/`containerd` when `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` opts into the same sidecar path. |
| `CORNUS_ADVERTISE_REGISTRY` | — | derived | Overrides the `host[:port]` (and optional scheme) the server advertises to clients as the registry deploy targets can pull from (`GET /.cornus/v1/info`). |
| `CORNUS_REPLICA_ID` | — | — | Stable identity for this replica; used by the distributed hub store and the GC leader gate. |

## Storage

See [Storage backends](/reference/storage-backends) for the full backend catalog.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | filesystem under the data dir | Registry persistence backend: a path, `file://`, `mem://`, `s3://bucket?region=&endpoint=&path_style=`, or (behind `-tags cloudblob`) `gs://` / `azblob://`. |

## Remote 9P file cache and writable mounts

These settings control the cache used for immutable client-local mounts and the optional coherence features for writable `,async` mounts. The file cache is server-only. Coherence flags must be set in both the server environment and the deploy-caller environment because the endpoints negotiate their shared feature set.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_FILE_CACHE` | `--file-cache` | off | Enable the on-disk per-file cache for immutable remote reads. |
| `CORNUS_FILE_CACHE_DIR` | `--file-cache-dir` | — | Required directory for cache files. Use a dedicated volume rather than the server data directory. |
| `CORNUS_FILE_CACHE_CHUNK_SIZE` | `--file-cache-chunk-size` | `1048576` | Cache block size in bytes. |
| `CORNUS_FILE_CACHE_MAX_BYTES` | `--file-cache-max-bytes` | unlimited | Soft cache-size cap enforced by garbage collection. |
| `CORNUS_BLOCK_COHERENCE` | — | classic | Comma- or space-separated `subhash`, `defer`, and `subfill` options (`subfill` implies `subhash`). Empty keeps the classic protocol. |
| `CORNUS_BLOCK_READAHEAD` | — | off | Byte cap for adaptive speculative prefetch under `subfill`, for example `64k` or `262144`. It is proxy-side only. |

## Authentication and API policy

See [Auth and TLS](/topics/auth-and-tls) for the auth model. With no auth env set, the server accepts requests without a credential.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_AUTH_TOKEN` | — | — | A static bearer token accepted as a credential. |
| `CORNUS_TLS_CERT` | `--tls-cert` | — | PEM certificate file; serve HTTPS when set with `--tls-key`. |
| `CORNUS_TLS_KEY` | `--tls-key` | — | PEM private-key file; serve HTTPS when set with `--tls-cert`. |
| `CORNUS_TLS_CLIENT_CA` | `--tls-client-ca` | — | PEM CA bundle to verify client certificates (mTLS). A verified cert CommonName becomes the caller identity; presenting a cert stays optional. |
| `CORNUS_JWT_ISSUER` | — | — | Expected JWT `iss` claim. |
| `CORNUS_JWT_AUDIENCE` | — | — | Expected JWT `aud` claim (must match a client's `kube-auth.audience`). |
| `CORNUS_JWT_HS256_SECRET` | — | — | Shared secret for verifying HS256-signed JWTs. |
| `CORNUS_JWT_PUBLIC_KEY` | — | — | Path to a PEM public key (RSA→RS256, ECDSA→ES256) for verifying asymmetric JWTs. |
| `CORNUS_JWT_JWKS_FILE` | — | — | Path to a local JWKS document for JWT verification. |
| `CORNUS_JWT_JWKS_URL` | — | — | URL of a remote JWKS endpoint for JWT verification. |
| `CORNUS_API_POLICY` | — | — | Per-identity authorization policy for the `/.cornus/v1/*` surface. |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | Allow unauthenticated pulls from the registry even when auth is otherwise enabled. |
| `CORNUS_CLIENT_TOKEN` | — | — | A client-scoped token used by the caretaker Docker-API proxy to drive the client deploy API. |
| `CORNUS_CLIENT_TOKEN_SECRET` | — | — | Kubernetes Secret reference (`name/key`) holding the client-scoped token; required to enable the workload `docker:` block. |
| `CORNUS_CARETAKER_TOKEN` | — | — | A token that authenticates caretaker (sidecar) callbacks to the server. |
| `CORNUS_CARETAKER_TOKEN_SECRET` | — | — | Kubernetes Secret reference holding the caretaker token. |
| `CORNUS_CARETAKER_TLS_SECRET` | — | — | Kubernetes Secret holding TLS material for the caretaker. |

## Registry

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | filesystem | See [Storage](#storage) / [Storage backends](/reference/storage-backends). |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | Allow anonymous registry pulls (see [Authentication](#authentication-and-api-policy)). |
| `CORNUS_REGISTRY_MIRROR` | — | — | Turn a local registry miss into a pull-through proxy to this upstream host (e.g. `docker.io`). |
| `CORNUS_REGISTRY_MIRROR_CACHE` | — | on | Persist mirror-fetched content into the local store (pull-through cache). |
| `CORNUS_REGISTRY_SOURCE` | — | `host-native` on a host backend | Re-export the deploy backend's own local image store through `/v2/*` instead of a separate CAS. `host-native` resolves to the local Docker daemon under the `dockerhost` backend and the host containerd store under the `containerd` backend; it is the **default** on those host backends. `off` forces the classic persistent CAS. With no `--storage` the registry keeps **no separate content store**. Mutually exclusive with `CORNUS_REGISTRY_MIRROR`. See [Reusing a local image store](#reusing-a-local-image-store). |

### Reusing a local image store

When you develop against a **local Docker or containerd host**, you already have
the image locally (from `docker build` / `docker pull`, or a cornus build), so
keeping a second copy in a separate cornus registry is redundant. So on a host
backend cornus's `/v2/*` registry **defaults to a view over that local store** —
`CORNUS_REGISTRY_SOURCE=host-native`, resolved per backend. In both cases no
separate CAS is kept (with no `--storage`), `_catalog` / tag listings reflect only
the local store, and image lifecycle is the runtime's job (`docker image prune`,
etc.):

- Under `containerd`, `/v2/*` is backed by the host containerd's **native content
  store** directly — a full **read-write** view. A `cornus build` that pushes to
  `/v2/*` imports straight into that store (blobs by digest + an image record), so
  the image is immediately deployable; a pull re-exports from it. No build-worker
  configuration is needed.
- Under `dockerhost`, `/v2/*` is a **read-only** view of the local Docker daemon:
  a manifest/blob miss is served via `docker save`, and a deploy of an image the
  daemon already has skips the registry pull. Because classic Docker has no
  digest-addressable content store to write blob-by-blob, a `/v2/*` **push is
  rejected `405`** — a `cornus build` instead routes through the server, which
  `docker load`s the result into the daemon. (So build with `cornus build` /
  `cornus compose build` against the server, not an in-process push.)

To keep the classic push-able CAS registry instead, set
**`CORNUS_REGISTRY_SOURCE=off`**, or pass an explicit **`--storage`** (which keeps
a CAS as the primary layer and re-exports only on a miss — a union view). A
configured `CORNUS_REGISTRY_MIRROR`, or a non-host backend (`bare`/`kubernetes`),
also keeps the classic CAS.

Intended for local development, not a high-fanout shared registry. One caveat for
the `dockerhost` view: `docker save` recomputes digests, so a manifest digest
learned from a prior push may differ from the re-exported one — pull by tag.
(The `containerd` view reads the native content store, so digests are preserved.)

## Garbage collection

Space is reclaimed on demand via `POST /.cornus/v1/gc` and, optionally, periodically.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_GC_INTERVAL` | — | disabled | Go duration (e.g. `1h`) for the background storage-GC scheduler. Unset disables it; a malformed or non-positive value is a startup error. Enable on at most one replica when several share one `s3://` store. |
| `CORNUS_GC_LEASE` | — | disabled | Enables a Kubernetes `coordination.k8s.io` Lease leader gate for periodic GC (`namespace/name`, or `kube` for the default `cornus-gc`). Requires `CORNUS_GC_INTERVAL` to be set. |

## Build engine

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_BUILD_WORKER` | — | in-process BuildKit | Selects the build worker; `containerd` delegates execution, snapshots, and content to the host containerd. |
| `CORNUS_BUILD_CONCURRENCY` | — | `NumCPU` | Number of concurrent `/.cornus/v1/build` executions permitted (non-positive/unparseable falls back to the default). |
| `CORNUS_MAX_BUILD_CONTEXT_BYTES` | — | — | Upper bound on an uploaded build context's size. |
| `CORNUS_BUILD_CACHE_KEEP_BYTES` | — | — | Target size for the build cache retained by GC. |
| `CORNUS_LAZY_BUILD` | — | off | Serve `--build-context` dirs on demand over 9P (lazy build) server-wide instead of syncing them eagerly. |
| `CORNUS_LAZY_9P` | — | — | Tunes the lazy 9P build-context / remote-snapshotter path. |
| `CORNUS_SNAPSHOTTER_TRACE` | — | off | Enables tracing of the remote snapshotter (diagnostics). |

## Deploy backend

See [Deploy backends](/reference/deploy-backends).

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | Selects the deploy backend: `dockerhost`, `containerd`, `bare`, or `kubernetes` / `k8s`. Env-only (no CLI flag). |
| `CORNUS_ALLOW_BIND_SOURCES` | — | deny | Colon/comma-separated host-path prefixes that host-bind mounts are allowed to source from (default-deny otherwise). |
| `CORNUS_ALLOW_PRIVILEGED` | — | deny | Allows privileged workloads on the kubernetes backend. |
| `CORNUS_EGRESS_POLICY` | — | — | Server-side policy governing which egress gateway routes are permitted. |
| `CORNUS_EGRESS_GATEWAY` | — | off | Marks this server as an egress gateway terminus. |
| `CORNUS_CREDENTIALS_URL` | — | — | Advertised to a workload as the endpoint its generic credential delivery fetches from (injected env var). |
| `CORNUS_CARETAKER_CONFIG` | — | — | JSON caretaker role config passed to a caretaker sidecar/companion. |
| `CORNUS_AGENT_IMAGE` | — | — | Cornus-embedding image used for a mount/egress/deploy caretaker sidecar or companion — the kubernetes pod sidecar, the `dockerhost`/`containerd`/`bare` egress companion, and (with `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`/`CORNUS_BARE_REMOTE`) the always-on remote companion (mounts, port-forward/tunnel rerouting, exec agent-forwarding). |
| `CORNUS_AGENT_DIR` | — | — | Directory for client-agent artifacts (client-side). |
| `CORNUS_DOCKER_REMOTE` | — | off | Opts the `dockerhost` backend into an always-on per-instance remote-companion sidecar, sharing each instance's network namespace, whether or not the deploy uses `--mount` — for a Docker daemon that is not co-located with this server (e.g. `DOCKER_HOST=tcp://...`). It realizes client-local mounts via the companion (a Docker volume with `rshared`/`rslave` propagation) instead of the default single-host kernel-9p fast path, and reroutes `cornus port-forward`/`cornus tunnel` and `cornus exec --forward-agent` through the companion instead of the server dialing the instance directly. Needs `CORNUS_AGENT_IMAGE` and `CORNUS_ADVERTISE_URL`. See [deploy backends](/reference/deploy-backends). |

### Containerd backend

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_CONTAINERD_ADDRESS` | — | `/run/containerd/containerd.sock` | Containerd socket (the standard `CONTAINERD_ADDRESS` is honored as a fallback). |
| `CORNUS_CONTAINERD_NAMESPACE` | — | `cornus` | Containerd namespace for workloads. |
| `CORNUS_CONTAINERD_SNAPSHOTTER` | — | `overlayfs` | Rootfs snapshotter (set `native` on overlay-backed hosts). |
| `CORNUS_CONTAINERD_INSECURE_REGISTRIES` | — | `localhost` only | Comma-separated `host[:port]` treated as plain-HTTP for image pulls. |
| `CORNUS_CONTAINERD_LOG_MAX_BYTES` | — | 16 MiB | Log rotation size (one old generation kept). |
| `CORNUS_CNI_BIN_DIR` | — | `/opt/cni/bin` (also `CNI_PATH`) | Directory the CNI plugins are discovered in. |
| `CORNUS_CNI_SUBNET_BASE` | — | `10.4` | Base for the `/24` carved per compose network. |
| `CORNUS_CONTAINERD_REMOTE` | — | off | Opts the `containerd` backend into the same always-on per-instance remote-companion sidecar as `CORNUS_DOCKER_REMOTE`, joining each instance's pinned network namespace, whether or not the deploy uses `--mount` (a companion container/task performs the kernel 9P mount, relayed via a shared host directory with `rshared`/`rslave` OCI mount options, and the companion also reroutes `cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent`). Does **not** make containerd itself remote-reachable (its client dialer is unix-socket-only) — only changes how mounts/port-forward/exec-agent-forwarding are realized on an otherwise still-co-located daemon. Needs `CORNUS_AGENT_IMAGE` and `CORNUS_ADVERTISE_URL`. See [deploy backends](/reference/deploy-backends). |
| `CORNUS_DOCKER_SOCK` | — | `/var/run/docker.sock` | Docker socket for the `dockerhost` backend (also the client `cornus daemon docker` listen socket). |

### Bare backend

The daemonless backend (`CORNUS_DEPLOY_BACKEND=bare`). Shares the `CORNUS_CNI_*` knobs above with `containerd`; needs no daemon socket.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_BARE_RUNTIME` | — | `runc` | OCI runtime binary driven directly (`runc`, `crun`, `youki`, or `runsc` for gVisor — any runc-CLI-compatible binary); validated at startup. |
| `CORNUS_BARE_STATS_SOURCE` | — | auto (by runtime name) | Where `Stats` reads metrics: `runtime` (`runc events --stats`) or `cgroup` (host cgroup files). Defaults by runtime basename — `runsc`/`gvisor` are sandboxed, so they use `runtime`; `runc`/`crun`/`youki` use `cgroup`. Set it to override an oddly named install. |
| `CORNUS_BARE_SNAPSHOTTER` | — | overlay (native fallback) | Rootfs snapshotter; set `native` on overlay-backed / docker-in-docker hosts where overlay-on-overlay is rejected. |
| `CORNUS_BARE_INSECURE_REGISTRIES` | — | `localhost` only | Comma-separated `host[:port]` treated as plain-HTTP for image pulls. |
| `CORNUS_BARE_SYSTEMD_CGROUP` | — | off (cgroupfs) | Switches the runtime to the systemd cgroup driver (otherwise cgroupfs, which runc manages directly on v1 and v2). |
| `CORNUS_BARE_DNS` | — | on | In-process resolver on the netns gateway answering guest container DNS; set a false value to disable and fall back to hosts-file resolution only. |
| `CORNUS_BARE_SHIM` | — | off | Opts into the detached per-container supervision shim (cornus's conmon analogue), which survives a cornus restart; off keeps the default in-process supervisor. |
| `CORNUS_BARE_REMOTE` | — | off | Opts the `bare` backend into the always-on per-instance remote-companion sidecar (as `CORNUS_CONTAINERD_REMOTE`): the companion performs client-local mounts and reroutes `cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent`. Needs `CORNUS_AGENT_IMAGE` and `CORNUS_ADVERTISE_URL`. |

### Kubernetes backend

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_K8S_NAMESPACE` | — | in-cluster / current | Namespace the kubernetes backend deploys into. |
| `CORNUS_K8S_NET_DRIVER` | — | `services` | Default network driver for user networks (`services`, `bridge`, `ipvlan`, `macvlan`, `cilium`). |
| `CORNUS_K8S_NET_STRICT` | — | `false` | Fail (rather than degrade) when the requested network fabric cannot be realised. |
| `CORNUS_K8S_POLICY_CNI` | — | `false` | Enables NetworkPolicy-based isolation on a policy-capable CNI. |
| `CORNUS_K8S_IMAGE_PULL_POLICY` | — | backend default | Overrides the pod `imagePullPolicy`. |
| `CORNUS_K8S_SIDECAR_IMAGE` | — | the cornus image | Image used for the caretaker sidecar. |
| `CORNUS_KNATIVE_STRICT` | — | `false` | Fail a Knative-enabled deployment when the cluster does not serve `serving.knative.dev/v1`, instead of running it as a normal Deployment with a warning. |

### Ingress defaults

Server-side fallbacks for workloads that opt into [ingress](/topics/ingress) (kubernetes backend). Also settable as Helm `ingress.*` values.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | — | — | Base wildcard domain for auto-deriving `<name>.<domain>` hosts. Empty means a workload must set its own host or domain. |
| `CORNUS_INGRESS_CLASS` | — | cluster default | Default `IngressClassName` for created Ingresses. |
| `CORNUS_INGRESS_TLS_ISSUER` | — | — | Default cert-manager cluster-issuer for TLS-enabled ingresses. |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | — | `false` | When true (and a domain is set), reject a workload whose resolved host falls outside the domain. |

## Tunnels

See [Public tunnels](/topics/tunnels).

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_TUNNEL_BACKEND` | — | `ngrok` | Public-URL tunnel backend: `ngrok` (default), `ssh` (SSH reverse-tunneling), `cloudflare` (Cloudflare Tunnel), or `tailscale` (Tailscale Funnel). |
| `CORNUS_TUNNEL_AUTHTOKEN` | — | — | Server-side default credential for the selected tunnel backend, used when a client omits one. The same variable name also populates the client's `cornus tunnel --authtoken` flag when set in *its* environment instead — same name, two different processes, same kind of value. |
| `CORNUS_TUNNEL_CLOUDFLARED_BIN` | — | `cloudflared` on PATH | Path to the `cloudflared` binary. |
| `CORNUS_TUNNEL_TAILSCALE_BIN` | — | `tailscale` on PATH | Path to the `tailscale` binary. |
| `CORNUS_TUNNEL_SSH_ADDR` | — | — | SSH tunnel server address. |
| `CORNUS_TUNNEL_SSH_USER` | — | — | SSH tunnel user. |
| `CORNUS_TUNNEL_SSH_BIND` | — | — | Remote bind address for the SSH reverse tunnel. |
| `CORNUS_TUNNEL_SSH_URL_TEMPLATE` | — | — | Template for the public URL derived from an SSH tunnel. |
| `CORNUS_TUNNEL_SSH_URL_FROM_SESSION` | — | off | Derive the public URL from the SSH session output. |
| `CORNUS_TUNNEL_SSH_HOSTKEY` | — | — | Expected SSH host key. |
| `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` | — | — | Path to a `known_hosts` file for SSH host verification. |
| `CORNUS_TUNNEL_SSH_INSECURE` | — | off | Skip SSH host-key verification (testing only). |

## Hub (workload-to-workload overlay)

See [the workload hub](/topics/hub).

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_HUB_STORE` | — | in-memory | Hub catalog store; `kube` uses a Kubernetes-backed store. |
| `CORNUS_HUB_REDIS` | — | — | Redis URL for a distributed hub store (enables cross-replica catalog). |
| `CORNUS_HUB_FORWARD_URL` | — | — | URL a replica forwards hub relay traffic to. |
| `CORNUS_HUB_FORWARD_CA` | — | — | PEM CA bundle verifying the hub forward endpoint. |
| `CORNUS_HUB_POLICY` | — | — | Policy governing which identities may reach which hub services. |
| `CORNUS_HUB_REGISTER_POLICY` | — | — | Policy governing which identities may register (export) hub services. |

## Observability

See the [architecture overview](/architecture/) for the observability model.

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_OTEL` | `--otel` | off | Enable OpenTelemetry (traces/metrics/logs) via the standard `OTEL_*` env. Also enabled implicitly when any `OTEL_*` exporter/endpoint env var is set. |
| `CORNUS_METRICS_PROMETHEUS` | — | off | Expose a Prometheus metrics endpoint (only effective when OpenTelemetry is enabled). |

The same `CORNUS_OTEL` / `OTEL_*` gate also enables tracing in the **client CLI**:
set it in the environment where you run `cornus` and each invocation emits a root
span that propagates a W3C `traceparent` to the server (and onward to the
caretaker), so a `cornus deploy` / `cornus build` / `cornus compose up` shows up
as one end-to-end trace rather than an isolated server span.

## Client-side variables (for reference)

These are read by the CLI, not the server, but appear in the same `CORNUS_*` namespace. See [Connection config](/reference/connection-config) and [Remote workflows](/topics/remote-workflows).

| Variable | Default | Meaning |
| --- | --- | --- |
| `CORNUS_SERVER` / `CORNUS_HOST` | selected profile, then `http://localhost:5000` | Remote cornus server URL for client commands. |
| `CORNUS_TOKEN` | — | Bearer token for client requests (overrides a profile `token`). |
| `CORNUS_CONFIG` | platform config path | Path to the client [connection config](/reference/connection-config) file. |
| `CORNUS_CONTEXT` | config `current-context` | Connection profile to use. |
| `CORNUS_OUTPUT` | `auto` | Output rendering mode (`auto`, `plain`, `fancy`, `json`). See [output modes](/guides/output-modes). |
| `CORNUS_CONDUIT` | profile / `port-forward` | Session conduit mode (`port-forward` or `socks5`). |
| `CORNUS_VIA_SERVER` | profile / direct | Route workload streaming through the server proxy. |
| `CORNUS_BUILDER` | — | Remote build endpoint for delegated builds. |
| `CORNUS_REGISTRY` | server-advertised host | Registry host for tags without a registry part (remote builds). |
