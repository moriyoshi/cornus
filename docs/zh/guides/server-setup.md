# 设置服务端

每条 `cornus` 命令都要与服务端通信。本页为服务端可能采取的每种部署形态提供一份简短的操作手册: 它需要什么、用哪条命令启动，以及如何确认已经成功。[`cornus setup`](/zh/cli/setup) 会直接链接到你所选形态对应的小节。

这些是操作手册，不是参考文档。每一节末尾都有指向该主题完整说明页面的链接；当你需要详尽的 flag、值或能力清单时请循此前往。

## 该选哪种形态 {#which}

服务端只是一个进程。各形态之间不同的是它**在哪里运行**，以及它**驱动哪种 runtime** ([部署后端](/zh/reference/deploy-backends)) 。

| 你想要 | 形态 | `cornus setup` 场景 |
| --- | --- | --- |
| 以最少准备试用 Cornus | [本地，Docker](#local-docker) | `local` |
| 不用 Docker daemon | [本地，containerd](#local-containerd) 或 [bare](#local-bare) | `local` |
| 完全不用任何守护进程 | [本地，bare](#local-bare) | `local` |
| 使用 Incus instance | [本地，Incus](#local-incus) | `local` |
| 从本机部署到集群 | [本地，Kubernetes](#local-kubernetes) | `local` |
| 使用性能更强的构建 / 部署主机 | [通过 SSH 的远程主机](#ssh) | `ssh-*` |
| 让 Cornus 运行在它所部署的集群内 | [集群内](#in-cluster) | `kube-port-forward`、`kube-url` |
| 保持主机干净 | [作为容器](#in-a-container) | `docker-container` |
| 使用别人运维的服务端 | [无需设置](#existing) | `url` |

有两条规则适用于所有形态:

- **构建引擎需要特权。** 它使用 runc、overlayfs 和 user namespace，因此请以 root / 特权方式运行服务端，或使用 `cornus serve --rootless`。参见[权限模型](/zh/reference/deploy-backends#权限要求)。
- **决定之前先检查。** `cornus daemon preflight` 会执行与 `cornus serve` 启动时相同的主机检查，并在 `cornus serve` 会拒绝启动的配置下返回非零退出码。下面每份手册都会用到它。

## 本地服务端 {#local}

Cornus 运行在你的机器上。它的数据目录存放 registry CAS 和构建缓存——传入 `--data-dir` (或 `CORNUS_DATA`) 可让它们在重启后保留。

先获取二进制文件: [安装](/zh/introduction/installation)。

### Docker {#local-docker}

默认方式，也是准备工作最少的。

**需要:** Docker socket `/var/run/docker.sock`。

```sh
cornus daemon preflight                     # 先验证主机
cornus serve --data-dir ~/.local/share/cornus
```

**确认:** 服务端就绪时 `cornus health` 不输出内容且退出码为 0。

**详见:** [`dockerhost` 后端](/zh/reference/deploy-backends#dockerhost-默认)。

### containerd {#local-containerd}

不需要 dockerd，但仍然使用守护进程。

**需要:** root、containerd socket，以及 `/opt/cni/bin` 中的 CNI plugin (`bridge`、`portmap`、`host-local`、`loopback`) 。

```sh
sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=containerd cornus serve --data-dir /var/lib/cornus
```

**确认:** `cornus health`。

**详见:** [`containerd` 后端](/zh/reference/deploy-backends#containerd)。

### bare，无守护进程 {#local-bare}

Cornus 自行驱动 OCI runtime 并自任监督者——既不用 dockerd，也不用 containerd。

**需要:** root、`PATH` 上的 OCI runtime，以及同样的 CNI plugin。默认是 `runc`；`CORNUS_BARE_RUNTIME` 可选择 `crun`、`youki` 或 `runsc` (gVisor) 。

```sh
sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight
sudo CORNUS_DEPLOY_BACKEND=bare cornus serve --data-dir /var/lib/cornus
```

runtime 缺失会在启动时就以可操作的错误快速失败，而不是等到第一次部署。

::: warning 这种形态请用 systemd 运行
在 `bare` 上，**cornus 自己就是工作负载的监督者**——它等待每个容器的 PID 1 并自行应用重启策略 (`CORNUS_BARE_SHIM` 可以把这件事拆到每容器的 shim 里，但默认关闭) 。因此在终端里启动的服务端一旦退出，就会把工作负载的监督一并带走；而在重启后重新接管幸存者、在主机重启后重建工作负载的启动 reconcile，也只有 cornus 在运行时才会执行。让工作负载能挺过崩溃与重启的，正是 `Restart=on-failure` 和 `WantedBy=multi-user.target`。

其他后端把监督交给各自的 daemon 或集群，因此失去 cornus 失去的是 API 而不是工作负载——在那些后端上，前台运行的 `cornus serve` 仍是合理的开发循环。

`cornus setup` 会为这种形态提供匹配的 `cornus.service`，请直接采用，而不要自己拼装。
:::

**确认:** `cornus health`。

**详见:** [`bare` 后端](/zh/reference/deploy-backends#bare)。

### Incus {#local-incus}

工作负载成为 Incus 应用容器。

**需要:** incusd **6.3+** (更早的版本没有 OCI 支持) 、对其 socket 的访问权限，以及 **daemon 主机上** 的 `skopeo` 和 `umoci`——incusd 会调用它们来平坦化镜像，因此它们要装在 incusd 运行的主机上。

```sh
CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight
CORNUS_DEPLOY_BACKEND=incus cornus serve --data-dir ~/.local/share/cornus
```

`CORNUS_INCUS_SOCKET` (默认 `/var/lib/incus/unix.socket`) 和 `CORNUS_INCUS_PROJECT` (默认 `default`) 可覆盖目标。

**确认:** `cornus health`。

**详见:** [`incus` 后端](/zh/reference/deploy-backends#incus)。

### 从本机使用 Kubernetes {#local-kubernetes}

服务端在本地运行，部署到你的 kubeconfig 能访问的集群——[k3s](https://k3s.io/)、kind、minikube 或远程集群。完全不涉及本地容器 runtime。

**需要:** 一个可访问的集群 (`KUBECONFIG`，否则 `~/.kube/config`) ，以及在 `CORNUS_K8S_NAMESPACE` (默认 `default`) 中管理 Deployment 和 Service 的 RBAC 权限。

```sh
CORNUS_DEPLOY_BACKEND=kubernetes cornus daemon preflight
CORNUS_ADVERTISE_REGISTRY=192.0.2.10:5000 \
  CORNUS_DEPLOY_BACKEND=kubernetes cornus serve --data-dir ~/.local/share/cornus
```

::: warning 拉取镜像的是节点，不是你
这里的 `CORNUS_ADVERTISE_REGISTRY` 不是可选项。从这台服务端的 registry 拉取所构建镜像的是集群节点自身，因此 `127.0.0.1:5000` 之类的地址*在节点上*指向的是节点自己——于是每次部署都会因为拉不到那个躺在你机器上的镜像而失败。请把它设置为节点能访问的地址。
:::

Cornus 本来就以运行在集群**内部**为主，那样 registry 天然就是节点可访问的服务端点，这个问题根本不会出现。除非你确实需要服务端在本地，否则请优先选择[集群内](#in-cluster)。

**详见:** [`kubernetes` 后端](/zh/reference/deploy-backends#kubernetes-k8s)。

## 通过 SSH 的远程主机 {#ssh}

服务端运行在另一台机器上，你的 CLI 通过不绑定本地端口的 SSH 隧道访问它。它驱动哪种 runtime 是在**那一侧**用 `CORNUS_DEPLOY_BACKEND` 决定的——隧道本身与后端无关。

四者的步骤形状相同:

1. 在远程主机上安装 cornus 二进制文件 ([安装](/zh/introduction/installation)) 。
2. 满足该后端的前提条件 (见下) 。
3. 验证: `ssh HOST '<env> cornus daemon preflight'`。
4. 绑定到回环地址运行——隧道在该主机上出口，因此它自己的回环地址即可到达服务端: `ssh HOST '<env> cornus serve --addr 127.0.0.1:5000'`。
5. 配置你这一侧: `cornus setup --scenario ssh-<backend>`。

第 4 步值得用 systemd 单元而不是 shell 来做。`cornus setup` 会为你所选后端生成正确的 `cornus.service`，并把前提条件写成注释——请直接采用它，而不要自己拼装。

### Docker {#ssh-docker}

**主机需要:** Docker socket。**环境变量:** 无 (默认) 。

```sh
ssh HOST 'cornus daemon preflight'
cornus setup --scenario ssh-docker
```

### containerd {#ssh-containerd}

**主机需要:** root、containerd socket、`/opt/cni/bin` 中的 CNI plugin。**环境变量:** `CORNUS_DEPLOY_BACKEND=containerd`。

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=containerd cornus daemon preflight'
cornus setup --scenario ssh-containerd
```

### bare {#ssh-bare}

**主机需要:** root、`PATH` 上的 OCI runtime、CNI plugin。**环境变量:** `CORNUS_DEPLOY_BACKEND=bare`。

```sh
ssh HOST 'sudo CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight'
cornus setup --scenario ssh-bare
```

### Incus {#ssh-incus}

**主机需要:** incusd 6.3+、socket 访问权限、`skopeo` 和 `umoci`。**环境变量:** `CORNUS_DEPLOY_BACKEND=incus`。

```sh
ssh HOST 'CORNUS_DEPLOY_BACKEND=incus cornus daemon preflight'
cornus setup --scenario ssh-incus
```

### 作为该主机上的容器 {#ssh-container}

你不必在远程主机上安装二进制文件。在 Docker 主机上，服务端可以直接从发布的镜像运行，并通过同一条隧道访问——`cornus setup --scenario ssh-docker` 会询问“服务端是否作为容器运行在远程主机上”，并切换到这种形态。

**主机需要:** Docker，以及一个用作数据目录的主机目录。不需要 cornus 二进制文件，也不需要 systemd 单元。

```sh
# 先在那边检查绑定。
ssh HOST 'docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight'

ssh HOST 'docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000'
```

它发布到**远程主机的回环地址**，而那正是 SSH 隧道的出口——该主机的网络上不会暴露任何东西。这种形态不会提供 systemd 单元: 没有可供其启动的二进制文件，而重启后让服务端回来的是 `--restart unless-stopped`。

这些绑定的分量与本地情形相同，参见[作为容器](#in-a-container)与[在容器中运行服务端](/zh/guides/server-in-a-container)。

**四者共同的 registry 注意事项:** 如果该主机的部署目标无法从推导出的 registry 地址拉取，请设置 `--registry-host`。

**详见:** [通过 SSH 访问远程容器主机](/zh/guides/remote-docker-hosts)。

## 集群内 {#in-cluster}

这是 Cornus 设计时所围绕的形态: 服务端作为 StatefulSet 运行在它所部署的集群内，因此它的 registry 天然就是节点可访问的服务端点，构建缓存也能在重启后保留。

**需要:** 一个集群以及 `kubectl` / `helm`。你的机器上除 CLI 外别无所需。

```sh
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus
kubectl rollout status statefulset/cornus --timeout=300s
```

Helm 是推荐路径: chart 带有版本，其镜像标签也跟随 chart 版本，因此一条命令就能得到彼此匹配的服务端与清单。使用 raw 清单也可以，但必须固定到**发布标签**而绝不能是分支——它安装的是一个具有广泛 RBAC 的特权 StatefulSet。

然后把 CLI 指向它:

```sh
cornus setup --scenario kube-port-forward   # 自动端口转发，无需对外暴露
cornus setup --scenario kube-url            # 或通过 ingress URL 访问
```

**Registry 暴露方式:** NodePort registry 会自动通告节点地址；对 ClusterIP 或 ingress，请设置 `registry.advertiseHost` (或客户端的 `--registry-host`) 。`cornus setup` 会为你生成匹配的 `cornus-values.yaml`。

**详见:** [安装](/zh/introduction/installation)、[Helm chart 值](/zh/reference/helm-values)、[使用远程集群](/zh/guides/remote-clusters)，以及在单节点 k3s 集群上走完整个流程的[快速开始](/zh/introduction/quick-start)。

## 作为 docker 主机上的容器 {#in-a-container}

服务端本身作为容器运行在它所管理的 Docker 主机上。这里的难点完全在于绑定挂载，而写错了并不会在启动时失败——它会在部署时静默失败。

**需要:** Docker，以及一个用作数据目录的主机目录。仅此而已——不需要 Compose，也不会生成任何文件。

```sh
# 先在你将要运行的镜像里检查绑定。
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight

docker run -d --name cornus --privileged --restart unless-stopped \
  -p 127.0.0.1:5000:5000 \
  -e CORNUS_DATA=/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest serve --addr :5000
```

请**先**运行 preflight: 趁什么都还没跑起来时修正绑定要便宜得多。这三个选项各有各的用处——socket 绑定使它用的是主机的 docker 而不是无从谈起，`:rshared` 使 cornus 在容器内所做的挂载能够传到主机，而 `--privileged` 是进程内构建和内核 9P 挂载所必需的。

`cornus setup --scenario docker-container` 会询问主机数据目录和端口，并打印填入了你的回答的这条命令。

**确认:** `cornus health`。

**详见:** [在容器中运行服务端](/zh/guides/server-in-a-container)。

## 别人运维的服务端 {#existing}

无需设置。向运维它的人索取 URL 以及必要时的凭据，然后:

```sh
cornus setup --scenario url
```

如果它需要认证，向导会引导你注册 SSH 密钥或保存令牌。参见[安全与认证](/zh/guides/security)。

## 服务端启动之后 {#after}

```sh
cornus health                # 它在监听吗
cornus version               # CLI 能通过已配置的配置文件访问到它吗
cornus compose up            # 部署点什么
```

如果 `cornus health` 成功而 `cornus version` 失败，那么服务端没问题，有问题的是连接配置文件——请重新运行 [`cornus setup`](/zh/cli/setup)，或参见[连接配置](/zh/reference/connection-config)。

**另请参阅**: [cornus setup](/zh/cli/setup)、[部署后端](/zh/reference/deploy-backends)、[cornus serve](/zh/cli/serve)、[安全与认证](/zh/guides/security)。
