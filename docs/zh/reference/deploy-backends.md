# 部署后端

cornus 部署引擎将 [deploy spec](/zh/reference/deploy-spec)——原生 `deploy.yaml`，或由 Compose 文件 / devcontainer 转换而来——应用到**四种可互换后端**之一。它们位于同一接口之后，通过环境变量 `CORNUS_DEPLOY_BACKEND` 选择（仅环境变量，没有 CLI flag）。

| `CORNUS_DEPLOY_BACKEND` | 目标 | 网络 | 说明 |
| --- | --- | --- | --- |
| `dockerhost`（默认） | 本地 Docker daemon | Docker network | 需要 Docker socket（`/var/run/docker.sock`）。 |
| `containerd` | 裸 containerd host，无 dockerd | CNI bridge + portmap | 仅 Linux；需要 root + CNI plugin。 |
| `bare` | 直接使用 OCI runtime CLI（runc/crun/youki）——**无守护进程** | CNI bridge + portmap | 仅 Linux；需要 root + OCI runtime 二进制 + CNI plugin。镜像拉取、监督、cgroup 均由 cornus 自行拥有。 |
| `kubernetes` / `k8s` | Kubernetes 集群（client-go） | Deployment + Service | 仅 server / in-cluster；受 RBAC 限制。 |

该选择既适用于服务器（`cornus serve`），也适用于**未**指定 `--server` 的本地 [`cornus deploy`](/zh/cli/deploy)。唯一例外是仅 server/in-cluster 支持的 `kubernetes`：本地 `cornus deploy` 设置 `CORNUS_DEPLOY_BACKEND=kubernetes` 时会警告并回退到 `dockerhost`。

四个后端均支持同一核心 spec 字段（`name` / `image` / `replicas` / `restart` / `env` / `ports` / `mounts`）、客户端本地 9P bind mount、Compose user network 和已发布端口转发，因此可不变地跨后端移动同一工作流。个别字段仅映射到部分后端时，[deploy spec 参考](/zh/reference/deploy-spec)会逐字段说明。

权限处理是**默认拒绝**：除非通过 `CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES` 显式允许，否则拒绝 privileged container 和 host bind mount。参见[认证与 TLS](/zh/topics/auth-and-tls)。

## `dockerhost`（默认）

在本地 Docker daemon 上以 container 运行工作负载。它需要 Docker socket（`/var/run/docker.sock`，可由 `CORNUS_DOCKER_SOCK` 覆盖）。这是功能最完整的后端：它将最多的 spec 字段直接映射到 Docker create-time 和 host-config option；Compose user network 会成为真实的 Docker user-defined network（libnetwork 原生提供 DNS 和每网络隔离）。

在 [host-native 重新导出](/zh/reference/server-env-vars#reusing-a-local-image-store)（本后端上的默认值）下，对 daemon 已有的镜像（bare 或 loopback 主机引用），该后端会**跳过 registry 拉取**，因为拉取它会经由 cornus 的 registry 往返回到同一个 daemon；外部引用（例如 `docker.io/...`）仍会正常拉取。

## `containerd`

`CORNUS_DEPLOY_BACKEND=containerd` 在**裸 containerd host 原生**运行工作负载——无需 dockerd——并直接通过 containerd v1 client 实现完整 deploy interface。它**仅支持 Linux**（其他平台返回不支持错误），并与 `dockerhost` 一样，既可供 server 使用，也可供没有 server 的本地 `cornus deploy` 使用。

它需要：

- containerd socket（`CORNUS_CONTAINERD_ADDRESS`，默认 `/run/containerd/containerd.sock`；标准 `CONTAINERD_ADDRESS` 是 fallback）；
- **root**（创建 network namespace 并运行 CNI plugin）；
- 安装标准 CNI plugin（`bridge`、`portmap`、`host-local`、`loopback`；通过 `CORNUS_CNI_BIN_DIR`、`CNI_PATH` 或 `/opt/cni/bin` 发现）。

工作负载位于 `cornus` containerd namespace（`CORNUS_CONTAINERD_NAMESPACE`）；后端状态（volume、log、CNI config）位于 `<DataDir>/containerd/`。

- **网络**为普通 CNI bridge，通过 portmap 发布 host port。每个 compose network 从 `CORNUS_CNI_SUBNET_BASE`（默认 `10.4`）分得自己的 `/24`；已发布端口仅 DNAT 到 replica 0。container 间名称解析经 hosts-file sync（nerdctl 风格）实现。支持 UDP port mapping（Kubernetes 后端不支持）。
- **镜像拉取**自行决定 plain-HTTP 或 TLS：`localhost` 镜像仓库自动使用 plain-HTTP，`CORNUS_CONTAINERD_INSECURE_REGISTRIES`（逗号分隔的 `host[:port]`）可扩展到显式 host。`CORNUS_CONTAINERD_SNAPSHOTTER` 覆盖 rootfs snapshotter（在 docker-in-docker 等 overlay host 上设置 `native`）。
- **日志**保留于数据目录，并按 `CORNUS_CONTAINERD_LOG_MAX_BYTES`（默认 16 MiB，保留一个旧 generation）滚动，跨 cornus 重启仍存在。**Restart policy** 交由 containerd restart-monitor plugin。

将其与 containerd **build worker**（`CORNUS_BUILD_WORKER=containerd`）配对，可使 build 将 execution、snapshot 和 content 交给同一 host containerd；带 tag 的 build 会直接进入 host image store，因此新构建镜像部署时无需经镜像仓库往返。注意，containerd worker **不支持** lazy build-context path（`--lazy` / `CORNUS_LAZY_BUILD`）。

**相对 `dockerhost` 的已知缺口：**attach 仅输出，healthcheck 被忽略（有警告）。目前不测试也不支持 rootless containerd。

## `bare`

`CORNUS_DEPLOY_BACKEND=bare` 以**无守护进程**方式运行工作负载——既无 dockerd，也无 containerd。cornus 直接驱动底层 **OCI runtime CLI**（`runc`，或经 `CORNUS_BARE_RUNTIME` 使用 `crun`/`youki`/`runsc`），并自行拥有守护进程原本提供的一切：将镜像拉取至进程内 content store、layer 解包 + rootfs 组装、OCI `config.json` 生成、**进程监督 + restart policy**、cgroup 生命周期以及日志。这实际上是 **cornus 成为自己的 Podman**。它同样**仅支持 Linux**，既可供 server 使用，也可供本地 `cornus deploy` 使用。状态位于 `<DataDir>/bare/`。

它需要：

- **root**（用于 snapshotter mount、network namespace、CNI plugin 和 container cgroup）；
- `PATH` 上的 **OCI runtime 二进制**（默认 `runc`；启动时校验——缺失会以可操作的错误快速失败）；
- 安装标准 **CNI plugin**（`bridge`、`portmap`、`host-local`、`loopback`；通过 `CORNUS_CNI_BIN_DIR`、`CNI_PATH` 或 `/opt/cni/bin` 发现）。

网络、hosts-file 名称解析和 DataDir volume 的行为与 `containerd` 后端**完全一致**——daemon 无关的机制是共享代码（CNI bridge + portmap，每个 compose network 从 `CORNUS_CNI_SUBNET_BASE` 分得 `/24`，已发布端口 DNAT 到 replica 0，每实例 `/etc/hosts` sync，仅在为空时复制的 volume seeding）。此外，netns gateway 上的进程内 resolver 会回答 guest DNS（用 `CORNUS_BARE_DNS=false` 禁用）。镜像拉取自行决定 plain-HTTP 或 TLS（`localhost` 自动，`CORNUS_BARE_INSECURE_REGISTRIES` 扩展），rootfs snapshotter 为 overlay 并带 native fallback（在 overlay/docker-in-docker host 上设 `CORNUS_BARE_SNAPSHOTTER=native`）。

`bare` 独有之处在于 **cornus 就是 supervisor**。`runc create`/`start` 会立即返回，且 runc 的 `/run` state 位于 tmpfs，因此 cornus 自身经 pidfd 等待每个 container 的 PID1，施加 restart policy（`no` / `on-failure[:N]`——containerd restart-monitor 无法表达 / `always` / `unless-stopped`）并带上限退避后重启。两种 supervisor 形式共享该引擎：进程内的（默认）与可选的**每 container 独立 shim**（`CORNUS_BARE_SHIM`，cornus 的 conmon 类比），后者可在 cornus 重启后存活。启动 **reconcile** pass 在 server 重启后重新附着到存活者，并在 host 重启后完整重建工作负载（netns pin 位于 tmpfs，因此 pin 丢失即是重启信号）。每实例状态——镜像、snapshot、IP、端口、restart policy 以及期望与观测状态——持久化为 `<DataDir>/bare/records/<id>/record.json`，即替代 containerd metadata DB 的存储。

客户端本地 bind mount 默认走与其他 host 后端相同的单机 kernel-9p 快路径，`CORNUS_BARE_REMOTE=1` 则切换到 caretaker-sidecar 路径（需要 `CORNUS_AGENT_IMAGE`）——并且与 `dockerhost`/`containerd` 一样，该 companion 在 remote 模式下负责为 [`cornus port-forward`](/zh/cli/port-forward)/[`cornus tunnel`](/zh/cli/tunnel) 改路并启用 [`cornus exec --forward-agent`](/zh/cli/exec)。为与 `containerd` 对等，完整的可选接口面（`MountingBackend`、`EgressBackend`、`RemoteCapable`、volume 移除）均已实现。

**gVisor（`runsc`）。**设置 `CORNUS_BARE_RUNTIME=runsc` 会让每个工作负载在 gVisor 沙箱内运行。由于沙箱拥有 guest 的 cgroup 计量与文件系统，cornus 会自动适配两项操作（按 runtime 名称检测，可用 `CORNUS_BARE_STATS_SOURCE` 覆盖）：`cornus stats` 改为读取 runtime 自身的指标（`runsc events --stats`）而非 host cgroup 文件，`cornus cp` 则在容器**内部**运行 `tar` 而非经由 host 的 `/proc/<pid>/root`。由此带来两点注意：`cornus cp` 需要镜像内存在 `tar` 二进制（scratch/distroless 镜像无法复制），且不报告每容器的网络计数（`cornus stats` 的网络 I/O 显示为 0）。其余一切——监督、restart policy、网络、volume——均保持不变。

**相对 `dockerhost` 的已知缺口：**与 `containerd` 一样，attach 仅输出，healthcheck 被忽略（有警告）。目前不支持 rootless，且会明确报错。

## `kubernetes` / `k8s`

`CORNUS_DEPLOY_BACKEND=kubernetes`（或 `k8s`）使用 **client-go** 部署至 Kubernetes 集群，将每个工作负载呈现为一个 **Deployment** 加一个承载其已发布端口的 **Service**。它**仅适用于 server / in-cluster**：使用此后端的本地 `cornus deploy` 会警告并回退到 `dockerhost`。随附 Kubernetes manifest 和 Helm chart 预设的正是此后端。

它受 RBAC 和 namespace（`CORNUS_K8S_NAMESPACE`）限制，并且是唯一实现高级 spec block 的后端：经 network driver pipeline 的 user network（`CORNUS_K8S_NET_DRIVER`：`services`、经 Multus 的 `bridge`/`ipvlan`/`macvlan`、`cilium`）、强制 egress proxy、每 pod caretaker DNS resolver、credential brokering、客户端侧 egress relay 和工作负载到工作负载的 [hub](/zh/topics/hub) 覆盖网络。Rolling update 映射为 Deployment 的 `strategy.rollingUpdate`。

它通过 Kubernetes API 而非 CLI 运行所在机器执行部署，因此 Kubernetes 后端支撑[远程工作流](/zh/topics/remote-workflows)：开发者驱动集群内 cornus server，每端口转发或 SOCKS5 conduit 将工作负载端口带回笔记本电脑。

## 权限要求

运行工作负载的后端和进程内构建引擎的权限要求不同，并由此决定 Cornus server 的运行方式：

- **执行构建**的 Cornus 需要提权——构建引擎运行 runc + overlayfs + user namespace；单独的 registry 和 deploy subsystem 则不需要。
- `dockerhost` 需要 Docker socket；`containerd` 需要其 socket、**root** 和 CNI plugin；`bare` 需要 **root**、OCI runtime 二进制和 CNI plugin（完全不需要守护进程 socket）；`kubernetes` 在集群内的 RBAC 下运行。

```sh
# Simplest: run the container privileged (the shipped default).
#   compose: privileged: true   |   k8s: securityContext.privileged: true

# Rootless: run unprivileged with the prerequisites present, then:
cornus serve --rootless          # or CORNUS_ROOTLESS=1
```

Rootless 需要 `uidmap`（`newuidmap` / `newgidmap`）、`rootlesskit`、`slirp4netns` 以及相应 `securityContext`。镜像包含 `uidmap`。某些主机（例如设置了 `kernel.apparmor_restrict_unprivileged_userns=1` 的近期 Ubuntu）需要 AppArmor profile 或放宽 sysctl。

这与**工作负载**权限不同：无论 server 如何运行，后者均为默认拒绝；除非显式允许（`CORNUS_ALLOW_PRIVILEGED`、`CORNUS_ALLOW_BIND_SOURCES`；参见[认证与 TLS](/zh/topics/auth-and-tls)），否则拒绝 privileged container 与 host bind mount。

## 另请参阅

- [`cornus deploy`](/zh/cli/deploy)——应用 spec 的命令。
- [Deploy spec 参考](/zh/reference/deploy-spec)——每个字段及其支持后端。
- [服务器环境变量](/zh/reference/server-env-vars)——`CORNUS_DEPLOY_BACKEND` 和各后端设置。
- [远程工作流](/zh/topics/remote-workflows)——从笔记本电脑驱动 Kubernetes 后端。
