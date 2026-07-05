# cornus serve

运行 cornus 服务器：OCI 镜像仓库、构建引擎和部署引擎全部位于一个进程中。

## 概要

```sh
cornus serve [flags]
```

## 说明

`cornus serve` 启动统一 HTTP 服务器，托管 `/v2/*`（OCI 镜像仓库）和 `/.cornus/v1/*`（构建、部署、exec 和 tunnel 端点）。它会监听至被中断（`Ctrl-C` 或 `SIGTERM`）。

镜像仓库 blob 和 manifest 经 `--storage` 所选存储后端持久化；未设置时，存储位于数据目录下。支持的 URL 形式参见[存储后端](/zh/reference/storage-backends)。

同时设置 `--tls-cert` 和 `--tls-key` 时，服务器使用 HTTPS。添加 `--tls-client-ca` 会启用 mutual TLS：经过验证的 client certificate 的 CommonName 成为调用方 identity，同时 client certificate 仍为可选。参见[认证与 TLS](/zh/topics/auth-and-tls)。

服务器支持的完整环境变量列表参见[服务器环境变量](/zh/reference/server-env-vars)。

## Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | `CORNUS_ADDR` | `:5000` | `/v2/*` 和 `/.cornus/v1/*` 的 HTTP 监听地址。 |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | 以 rootless 模式（user namespace）运行构建引擎。 |
| `--storage` | `CORNUS_STORAGE` | 数据目录 | 镜像仓库持久化后端：路径、`file://`、`mem://` 或 `s3://bucket?region=&endpoint=&path_style=`。参见[存储后端](/zh/reference/storage-backends)。 |
| `--otel` | `CORNUS_OTEL` | `false` | 通过标准 `OTEL_*` 环境变量启用 OpenTelemetry（trace/metric/log）。设置任意 `OTEL_*` exporter/endpoint 环境变量时也会隐式启用。 |
| `--tls-cert` | `CORNUS_TLS_CERT` | — | PEM certificate 文件；与 `--tls-key` 一同设置时提供 HTTPS。 |
| `--tls-key` | `CORNUS_TLS_KEY` | — | PEM private-key 文件；与 `--tls-cert` 一同设置时提供 HTTPS。 |
| `--tls-client-ca` | `CORNUS_TLS_CLIENT_CA` | — | 用于验证 client certificate（mTLS）的 PEM CA bundle。已验证 certificate 的 CommonName 成为调用方 identity；提交 certificate 仍是可选的。 |
| `--file-cache` | `CORNUS_FILE_CACHE` | `false` | 为不变的客户端本地挂载读取启用 server 按文件缓存。需要 `--file-cache-dir`。 |
| `--file-cache-dir` | `CORNUS_FILE_CACHE_DIR` | — | 存放文件缓存数据的必需目录。请使用专用卷。 |
| `--file-cache-chunk-size` | `CORNUS_FILE_CACHE_CHUNK_SIZE` | `1048576` | 文件缓存块大小 (bytes)。 |
| `--file-cache-max-bytes` | `CORNUS_FILE_CACHE_MAX_BYTES` | 无限制 | 由垃圾回收实施的文件缓存软大小上限。 |

## 示例

在默认地址提供服务，并将数据存于数据目录：

```sh
cornus serve
```

监听指定地址并将镜像仓库保存在内存：

```sh
cornus serve --addr :8080 --storage mem://
```

将镜像仓库持久化至 S3 兼容存储：

```sh
cornus serve --storage 's3://my-bucket?region=us-east-1&path_style=true'
```

使用 mutual TLS 提供 HTTPS：

```sh
cornus serve \
  --tls-cert server.crt \
  --tls-key server.key \
  --tls-client-ca clients-ca.pem
```

## 另请参阅

- [存储后端](/zh/reference/storage-backends)
- [服务器环境变量](/zh/reference/server-env-vars)
- [认证与 TLS](/zh/topics/auth-and-tls)
- [架构](/zh/architecture/)
