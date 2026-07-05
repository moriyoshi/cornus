# 在容器中运行服务端

把 cornus 服务端本身作为容器运行在**它所管理的主机运行时**上 — 这是 Kubernetes 上集群内 Helm 安装在主机后端上的对应做法。除了一个容器之外，主机上无需安装任何东西即可获得 cornus，而工作负载仍然落在主机自身的运行时上。

具体效果取决于后端，而这些差异并非表面上的。docker 和 containerd 都能自行配置: 两者都会向即将用于部署的运行时询问「我运行在哪个容器里」，并据此推导出自己的主机路径。bare 没有可询问的守护进程，本来也不需要路径转换; incus 则不会把 cornus 自己的任何路径交给运行时 — 对这两者而言，唯一的问题是服务端能否访问到自己的工作负载。下面按后端分别说明。

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

`cornus port-forward`、`cornus tunnel` 以及从服务端访问已发布端口，都会拨号到工作负载的容器 IP。docker 会丢弃两个不同网桥网络之间的流量，因此容器化的服务端本来没有到声明了 `networks:` 的工作负载的路由，反方向也同样没有。

cornus 会自行处理这一点: 在向用户自定义网络部署时，它先把 **自己的容器** 接入该网络，并在该部署 (以及该网络) 被拆除时断开。无需任何配置。接入用户自定义网络不会改动服务端自身的默认路由，因此它的出站连通性保持不变。

由于服务端随后成为工作负载所在网络的成员，docker 的内置 DNS 可以在该网络上按容器名解析到它 — 因此 caretaker companion 与工作负载遥测所拨号的 `CORNUS_ADVERTISE_URL` 可以直接写服务端的容器名 (`ws://cornus:5000`) 。

有两点副作用值得了解:

- **服务端会占用每个此类网络地址池中的一个地址。** 如果你把某个网络的 `ipam.config` 子网或 `ip_range` 正好按副本数来设置，现在就会少一个，多出来的那个副本会以守护进程的 "no available IPv4 addresses on this network's address pools" 启动失败。把它放宽一个即可。当 cornus 自己就是那个额外的占用者时，它会把这一说明附加到错误信息中。
- **重建服务端容器会丢失它的接入。** docker 的端点属于容器，因此它们在 `docker restart` 后仍然存在，但在升级所用的 `docker rm` 加 `docker run` 之后不会。cornus 会在下一次需要访问工作负载时按需重新接入，因此这是可以自愈的; 升级之后你可能会看到每个网络一行 `attached this cornus server's own container to a running workload's network` 日志。

有一种情况仍需你自行处理: 完全没有 `networks:` 的工作负载会落在默认网桥上，而 cornus 不会自动把自己接入默认网桥 — 接入默认网桥 **会** 移动容器的默认路由，悄悄改掉服务端自身的出站路径比它所修复的故障更糟。如果你的服务端容器也不在默认网桥上，`port-forward` 会报告缺少路由，并给出两种解决办法:

- 在上面的 `docker run` 中加上 `--network host`，让服务端与主机对每个 docker 网络拥有相同的视图；或者
- 设置 `CORNUS_DOCKER_REMOTE=1` (以及 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`) ，改为通过每实例的 companion 访问工作负载。这会为每个副本多运行一个容器，因此除非你出于其他原因需要 companion，否则优先选择主机网络。

## containerd

```sh
ctr run -d --net-host --privileged \
  --mount type=bind,src=/run/containerd/containerd.sock,dst=/run/containerd/containerd.sock,options=rbind:rw \
  --mount type=bind,src=/srv/cornus,dst=/var/lib/cornus,options=rbind:rw \
  --mount type=bind,src=/run/cornus,dst=/run/cornus,options=rbind:rw \
  --env CORNUS_DATA=/var/lib/cornus \
  --env CORNUS_DEPLOY_BACKEND=containerd \
  ghcr.io/moriyoshi/cornus:latest cornus \
  cornus serve --addr :5000
```

这里用 `ctr` (或 `nerdctl`) 而不是 `docker` 启动，这并非风格取舍。cornus 通过向即将用于部署的那个 containerd 询问「我运行在哪个容器里」来发现自己的挂载和网络模式 — 因此只有当 **正是那个 containerd 创建了服务端容器** 时，它才能找到自己。如果在 containerd 主机上用 docker 启动服务端，它会落在 docker 自己的 containerd (另一个套接字) 上，查询将一无所获; 此时 cornus 会如实报告，你改用 `CORNUS_HOST_PATH_MAP` 声明对应关系。

只要绑定了套接字，就无需任何路径配置: 数据目录在主机上的写法会被自动发现，与 docker 上完全一样。

上面四个绑定各有其理由，其中两个在没有预检时会无声失败:

- **containerd 套接字** 是自我探查得以进行的前提。没有它，cornus 无法知道自己是哪个容器，会假定自己的路径已经与 containerd 一致，从而交出只在容器内部有效的写法。containerd 会在主机上把每一个都新建为空: 没有数据的卷、缺失的托管 `/etc/hosts`，以及从未被暂存的日志 shim — 于是对一个完全健康的容器，`cornus logs` 什么也返回不了。
- **`/srv/cornus:/var/lib/cornus`** 在这里是必需的，而非可选。该后端在**每一次**部署中都会用到数据目录下的路径 (卷的实际存储、托管的 `/etc/hosts`、日志文件) ，因此当运行时看不到数据目录时，服务端会拒绝启动，而不是产出空的工作负载。
- **`/run/cornus`** 是 cornus 固定每个实例网络命名空间并把该路径交给 containerd 的地方，而 containerd 的 shim 会在*主机*的挂载命名空间中重新打开它。`/run` 是容器私有的，所以没有这个绑定，每一次部署都会失败 — 而且失败得很晚: 在镜像拉取之后，也在原本健康的部署已被拆除之后。预检会以 `netns-host-visible` 报告这一点并拒绝启动。
- **`--net-host`** 让 CNI 网桥和端口发布的 NAT 规则建立在主机上，而不是服务端容器内部。没有它，部署会**成功**并报告端口已发布，而主机在这些端口上什么也看不到; 为避免这种情况，预检会拒绝启动。如果你仍然想这样运行 — 无论哪种情况，服务端自身的 `port-forward` 和 `tunnel` 都能访问到工作负载 — 设置 `CORNUS_HOST_NETWORK=0` 表示已知悉，服务端会带着一条警告启动。

发布的镜像已在 `/opt/cni/bin` 内置 CNI 插件 (`bridge`、`portmap`、`host-local`、`loopback`) 以及它们会调用的 `iptables`，因此无需另行安装。若你想自备插件，用 `CORNUS_CNI_BIN_DIR` 指向别处。

## bare

`bare` 与 containerd 共用 CNI 网络 — cornus fork 的是同一批插件，所以网桥和端口发布的 NAT 规则都建立在 cornus 自己的网络命名空间里 — 但 containerd 那一节的其他内容都不适用于它。它无守护进程，因此没有套接字要绑定; 它的 OCI 运行时是 cornus 自己的子进程，因此共享 cornus 的挂载命名空间，路径不可能出现分歧: 数据目录和 netns 目录都不需要绑定。

于是只剩下一个影响: 已发布端口。用 `--network host` 运行服务端容器，其行为就与直接跑在主机上一致。不加它时，`ports:` 在服务端容器内部实现，主机在那里什么也看不到，而服务端自身的 `port-forward` 和 `tunnel` 仍然可用。cornus 无法检测你属于哪种情况 (没有可询问的守护进程) ，因此会报告无法判定; `CORNUS_HOST_NETWORK=1` 或 `=0` 可以明确指定。

## incus

Incus 实例由 incusd 接入网络，位于主机网络命名空间中 incusd 自己的网桥上。服务端所处的位置决定了它能否访问到这些实例，受支持的答案有两种。

**作为 incus 实例**运行在同一守护进程上，是完全不存在路由问题的做法，也是不需要第二个容器运行时的做法: 服务端与它的工作负载一同位于 incusd 的网桥上，因此既不需要主机网络也不需要 companion 就能访问它们。cornus 会自行识别这一点 — 它会认出自己所在的实例，预检以 ok 报告 `workload-routing` 并指出实例名。

唯一需要注意的是如何暴露守护进程套接字。请用 **proxy** 设备而不是 disk 设备: 在非特权实例内部，disk 设备会被 id 映射成 `nobody`，cornus 无法连接。另外，发布路径的父目录必须在镜像中已经存在 — 监听端在实例启动时创建，而 `/var/lib/incus` 在大多数镜像中并不存在，会以 `bind: no such file or directory` 失败:

```sh
incus config device add cornus incusd proxy \
  listen=unix:/tmp/incus-daemon.sock \
  connect=unix:/var/lib/incus/unix.socket \
  bind=instance mode=0660
incus config set cornus environment.CORNUS_INCUS_SOCKET=/tmp/incus-daemon.sock
```

`connect` 是主机上的套接字，`listen` 是它在实例内部的呈现位置，而 `CORNUS_INCUS_SOCKET` 随后就指向后者。

除此之外没有任何要绑定的东西: 该后端不会把自己的任何路径交给 incusd，因此既没有要转换的东西，也没有需要让主机可见的数据目录。

**与 incusd 并列**运行在同一主机上的容器里同样可行，只需要守护进程的套接字和主机网络。用哪个容器运行时都可以 — 下面的选项对 `podman run` 完全相同:

```sh
docker run -d --name cornus --network host \
  -p 5000:5000 \
  -v /var/lib/incus/unix.socket:/var/lib/incus/unix.socket \
  -e CORNUS_DEPLOY_BACKEND=incus \
  ghcr.io/moriyoshi/cornus:latest
```

给它一条通往 incus 网桥的路由，靠的就是 `--network host`。没有它，服务端没有路由，也无法获得一条 — 上面 docker 的自我接入在这里没有对应做法，因为 cornus 容器不是一个 Incus 实例 — 于是 `port-forward`、`tunnel` 以及 caretaker 的回连都无法访问到工作负载。预检会以 `workload-routing` 说明这一点，失败的拨号也会指名同一原因，而不是留给你一个光秃秃的超时。部署本身不受影响，`ports:` 映射仍会在主机上发布，因为 incus 用一个在守护进程 (而非 cornus) 命名空间中监听的 proxy 设备来实现它。

如果两者都不合适，设置 `CORNUS_INCUS_REMOTE=1` (以及 `CORNUS_AGENT_IMAGE` 和 `CORNUS_ADVERTISE_URL`) 可改为通过每实例的 companion 访问每个实例。这会为每个副本多运行一个实例，因此除非你出于其他原因需要 companion，否则优先选择上面两种做法。

三者都不设置时，拨号会失败，但 cornus 会指明原因，而不是留给你一个光秃秃的超时。

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

当 cornus 无法向运行时询问你的容器有哪些挂载时 (没有自我探查机制的运行时 (`bare`) ，或守护进程不会报告的容器 — 例如在 containerd 主机上由 docker 启动的服务端) ，请显式声明这一对应关系:

```sh
-e CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus
```

多个键值对以逗号分隔；显式条目总是优先于检测到的条目，因此这也是纠正错误推断的方式。格式错误的值会导致启动失败，而不是被静默忽略。

## 哪些不会被转换

**你**在部署规格或 Compose 文件中写下的绑定挂载本身就是主机路径 — 打开它的是守护进程 — 因此会原样传递，与非容器化服务端上完全一样。`CORNUS_ALLOW_BIND_SOURCES` 同理: 这些前缀要按主机所见的样子书写。

只有 cornus 自己在其数据目录下准备的路径才会被转换。

## 关于 Docker-in-Docker 的说明

与它所驱动的守护进程**并列**容器化的 cornus (两者在同一个容器中，如 Docker-in-Docker 测试环境) 共享该守护进程的挂载命名空间，因此它的路径本就一致，不会进行任何转换。cornus 通过运行时是否确认自己创建了 cornus 所在的容器来区分这一情形与上文的情形，所以这两种形态无需不同的配置。
