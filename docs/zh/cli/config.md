# cornus config

管理用于访问远程 cornus server 的客户端侧 connection profile（context），其结构类似 `kubectl config`。

## 概要

```sh
cornus config <subcommand> [flags]
```

## 说明

`cornus config` 读写 cornus client config file；其中保存一个或多个命名 context（connection profile）及 current-context pointer。文件位于 platform user config dir，或由全局 `--config-file` flag / `CORNUS_CONFIG` 指定的路径。

每个 context 描述如何访问 server：base URL、bearer token 或 ServiceAccount-minted auth、TLS material、到 in-cluster Service 的可选 automatic port-forward、direct-vs-proxy `via-server` toggle，以及 session conduit（port-forward 或 SOCKS5）。完整 schema 见[连接配置](/zh/reference/connection-config)。

### Client config 文件格式

文件为 YAML，以名称为 key 的 `contexts:` map 及 `current-context:` field，例如：

```yaml
current-context: prod
contexts:
  prod:
    server: https://cornus.example.com:5000
    token: eyJhbGci...
  staging:
    namespace: cornus-system
```

`view` 默认 redact bearer token，除非给出 `--show-tokens`（或 `--export`）。所有 field 见[连接配置](/zh/reference/connection-config)。

## cornus config get-contexts

以表格列出已配置 connection profile（`*` 标记 current context）。

```sh
cornus config get-contexts
```

## cornus config current-context

打印当前（默认）context name。没有设置时返回 error。

```sh
cornus config current-context
```

## cornus config use-context

设置当前（默认）context。

```sh
cornus config use-context <name>
```

## cornus config set-context

创建或更新 context。

```sh
cornus config set-context [flags] <name>
```

默认 `set-context` 会**替换**同名已有 context：结果精确等于该次 invocation 指定的内容。分层顺序是 `--from-file`（base）、各 individual flag、`--from-file-override`（top）。使用 `--merge` 则将给定 setting 分层到已有 context，保留未设置 field——即 edit-in-place mode。

Config 尚无 context 且 terminal 为 interactive 时，新建 context 会被提示设为 default（current）context。`--insecure-skip-verify` 只会启用该 setting。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | — | — | Cornus server base URL（`http(s)://host:port`）。 |
| `--token` | — | — | 作为 `Authorization: Bearer` 发送的 bearer token / JWT。 |
| `--tls-ca-cert` | — | — | 验证 server certificate 的 PEM CA bundle。 |
| `--tls-client-cert` | — | — | mTLS 所用 PEM client certificate（需要 `--tls-client-key`）。 |
| `--tls-client-key` | — | — | mTLS 所用 PEM client key（需要 `--tls-client-cert`）。 |
| `--insecure-skip-verify` | — | `false` | 禁用 server certificate verification（仅测试）。 |
| `-n`, `--namespace` | — | — | cornus install 的 namespace；除非设置 `--pf-service` 或 `--no-detect`，否则自动检测 Service 和 port。 |
| `--no-detect` | — | `false` | 保存 `--namespace` 而不联系 cluster 检测 Service。 |
| `--pf-kube-context` | — | — | automatic port-forward 所用 kubeconfig context。 |
| `--pf-namespace` | — | — | 要 port-forward 的 in-cluster Service namespace（`--namespace` 的别名）。 |
| `--pf-service` | — | — | 要 port-forward 的 in-cluster Service 名称（跳过 auto-detection）。 |
| `--pf-remote-port` | — | — | 要 port-forward 的 Service port。 |
| `--kube-auth-service-account` | — | — | 通过 TokenRequest API 从此 cluster ServiceAccount 签发 bearer token（代替 static `--token`）。 |
| `--kube-auth-audience` | — | — | 签发 ServiceAccount token 的 audience；必须与 server `CORNUS_JWT_AUDIENCE` 匹配。 |
| `--kube-auth-namespace` | — | — | ServiceAccount namespace（默认 `--pf-namespace`）。 |
| `--kube-auth-kube-context` | — | — | 用于签发 token 的 kubeconfig context（默认 `--pf-kube-context`）。 |
| `--kube-auth-expiration-seconds` | — | `3600` | 请求 token lifetime，单位秒（0 = 默认 3600）。 |
| `--via-server` / `--no-via-server` | — | — | 让 workload log/port-forward 经 cornus server proxy 路由，而非用 kubeconfig 直接访问 pod（仅 cluster profile）。每次运行可由 `CORNUS_VIA_SERVER` 或 command `--via-server` flag 覆盖。 |
| `--conduit-mode` | — | — | Client session 暴露 port 的方式：`port-forward`（每 port local listener，默认）、`socks5`（一个按名称访问 service 的 split-tunnel proxy），或还会设置 proxy bind address 和 suffix 的 `socks5://host:port[?suffix=SUFFIX]` URL。每次运行可由 `CORNUS_CONDUIT` 或 command `--conduit` flag 覆盖。 |
| `--socks5-service-host-suffix` | — | `.cornus.internal` | SOCKS5 `CONNECT` target 会 tunnel 至匹配 service 的 host suffix；其他 host 由 conduit 直接访问。 |
| `--socks5-resolve` | — | — | 高级 SOCKS5 resolution rule `PATTERN=REPLACE`（可重复、有序、首个匹配获胜）；替换 suffix 默认规则。 |
| `--from-file` | — | — | 加载 context definition（bare Context object、JSON/YAML）作为 base layer，individual flag 覆盖它；可重复，后者获胜。 |
| `--from-file-override` | — | — | 加载覆盖 individual flag 的 context definition；可重复，后者获胜。 |
| `--merge` | — | `false` | 将给定 setting merge 至已有 context 而非替换：未设置 field 保留 stored value（edit-in-place）。 |

## cornus config delete-context

删除 context。若 current-context pointer 指向该 context，则一并清除。

```sh
cornus config delete-context <name>
```

## cornus config view

打印 client config file，默认 redact bearer token。

```sh
cornus config view [flags]
```

`--export` 改为将单个 context 打印为 bare Context object（没有 `contexts:` wrapper），可 round-trip 回 `set-context --from-file`；该 mode 默认包含 token（目的就是 reusable export），除非 `--redact`。未使用 `--export` 时，export context 由全局 `--context` flag 选择，否则使用 current context。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--show-tokens` | — | `false` | 打印 bearer token 而非 redact（whole-file view）。 |
| `--export` | — | `false` | 仅打印一个 context，作为可传回 `set-context --from-file` 的 bare Context object。 |
| `--redact` | — | `false` | 使用 `--export` 时，将 bearer token 替换为 `REDACTED`（export 默认包含真实 token）。 |
| `-o`, `--output-file` | — | stdout | 写入此 file（以 `0600` 创建）而非 stdout。 |

## 示例

创建直接访问 server 的 context 并设为 current：

```sh
cornus config set-context prod --server https://cornus.example.com:5000 --token "$TOKEN"
cornus config use-context prod
```

创建自动检测 in-cluster Service 并签发 ServiceAccount token 的 cluster context：

```sh
cornus config set-context staging \
  --namespace cornus-system \
  --kube-auth-service-account cornus-client \
  --kube-auth-audience cornus
```

原地编辑已有 context（保留未设置 field）：

```sh
cornus config set-context prod --merge --conduit-mode socks5
```

导出一个 context（含其 token），以便在其他位置复用：

```sh
cornus config view --export --context prod -o prod-context.yaml
```
