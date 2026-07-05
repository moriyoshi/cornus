# cornus exec

通过远程 cornus 服务器在部署的第一个实例中运行命令 (docker exec) 。

## 概要

```sh
cornus exec [flags] <name> -- <cmd> [args...]
```

## 说明

`cornus exec` 会针对远程 cornus 服务器创建并启动 exec，将本地 stdio 桥接到部署第一个实例中运行的命令。部署名称后的所有内容都会原样传给命令，因此 `-c` 等 flag 会到达命令而不是 cornus。

服务器由 `--server` / `CORNUS_SERVER` 选择，否则回退到选定连接配置文件 (参见 [`cornus config`](/zh/cli/config)) 。

使用 `-i` 时，本地 stdin 会转发给命令。使用 `-t` 时 cornus 请求 pseudo-TTY，但仅在 stdin 本身是终端时请求: pipe 或 CI 调用会降级为普通 stream 并给出警告，而不会创建客户端无法驱动的服务器 PTY。TTY 模式下本地终端以 raw mode 驱动，窗口大小变化会被转发。

cornus 将远程命令的退出码作为自身退出码传播。若命令已结束但无法读取退出状态 (inspect 失败) ，cornus 以 `125` 退出，符合 docker 对“命令已运行但工具无法完成”的约定。

`--forward-agent` 将本地 ssh-agent (`SSH_AUTH_SOCK`) 转发进 exec session，使 session 中运行的 `ssh` 等命令可以使用 agent 持有的密钥。此转发使用 caretaker `AgentRelayRole`，根据 backend 有两种可用方式:

- **dockerhost/containerdhost**: 适用于 remote-mode backend (`CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE`，参见[部署后端](/zh/reference/deploy-backends))，因为它复用该模式已为每个 instance 预置的常驻 companion sidecar。对同机 (非 remote) backend 使用时，会以明确错误拒绝。
- **kubernetes**: 仅适用于通过 [DeploySpec](/zh/reference/deploy-spec) 设置 `agentForward` 后应用的部署 (Compose service 通过 `x-cornus-agent-forward: true` 设置) 。这是每个 deployment 的显式 opt-in，因为 kubernetes 没有 backend 范围的“remote mode”，而仅为此功能给每个部署运行 caretaker sidecar 会造成浪费。对应用时未设置该选项的部署使用时，会以明确错误拒绝。

与 `ssh -A` 一样，只应对信任的 cornus 服务器使用: exec session 打开期间，服务器可以要求转发的 agent 为任意 challenge 签名，并不限于 exec 命令本身发出的 challenge。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | 选定 profile | 远程 cornus 服务器 URL (`http(s)://` 或 `ws(s)://`) 。回退到选定连接配置文件。 |
| `-i`, `--interactive` | — | `false` | 保持 stdin 打开并转发给命令。 |
| `-t`, `--tty` | — | `false` | 分配 pseudo-TTY (stdin 不是终端时降级为普通 stream) 。 |
| `--forward-agent` | — | `false` | 将本地 ssh-agent 转发进 exec session (remote-mode dockerhost/containerdhost，或 deployment 上设置了 `agentForward` 的 kubernetes) 。 |
| `name` (位置参数) | — | 必需 | 要 exec 进入的部署名称。 |
| `cmd...` (位置参数) | — | 必需 | 要运行的命令和参数 (原样传递) 。 |

## 示例

运行一次性命令:

```sh
cornus exec myapp -- ls -la /app
```

打开交互式 shell:

```sh
cornus exec -it myapp -- sh
```

指定服务器:

```sh
cornus exec --server https://cornus.example.com myapp -- env
```

## 另请参阅

- [`cornus deploy`](/zh/cli/deploy)
- [`cornus config`](/zh/cli/config)
- [使用远程集群](/zh/guides/remote-clusters)
