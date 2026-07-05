# 在容器中运行服务端

把 cornus 服务端本身作为容器运行在**它所管理的 docker 或 containerd 主机**上 — 这是 Kubernetes 上集群内 Helm 安装在主机后端上的对应做法。除了一个容器之外，主机上无需安装任何东西即可获得 cornus，而工作负载仍然落在主机自身的运行时上。

这与[远程 docker/containerd 主机](/zh/guides/remote-docker-hosts)不同: 那里服务端运行在 SSH 隧道的另一端，你从自己的机器连过去。这里服务端与运行时位于同一主机，只是恰好被容器化了。

## 为什么这些绑定很重要

cornus 交给容器运行时的每一个路径，都由该运行时在**主机**的文件系统中解析，而不是在 cornus 的文件系统中。直接运行在主机上的服务端永远不会察觉，因为两者是同一个。而容器化的服务端必须被告知两者如何对应，否则它交出去的路径在对面没有任何意义。

这种失败是无声的。运行时拿到找不到的路径后会直接创建它并照常启动工作负载: 你的挂载变成一个空目录，没有命令失败，任何日志里也不会出现任何信息。因此 cornus 拒绝猜测 — 能检测出对应关系时就检测，否则就提前说明。

## Docker

```sh
docker run -d --name cornus \
  --privileged \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest
```

- **套接字绑定**决定了目标是主机的 docker，而不是根本没有目标。
- 数据目录上的 **`:rshared`** 让 cornus 在容器内创建的挂载能够到达主机。没有它，[客户端本地挂载](/zh/guides/deploying-workloads#将-client-local-directory-mount-到-remote-workload-local-mount-经-9p-stream) (`--local-mount`) 会被拒绝并附带说明；其余功能一切正常。
- **`--privileged`** 用于在进程内构建镜像，以及为 `--local-mount` 执行内核 9P 挂载。只部署已构建镜像的服务端可以去掉它。

cornus 通过向守护进程询问它自己所在的容器来发现 `/srv/cornus` 与 `/var/lib/cornus` 的对应关系，因此无需额外配置。

### 访问工作负载

`cornus port-forward`、`cornus tunnel` 以及从服务端访问已发布端口，都会拨号到工作负载的容器 IP。位于自身网桥网络上的服务端没有到用户自定义网络上工作负载的路由，拨号会超时并给出原因说明。有两种解决办法:

- 在上面的 `docker run` 中加上 `--network host`，让服务端与主机对每个 docker 网络拥有相同的视图；或者
- 设置 `CORNUS_DOCKER_REMOTE=1` (以及 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`) ，改为通过每实例的 companion 访问工作负载。这会为每个副本多运行一个容器，因此除非你出于其他原因需要 companion，否则优先选择主机网络。

## containerd

::: warning
**containerd** 主机上的容器化 cornus 尚未完成。路径转换已经可用，但后端的 CNI 网络仍然在 cornus 所处的网络命名空间内部构建工作负载网络，因此服务端必须共享主机的命名空间 — 而且容器镜像目前还没有随附 CNI 插件二进制文件。在这两点都解决之前，请[直接在主机上](/zh/guides/remote-docker-hosts)运行 containerd 后端。
:::

它将会有的要求如下，preflight 已经会报告它们:

- `-v /run/containerd/containerd.sock:/run/containerd/containerd.sock`
- `-v /srv/cornus:/var/lib/cornus:rshared` — 这里是必需的，而非可选。containerd 后端在**每一次**部署中都会用到数据目录下的路径 (卷的实际存储、托管的 `/etc/hosts`、日志文件) ，因此当运行时看不到数据目录时，服务端会拒绝启动，而不是产出空的工作负载。
- `--network host`，用于 CNI 的底层连接。
- 容器内 `/opt/cni/bin` 或 `CORNUS_CNI_BIN_DIR` 下的 CNI 插件 (`bridge`、`portmap`、`host-local`、`loopback`) 。

## 在确定之前先检查

在你打算用来提供服务的镜像内部，用相同的挂载和环境运行 preflight。它执行的检查与 `cornus serve` 启动时完全相同，并在服务端会拒绝启动的配置下以非零状态退出:

```sh
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight
```

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

`--output json` 以单个对象给出相同的结果，可用于 CI 门禁。

运行中的服务端会报告相同的结论: 启动时的一行摘要和每个问题一条警告，以及客户端从 `GET /.cornus/v1/info` 读取的能力标志 — 这样 `cornus setup` 的验证可以在你依赖它之前就告诉你某个服务端无法实现 `--local-mount`。

## 自己声明对应关系

当 cornus 无法向运行时询问你的容器有哪些挂载时 (非 docker 运行时，或守护进程不会报告的容器) ，请显式声明这一对应关系:

```sh
-e CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus
```

多个键值对以逗号分隔；显式条目总是优先于检测到的条目，因此这也是纠正错误推断的方式。格式错误的值会导致启动失败，而不是被静默忽略。

## 哪些不会被转换

**你**在部署规格或 Compose 文件中写下的绑定挂载本身就是主机路径 — 打开它的是守护进程 — 因此会原样传递，与非容器化服务端上完全一样。`CORNUS_ALLOW_BIND_SOURCES` 同理: 这些前缀要按主机所见的样子书写。

只有 cornus 自己在其数据目录下准备的路径才会被转换。

## 关于 Docker-in-Docker 的说明

与它所驱动的守护进程**并列**容器化的 cornus (两者在同一个容器中，如 Docker-in-Docker 测试环境) 共享该守护进程的挂载命名空间，因此它的路径本就一致，不会进行任何转换。cornus 通过运行时是否确认自己创建了 cornus 所在的容器来区分这一情形与上文的情形，所以这两种形态无需不同的配置。
