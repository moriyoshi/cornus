# 部署后端

cornus 部署引擎将 [deploy spec](/zh/reference/deploy-spec)——原生 `deploy.yaml`，或由 Compose 文件 / devcontainer 转换而来——应用到**六种可互换后端**之一。它们位于同一接口之后，通过环境变量 `CORNUS_DEPLOY_BACKEND` 选择 (仅环境变量，没有 CLI flag) 。

| `CORNUS_DEPLOY_BACKEND` | 目标 | 网络 | 说明 |
| --- | --- | --- | --- |
| `dockerhost` (默认) | 本地 Docker daemon | Docker network | 需要 Docker socket (`/var/run/docker.sock`) 。 |
| `podman` | Podman daemon，经其**原生 libpod API** | Podman network (netavark) | rootful 或 rootless 均可。没有默认 socket: 设置 `CORNUS_PODMAN_SOCKET`，或用 `CORNUS_PODMAN_SERVICE=1` 让 cornus 自己运行 `podman system service`。 |
| `containerd` | 裸 containerd host，无 dockerd | CNI bridge + portmap | 仅 Linux；需要 root + CNI plugin。 |
| `bare` | 直接使用 OCI runtime CLI (runc/crun/youki) ——**无守护进程** | CNI bridge + portmap | 仅 Linux；需要 root + OCI runtime 二进制 + CNI plugin。镜像拉取、监督、cgroup 均由 cornus 自行拥有。 |
| `incus` | [Incus](https://linuxcontainers.org/incus/) daemon (6.3+) | Incus instance 网络 + `proxy` device | 仅 Linux；将 OCI 镜像作为 Incus **应用容器**运行。daemon 主机上需要 `skopeo` + `umoci`。spec 覆盖面最窄——参见下文。 |
| `kubernetes` / `k8s` | Kubernetes 集群 (client-go) | Deployment + Service | 仅 server / in-cluster；受 RBAC 限制。 |

该选择既适用于服务器 (`cornus serve`) ，也适用于**未**指定 `--server` 的本地 [`cornus deploy`](/zh/cli/deploy)。唯一例外是仅 server/in-cluster 支持的 `kubernetes`: 本地 `cornus deploy` 设置 `CORNUS_DEPLOY_BACKEND=kubernetes` 时会警告并回退到 `dockerhost`。

四个 host 后端均支持同一核心 spec 字段 (`name` / `image` / `replicas` / `restart` / `env` / `ports`) ，其中 `dockerhost` / `containerd` / `bare` 还共享客户端本地 9P bind mount、Compose user network 和已发布端口转发，因此可不变地在这三者之间移动同一工作流。`incus` 仍是例外，但差距已比过去窄得多: 它映射核心字段、`entrypoint` 覆盖、服务器主机 bind mount、托管 volume、`sysctls`、`ulimits`、`tmpfs` 和 `shmSize`，但不支持客户端本地 9P bind mount、healthcheck、user network 以及仅有 command 的覆盖——而且它现在对每个无法映射的字段都会告警，不再静默丢弃任何东西。个别字段仅映射到部分后端时，[deploy spec 参考](/zh/reference/deploy-spec)会逐字段说明。

```mermaid
flowchart LR
    spec["deploy spec<br/>deploy.yaml · Compose 文件 · devcontainer"]
    engine["deploy 引擎<br/>单一 backend interface<br/>由 CORNUS_DEPLOY_BACKEND 选定其一"]
    spec --> engine

    engine --> dh["dockerhost<br/>(默认)"]
    engine --> pm["podman"]
    engine --> cd["containerd"]
    engine --> ba["bare"]
    engine --> ic["incus"]
    engine --> k8["kubernetes"]

    dh --> dhT["dockerd<br/>Docker user-defined network"]
    pm --> pmT["podman (libpod API)<br/>netavark network + aardvark-dns"]
    ic --> icT["incusd 6.3+<br/>instance 网络 + proxy 设备"]
    k8 --> k8T["Kubernetes API<br/>Deployment + Service"]

    subgraph shared["共享的、与守护进程无关的机制: CNI bridge + portmap、每个 compose network 一个 /24、hosts-file 同步、DataDir volume"]
        cdT["containerd<br/>负责拉取、unpack、监督"]
        baT["runc · crun · youki · runsc<br/>无守护进程 — 拉取、unpack、监督、cgroup 均由 cornus 负责"]
    end

    cd --> cdT
    ba --> baT
```

Web UI 的文件浏览器会通过后端所提供的路径访问工作负载的文件。`dockerhost` / `podman`、`containerd` 与 `bare` 通过容器 init 进程读取其根文件系统来提供**结构化文件操作** (`deploy.FSOperator`)，因此在同一个工作负载内部的重命名或复制会就地完成，而不必把每个字节送到调用方再送回来; `kubernetes` 通过 caretaker 做同样的事。它需要 root (读取 root 拥有的容器的 `/proc/<pid>/root` 是特权操作) ，并且在非本地或 rootless 的守护进程上会被拒绝，因为那里的 pid 并不指向本服务器能够访问的东西。任何拒绝都会回退到中继，因此浏览器在两种情况下都能工作，只是失去快速路径。

权限处理是**默认拒绝**: 除非通过 `CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES` 显式允许，否则拒绝 privileged container 和 host bind mount。参见[安全与认证](/zh/guides/security)。

在 host 后端 (`dockerhost`、`containerd`、`bare`) 上，工作负载*旁边*需要运行的一切——客户端本地 mount、客户端侧 egress，以及 remote 模式下下文所述的端口转发改路与 ssh-agent 中继——都由**每个副本一个 companion `cornus caretaker` container** 实现，并共享该副本的 network namespace。因此一次部署即可同时使用客户端本地 mount 和客户端侧 egress。

客户端来源的凭据在 host 后端上**完全不需要 companion**，因此也不需要 `CORNUS_ADVERTISE_URL` 或 `CORNUS_AGENT_IMAGE`，因为同机的服务器可以自行完成每一种交付。[`env` 交付](/zh/guides/credentials)在部署时解析并在创建容器时写入其环境变量; `file` 渲染到服务器自身的数据目录下并以只读方式绑定; `endpoint` 则是服务器在*工作负载 network namespace 内*绑定的监听器，它在没有原先那个边车的情况下保持了与 kubernetes 相同的安全模型 (命名空间边界即为授权) 。仅剩一处欠缺。在会把 ID 重映射进用户命名空间的运行时 (rootless 的 `podman`) 上，cornus 会向其索取映射，并以工作负载真正读取时所用的 ID 拥有凭据文件，因此非 root 的工作负载也能读取自己的凭据; 在这类运行时上数据目录会变为可遍历 (`0711` — 可穿过但不可列出) ，而其中的密钥仍为 `0600`。例外是 `incus` 上的 `file`，而且它是被拒绝而非尚未实现，原因是时序而非权限: incus 把 ID 映射记录在实例本身上，而凭据文件必须在实例存在之前写出 (它作为创建请求中的 disk 设备送达) ，且守护进程本身并不暴露可供事先查询的映射基点; 在 `incus` 上 `env` 与 `endpoint` 均可用。另外 `incus` 达成这一切时并不是 AttachingBackend: 它的 companion 是兄弟实例，无法承载挂载或出站流量，但这丝毫不妨碍服务器自行实现凭据交付。在 remote 模式下它们都会被拒绝，因为那里运行时的路径与进程 ID 都不是这台服务器能解析的。

## `dockerhost` (默认)

在本地 Docker daemon 上以 container 运行工作负载。它需要 Docker socket (`/var/run/docker.sock`，可由 `CORNUS_DOCKER_SOCK` 覆盖) 。这是功能最完整的后端: 它将最多的 spec 字段直接映射到 Docker create-time 和 host-config option；Compose user network 会成为真实的 Docker user-defined network (libnetwork 原生提供 DNS 和每网络隔离) 。

在 [host-native 重新导出](/zh/reference/server-env-vars#reusing-a-local-image-store) (本后端上的默认值) 下，对 daemon 已有的镜像 (bare 或 loopback 主机引用) ，该后端会**跳过 registry 拉取**，因为拉取它会经由 cornus 的 registry 往返回到同一个 daemon；外部引用 (例如 `docker.io/...`) 仍会正常拉取。

**客户端本地 bind mount** 通常通过在 cornus **服务器**自己的主机上直接 kernel-9p 挂载调用方的 export 来实现——这条单机快路径假定服务器与它所驱动的 Docker daemon 同机。设置 `CORNUS_DOCKER_REMOTE=1` 则改为启用 caretaker-sidecar 路径 (与 `kubernetes` 后端一直使用的机制相同) : 一个 companion `cornus caretaker` container 自己执行 kernel 9P 挂载，再由带 `rshared`/`rslave` propagation 的 Docker 托管 volume 把它中继进应用 container——因此即使服务器与 daemon 不共享文件系统 (例如 `DOCKER_HOST=tcp://...`) ，挂载也能工作。这需要把 `CORNUS_AGENT_IMAGE` 设为内嵌 cornus 的镜像，与本后端已有的 egress-companion 路径完全一致。`CORNUS_DOCKER_REMOTE` 和 `CORNUS_AGENT_IMAGE` 见[服务器环境变量](/zh/reference/server-env-vars)。

**当运行时在另一个挂载命名空间中运行 container 时** (目前是 rootless 的 `podman`) ，这条快路径还需要 cornus 挂载目标目录 (`<data-dir>/mounts`) 具备 **shared propagation**。服务器在自己的挂载命名空间中执行 kernel 9P 挂载，因此 container 位于另一个命名空间的运行时，只有在该挂载传播过去时才能看到它。否则部署会以一个 **空的** 挂载启动，且任何环节都不会报错，所以 cornus 会预先拒绝并指明修复方式。请在 **运行时启动之前** 给该目录设置 shared propagation——在主机上执行 `mount --make-rshared /`，或者当 cornus 自身被容器化时，用 `:rshared` 绑定数据目录。这个顺序无法调整: 挂载命名空间只在创建时加入 peer group，已经存在的命名空间无法事后成为 peer。

在 remote 模式下，无论部署是否使用 `--mount`，该 companion **都会按实例创建**并共享应用 container 的 network namespace——它是一个 "remote companion"，而不只是 mount 中继。这也正是 [`cornus port-forward`](/zh/cli/port-forward) 和 [`cornus tunnel`](/zh/cli/tunnel) 在 `CORNUS_DOCKER_REMOTE=1` 下还能工作的原因: 没有 companion，服务器就没有通往实例自身网络的路由来桥接二者，于是两者都改为经 companion 的共享 netns 改路，而不是直接拨号到实例。同一个 companion 还让 [`cornus exec --forward-agent`](/zh/cli/exec) 能把本地 ssh-agent 转发进任意 remote 模式实例的 exec 会话。

把两条 mount 路径并排看: 调用方的 export 抵达服务器的方式完全相同，只有最后一跳不同。

```mermaid
flowchart TB
    subgraph fast["默认 — 单机快路径"]
        direction LR
        c1["你的机器<br/>导出的目录"]
        s1["cornus 服务器<br/>在 DataDir/mounts/ 下做 kernel 9P 挂载"]
        a1["应用 container"]
        c1 -- "deploy-attach WebSocket 上的 9P" --> s1
        s1 -- "bind mount — 前提是服务器与 dockerd<br/>共享同一文件系统" --> a1
    end

    subgraph rem["CORNUS_DOCKER_REMOTE=1 — remote companion"]
        direction LR
        c2["你的机器<br/>导出的目录"]
        s2["cornus 服务器<br/>例如 DOCKER_HOST=tcp://..."]
        k2["cornus caretaker companion<br/>每实例一个，共享应用的 netns<br/>自己执行 kernel 9P 挂载"]
        a2["应用 container"]
        c2 -- "deploy-attach WebSocket 上的 9P" --> s2
        s2 -- "把 9P 中继给经 CORNUS_ADVERTISE_URL<br/>回拨上来的 companion" --> k2
        k2 -- "带 rshared / rslave propagation 的 Docker volume" --> a2
        s2 -. "port-forward · tunnel · exec --forward-agent<br/>经 companion 的共享 netns 改路" .-> k2
    end
```

## `podman`

在 Podman 上运行 workload，走的是它的**原生 libpod API** (`/v5.0.0/libpod/...`)，而不是 Podman 的 Docker 兼容端点。

这是刻意的选择。Podman 的兼容层对外声明的 Docker API 版本在近四年里一直停留在 v1.41，而它若干未修复的缺陷正好落在本后端依赖的路径上: 兼容版的容器 stats 会把 pod 内容器的 CPU 报高，兼容版 attach 会把请求体 echo 回流中，兼容版 archive `PUT` 的行为与 Docker 不一致。cornus 原样透传 stats、以裸流桥接 attach，并在该 archive 端点之上实现 `cornus cp`，因此若建立在兼容层之上，这三个缺陷都会被继承。libpod 的路由没有这些问题，而且还多给了一样兼容层给不了的东西: 按 pull 传入的 `tlsVerify`。有了它，workload 无需在 daemon 主机上修改 `registries.conf`，就能从 cornus 自带的明文 HTTP loopback registry 拉取镜像。

### 告诉 cornus 如何连接 Podman

与其他所有后端不同，podman **没有默认端点**，cornus 也不会去寻找: 不看 `CONTAINER_HOST`，不看 `DOCKER_HOST`，也不看约定的 socket 路径。下面两个变量都未设置时，服务器拒绝启动。理由是可诊断性: 一个会自行找到 daemon 的服务器，事后无法从故障报告中回答它究竟驱动了哪个 daemon。

| 变量 | 含义 |
| --- | --- |
| `CORNUS_PODMAN_SOCKET` | 就用这个端点。可以是路径、`unix://` / `tcp://` URL，或 `ssh://` 目标。 |
| `CORNUS_PODMAN_SERVICE=1` | cornus 自己在私有 socket 上运行并监督 `podman system service`。只需要 `PATH` 上有 `podman` 二进制，无需启用任何 socket unit。 |

两者同时设置属于错误，而不是某种优先级 — 究竟哪一个被忽略，正是故障处理时没人记得住的细节。

```sh
# rootless
systemctl --user enable --now podman.socket
export CORNUS_PODMAN_SOCKET="$XDG_RUNTIME_DIR/podman/podman.sock"

# rootful
sudo systemctl enable --now podman.socket
export CORNUS_PODMAN_SOCKET=/run/podman/podman.sock

# 远程，直接使用 `podman system connection` 保存的目标
export CORNUS_PODMAN_SOCKET="ssh://core@host/run/user/1000/podman/podman.sock"
```

启用 `podman.socket` 并不会把 Podman 变成守护进程: 该 unit 是 socket activation，服务按需启动、空闲即退出。`CORNUS_PODMAN_SERVICE=1` 启动的子进程行为相同，区别只在于由谁持有它的生命周期。

**已指定但无法连接**的端点不算启动错误。podman 停止只是一种可能恢复的运行时状态，服务器仍会启动并继续提供 registry；只有永远无法自行恢复的"选择器未设置"才在启动时致命。

### rootless

在 rootless podman 上，部署、日志、exec 和 `cornus cp` 都可用。做不到的是从本机直接连到 workload: rootless 容器的网络命名空间位于 `pasta` / `slirp4netns` 之后，从外部没有路由。

因此在 rootless daemon 上，[`cornus port-forward`](/zh/cli/port-forward) 和 [`cornus tunnel`](/zh/cli/tunnel) 会**立即拒绝**而不是等待超时 — 超时会被读成"workload 挂了"，把人引向错误的方向。设置 `CORNUS_PODMAN_REMOTE=1`，即可通过与 workload 共享命名空间的 per-instance companion 到达它；与其他后端的 remote 模式一样，它还需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`。**rootful** podman 没有这些限制。

cornus 是向 daemon 询问它是否为 rootless (`host.security.rootless`)，而不是从 socket 路径猜测 — 因为 rootful socket 可以被 bind mount 到任何位置。

### 主机原生重新导出

在本后端默认启用的[主机原生重新导出](/zh/reference/server-env-vars#reusing-a-local-image-store)下，`/v2/*` registry 提供的是 **podman 的**镜像存储 — 由部署后端决定，而不是由 `DOCKER_HOST` 决定。如果 podman 服务器的 registry 重新导出了 Docker daemon 的镜像，它提供的就是一个自己从不部署到的 runtime 的镜像: 这是错误而不是故障，因此不会有任何东西报告它。

## `containerd`

`CORNUS_DEPLOY_BACKEND=containerd` 在**裸 containerd host 原生**运行工作负载——无需 dockerd——并直接通过 containerd v1 client 实现完整 deploy interface。它**仅支持 Linux** (其他平台返回不支持错误) ，并与 `dockerhost` 一样，既可供 server 使用，也可供没有 server 的本地 `cornus deploy` 使用。

它需要:

- containerd socket (`CORNUS_CONTAINERD_ADDRESS`，默认 `/run/containerd/containerd.sock`；标准 `CONTAINERD_ADDRESS` 是 fallback) ；
- **root** (创建 network namespace 并运行 CNI plugin) ；
- 安装标准 CNI plugin (`bridge`、`portmap`、`host-local`、`loopback`；通过 `CORNUS_CNI_BIN_DIR`、`CNI_PATH` 或 `/opt/cni/bin` 发现) 。

工作负载位于 `cornus` containerd namespace (`CORNUS_CONTAINERD_NAMESPACE`) ；后端状态 (volume、log、CNI config) 位于 `<DataDir>/containerd/`。

- **网络**为普通 CNI bridge，通过 portmap 发布 host port。每个 compose network 从 `CORNUS_CNI_SUBNET_BASE` (默认 `10.4`) 分得自己的 `/24`；已发布端口仅 DNAT 到 replica 0。container 间名称解析经 hosts-file sync (nerdctl 风格) 实现。支持 UDP port mapping (Kubernetes 后端不支持) 。
- **镜像拉取**自行决定 plain-HTTP 或 TLS: `localhost` 镜像仓库自动使用 plain-HTTP，`CORNUS_CONTAINERD_INSECURE_REGISTRIES` (逗号分隔的 `host[:port]`) 可扩展到显式 host。`CORNUS_CONTAINERD_SNAPSHOTTER` 覆盖 rootfs snapshotter (在 docker-in-docker 等 overlay host 上设置 `native`) 。
- **日志**保留于数据目录，并按 `CORNUS_CONTAINERD_LOG_MAX_BYTES` (默认 16 MiB，保留一个旧 generation) 滚动，跨 cornus 重启仍存在。**Restart policy** 交由 containerd restart-monitor plugin。

```mermaid
flowchart TB
    reg[("cornus registry /v2/*")]
    server["cornus 服务器 — deploy 引擎，需要 root"]
    server -- "拉取 — 由 cornus 自己的 resolver 决定 plain HTTP 还是 TLS<br/>(localhost、CORNUS_CONTAINERD_INSECURE_REGISTRIES)" --> reg

    subgraph ctrd["containerd · namespace CORNUS_CONTAINERD_NAMESPACE (默认 cornus)"]
        img["content + snapshot<br/>CORNUS_CONTAINERD_SNAPSHOTTER"]
        t0["task · replica 0"]
        tn["task · replica 1..N"]
        rmon["restart-monitor plugin<br/>负责 restart policy"]
    end

    subgraph host["由 cornus 自己驱动 — 与 bare 后端共享的代码"]
        cni["CNI bridge + portmap<br/>从 CORNUS_CNI_SUBNET_BASE 为每个 compose network 分配 /24"]
        hosts["每实例 /etc/hosts 同步<br/>nerdctl 风格的名称解析"]
        state["DataDir/containerd/ — volume · CNI config · 日志<br/>日志经 binary:// shim 写入，跨 cornus 重启仍存在"]
    end

    server -- "经 CORNUS_CONTAINERD_ADDRESS 的 containerd v1 client" --> ctrd
    server --> host
    img --> t0
    img --> tn
    cni -- "已发布端口仅 DNAT 到 replica 0" --> t0
    t0 --> state
    tn --> state
```

将其与 containerd **build worker** (`CORNUS_BUILD_WORKER=containerd`) 配对，可使 build 将 execution、snapshot 和 content 交给同一 host containerd；带 tag 的 build 会直接进入 host image store，因此新构建镜像部署时无需经镜像仓库往返。注意，containerd worker **不支持** lazy build-context path (`--lazy` / `CORNUS_LAZY_BUILD`) 。

**客户端本地 bind mount 需要 `CORNUS_CONTAINERD_REMOTE=1`。** 与 `dockerhost`、`bare` 不同，本后端没有单机 kernel-9p 快路径 (服务器侧的那条路径只属于那两个后端) ，因此在未设置该 flag 时，带 `--mount` 的 deploy 会被提前拒绝，错误信息会点明这个变量。设置该 flag 后，挂载由与 `dockerhost` remote 模式相同的 caretaker-sidecar 机制实现 (由 companion `cornus caretaker` container/task 执行 kernel 9P 挂载，再经带 `rshared`/`rslave` OCI mount option 的共享主机目录传播进应用 container) ，同时需要 `CORNUS_AGENT_IMAGE`。与 `dockerhost` 不同，该 flag **不会**带来真正的远程 daemon 支持: containerd 的 client dialer 只连接本地 unix socket，因此无论该 flag 如何设置，本后端都无条件与 cornus 服务器同机——sidecar 机制本身仍然值得拥有 (它免去服务器自身需要 kernel 挂载权限，也是后续功能可以复用的基础) ，但它并不是通往非同机 containerd 主机的路径。

与 `dockerhost` 一样，`CORNUS_CONTAINERD_REMOTE=1` 无论是否使用 `--mount` 都会按实例创建该 companion (加入应用已固定的 network namespace) ，原因也相同: 正是它为 [`cornus port-forward`](/zh/cli/port-forward)/[`cornus tunnel`](/zh/cli/tunnel) 改路，并在 `ForwardPort` 的常规直连 IP 拨号介入时启用 [`cornus exec --forward-agent`](/zh/cli/exec)。这里它只是免去服务器直接拨入 CNI bridge 网络所需的路由或权限，与上面 (尚未解决的) 真正远程 daemon 的问题是两回事。

**相对 `dockerhost` 的已知缺口:** attach 仅输出，healthcheck 被忽略 (有警告) 。目前不测试也不支持 rootless containerd。

## `bare`

`CORNUS_DEPLOY_BACKEND=bare` 以**无守护进程**方式运行工作负载——既无 dockerd，也无 containerd。cornus 直接驱动底层 **OCI runtime CLI** (`runc`，或经 `CORNUS_BARE_RUNTIME` 使用 `crun`/`youki`/`runsc`) ，并自行拥有守护进程原本提供的一切: 将镜像拉取至进程内 content store、layer 解包 + rootfs 组装、OCI `config.json` 生成、**进程监督 + restart policy**、cgroup 生命周期以及日志。这实际上是 **cornus 成为自己的 Podman**。它同样**仅支持 Linux**，既可供 server 使用，也可供本地 `cornus deploy` 使用。状态位于 `<DataDir>/bare/`。

它需要:

- **root** (用于 snapshotter mount、network namespace、CNI plugin 和 container cgroup) ；
- `PATH` 上的 **OCI runtime 二进制** (默认 `runc`；启动时校验——缺失会以可操作的错误快速失败) ；
- 安装标准 **CNI plugin** (`bridge`、`portmap`、`host-local`、`loopback`；通过 `CORNUS_CNI_BIN_DIR`、`CNI_PATH` 或 `/opt/cni/bin` 发现) 。

网络、hosts-file 名称解析和 DataDir volume 的行为与 `containerd` 后端**完全一致**——daemon 无关的机制是共享代码 (CNI bridge + portmap，每个 compose network 从 `CORNUS_CNI_SUBNET_BASE` 分得 `/24`，已发布端口 DNAT 到 replica 0，每实例 `/etc/hosts` sync，仅在为空时复制的 volume seeding) 。此外，netns gateway 上的进程内 resolver 会回答 guest DNS (用 `CORNUS_BARE_DNS=false` 禁用) 。镜像拉取自行决定 plain-HTTP 或 TLS (`localhost` 自动，`CORNUS_BARE_INSECURE_REGISTRIES` 扩展) ，rootfs snapshotter 为 overlay 并带 native fallback (在 overlay/docker-in-docker host 上设 `CORNUS_BARE_SNAPSHOTTER=native`) 。

`bare` 独有之处在于 **cornus 就是 supervisor**。`runc create`/`start` 会立即返回，且 runc 的 `/run` state 位于 tmpfs，因此 cornus 自身经 pidfd 等待每个 container 的 PID1，施加 restart policy (`no` / `on-failure[:N]`——containerd restart-monitor 无法表达 / `always` / `unless-stopped`) 并带上限退避后重启。两种 supervisor 形式共享该引擎: 进程内的 (默认) 与可选的**每 container 独立 shim** (`CORNUS_BARE_SHIM`，cornus 的 conmon 类比) ，后者可在 cornus 重启后存活。启动 **reconcile** pass 在 server 重启后重新附着到存活者，并在 host 重启后完整重建工作负载 (netns pin 位于 tmpfs，因此 pin 丢失即是重启信号) 。每实例状态——镜像、snapshot、IP、端口、restart policy 以及期望与观测状态——持久化为 `<DataDir>/bare/records/<id>/record.json`，即替代 containerd metadata DB 的存储。

```mermaid
flowchart TB
    apply["Apply(spec)"] --> create["runc create + start"]
    create --> ret["立即返回<br/>runc 的 /run state 位于 tmpfs"]
    ret --> sup["cornus supervisor 经 pidfd 等待 container 的 PID 1<br/>默认进程内，或用 CORNUS_BARE_SHIM 启用独立 shim"]
    sup --> exit["PID 1 退出"]
    exit --> pol{"restart policy"}
    pol -- "no" --> stop["保持停止"]
    pol -- "on-failure:N · always · unless-stopped" --> back["带上限的退避"]
    back --> create

    boot["服务器重启"] --> rec["从 DataDir/bare/records/id/record.json reconcile"]
    rec --> pin{"netns pin 是否仍在 tmpfs 上"}
    pin -- "在 — 存活者" --> sup
    pin -- "不在 — 主机已重启" --> create
```

客户端本地 bind mount 默认走与其他 host 后端相同的单机 kernel-9p 快路径，`CORNUS_BARE_REMOTE=1` 则切换到 caretaker-sidecar 路径 (需要 `CORNUS_AGENT_IMAGE`) 。与 `dockerhost`/`containerd` 不同，该 companion **只负责 mount**，且仅在部署确实声明了客户端本地 mount 时才存在: [`cornus port-forward`](/zh/cli/port-forward)/[`cornus tunnel`](/zh/cli/tunnel) 直接拨号到实例自身的 IP (在这里是正确的——无 daemon 的后端始终与服务器同机) ，而 [`cornus exec --forward-agent`](/zh/cli/exec) 不可用，会被预先拒绝。为与 `containerd` 对等，完整的可选接口面 (`MountingBackend`、`EgressBackend`、`RemoteCapable`、volume 移除) 均已实现。

**gVisor (`runsc`) 。**设置 `CORNUS_BARE_RUNTIME=runsc` 会让每个工作负载在 gVisor 沙箱内运行。由于沙箱拥有 guest 的 cgroup 计量与文件系统，cornus 会自动适配两项操作 (按 runtime 名称检测，可用 `CORNUS_BARE_STATS_SOURCE` 覆盖) : `cornus stats` 改为读取 runtime 自身的指标 (`runsc events --stats`) 而非 host cgroup 文件，`cornus cp` 则在容器**内部**运行 `tar` 而非经由 host 的 `/proc/<pid>/root`。由此带来两点注意: `cornus cp` 需要镜像内存在 `tar` 二进制 (scratch/distroless 镜像无法复制) ，且不报告每容器的网络计数 (`cornus stats` 的网络 I/O 显示为 0) 。其余一切——监督、restart policy、网络、volume——均保持不变。

**相对 `dockerhost` 的已知缺口:** 与 `containerd` 一样，attach 仅输出，healthcheck 被忽略 (有警告) 。目前不支持 rootless，且会明确报错。

## `incus`

`CORNUS_DEPLOY_BACKEND=incus` 将工作负载部署为 **[Incus](https://linuxcontainers.org/incus/) 应用容器**，通过官方 Go client 与 Incus daemon 的 REST API (本地 unix socket) 通信。Incus 6.3+ 可以直接把 OCI 镜像作为应用容器运行，这正是 cornus 所针对的能力——与你在其他后端上运行的 OCI 镜像完全相同，只是由 incusd 而非 dockerd、containerd 或 cornus 自身来监督。它**仅支持 Linux** (其他平台返回不支持错误) ，并与其他 host 后端一样，既可供 server 使用，也可供没有 server 的本地 `cornus deploy` 使用。

它需要:

- incus daemon socket (`CORNUS_INCUS_SOCKET`，默认 `/var/lib/incus/unix.socket`) 及其访问权限，
- **Incus 6.3 或更高版本**——更早的版本没有 OCI 支持，部署会以 `Unsupported protocol: oci` 失败，以及
- **daemon 主机上的 `skopeo` 和 `umoci`**: incusd 自身会调用它们来展平 OCI 镜像。它们需要安装在 *incusd* 运行的主机上，而非 cornus 运行的主机上。

instance 在 `CORNUS_INCUS_PROJECT` (默认 `default`) 选定的 project 中创建，命名为 `cornus-<app>-<replica>`。

请注意拉取箭头的起点——这是唯一一个不自己获取镜像的后端。

```mermaid
flowchart TB
    server["cornus 服务器 — deploy 引擎"]
    reg[("cornus registry /v2/*")]

    subgraph dhost["incus daemon 主机"]
        incusd["incusd 6.3+"]
        tools["skopeo + umoci — flatten OCI 镜像<br/>需要装在这台主机上，而不是 cornus 运行的地方"]
        prox["host 侧绑定的 proxy 设备<br/>仅 replica 0 · TCP 与 UDP"]
        subgraph inst["instance cornus-app-replica"]
            pid1["OCI 镜像自身的 PID 1"]
            cfg["user.cornus.* · Compose labels · environment.*<br/>limits.cpu.allowance · limits.memory<br/>security.privileged · boot.autorestart"]
        end
    end

    server -- "经 CORNUS_INCUS_SOCKET 的 REST<br/>InstanceSource Protocol: oci" --> incusd
    incusd --> tools
    tools -- "拉取 — 要让 skopeo 接受 plain-HTTP registry，<br/>daemon 主机需要自己的 registries.conf.d 条目" --> reg
    incusd --> inst
    incusd --> prox
    prox --> pid1
    pid1 -- "console 日志: 单一 raw PTY 流，无时间戳，<br/>不区分 stdout/stderr" --> server
```

- **执行镜像拉取的是 incusd，而不是 cornus。** cornus 把指向自身注册表的 OCI remote (`InstanceSource{Protocol: "oci"}`) 交给 daemon，由 incusd 通过 skopeo 拉取。由于 skopeo 默认使用 HTTPS，明文 HTTP 的注册表需要声明为 insecure: `CORNUS_INCUS_INSECURE_REGISTRIES` (逗号 / 空格分隔的 `host[:port]`) 让 cornus 以 `http://` 访问这些主机，而 **daemon 主机**还需要一条对应的 `/etc/containers/registries.conf.d/` 条目，好让 skopeo 也认可。环回地址的注册表在 cornus 一侧会自动按明文 HTTP 处理。
- **身份与元数据**存放在 Incus 的 `user.*` 配置命名空间中，这是唯一允许任意键的位置: `user.cornus.managed`、`user.cornus.app`、表示来源的 `user.cornus.origin.*` 一组键，以及所有 Compose `labels:`。环境变量写入 `environment.*`，CPU / 内存上限写入 `limits.cpu.allowance` / `limits.memory`，`privileged: true` (与别处一样受策略约束) 写入 `security.privileged`。
- **Apply 会重建。** Incus 拒绝删除运行中的 instance，因此应用 spec 时会先停止并删除该应用的现有 instance，再以 `Start: true` 创建新的 instance。
- **已发布端口**成为在 host 侧 bind 的 Incus `proxy` device，并按跨后端约定仅附加到 **replica 0**。TCP 和 UDP 映射均受支持。
- **restart policy** 映射到布尔值 `boot.autorestart`。除 `no` 之外的取值都会启用它；Incus 没有重试次数上限，因此 `restart: on-failure:N` 无法表达其中的 `N` (与 `containerd` 的限制相同) 。
- **日志**是 instance 的**控制台**日志——OCI PID 1 的 stdout/stderr 合并为单条原始 PTY 流，cornus 会将其重新 framing 为通常的 stdout 流。该来源中不存在逐行时间戳，也没有 stdout/stderr 分离，因此 `--since` / `--until` / `--follow` / `--tail` / `--timestamps` 都无法支持；它们会各自单独告警，而不是被静默忽略 (格式错误的 `--since` 仍然是错误) 。
- **`cornus stats`** 能准确报告内存、pids 和网络，但 Incus 不公开主机级 CPU 总量，因此据此推导的 **CPU 百分比会偏低或为零**。
- **`cornus cp`** 基于 Incus 的 instance file API。该 API 既不携带文件大小，也不携带符号链接目标，因此 cornus 需要读完内容来测量大小，并把链接按内容读取——结果正确，但并非廉价的 stat。
- **[`cornus port-forward`](/zh/cli/port-forward) 和 [`cornus tunnel`](/zh/cli/tunnel)** 直接连接 instance 自身可路由的 IPv4 (取自其 instance state) ，TCP 和 UDP 均可，不涉及任何 companion。在 remote 模式 (见下文) 下，这种直接连接恰恰是靠不住的，因此流量会改为经该 replica 的 companion 转发。
- **[`cornus exec`](/zh/cli/exec)** 受支持，包括 TTY 尺寸设置。**attach 是刻意不支持的**: Incus 公开的是连接到 PID 1 的控制台，而非 docker-attach 的流语义，因此 `cornus attach` 会返回明确的错误并引导你使用 exec。

**与 `dockerhost` 相比的已知差距。** 除上述日志和 stats 的注意事项外，incus 后端不映射: **仅有 command** 的覆盖、`healthcheck`、客户端本地 9P bind mount、Compose user `networks` 以及 `knative`。`healthcheck` 是永久性的差距而非待办: Incus 没有任何 instance 级别的探针，因此一个未退出却已不健康的工作负载会一直被报告为 running。

**没有任何东西被静默丢弃。** 其余每个 spec 字段要么被映射，要么产生一条指明字段名的告警；而只使用受支持功能的 spec **完全不会产生告警**——这两半都由测试固定。之所以值得写明，是因为直到不久前它还只是愿景: 该后端曾对九个字段告警，却对另外约二十个 (`hostname`、`stopSignal`、`capAdd` / `capDrop`、`devices`、`init`、`tty`、DNS 设置等) 一言不发地丢弃。现在没看到告警，就意味着该字段被遵守了。

**有三个字段是有条件映射的**，而条件本身才是要点:

- **`entrypoint`** 会变成 instance 的 `oci.entrypoint`，该键替换镜像的整个 argv，并由 `command` 提供其参数——与 `entrypoint` 在其他后端上的语义相同。无法表达的是**仅有 command** 的覆盖: 没有 `entrypoint:` 的 `command:` 意思是“保留镜像的 `ENTRYPOINT`，只替换它的参数”，而 `oci.entrypoint` 只能整体替换 argv；后端也根本看不到为此所需的切分，因为镜像是 incus 自己拉取的，而它暴露的 argv (镜像配置中被展平的 `Process.Args`) 早已丢失了 `ENTRYPOINT` 与 `CMD` 的边界。这种情形会告警，并指明变通做法: 把 `entrypoint` 也设上。
- **`workingDir`** 在为**绝对路径**时映射为 `oci.cwd`。相对路径会告警: `oci.cwd` 按绝对路径校验，incusd 会拒绝该 create；而相对路径所依据的镜像自身工作目录，在这里同样看不到。
- **`user`** 在为**数值**时映射: `1000` 或 `1000:1000` 变成 `oci.uid` / `oci.gid` (只给 uid 时组仍由镜像决定) 。用户名或组名形式 (`app`、`1000:staff`) 会告警，因为解析名字需要镜像的 `/etc/passwd`，而该后端从不曾看到它——这与 kubernetes 后端的纯数值限制相同。`1000:staff` 会被**整体**拒绝，而不是遵守 uid 却丢掉组，否则进程就会跑在没人要求过的组里。

**Mount 与 volume。** 服务器主机的 bind 路径会变成 incus `disk` device，并由与其他 host 后端相同的默认拒绝 `hostpolicy` (`CORNUS_ALLOW_BIND_SOURCES`) 把关——允许列表之外的 source 仍然会让部署失败，而不是被挂载。客户端本地 9P bind mount 仍不受支持，服务器会在它们到达后端之前就拒绝。incus disk device 无法表达的内容会逐条 mount 告警: source 为空或为相对路径、target 为根 (`/`) ，以及 SELinux relabel 请求。托管 `volumes` 会在所配置的 pool 中以 custom storage volume 形式 provision；匿名 volume 在其部署消失时被回收，命名 volume 由 `compose down --volumes` 移除。

**同样被映射的** (每一类都会对 Incus 不接受的条目逐条告警) : `sysctls` (映射为 `linux.sysctl.*`，但不含 incusd 为 OCI 容器自行设置的那两个) 、`ulimits` (映射为 `limits.kernel.*`，仅限 Incus 文档化的 rlimit 名称，且 soft 上限不超过 hard 时) ，以及 `tmpfs` 和 `shmSize` (都是 tmpfs disk device，其中 `size` 是 Incus 唯一能表达的挂载选项) 。

**Remote 模式。** `CORNUS_INCUS_REMOTE` 打开 caretaker companion 路径，与 `CORNUS_DOCKER_REMOTE` 等对各自后端所做的一样。每个 replica 都会获得一个运行 `cornus caretaker`、带 PortForward 和 AgentRelay role 的 companion instance，这正是让 [`cornus port-forward`](/zh/cli/port-forward) / [`cornus tunnel`](/zh/cli/tunnel) 能触及服务器无路可达的工作负载、并让 [`cornus exec --forward-agent`](/zh/cli/exec) 可用的原因。不打开它时，该后端会声明 agent 转发不可用，`--forward-agent` 会被预先拒绝。Remote 模式需要 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL` (companion 镜像，以及 companion 回拨的 cornus URL) ；缺少其中之一时，部署会在拆除任何东西之前就失败，而不是进行到一半才失败。

incus 的 companion 有两点不同于其他所有 host 后端，且都是 Incus 强加而非主动选择的。其一，它是**兄弟 instance** 而不是共享 netns 的 sidecar，因为 Incus 没有公开任何让一个 instance 加入另一个 instance 网络命名空间的手段——所以 caretaker 是按地址而不是经 loopback 连接 app instance。其二，被转发的 agent socket 承载在**共享的 custom storage volume** 上 (以 `security.shifted` 创建，好让两个 id map 不同的非特权 instance 都能挂载) ，而不是从服务器数据目录做 bind——bind 会假定服务器能看到守护进程主机的文件系统。

Remote 模式**并不会**带来其他后端从 companion 获得的客户端本地 mount，而且这个差距是结构性的，而非只是尚未构建: 9P mount 必须传播进 app 的 mount 命名空间，兄弟 instance 做不到这一点——那需要一个运行在 app instance 内部的 caretaker。客户端侧 egress 未接线也是同样的原因。

以上所有内容都有一点需要说明: companion 路径从未针对真实的 `incusd` 运行过。它是基于 vendor 的 Incus v6 API 构建的，因此请将其视为在 remote 模式下受支持，而不是已在实际环境中得到验证。

## `kubernetes` / `k8s`

`CORNUS_DEPLOY_BACKEND=kubernetes` (或 `k8s`) 使用 **client-go** 部署至 Kubernetes 集群，将每个工作负载呈现为一个 **Deployment** 加一个承载其已发布端口的 **Service**。它**仅适用于 server / in-cluster**: 使用此后端的本地 `cornus deploy` 会警告并回退到 `dockerhost`。随附 Kubernetes manifest 和 Helm chart 预设的正是此后端。

它受 RBAC 和 namespace (`CORNUS_K8S_NAMESPACE`) 限制，并且是唯一实现高级 spec block 的后端: 经 network driver pipeline 的 user network (`CORNUS_K8S_NET_DRIVER`: `services`、经 Multus 的 `bridge`/`ipvlan`/`macvlan`、`cilium`) 、强制 egress proxy、每 pod caretaker DNS resolver、credential brokering、客户端侧 egress relay 和工作负载到工作负载的 [hub](/zh/guides/hub) 覆盖网络。Rolling update 映射为 Deployment 的 `strategy.rollingUpdate`。

它通过 Kubernetes API 而非 CLI 运行所在机器执行部署，因此 Kubernetes 后端支撑[使用远程集群](/zh/guides/remote-clusters): 开发者驱动集群内 cornus server，每端口转发或 SOCKS5 conduit 将工作负载端口带回笔记本电脑。

`ForwardPort` (因而 [`cornus port-forward`](/zh/cli/port-forward)/[`cornus tunnel`](/zh/cli/tunnel)) 在这里完全不需要 companion sidecar——它直接借助 Kubernetes API 自身的 `pods/portforward` 子资源。[`cornus exec --forward-agent`](/zh/cli/exec) 同样受支持，但与 host 后端那种作用于整个后端的 remote 模式不同，它是**按部署启用**的: 在 [DeploySpec](/zh/reference/deploy-spec) 中设置 `agentForward`，即可把一个 `AgentRelayRole` 折入该 pod 的 caretaker (若该 pod 没有其他 caretaker role，则创建一个最小的) 。未设置该字段就应用的部署会以明确的错误拒绝 `--forward-agent`。

```mermaid
flowchart LR
    spec["deploy spec"] --> be["kubernetes 后端<br/>client-go，namespace 为 CORNUS_K8S_NAMESPACE"]
    be --> dep["Deployment<br/>replicas · strategy.rollingUpdate"]
    be --> svc["Service<br/>已发布端口"]
    dep --> pod["Pod"]
    svc --> pod
    pod --> app["应用 container"]
    pod -. "仅当 spec 需要时" .-> ct["caretaker sidecar<br/>mount · egress proxy · DNS · credential · hub<br/>设置 agentForward 时还有 AgentRelayRole"]
    cli["cornus port-forward<br/>cornus tunnel"] -- "pods/portforward 子资源 —<br/>这里不需要 companion sidecar" --> pod
```

## 权限要求

运行工作负载的后端和进程内构建引擎的权限要求不同，并由此决定 Cornus server 的运行方式:

- **执行构建**的 Cornus 需要提权——构建引擎运行 runc + overlayfs + user namespace；单独的 registry 和 deploy subsystem 则不需要。
- `dockerhost` 需要 Docker socket；`containerd` 需要其 socket、**root** 和 CNI plugin；`bare` 需要 **root**、OCI runtime 二进制和 CNI plugin (完全不需要守护进程 socket) ；`incus` 需要访问 incus daemon socket (以及 daemon 主机上的 `skopeo`/`umoci`) ，工作负载自身的权限交由 incusd 处理；`kubernetes` 在集群内的 RBAC 下运行。

```sh
# Simplest: run the container privileged (the shipped default).
#   compose: privileged: true   |   k8s: securityContext.privileged: true

# Rootless: run unprivileged with the prerequisites present, then:
cornus serve --rootless          # or CORNUS_ROOTLESS=1
```

Rootless 需要 `uidmap` (`newuidmap` / `newgidmap`) 、`rootlesskit`、`slirp4netns` 以及相应 `securityContext`。镜像包含 `uidmap`。某些主机 (例如设置了 `kernel.apparmor_restrict_unprivileged_userns=1` 的近期 Ubuntu) 需要 AppArmor profile 或放宽 sysctl。

这与**工作负载**权限不同: 无论 server 如何运行，后者均为默认拒绝；除非显式允许 (`CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES`；参见[安全与认证](/zh/guides/security)) ，否则拒绝 privileged container 与 host bind mount。

## 另请参阅

- [`cornus deploy`](/zh/cli/deploy)——应用 spec 的命令。
- [Deploy spec 参考](/zh/reference/deploy-spec)——每个字段及其支持后端。
- [服务器环境变量](/zh/reference/server-env-vars)——`CORNUS_DEPLOY_BACKEND` 和各后端设置。
- [使用远程集群](/zh/guides/remote-clusters)——从笔记本电脑驱动 Kubernetes 后端。
