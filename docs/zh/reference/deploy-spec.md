# 部署规范参考

**部署规范**是对 cornus 所运行工作负载的声明式描述。它是传给 [`cornus deploy -f`](/zh/cli/deploy) 的 YAML (或 JSON) 文档。它以*命令式*方式应用: 输入一份规范后，所选的[部署后端](/zh/reference/deploy-backends)会使实际状态收敛至该规范 (创建或重新创建工作负载)。

Compose 文件或 devcontainer 会在内部转换为相同的规范，因此这里的每个字段也都可通过 [`cornus compose`](/zh/cli/compose) 使用。四个后端: `dockerhost` (默认)、`containerd`、`bare` 和 `kubernetes`，都位于同一接口之后并遵循相同规范，但并非每个字段都能映射到每个后端。当源码记录了每后端行为时，会在字段描述中说明。

规范的权威源码是 [`pkg/.cornus/v1/deploy.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/.cornus/v1/deploy.go)。

## 示例

一份较完整的规范，展示常用字段以及一些嵌套块:

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

## 顶层字段 (`DeploySpec`)

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 唯一标识部署；受管理资源会带有该标签，以实现幂等应用/删除。 |
| `image` | string | 是 | — | 要运行的镜像引用，最好固定 digest。 |
| `command` | []string | 否 | 镜像 `CMD` | 覆盖镜像的默认命令 (Docker `CMD`): 即传给仍然生效的镜像 `ENTRYPOINT` 的参数。在 kubernetes 上它会放入容器的 `Args`，从而保留镜像 entrypoint。 |
| `entrypoint` | []string | 否 | 镜像 `ENTRYPOINT` | 覆盖镜像 entrypoint (Docker `ENTRYPOINT` / Kubernetes 容器 `command`)。设置后，`command` 提供其参数；空值保留镜像默认值。 |
| `env` | map[string]string | 否 | — | 环境变量，以映射中的 `KEY=VALUE` 形式应用。 |
| `ports` | [][PortMapping](#portmapping) | 否 | — | 将主机端口映射至容器端口。 |
| `mounts` | [][Mount](#mount) | 否 | — | 将主机路径绑定到容器内。 |
| `volumes` | [][VolumeSpec](#volumespec) | 否 | — | 后端为其配置存储的受管 (非绑定) 卷。 |
| `networks` | [][NetworkAttachment](#networkattachment) | 否 | — | 此工作负载加入的用户定义网络 (Compose `networks:`)。空值表示仅默认连通性。 |
| `proxy` | [ProxySpec](#proxyspec) | 否 | — | 请求一个执行出站策略的用户空间代理。**仅 kubernetes** (dockerhost 从 libnetwork 获得隔离并忽略它)。 |
| `dns` | [DNSSpec](#dnsspec) | 否 | — | 请求每 Pod 的 caretaker DNS 解析器。**仅 kubernetes。** |
| `hub` | [HubSpec](#hubspec) | 否 | — | 将工作负载加入服务器的工作负载到工作负载覆盖网络。**仅 kubernetes。** 请参阅[工作负载 Hub](/zh/guides/hub)。 |
| `docker` | [DockerSpec](#dockerspec) | 否 | — | 向工作负载暴露 Docker Engine API 端点。**仅 kubernetes。** 要求服务器上有 `CORNUS_CLIENT_TOKEN_SECRET`。 |
| `credentials` | [CredentialSpec](#credentialspec) | 否 | — | 将由客户端签发的短期凭据代理至工作负载。在 **kubernetes** 上实现；主机后端通过配套 caretaker；其他后端会警告并忽略。请参阅[凭据](/zh/guides/credentials)。 |
| `restart` | string | 否 | `unless-stopped` | 重启策略: `no`、`always`、`on-failure` 或 `unless-stopped`。 |
| `restartMaxAttempts` | int | 否 | `0` (后端默认值，无限制) | 限制 `on-failure` 策略的重启次数。**仅 dockerhost** (kubernetes 和 containerd 无法限制次数，会忽略它)。 |
| `replicas` | int | 否 | 后端默认值 | 所需实例数。所有后端均支持；在主机后端中，已发布的主机端口只会指向副本 0。 |
| `privileged` | bool | 否 | `false` | 以完全特权运行 (Docker `--privileged` / Kubernetes `securityContext.privileged`)。需显式启用；默认拒绝姿态请参阅[安全与认证](/zh/guides/security)。 |
| `healthcheck` | [Healthcheck](#healthcheck) | 否 | — | 容器健康检查。 |
| `resources` | [Resources](#resources) | 否 | — | CPU/内存限制和预留。 |
| `updateConfig` | [UpdateConfig](#updateconfig) | 否 | — | 滚动更新策略。**仅 kubernetes** (主机后端会重新创建单个实例并忽略它)。 |
| `user` | string | 否 | 镜像默认值 | 进程运行时使用的用户 (及可选组): `uid`、`uid:gid`、`user` 或 `user:group`。kubernetes 仅映射**数值型** `uid[:gid]`，无法表示用户名。 |
| `workingDir` | string | 否 | 镜像默认值 | 容器工作目录 (compose `working_dir`)。 |
| `hostname` | string | 否 | 后端默认值 | 容器主机名 (compose `hostname`)。 |
| `labels` | map[string]string | 否 | — | 用户元数据。在 kubernetes 上它们会成为 Pod 模板的**注解** (而非标签)；cornus 自己的管理标签在键冲突时始终优先。 |
| `stopSignal` | string | 否 | 镜像默认值 | 用于停止主进程的信号，例如 `SIGTERM`。仅 dockerhost；kubernetes 和 containerd 忽略。 |
| `stopGracePeriod` | string | 否 | 后端默认值 | 发送停止信号后、强制终止前的等待时间，采用 Go duration 格式 (`10s`、`1m30s`)。containerd 忽略。 |
| `init` | bool (nullable) | 否 | 后端默认值 | `true` 请求 / `false` 拒绝由 PID 1 init 回收僵尸进程 (compose `init`)。仅 dockerhost；kubernetes 和 containerd 忽略。 |
| `tty` | bool | 否 | `false` | 分配伪 TTY (compose `tty`)。 |
| `stdinOpen` | bool | 否 | `false` | 保持容器的 stdin 打开 (compose `stdin_open`)。containerd 忽略。 |
| `readOnly` | bool | 否 | `false` | 将根文件系统以只读方式挂载 (compose `read_only`)。 |
| `capAdd` | []string | 否 | — | 添加 Linux capability (compose `cap_add`)。 |
| `capDrop` | []string | 否 | — | 移除 Linux capability (compose `cap_drop`)。 |
| `securityOpt` | []string | 否 | — | 安全选项 (compose `security_opt`)。dockerhost 原样传递；kubernetes/containerd 只映射已知选项 (`no-new-privileges`、`label=`)，并对 `seccomp=`/`apparmor=` 发出警告。 |
| `groupAdd` | []string | 否 | — | 附加组 (compose `group_add`)。kubernetes/containerd 仅接受**数值 GID**，并会警告后跳过名称。 |
| `sysctls` | map[string]string | 否 | — | 具命名空间的内核参数 (compose `sysctls`)。 |
| `extraHosts` | []string | 否 | — | 自定义 `/etc/hosts` 条目，格式为 `host:ip` (compose `extra_hosts`)。containerd 忽略。 |
| `dnsServers` | []string | 否 | — | 自定义 nameserver (compose `dns`)。不同于 `dns` caretaker 字段。containerd 忽略。 |
| `dnsSearch` | []string | 否 | — | 自定义 DNS 搜索域 (compose `dns_search`)。containerd 忽略。 |
| `dnsOptions` | []string | 否 | — | 自定义解析器选项 (compose `dns_opt`)，每项为 `name` 或 `name:value`。containerd 忽略。 |
| `ulimits` | [][Ulimit](#ulimit) | 否 | — | 每种资源的 rlimit (compose `ulimits`)。kubernetes 忽略。 |
| `tmpfs` | []string | 否 | — | tmpfs 挂载，每项是容器路径，可选以 `:` 分隔的选项 (例如 `/run:size=64m`)。 |
| `devices` | []string | 否 | — | 主机设备映射 (compose `devices`)，每项为 `host:container[:perms]` (`perms` 默认 `rwm`)。kubernetes 忽略。 |
| `shmSize` | int64 | 否 | `0` (后端默认值) | `/dev/shm` 的字节大小 (compose `shm_size`)。 |
| `pidMode` | string | 否 | 后端默认值 | PID 命名空间模式 (compose `pid`)，例如 `host`。kubernetes/containerd 仅映射 `host`。 |
| `ipcMode` | string | 否 | 后端默认值 | IPC 命名空间模式 (compose `ipc`)，例如 `host`。kubernetes/containerd 仅映射 `host`。 |
| `egress` | [EgressSpec](#egressspec) | 否 | — | 通过客户端侧网络视点路由出站流量。请参阅[Egress](/zh/guides/egress)。 |
| `ingress` | [IngressSpec](#ingressspec) | 否 | — | 请求在工作负载发布端口前方的公网 HTTP(S) Ingress。**仅 kubernetes** (主机后端会警告并忽略)。请参阅 [Ingress](/zh/guides/ingress)。 |

::: tip
`restart` 从 Compose 的 `deploy.restart_policy.condition` 映射而来 (`none`→`no`、`on-failure`→`on-failure`、`any`→`always`)；当规划器写入规范时，它优先于服务级的 `restart:`。
:::

## 嵌套类型

### PortMapping

将主机端口映射到容器端口 (`ports[]`)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | int | 是 | — | 要发布的主机端口。 |
| `container` | int | 是 | — | 要访问的容器端口。 |
| `protocol` | string | 否 | `tcp` | `tcp` 或 `udp`。 |
| `hostIP` | string | 否 | `0.0.0.0` (全部接口) | 将主机侧发布限制到特定接口 (compose `127.0.0.1:8080:80`)。主机后端支持；kubernetes Service 没有等价物。 |

### Mount

将主机源路径绑定到容器内 (`mounts[]`)。不同于受管 [`volumes`](#volumespec) 条目。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `source` | string | 是 | — | 要绑定的主机路径。 |
| `target` | string | 是 | — | 将它挂载到的容器路径。 |
| `readOnly` | bool | 否 | `false` | 以只读方式挂载。 |
| `selinux` | string | 否 | — | SELinux 重新标记 (compose `:z`/`:Z`): `z` 在容器间共享内容，`Z` 使其私有。dockerhost 会应用；containerd/kubernetes 不会重新标记。 |

### VolumeSpec
| `immutable` | bool | 否 | `false` | 内容在部署生命周期内不变的客户端本地只读挂载。启用 server 按文件缓存。对于 server 主机挂载会忽略。 |
| `asyncCache` | bool | 否 | `false` | 使用缓存一致 block protocol 的客户端本地可写挂载。需要一个 replica，且不能与 `readOnly` 或 `immutable` 组合。对于 server 主机挂载会忽略。 |

挂载到容器的受管 (非绑定) 卷 (`volumes[]`)。在 kubernetes 上它会成为动态配置的 PersistentVolumeClaim；在 dockerhost 上成为 Docker 匿名/具名卷。首次启动时，卷会以镜像在 `target` 提供的内容进行初始化 (Docker 卷语义)；后续启动会保留写入内容。

`name` 字段选择两种 Compose 卷形式:

- **匿名** (`name` 为空): 存储对该部署私有且为临时存储，删除部署时会被回收 (类似 `docker rm -v`)。
- **具名** (`name` 已设置): 共享的、项目作用域的存储，其生命周期独立于任何一个部署；使用它的任一单独部署执行 `cornus delete` 后，它都将**继续保留**。请提供已经带项目作用域的逻辑名称 (例如 `myproj_cache`)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 否 | 匿名 | 已设置 => 共享/持久具名卷；空值 => 匿名。 |
| `target` | string | 是 | — | 容器挂载路径。 |
| `size` | string | 否 | `1Gi` | 请求的大小，例如 `1Gi`。 |
| `storageClass` | string | 否 | 集群默认 class | PVC 的 Kubernetes StorageClass。 |
| `readOnly` | bool | 否 | `false` | 以只读方式挂载。 |
| `driver` | string | 否 | Docker 默认值 (`local`) | **具名**卷的卷插件 (compose `driver`)。仅 dockerhost；kubernetes/containerd 忽略。 |
| `driverOpts` | map[string]string | 否 | — | 不透明的驱动选项 (compose `driver_opts`)。仅 dockerhost。 |
| `labels` | map[string]string | 否 | — | **具名**卷上的用户元数据。dockerhost 会设置；kubernetes 将它们复制到 PVC (管理标签优先)；containerd 忽略。 |

### NetworkAttachment

工作负载在用户定义网络中的一个成员资格 (`networks[]`)，遵循 Docker/Compose 用户网络语义: 成员可由同一网络其他成员通过服务名称 (及任意别名) 访问，并且在网络结构支持时，与其未加入的网络隔离。

`driver` 选择 kubernetes 后端实现网络的方式；空值使用后端默认值 (`CORNUS_K8S_NET_DRIVER`，其自身默认 `services`)。已识别的 kubernetes 驱动有: `services` (仅 DNS，任何集群均可用)、`bridge`/`ipvlan`/`macvlan` (Multus CNI)、`cilium`。dockerhost 后端将 `driver` 直接传给 Docker 自己的网络驱动。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 项目作用域网络资源名称 (例如 `myproj_frontend`)。 |
| `driver` | string | 否 | `services` (kubernetes) / Docker bridge | 实现驱动 (见上文)。 |
| `driverOpts` | map[string]string | 否 | — | 传给驱动的不透明每网络选项 (compose `driver_opts`)。 |
| `aliases` | []string | 否 | — | 此成员在网络上的额外 DNS 名称。 |
| `default` | bool | 否 | `false` | kubernetes 的分离主网络模式: 替换 Pod 的主接口 (Multus default-network)。最多一个 attachment 可设置此项。dockerhost 忽略。 |
| `ip` | string | 否 | — | 以 CIDR 形式固定此成员在该网络上的 IPv4 地址 (例如 `10.222.14.7/24`)。仅限由 Multus 实现的网络；dockerhost 忽略 (libnetwork 原生处理地址)。 |
| `subnet` | string | 否 | — | 网络 IPAM 子网 (compose `ipam.config[0].subnet`)。dockerhost 和 Multus netdriver 使用；containerd 忽略。 |
| `gateway` | string | 否 | — | 网络 IPAM 网关。仅 dockerhost。 |
| `ipRange` | string | 否 | — | 网络 IPAM IP 范围。仅 dockerhost。 |
| `internal` | bool | 否 | `false` | 将网络限制为网络内部流量，不允许外部出站 (compose `internal`)。仅 dockerhost。 |
| `attachable` | bool | 否 | `false` | 允许独立容器加入 swarm 作用域网络 (compose `attachable`)。仅 dockerhost。 |
| `enableIPv6` | bool | 否 | `false` | 启用 IPv6 地址分配 (compose `enable_ipv6`)。仅 dockerhost。 |
| `labels` | map[string]string | 否 | — | 网络上的用户元数据。仅 dockerhost (管理标签优先)。 |
| `ipv6` | string | 否 | — | 固定此成员的每网络 IPv6 地址 (compose `ipv6_address`)。仅 dockerhost。 |
| `mac` | string | 否 | — | 固定此成员的 MAC 地址 (compose `mac_address`)。仅 dockerhost。 |
| `priority` | int | 否 | `0` | 网络挂载顺序 (compose `priority`): 优先级最高的网络先加入，其网关成为默认路由。仅 dockerhost。 |

### ProxySpec

配置工作负载的用户空间出站代理 (`proxy`)。**仅 kubernetes。** `allow` 是工作负载可访问的对等服务名称集合 (共享代理网络的服务)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `mode` | string | 否 | `enforcing` | `enforcing` (所有出站 TCP 被重定向至 nftables 边车，仅允许解析为 `allow` 对等方的目标: 真正的 L4 隔离) 或 `cooperative` (软隔离: 每个 `allow` 对等方的 DNS 名称指向由边车转发的回环地址；直接拨号原始 Pod IP 可绕过)。 |
| `allow` | []string | 否 | — | 工作负载可访问的对等服务名称。 |
| `ports` | map[string][]int | 否 | — | 协作模式: 每个 `allow` 对等方要代理的容器端口。 |
| `listenPort` | int | 否 | 后端默认值 | 边车监听被重定向流量的端口。 |

### DNSSpec

配置每 Pod 的 caretaker DNS 解析器 (`dns`)。**仅 kubernetes。** `records` 将对等服务名称映射至 Pod 应解析出的 IPv4 地址 (通常是对等方的用户网络 / Multus 次级地址)。`records` 之外的所有请求都转发到集群 DNS。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `records` | map[string]string | 否 | — | 要解析到的对等服务名称 → IPv4 地址。 |
| `requireUserNet` | bool | 否 | `false` | 标记指向 Multus 次级地址的记录。当集群无法实现 Multus 网络结构时，后端会完全跳过 DNS caretaker，解析会降级为集群 DNS。 |

### DockerSpec

配置 caretaker 的 Docker Engine API 端点 (`docker`)。**仅 kubernetes。** caretaker 会在 Pod 回环端点上绑定 Docker-API 代理，并注入 `DOCKER_HOST`，使标准 `docker` / `docker compose` 驱动管理该 Pod 自身 stack 的同一 cornus server。要求服务器上存在客户端作用域 token Secret (`CORNUS_CLIENT_TOKEN_SECRET`)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `transport` | string | 否 | `tcp` | `tcp` (绑定 `127.0.0.1:port`)、`unix` (在 `socketPath` 处绑定 socket)，或 `both` (`DOCKER_HOST` 随后指向 TCP 端点)。 |
| `port` | int | 否 | `2375` | `tcp` / `both` transport 的回环 TCP 端口。 |
| `socketPath` | string | 否 | `/cornus/docker/docker.sock` | `unix` / `both` transport 的 Unix socket 路径 (位于共享 emptyDir)。 |
| `envVar` | string | 否 | `DOCKER_HOST` | 用于向应用容器公布端点的环境变量。 |

### HubSpec
### TelemetrySpec

在 caretaker 中运行内置 OpenTelemetry Collector (Compose 的 service 或 project level `x-cornus-telemetry:`，CLI 的 `--telemetry-*`)。应用将 OTLP 发送到 pod-loopback receiver，Collector 导出到 `endpoint`；后端自动注入工作负载的 `OTEL_*` env。适用于所有后端。参见[可观测性](/zh/guides/observability#workload-telemetry)。需要启用 collector 的镜像 (`-tags otelcol`，发布镜像已设置)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | 否 | `false` | 开启 telemetry；非空 `endpoint` 也会开启。 |
| `endpoint` | string | 是 | — | 要导出的外部 OTLP backend (`grpc` 用 `host:port`，http/protobuf 用 URL)。 |
| `protocol` | string | 否 | `grpc` | exporter protocol：`grpc` 或 `http/protobuf`。 |
| `headers` | map[string]string | 否 | — | 静态 export header；Kubernetes 通过 Deployment 拥有的 Secret 投影，而不是放进 pod spec。 |
| `insecure` | bool | 否 | `false` | 禁用到 backend 的传输安全。 |
| `signals` | []string | 否 | 全部 | 将 pipeline 限制为 `traces`、`metrics` 和 `logs`。 |
| `serviceName` | string | 否 | deployment name | 覆盖注入应用的 `OTEL_SERVICE_NAME`。 |
| `resourceAttributes` | map[string]string | 否 | — | 与 cornus 派生默认值合并的额外 `OTEL_RESOURCE_ATTRIBUTES`。 |
| `grpcPort` / `httpPort` | int | 否 | `4317` / `4318` | Pod 内 OTLP receiver 回环端口。 |
| `debug` | bool | 否 | `false` | 同时将收集的 telemetry 输出到 Collector stdout。 |


请求工作负载到工作负载覆盖网络成员资格 (`hub`)。**仅 kubernetes。** 请参阅[工作负载 Hub](/zh/guides/hub)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `identity` | string | 否 | 部署名称 | 策略身份。 |
| `export` | [][HubExport](#hubexport-hubimport-hubimportdynamic) | 否 | — | 此工作负载在覆盖网络上承载的服务。 |
| `import` | [][HubImport](#hubexport-hubimport-hubimportdynamic) | 否 | — | 此工作负载通过覆盖网络访问的服务。 |
| `importDynamic` | [HubImportDynamic](#hubexport-hubimport-hubimportdynamic) | 否 | — | 让工作负载选择加入动态 import 发现。 |

#### HubExport / HubImport / HubImportDynamic

**`HubExport`**: 此工作负载在覆盖网络上承载的一项服务:

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 覆盖网络上的服务名称。 |
| `port` | int | 是 | — | 服务监听的端口。 |
| `deliver` | bool | 否 | `false` | 请求 ingress delivery (Hub 中继至此 Pod，再由其拨号 localhost 上的 `port`)，使服务无需能从 Hub 访问。 |
| `protocol` | string | 否 | `tcp` | `tcp` 或 `udp`。 |

**`HubImport`**: 此工作负载通过覆盖网络访问的一项服务:

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 要访问的服务名称。 |
| `ports` | []int | 是 | — | 要绑定回环监听器的端口。 |
| `protocol` | string | 否 | `tcp` | `tcp` 或 `udp`。 |

**`HubImportDynamic`**: 订阅 Hub 目录推送，并在**每个**已列出的服务的合成 IP 上绑定回环监听器 (排除该工作负载自身的 exports 和静态 imports)，随服务出现和消失添加/关闭监听器。不会配置 DNS 记录 (部署时名称未知):

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `ports` | []int | 是 | — | 每个已发现服务绑定的共享端口集。 |
| `protocol` | string | 否 | `tcp` | `tcp` 或 `udp`。 |

### CredentialSpec

将客户端来源的凭据代理至工作负载 (`credentials`)。密钥值由客户端签发 (绝不包含在此规范中)，并通过 cornus server 和 caretaker 边车交付。在前台 `cornus deploy --server` 会话中由 **kubernetes** 实现；主机后端使用配套 caretaker；`--detach` 和其他后端会拒绝/忽略它。请参阅[凭据](/zh/guides/credentials)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `sources` | [][CredentialSource](#credentialsource) | 否 | — | 每项都是容器可按需获取的一份凭据。 |

#### CredentialSource

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 逻辑凭据名称。也用作 capability key 和默认文件基名 / 端点路径段。 |
| `backend` | string | 是 | — | 签发凭据的客户端侧后端 (例如 `aws-sts`、`static`、`exec`)。在调用方机器上使用调用方自身的云/API 凭据运行。 |
| `config` | map[string]string | 否 | — | 非密钥后端配置 (例如 `role_arn`、`duration`、`region`)。绝不能包含密钥本身。 |
| `ttl` | string | 否 | 后端默认值 | 客户端侧缓存/刷新提示，采用 Go duration 字符串。 |
| `deliver` | [][CredentialDelivery](#credentialdelivery) | 否 | — | 容器使用凭据的方式。空值有效 (可获取但不会呈现)。 |

#### CredentialDelivery

一种与提供方无关、用于向容器呈现凭据的方式。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `kind` | string | 否 | `endpoint` | `endpoint` (HTTP 元数据服务器 / 注入认证的代理)、`file` (实体化到共享卷中的路径)，或 `env` (注入应用容器的环境)。 |
| `provider` | string | 否 | `generic` | **endpoint 类型。** `generic` 提供 cornus 原生 JSON 协议 (`GET /credentials/<name>`)；`aws-imds` 和未来适配器会将同一凭据渲染成云 SDK 预期的格式。 |
| `wellKnown` | bool | 否 | `false` | **endpoint 类型。** 在 Pod netns 中绑定提供方的规范链路本地地址 (例如 AWS `169.254.169.254`、IMDSv2)。需要 `NET_ADMIN`；为 false 时端点绑定回环地址，并通过注入的环境变量公布 (对 `aws-imds` 而言为 ECS 容器凭据端点 `AWS_CONTAINER_CREDENTIALS_FULL_URI`)。 |
| `upstream` | string | 否 | provider 默认值 | **endpoint 类型、auth-proxy 提供方。** 覆盖代理转发的供应商 API (例如兼容 Anthropic/OpenAI 的网关)。不含密钥。 |
| `path` | string | 否 | — | **file 类型。** 要将凭据实体化到的容器路径。 |
| `format` | string | 否 | `json` | **file 类型。** `json` (中立的 `{values,expiration}` 对象)、`env` (`KEY=VALUE` 行)、`raw` (单个值)，或 `aws-credentials` (ini profile)。 |
| `envVar` | string | 否 | — | **env 类型。** 要设置的应用容器环境变量。在部署时获取一次并写入 Kubernetes Secret (`secretKeyRef`): 静态、无运行时刷新且存在 etcd 中。短期凭据应优先使用 proxy/file 交付。 |
| `valueKey` | string | 否 | `value` 后为 `token` | **env 类型。** 用于提供环境变量值的凭据 values 键。 |

### Healthcheck

容器健康探测 (`healthcheck`)，以 Docker healthcheck 为模型。在 dockerhost 上会成为 Docker 容器 healthcheck；在 kubernetes 上成为 exec liveness (及 readiness) probe。`test` 使用 Docker 的 `CMD` 形式: 第一个元素是 `CMD` (执行其余元素)、`CMD-SHELL` (通过 shell 运行单个字符串) 或 `NONE` (禁用任何继承的 healthcheck)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `test` | []string | 否 | — | Docker `CMD` 形式的探测命令 (见上文)。 |
| `interval` | string | 否 | 后端默认值 | 探测间隔，Go duration 字符串 (`30s`)。 |
| `timeout` | string | 否 | 后端默认值 | 每次探测超时，Go duration 字符串。 |
| `startPeriod` | string | 否 | 后端默认值 | 失败开始计入前的宽限期，Go duration 字符串。 |
| `startInterval` | string | 否 | 后端默认值 | 启动期间的探测间隔 (compose `start_interval`)。 |
| `retries` | int | 否 | 后端默认值 | 判定不健康前连续失败次数。 |

::: warning containerd
containerd 后端会忽略健康检查 (并发出警告)。
:::

### Resources

限制工作负载的计算资源 (`*Limit` 字段) 和/或预留有保证的下限 (`reserved*` 字段，来自 compose `deploy.resources.reservations`)。零值字段表示“该维度未设置”。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `cpuLimit` | float64 | 否 | `0` (未设置) | 小数 CPU 核数 (例如 `0.5` = 半个核心)。Docker `NanoCpus`；kubernetes 使用 millicores 的 CPU quantity。 |
| `memoryLimit` | int64 | 否 | `0` (未设置) | 字节数。Docker `Memory`；kubernetes memory quantity。 |
| `reservedCpu` | float64 | 否 | `0` (未设置) | 预留下限。kubernetes `resources.requests.cpu`；**在 dockerhost 上无操作** (Docker 没有 CPU reservation)；containerd 忽略。 |
| `reservedMemory` | int64 | 否 | `0` (未设置) | 预留下限。kubernetes `resources.requests.memory`；dockerhost `MemoryReservation`；containerd 忽略。 |

### UpdateConfig

滚动更新策略 (`updateConfig`，来自 compose `deploy.update_config`)。**仅 kubernetes 映射它**，映射至 Deployment 的 `strategy.rollingUpdate`。其他 compose 参数 (`delay`、`monitor`、`max_failure_ratio`) 是 Deployment 无法表达的 swarm 概念，会在转换时丢弃。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `parallelism` | int | 否 | `0` (后端默认值为 1) | 一次更新的实例数。设置 `maxUnavailable` (stop-first) 或 `maxSurge` (start-first) 的大小。 |
| `order` | string | 否 | `stop-first` | `stop-first` (启动新实例前先停止旧实例) 或 `start-first` (移除旧实例前先激增启动新实例)。 |

### Ulimit

一项进程资源限制 (`ulimits[]`，compose `ulimits`)。Compose 的简写形式 (裸整数) 会设置 `soft == hard`。dockerhost 使用 `HostConfig.Ulimits`；containerd 使用 OCI `Process.Rlimits`；kubernetes 忽略。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 裸限制名称 (`nofile`、`nproc`)。 |
| `soft` | int64 | 否 | — | 软限制。 |
| `hard` | int64 | 否 | — | 硬限制。 |

### EgressSpec

通过客户端侧网络视点路由工作负载的**出站**流量 (`egress`)，适用于隔离集群或获准出站路径位于调用方一侧的 VPN/企业代理/SASE 网络。请参阅[Egress](/zh/guides/egress)。

路由按目标确定: 每个流会发送到四种路径之一: `client` (中继至客户端侧网络)、`gateway` (中继至持久出站网关节点，供 `--detach` 使用)、`cluster` (直接出站，无中继) 或 `deny` (丢弃)。`default` 适用于未匹配的目标，默认值为 `cluster`，因此启用 egress 绝不会悄悄转移集群内流量: 你要主动将目标**排除**到 client/gateway。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `mode` | string | 否 | `env` | `env` (将 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`ALL_PROXY` 传入容器: 所有后端，无中继)、`proxy` (caretaker 运行 HTTP CONNECT + SOCKS5 转发代理，经服务器中继回传: 目前 kubernetes，主机后端通过配套 caretaker)，或 `transparent` (所有出站 TCP 由 nftables 重定向捕获并中继: 目前 kubernetes)。 |
| `gateway` | string | 否 | — | **保留字段；当前必须为空。** `gateway` 路径目前经 cornus server 自身出站；验证会拒绝非空值。 |
| `proxies` | map[string]string | 否 | 客户端解析 | `env` 模式: 要注入的显式代理变量。空值会让客户端在部署时解析自身的操作系统代理配置。 |
| `rules` | [][EgressRule](#egressrule) | 否 | — | 声明式路由策略: 有序列表，首个匹配项优先，回退到 `default`。被 `script` 取代。 |
| `script` | string | 否 | — | 可选 PAC 风格 JavaScript (`FindProxyForURL`)，按目标决定路径。设置后取代 `rules`: `DIRECT`→`cluster`，`PROXY client`/`PROXY gateway`→中继路径，`DENY`→丢弃，无匹配→`default`。 |
| `default` | string | 否 | `cluster` | 无规则/脚本匹配目标时使用的路径: `cluster`、`client`、`gateway` 或 `deny`。 |
| `listenPort` | int | 否 | 后端默认值 | caretaker 代理的监听端口 (`proxy` 和 `transparent` 模式)。 |

`proxy` 和 `transparent` 模式会将流量经客户端隧道回传，因此需要实时 deploy-attach 会话 (不能与无状态 `--detach` 部署一起使用)；`env` 则不需要。

#### EgressRule

将目标映射至路径 (`egress.rules[]`)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `pattern` | string | 是 | — | 匹配目标主机 (glob，例如 `*.internal`)、CIDR (例如 `10.0.0.0/8`) 和/或显式端口 (例如 `api.example.com:443`、`10.0.0.0/8:5432`)。空主机或端口部分匹配任意值。 |
| `route` | string | 是 | — | `client`、`gateway`、`cluster` 或 `deny` 之一。 |

### IngressSpec

请求在工作负载 `ClusterIP` Service 前方的公网 HTTP(S) Ingress (`ingress`)。**仅 Kubernetes 后端**: 规范必须发布至少一个端口 (该 Service 是 Ingress 后端)；`dockerhost` / `containerd` 会警告并忽略它。请参阅 [Ingress](/zh/guides/ingress)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | 否 | `false` | 开启 ingress。非空 `hosts` (或 Compose 的 `host:`) 表示 `enabled`；裸 `x-cornus-ingress: {}` 会以每个字段默认值启用它。 |
| `hosts` | []string | 否 | 派生 | 外部主机名；每个都会成为共享一个 TLS 条目的独立 Ingress 规则。`@` 映射至 apex (基域本身，没有 `<name>.` 前缀)。空值会派生单个 `<subdomain>.<domain>` 主机；既无 host 又无基域会被拒绝。 |
| `domain` | string | 否 | `CORNUS_INGRESS_DOMAIN` | `hosts` 为空时，用于自动派生主机名的基域的客户端覆盖值。服务器可要求解析出的主机保留在其域内 (`CORNUS_INGRESS_ENFORCE_DOMAIN`)。 |
| `subdomain` | string | 否 | 部署名称 | 自动派生时添加到基域前的标签 (`<subdomain>.<domain>`)。Compose 转换器会设置 `<service>.<project>`。会清理为 DNS-1123 格式。 |
| `path` | string | 否 | `/` | 要路由的 HTTP 路径前缀。 |
| `pathType` | string | 否 | `Prefix` | Kubernetes 路径匹配类型: `Prefix`、`Exact` 或 `ImplementationSpecific`。 |
| `port` | int | 否 | 首个已发布端口 | ingress 要路由到的容器端口。非零值必须匹配规范的一个已发布端口。 |
| `className` | string | 否 | `CORNUS_INGRESS_CLASS`，然后集群默认值 | Ingress 的 `IngressClassName`。 |
| `annotations` | map[string]string | 否 | — | 原样合并到 Ingress 对象上的注解，用于 controller 专属参数。 |
| `tls` | [IngressTLS](#ingresstls) | 否 | — | 设置时为 host 请求 HTTPS；省略则为纯 HTTP。 |

#### IngressTLS

配置 ingress host 的 HTTPS (`ingress.tls`)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `secretName` | string | 否 | `<name>-tls` | 要提供服务的现有 TLS secret。设置 `clusterIssuer` (或服务器默认值) 时，cert-manager 会配置默认值。 |
| `clusterIssuer` | string | 否 | `CORNUS_INGRESS_TLS_ISSUER` | 设置 `cert-manager.io/cluster-issuer` 注解，使 cert-manager 配置证书。 |

### KnativeSpec

将工作负载部署为 Knative Serving Service (`knative`)。只有集群提供 `serving.knative.dev` 的 Kubernetes 后端才会实现它：后端会创建 `serving.knative.dev/v1` Service 而不是 Deployment 加 Service，由 Knative 管理 autoscaling、scale-to-zero 和 Route。普通集群及 `dockerhost` / `containerd` / `bare` 会警告并忽略它。通常由 `cornus deploy -f service.yaml` 的 Knative descriptor loader 设置。参见 [`cornus deploy`](/zh/cli/deploy)。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `enabled` | bool | 否 | `false` | 标记为 Knative Service。bare `{}` 以所有默认值启用。 |
| `minScale` / `maxScale` | int | 否 | `0` | autoscaling 下限 / 上限；`0` 表示允许 scale-to-zero / unlimited。 |
| `target` | int | 否 | — | 每 replica 的 autoscaling target。 |
| `concurrency` | int | 否 | `0` | 每 replica 同时 request 的硬上限；`0` 表示 unlimited。 |
| `class` | string | 否 | cluster default | autoscaler class：`kpa` 或 `hpa`。 |
| `metric` | string | 否 | `concurrency` | scaling metric：`concurrency`、`rps` 或 `cpu` (`cpu` 需要 `class: hpa`)。 |
| `timeoutSeconds` | int | 否 | `300` | 单个 request 的最长时间。 |
| `port` | int | 否 | first published | Knative 路由到的单一容器端口。 |
| `annotations` | map[string]string | 否 | — | 合并到 revision template 以提供以上字段之外的 autoscaling 参数。 |

## 另请参阅

- [`cornus deploy`](/zh/cli/deploy): 应用规范的命令。
- [部署后端](/zh/reference/deploy-backends): `dockerhost`、`containerd`、`bare` 和 `kubernetes` 如何实现这些字段。
- [Egress](/zh/guides/egress): 深入了解 `egress` 块。
- [Ingress](/zh/guides/ingress): 深入了解 `ingress` 块。
- [凭据](/zh/guides/credentials): `credentials` 块。
- [工作负载 Hub](/zh/guides/hub): `hub` 块及工作负载到工作负载覆盖网络。
