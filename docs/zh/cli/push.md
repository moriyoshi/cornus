# cornus push

将镜像复制到镜像仓库（例如 Cornus 自己的镜像仓库）。

## 概要

```sh
cornus push <source> <dest> [flags]
```

## 说明

`cornus push` 将镜像复制到目标镜像仓库引用。source 可以是另一个镜像仓库引用，也可以是本地镜像 tarball（OCI 或 docker-archive）。若 `source` 是磁盘上的已有文件，它会作为 tarball 加载并推送；否则将被解析为镜像仓库引用并复制。

设置 `CORNUS_TOKEN` 时，cornus 使用该 bearer token 向目标镜像仓库认证。token 只作用于目标镜像仓库主机，因此跨镜像仓库复制绝不会把 cornus token 发送给无关的源镜像仓库。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `source`（位置参数） | — | 必需 | 源：镜像仓库引用或本地镜像 tarball 路径。 |
| `dest`（位置参数） | — | 必需 | 目标镜像仓库引用，例如 `localhost:5000/app:v1`。 |
| `--insecure` | — | `true` | 允许 HTTP（非 TLS）镜像仓库。 |

设置 `CORNUS_TOKEN` 后，它会提供用于向目标镜像仓库认证的 bearer token。

## 示例

将本地 OCI tarball 推送到 cornus 镜像仓库：

```sh
cornus push ./app.tar localhost:5000/app:v1
```

将镜像从一个镜像仓库复制到另一个：

```sh
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest
```

向受 token 保护的镜像仓库认证：

```sh
CORNUS_TOKEN=$(cornus token ...) cornus push ./app.tar registry.example.com/app:v1
```

## 另请参阅

- [`cornus build`](/zh/cli/build)
- [`cornus token`](/zh/cli/token)
- [安全与认证](/zh/guides/security)
