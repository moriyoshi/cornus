# 临时预览环境

## 场景

对每个 pull request 或 feature branch，都需要一个一次性环境：构建 branch 镜像，在 cluster 中以每 PR 一个的名称部署，并给 reviewer 一个可点击的 public URL——可在 browser 或手机中访问，无需 VPN、kubeconfig 或永久 ingress。PR merge 或关闭时，所有内容都消失。Cornus 用一个 binary 完成整个循环：[`cornus build`](/zh/cli/build) 生成镜像，[`cornus deploy`](/zh/cli/deploy) 启动唯一命名的 workload，[`cornus tunnel`](/zh/cli/tunnel) 提供 public https URL。拆除只需一个 `--delete`。

## 使用的功能

- [`cornus build`](/zh/cli/build)——使用进程内 BuildKit engine 构建 branch image 并 push 到内置 registry。
- [`cornus deploy`](/zh/cli/deploy)——以每 PR 一个 `name` 应用 [deploy spec](/zh/reference/deploy-spec)。
- [`cornus tunnel`](/zh/cli/tunnel)——通过 hosted relay 将 workload port 暴露到 internet。
- [公共 tunnel](/zh/topics/tunnels)——shareable URL 的底层模型。

## 演练

以下步骤假定 connection profile 已选择 cluster server（见[远程集群](/zh/guides/remote-clusters)）；否则向每条 command 添加 `--server https://cornus.example.com`。

**1. 以 PR 命名全部内容。**一个变量同时限定 image tag 与 deployment name，因此 preview 不会冲突：

```sh
PR=123
IMAGE="registry.example:5000/app:pr-${PR}"
NAME="app-pr-${PR}"
```

**2. 构建并 push branch image。**Build 在 server 上运行（使用 `--builder` 或指定 builder 的 profile）；调用方不需要 Docker 或 build privilege：

```sh
cornus build -t "$IMAGE" .
```

**3. 以每 PR 一个名称部署。**生成带 PR-scoped name 和 image 的 spec，然后以 detached mode 应用，使环境可超出 command 生命周期：

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
YAML

cornus deploy -f preview.yaml --detach
```

**4. 发布 public URL。**`cornus tunnel` 请求 server host 指向 workload port 的 public tunnel，并打印 URL；即使 workload 从未发布该 port，也可到达：

```sh
cornus tunnel --authtoken "$NGROK_AUTHTOKEN" "$NAME" 80
# prints e.g. https://abcd-1234.ngrok-free.app  -- paste into the PR
```

将 URL 作为 PR comment 发布，reviewer 可点击访问。Command 保持运行到 `Ctrl-C`；对于 always-on preview，operator 可在 server 设置 default credential（`CORNUS_TUNNEL_AUTHTOKEN`），调用方无需提供 `--authtoken`。

**5. PR 关闭时全部拆除**——按名称删除 deployment（tunnel 随 command 结束而关闭）：

```sh
cornus deploy -f preview.yaml --delete
```

## 工作原理

每个阶段对应一个 server-side subsystem。`cornus build` 在 server 上运行 BuildKit engine，并将结果 push 至内置 registry，tag 使用 profile/server 声明的 registry，因此 deploy 的 `image` ref 无需另行运维 registry 即可解析。`cornus deploy --detach` 一次 POST spec 后退出，workload 无 client session 地保持运行；由于 `name` 是 PR-scoped 且 managed resource 带有其 label，apply 与 delete 均是 idempotent，不同 PR 的 preview 不会冲突。`cornus tunnel` 独立于 workload 的部署方式：cornus **server** 在进程内 host tunnel，并使用与 [`cornus port-forward`](/zh/cli/port-forward) 相同的 byte-bridge 将每条 inbound connection bridge 到 workload，因此在任意 backend（Docker host、containerd 或 Kubernetes）上均能到达 port，且无需 ingress。Tunnel credential 由 client 注入已认证 request，server 不会预先知晓。Backend（默认 `ngrok`，或 `ssh` / `cloudflare` / `tailscale`）在 server 侧选择；见[公共 tunnel](/zh/topics/tunnels)。

由于 deploy 是 detached，published port bind 在 server host 而非自动 forward 到本机——这正符合此处需求：public entry point 是 tunnel，而非 local listener。Detached deploy 还拒绝 client-local mount 与 client-sourced credential，因此按此方式构建的 preview 完全 self-contained。

## 变体

**使用 Compose project 而非 raw spec。**若 branch 提供 Compose file，将 project name 按 PR 限定，并 detached 启动，再用 `down` 拆除：

```sh
cornus compose -p "pr-${PR}" up --build -d
cornus tunnel "pr-${PR}-web" 80
# later:
cornus compose -p "pr-${PR}" down --volumes
```

**暴露 raw TCP**（database、gRPC endpoint）而非 HTTP：

```sh
cornus tunnel --proto tcp "$NAME" 5432
```

**使用 cluster ingress 而非 tunnel。**在已有 ingress controller 的 Kubernetes cluster 中，Cornus 可直接以 `networking.k8s.io/v1` Ingress 为每个 preview 提供 public URL——无需持续 relay process，URL 跨 detached deploy 存活（tunnel 仅在 command 存活时存在）。Operator 通过 Helm chart 的 `ingress` value 一次配置 cluster（设置 server 默认 `CORNUS_INGRESS_DOMAIN`、`CORNUS_INGRESS_CLASS`、`CORNUS_INGRESS_TLS_ISSUER`）：例如 wildcard preview domain `preview.example.com`、ingress class 和用于 HTTPS 的 cert-manager cluster-issuer。此时步骤 3 的 spec 只需启用 ingress，步骤 4 消失——host 从 deployment name 自动派生，无需计算每 PR URL：

```sh
cat > preview.yaml <<YAML
name: ${NAME}
image: ${IMAGE}
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true          # host auto-derived as <name>.<CORNUS_INGRESS_DOMAIN>
  tls: { }               # HTTPS via the server's default cluster-issuer
YAML

cornus deploy -f preview.yaml --detach
# reviewers browse https://app-pr-123.preview.example.com
```

Compose project 中，在 web service 添加裸 `x-cornus-ingress: {}` 即可；host 会按 project namespace 变为 `<service>.<project>.<domain>`（例如 `-p pr-123` 时为 `web.pr-123.preview.example.com`），大量 preview 可共用 base domain 而不冲突。在 file 顶部 project-level `x-cornus-ingress:` block 中放置 `domain:`、`class_name:` 等 shared override；每个 service 仍须独立 opt in。除非 operator 以 `CORNUS_INGRESS_ENFORCE_DOMAIN` 固定 domain，否则每个 server default 都可由 client per workload override。Workload 也可通过 `hosts:` 面向多个 name（token `@` 映射至 apex domain）。Ingress 仅 Kubernetes；Docker-host 或 containerd backend 会 warning 后忽略字段，file 保持 portable。拆除方式不变——`--delete` 移除 deployment，Kubernetes 随之 GC Ingress。按 cluster 选择：tunnel 无需 ingress controller，任意 backend 可用；ingress 是 cluster-native 并跨 command 存活，但需要 controller、wildcard DNS（HTTPS 还需 cert-issuer）。

**接入 CI。**完整 sequence 可脚本化且无需 daemon——build、deploy、tunnel——因此 pipeline 可在 `pull_request: opened` 创建 preview，并在 `closed` 时运行 `cornus deploy -f preview.yaml --delete`。Tunnel URL 会写到 stdout；捕获并发布回 PR 即可。

**另请参阅：**[构建镜像](/zh/guides/building-images) · [部署工作负载](/zh/guides/deploying-workloads) · [网络操作](/zh/guides/networking) · [隧道](/zh/guides/tunnels) · [Cookbook](/zh/cookbook/)
