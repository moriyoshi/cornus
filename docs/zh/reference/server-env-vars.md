# 服务器环境变量

本页列出 [`cornus serve`](/zh/cli/serve) 和 server subsystem 读取的 `CORNUS_*` environment variable。部分对应 `cornus serve` flag (下表注明) ；大部分是 server、deploy backend、build engine 和 tunnel 直接读取的 env-only knob。

::: info
此列表来自 source tree (`grep 'CORNUS_[A-Z0-9_]+' pkg cmd`) ，旨在作为实用参考。可能包括少数内部或演进中的 knob；权威行为始终在代码中。省略 test-only variable (`CORNUS_TEST_*`) 。CLI (而非 server) 消费的 client-side variable 在文末单独分组。
:::

## General / listener

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_ADDR` | `--addr` | `:5000` | `/v2/*` 和 `/.cornus/v1/*` 的 HTTP listen address。因为容器化的 caretaker 会反向连接服务器，所以默认监听全部网络接口；工作负载不需要访问服务器时，可设置 `CORNUS_ADDR=127.0.0.1:5000` 把它限制在本机。参见[监听地址与暴露范围](/zh/cli/serve#监听地址与暴露范围)。 |
| `CORNUS_DATA` | — | platform data dir | Server data directory (registry filesystem store、upload、backend state) 。 |
| `CORNUS_ROOTLESS` | `--rootless` | off | 在 rootless mode (user namespace) 运行 build engine。 |
| `CORNUS_BUILDER_URL` | `--builder-url` | — | 将构建委托给上游 cornus 构建器 (例如 `ws://127.0.0.1:5099`) ，而不是在进程内构建。参见[将构建委托给构建器](#delegating-builds-to-a-builder)。 |
| `CORNUS_BUILDER_AUTO` | `--[no-]builder-auto` | 开启 | 当进程内引擎无法运行且未设置 `--builder-url` 时，自动启动一个特权 cornus 构建器容器。 |
| `CORNUS_BUILDER_IMAGE` | `--builder-image` | 自建 | 固定使用已发布的镜像作为构建器，而不是从正在运行的二进制文件构建。 |
| `CORNUS_BUILDER_BASE_IMAGE` | `--builder-base-image` | 主机发行版 | 自建构建器镜像所使用的基础镜像。 |
| `CORNUS_LOG_LEVEL` | — | `info` | Log verbosity (`debug`、`info`、`warn`、`error`) 。 |
| `CORNUS_ADVERTISE_URL` | — | — | mount-agent / caretaker sidecar 回拨的 cornus URL。Kubernetes backend 的 client-local mount 必需；`dockerhost`/`containerd` 上通过 `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` 启用同一 sidecar 路径时同样必需。仅在确实有东西回拨时才会要求它: 在同一主机上自行实现 client-local mount 的 co-located server，以及在部署时解析的 [`env` 类凭据](/zh/guides/credentials)，既不需要它也不需要 `CORNUS_AGENT_IMAGE`。 |
| `CORNUS_ADVERTISE_REGISTRY` | — | derived | 覆盖 server 向 client 声明的、deploy target 可 pull 的 registry `host[:port]` (和可选 scheme) (`GET /.cornus/v1/info`) 。 |
| `CORNUS_ACTIVITY_MAX_BYTES` | — | `8388608` | 飞行记录日志 (`<data dir>/activity`) 的大小上限，并在旁保留一份此前的世代。参见 [cornus activity](/zh/cli/activity)。 |
| `CORNUS_HOST_PATH_MAP` | — | 自动检测 | 以逗号分隔的 `container-path=host-path` 键值对，声明此服务端的路径在主机上如何呈现，供**在容器中**针对主机 docker/containerd 运行的 cornus 使用。仅当服务端自己无法推断时才需要 (非 docker 运行时，或它无法检查的容器) 。显式条目总是优先于检测到的条目。格式错误的值会导致启动失败。参见[在容器中运行服务端](/zh/guides/server-in-a-container)。 |
| `CORNUS_HOST_NETWORK` | — | 自动检测 | `1`/`0`: 直接声明此服务端的容器是否与主机共享网络命名空间，覆盖自我探查得出的结论。在基于 CNI 的主机后端 (`containerd`、`bare`) 上，独立的命名空间意味着已发布端口会在服务端自己的容器内部做 NAT，主机看不到它们，因此当服务端**检测到**这种情况时会拒绝启动 — 设为 `0` 表示已知悉并仍然启动，或在服务端其实已有主机网络但无法判定时设为 `1`。`bare` 没有可询问的守护进程，因此这是它唯一的答案。格式错误的值会导致启动失败。参见[在容器中运行服务端](/zh/guides/server-in-a-container)。 |
| `CORNUS_REPLICA_ID` | — | — | 此副本的稳定身份；用于分布式 hub 存储和 GC 领导者选举控制，也是自动签发的副本间转发 JWT 的 `sub` 与 `kid`。 |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | 部署后端: `dockerhost`、`podman`、`kubernetes` (`k8s`) 、`containerd`、`bare` 或 `incus`。无法识别的值会导致启动错误。如果接受 `docker` 之类的近似值，就会在选择 `dockerhost` 的同时悄然让镜像仓库脱离主机原生重新导出。 |

## Storage

完整 backend catalog 见[存储后端](/zh/reference/storage-backends)。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | data dir 下的 filesystem | Registry persistence backend: 路径、`file://`、`mem://`、`s3://bucket?region=&endpoint=&path_style=`，或 (`-tags cloudblob` 后) `gs://` / `azblob://`。 |

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

认证模型见[安全与认证](/zh/guides/security)。未设置 auth env 时，server 无 credential 即可接受 request。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_AUTH_TOKEN` | — | — | 作为 credential 接受的 static bearer token。 |
| `CORNUS_INSTALLATION_SECRET` | — | 在 `CORNUS_DATA` 下生成 | 安装范围的内部签名 key。仅在启用客户端认证时，Cornus 用它为自身 registry 的 build/pull 流量签发短期 scoped credential。所有副本应设置相同值；它本身不会启用客户端认证。 |
| `CORNUS_AUTH_KEYSTORE` | — | 自动 | SSH 密钥注册存储模式: `file` 写入 `<CORNUS_DATA>/auth/authorized_keys`；`none` 保留声明式密钥，但禁用运行时注册。未设置时，只有通过其他方式配置了密钥身份验证或存储已存在，文件存储才会启用。 |
| `CORNUS_AUTHORIZED_KEYS` | — | — | 以换行分隔的 OpenSSH `authorized_keys` 条目。设置后会启用 SSH 公钥客户端身份验证；这些声明式条目通过 API 只读。 |
| `CORNUS_TLS_CERT` | `--tls-cert` | — | PEM certificate file；与 `--tls-key` 一同设置时提供 HTTPS。 |
| `CORNUS_TLS_KEY` | `--tls-key` | — | PEM private-key file；与 `--tls-cert` 一同设置时提供 HTTPS。 |
| `CORNUS_TLS_CLIENT_CA` | `--tls-client-ca` | — | 验证 client certificate (mTLS) 的 PEM CA bundle。已验证 cert CommonName 成为 caller identity；提交 cert 仍可选。 |
| `CORNUS_JWT_ISSUER` | — | — | 期望 JWT `iss` claim。 |
| `CORNUS_JWT_AUDIENCE` | — | — | 期望 JWT `aud` claim (必须匹配 client `kube-auth.audience`) 。 |
| `CORNUS_JWT_HS256_SECRET` | — | — | 验证 HS256-signed JWT 的 shared secret。 |
| `CORNUS_JWT_PUBLIC_KEY` | — | — | 验证 asymmetric JWT 的 PEM public key path (RSA→RS256、ECDSA→ES256) 。 |
| `CORNUS_JWT_JWKS_FILE` | — | — | JWT verification 用的 local JWKS document path。 |
| `CORNUS_JWT_JWKS_URL` | — | — | JWT verification 用的 remote JWKS endpoint URL。 |
| `CORNUS_JWT_SCOPE_MAP` | — | — | YAML/JSON scope map 的路径: 把第三方 issuer 的 claim 转换成 cornus scope 的 rule 集合。对不携带 cornus `scope` claim 的 JWKS 验证 token 是必需的。参见 [scope 映射](/zh/guides/security#将第三方-claim-映射到-cornus-scope)。 |
| `CORNUS_JWT_DEFAULT_SCOPE` | — | — | 没有命中任何 `CORNUS_JWT_SCOPE_MAP` rule 的 token 所用的 catch-all scope (例如 `api`)。它追加在 map 之后，因此显式 rule 仍具有决定权。 |
| `CORNUS_API_POLICY` | — | — | `/.cornus/v1/*` surface 的 per-identity authorization policy。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | 即使 auth 已启用，也允许 unauthenticated registry pull。 |
| `CORNUS_CLIENT_TOKEN` | — | — | Caretaker Docker-API proxy 用于驱动 client deploy API 的 client-scoped token。 |
| `CORNUS_CLIENT_TOKEN_SECRET` | — | — | 保存 client-scoped token 的 Kubernetes Secret reference (`name/key`) ；启用 workload `docker:` block 必需。 |
| `CORNUS_CARETAKER_TOKEN` | — | — | 认证 caretaker (sidecar) 回调至 server 的 token。 |
| `CORNUS_CARETAKER_TOKEN_SECRET` | — | — | 保存 caretaker token 的 Kubernetes Secret reference。 |
| `CORNUS_CARETAKER_TLS_SECRET` | — | — | 保存 caretaker TLS material 的 Kubernetes Secret。 |

## Registry

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_STORAGE` | `--storage` | filesystem | 见[Storage](#storage) / [存储后端](/zh/reference/storage-backends)。 |
| `CORNUS_REGISTRY_ANONYMOUS_PULL` | — | off | 允许 anonymous registry pull (见[认证](#authentication-和-api-policy)) 。 |
| `CORNUS_REGISTRY_MIRROR` | — | — | 将本地 registry 未命中转为对该上游主机 (例如 `docker.io`) 的 pull-through 代理。 |
| `CORNUS_REGISTRY_MIRROR_CACHE` | — | on | 将镜像拉取到的内容持久化到本地 store (pull-through 缓存) 。 |
| `CORNUS_REGISTRY_SOURCE` | — | 主机后端上为 `host-native` | 通过 `/v2/*` 重新导出 deploy backend 自身的本地镜像 store，而不是单独维护一个 CAS。`host-native` 在 `dockerhost` 后端下解析为本地 Docker daemon，在 `containerd` 后端下解析为主机 containerd store；在这些主机后端上是**默认值**。`off` 强制使用传统持久 CAS。未设置 `--storage` 时，registry **不保留单独的内容 store**。与 `CORNUS_REGISTRY_MIRROR` 互斥。见[复用本地镜像 store](#reusing-a-local-image-store)。 |

### 复用本地镜像 store {#reusing-a-local-image-store}

当你针对**本地 Docker 或 containerd 主机**开发时，镜像通常已经在本地
(来自 `docker build` / `docker pull`，或 cornus 构建) ，因此再向单独的 cornus registry 推送一份副本是多余的。
因此在主机后端上，cornus 的 `/v2/*` registry **默认成为该本地 store 的视图** —— `CORNUS_REGISTRY_SOURCE=host-native`，按后端解析。
两种情况下 (在未设置 `--storage` 时) 都不保留单独的 CAS，`_catalog` / 标签列表只反映本地 store，镜像生命周期由运行时负责 (`docker image prune` 等) :

- 在 `containerd` 下，`/v2/*` 由主机 containerd 的**原生内容 store**直接支撑 —— 一个完整的**读写**视图。
  向 `/v2/*` push 的 `cornus build` 会直接导入该 store (按 digest 的 blob + 一条镜像记录) ，因此镜像立即可部署；
  pull 则从中重新导出。无需任何 build worker 配置。
- 在 `dockerhost` 下，`/v2/*` 是本地 Docker daemon 的**只读**视图: manifest/blob 未命中经由 `docker save` 提供，
  对 daemon 已有的镜像，部署会跳过 registry 拉取。由于传统 Docker 没有可按 blob 逐块写入的、按 digest 寻址的内容 store，
  向 `/v2/*` 的 **push 会被拒绝 `405`** —— `cornus build` 转而经由服务器路由，服务器把结果 `docker load` 进 daemon。
  (因此请针对服务器用 `cornus build` / `cornus compose build` 构建，而不是 in-process push。)

要改为保留传统的可 push CAS registry，设置 **`CORNUS_REGISTRY_SOURCE=off`**，
或传入显式 **`--storage`** (它保留 CAS 作为主层，仅在未命中时重新导出 —— 联合视图) 。
已配置的 `CORNUS_REGISTRY_MIRROR`，或非主机后端 (`bare`/`kubernetes`) ，也会保留传统 CAS。

面向本地开发，而非高扇出的共享 registry。关于 `dockerhost` 视图有一个注意点: `docker save` 会重新计算 digest，
因此先前 push 得到的 manifest digest 可能与重新导出的不同 —— 请按 tag 拉取。
(`containerd` 视图读取原生内容 store，因此 digest 得以保留。)

## 垃圾回收

空间可通过 `POST /.cornus/v1/gc` 按需回收，也可定期回收。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_GC_INTERVAL` | — | disabled | Background storage-GC scheduler 的 Go duration (例如 `1h`) 。未设置即禁用；错误或非正值为 startup error。多个 replica 共享 `s3://` store 时，最多在一个上启用。 |
| `CORNUS_GC_LEASE` | — | disabled | 为 periodic GC 启用 Kubernetes `coordination.k8s.io` Lease leader gate (`namespace/name`，或默认 `cornus-gc` 的 `kube`) 。需要设置 `CORNUS_GC_INTERVAL`。 |

## Build engine

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_BUILD_WORKER` | — | in-process BuildKit | 选择 build worker；`containerd` 将 execution、snapshot 与 content 委托给 host containerd。 |
| `CORNUS_BUILD_CONCURRENCY` | — | `NumCPU` | 允许并发 `/.cornus/v1/build` execution 数量 (非正 / 不可解析时 fallback 到默认) 。 |
| `CORNUS_MAX_BUILD_CONTEXT_BYTES` | — | — | 上传 build context size 的上限。 |
| `CORNUS_BUILD_CACHE_KEEP_BYTES` | — | — | GC 保留的 build cache 目标大小。 |
| `CORNUS_LAZY_BUILD` | — | off | Server-wide 按需经 9P 提供 `--build-context` dir (lazy build) ，而非 eager sync。 |
| `CORNUS_LAZY_9P` | — | — | 调整 lazy 9P build-context / remote-snapshotter path。 |
| `CORNUS_SNAPSHOTTER_TRACE` | — | off | 启用 remote snapshotter tracing (diagnostic) 。 |

### 将构建委托给构建器 {#delegating-builds-to-a-builder}

进程内构建引擎无法在非特权下运行。BuildKit 会挂载每一个快照，而 `mount(2)` 需要 `CAP_SYS_ADMIN`，因此非特权的 `cornus serve` 会让所有构建失败——通常表现为读取 Dockerfile 时的 `lchown ...: operation not permitted`，或 `failed to mount ...: operation not permitted`。仅靠 `--rootless` 并不能改变这一点: 它只设置 BuildKit 的 rootless 标志，并不会创建 user namespace；而在 `kernel.apparmor_restrict_unprivileged_userns=1` 的主机 (Ubuntu 24.04 及以后) 上，非特权 user namespace 本身就被禁止。

默认情况下这会自动处理。首次构建时，无法执行 `mount(2)` 的服务器会启动一个特权 cornus 构建器容器，并将构建委托给它:

```
build engine cannot mount(2) as this user; using a containerized builder
delegating builds to containerized builder url=ws://127.0.0.1:5099
```

构建器镜像不是拉取的，而是**从正在运行的二进制文件构建**: 服务器通过 Docker 守护进程把自身的可执行文件打包成一个一次性镜像 `cornus-builder:<binary-hash>`，因此构建器与服务器逐字节相同，无需访问任何 registry，也不会出现版本漂移。标签是该二进制文件的内容哈希，所以升级 cornus 会产生新镜像，而二进制文件未变时则复用已有镜像 (首次构建需要几秒来创建它，之后不再需要) 。

基础镜像默认取自主机自身的发行版 (读取 `/etc/os-release`) ，因为本地构建的 cornus 通常动态链接到主机的 libc，在不匹配的基础镜像上无法执行。可用 `--builder-base-image` 覆盖；指定 `--builder-image` 则固定使用已发布的镜像而不自建。基础镜像必须提供 `runc`，BuildKit 在每个 `RUN` 时都会调用它；若缺失，会在构建镜像时安装。

容器名为 `cornus-builder`，以 `--privileged` 和主机网络运行，并使用自己的 `cornus-builder-cache` 卷——绝不使用服务器的数据目录，因为构建器以 root 运行，会留下 root 所有的快照。它是延迟启动的，因此从不构建的服务器不会启动构建器；重启时会接管已有容器而不是重复创建，从而保持构建缓存是热的。权限是通过实际尝试一次 bind mount 来探测的，而不是检查 uid——因为进程可能是 root 却被阻止，也可能非 root 却有权限。

构建器还会**镜像此服务器的镜像仓库模式**，因为构建会话会原样中继给它，最终由构建器决定如何交付结果:

- **重新导出模式** (主机后端且未设 `--storage`，即默认): 镜像仓库为只读，因此构建器会共享主机的 Docker socket，并像进程内构建一样将完成的镜像 `docker load` 到同一守护进程。若让构建器自行解析模式，它会改为推送至只读镜像仓库，并以 `405 Method Not Allowed` 失败。
- **CAS 模式** (显式设置 `--storage` 或使用非主机后端): 构建器获得自己的存储，并将结果推送至目标镜像仓库。

更改模式会改变构建器配置，因此会重新创建现有构建器，而不是复用它。此方式不支持重新导出主机 **containerd** store 的镜像仓库，因为容器化构建器无法写入该 store；系统会给出说明并拒绝配置，而不是等到稍后才失败。

由于它只在构建本来就会彻底失败的主机上生效，因此不会改变已经能够成功构建的主机的行为。可用 `--no-builder-auto` 关闭；另外它需要一个可访问的 Docker 守护进程。

若想自行管理构建器，请显式地为服务器指定:

```sh
docker run -d --name cornus-builder --privileged --network host \
  -v cornus-builder-cache:/var/lib/cornus \
  ghcr.io/moriyoshi/cornus:latest \
  serve --addr 127.0.0.1:5099 --storage /var/lib/cornus/registry

cornus serve --addr :5000 --builder-url ws://127.0.0.1:5099
```

两个构建入口都会被委托: `GET /.cornus/v1/build/attach` 作为原始 WebSocket 直接转接到构建器，`POST /.cornus/v1/build` 则连同其 context tar 和查询参数原样转发。由于 attach 路径是逐字节中继的，构建器是针对**调用方**的导出来终结 9P 的——调用方的构建上下文、命名上下文和 secret 不会落到执行委托的主机上。在任何内容到达构建器之前，执行委托的服务器仍会照常执行授权检查。

自行运行构建器时有三点需要注意 (上面的自动构建器已经处理了全部三点):

- **要传 `--storage`。** 否则服务器会默认使用 host-native re-export，尝试把结果加载进本地 Docker 守护进程，而构建器容器并没有它。此时构建会成功，只在导出阶段失败，并给出容易误导的 `failed to copy to tar: read/write on closed pipe`。
- **给它独立的数据目录或卷。** 构建器以 root 运行，因此共享非特权服务器的数据目录会留下 root 所有的快照 (`drwx------`) ，非特权服务器随后将无法进入这些目录。
- 推荐使用如上的 `--network host`，这样 `localhost:5000/app` 之类的镜像引用在构建器内外含义一致。

## Deploy backend

见[部署后端](/zh/reference/deploy-backends)。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_DEPLOY_BACKEND` | — | `dockerhost` | 选择 deploy backend: `dockerhost`、`containerd`、`bare`、`incus` 或 `kubernetes` / `k8s`。Env-only (无 CLI flag) 。 |
| `CORNUS_ALLOW_BIND_SOURCES` | — | deny | 允许作为 host-bind mount source 的、以 colon/comma 分隔的 host-path prefix (默认拒绝) 。 |
| `CORNUS_ALLOW_PRIVILEGED` | — | deny | 允许 Kubernetes backend 上的 privileged workload。 |
| `CORNUS_EGRESS_POLICY` | — | — | 管理允许哪些 egress gateway route 的 server-side policy。 |
| `CORNUS_EGRESS_GATEWAY` | — | off | 将此 server 标为 egress gateway terminus。 |
| `CORNUS_CREDENTIALS_URL` | — | — | 作为 generic credential delivery fetch endpoint 向 workload 声明 (injected env var) 。 |
| `CORNUS_CARETAKER_CONFIG` | — | — | 传给 caretaker sidecar/companion 的 JSON caretaker role config。 |
| `CORNUS_AGENT_IMAGE` | — | — | 用于 mount/egress/deploy caretaker sidecar 或 companion 的、内嵌 cornus 的 image——Kubernetes pod sidecar、`dockerhost`/`containerd`/`bare` 的 egress companion，以及 (配合 `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`/`CORNUS_BARE_REMOTE`) 始终在线的 remote companion (mount、port-forward/tunnel 改路、exec agent 转发) 。 |
| `CORNUS_AGENT_DIR` | — | — | Client-agent artifact 的 directory (client-side) 。 |
| `CORNUS_DOCKER_REMOTE` | — | off | 让 `dockerhost` backend 使用始终在线的每实例 remote-companion sidecar: companion 共享每个实例的 network namespace，无论部署是否使用 `--mount` 都会创建——面向与本 server 不同机的 Docker daemon (例如 `DOCKER_HOST=tcp://...`) 。client-local mount 改由 companion 实现 (带 `rshared`/`rslave` propagation 的 Docker volume) ，而不是默认的单机 kernel-9p 快路径；`cornus port-forward`/`cornus tunnel` 和 `cornus exec --forward-agent` 也经 companion 改路，而不是由 server 直接拨号到实例。需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。见[部署后端](/zh/reference/deploy-backends)。 |
| `CORNUS_PODMAN_SOCKET` | — | 无 | `podman` 后端使用的 Podman API 端点: 一个路径、`unix://` / `tcp://` URL，或 `ssh://` 目标。**没有默认值**，也不会去探测 `CONTAINER_HOST` / `DOCKER_HOST` 或约定的 socket 路径。本变量与 `CORNUS_PODMAN_SERVICE` 都未设置时，服务器拒绝启动，这样它到底驱动了哪个 daemon 始终能从配置中回答。两者同时设置属于错误。 |
| `CORNUS_PODMAN_SERVICE` | — | off | 让 cornus 自己在私有 socket 上运行并监督 `podman system service`，因此无需启用任何 `podman.socket` unit。只需要 `PATH` 上有 `podman` 二进制。与 `CORNUS_PODMAN_SOCKET` 互斥。 |
| `CORNUS_PODMAN_REMOTE` | — | off | 让 `podman` 后端启用按实例常驻的 remote companion，作用与 `CORNUS_DOCKER_REMOTE` 之于 `dockerhost` 相同。**对 rootless podman 上的 `cornus port-forward` / `cornus tunnel` 是必需的**: rootless workload 的网络命名空间在宿主机上不可路由，未设置时这些命令会立即拒绝而不是等待超时。同时需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。 |

### Containerd backend

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_CONTAINERD_ADDRESS` | — | `/run/containerd/containerd.sock` | Containerd socket (标准 `CONTAINERD_ADDRESS` 是 fallback) 。 |
| `CORNUS_CONTAINERD_NAMESPACE` | — | `cornus` | Workload 的 containerd namespace。 |
| `CORNUS_CONTAINERD_SNAPSHOTTER` | — | `overlayfs` | Rootfs snapshotter (overlay-backed host 设为 `native`) 。 |
| `CORNUS_CONTAINERD_INSECURE_REGISTRIES` | — | 仅 `localhost` | Image pull 时视为 plain-HTTP 的逗号分隔 `host[:port]`。 |
| `CORNUS_CONTAINERD_LOG_MAX_BYTES` | — | 16 MiB | Log rotation size (保留一个旧 generation) 。 |
| `CORNUS_CNI_BIN_DIR` | — | `/opt/cni/bin` (另有 `CNI_PATH`) | 发现 CNI plugin 的 directory。 |
| `CORNUS_CNI_SUBNET_BASE` | — | `10.4` | 每个 compose network 分配的 `/24` base。 |
| `CORNUS_CONTAINERD_REMOTE` | — | off | 让 `containerd` backend 使用与 `CORNUS_DOCKER_REMOTE` 相同的、始终在线的每实例 remote-companion sidecar: companion 加入每个实例已固定的 network namespace，无论部署是否使用 `--mount` 都会创建 (由 companion container/task 执行 kernel 9P mount，经带 `rshared`/`rslave` OCI mount option 的共享主机目录中继；`cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` 同样经 companion 改路) 。它**不会**让 containerd 本身可远程访问 (其 client dialer 只连本地 unix socket) ——它改变的是在仍然同机的 daemon 上如何实现 port-forward / exec agent 转发，而且正是它才让本后端可以使用客户端本地 mount: 与 `dockerhost`/`bare` 不同，这里没有可回退的 kernel-9p 快路径，所以未设置时带 `--mount` 的 deploy 会被拒绝 (错误信息会点明这个变量) 。需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。见[部署后端](/zh/reference/deploy-backends)。 |
| `CORNUS_DOCKER_SOCK` | — | `$XDG_RUNTIME_DIR/cornus-docker.sock` | 客户端 [`cornus daemon docker`](/zh/cli/daemon) 代理**监听**的 Unix socket。它**不**配置 `dockerhost` backend — 该 backend 读取 `DOCKER_HOST`。 |

### Bare backend

无守护进程的后端 (`CORNUS_DEPLOY_BACKEND=bare`) 。与 `containerd` 共享上面的 `CORNUS_CNI_*` 参数；不需要守护进程 socket。

| Variable | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_BARE_RUNTIME` | — | `runc` | 直接驱动的 OCI runtime 二进制 (`runc`、`crun`、`youki` 或 gVisor 的 `runsc`——任何 runc-CLI 兼容二进制) ；启动时校验。 |
| `CORNUS_BARE_STATS_SOURCE` | — | 自动 (按 runtime 名称) | `Stats` 读取指标的来源: `runtime` (`runc events --stats`) 或 `cgroup` (host cgroup 文件) 。默认按 runtime basename 决定——`runsc`/`gvisor` 为沙箱化，使用 `runtime`；`runc`/`crun`/`youki` 使用 `cgroup`。命名特殊的安装可用此项覆盖。 |
| `CORNUS_BARE_SNAPSHOTTER` | — | overlay (native fallback) | Rootfs snapshotter；在拒绝 overlay-on-overlay 的 overlay-backed / docker-in-docker host 上设为 `native`。 |
| `CORNUS_BARE_INSECURE_REGISTRIES` | — | 仅 `localhost` | Image pull 时视为 plain-HTTP 的逗号分隔 `host[:port]`。 |
| `CORNUS_BARE_SYSTEMD_CGROUP` | — | off (cgroupfs) | 将 runtime 切换到 systemd cgroup driver (否则为 cgroupfs，runc 在 v1 和 v2 上直接管理) 。 |
| `CORNUS_BARE_DNS` | — | on | netns gateway 上回答 guest container DNS 的进程内 resolver；设为 false 值可禁用，仅回退到 hosts-file 解析。 |
| `CORNUS_BARE_SHIM` | — | off | 启用每 container 独立监督 shim (cornus 的 conmon 类比) ，可在 cornus 重启后存活；off 时保持默认的进程内 supervisor。 |
| `CORNUS_BARE_REMOTE` | — | off | 让 `bare` backend 使用始终在线的每实例 remote-companion sidecar (与 `CORNUS_CONTAINERD_REMOTE` 相同) : companion 执行 client-local mount，并为 `cornus port-forward`/`cornus tunnel`/`cornus exec --forward-agent` 改路。需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。 |

### Incus backend

Incus backend (`CORNUS_DEPLOY_BACKEND=incus`) ，它将 OCI 镜像作为 Incus 应用容器运行。需要 Incus 6.3+ 以及 **daemon** 主机上的 `skopeo` + `umoci`。完全不使用 `CORNUS_CNI_*` 参数——instance 网络由 incusd 拥有。

| 变量 | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_INCUS_SOCKET` | — | `/var/lib/incus/unix.socket` | Incus daemon unix socket。 |
| `CORNUS_INCUS_PROJECT` | — | `default` | 创建 instance 所用的 Incus project。 |
| `CORNUS_INCUS_STORAGE_POOL` | — | `default` | 后端创建 custom volume 所用的 Incus storage pool: 部署的托管 `volumes`，以及 remote 模式下 companion 的共享 agent volume。两者都没有的部署完全不会碰 pool——instance 从 project 的 profile 取得其根磁盘。 |
| `CORNUS_INCUS_INSECURE_REGISTRIES` | — | 仅环回地址 | 将镜像引用交给 incusd 时按明文 HTTP 处理的 `host[:port]`，以逗号 / 空格分隔。由于 incusd 通过 `skopeo` 拉取，而 skopeo 会拒绝明文 HTTP 的注册表，daemon 主机上还需要一条对应的 `/etc/containers/registries.conf.d/` 条目。 |
| `CORNUS_INCUS_REMOTE` | — | off | 打开 caretaker companion 路径，与 `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE`/`CORNUS_BARE_REMOTE` 对各自 backend 所做的一样: 每个 replica 都会获得一个带 PortForward 和 AgentRelay caretaker role 的 companion instance，端口转发流量改为经它转发而不是直接连接 instance 地址，`cornus exec --forward-agent` 也随之可用 (不打开它时会被预先拒绝) 。需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`；缺少其中之一时部署会预先失败。它不会带来客户端本地 mount 或 egress——参见[部署后端](/zh/reference/deploy-backends#incus)。 |

### Kubernetes backend

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_K8S_NAMESPACE` | — | in-cluster / current | Kubernetes backend 部署到的 namespace。 |
| `CORNUS_KUBE_QPS` | — | `50` | Kubernetes 客户端每秒查询数的请求速率限制。可增减此值以调整并发部署和就绪操作期间的客户端限流。 |
| `CORNUS_KUBE_BURST` | — | `100` | Kubernetes 客户端限速器的突发容量。 |
| `CORNUS_K8S_NET_DRIVER` | — | `services` | User network 默认 driver (`services`、`bridge`、`ipvlan`、`macvlan`、`cilium`) 。 |
| `CORNUS_K8S_NET_STRICT` | — | `false` | 无法实现请求 network fabric 时 fail，而非 degrade。 |
| `CORNUS_K8S_POLICY_CNI` | — | `false` | 在支持 policy 的 CNI 上启用 NetworkPolicy isolation。 |
| `CORNUS_K8S_IMAGE_PULL_POLICY` | — | backend default | 覆盖 pod `imagePullPolicy`。 |
| `CORNUS_K8S_SIDECAR_IMAGE` | — | cornus image | Caretaker sidecar 使用的 image。 |
| `CORNUS_KNATIVE_STRICT` | — | `false` | 当 cluster 不提供 `serving.knative.dev/v1` 时，使启用 Knative 的部署失败，而不是带警告作为普通 Deployment 运行。 |

### Ingress 默认值

选择加入 [ingress](/zh/guides/ingress) (kubernetes backend) 的 workload 所用的服务器端 fallback。也可设置为 Helm `ingress.*` value。

| Variable | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | — | — | 自动派生 `<name>.<domain>` host 的 base wildcard domain。空时 workload 必须设置自己的 host/domain。 |
| `CORNUS_INGRESS_CLASS` | — | cluster default | 创建 Ingress 的默认 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | — | — | TLS-enabled ingress 的默认 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | — | `false` | 为 true (且设置 domain) 时，拒绝 resolved host 在 domain 外的 workload。 |
| `CORNUS_INGRESS_LISTEN` | — | — | 将服务器自身的入口前端绑定到该地址 (例如 `:8080`)，在服务器所在的网络上提供已声明的主机和路径。为空时，前端入口只能通过[`cornus ingress-tunnel`](/zh/cli/ingress-tunnel)访问。绑定失败只记录日志，不会致命。 |
| `CORNUS_INGRESS_CONTROLLER` | — | 自动发现 | 入口隧道把流量交给的集群入口控制器 Service，格式为 `<namespace>/<service>[:httpPort/httpsPort]`。为空时按知名名称自动发现。 |

## 隧道

参见[隧道](/zh/guides/tunnels)。

| 变量 | 标志 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_TUNNEL_BACKEND` | — | `ngrok` | 公共 URL 隧道后端: `ngrok` (默认) 、`ssh` (SSH 反向隧道) 、`cloudflare` (Cloudflare Tunnel) 或 `tailscale` (Tailscale Funnel) 。 |
| `CORNUS_TUNNEL_AUTHTOKEN` | — | — | 所选隧道后端的服务器端默认凭据，在客户端省略凭据时使用。同名变量若设置在客户端自身环境中，也会填充客户端的 `cornus tunnel --authtoken` 标志——名称相同、进程不同、值的种类相同。 |
| `CORNUS_TUNNEL_CLOUDFLARED_BIN` | — | PATH 中的 `cloudflared` | `cloudflared` 二进制文件的路径。 |
| `CORNUS_TUNNEL_TAILSCALE_BIN` | — | PATH 中的 `tailscale` | `tailscale` 二进制文件的路径。 |
| `CORNUS_TUNNEL_SSH_ADDR` | — | — | SSH 隧道服务器地址。 |
| `CORNUS_TUNNEL_SSH_USER` | — | — | SSH 隧道用户。 |
| `CORNUS_TUNNEL_SSH_BIND` | — | — | SSH 反向隧道的远程绑定地址。[入口隧道](/zh/cli/ingress-tunnel)可以保留该端口，同时把主机部分替换为要发布的入口主机名；sish 风格的中继正是以此授予你声明的主机名。 |
| `CORNUS_TUNNEL_SSH_URL_TEMPLATE` | — | — | 从 SSH 隧道派生公共 URL 的模板。 |
| `CORNUS_TUNNEL_SSH_URL_FROM_SESSION` | — | off | 从 SSH 会话输出派生公共 URL。 |
| `CORNUS_TUNNEL_SSH_HOSTKEY` | — | — | 预期的 SSH 主机密钥。 |
| `CORNUS_TUNNEL_SSH_KNOWN_HOSTS` | — | — | 用于验证 SSH 主机的 `known_hosts` 文件路径。 |
| `CORNUS_TUNNEL_SSH_INSECURE` | — | off | 跳过 SSH 主机密钥验证 (仅供测试) 。 |

## Hub (工作负载到工作负载覆盖网络)

参见[工作负载 Hub](/zh/guides/hub)。

| 变量 | 标志 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_HUB_STORE` | — | 内存 | Hub 目录存储；`kube` 使用 Kubernetes 后端存储。 |
| `CORNUS_HUB_REDIS` | — | — | 分布式 Hub 存储的 Redis URL (启用跨副本目录) 。 |
| `CORNUS_HUB_FORWARD_URL` | — | — | 副本将 Hub 中继流量转发到的 URL。 |
| `CORNUS_HUB_FORWARD_CA` | — | — | 用于验证 Hub 转发端点的 PEM CA bundle。 |
| `CORNUS_HUB_POLICY` | — | — | 管理哪些身份可以访问哪些 Hub 服务的策略。 |
| `CORNUS_HUB_REGISTER_POLICY` | — | — | 管理哪些身份可以注册 (导出) Hub 服务的策略。 |

启用客户端身份验证时，选择任一分布式存储也会自动启用副本间转发凭据。每个副本都在 `$CORNUS_DATA/peer.key` 保存 ECDSA P-256 私钥，只按现有心跳或 Lease 的有效期发布公钥，并向三个 `/.cornus/v1/*/forward` 端点发送有效期为 5 分钟、scope 为 `peer` 的 ES256 JWT。无需额外环境变量。设置 `CORNUS_AUTH_TOKEN` 时，为支持混合版本的滚动更新，旧的共享令牌仍保持绝对优先级。

## 可观测性

观测模型见[架构概览](/zh/architecture/)。

| 变量 | Flag | 默认值 | 含义 |
| --- | --- | --- | --- |
| `CORNUS_OTEL` | `--otel` | off | 经标准 `OTEL_*` env 启用 OpenTelemetry trace/metric/log；设置任意 `OTEL_*` exporter/endpoint env 也会隐式启用。 |
| `CORNUS_METRICS_PROMETHEUS` | — | off | 暴露 Prometheus metric endpoint (仅在 OpenTelemetry 启用时有效) 。 |
| `CORNUS_OBS` | `--[no-]obs` | on | 启用内置可观测性存储: 记录已部署工作负载的日志，并将其 OTLP 追踪和指标接收到本地数据库。这不同于由 cornus 检测*自身*的 `CORNUS_OTEL`。默认值取决于构建: 每个发布版二进制文件和已发布镜像 (均包含该存储) 中为 on；自行构建但未使用 `-tags "imbh sable_extern_lib"` 的二进制文件中为 off。使用 `--no-obs` 将其关闭。 |
| `CORNUS_OBS_DIR` | `--obs-dir` | `<data-dir>/observability` | 保存可观测性数据库的目录。相对路径以数据目录为根。 |
| `CORNUS_OBS_RETENTION` | `--obs-retention` | `168h` | 丢弃早于此时限的已记录遥测数据 (`0` = 保留到大小上限生效) 。向上取整到整天。 |
| `CORNUS_OBS_MAX_BYTES` | `--obs-max-bytes` | `536870912` | 存储的磁盘大小上限，以字节为单位 (`0` = 无限制) 。 |
| `CORNUS_OBS_RECORD_LOGS` | `--obs-record-logs` | on | 将每个受管工作负载的 stdout/stderr 记录到存储。每个工作负载会占用一个跟随流；`--no-obs-record-logs` 将其关闭。 |
| `CORNUS_OBS_RECORD_METRICS` | `--obs-record-metrics` | on | 定时采样并记录每个受管工作负载的 CPU、内存、网络和磁盘用量，同时记录服务器自身的用量。与日志记录不同，这**不**要求启用 `CORNUS_OBS`: 仅设置 `CORNUS_OBS_EXPORT_ENDPOINT` 时也可工作，即只转发而不存储。`--no-obs-record-metrics` 将其关闭。 |
| `CORNUS_OBS_METRICS_INTERVAL` | `--obs-metrics-interval` | `15s` | 每个工作负载副本的采样频率，以及服务器自身指标的收集频率。间隔越短，分辨率越高，存储的数据点和后端调用也按比例增加。 |
| `CORNUS_OBS_EXPORT_ENDPOINT` | `--obs-export-endpoint` | — | 除存储外，还将收到的工作负载遥测数据转发到此上游 OTLP/HTTP 后端。它独立于 `CORNUS_OBS`: 启用存储时，cornus 会保留副本并转发；未启用存储时，它充当纯遥测网关 (无需 `imbh` 构建) 。 |
| `CORNUS_OBS_EXPORT_HEADERS` | `--obs-export-header` | — | 添加到每次转发导出的 `KEY=VALUE` 标头，例如上游的身份验证令牌。此 flag 可重复指定。 |
| `CORNUS_OBS_EXPORT_INSECURE` | `--obs-export-insecure` | off | 跳过对重新导出上游的 TLS 验证。 |

同一 `CORNUS_OTEL` / `OTEL_*` gate 也在**客户端 CLI**启用 tracing: 在运行 `cornus` 的环境设置它，每次 invocation 都会产生 root span，并向 server (再向 caretaker) 传递 W3C `traceparent`，因此 `cornus deploy` / `cornus build` / `cornus compose up` 呈现为一条端到端 trace，而不是孤立 server span。

## Client-side variable (参考)

这些由 CLI 而非 server 读取，但位于同一 `CORNUS_*` namespace。见[连接配置](/zh/reference/connection-config)和[使用远程集群](/zh/guides/remote-clusters)。

| Variable | 默认值 | 含义 |
| --- | --- | --- |
| `CORNUS_SERVER` / `CORNUS_HOST` | selected profile，随后 `http://localhost:5000` | Client command 的 remote cornus server URL。 |
| `CORNUS_TOKEN` | — | Client request 的 bearer token (覆盖 profile `token`) 。 |
| `CORNUS_TOKEN_CACHE` | `auto` | CLI 保存短期凭据 (签发的 SSH key session、交换得到的 token) 的位置: `auto` (OS keyring，不可用时回退到文件)、`keyring`、`file` 或 `none`。`none` 表示每次调用都重新签发 —— 在不能接受凭据落盘的场合设置它。文件后端位于 `$XDG_RUNTIME_DIR/cornus/tokens` (0700 目录下的 0600 文件)，由 tmpfs 支撑并在登出时清除。 |
| `CORNUS_CONFIG` | platform config path | Client [连接配置](/zh/reference/connection-config) file path。 |
| `CORNUS_CONTEXT` | config `current-context` | 要使用的 connection profile。 |
| `CORNUS_OUTPUT` | `auto` | Output rendering mode (`auto`、`plain`、`fancy`、`json`) 。见[输出模式](/zh/guides/output-modes)。 |
| `CORNUS_CONDUIT` | profile / `port-forward` | Session conduit mode (`port-forward` 或 `socks5`) 。 |
| `CORNUS_VIA_SERVER` | profile / direct | 让 workload streaming 经 server proxy。 |
| `CORNUS_BUILDER` | — | Delegated build 的 remote build endpoint。 |
| `CORNUS_REGISTRY` | server-advertised host | 不带 registry 部分的 tag 所用 registry host (remote build) 。 |
| `CORNUS_GH_BIN` | PATH 中的 `gh` | `github-cli` [凭据](/zh/guides/credentials)源所运行的 GitHub CLI 路径。token 是在持有 deploy session 的机器上签发的，因此这个变量也在那里读取。spec 中显式的 `config.command` 优先级更高。 |
