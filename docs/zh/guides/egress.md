# Egress

以下是面向任务的操作方法，用于让远程工作负载的出站流量经调用方网络访问 VPN、企业代理、SASE gateway 或隔离集群。原理参见[客户端侧 egress](/zh/topics/egress)和 [deploy spec](/zh/reference/deploy-spec)。若要改为向工作负载提供调用方签发的 secret，请参见[凭据](/zh/guides/credentials)指南。

## 让远程工作负载的出站流量经调用方网络路由

让工作负载的 egress 经您机器的网络访问 VPN、企业代理或隔离集群。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --egress proxy
```

- `--egress` 模式为 `env`（传播调用方 proxy env var，所有后端支持，无 relay）、`proxy`（caretaker forward proxy 经服务器 relay 回来）或 `transparent`（nftables redirect，覆盖忽略 proxy var 的应用）。
- `client` route 需要存活的 deploy-attach session。因此直接运行 `cornus deploy --detach` 会拒绝该 route，而 `cornus compose up -d` 会由后台 agent 持有 session。Compose 中使用 `x-cornus-egress:` extension。

**另请参阅：**[客户端侧 egress](/zh/topics/egress)、[cornus deploy](/zh/cli/deploy)

## 仅让特定目标经调用方路由

添加有序、首个匹配优先的路由规则，使只有选定目标经客户端离开。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route '*.corp.example=client' \
  --egress-route '10.0.0.0/8=cluster'
```

- 每条规则格式为 `PATTERN=ROUTE`，route 可为 `client`、`gateway`、`cluster` 或 `deny`；`--egress-route` 可重复，首个匹配生效。
- Pattern 匹配目标 host（glob）、CIDR 和 / 或显式端口（例如 `api.example.com:443`）。

**另请参阅：**[客户端侧 egress](/zh/topics/egress)、[cornus deploy](/zh/cli/deploy)

## 设置默认 egress route

选择没有规则匹配的目标去向；默认值为 `cluster`，所以启用 egress 不会悄然改道集群内流量。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy --egress-route 'api.internal=client' --egress-default deny
```

- `--egress-default` 可取 `cluster`（默认）、`client`、`gateway` 或 `deny`。
- `client` route 需要存活 session；持久化的 detached egress 只能路由到 `gateway` / `cluster` / `deny`。

**另请参阅：**[客户端侧 egress](/zh/topics/egress)、[cornus deploy](/zh/cli/deploy)

## 使用 PAC 风格 policy script 配置 egress

使用兼容 PAC 的 `FindProxyForURL` 程序替代规则列表，使现有企业 PAC 文件可直接使用。

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

- `--egress-pac` 优先于 `--egress-route`。返回值映射为 `DIRECT` -> `cluster`、`PROXY client` / `PROXY gateway` -> 相应 route、`DENY` -> 丢弃、无匹配 -> `--egress-default`。
- script 在 sandbox 中运行（没有 `require`，没有实时 I/O），发生错误或超时时会 fail closed 到 `deny`。

**另请参阅：**[客户端侧 egress](/zh/topics/egress)、[cornus deploy](/zh/cli/deploy)
