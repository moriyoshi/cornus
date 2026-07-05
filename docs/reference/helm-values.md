# Helm chart values

The Cornus Helm chart (`deploy/helm/cornus`, also published as an OCI artifact)
deploys the server in-cluster as a `StatefulSet` + PVC + `Service` + RBAC, preset to
the [`kubernetes`](/reference/deploy-backends) deploy backend. This page documents
every value in `values.yaml`; see [Installation](/introduction/installation)
for the install walkthrough.

Chart version `0.3.0`, app version `0.1.0`.

## Installing

```sh
# From the OCI registry (recommended):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# From a checked-out chart, overriding values:
helm install cornus deploy/helm/cornus \
  --set storage='s3://my-bucket?region=us-east-1' \
  --set tls.enabled=true
```

Override values with `--set key=value` or a `-f my-values.yaml` file.

## Image

| Value | Default | Description |
| --- | --- | --- |
| `image.repository` | `ghcr.io/moriyoshi/cornus` | Server image. Override for a locally built or mirrored image. |
| `image.tag` | `""` | Image tag. Empty defaults to the chart `appVersion`. |
| `image.pullPolicy` | `IfNotPresent` | Standard Kubernetes image pull policy. |

## Server

| Value | Default | Description |
| --- | --- | --- |
| `addr` | `":5000"` | Listen address inside the container. |
| `replicas` | `1` | Server replica count. `1` keeps single-replica behavior; `> 1` switches on multi-replica mode (see [Multi-replica mode](#multi-replica-mode) for its requirements). |
| `deployBackend` | `kubernetes` | Backend for workloads created through `/.cornus/v1/deploy` (sets `CORNUS_DEPLOY_BACKEND`). The chart runs in-cluster and deploys into its own namespace. Set `dockerhost` only if the pod has a Docker socket mounted. |
| `resources` | `{}` | Pod resource requests/limits, rendered verbatim. |

## Storage and garbage collection

| Value | Default | Description |
| --- | --- | --- |
| `storage` | `""` | Registry persistence backend (`CORNUS_STORAGE`): a path, `file://`, `mem://`, or `s3://bucket?region=...`. Empty keeps the CAS on the data-dir PVC. Must be an `s3://` URL when `replicas > 1`. See [storage backends](/reference/storage-backends). |
| `persistence.size` | `20Gi` | Size of the per-replica data-dir PVC. |
| `persistence.storageClassName` | unset | PVC storage class (commented out by default; cluster default is used). |
| `gc.interval` | `""` | `CORNUS_GC_INTERVAL`: a Go duration (e.g. `24h`). When set, each replica runs the storage mark-and-sweep GC on this period. Empty means GC only runs on demand via `POST /.cornus/v1/gc`. |
| `gc.lease` | `""` | `CORNUS_GC_LEASE`: opt-in cross-replica GC coordination (requires `gc.interval`). `kube` elects a single sweeper per tick via a `coordination.k8s.io` Lease; `kube:<name>` or `kube:<namespace>/<name>` override the Lease identity. Makes `gc.interval` safe with `replicas > 1`. |

## Service and registry exposure

`registry.exposure` selects how workload images are exposed for cluster nodes to pull — the server advertises a node-reachable registry host via `GET /.cornus/v1/info`, and the chart wires the matching topology.

| Value | Default | Description |
| --- | --- | --- |
| `service.type` | `""` | Service type. Empty derives it from `registry.exposure` (`NodePort` for `nodePort`, else `ClusterIP`). Set only to override. |
| `service.port` | `5000` | Service port. |
| `registry.exposure` | `nodePort` | Topology the server advertises: `nodePort`, `clusterIP`, `hostPort`, `hostNetwork`, or `ingress` (see table below). |
| `registry.nodePort` | `30500` | Fixed NodePort for `nodePort` exposure (lets each node's containerd registry config be pre-provisioned). Empty lets Kubernetes allocate one. |
| `registry.hostPort` | `5000` | Node port the registry binds for `hostPort` exposure. |
| `registry.advertiseHost` | `""` | Overrides the registry host baked into deploy pull refs (`CORNUS_ADVERTISE_REGISTRY`). Required for `clusterIP` / `hostPort` / `hostNetwork` / `ingress`. Prefix with `https://` for a TLS registry. |
| `registry.nodeCIDR` | `""` | For `nodePort` / `clusterIP`, emits a NetworkPolicy allowing nodes in this CIDR to reach the registry port — required under a default-deny posture. |

### `registry.exposure` values

| Value | How nodes pull | Requires |
| --- | --- | --- |
| `nodePort` (default) | `localhost:<nodePort>` on each node | Nothing extra; auto-advertised from the Service. |
| `clusterIP` | The Service ClusterIP | `advertiseHost`; `nodeCIDR` allow under default-deny; node trust for the ClusterIP. |
| `hostPort` | `<nodeIP>:<port>` (CNI portmap) | `advertiseHost`; pin the pod with `nodeSelector`. NetworkPolicy-immune. |
| `hostNetwork` | Host netns listener | A privileged PodSecurity namespace; `advertiseHost`; pin the pod. NetworkPolicy-immune. |
| `ingress` | An ingress host/VIP | `advertiseHost`; nodes resolve the host and trust its cert (real DNS + TLS). |

## Ingress defaults

Server-side fallbacks for workloads that opt into [ingress](/guides/ingress) (deploy spec `ingress:` / Compose `x-cornus-ingress:`). Leave every field empty (the default) to require each workload to specify its own host, so nothing is auto-exposed.

| Value | Default | Description |
| --- | --- | --- |
| `ingress.domain` | `""` | `CORNUS_INGRESS_DOMAIN`: base wildcard domain for host auto-derivation (e.g. `preview.example.com`). Empty means a workload must set its own host or domain. |
| `ingress.className` | `""` | `CORNUS_INGRESS_CLASS`: default `IngressClassName`. Empty uses the cluster default. |
| `ingress.tlsIssuer` | `""` | `CORNUS_INGRESS_TLS_ISSUER`: default cert-manager cluster-issuer for TLS-enabled ingresses. Empty means a TLS-requesting workload must supply its own secret/issuer. |
| `ingress.enforceDomain` | `false` | `CORNUS_INGRESS_ENFORCE_DOMAIN`: when true (and `domain` is set), reject a workload whose resolved host falls outside `domain`, so a shared controller cannot be made to serve an arbitrary hostname. |

## Privilege

| Value | Default | Description |
| --- | --- | --- |
| `privileged` | `true` | The in-process build engine needs runc + overlayfs; `privileged` is the simplest posture. Set `false` and provide the rootless prerequisites for hardened clusters. See [Privilege posture](/reference/deploy-backends). |

## TLS

Opt-in HTTPS. When enabled, the server serves from a mounted Secret (`tls.crt` / `tls.key`, plus `ca.crt` for mTLS) and hot-reloads the cert on file change.

| Value | Default | Description |
| --- | --- | --- |
| `tls.enabled` | `false` | Serve HTTPS. |
| `tls.secretName` | `cornus-tls` | Secret mounted at `/etc/cornus/tls`. Produced by cert-manager when `tls.certManager.enabled`, else provide an existing one with the same keys. |
| `tls.clientCA` | `false` | Verify client certs (mTLS) using `ca.crt` from the Secret. A verified cert's CommonName becomes the caller identity (see `CORNUS_API_POLICY` in [Security and authentication](/guides/security)). |
| `tls.certManager.enabled` | `false` | Render a cert-manager `Certificate` that writes `secretName` and is auto-rotated. Requires cert-manager and an Issuer/ClusterIssuer. |
| `tls.certManager.issuerRef.name` | `""` | Issuer/ClusterIssuer name. |
| `tls.certManager.issuerRef.kind` | `ClusterIssuer` | `ClusterIssuer` or `Issuer`. |
| `tls.certManager.dnsNames` | `[]` | DNS names for the cert; defaults to the in-cluster Service name when empty. |
| `tls.certManager.duration` | `2160h` | Certificate lifetime (90d). |
| `tls.certManager.renewBefore` | `720h` | Renew-before window (30d); cornus hot-reloads the new cert. |

## Auth (JWT)

Opt-in JWT verification for the server API (the kube-auth turnkey path). Each set value renders the matching `CORNUS_JWT_*` env; leaving all empty renders nothing (auth stays off unless configured elsewhere). See [Security and authentication](/guides/security).

| Value | Default | Description |
| --- | --- | --- |
| `auth.jwt.jwksURL` | `""` | HTTPS URL of a JWKS document (`CORNUS_JWT_JWKS_URL`), e.g. the cluster's ServiceAccount OIDC JWKS. Mutually exclusive with `jwksConfigMap` / `jwksSecret`. |
| `auth.jwt.jwksConfigMap` | `""` | Name of an existing ConfigMap holding a JWKS document to mount (`CORNUS_JWT_JWKS_FILE`). Set exactly one of `jwksConfigMap` / `jwksSecret`. |
| `auth.jwt.jwksSecret` | `""` | Name of an existing Secret holding a JWKS document to mount. |
| `auth.jwt.jwksKey` | `jwks.json` | Key inside the ConfigMap/Secret holding the JWKS JSON. Mounted read-only at `/etc/cornus/jwks`. |
| `auth.jwt.audience` | `""` | Required `aud` claim (`CORNUS_JWT_AUDIENCE`). Tokens minted by `cornus kube-auth` must use the same audience. |
| `auth.jwt.issuer` | `""` | Optional expected `iss` claim (`CORNUS_JWT_ISSUER`). Unset skips the issuer check. |

## Caretaker TLS

| Value | Default | Description |
| --- | --- | --- |
| `caretakerTlsSecret` | `""` | Name of an existing Secret (`CORNUS_CARETAKER_TLS_SECRET`) whose material server-bound caretaker sidecars present when dialing the server. Keys follow the `kubernetes.io/tls` convention: `ca.crt` (added to system roots — use with a private-CA `tls.enabled` cert) and, optionally, `tls.crt` / `tls.key` (an mTLS client pair for `tls.clientCA`). Empty renders nothing. |

## Tailscale Funnel sidecar

Opt-in sidecar for the `tailscale` [tunnel backend](/guides/tunnels#backends): a `tailscaled` container that joins the tailnet unattended via an authkey Secret, plus an initContainer that copies the `tailscale` CLI onto a volume shared with the cornus container, so no custom cornus image is needed. Runs in userspace networking mode (no `NET_ADMIN`, no TUN device). See the [Tunnels guide](/guides/tunnels) for the full walkthrough.

| Value | Default | Description |
| --- | --- | --- |
| `tailscale.enabled` | `false` | Enable the sidecar. Sets `CORNUS_TUNNEL_BACKEND=tailscale`, `CORNUS_TUNNEL_TAILSCALE_BIN`, and `TS_SOCKET` on the cornus container. |
| `tailscale.image.repository` / `tag` / `pullPolicy` | `ghcr.io/tailscale/tailscale` / `stable` / `IfNotPresent` | Sidecar and initContainer image. |
| `tailscale.authKeySecret` | `""` | **Required when enabled.** Name of an existing Secret holding a tailnet auth key. Use a reusable, ideally ephemeral-tagged key — the sidecar's state directory is an `emptyDir`, not persisted across pod restarts. |
| `tailscale.authKeySecretKey` | `authkey` | Key inside the Secret holding the auth key. |
| `tailscale.hostname` | `""` | `TS_HOSTNAME`: the tailnet device name, so the Funnel URL is stable across restarts. Empty derives it from the release fullname. |
| `tailscale.extraArgs` | `""` | Extra flags appended to the sidecar's unattended `tailscale up` (`TS_EXTRA_ARGS`), e.g. `--accept-dns=false`. The chart already supplies `--authkey` and `--hostname`. |
| `tailscale.resources` | `{}` | Sidecar container resources. |

## RBAC and scheduling

| Value | Default | Description |
| --- | --- | --- |
| `rbac.create` | `true` | Grant RBAC for the in-cluster kubernetes deploy backend and, when `replicas > 1`, the kube-native hub store (HubEndpoint CRs, Leases, CRD self-install). The Lease verbs also cover `gc.lease`. |
| `nodeSelector` | `{}` | Standard pod `nodeSelector`. |
| `tolerations` | `[]` | Standard pod tolerations. |
| `affinity` | `{}` | Pod affinity, rendered verbatim when set. Empty with `replicas > 1` renders a default soft pod anti-affinity that spreads replicas across nodes; setting this replaces that default. |

## Multi-replica mode

Setting `replicas > 1` switches the workload-to-workload [hub](/guides/hub)
to its multi-replica mode: the chart sets `CORNUS_HUB_STORE=kube`, adds a headless
Service for stable per-pod DNS, and points `CORNUS_HUB_FORWARD_URL` at it for
cross-replica delivery. Requirements and caveats:

- **`storage` MUST be an `s3://` URL** (enforced at render time) — each replica gets
  its own PVC, so a PVC-backed CAS would be inconsistent across replicas behind one
  Service. The PVC then holds only the per-replica build cache.
- The `StatefulSet` `serviceName` switches to the headless Service, and that field is
  immutable: moving an existing release between `1` and `> 1` replicas requires
  deleting the `StatefulSet` first (PVCs are retained).
- With `tls.enabled`, inter-replica forward dials use `wss://` and verify the serving
  cert against the container trust store, so the cert must cover the per-pod names
  (`*.<fullname>-hub.<namespace>.svc`) and chain to a trusted root.
- **Garbage collection:** `gc.interval` alone runs an uncoordinated sweep on every
  replica over the shared S3 CAS. Set `gc.lease: kube` together with `gc.interval` so
  the replicas elect a single sweeper per tick through a Lease.

## See also

- [Installation](/introduction/installation) — install walkthrough and running the server.
- [Deploy backends](/reference/deploy-backends) — the `kubernetes` backend this chart presets.
- [Storage backends](/reference/storage-backends) — the `storage` value and object-store CAS.
- [Server environment variables](/reference/server-env-vars) — the `CORNUS_*` env the chart renders.
- [Tunnels guide](/guides/tunnels) — step-by-step setup for every tunnel backend, including the Tailscale sidecar.
