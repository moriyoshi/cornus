# cornus hub

从任何位置（例如开发者笔记本电脑）作为 spoke 加入 cornus 工作负载到工作负载的覆盖网络。

## 概要

```sh
cornus hub [flags]
```

## 说明

`cornus hub` 将本主机作为 spoke 连接到 cornus 覆盖网络，并复用 caretaker hub role。本主机提供的服务会被注册为可交付服务：hub 将入站流量 relay 到这个 spoke，再由 spoke 连接本地目标，因此位于 NAT 后的主机无需可被 hub 直接访问。本主机访问的服务会绑定本地 loopback listener，并汇入 hub。至少需要一项 `--register` 或 `--reach`。

连接由 `--server` 解析，否则使用选定连接配置文件——包括其中的 token / kube-auth 和自动端口转发（参见 [`cornus config`](/zh/cli/config)）。解析出的 token 会随 caretaker 的 WebSocket handshake 传送，profile 的 TLS 材料用于 dial。在解析连接前会校验 flag，因此错误 flag 不会启动端口转发。命令会一直运行到 `Ctrl-C`（或 `SIGTERM`）。

覆盖网络模型请参见[工作负载 hub](/zh/topics/hub)。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | cornus 服务器的 Hub URL（`ws(s)://` 或 `http(s)://`）。回退到选定连接配置文件。 |
| `--identity` | — | — | 此 spoke 的 identity（用于 hub policy）。 |
| `--register` | — | — | 向覆盖网络提供本地服务：`name=host:port`（通过 delivery relay 到该 spoke）。可重复。 |
| `--reach` | — | — | 访问一个覆盖网络服务：`name=listen_ip:port`（绑定本地 listener）。可重复。 |

## 示例

向覆盖网络提供本地运行的服务：

```sh
cornus hub --identity laptop --register api=127.0.0.1:8080
```

通过本地 loopback 端口访问覆盖网络服务：

```sh
cornus hub --identity laptop --reach db=127.0.0.1:5432
```

同时进行两项操作：

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```
