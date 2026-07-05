# cornus port-forward

将一个或多个本地端口转发到部署的容器端口，包括从未发布到主机或通过 Service 暴露的端口。

## 概要

```sh
cornus port-forward [flags] <name> <ports...>
```

## 说明

`cornus port-forward` 为每个映射绑定本地 listener，并将每个已接受连接通过各自的 tunnel 转发到部署的第一个实例，类似 `kubectl port-forward`。它在前台运行，直至 `Ctrl-C`(或 `SIGTERM`)。

对于集群连接配置文件，它使用您的 kubeconfig credential，通过 Kubernetes `pods/portforward` SPDY subresource 将每个连接直接 tunnel 到工作负载 pod(服务器自身的 ServiceAccount 通常无法做到)；仅当直接尝试无法打开 tunnel 时才回退至服务器代理。对于非集群 profile，它经 cornus 服务器 tunnel，由服务器桥接至容器。无论哪种方式，都能访问从未发布到主机或经 Service 暴露的端口。

每个端口映射为 `LOCAL:REMOTE`(本地和容器端口相同时可只写 `PORT`)，可在 Compose ports notation 中附加 `/tcp` 或 `/udp` 后缀(默认 `tcp`)，例如 `5353:53/udp`。端口必须在 `1..65535`。`/udp` 映射转发 datagram 而非 byte stream，并受 dockerhost、containerd 和 bare 后端支持；Kubernetes port-forward 仅支持 TCP，因此此类映射会以警告跳过。

连接由 `--server` 解析，否则使用选定连接配置文件(参见 [`cornus config`](/zh/cli/config))。port-forward 如何融入访问工作负载的完整方式集合，请参见[远程工作流](/zh/topics/remote-workflows)。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | 远程 cornus 服务器 URL(`http(s)://` 或 `ws(s)://`)。回退到选定连接配置文件。 |
| `--address` | — | `127.0.0.1` | 绑定 listener 的本地地址。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | 通过 cornus 服务器代理路由转发，而不是使用 kubeconfig 直接连接 pod(仅集群 profile)。`--no-via-server` 强制直接路径。覆盖 `CORNUS_VIA_SERVER` 和 profile。 |

位置参数:

- `<name>`——要转发到的部署名称(必需)。
- `<ports...>`——一个或多个 `LOCAL:REMOTE[/tcp|/udp]` 映射，或只写 `PORT`(必需)。

## 示例

将本地端口 8080 转发至容器端口 80:

```sh
cornus port-forward web 8080:80
```

一次转发多个端口，并绑定所有接口:

```sh
cornus port-forward --address 0.0.0.0 web 8080:80 5432:5432
```

转发 UDP 端口(dockerhost / containerd 后端):

```sh
cornus port-forward dns 5353:53/udp
```

强制走服务器代理路径，而不是直接到 pod:

```sh
cornus port-forward --via-server web 8080:80
```
