# Egress

远程工作负载的出站流量通常从运行时所在位置发出。客户端侧出站流量会改为经由部署该工作负载的机器路由，适用于 VPN、企业代理、SASE 网关，或只有调用方一侧具备获准出站路径的隔离集群。它依托实时的 `cornus deploy --server` 会话及每个 Pod 的 caretaker 边车。其配套功能是为工作负载提供由调用方签发的密钥，即[凭据代理](/zh/guides/credentials)；如需处理*入站*方向，即为工作负载提供公网主机名，请参阅 [Ingress](/zh/guides/ingress)。

## 工作原理

### 模式

客户端侧出站流量会通过客户端侧的网络视点路由远程容器的出站流量。它提供三种透明度逐步提升的模式，可通过部署规范中的 `egress:` 块、Compose 的可移植 `x-cornus-egress:` 扩展或 `cornus deploy --egress` 选择启用。

| 模式 | 后端 | 机制 |
| --- | --- | --- |
| `env` | 全部 | 将调用方自身的代理环境变量 (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` / `ALL_PROXY`，从操作系统解析) 传入容器。要求容器能访问该代理；不使用中继。 |
| `proxy` | kubernetes、dockerhost、containerd | caretaker 在回环地址运行 HTTP CONNECT 代理和 SOCKS5，应用的代理环境变量指向它；每条连接都会经服务器隧道回传至终端。 |
| `transparent` | kubernetes、dockerhost、containerd | 通过 nftables 重定向捕获所有应用 TCP 流量，并借助 `SO_ORIGINAL_DST` 恢复原始目标，因此也能覆盖忽略代理变量的应用。 |

在 Compose 中，可在项目级或每个服务上使用 `x-cornus-egress` 扩展 (服务块会完全覆盖项目默认值)。`x-` 前缀使文件对标准 Compose 工具仍然有效，它们会忽略 `x-*` 键。

```yaml
x-cornus-egress:
  mode: proxy
  default: cluster
  rules:
    - pattern: "*.corp.example"
      route: client

services:
  worker:
    image: registry.example/worker:v1
    # This service inherits the project-level policy.
```

### 路由：四种路径

每个目标都会解析为恰好一种路径：

| 路径 | 含义 |
| --- | --- |
| `client` | 中继到客户端侧网络。需要实时 deploy-attach 会话。 |
| `gateway` | 中继到持久化的出站节点 (Cornus 服务器自身)。可配合 `--detach` 使用。 |
| `cluster` | 直接从 Pod 自身网络出站：本地拨号，绝不经中继。 |
| `deny` | 丢弃连接。 |

`default` 适用于未匹配的目标，默认值为 `cluster`，因此启用出站流量绝不会悄悄地转移集群内流量：调用方需要主动将目标*排除*到客户端或网关。中继模式 (`proxy`、`transparent`) 需要实时会话，因此 `cornus deploy --detach` 会拒绝 `client` 路径。若要获得持久的分离式出站流量，仅可路由到 `gateway` / `cluster` / `deny`；服务器即为网关，并且要求运维人员通过 `CORNUS_EGRESS_GATEWAY=1` 显式启用。不支持且会拒绝使用独立的 `gateway:` URL。

策略会在每一跳重新评估：caretaker 先分类，服务器再次检查 (已受损的 Pod 无法自行提升路由权限)，客户端最后再检查一次作为防线，因此三方结果保持一致。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)、[cornus deploy](/zh/cli/deploy)

## 让远程工作负载的出站流量经调用方网络路由

让工作负载的 egress 经您机器的网络访问 VPN、企业代理或隔离集群。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` 模式为 `env` (传播调用方 proxy env var，所有后端支持，无 relay)、`proxy` (caretaker forward proxy 经服务器 relay 回来) 或 `transparent` (nftables redirect，覆盖忽略 proxy var 的应用)。
- `client` route 需要存活的 deploy-attach session。因此直接运行 `cornus deploy --detach` 会拒绝该 route，而 `cornus compose up -d` 会由后台 agent 持有 session。Compose 中使用 `x-cornus-egress:` extension。

**另请参阅：**[cornus deploy](/zh/cli/deploy)

## 仅让特定目标经调用方路由

添加有序、首个匹配优先的路由规则，使只有选定目标经客户端离开。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route '*.corp.example=client' \
  --egress-route '10.0.0.0/8=cluster'
```

- 每条规则格式为 `PATTERN=ROUTE`，route 可为 `client`、`gateway`、`cluster` 或 `deny`；`--egress-route` 可重复，首个匹配生效。
- Pattern 匹配目标 host (glob)、CIDR 和 / 或显式端口 (例如 `api.example.com:443`)。

**另请参阅：**[cornus deploy](/zh/cli/deploy)

## 设置默认 egress route

选择没有规则匹配的目标去向；默认值为 `cluster`，所以启用 egress 不会悄然改道集群内流量。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` 可取 `cluster` (默认)、`client`、`gateway` 或 `deny`。
- `client` route 需要存活 session；持久化的 detached egress 只能路由到 `gateway` / `cluster` / `deny`。

**另请参阅：**[cornus deploy](/zh/cli/deploy)

## 使用 PAC 风格 policy script 配置 egress

除有序规则列表外，路由决策还可以由**兼容 PAC 的 JavaScript 程序**决定，即 `FindProxyForURL(url, host)` 函数，因此现有企业 PAC 文件可以直接使用。它设在出站规范的 `script:` 中 (或 CLI 的 `--egress-pac`)；如存在该字段，则会取代 `rules`。脚本由沙箱化的纯 Go JS 引擎执行，提供标准纯 PAC 内置函数 (`shExpMatch`、`dnsDomainIs`、`isInNet` 等)、受限的每次调用截止时间，并在出错或超时时**故障闭合为 `deny`**。运行时不具备环境权限：没有 `require`，没有实时网络或 DNS I/O (名称只会解析为调用方已知的目标 IP)，并且 `Date` / 随机数是确定性的，因此在 caretaker、服务器和客户端三个评估点中都能得到可复现的结果。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-pac ./egress.pac
```

```js
function FindProxyForURL(url, host) {
  if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
  if (shExpMatch(host, "*.blocked.example")) return "DENY";
  return "DIRECT";
}
```

`FindProxyForURL` 的返回字符串按如下方式映射为路径 (使用第一个以 `;` 分隔的指令)：

| PAC 返回值 | 路径 |
| --- | --- |
| `DIRECT` | `cluster` (直接从 Pod 网络连接) |
| `DENY` / `BLOCK` | `deny` |
| `CLIENT` / `CLUSTER` / `GATEWAY` | 对应路径 (Cornus 扩展) |
| `PROXY client` / `PROXY gateway` / `PROXY cluster` | 对应路径 |
| `PROXY host:port` (具体代理) | `client`：客户端持有实际代理并应用它 |
| 空值、null 或无法识别 | `default` |

同一个程序也可以内联写在规范中：

```yaml
egress:
  mode: proxy
  default: cluster
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, ".corp.example")) return "PROXY client";
      if (shExpMatch(host, "*.blocked.example")) return "DENY";
      return "DIRECT";
    }
```

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)、[cornus deploy](/zh/cli/deploy)
