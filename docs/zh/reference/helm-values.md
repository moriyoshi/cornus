# Helm chart 值

Cornus Helm chart（`deploy/helm/cornus`，也作为 OCI artifact 发布）将 server 以 `StatefulSet` + PVC + `Service` + RBAC 部署到 cluster 内，并预设为 [`kubernetes`](/zh/reference/deploy-backends) deploy backend。本页记录 `values.yaml` 中每个值；安装演练见[安装](/zh/introduction/installation)。

Chart version `0.3.0`，app version `0.1.0`。

## 安装

```sh
# From the OCI registry (recommended):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# From a checked-out chart, overriding values:
helm install cornus deploy/helm/cornus \
  --set storage='s3://my-bucket?region=us-east-1' \
  --set tls.enabled=true
```

使用 `--set key=value` 或 `-f my-values.yaml` file 覆盖值。

## Image

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `image.repository` | `ghcr.io/moriyoshi/cornus` | Server image。可为本地构建或镜像镜像 override。 |
| `image.tag` | `""` | Image tag。空时默认 chart `appVersion`。 |
| `image.pullPolicy` | `IfNotPresent` | 标准 Kubernetes image pull policy。 |

## Server

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `addr` | `":5000"` | Container 内 listen address。 |
| `replicas` | `1` | Server replica count。`1` 保持 single-replica behavior；`> 1` 开启 multi-replica mode（要求见[Multi-replica mode](#multi-replica-mode)）。 |
| `deployBackend` | `kubernetes` | 通过 `/.cornus/v1/deploy` 创建 workload 的 backend（设置 `CORNUS_DEPLOY_BACKEND`）。Chart 在 cluster 内运行，并部署到自身 namespace。仅当 pod mount Docker socket 时设置 `dockerhost`。 |
| `resources` | `{}` | Pod resource request/limit，原样 render。 |

## Storage 和垃圾回收

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `storage` | `""` | Registry persistence backend（`CORNUS_STORAGE`）：路径、`file://`、`mem://` 或 `s3://bucket?region=...`。空值将 CAS 保留在 data-dir PVC。`replicas > 1` 时必须为 `s3://` URL。见[存储后端](/zh/reference/storage-backends)。 |
| `persistence.size` | `20Gi` | 每 replica data-dir PVC 的大小。 |
| `persistence.storageClassName` | unset | PVC storage class（默认注释，使用 cluster default）。 |
| `gc.interval` | `""` | `CORNUS_GC_INTERVAL`：Go duration（例如 `24h`）。设置后每个 replica 都按此周期运行 storage mark-and-sweep GC。空表示仅通过 `POST /.cornus/v1/gc` 按需运行。 |
| `gc.lease` | `""` | `CORNUS_GC_LEASE`：opt-in cross-replica GC coordination（需要 `gc.interval`）。`kube` 通过 `coordination.k8s.io` Lease 在每个 tick 选出一个 sweeper；`kube:<name>` 或 `kube:<namespace>/<name>` 覆盖 Lease identity。使 `gc.interval` 在 `replicas > 1` 时安全。 |

## Service 和 registry 暴露

`registry.exposure` 选择将 workload image 暴露给 cluster node pull 的方式——server 经 `GET /.cornus/v1/info` 声明 node-reachable registry host，chart 配置相应 topology。

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `service.type` | `""` | Service type。空时从 `registry.exposure` 派生（`nodePort` 为 `NodePort`，否则为 `ClusterIP`）。只在 override 时设置。 |
| `service.port` | `5000` | Service port。 |
| `registry.exposure` | `nodePort` | Server 声明的 topology：`nodePort`、`clusterIP`、`hostPort`、`hostNetwork` 或 `ingress`（见下表）。 |
| `registry.nodePort` | `30500` | `nodePort` exposure 的固定 NodePort（使每 node 的 containerd registry config 可预配）。空时让 Kubernetes 分配。 |
| `registry.hostPort` | `5000` | `hostPort` exposure 中 registry bind 的 node port。 |
| `registry.advertiseHost` | `""` | 覆盖写入 deploy pull ref 的 registry host（`CORNUS_ADVERTISE_REGISTRY`）。`clusterIP` / `hostPort` / `hostNetwork` / `ingress` 必需。TLS registry 以 `https://` 前缀。 |
| `registry.nodeCIDR` | `""` | 对 `nodePort` / `clusterIP`，生成允许此 CIDR node 访问 registry port 的 NetworkPolicy——default-deny posture 下必需。 |

### `registry.exposure` 值

| Value | Node 如何 pull | 需要 |
| --- | --- | --- |
| `nodePort`（默认） | 每 node 上的 `localhost:<nodePort>` | 无额外要求；从 Service 自动声明。 |
| `clusterIP` | Service ClusterIP | `advertiseHost`；default-deny 下 `nodeCIDR` allow；node trust ClusterIP。 |
| `hostPort` | `<nodeIP>:<port>`（CNI portmap） | `advertiseHost`；使用 `nodeSelector` 固定 pod。NetworkPolicy-immune。 |
| `hostNetwork` | Host netns listener | Privileged PodSecurity namespace；`advertiseHost`；固定 pod。NetworkPolicy-immune。 |
| `ingress` | Ingress host/VIP | `advertiseHost`；node 可解析 host 且信任其 cert（真实 DNS + TLS）。 |

## Ingress 默认值

为 opt in [ingress](/zh/guides/ingress)（deploy spec `ingress:` / Compose `x-cornus-ingress:`）的 workload 提供 server-side fallback。将所有 field 保持空（默认）会要求每个 workload 指定自己的 host，因此不会自动暴露任何内容。

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `ingress.domain` | `""` | `CORNUS_INGRESS_DOMAIN`：用于 host auto-derivation 的 base wildcard domain（例如 `preview.example.com`）。空表示 workload 必须设置自己的 host 或 domain。 |
| `ingress.className` | `""` | `CORNUS_INGRESS_CLASS`：默认 `IngressClassName`。空时使用 cluster default。 |
| `ingress.tlsIssuer` | `""` | `CORNUS_INGRESS_TLS_ISSUER`：TLS-enabled ingress 的默认 cert-manager cluster-issuer。空表示请求 TLS 的 workload 必须提供自己的 secret/issuer。 |
| `ingress.enforceDomain` | `false` | `CORNUS_INGRESS_ENFORCE_DOMAIN`：为 true（且设置 `domain`）时，拒绝 resolved host 在 `domain` 外的 workload，防止 shared controller 按 client 要求提供任意 hostname。 |

## Privilege

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `privileged` | `true` | In-process build engine 需要 runc + overlayfs；`privileged` 是最简单 posture。对 hardened cluster，设为 `false` 并提供 rootless prerequisite。见[权限要求](/zh/reference/deploy-backends)。 |

## TLS

Opt-in HTTPS。启用后，server 从 mounted Secret（`tls.crt` / `tls.key`，mTLS 还包括 `ca.crt`）提供服务，并在 file change 时 hot-reload cert。

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `tls.enabled` | `false` | 提供 HTTPS。 |
| `tls.secretName` | `cornus-tls` | Mount 到 `/etc/cornus/tls` 的 Secret。当 `tls.certManager.enabled` 时由 cert-manager 生成，否则请提供含相同 key 的 existing Secret。 |
| `tls.clientCA` | `false` | 使用 Secret 中 `ca.crt` 验证 client cert（mTLS）。已验证 cert 的 CommonName 成为 caller identity（见[安全与认证](/zh/guides/security)中的 `CORNUS_API_POLICY`）。 |
| `tls.certManager.enabled` | `false` | Render 一个写入 `secretName` 且自动 rotate 的 cert-manager `Certificate`。需要 cert-manager 与 Issuer/ClusterIssuer。 |
| `tls.certManager.issuerRef.name` | `""` | Issuer/ClusterIssuer name。 |
| `tls.certManager.issuerRef.kind` | `ClusterIssuer` | `ClusterIssuer` 或 `Issuer`。 |
| `tls.certManager.dnsNames` | `[]` | Certificate 的 DNS name；空时默认 in-cluster Service name。 |
| `tls.certManager.duration` | `2160h` | Certificate lifetime（90d）。 |
| `tls.certManager.renewBefore` | `720h` | Renew-before window（30d）；cornus hot-reload 新 certificate。 |

## Auth（JWT）

Server API 的 opt-in JWT verification（kube-auth turnkey path）。每个设置的 value 都 render 对应 `CORNUS_JWT_*` env；全部留空则不 render 内容（除非另有配置，否则 auth 保持关闭）。见[安全与认证](/zh/guides/security)。

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `auth.jwt.jwksURL` | `""` | JWKS document 的 HTTPS URL（`CORNUS_JWT_JWKS_URL`），例如 cluster ServiceAccount OIDC JWKS。与 `jwksConfigMap` / `jwksSecret` 互斥。 |
| `auth.jwt.jwksConfigMap` | `""` | 包含 JWKS document、要 mount 的 existing ConfigMap 名称（`CORNUS_JWT_JWKS_FILE`）。恰好设置 `jwksConfigMap` / `jwksSecret` 之一。 |
| `auth.jwt.jwksSecret` | `""` | 包含 JWKS document、要 mount 的 existing Secret 名称。 |
| `auth.jwt.jwksKey` | `jwks.json` | ConfigMap/Secret 内保存 JWKS JSON 的 key。只读 mount 至 `/etc/cornus/jwks`。 |
| `auth.jwt.audience` | `""` | 所需 `aud` claim（`CORNUS_JWT_AUDIENCE`）。`cornus kube-auth` 签发的 token 必须使用相同 audience。 |
| `auth.jwt.issuer` | `""` | 可选 expected `iss` claim（`CORNUS_JWT_ISSUER`）。未设置时跳过 issuer check。 |

## Caretaker TLS

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `caretakerTlsSecret` | `""` | Existing Secret（`CORNUS_CARETAKER_TLS_SECRET`）名称；其 material 由 server-bound caretaker sidecar 在 dial server 时提交。Key 遵循 `kubernetes.io/tls` convention：`ca.crt`（加到 system root——用于 private-CA `tls.enabled` cert），以及可选 `tls.crt` / `tls.key`（用于 `tls.clientCA` 的 mTLS client pair）。空时不 render 内容。 |

## Tailscale Funnel sidecar

面向 `tailscale` [tunnel backend](/zh/guides/tunnels#后端) 的 opt-in sidecar：一个通过 authkey Secret 以非交互方式加入 tailnet 的 `tailscaled` 容器，加上一个 initContainer，将 `tailscale` CLI 复制到与 cornus 容器共享的 volume 上，因此无需自定义 cornus image。以 userspace networking mode 运行（无需 `NET_ADMIN`，无需 TUN device）。完整演练见[隧道指南](/zh/guides/tunnels)。

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `tailscale.enabled` | `false` | 启用该 sidecar。会为 cornus 容器设置 `CORNUS_TUNNEL_BACKEND=tailscale`、`CORNUS_TUNNEL_TAILSCALE_BIN` 和 `TS_SOCKET`。 |
| `tailscale.image.repository` / `tag` / `pullPolicy` | `ghcr.io/tailscale/tailscale` / `stable` / `IfNotPresent` | Sidecar 和 initContainer 使用的 image。 |
| `tailscale.authKeySecret` | `""` | **启用时必填。** 一个 existing Secret 的名称，其中保存了 tailnet auth key。请使用可复用、最好打上 ephemeral 标签的 key —— sidecar 的状态目录是 `emptyDir`，不会在 pod 重启后持久化。 |
| `tailscale.authKeySecretKey` | `authkey` | Secret 中保存该 auth key 的 key 名。 |
| `tailscale.hostname` | `""` | `TS_HOSTNAME`：tailnet 设备名，使 Funnel URL 在重启后保持稳定。空时从 release fullname 派生。 |
| `tailscale.extraArgs` | `""` | 追加到 sidecar 非交互式 `tailscale up`（`TS_EXTRA_ARGS`）的额外 flag，例如 `--accept-dns=false`。Chart 已自动提供 `--authkey` 和 `--hostname`。 |
| `tailscale.resources` | `{}` | Sidecar 容器的 resources。 |

## RBAC 和调度

| Value | 默认值 | 说明 |
| --- | --- | --- |
| `rbac.create` | `true` | 为 in-cluster Kubernetes deploy backend 以及 `replicas > 1` 时 kube-native hub store 授予 RBAC（HubEndpoint CR、Lease、CRD self-install）。Lease verb 同时覆盖 `gc.lease`。 |
| `nodeSelector` | `{}` | 标准 pod `nodeSelector`。 |
| `tolerations` | `[]` | 标准 pod toleration。 |
| `affinity` | `{}` | Pod affinity，设置时原样 render。`replicas > 1` 且为空时，会 render 默认 soft pod anti-affinity，将 replica 分散到 node；设置此项会替换默认值。 |

## Multi-replica mode

设置 `replicas > 1` 会让 workload-to-workload [hub](/zh/guides/hub) 切换到 multi-replica mode：chart 设置 `CORNUS_HUB_STORE=kube`，添加用于稳定 per-pod DNS 的 headless Service，并将 `CORNUS_HUB_FORWARD_URL` 指向它以实现 cross-replica delivery。要求和注意事项：

- **`storage` 必须是 `s3://` URL**（render time 强制）——每 replica 有自己的 PVC，因此一个 Service 后的 PVC-backed CAS 会在 replica 间不一致。此时 PVC 只保存 per-replica build cache。
- StatefulSet `serviceName` 切换至 headless Service，且该 field immutable：将已有 release 在 `1` 与 `> 1` replica 间迁移，需要先删除 StatefulSet（PVC 被保留）。
- 使用 `tls.enabled` 时，inter-replica forward dial 使用 `wss://`，并通过 container trust store 验证 serving cert，因此 cert 必须覆盖 per-pod name（`*.<fullname>-hub.<namespace>.svc`）并 chain 到 trusted root。
- **Garbage collection：**单独的 `gc.interval` 会让每 replica 对 shared S3 CAS 运行不协调 sweep。请将 `gc.lease: kube` 与 `gc.interval` 一同设置，使 replica 每 tick 通过 Lease 选出一个 sweeper。

## 另请参阅

- [安装](/zh/introduction/installation)——安装演练和运行 server。
- [部署后端](/zh/reference/deploy-backends)——此 chart 预设的 `kubernetes` backend。
- [存储后端](/zh/reference/storage-backends)——`storage` value 与 object-store CAS。
- [服务器环境变量](/zh/reference/server-env-vars)——chart render 的 `CORNUS_*` env。
- [隧道指南](/zh/guides/tunnels)——每种 tunnel backend（包括 Tailscale sidecar）的分步设置说明。
