# 安装

Cornus 是单个 Go 二进制文件。同一个二进制文件既提供服务器 (`cornus serve`) ，也可作为客户端驱动服务器 (`cornus build`、`cornus deploy`、`cornus compose` 等) 。您可以安装预构建 CLI、运行已发布的容器镜像，或从源码构建。

::: warning 1.0 之前
Cornus 正在积极开发中，尚不保证 CLI 或 API 稳定。请固定发布产物的版本，并在升级前查看发布说明。
:::

## 预构建 CLI 二进制文件

每个 [GitHub Release](https://github.com/moriyoshi/cornus/releases) 都附带预构建二进制文件、`SHA256SUMS` 清单和无密钥 cosign 捆绑包 (`SHA256SUMS.bundle`):

| 平台 | 资产 |
| --- | --- |
| linux | `cornus-linux-amd64`、`cornus-linux-arm64` |
| macOS | `cornus-darwin-amd64`、`cornus-darwin-arm64` |
| Windows | `cornus-windows-amd64.exe` |

每个二进制文件都是自包含且开箱即用的:

- 内嵌 [`cornus web`](/zh/cli/web) 使用的 Web 应用；运行 UI 不需要安装 Node.js。
- 内嵌 OpenTelemetry Collector 和[内置可观测性存储](/zh/guides/observability)，因此 `cornus serve` 无需额外参数、也无需安装任何东西，即可记录工作负载的日志、追踪和指标。用 `cornus version --features` 查看某个二进制文件包含哪些功能。

linux 二进制文件是**完全静态**的，因此可在任何发行版上运行 —— 包括 Alpine 和 distroless。由于随附了可观测性存储，发布文件的下载体积比纯 CLI 大得多: 目前根据平台不同约为 86–107 MB。`--no-obs` 会在运行时禁用记录，但不会减小下载的二进制文件。

下载二进制文件和验证文件，验证已签名的校验和清单，然后将二进制文件放入 `PATH`:

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus-linux-amd64
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS.bundle
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity-regexp '^https://github\.com/moriyoshi/cornus/\.github/workflows/release\.yml@refs/tags/v[0-9].*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
grep ' cornus-linux-amd64$' SHA256SUMS | sha256sum -c -
mv cornus-linux-amd64 cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

对于 arm64，请将 `amd64` 替换为 `arm64`。

## 容器镜像

发布工作流会将预构建的多架构 (amd64/arm64) 镜像发布到 GHCR:

* `ghcr.io/moriyoshi/cornus:<version>` 发布于 `v*` tag (同时标记为 `latest` 和 `<major>.<minor>`)

镜像中包含第三方许可证归属信息。随附 Kubernetes manifest 和 Helm chart 部署的就是此镜像；它也可以直接作为本地 Docker 容器运行。

### 作为本地 Docker 容器运行

为进程内构建引擎以特权方式运行服务器，并挂载 Docker socket，使 `dockerhost` 部署后端可以在该主机上运行容器:

```sh
docker run -d --name cornus --privileged -p 5000:5000 \
  -v cornus-data:/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/moriyoshi/cornus:latest          # 服务器位于 http://localhost:5000
```

或者使用 Compose:

```yaml
services:
  cornus:
    image: ghcr.io/moriyoshi/cornus:latest
    container_name: cornus
    privileged: true
    ports:
      - "5000:5000"
    volumes:
      - cornus-data:/var/lib/cornus
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "cornus", "version"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  cornus-data:
```

进程内构建引擎需要 `privileged: true` (runc + overlayfs + user namespace) ；rootless 替代方案和完整权限模型请参见[权限说明](/zh/reference/deploy-backends)。请用持久卷支撑 `/var/lib/cornus`；参见[数据目录和持久化](/zh/reference/storage-backends)。

## 在 Kubernetes 上运行

将 Cornus 作为集群内 StatefulSet 部署，使镜像仓库 CAS 和构建缓存能够跨重启保留。

```sh
# 推荐: 从 OCI 镜像仓库安装 Helm (镜像 tag 跟随 chart 版本):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# 或使用原始 manifest / 已签出的 chart:
kubectl apply -f deploy/k8s/cornus.yaml
helm install cornus deploy/helm/cornus
```

- Manifest 包含 `StatefulSet` + PVC (数据位于 `/var/lib/cornus`) 、`Service`、`ServiceAccount` 和 `Role`/`RoleBinding` RBAC；manifest 和 chart 都设置 `CORNUS_DEPLOY_BACKEND=kubernetes` (Helm 值为 `deployBackend`) ，使服务器部署到自身 namespace。存活 / 就绪探针为 `/healthz` 和 `/readyz`。
- 值得了解的 chart 值: `storage` (`CORNUS_STORAGE`；留空则将 CAS 保存在每 pod PVC 上) 、`replicas` (多副本 hub 需要 `s3://` `storage` URL) 以及用于配置对应 JWT 验证环境变量的 `auth.jwt.*`。完整列表见 [Helm chart 值](/zh/reference/helm-values)参考。

::: tip
如需在全新单节点集群上完成 serve → build → deploy 的完整演练，请参见[快速开始](/zh/introduction/quick-start)。
:::

## 从源码构建

构建需要 Go 1.26。若要生成完全静态、可用于容器的二进制文件:

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo" -o cornus ./cmd/cornus
```

如需同时启用 Google Cloud Storage (`gs://`) 和 Azure Blob (`azblob://`) 镜像仓库存储后端，请添加 `cloudblob` 构建 tag (默认构建会对这些 scheme 返回清晰的“not supported in this build”错误) :

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
```

::: warning
进程内构建引擎仅支持 Linux，并会引入大量 BuildKit 依赖。只要 `go build` 可以运行，构建就能编译；但执行构建需要 root 或 rootless user-namespace 栈。镜像仓库和部署子系统无需特殊权限。权限说明参见[架构概览](/zh/architecture/)。
:::

## 后续步骤

* [快速开始](/zh/introduction/quick-start)——serve、构建并部署一个 Compose 项目。
* [Cornus 是什么？](/zh/introduction/what-is-cornus)——三个子系统及其协作方式。
