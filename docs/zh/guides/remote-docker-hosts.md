# 通过 SSH 访问远程 docker/containerd 主机

通过 SSH 隧道访问直接运行在远程 **docker 或 containerd 主机** 上的 cornus 服务器。这是 [远程集群](/zh/guides/remote-clusters) 的 docker/containerd 主机对应方案 (后者改为通过 Kubernetes API 隧道连接)。配置好上下文后，普通命令 (`deploy`、`compose`、`exec`、`build` 等) 会通过隧道路由，无需每条命令都添加标志。

隧道**不会绑定本地端口**：cornus 会通过 SSH 连接直接拨号到远程服务器，因此本机不会留下任何监听端口。

若要以交互方式构建该上下文 (选择 SSH 目标和远程地址、验证连接，并为主机生成 systemd 单元)，请运行 [`cornus setup`](/zh/cli/setup) 向导。

## 设置上下文

如果主机已经在 `~/.ssh/config` 中，只需指定别名，cornus 会读取其余配置 (HostName、User、Port、IdentityFile、known_hosts、ProxyJump)：

```sh
cornus config set-context devbox --ssh-host devbox
cornus config use-context devbox
cornus compose -f compose.yaml up -d   # 在 devbox 上通过隧道运行
```

没有 ssh_config 条目时，请显式给出地址和凭据：

```sh
cornus config set-context devbox \
  --ssh-host ssh.example.com:22 \
  --ssh-user ops \
  --ssh-identity-file ~/.ssh/id_ed25519
```

`cornus config get-contexts` 会将 SSH 隧道配置文件显示为 `(ssh-tunnel ops@ssh.example.com:22 -> 127.0.0.1:5000)`。

- `--ssh-remote-addr` 是从远程主机视角 cornus 服务器的监听位置 (默认 `127.0.0.1:5000`，与 `cornus serve --addr` 一致)。
- 显式的 `--ssh-*` 标志会覆盖从 ssh_config 解析出的值；`--ssh-no-config` 会完全忽略 ssh_config。

## 认证

认证遵循 OpenSSH：

- 默认使用本地 **ssh-agent**。如果 agent 中的某把密钥被主机拒绝，导致 "too many authentication failures"，请传入 `--ssh-no-agent`。
- `--ssh-identity-file` 会添加显式密钥。受口令保护的密钥只会在第一次前台连接时提示 **一次**：依次遵循 `SSH_ASKPASS` / `SSH_ASKPASS_REQUIRE`，然后使用终端。重新连接时不会提示。对于无人值守地跨越断线维持的隧道，请将密钥加载进 ssh-agent (agent 会保存解密后的密钥；cornus 不会保存任何解密后的内容)。
- 主机密钥验证为 **fail-closed**：cornus 使用你的 `known_hosts` (`--ssh-known-hosts`、ssh_config 的 `UserKnownHostsFile` 或 `~/.ssh/known_hosts`)，或使用 `--ssh-host-key` 固定的密钥。`--ssh-insecure-host-key` 会禁用检查 (仅用于开发)。

## 穿过隧道的 TLS

SSH 隧道传输原始字节，因此如果远程服务器终止 TLS，你可以使用 `--ssh-tls` 以端到端 HTTPS 拨号。因为端点会经隧道以 `127.0.0.1:<port>` 拨号，请告知 cornus 证书的真实主机名以匹配验证：

```sh
cornus config set-context devbox --ssh-host devbox \
  --ssh-tls --tls-server-name cornus.internal.example.com
```

或者使用 `--tls-ca-cert` 提供信任所呈现证书的 CA，或在开发时使用 `--insecure-skip-verify`。

## Bastion 与 ProxyCommand

原生支持 `ProxyJump` (bastion 链)：在主机别名的 ssh_config 中设置它，cornus 会在进程内拨号每一跳：

```
Host devbox
  HostName 10.0.0.5
  User ops
  ProxyJump bastion.example.com
```

对于进程内路径未实现的 `ProxyCommand` 或 `Match` 块，cornus 会回退到系统 `ssh` 二进制程序：运行一个持久的 `ssh -N -L <unix-socket>:<remote>` 并拨号该 unix socket (仍然不使用本地 TCP 端口)。当主机有 `ProxyCommand` 时会自动这样做，也可以用 `--ssh-use-binary` 强制。它需要 `ssh` 二进制程序，且只支持 Linux/macOS。

## 重新连接

SSH 连接断开时 (网络短暂中断、sshd 重启、主机重启)，cornus 会按需重新建立，因此后续命令会透明地成功。链接断开时正处于**流传输中**的命令 (`logs -f`、交互式 `exec`、正在运行的构建) 会将断开显示为错误；链接恢复后请重新运行一次。

## 注册表说明

如果远程主机的注册表只能通过同一个 SSH 隧道访问，请设置部署目标能自行拉取的显式 `--registry` / `CORNUS_REGISTRY`。节点会自己拉取镜像，而不会通过 CLI 的隧道。请参阅[构建镜像](/zh/guides/building-images)。

**另请参阅：** [远程集群](/zh/guides/remote-clusters)、[cornus config](/zh/cli/config)。
