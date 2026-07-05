# 镜像仓库和存储

Cornus 内置一个由 content-addressable store 支撑的小型 OCI registry（`/v2/*`）。以下方法涵盖选择 storage backend、移入和移出 image，以及回收空间。完整 backend catalog 见[存储后端](/zh/reference/storage-backends)。

## 使用 filesystem storage 提供 registry

将 registry CAS 持久化到 local disk（`--storage` 未设置时的默认值）。

```sh
cornus serve --storage /var/lib/cornus     # or file:///var/lib/cornus
cornus serve                               # unset: store lives under the data dir
```

- Bare path 或 `file://path` 会在该 directory 下写入 CAS layout，并跨 restart 持久。
- 省略 `--storage` 时，store 位于 server data directory（`--data-dir` / `CORNUS_DATA`）。

**另请参阅：**[存储后端](/zh/reference/storage-backends)、[cornus serve](/zh/cli/serve)

## 使用临时 in-memory storage 提供服务

将整个 registry 保存在 process memory 中，适用于 test 和一次性 server。

```sh
cornus serve --storage mem://
```

- Server 停止时会丢失全部内容。不适合持久 registry。

**另请参阅：**[存储后端](/zh/reference/storage-backends)、[cornus serve](/zh/cli/serve)

## 使用 S3 或 S3-compatible storage 提供服务

将 CAS 存于 S3 bucket，以 native S3 multipart upload stream。

```sh
# AWS S3 (credentials from the standard AWS chain):
cornus serve --storage 's3://my-bucket?region=us-east-1'

# S3-compatible (MinIO and similar): override endpoint + path style:
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

- Query param：`region`、`endpoint`、`path_style`、显式 `access_key` / `secret_key`（否则使用标准 AWS credential chain）。
- 多个 replica 共享 `s3://` CAS 时，最多在一个 replica 上启用 `CORNUS_GC_INTERVAL`（见垃圾回收）。

**另请参阅：**[存储后端](/zh/reference/storage-backends)、[服务器环境变量](/zh/reference/server-env-vars)

## 使用 GCS 或 Azure Blob storage 提供服务

使用 Google Cloud Storage（`gs://`）或 Azure Blob（`azblob://`）作为 durable backend。

```sh
# These schemes require a -tags cloudblob build:
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
cornus serve --storage 'azblob://my-container'
```

- 默认 binary 对这些 scheme 返回清晰的“not supported in this build”错误；使用 `-tags cloudblob` build 启用。
- Credential 来自标准 Google / Azure credential chain。

**另请参阅：**[存储后端](/zh/reference/storage-backends)、[cornus serve](/zh/cli/serve)

## 向 registry push 和 pull image

用 `cornus push` 或标准 Docker tooling 将 image 移入 registry。

```sh
# Push a local OCI/docker-archive tarball, or copy a registry ref, with cornus:
cornus push ./app.tar localhost:5000/app:v1
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest

# Stock docker against the same registry:
docker push localhost:5000/app:v1
docker pull localhost:5000/app:v1
```

- `source` argument 是 disk file 时作为 tarball load；否则视为 registry reference。
- `cornus push --insecure`（默认 `true`）允许 plain-HTTP registry。启用 auth 时，设置 `CORNUS_TOKEN`，`cornus push` 会将其作为 registry bearer credential 发送。

**另请参阅：**[cornus push](/zh/cli/push)、[构建镜像](/zh/guides/building-images)

## 允许匿名 pull

允许未认证 client pull，同时 push 与 delete 仍要求 auth。

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

- 仅当其他 auth 已启用时有意义。它打开 `/v2/*` 下的 `GET` / `HEAD`，其他每个 verb 仍需 credential。
- `CORNUS_API_POLICY` 中显式 `pull` rule 会覆盖它（两者均设置时 startup warning）。

**另请参阅：**[保护服务器](/zh/guides/security)、[认证与 TLS](/zh/topics/auth-and-tls)

## 向 cluster runtime 声明 registry

告知 deploy target 应从哪个 registry host pull build image。

```sh
# host[:port], or https://host for a TLS registry:
CORNUS_ADVERTISE_REGISTRY=cornus.example:5000 cornus serve
```

- Server 在 `GET /.cornus/v1/info` 发布此 registry，deploy target 从中 pull；未设置时由 server 的访问方式派生。
- `CORNUS_ADVERTISE_URL` 是独立 knob：pod mount-agent / caretaker dial 回的 in-cluster cornus URL（Kubernetes backend 中 client-local mount 需要它）。

**另请参阅：**[服务器环境变量](/zh/reference/server-env-vars)、[远程集群](/zh/guides/remote-clusters)

## 使用外部 OCI registry，而非内置 registry

Push 到并从已有 registry deploy。

```sh
# Copy a build's output into any external registry:
CORNUS_TOKEN=$(cornus token issue --sub ci --hs256-secret "$SECRET") \
  cornus push ./app.tar registry.example.com/app:v1
```

- `cornus push` 可指向任意 OCI registry reference；bearer token 仅 scope 到 destination host，因此 cross-registry copy 不会向无关 source 泄露 token。
- Remote build 中，`CORNUS_REGISTRY` 为未含 registry 部分的 tag 设置 registry host。

**另请参阅：**[cornus push](/zh/cli/push)、[构建镜像](/zh/guides/building-images)

## 使用垃圾回收回收空间

对 CAS 执行 mark-and-sweep，清理 unreferenced blob 和 stale build cache。

```sh
# On demand: POST the GC endpoint on a running server.
curl -X POST http://localhost:5000/.cornus/v1/gc

# Periodically: run the same GC on an interval (Go duration).
CORNUS_GC_INTERVAL=1h cornus serve
```

- `POST /.cornus/v1/gc` 是 destructive endpoint；启用 auth 时受 `CORNUS_API_POLICY` 中 `gc` action 限制。
- 未设置 `CORNUS_GC_INTERVAL` 即禁用 scheduler；错误或非正值是 startup error。多个 replica 共享 `s3://` store 时，最多在一个 replica 上启用它。`CORNUS_GC_LEASE` 添加 Kubernetes Lease leader gate。

**另请参阅：**[服务器环境变量](/zh/reference/server-env-vars)、[存储后端](/zh/reference/storage-backends)
