# 快速开始

Cornus 的主要目标是在本地 Kubernetes 集群内运行。本演练会使用预构建的 `cornus` 二进制文件和发布到 `ghcr.io/moriyoshi/cornus` 的多架构镜像，让您从零开始在单节点 [k3s](https://k3s.io/) 集群中运行一个工作负载。

无需克隆仓库、无需 Go 工具链、也无需 Docker：k3s 原生运行 containerd，Cornus 自身的集群内构建引擎构建示例镜像，`cornus compose` 则直接与服务器通信，因此整个流程中没有 Docker daemon。全部内容就是一个普通的 `compose.yaml` 和一条命令。

## 1. 安装 Cornus CLI

下载适用于您平台的预构建静态二进制文件并将其放入 `PATH`：

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

（arm64 平台请将 `amd64` 替换为 `arm64`。）容器镜像和从源码构建的说明请参见[安装](/zh/introduction/installation)。

## 2. 安装 k3s 和 Cornus，然后将 CLI 指向它

Cornus 通过固定 NodePort（`30500`）暴露，因此 CLI 和节点的 containerd 都能通过真实服务端点访问它；此处不依赖 `kubectl port-forward`。先将 k3s 的 containerd 配置为把 `localhost:30500` 视作纯 HTTP 镜像仓库（第 3 步构建的示例镜像将在此提供），再安装 k3s：

```sh
sudo mkdir -p /etc/rancher/k3s
sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
EOF
curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
```

现在安装 Cornus。随附的 manifest 是一个特权 StatefulSet（构建引擎需要此权限），包含 PVC、用于在集群内执行部署的 RBAC，以及 NodePort `Service`（`30500` -> container `5000`）；它已指向已发布的 GHCR 镜像，因此无需构建：节点 containerd 会直接从 GHCR 拉取 `ghcr.io/moriyoshi/cornus`。

```sh
kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml
# 或使用推荐的 Helm 路径——直接从 OCI 镜像仓库安装 chart：
#   helm install cornus oci://ghcr.io/moriyoshi/charts/cornus --version 0.1.0
kubectl rollout status statefulset/cornus --timeout=300s
```

确认服务器已启动，并可通过 NodePort 提供服务：

```sh
curl http://localhost:30500/healthz        # -> {"status":"ok"}
```

将其保存为默认连接配置文件，这样后续命令无需使用 `--server` 或 `CORNUS_HOST`：

```sh
cornus config set-context demo --server http://localhost:30500
cornus config use-context demo
```

有关连接远程或没有 ingress 的集群，请参见[连接配置](/zh/reference/connection-config)和[远程工作流](/zh/topics/remote-workflows)。

## 3. 使用 Compose 文件构建并部署

Cornus CLI 支持 Compose。创建一个普通的 `compose.yaml`——即 `docker compose` 会读取的同一文件——其中的 `build:` 部分使用 cache mount 和 secret mount，并发布一个端口：

```sh
mkdir -p demo
tee demo/Dockerfile >/dev/null <<'EOF'
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl busybox-extras
RUN --mount=type=secret,id=token \
    test -f /run/secrets/token && echo "secret present (not stored in image)"
RUN mkdir -p /www && echo 'cornus demo' > /www/index.html
CMD ["sh", "-c", "echo cornus demo && exec httpd -f -v -p 80 -h /www"]
EOF
echo -n s3cret > /tmp/token

tee demo/compose.yaml >/dev/null <<'EOF'
name: demo
services:
  web:
    build:
      context: .
      secrets:
        - token
    ports:
      - "8080:80"
secrets:
  token:
    file: /tmp/token
EOF
```

现在将它启动。一条命令会在集群中构建镜像（上下文和密钥会通过 9P-on-WebSocket 流向 Cornus pod，因此主机无需构建权限或 Docker），将镜像推送到 Cornus 的集群内镜像仓库、完成部署，并把发布的端口转发回您的机器：

```sh
cd demo
cornus compose up
```

服务会以 `localhost:30500/demo-web:latest` 构建并部署（镜像引用为 `<project>-<service>`，因此带有 `build:` 的服务不会另行设置 `image:`）。命令会在前台保持会话，流式传输任何客户端本地挂载并为工作负载的已发布端口建立 tunnel；它将显示 `forwarding 127.0.0.1:8080 -> :80`。示例容器在 `:80` 提供页面，所以尽管工作负载运行在集群中，`curl http://127.0.0.1:8080` 仍会返回 `cornus demo`。请保持该命令运行。

## 4. 检查并清理

在另一个终端执行（工作负载名称为 `<project>-<service>`）：

```sh
kubectl get deployment,service demo-web
kubectl logs deployment/demo-web           # -> cornus demo
cornus compose logs demo-web               # same logs, no kubectl needed
```

`cornus compose logs` 会流式输出各服务的日志；可添加 `--follow` 持续跟随，使用 `--tail`、`--since` 或 `-t`，并通过服务名称过滤（默认：全部）。

然后清理——按 Ctrl-C 停止前台 `cornus compose up` 以释放已发布端口的 tunnel，移除服务，并移除集群：

```sh
cornus compose down
/usr/local/bin/k3s-uninstall.sh
rm -rf demo /tmp/token
```

::: tip 变体
同一流程也适用于 k0s（单二进制 containerd）、kind（映射 node port，或在构建和部署之间加载镜像）、普通 Docker 主机（挂载 Docker socket 的 `dockerhost` 后端）和裸 containerd 主机（`CORNUS_DEPLOY_BACKEND=containerd`）。参见[部署后端](/zh/reference/deploy-backends)。
:::

## 直接使用引擎

`cornus compose up` 是构建引擎和部署引擎这两个原语上的便捷封装；当您需要显式控制、没有 Compose 文件，或要在中间插入步骤时，可直接使用它们：

```sh
# Build in the cluster and push to the registry. --builder streams the context and
# the secret over 9P-on-WebSocket to the Cornus pod, so the host needs no Docker
# and no build privileges:
cornus build --builder ws://localhost:30500/.cornus/v1/build/attach \
  -t localhost:30500/demo:v1 \
  --secret id=token,src=/tmp/token demo

curl http://localhost:30500/v2/demo/tags/list    # -> {"name":"demo","tags":["v1"]}

# Deploy from a native spec — the schema every higher-level surface translates
# into. It uses the current connection profile (an explicit --server overrides):
tee demo.yaml >/dev/null <<'EOF'
name: demo
image: localhost:30500/demo:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
EOF
cornus deploy -f demo.yaml
```

完整字段集请参见 [`cornus build`](/zh/cli/build)、[`cornus push`](/zh/cli/push)、[`cornus deploy`](/zh/cli/deploy) 和[部署 spec 参考](/zh/reference/deploy-spec)。

## 后续步骤

* [输出模式](/zh/guides/output-modes)——在 CI 中选择 `plain`，或为 agent 选择 `json`。
* [远程工作流](/zh/topics/remote-workflows)——将 CLI 指向远程集群。
* [隧道](/zh/guides/tunnels)——将工作负载公开暴露。
* [工作负载 hub](/zh/topics/hub)——按名称访问其他工作负载。
