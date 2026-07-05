# 连接配置参考

**连接配置**是 CLI 侧、kubeconfig 风格的文件，描述如何访问 remote cornus server: 一组命名 **context**，每个包含 endpoint、credential、TLS material 和可选 in-cluster port-forward target。它位于开发者机器，**server 永远不会读取它** (server 使用独立的、位于 data directory 的 server-side config) 。

通常应使用 [`cornus config`](/zh/cli/config) 管理此文件，而不是手工编辑；此处记录其格式。Canonical source of truth 是 [`pkg/clientconfig/clientconfig.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/clientconfig/clientconfig.go)。

## 文件位置

默认路径位于 platform user config directory 下的 `cornus/config.yaml`:

- Linux/BSD: `~/.config/cornus/config.yaml`
- macOS: `~/Library/Application Support/cornus/config.yaml`
- Windows: `%AppData%\cornus\config.yaml`

显式设置的 `$XDG_CONFIG_HOME` 在**所有** OS 上均被遵循 (为采用 XDG 的用户提供 opt-in) : 此时文件为 `$XDG_CONFIG_HOME/cornus/config.yaml`。全局 `--config-file` flag 和 `CORNUS_CONFIG` environment variable 完全覆盖此路径。

文件包含 bearer token 和 key path，因此以 `0700` directory 下的 `0600` mode 写入。缺失文件不是 error——CLI 将其视为空 config。

## 示例配置

```yaml
current-context: staging
contexts:
  local:
    server: http://127.0.0.1:5000

  remote-docker:
    # 不使用静态服务器 URL: 通过 SSH 将 HTTP 传送到远程回环监听器。
    ssh-tunnel:
      addr: devbox
      user: ops
      remote-addr: 127.0.0.1:5000

  staging:
    server: https://cornus.staging.example.com
    # 这个环境里的镜像都基于 Debian; 优先尝试 bash。
    shells:
      - /bin/bash
      - /bin/sh
    key-auth:
      identity-file: /home/alice/.ssh/id_ed25519
      key-fingerprint: SHA256:example
      name: alice-laptop
    tls:
      ca-cert: /etc/cornus/staging-ca.pem
    conduit:
      mode: socks5
      socks5:
        listen: 127.0.0.1:1080
        service-host-suffix: .cornus.internal
      ingress:
        mode: emulate
        certificates:
          - certificate: /etc/cornus/web.pem
            key: /etc/cornus/web-key.pem

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
| `server` | string | — | cornus server base URL (例如 `https://cornus.example.com` 或 `http://127.0.0.1:5000`) 。设置 `port-forward` 且 `server` 为空时，CLI forward 至 in-cluster Service，并改为 dial local end。 |
| `registry-host` | string | 从 server 派生 | 覆盖 build image tag 与 deploy pull ref 所带的 `host[:port]`。通常为空，此时 CLI 向 server 请求 (`GET /.cornus/v1/info`) ，再 fallback 到 `server` endpoint host。仅在 server 无法 introspect 的 topology 设置。 |
| `token` | string | `CORNUS_TOKEN` env | 作为 `Authorization: Bearer` 发送的 bearer token / JWT。空时回退到 `CORNUS_TOKEN` environment variable。 |
| `tls` | [TLS](#tls) | system default | HTTPS endpoint 的可选 custom-CA / mTLS / insecure setting。 |
| `port-forward` | [PortForward](#portforward) | — | 设置时，CLI 在 dial 前 forward 至的 in-cluster Service。 |
| `kube-auth` | [KubeAuth](#kubeauth) | — | 设置时，从 cluster (经 Kubernetes TokenRequest API 的 short-lived ServiceAccount token) 派生 bearer token，而非 static `token`。优先于 `token`，但低于显式 `CORNUS_TOKEN` override。 |
| `key-auth` | [KeyAuth](#keyauth) | — | 设置时，证明持有已注册的 SSH 密钥并签发短期会话。优先于 `kube-auth` 和 `token`，但低于 `CORNUS_TOKEN`。`key-auth` 与 `kube-auth` 互斥。 |
| `via-server` | bool (nullable) | unset (direct) | 强制 workload stream operation (compose log、port-forward) 经 cornus server proxy，而非 CLI 用开发者 kubeconfig 直接访问 workload pod。仅对 cluster profile 有意义。最低优先级，低于 `CORNUS_VIA_SERVER` env var 和 `--via-server` flag。仅改变 transport，不禁用 `kube-auth` token minting。 |
| `conduit` | [Conduit](#conduit) | port-forward | Client session 向调用方暴露 deployment port 的方式。最低优先级，低于 `CORNUS_CONDUIT` env var 和 `--conduit` flag。见[网络与 conduit](/zh/guides/networking)。 |
| `ssh-tunnel` | [SSHTunnel](#sshtunnel) | — | `server` 为空时，通过 SSH 访问 cornus 服务器。这相当于主机后端的 `port-forward`；两种自动传输方式互斥。显式的 `server` 会使此块失效。 |
| `tunnel` | [Tunnel](#tunnel) | — | 公共隧道 ([`cornus tunnel`](/zh/cli/tunnel)、[`cornus ingress-tunnel`](/zh/cli/ingress-tunnel)) 的默认值，避免每次调用都重复指定。 |
| `shells` | 字符串列表 | — | 通过此配置到达的工作负载的交互 shell 候选，按优先顺序排列。由 [`cornus web`](/zh/cli/web#终端-shell-探测) 的终端读取，它先探测工作负载自身的 `x-cornus-shells:`，然后是这些，最后才是浏览器自己的列表。每个条目是命令**字符串**而不是预先切分好的参数列表 (`/bin/busybox sh` 是一个条目)。安全敏感: 它指定了一个会在你的工作负载内部执行的二进制文件，因此项目覆盖只有在受信任时才能提供它。 |

## `KeyAuth`

选择用于短期 Cornus 客户端会话的 SSH 签名者。配置文件只保存路径与公钥指纹，绝不保存私钥内容或签发的会话令牌。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `identity-file` | string | — | 本地 SSH 私钥路径。加密密钥使用常规交互式输入或 `SSH_ASKPASS`。 |
| `key-fingerprint` | string | — | SHA256 公钥指纹。没有私钥文件时从 `SSH_AUTH_SOCK` 选择密钥；有文件时固定预期公钥，并允许后台代理在不解锁密钥的情况下查询会话缓存。 |
| `name` | string | 指纹 | 易读的注册名称及生成的调用方身份。 |
| `scope` | string | `api` | 请求的会话 scope。 |
| `ttl` | string | `1h` | 请求的 Go duration 格式有效期，最长 `24h`。 |

## `Conduit`

Context 的 session conduit preference: mode 以及 SOCKS5 proxy setting。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `mode` | string | `port-forward` | `port-forward` (每 port automatic forwarding，Compose-like) 或 `socks5` (单个 client-side SOCKS5 split-tunnel proxy) 。 |
| `socks5` | [Socks5](#socks5) | — | 调整 SOCKS5 proxy；仅在 `mode` 为 `socks5` 时使用。 |
| `ingress` | [Ingress](#ingress) | — | 配置原生或模拟的 ingress 处理以及可选的用户提供服务器证书。 |

## `Socks5`

配置 SOCKS5 split-tunnel proxy。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:1080` | Proxy bind 的 local address。 |
| `service-host-suffix` | string | `.cornus.internal` | 构造日常默认 resolution rule: 带此 suffix 的 CONNECT host 被剥离为 service name 并 tunnel 向内，其余直接 egress。设置 `resolve` 时忽略。 |
| `resolve` | [][ResolveRule](#resolverule) | — | 完全替代 suffix 默认行为的高级、有序 resolution rule list；首个匹配 rule 获胜。 |
| `bare-service-names` | bool (nullable) | enabled | 是否将命名 live service 的 bare、single-label host (例如 `web`，除了 `web.cornus.internal`) 向内路由。若 service name 会遮蔽应直接访问的真实 single-label host，设为 `false` 禁用。 |

## `SSHTunnel`

描述用于访问远程容器主机上 cornus 服务器的 SSH 连接。该传输与后端无关——它承载的是通往 cornus 服务器的原始字节，因此同一个块可以原封不动地访问 `dockerhost`、`containerd`、`bare` 或 `incus` 服务器。配置后，普通命令会透明使用它，无需逐个命令指定隧道 flag。`addr` 可以是 `ssh_config` 主机 alias，因此除非 `no-ssh-config` 将其禁用，否则会继续应用常规的用户、端口、身份、代理和主机密钥设置。

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `addr` | string | — | SSH 目标: `ssh_config` `Host` alias 或字面量 `host[:port]`。 |
| `user` | string | `ssh_config`，然后是当前用户 | SSH 登录用户。 |
| `remote-addr` | string | `127.0.0.1:5000` | 从远程主机看到的 Cornus 监听地址。 |
| `identity-file` | string | SSH agent / `ssh_config` | 用于公钥身份验证的显式 PEM 私钥路径。 |
| `no-agent` | bool | `false` | 禁用通过本地 `SSH_AUTH_SOCK` 进行身份验证。 |
| `known-hosts` | string | `ssh_config`，然后是 `~/.ssh/known_hosts` | 用于主机密钥验证的显式 `known_hosts` 文件。 |
| `host-key` | string | — | 将预期的一个主机密钥固定为 `authorized_keys` 格式的一行。 |
| `insecure-host-key` | bool | `false` | 禁用主机密钥验证。仅供开发使用。 |
| `remote-tls` | bool | `false` | 通过 SSH 隧道使用 HTTPS，因为远程 cornus 进程会终止 TLS。通常与 `tls.server-name` 配合使用。 |
| `no-ssh-config` | bool | `false` | 跳过用户和系统 SSH 配置文件；仅使用此块中的显式字段。 |
| `use-ssh-binary` | bool | auto | 强制使用持久的 `ssh -N -L` 回退传输。当解析后的主机带有 `ProxyCommand` 时，Cornus 会自动选择它，并遵循包括 `Match` 在内的完整 OpenSSH 配置。 |

## `Ingress`

配置通过 SOCKS5 conduit 访问的 ingress。其证书规则还会在原生 Kubernetes 部署 (包括分离部署) 之前用于创建并接入托管 TLS Secret；此具体化过程无需 conduit 保持运行。

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `mode` | string | off | `native` 使用集群 ingress controller；`emulate` 在本地终止 ingress；空或 off 禁用 ingress 处理。 |
| `controller` | [IngressController](#ingresscontroller) | 自动发现 | 原生 ingress controller Service 覆盖。 |
| `ca-file` | string | 自动生成 | 用于签发 emulate 模式回退叶证书的 CA 证书。必须与 `ca-key-file` 配对。 |
| `ca-key-file` | string | 自动生成 | 与 `ca-file` 配对的私钥。 |
| `certificates` | [][IngressCertificate](#ingresscertificate) | — | 模拟 ingress 与原生 ingress 共用的有序用户提供服务器证书规则。 |

## `Tunnel`

公共隧道的按配置文件默认值。它**不保存任何凭据**——只保存其路径——因此被共享或提交到仓库的配置文件绝不会泄露 authtoken。

| 键 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `authtoken-file` | string | — | 保存隧道后端凭据的文件路径，用作 `--authtoken-file` 的默认值。为空表示每次运行时传入，或依赖服务器自身的默认值 (服务器环境中的 `CORNUS_TUNNEL_AUTHTOKEN`)。 |
| `ingress-host-mode` | string | `auto` | [`cornus ingress-tunnel`](/zh/cli/ingress-tunnel)的 `--host-mode` 默认值: `auto`、`passthrough`、`alias` 或 `rewrite`。参见 [Host 处理](/zh/cli/ingress-tunnel#host-处理)。 |

显式 flag 始终优先于这些默认值。[`cornus setup`](/zh/cli/setup)会在探测服务器实际能够托管的能力后，提示你填写它们。

## `IngressCertificate`

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `pattern` | string | 证书 DNS SAN | 精确 DNS 名称或形如 `*.example.com` 的单标签通配符。显式 pattern 必须被证书 SAN 覆盖。精确规则优先于通配符；通配符之间以最长后缀优先。 |
| `certificate` | string | — | PEM 证书链路径。必须与 `key` 一起指定。 |
| `key` | string | — | 匹配的 PEM 私钥路径。必须与 `certificate` 一起指定。 |

对于模拟 ingress，SNI 选择规则，未匹配的名称使用已配置或已生成的回退 CA。对于原生 Kubernetes ingress，每个显式的具体 ingress host 都必须匹配一条规则。Cornus 会将选择同一证书的 host 分组，创建由工作负载 Deployment 拥有的稳定 `kubernetes.io/tls` Secret，在证书轮换时更新它们，将它们接入 Ingress，并删除已过时的托管 Secret。使用托管证书时，必须将自动派生的 host 和 `@` token 展开为具体主机名。

由于原生具体化会在部署请求中发送私钥字节，因此 Cornus 仅允许通过 HTTPS、SSH 隧道 / custom dialer 或回环上的明文 HTTP (包括本地 Kubernetes 端口转发) 发送。它会在序列化请求之前拒绝远程明文 HTTP。

## `IngressController`

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `kube-context` | string | 配置文件的集群 context | 用于原生 controller 端口转发的 kubeconfig context。 |
| `namespace` | string | — | ingress controller Service 所在的 namespace。 |
| `service` | string | 自动发现 | ingress controller Service 名称。 |
| `http-port` | int | 自动发现 | Controller HTTP Service port。 |
| `https-port` | int | 自动发现 | Controller HTTPS Service port。 |

## `ResolveRule`

一条 SOCKS5 resolution rule。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `pattern` | string | — | 用于测试 `host:port` CONNECT subject 的 regexp。 |
| `replace` | string | — | 产生 `service:port` 的 template (接受 sed-style `\1` backreference) 。 |

## `TLS`

HTTPS endpoint 的 client-side TLS material。未设置任何内容时，`Config()` 返回 system default。`client-cert` 和 `client-key` 必须同时设置。

| Field | Type | 默认值 | 说明 |
| --- | --- | --- | --- |
| `ca-cert` | string | system trust store | 验证 server certificate 的 PEM CA bundle path，适用于 server CA 不在 system trust store 的情形。 |
| `server-name` | string | URL 主机名 | 覆盖 SNI 和证书主机名，例如 `remote-tls` 通过 `127.0.0.1` 访问带证书的服务器时。 |
| `insecure-skip-verify` | bool | `false` | 禁用 server certificate verification。仅测试。 |
| `client-cert` | string | — | mTLS 的 PEM client certificate path。 |
| `client-key` | string | — | mTLS 的匹配 PEM client key path。 |

Server 侧 mTLS 和 bearer authentication 见[安全与认证](/zh/guides/security)。

## `PortForward`

Dial 前要 forward 至的 in-cluster Service (由 CLI service-forwarder 消费) 。

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

## `TokenExchange`

通过 [OAuth 2.0 Token Exchange](/zh/guides/security#用第三方-token-换取-cornus-凭据)，把上面各字段产生的凭据换成短期的 Cornus 凭据，并在命令之间缓存。

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 执行交换。 |
| `scope` | string | — | 收窄签发的凭据 (例如 `registry:pull`)。留空则接受服务器 scope map 所授予的全部内容。 |

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus \
  --token-exchange --token-exchange-scope registry:pull
```

- 它与哪个字段产生了 subject token 无关，因此集群 ServiceAccount token、OIDC token 和静态 `token` 都以相同方式交换。
- `scope` 只能**收窄**。服务器策略未授予的 scope 会被拒绝，而不是被悄悄缩减 —— 因此固定了 scope 的 profile 在其下策略发生变化时会明确失败，而不是无声地获得访问权。
- `key-auth` profile 不受影响: 该凭据本就由 Cornus 签发并标明其 scope，没有可交换的内容。
- 没有 exchange endpoint 的服务器 (较旧的 Cornus，或未配置 JWT/JWKS verifier 的服务器) 不算错误。凭据会像以前一样直接发送。

签发的凭据会被缓存，因此交换按 token 生命周期发生一次，而不是每条命令一次；参见 [`CORNUS_TOKEN_CACHE`](/zh/reference/server-env-vars)。

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

自动发现的文件是工作树输入，而非受信任的 credential store。默认仅应用 `via-server`；endpoint、token、TLS、registry、port-forward、kube-auth、SSH-tunnel、conduit 和 shells 设置都会忽略。在 Unix 上，Cornus 还会忽略由其他用户拥有的文件，或位于 world-writable 且 non-sticky 目录中的文件。

`shells` 虽不携带任何凭证，却也在被剥除之列: 它指定了 web 终端在你的工作负载内部执行的二进制文件，而任何能提交 pull request 的人都能写入的文件不应替你做这个选择。

仅在信任工作树时使用 `--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE=1`。显式命名的 `--context-file` 也会受信任。改变 endpoint 的覆盖必须提供自己的 `token` 或 `kube-auth`；否则会丢弃选定 context 的 credential。Cornus 会在跳过或剥离项目覆盖时发出警告。

## 另请参阅

- [`cornus config`](/zh/cli/config)——创建、选择和编辑 context。
- [网络与 conduit](/zh/guides/networking)——conduit mode 和 port-forward。
- [使用远程集群](/zh/guides/remote-clusters)——从 profile 驱动 remote server。
- [安全与认证](/zh/guides/security)——bearer token、mTLS 和 cluster-minted identity。
