# 服务器环境变量

本页列出 [`cornus serve`](/zh/cli/serve) 和 server subsystem 读取的 `CORNUS_*` environment variable。部分对应 `cornus serve` flag（下表注明）；大部分是 server、deploy backend、build engine 和 tunnel 直接读取的 env-only knob。

::: info
此列表来自 source tree（`grep 'CORNUS_[A-Z0-9_]+' pkg cmd`），旨在作为实用参考。可能包括少数内部或演进中的 knob；权威行为始终在代码中。省略 test-only variable（`CORNUS_TEST_*`）。CLI（而非 server）消费的 client-side variable 在文末单独分组。
:::

## General / listener

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_ADDR` | `--addr` | `:5000` | `/v2/*` 和 `/.cornus/v1/*` 的 HTTP listen address。 |
| `CORNUS_DATA` | — | platform data dir | Server data directory（registry filesystem store、upload、backend state）。 |
| `CORNUS_ROOTLESS` | `--rootless` | off | 在 rootless mode（user namespace）运行 build engine。 |
| `CORNUS_LOG_LEVEL` | — | `info` | Log verbosity（`debug`、`info`、`warn`、`error`）。 |
| `CORNUS_ADVERTISE_URL` | — | — | Pod mount-agent / caretaker dial 回的 in-cluster cornus URL。Kubernetes backend 的 client-local mount 必需。 |
| `CORNUS_ADVERTISE_REGISTRY` | — | derived | 覆盖 server 向 client 声明的、deploy target 可 pull 的 registry `host[:port]`（和可选 scheme）（`GET /.cornus/v1/info`）。 |
| `CORNUS_REPLICA_ID` | — | — | 此 replica 的 stable identity；distributed hub store 和 GC leader gate 使用。 |

## Storage

完整 backend catalog 见[存储后端](/zh/reference/storage-backends)。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | data dir 下的 filesystem | Registry persistence backend：路径、`file://`、`mem://`、`s3://bucket?region=&endpoint=&path_style=`，或（`-tags cloudblob` 后）`gs://` / `azblob://`。 |

## 远程 9P 文件缓存和可写挂载

这些设置控制不变的客户端本地挂载所用缓存，以及可写 `,async` 挂载的可选一致性功能。文件缓存仅在 server 端使用。由于端点会协商共享功能集，必须在 server 环境和 deploy caller 环境中同时设置一致性 flag。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_FILE_CACHE` | `--file-cache` | off | 为不变的远程读取启用磁盘上的按文件缓存。 |
| `CORNUS_FILE_CACHE_DIR` | `--file-cache-dir` | — | 缓存文件的必需目录。请使用独立卷，不要与 server 数据目录共用。 |
| `CORNUS_FILE_CACHE_CHUNK_SIZE` | `--file-cache-chunk-size` | `1048576` | 缓存块大小 (bytes)。 |
| `CORNUS_FILE_CACHE_MAX_BYTES` | `--file-cache-max-bytes` | 无限制 | 由垃圾回收实施的缓存软大小上限。 |
| `CORNUS_BLOCK_COHERENCE` | — | classic | 以逗号或空格分隔的 `subhash`、`defer`、`subfill` 选项 (`subfill` 隐含 `subhash`)。空值保持 classic protocol。 |
| `CORNUS_BLOCK_READAHEAD` | — | off | `subfill` 下自适应投机 prefetch 的 bytes cap，例如 `64k`、`262144`。仅应用于 proxy 端。 |

## Authentication 和 API policy

认证模型见[认证与 TLS](/zh/topics/auth-and-tls)。未设置 auth env 时，server 无 credential 即可接受 request。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_AUTH_TOKEN` | — | — | 作为 credential 接受的 static bearer token。 |
| `CORNUS_TLS_CERT` | `--tls-cert` | — | PEM certificate file；与 `--tls-key` 一同设置时提供 HTTPS。 |
| `CORNUS_TLS_KEY` | `--tls-key` | — | PEM private-key file；与 `--tls-cert` 一同设置时提供 HTTPS。 |
| `CORNUS_TLS_CLIENT_CA` | `--tls-client-ca` | — | 验证 client certificate（mTLS）的 PEM CA bundle。已验证 cert CommonName 成为 caller identity；提交 cert 仍可选。 |
| `CORNUS_JWT_ISSUER` | — | — | 期望 JWT `iss` claim。 |
| `CORNUS_JWT_AUDIENCE` | — | — | 期望 JWT `aud` claim（必须匹配 client `kube-auth.audience`）。 |
| `CORNUS_JWT_HS256_SECRET` | — | — | 验证 HS256-signed JWT 的 shared secret。 |
| `CORNUS_JWT_PUBLIC_KEY` | — | — | 验证 asymmetric JWT 的 PEM public key path（RSA→RS256、ECDSA→ES256）。 |
| `CORNUS_JWT_JWKS_FILE` | — | — | JWT verification 用的 local JWKS document path。 |
| `CORNUS_JWT_JWKS_URL` | — | — | JWT verification 用的 remote JWKS endpoint URL。 |
| `CORNUS_API_POLICY` | — | — | `/.cornus/v1/*` surface 的 per-identity authorization policy。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | 即使 auth 已启用，也允许 unauthenticated registry pull。 |
| `CORNUS_CLIENT_TOKEN` | — | — | Caretaker Docker-API proxy 用于驱动 client deploy API 的 client-scoped token。 |
| `CORNUS_CLIENT_TOKEN_SECRET` | — | — | 保存 client-scoped token 的 Kubernetes Secret reference（`name/key`）；启用 workload `docker:` block 必需。 |
| `CORNUS_CARETAKER_TOKEN` | — | — | 认证 caretaker（sidecar）回调至 server 的 token。 |
| `CORNUS_CARETAKER_TOKEN_SECRET` | — | — | 保存 caretaker token 的 Kubernetes Secret reference。 |
| `CORNUS_CARETAKER_TLS_SECRET` | — | — | 保存 caretaker TLS material 的 Kubernetes Secret。 |

## Registry

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | filesystem | 见[Storage](#storage) / [存储后端](/zh/reference/storage-backends)。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | 允许 anonymous registry pull（见[认证](#authentication-和-api-policy)）。 |
| `CORNUS_REGISTRY_MIRROR` | — | — | 将本地 registry 未命中转为对该上游主机（例如 `docker.io`）的 pull-through 代理。 |
| `CORNUS_REGISTRY_MIRROR_CACHE` | — | on | 将镜像拉取到的内容持久化到本地 store（pull-through 缓存）。 |
| `CORNUS_REGISTRY_SOURCE` | — | 主机后端上为 `host-native` | 通过 `/v2/*` 重新导出 deploy backend 自身的本地镜像 store，而不是单独维护一个 CAS。`host-native` 在 `dockerhost` 后端下解析为本地 Docker daemon，在 `containerd` 后端下解析为主机 containerd store；在这些主机后端上是**默认值**。`off` 强制使用传统持久 CAS。未设置 `--storage` 时，registry **不保留单独的内容 store**。与 `CORNUS_REGISTRY_MIRROR` 互斥。见[复用本地镜像 store](#reusing-a-local-image-store)。 |

### 复用本地镜像 store {#reusing-a-local-image-store}

当你针对**本地 Docker 或 containerd 主机**开发时，镜像通常已经在本地
（来自 `docker build` / `docker pull`，或 cornus 构建），因此再向单独的 cornus registry 推送一份副本是多余的。
因此在主机后端上，cornus 的 `/v2/*` registry **默认成为该本地 store 的视图** —— `CORNUS_REGISTRY_SOURCE=host-native`，按后端解析。
两种情况下（在未设置 `--storage` 时）都不保留单独的 CAS，`_catalog` / 标签列表只反映本地 store，镜像生命周期由运行时负责（`docker image prune` 等）：

- 在 `containerd` 下，`/v2/*` 由主机 containerd 的**原生内容 store**直接支撑 —— 一个完整的**读写**视图。
  向 `/v2/*` push 的 `cornus build` 会直接导入该 store（按 digest 的 blob + 一条镜像记录），因此镜像立即可部署；
  pull 则从中重新导出。无需任何 build worker 配置。
- 在 `dockerhost` 下，`/v2/*` 是本地 Docker daemon 的**只读**视图：manifest/blob 未命中经由 `docker save` 提供，
  对 daemon 已有的镜像，部署会跳过 registry 拉取。由于传统 Docker 没有可按 blob 逐块写入的、按 digest 寻址的内容 store，
  向 `/v2/*` 的 **push 会被拒绝 `405`** —— `cornus build` 转而经由服务器路由，服务器把结果 `docker load` 进 daemon。
  （因此请针对服务器用 `cornus build` / `cornus compose build` 构建，而不是 in-process push。）

要改为保留传统的可 push CAS registry，设置 **`CORNUS_REGISTRY_SOURCE=off`**，
或传入显式 **`--storage`**（它保留 CAS 作为主层，仅在未命中时重新导出 —— 联合视图）。
已配置的 `CORNUS_REGISTRY_MIRROR`，或非主机后端（`bare`/`kubernetes`），也会保留传统 CAS。

面向本地开发，而非高扇出的共享 registry。关于 `dockerhost` 视图有一个注意点：`docker save` 会重新计算 digest，
因此先前 push 得到的 manifest digest 可能与重新导出的不同 —— 请按 tag 拉取。
（`containerd` 视图读取原生内容 store，因此 digest 得以保留。）

## 垃圾回收

空间可通过 `POST /.cornus/v1/gc` 按需回收，也可定期回收。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_GC_INTERVAL` | — | disabled | Background storage-GC scheduler 的 Go duration（例如 `1h`）。未设置即禁用；错误或非正值为 startup error。多个 replica 共享 `s3://` store 时，最多在一个上启用。 |
| `CORNUS_GC_LEASE` | — | disabled | 为 periodic GC 启用 Kubernetes `coordination.k8s.io` Lease leader gate（`namespace/name`，或默认 `cornus-gc` 的 `kube`）。需要设置 `CORNUS_GC_INTERVAL`。 |

## Build engine

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_BUILD_WORKER` | — | in-process BuildKit | 选择 build worker；`containerd` 将 execution、snapshot 与 content 委托给 host containerd。 |
| `CORNUS_BUILD_CONCURRENCY` | — | `NumCPU` | 允许并发 `/.cornus/v1/build` execution 数量（非正 / 不可解析时 fallback 到默认）。 |
| `CORNUS_MAX_BUILD_CONTEXT_BYTES` | — | — | 上传 build context size 的上限。 |
| `CORNUS_BUILD_CACHE_KEEP_BYTES` | — | — | GC 保留的 build cache 目标大小。 |
| `CORNUS_LAZY_BUILD` | — | off | Server-wide 按需经 9P 提供 `--build-context` dir（lazy build），而非 eager sync。 |
| `CORNUS_LAZY_9P` | — | — | 调整 lazy 9P build-context / remote-snapshotter path。 |
| `CORNUS_SNAPSHOTTER_TRACE` | — | off | 启用 remote snapshotter tracing（diagnostic）。 |

## Deploy backend

见[部署后端](/zh/reference/deploy-backends)。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | 选择 deploy backend：`dockerhost`、`containerd`、`bare` 或 `kubernetes` / `k8s`。Env-only（无 CLI flag）。 |
| `CORNUS_ALLOW_BIND_SOURCES` | — | deny | 允许作为 host-bind mount source 的、以 colon/comma 分隔的 host-path prefix（默认拒绝）。 |
| `CORNUS_ALLOW_PRIVILEGED` | — | deny | 允许 Kubernetes backend 上的 privileged workload。 |
| `CORNUS_EGRESS_POLICY` | — | — | 管理允许哪些 egress gateway route 的 server-side policy。 |
| `CORNUS_EGRESS_GATEWAY` | — | off | 将此 server 标为 egress gateway terminus。 |
| `CORNUS_CREDENTIALS_URL` | — | — | 作为 generic credential delivery fetch endpoint 向 workload 声明（injected env var）。 |
| `CORNUS_CARETAKER_CONFIG` | — | — | 传给 caretaker sidecar/companion 的 JSON caretaker role config。 |
| `CORNUS_AGENT_IMAGE` | — | — | In-cluster mount/deploy agent 使用的 image。 |
| `CORNUS_AGENT_DIR` | — | — | Client-agent artifact 的 directory（client-side）。 |

### Containerd backend

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_CONTAINERD_ADDRESS` | — | `/run/containerd/containerd.sock` | Containerd socket（标准 `CONTAINERD_ADDRESS` 是 fallback）。 |
| `CORNUS_CONTAINERD_NAMESPACE` | — | `cornus` | Workload 的 containerd namespace。 |
| `CORNUS_CONTAINERD_SNAPSHOTTER` | — | `overlayfs` | Rootfs snapshotter（overlay-backed host 设为 `native`）。 |
| `CORNUS_CONTAINERD_INSECURE_REGISTRIES` | — | 仅 `localhost` | Image pull 时视为 plain-HTTP 的逗号分隔 `host[:port]`。 |
| `CORNUS_CONTAINERD_LOG_MAX_BYTES` | — | 16 MiB | Log rotation size（保留一个旧 generation）。 |
| `CORNUS_CNI_BIN_DIR` | — | `/opt/cni/bin`（另有 `CNI_PATH`） | 发现 CNI plugin 的 directory。 |
| `CORNUS_CNI_SUBNET_BASE` | — | `10.4` | 每个 compose network 分配的 `/24` base。 |
| `CORNUS_DOCKER_SOCK` | — | `/var/run/docker.sock` | `dockerhost` backend 的 Docker socket（也是 client `cornus daemon docker` listen socket）。 |

### Bare backend

无守护进程的后端（`CORNUS_DEPLOY_BACKEND=bare`）。与 `containerd` 共享上面的 `CORNUS_CNI_*` 参数；不需要守护进程 socket。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_BARE_RUNTIME` | — | `runc` | 直接驱动的 OCI runtime 二进制（`runc`、`crun`、`youki` 或 gVisor 的 `runsc`——任何 runc-CLI 兼容二进制）；启动时校验。 |
| `CORNUS_BARE_STATS_SOURCE` | — | 自动（按 runtime 名称） | `Stats` 读取指标的来源：`runtime`（`runc events --stats`）或 `cgroup`（host cgroup 文件）。默认按 runtime basename 决定——`runsc`/`gvisor` 为沙箱化，使用 `runtime`；`runc`/`crun`/`youki` 使用 `cgroup`。命名特殊的安装可用此项覆盖。 |
| `CORNUS_BARE_SNAPSHOTTER` | — | overlay（native fallback） | Rootfs snapshotter；在拒绝 overlay-on-overlay 的 overlay-backed / docker-in-docker host 上设为 `native`。 |
| `CORNUS_BARE_INSECURE_REGISTRIES` | — | 仅 `localhost` | Image pull 时视为 plain-HTTP 的逗号分隔 `host[:port]`。 |
| `CORNUS_BARE_SYSTEMD_CGROUP` | — | off（cgroupfs） | 将 runtime 切换到 systemd cgroup driver（否则为 cgroupfs，runc 在 v1 和 v2 上直接管理）。 |
| `CORNUS_BARE_DNS` | — | on | netns gateway 上回答 guest container DNS 的进程内 resolver；设为 false 值可禁用，仅回退到 hosts-file 解析。 |
| `CORNUS_KNATIVE_STRICT` | — | `false` | 当 cluster 不提供 `serving.knative.dev/v1` 时，使启用 Knative 的部署失败，而不是带警告作为普通 Deployment 运行。 |
| `CORNUS_BARE_SHIM` | — | off | 启用每 container 独立监督 shim（cornus 的 conmon 类比），可在 cornus 重启后存活；off 时保持默认的进程内 supervisor。 |
| `CORNUS_BARE_REMOTE` | — | off | 让 `bare` backend 使用始终在线的每实例 remote-companion sidecar（与 `CORNUS_CONTAINERD_REMOTE` 相同）：companion 执行 client-local mount，并为 `cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` 改路。需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。 |

### Kubernetes backend

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_K8S_NAMESPACE` | — | in-cluster / current | Kubernetes backend 部署到的 namespace。 |
| `CORNUS_K8S_NET_DRIVER` | — | `services` | User network 默认 driver（`services`、`bridge`、`ipvlan`、`macvlan`、`cilium`）。 |
| `CORNUS_K8S_NET_STRICT` | — | `false` | 无法实现请求 network fabric 时 fail，而非 degrade。 |
| `CORNUS_K8S_POLICY_CNI` | — | `false` | 在支持 policy 的 CNI 上启用 NetworkPolicy isolation。 |
| `CORNUS_K8S_IMAGE_PULL_POLICY` | — | backend default | 覆盖 pod `imagePullPolicy`。 |
| `CORNUS_K8S_SIDECAR_IMAGE` | — | cornus image | Caretaker sidecar 使用的 image。 |

### Ingress 默认值

选择加入 [ingress](/zh/topics/ingress)（kubernetes backend）的 workload 所用的服务器端 fallback。也可设置为 Helm `ingress.*` value。

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | — | — | 自动派生 `<name>.<domain>` host 的 base wildcard domain。空时 workload 必须设置自己的 host/domain。 |
| `CORNUS_INGRESS_CLASS` | — | cluster default | 创建 Ingress 的默认 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | — | — | TLS-enabled ingress 的默认 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | — | `false` | 为 true（且设置 domain）时，拒绝 resolved host 在 domain 外的 workload。 |

## Tunnel

Tunnel 见[公网隧道](/zh/topics/tunnels)。`CORNUS_TUNNEL_BACKEND` 选择 public-URL backend：`ngrok`（默认）、`ssh`、`cloudflare` 或 `tailscale`；`CORNUS_TUNNEL_AUTHTOKEN` 提供 server-side 的默认 credential，在 client 省略时使用——同一个变量名在 client 自身的环境变量中设置时，也会填充 client 的 `cornus tunnel --authtoken` flag，即同一个名字在两个不同进程中承载同一类值。`CORNUS_TUNNEL_CLOUDFLARED_BIN`、`CORNUS_TUNNEL_TAILSCALE_BIN` 选择 binary；`CORNUS_TUNNEL_SSH_ADDR`、`CORNUS_TUNNEL_SSH_USER`、`CORNUS_TUNNEL_SSH_BIND`、`CORNUS_TUNNEL_SSH_URL_TEMPLATE`、`CORNUS_TUNNEL_SSH_URL_FROM_SESSION`、`CORNUS_TUNNEL_SSH_HOSTKEY`、`CORNUS_TUNNEL_SSH_KNOWN_HOSTS`、`CORNUS_TUNNEL_SSH_INSECURE` 配置 SSH reverse tunnel。

## Hub

Hub 见[工作负载 Hub](/zh/topics/hub)。Hub variable：`CORNUS_HUB_STORE`（catalog store，`kube` 使用 Kubernetes-backed store）、`CORNUS_HUB_REDIS`（distributed store Redis URL）、`CORNUS_HUB_FORWARD_URL` / `CORNUS_HUB_FORWARD_CA`（replica forward URL/CA）、`CORNUS_HUB_POLICY`（identity 到可访问 hub service 的 policy）和 `CORNUS_HUB_REGISTER_POLICY`（identity 到可注册/export service 的 policy）。

## 可观测性

观测模型见[架构概览](/zh/architecture/)。`CORNUS_OTEL`（`--otel`，默认 off）经标准 `OTEL_*` env 启用 OpenTelemetry trace/metric/log；设置任意 `OTEL_*` exporter/endpoint env 也会隐式启用。`CORNUS_METRICS_PROMETHEUS`（默认 off）暴露 Prometheus metric endpoint（仅在 OpenTelemetry 启用时有效）。

同一 `CORNUS_OTEL` / `OTEL_*` gate 也在**客户端 CLI**启用 tracing：在运行 `cornus` 的环境设置它，每次 invocation 都会产生 root span，并向 server（再向 caretaker）传递 W3C `traceparent`，因此 `cornus deploy` / `cornus build` / `cornus compose up` 呈现为一条端到端 trace，而不是孤立 server span。

## Client-side variable（参考）

这些由 CLI 而非 server 读取，但位于同一 `CORNUS_*` namespace。见[连接配置](/zh/reference/connection-config)和[远程工作流](/zh/topics/remote-workflows)。

| Variable | 默认值 | 含义 |
| --- | --- | --- |
| `CORNUS_SERVER` / `CORNUS_HOST` | selected profile，随后 `http://localhost:5000` | Client command 的 remote cornus server URL。 |
| `CORNUS_TOKEN` | — | Client request 的 bearer token（覆盖 profile `token`）。 |
| `CORNUS_CONFIG` | platform config path | Client [连接配置](/zh/reference/connection-config) file path。 |
| `CORNUS_CONTEXT` | config `current-context` | 要使用的 connection profile。 |
| `CORNUS_OUTPUT` | `auto` | Output rendering mode（`auto`、`plain`、`fancy`、`json`）。见[输出模式](/zh/guides/output-modes)。 |
| `CORNUS_CONDUIT` | profile / `port-forward` | Session conduit mode（`port-forward` 或 `socks5`）。 |
| `CORNUS_VIA_SERVER` | profile / direct | 让 workload streaming 经 server proxy。 |
| `CORNUS_BUILDER` | — | Delegated build 的 remote build endpoint。 |
| `CORNUS_REGISTRY` | server-advertised host | 不带 registry 部分的 tag 所用 registry host（remote build）。 |
