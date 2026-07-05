# cornus exec

通过远程 cornus 服务器在部署的第一个实例中运行命令（docker exec）。

## 概要

```sh
cornus exec [flags] <name> -- <cmd> [args...]
```

## 说明

`cornus exec` 会针对远程 cornus 服务器创建并启动 exec，将本地 stdio 桥接到部署第一个实例中运行的命令。部署名称后的所有内容都会原样传给命令，因此 `-c` 等 flag 会到达命令而不是 cornus。

服务器由 `--server` / `CORNUS_SERVER` 选择，否则回退到选定连接配置文件（参见 [`cornus config`](/zh/cli/config)）。

使用 `-i` 时，本地 stdin 会转发给命令。使用 `-t` 时 cornus 请求 pseudo-TTY，但仅在 stdin 本身是终端时请求：pipe 或 CI 调用会降级为普通 stream 并给出警告，而不会创建客户端无法驱动的服务器 PTY。TTY 模式下本地终端以 raw mode 驱动，窗口大小变化会被转发。

cornus 将远程命令的退出码作为自身退出码传播。若命令已结束但无法读取退出状态（inspect 失败），cornus 以 `125` 退出，符合 docker 对“命令已运行但工具无法完成”的约定。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | 选定 profile | 远程 cornus 服务器 URL（`http(s)://` 或 `ws(s)://`）。回退到选定连接配置文件。 |
| `-i`, `--interactive` | — | `false` | 保持 stdin 打开并转发给命令。 |
| `-t`, `--tty` | — | `false` | 分配 pseudo-TTY（stdin 不是终端时降级为普通 stream）。 |
| `name`（位置参数） | — | 必需 | 要 exec 进入的部署名称。 |
| `cmd...`（位置参数） | — | 必需 | 要运行的命令和参数（原样传递）。 |

## 示例

运行一次性命令：

```sh
cornus exec myapp -- ls -la /app
```

打开交互式 shell：

```sh
cornus exec -it myapp -- sh
```

指定服务器：

```sh
cornus exec --server https://cornus.example.com myapp -- env
```

## 另请参阅

- [`cornus deploy`](/zh/cli/deploy)
- [`cornus config`](/zh/cli/config)
- [使用远程集群](/zh/guides/remote-clusters)
