# 工作负载 Hub

Cornus 服务器也可充当连接不存在可路由网络的工作负载的**星型 Hub**，包括跨节点、跨集群或位于 NAT 后的笔记本电脑。它将实时挂载的汇合思路从"将一个调用方的导出中继至一个 Pod"扩展为"在已注册工作负载之间中继任意 TCP/UDP 流"。每个参与者都是一个**辐条**；辐条注册自己承载的服务，并按名称访问其他辐条的服务。如果需要面向公网而非其他工作负载的对应功能，请参阅[隧道](/zh/guides/tunnels)。

## 工作原理

### 中继模型

辐条可用两种模式之一注册每项服务：

- **dial-direct**：注册一个 Hub 可访问的地址；Hub 自行拨号。
- **delivery**：注册时不提供地址；Hub 通过向承载辐条*反向*打开 ingress 流来访问服务，该辐条拨号至自己的本地目标并拼接连接。这使 NAT 后和跨集群的目标变得可达：Hub 不需要到达它们的路由。

要访问对等方，源辐条会打开一个命名服务的数据流；Hub 查询该服务，并选择拨号 (直连) 或交付 (通过所属辐条)，然后拼接字节流。**TCP 和 UDP** 均可使用：中继只复制字节，因此 UDP 仅需在两个转换点进行帧封装，通过 `/udp` 端口后缀选择。流量路径为 `app -> caretaker -> hub -> {dial | ingress to spoke}`。

### 策略

访问由两个可选矩阵控制，且仅在配置时执行：**reach** 矩阵 (调用方身份到允许访问的被调用服务，`CORNUS_HUB_POLICY`) 和 **register** 矩阵 (身份到可承载服务名称，`CORNUS_HUB_REGISTER_POLICY`)。辐条会在控制流中声明其身份，但使用 mTLS 时，身份会权威地取自已验证客户端证书的 CommonName，因此策略依据辐条无法伪造的凭据。有关如何建立身份，请参阅[安全与认证](/zh/guides/security)。

### 运行多个副本

Hub 可运行单副本 (默认的内存注册表，适合大多数部署)，也可运行多副本以获得 HA 和连接数量扩展能力。每个副本是其所连接辐条的唯一权威，因此副本拥有互不重叠的注册表分区，它们的并集即为分布式注册表：没有写入冲突，也没有 CRDT 合并。某个副本失效时，其整个分区会消失，辐条会在负载均衡器后重新连接并获得新的所有者。存储选择顺序为 `CORNUS_HUB_REDIS` (共享一个 Redis 的两个副本构成一个 Hub)，然后是 `CORNUS_HUB_STORE=kube` (以 Kubernetes API server 作为租约支持的注册表，无需外部基础设施)，否则使用内存单副本注册表。当 delivery 查找命中非所有者副本时，它会通过已认证的内部端点转发给所有者，从而形成两跳 delivery 路径。

**另请参阅：**[cornus hub](/zh/cli/hub)、[服务器环境变量](/zh/reference/server-env-vars)

## 从 CLI 作为辐条加入 Hub

`cornus hub` 可从任意位置加入覆盖网络，例如 NAT 后的笔记本电脑，用于将本地服务提供给覆盖网络，和/或按名称访问覆盖网络服务。

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```

- `--register name=host:port` 以一个名称提供本地服务 (中继至此辐条，因此 NAT 后的主机仍然可达)；`--reach name=listen_ip:port` 绑定一个本地监听器，将其转发至覆盖网络中同名的服务。至少需要一个。
- `--server` 为可选项，默认使用所选连接配置文件 (显式 `--server` 优先)。目前，携带客户端 TLS 材料 (自定义 CA、mTLS 证书或跳过不安全验证) 的配置文件会被 `hub` 拒绝；请使用系统信任存储接受的服务器证书。

**另请参阅：**[cornus hub](/zh/cli/hub)

## 在工作负载之间导出和导入服务

对于部署在 Kubernetes 上的工作负载，Hub 成员资格通过部署规范中的 `hub:` 块声明，而不是 CLI。`export` 列出工作负载承载的服务；`import` 列出它访问的服务 (后端会为每个 import 分配一个合成回环 IP，并为其配置 DNS 记录和 caretaker 监听器，因此应用中的普通 `dial(peer)` 会解析为该合成 IP，并在无需应用感知的情况下流入 Hub)。

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

- 当服务无法从 Hub 直接访问时，在 export 上设置 `deliver: true` (Hub 中继到 Pod，Pod 再拨号至 localhost 上的端口)。
- `importDynamic` 让工作负载选择加入动态发现：不同于静态 `import` 列表，caretaker 会订阅 Hub 目录推送，并在目录服务出现或消失时，为每个已列出服务的确定性合成 IP 绑定监听器。
- `hub:` 仅限 Kubernetes。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)
