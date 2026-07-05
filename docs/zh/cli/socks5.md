# cornus socks5

运行客户端侧 SOCKS5 split-tunnel proxy，以名称访问 cornus 服务器的工作负载。

## 概要

```sh
cornus socks5 [flags]
```

## 说明

`cornus socks5` 绑定本地 SOCKS5 proxy。`CONNECT` target 的 `host:port` 若匹配 resolution rule——默认是任何带有 `--service-host-suffix` 的 host (例如 `web.cornus.internal`) ——会经服务器的 port-forward transport tunnel 到该服务；其他所有目标都由本机直接 dial。命令会在前台运行至 `Ctrl-C` (或 `SIGTERM`) 。

这是由 `cornus config set-context --conduit-mode socks5` 选择的每 session conduit mode 的临时对应方式。它以 profile 的 SOCKS5 设置为起点，再应用显式 flag override。SOCKS5 conduit 与 port-forward 和其他工作负载访问方式的关系，请参见[网络与 conduit](/zh/guides/networking)。

连接由 `--server` 解析，否则使用选定连接配置文件 (参见 [`cornus config`](/zh/cli/config)) 。

SOCKS5 conduit 也可以按名称访问工作负载声明的 ingress 主机——请参见 [Ingress](/zh/guides/ingress#通过-conduit-从本机访问-ingress)。

### Resolution rule

`--service-host-suffix` 是简单方式: 以该 suffix 结尾的任何 host 都会被 tunnel 到匹配服务，并通过剥离 suffix 得到服务名称。`--resolve PATTERN=REPLACE` 是高级形式: 规则有序，首个匹配获胜，并替代 suffix 默认行为。`PATTERN` 匹配 `host:port`，`REPLACE` 产生 `service:port`，支持 sed 风格 `\1` backreference。`PATTERN` 必须能编译为 regular expression。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | 远程 cornus 服务器 URL (`http(s)://` 或 `ws(s)://`) 。回退到选定连接配置文件。 |
| `--listen` | — | `127.0.0.1:1080` | 要绑定 SOCKS5 proxy 的本地地址 (或 profile 值) 。 |
| `--service-host-suffix` | — | `.cornus.internal` | `CONNECT` target 被 tunnel 到匹配服务的 host suffix；其他 host 直接 egress。 |
| `--resolve` | — | — | 高级 resolution rule `PATTERN=REPLACE` (可重复、有序、首个匹配获胜) ；替代 suffix 默认值。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | 让 tunnel 连接经 cornus 服务器代理路由，而非使用 kubeconfig 直接连接 pod (仅集群 profile) 。`--no-via-server` 强制直接路径。覆盖 `CORNUS_VIA_SERVER` 和 profile。 |
| `--allow-non-loopback` | — | `false` | 允许将 `--listen` 绑定到非 loopback 地址 (请参见下面的安全说明) 。 |

## 默认仅限 loopback

该 proxy 不执行身份验证 (它提供 SOCKS5 无身份验证方式)，并从运行它的机器直接连接任意不匹配的目标。因此，绑定到非 loopback 地址后，它会成为任何能够路由到该地址的人都可使用的**开放 proxy**，甚至可以成为进入该机器自身 loopback 服务的路径。为此，除非传入 `--allow-non-loopback` 表明这种暴露是有意的，否则会拒绝非 loopback 的 `--listen` (例如 `0.0.0.0:1080`) 。即使如此，proxy 也会拒绝连接 loopback 和 link-local 目标，因此不能用它访问 proxy 主机上的服务。

## 示例

在默认地址启动 proxy:

```sh
cornus socks5
```

然后将客户端指向它，并按名称访问服务:

```sh
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

绑定自定义地址并使用其他 service-host suffix:

```sh
cornus socks5 --listen 127.0.0.1:1085 --service-host-suffix .svc.local
```

使用高级 resolution rule，而不是 suffix 默认值:

```sh
cornus socks5 --resolve '^(.+)\.internal:(\d+)$=\1:\2'
```
