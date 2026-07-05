# 连接配置参考

**连接配置**是 CLI 侧、kubeconfig 风格的文件，描述如何访问 remote cornus server：一组命名 **context**，每个包含 endpoint、credential、TLS material 和可选 in-cluster port-forward target。它位于开发者机器，**server 永远不会读取它**（server 使用独立的、位于 data directory 的 server-side config）。

通常应使用 [`cornus config`](/zh/cli/config) 管理此文件，而不是手工编辑；此处记录其格式。Canonical source of truth 是 [`pkg/clientconfig/clientconfig.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/clientconfig/clientconfig.go)。

## 文件位置

默认路径位于 platform user config directory 下的 `cornus/config.yaml`：

- Linux/BSD：`~/.config/cornus/config.yaml`
- macOS：`~/Library/Application Support/cornus/config.yaml`
- Windows：`%AppData%\cornus\config.yaml`

显式设置的 `$XDG_CONFIG_HOME` 在**所有** OS 上均被遵循（为采用 XDG 的用户提供 opt-in）：此时文件为 `$XDG_CONFIG_HOME/cornus/config.yaml`。全局 `--config-file` flag 和 `CORNUS_CONFIG` environment variable 完全覆盖此路径。

文件包含 bearer token 和 key path，因此以 `0700` directory 下的 `0600` mode 写入。缺失文件不是 error——CLI 将其视为空 config。

## 示例配置

```yaml
current-context: staging
contexts:
  local:
    server: http://127.0.0.1:5000

  staging:
    server: https://cornus.staging.example.com
    token: eyJhbGciOi...
    tls:
      ca-cert: /etc/cornus/staging-ca.pem
    conduit:
      mode: socks5
      socks5:
        listen: 127.0.0.1:1080
        service-host-suffix: .cornus.internal

  prod-cluster:
    # No static server URL: dial the in-cluster Service via port-forward.
    port-forward:
      kube-context: prod
      namespace: cornus
      service: cornus
      remote-port: 5000
    kube-auth:
      audience: cornus
      expiration-seconds: 3600
    registry-host: registry.prod.example.com:5000
```

## `File`

顶层 document。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `current-context` | string | — | 未给出 `--context` flag 时使用的 context。空表示“未选 context”；CLI 随后依赖每 command flag 与 environment variable。 |
| `contexts` | map[string][Context](#context) | — | 以名称为 key 的 connection profile。 |

## `Context`

一个命名 remote endpoint，包含访问它所需 credential 与 transport setting。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `server` | string | — | cornus server base URL（例如 `https://cornus.example.com` 或 `http://127.0.0.1:5000`）。设置 `port-forward` 且 `server` 为空时，CLI forward 至 in-cluster Service，并改为 dial local end。 |
| `registry-host` | string | 从 server 派生 | 覆盖 build image tag 与 deploy pull ref 所带的 `host[:port]`。通常为空，此时 CLI 向 server 请求（`GET /.cornus/v1/info`），再 fallback 到 `server` endpoint host。仅在 server 无法 introspect 的 topology 设置。 |
| `token` | string | `CORNUS_TOKEN` env | 作为 `Authorization: Bearer` 发送的 bearer token / JWT。空时回退到 `CORNUS_TOKEN` environment variable。 |
| `tls` | [TLS](#tls) | system default | HTTPS endpoint 的可选 custom-CA / mTLS / insecure setting。 |
| `port-forward` | [PortForward](#portforward) | — | 设置时，CLI 在 dial 前 forward 至的 in-cluster Service。 |
| `kube-auth` | [KubeAuth](#kubeauth) | — | 设置时，从 cluster（经 Kubernetes TokenRequest API 的 short-lived ServiceAccount token）派生 bearer token，而非 static `token`。优先于 `token`，但低于显式 `CORNUS_TOKEN` override。 |
| `via-server` | bool（nullable） | unset（direct） | 强制 workload stream operation（compose log、port-forward）经 cornus server proxy，而非 CLI 用开发者 kubeconfig 直接访问 workload pod。仅对 cluster profile 有意义。最低优先级，低于 `CORNUS_VIA_SERVER` env var 和 `--via-server` flag。仅改变 transport，不禁用 `kube-auth` token minting。 |
| `conduit` | [Conduit](#conduit) | port-forward | Client session 向调用方暴露 deployment port 的方式。最低优先级，低于 `CORNUS_CONDUIT` env var 和 `--conduit` flag。见[远程工作流](/zh/topics/remote-workflows)。 |

## `Conduit`

Context 的 session conduit preference：mode 以及 SOCKS5 proxy setting。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `mode` | string | `port-forward` | `port-forward`（每 port automatic forwarding，Compose-like）或 `socks5`（单个 client-side SOCKS5 split-tunnel proxy）。 |
| `socks5` | [Socks5](#socks5) | — | 调整 SOCKS5 proxy；仅在 `mode` 为 `socks5` 时使用。 |

## `Socks5`

配置 SOCKS5 split-tunnel proxy。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:1080` | Proxy bind 的 local address。 |
| `service-host-suffix` | string | `.cornus.internal` | 构造日常默认 resolution rule：带此 suffix 的 CONNECT host 被剥离为 service name 并 tunnel 向内，其余直接 egress。设置 `resolve` 时忽略。 |
| `resolve` | [][ResolveRule](#resolverule) | — | 完全替代 suffix 默认行为的高级、有序 resolution rule list；首个匹配 rule 获胜。 |
| `bare-service-names` | bool（nullable） | enabled | 是否将命名 live service 的 bare、single-label host（例如 `web`，除了 `web.cornus.internal`）向内路由。若 service name 会遮蔽应直接访问的真实 single-label host，设为 `false` 禁用。 |

## `ResolveRule`

一条 SOCKS5 resolution rule。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `pattern` | string | — | 用于测试 `host:port` CONNECT subject 的 regexp。 |
| `replace` | string | — | 产生 `service:port` 的 template（接受 sed-style `\1` backreference）。 |

## `TLS`

HTTPS endpoint 的 client-side TLS material。未设置任何内容时，`Config()` 返回 system default。`client-cert` 和 `client-key` 必须同时设置。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `ca-cert` | string | system trust store | 验证 server certificate 的 PEM CA bundle path，适用于 server CA 不在 system trust store 的情形。 |
| `insecure-skip-verify` | bool | `false` | 禁用 server certificate verification。仅测试。 |
| `client-cert` | string | — | mTLS 的 PEM client certificate path。 |
| `client-key` | string | — | mTLS 的匹配 PEM client key path。 |

Server 侧 mTLS 和 bearer authentication 见[认证与 TLS](/zh/topics/auth-and-tls)。

## `PortForward`

Dial 前要 forward 至的 in-cluster Service（由 CLI service-forwarder 消费）。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `kube-context` | string | current kube context | 要使用的 kubeconfig context。 |
| `namespace` | string | — | Service namespace。 |
| `service` | string | — | 要 forward 的 Service name。 |
| `remote-port` | int | — | Service port；CLI 将其解析为 ready backing pod 及其 target port。 |

## `KubeAuth`

作为 cornus bearer credential 签发的 cluster-issued ServiceAccount token。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `kube-context` | string | `port-forward` block 值 | 要针对其签发的 kubeconfig context。 |
| `namespace` | string | `port-forward` block 值 | ServiceAccount namespace。 |
| `service-account` | string | — | 要为其签发 token 的 ServiceAccount。 |
| `audience` | string | — | Token audience。必须匹配 server 的 `CORNUS_JWT_AUDIENCE`。 |
| `expiration-seconds` | int64 | cluster default | 请求的 token lifetime。 |

## 项目 context 覆盖

项目可以放置 bare `Context` 文档，文件名为 `cornus-context.json`、`cornus-context.yaml`、`cornus-context.yml` 或 `cornus-context.toml`。Cornus 从工作目录向上搜索，使用最近的文件，并在仓库根目录或主目录停止。其字段会覆盖选定的已存 context；显式命令 flag 和环境变量仍优先。未选择已存 context 时，它也可以提供连接。

```yaml
server: https://cornus.staging.example.com
via-server: true
conduit:
  mode: socks5
```

显式指定文件请使用 `--context-file PATH` 或 `CORNUS_CONTEXT_FILE=PATH`。显式指定的文件不存在会报错。`--no-context-file` 会禁用发现，且不能与 `--context-file` 一起使用。

### 信任边界

自动发现的文件是工作树输入，而非受信任的 credential store。默认仅应用 `via-server`；endpoint、token、TLS、registry、port-forward、kube-auth、SSH-tunnel 和 conduit 设置都会忽略。在 Unix 上，Cornus 还会忽略由其他用户拥有的文件，或位于 world-writable 且 non-sticky 目录中的文件。

仅在信任工作树时使用 `--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE=1`。显式命名的 `--context-file` 也会受信任。改变 endpoint 的覆盖必须提供自己的 `token` 或 `kube-auth`；否则会丢弃选定 context 的 credential。Cornus 会在跳过或剥离项目覆盖时发出警告。

## 另请参阅

- [`cornus config`](/zh/cli/config)——创建、选择和编辑 context。
- [远程工作流](/zh/topics/remote-workflows)——conduit、port-forward 及驱动 remote server。
- [认证与 TLS](/zh/topics/auth-and-tls)——bearer token、mTLS 和 cluster-minted identity。
