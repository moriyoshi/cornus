# cornus setup

用于创建并验证连接配置文件 (即“上下文”) 以访问 cornus 服务器的交互式向导，随后会输出适合该场景的设置指引。它是 [`cornus config set-context`](/zh/cli/config) 的引导式前端，不引入新的配置文件语义。

## 概要

```sh
cornus setup
```

## 说明

`cornus config set-context` 是一长串涵盖多个不同部署拓扑的标志。`cornus setup` 则会询问要配置哪种拓扑，只提出该拓扑所需的问题，写入上下文 (复用同一个客户端配置文件)，可选择测试连接，最后给出后续步骤清单以及等效的 `set-context` 命令。

在真实终端中，向导会呈现丰富的对话界面；在管道、CI 或 `--output plain` 下，它会回退到纯文本行提示 (请参阅[非交互式使用](#非交互式使用))。它拒绝 `--output json`，因为提示会破坏 NDJSON；脚本请使用 `cornus config set-context`。

### 导航

在任何问题处都可以返回或退出。

- **返回上一步** — 在丰富对话界面中按 `Esc` ⎋ 或 `Ctrl-D`，或在纯文本提示中输入 `<` 并按 `Enter` ⏎。从第一个问题返回会回到场景选择器；修改较早的答案只会重新询问依赖它的问题。在回答完所有问题前不会写入任何内容，因此返回始终是安全的。
- **取消向导** — 按 `Ctrl-C` ⌃C。在保存配置文件前，这不会改变配置；保存只在最后以一个原子步骤完成。

## 场景

第一个问题从以下选项中选择一个：

- **本地服务器** — 运行在本机的 `cornus serve` (纯 HTTP 回环)。
- **远程 Docker 主机 (SSH)** — 通过 SSH 隧道访问 docker 主机。
- **远程 containerd 主机 (SSH)** — 通过 SSH 隧道访问 containerd 主机。
- **Kubernetes (自动端口转发)** — 通过自动端口转发访问的集群内安装。向导会自动检测 cornus Service 和端口，无法检测时回退为手动输入。
- **Kubernetes (直接 URL)** — 通过 ingress URL 访问的集群内安装。
- **其他服务器 URL** — 已知 URL 的服务器。

每个场景只会询问所需的内容 (端点或 SSH/Kubernetes 目标、TLS、认证以及可选的注册表主机覆盖)。高级传输选项 (mTLS、`via-server`、通用 conduit/SOCKS5 模式) 请参阅 [`cornus config set-context --help`](/zh/cli/config)。

对于两个 **Kubernetes** 场景，向导还会探测服务器公布的 ingress (`/.cornus/v1/info`)，并询问是否通过 SOCKS5 conduit 访问工作负载的 ingress 主机。建议的合理默认值是：服务器公布 ingress controller 时为 **native** (隧道连接到检测到的 ingress controller)，只暴露 ingress 域名时为 **emulate** (带生成证书的客户端反向代理)，否则为 **off**。你的选择会写入配置文件的 `conduit.ingress` 块并选择 socks5 conduit。请参阅[Ingress](/zh/guides/ingress)。

## 验证

保存后，向导会询问是否测试连接：它会完全按真实命令的方式解析配置文件 (包括任何端口转发)，并调用服务器的 `/.cornus/v1/info` 端点，将结果归类为可达、需要认证、连接被拒绝、TLS 问题、超时等，同时给出修复提示。验证不会使命令失败，配置文件无论如何都会保持已保存状态。

## 产物

对于 SSH 场景，向导会询问是否为远程主机写入 `cornus.service` systemd 单元；对于 Kubernetes 场景，它会询问是否写入 `cornus-values.yaml` helm values 片段。每项都会在写入前询问 (写入文件、输出到标准输出、跳过)，并在已有文件时要求确认覆盖。

## 非交互式使用

非 TTY stdin 会针对脚本输入运行纯文本行提示，而不是报错，因此可以通过 here-document 驱动向导：

```sh
printf '1\n\n\n\n\n' | cornus --output plain setup   # 本地场景，使用所有默认值
```

每个提示都会打印其默认值，EOF 会**不保存便中止**。截断或错误的脚本会中止，而不会悄然写入错误的配置文件。真正的自动化应直接使用确定性的 [`cornus config set-context`](/zh/cli/config)。

## 与 `config set-context` 的关系

向导写入与 `cornus config` 相同的客户端配置文件，并在指引中打印与所创建配置文件等效的 `cornus config set-context …` 命令 (bearer token 会被隐藏)。向导能做的所有事都可以通过 `set-context` 非交互式完成；向导仅提供引导路径和服务器端设置步骤。

**另请参阅：** [cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)、[使用远程集群](/zh/guides/remote-clusters)。
