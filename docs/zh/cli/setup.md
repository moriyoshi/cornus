# cornus setup

用于创建并验证连接配置文件 (即“上下文”) 以访问 cornus 服务器的交互式向导，随后会输出适合该场景的设置指引。它是 [`cornus config set-context`](/zh/cli/config) 的引导式前端，不引入新的配置文件语义。

## 概要

```sh
cornus setup
cornus setup --scenario local   # 跳过开头的场景选择器
```

## 说明

`cornus config set-context` 是一长串涵盖多个不同部署拓扑的标志。`cornus setup` 则会询问要配置哪种拓扑，只提出该拓扑所需的问题，写入上下文 (复用同一个客户端配置文件)，可选择测试连接，最后给出后续步骤清单以及等效的 `set-context` 命令。

在真实终端中，向导会呈现丰富的对话界面，下文的设置指引也会带有格式 (加粗标题、编号步骤、高亮命令) 。在管道、CI、`--output plain` 或设置了 `NO_COLOR` 时，它会回退到纯文本行提示和无格式文本，完全不输出转义序列 (请参阅[非交互式使用](#非交互式使用))。它拒绝 `--output json`，因为提示会破坏 NDJSON；脚本请使用 `cornus config set-context`。

### 导航

在任何问题处都可以返回或退出。

- **返回上一步** — 在丰富对话界面中按 `Esc` ⎋ 或 `Ctrl-D`，或在纯文本提示中输入 `<` 并按 `Enter` ⏎。从第一个问题返回会回到场景选择器；修改较早的答案只会重新询问依赖它的问题。在回答完所有问题前不会写入任何内容，因此返回始终是安全的。
- **取消向导** — 按 `Ctrl-C` ⌃C。在保存配置文件前，这不会改变配置；保存只在最后以一个原子步骤完成。

## 场景

第一个问题从以下选项中选择一个:

- **本地服务器** — 运行在本机的 `cornus serve` (纯 HTTP 回环)。
- **远程 Docker 主机 (SSH)** — 通过 SSH 隧道访问 docker 主机。
- **远程 containerd 主机 (SSH)** — 通过 SSH 隧道访问 containerd 主机。
- **远程无守护进程主机 (SSH)** — 访问服务端自行驱动 OCI runtime (`runc`/`crun`/`youki`) 、完全不使用守护进程的主机。参见 [`bare` 后端](/zh/reference/deploy-backends#bare)。
- **远程 Incus 主机 (SSH)** — 访问服务端将工作负载部署为 [Incus](https://linuxcontainers.org/incus/) 应用容器的主机。参见 [`incus` 后端](/zh/reference/deploy-backends#incus)。
- **Kubernetes (自动端口转发)** — 通过自动端口转发访问的集群内安装。向导会自动检测 cornus Service 和端口，无法检测时回退为手动输入。
- **Kubernetes (直接 URL)** — 通过 ingress URL 访问的集群内安装。
- **其他服务器 URL** — 已知 URL 的服务器。
- **Docker 主机 (在容器中运行服务端)** — 在此 docker 主机上把服务端本身作为容器运行。配置文件是普通的回环配置；这个场景真正提供的是那条 `docker run` 命令本身，因为绑定挂载才是全部难点，而写错一个会静默失败。参见[在容器中运行服务端](/zh/guides/server-in-a-container)。

四个 **SSH** 场景提出的问题完全相同，生成的配置文件类型也相同: 隧道与后端无关，不同的只是对端的服务端。选择哪一个决定的是向导展示的设置指引，以及生成的 systemd 单元所设置的 `CORNUS_DEPLOY_BACKEND`。

**远程 Docker 主机 (SSH)** 还会额外询问服务端是否作为**容器**运行在该主机上，这样你就无需在远程主机安装任何二进制文件。回答“是”后，指引会切换成 `ssh HOST 'docker run …'` 的形态，并询问主机数据目录，同时不再提供 systemd 单元——没有可供其启动的二进制文件，而挺过重启靠的是 `--restart unless-stopped`。两种方式下隧道与所保存的配置文件完全相同，不同的只是对端。参见[作为该主机上的容器](/zh/guides/server-setup#ssh-container)。

每个场景只会询问所需的内容 (端点或 SSH/Kubernetes 目标、TLS、认证以及可选的注册表主机覆盖)。高级传输选项 (mTLS、`via-server`、通用 conduit/SOCKS5 模式) 请参阅 [`cornus config set-context --help`](/zh/cli/config)。

### 预设

`--scenario NAME` 会跳过开头的场景选择器，直接从该场景的问题开始。名称按选择器顺序为 `local`、`ssh-docker`、`ssh-containerd`、`ssh-bare`、`ssh-incus`、`kube-port-forward`、`kube-url`、`url`、`docker-container`；未知名称会被拒绝，并列出全部有效名称。其余行为不变，只是没有可返回的选择器，因此从第一个问题返回会取消向导。

## 设置服务端

向导配置的是*客户端配置文件*，它从不安装或启动服务端。因此它的第二个问题会先于其他一切提出: 服务端是否已经存在。

> Is the cornus server already set up?

回答**否**，向导会立即打印所选场景的设置指引，然后继续提问。它以一行概要开头 (该形态归结成的那一条命令) ，随后是编号的细节: 前提条件、用于检查它们的 `cornus daemon preflight` 命令，以及 `cornus serve` 的调用方式。回答**是**则不会打印指引。

指引之所以放在提问*之前*，是因为后面的问题正是**关于**这套设置的: 服务器 URL、发布端口、主机数据目录，描述的都是指引刚刚告诉你如何运行的那台服务端。对于尚未问到的值，指引会展示向导自己的默认值，而那正是它接下来要提议的，因此除非你有意同时偏离两者，二者始终一致。

指引只显示一次。收尾清单只列出接下来该做什么: 在那里重复设置步骤，等于在讲解一台参数已被同一份清单写入磁盘的服务端该如何搭建。

该答案还会抑制三个针对未监听服务端不可能成功的步骤: Kubernetes 场景的 ingress 探测 (否则要白等两次超时) 、SSH 密钥注册，以及[连接测试](#验证)。每一项都会替换为服务端启动后应运行的命令。

仅在**本地服务器**场景中，回答“否”后还会再问一个问题:

> Which container runtime will this server drive?

可选 Docker、containerd、bare、Incus 或 Kubernetes。该答案完全不会进入保存的配置文件——部署后端是服务端的事，在那里用 `CORNUS_DEPLOY_BACKEND` 选择。之所以提这个问题，是为了让指引能说明各后端差异极大的真实前提条件: `bare` 需要 root 以及 OCI runtime 和 CNI plugin，`incus` 需要 incusd 6.3+ 并在 daemon 主机上安装 `skopeo` 和 `umoci`，而 `kubernetes` 这些都不需要——只需要一个 `KUBECONFIG` 能访问到的集群。其他场景不会提问，因为它们的名称本身已经说明涉及哪种 runtime。参见[部署后端](/zh/reference/deploy-backends)。

**尽管参考文档称 Kubernetes 为“仅 server / in-cluster”，这里仍然提供该选项。** 该限制针对的是*不带服务端的* [`cornus deploy`](/zh/cli/deploy)，它会警告并回退到 `dockerhost`；但 `cornus serve` **本身就是服务端**，而该后端会从 in-cluster 配置回退到常规的 `KUBECONFIG` / `~/.kube/config` 规则。因此在本机运行 `cornus serve` 并部署到 [k3s](https://k3s.io/)、kind、minikube 或远程集群是受支持的用法，它与上文两个 **Kubernetes** *场景*不同: 那两个配置的是访问运行在集群**内部**的 cornus 的客户端。

这种拓扑有其特有的陷阱，因此指引会指出它: 从服务器 registry 拉取所构建镜像的是集群节点自身，所以回环地址对它们毫无用处——请把 `CORNUS_ADVERTISE_REGISTRY` 设置为节点能访问的地址 (参见[构建镜像](/zh/guides/building-images)) 。随后指引还会给出另一种方案: Cornus 本来就以运行在集群**内部**为主，那样 registry 天然就是节点可访问的服务端点，这个陷阱根本不会出现。推荐的做法是 Helm:

```sh
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus
```

chart 带有版本，其镜像标签也跟随 chart 版本，因此一条命令就能得到彼此匹配的服务端与清单，无需自己处理版本固定。使用 raw 清单也可以，但必须固定到发布标签而不是分支；参见[安装](/zh/introduction/installation)，以及在单节点 k3s 集群上走完整个流程的[快速开始](/zh/introduction/quick-start)。

### SSH 目标

对于 **SSH** 场景，目标问题会列出 `~/.ssh/config` 中声明的 `Host` alias，并标注各自解析到的目标 (`ops@10.0.0.5:2222`)。`Host *` 这类通配符模式不会出现在列表中: 它们配置的是一类主机，而不是一个可连接的主机。直接输入目标始终作为最后一个选项保留，因为目标主机可能根本不在配置文件里；当没有可读的配置文件，或其中没有声明可用的 alias 时，这个问题就是普通的自由输入提示。

对于 SSH 主机和直接 URL 场景，身份验证方式有三种: **SSH 密钥**、**静态令牌**、**无**。SSH 密钥注册只在配置文件保存后运行，因此取消或注册失败都不会丢失配置文件。对于 SSH 主机场景，向导会先尝试通过已配置的 SSH 传输在远程主机上运行 `cornus auth enrollment-code`；否则，它会要求输入从服务器主机获取的代码。

对于两个 **Kubernetes** 场景，向导还会探测服务器公布的 ingress (`/.cornus/v1/info`)，并询问是否通过 SOCKS5 conduit 访问工作负载的 ingress 主机。建议的合理默认值是: 服务器公布 ingress controller 时为 **native** (隧道连接到检测到的 ingress controller)，只暴露 ingress 域名时为 **emulate** (带生成证书的客户端反向代理)，否则为 **off**。你的选择会写入配置文件的 `conduit.ingress` 块并选择 socks5 conduit。请参阅[Ingress](/zh/guides/ingress)。

## 验证

保存后，向导会询问是否测试连接: 它会完全按真实命令的方式解析配置文件 (包括任何端口转发)，并调用服务器的 `/.cornus/v1/info` 端点，将结果归类为可达、需要认证、连接被拒绝、TLS 问题、超时等，同时给出修复提示。验证不会使命令失败，配置文件无论如何都会保持已保存状态。如果你回答服务端尚未设置好，则根本不会提出连接测试，因为它必然失败。

## 产物

设置产物在最后一个问题之后、保存配置文件**之前**提出，这样它们是在你仍在搭建服务端的过程中到手，而不是事后才有。它们就是指引中那些命令的文件形态，由你的回答组装而成——这正是它们排在提问之后、而指引排在提问之前的原因。对于 SSH 场景，向导会询问是否为远程主机写入 `cornus.service` systemd 单元；对于 Kubernetes 场景，它会询问是否写入 `cornus-values.yaml` helm values 片段。**本地无守护进程**的服务端也会得到同一份单元，因为在 `bare` 上 cornus 就是工作负载的监督者: 从 shell 启动的服务端一旦退出，就不再对任何工作负载应用重启策略。其他本地后端把监督交给各自的 daemon，因此不会收到任何提议。每项都会在写入前询问 (写入文件、输出到标准输出、跳过)，并在已有文件时要求确认覆盖。

容器安装场景刻意**不生成任何文件**。该形态只需要 Docker，因此它的指引直接打印填入了你的回答 (主机数据目录与端口) 的 `docker run` 命令，而不是一份需要 Compose 才能使用的 Compose 文件，也不是一个你必须先读过才敢信任的 shell 脚本。

该 systemd 单元会带上场景对应的 `CORNUS_DEPLOY_BACKEND`，并以注释形式写明该后端的前提条件: `containerd` 和 `bare` 需要 root 与 `/opt/cni/bin`，`bare` 有 runtime 覆盖项，`incus` 有 socket 与 project 覆盖项以及 `skopeo` / `umoci` 要求。写成注释是因为这些前提条件都不会在单元启动时失败: 缺少它们的单元看上去一切正常，直到部署因几层之外的原因失败才会暴露。

## 非交互式使用

非 TTY stdin 会针对脚本输入运行纯文本行提示，而不是报错，因此可以通过 here-document 驱动向导:

```sh
printf '1\n\n\n\n\n\n' | cornus --output plain setup                 # 本地场景，使用所有默认值
printf '\n\n\n\n\n' | cornus --output plain setup --scenario local   # 同上，但无需回答场景选择
```

每个提示都会打印其默认值，EOF 会**不保存便中止**。截断或错误的脚本会中止，而不会悄然写入错误的配置文件。真正的自动化应直接使用确定性的 [`cornus config set-context`](/zh/cli/config)。

## 与 `config set-context` 的关系

向导写入与 `cornus config` 相同的客户端配置文件，并在指引中打印与所创建配置文件等效的 `cornus config set-context …` 命令 (bearer token 会被隐藏)。向导能做的所有事都可以通过 `set-context` 非交互式完成；向导仅提供引导路径和服务器端设置步骤。

**另请参阅**: [cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)、[使用远程集群](/zh/guides/remote-clusters)。
