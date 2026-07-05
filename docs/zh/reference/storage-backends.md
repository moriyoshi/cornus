# 镜像仓库存储后端

cornus 镜像仓库是一个构建在 sha256 **内容寻址存储**（CAS）之上的小型 OCI 镜像仓库（`/v2/*`）：每个 blob——镜像 layer、config 和 manifest——都以自身字节的 digest 作为 key，因此相同内容只存储一次，并可通过重新 hash 验证完整性。Manifest 和 tag 是指向该存储的轻量引用。

CAS 的*存放位置*可插拔，通过 [`cornus serve`](/zh/cli/serve) 的 `--storage` flag（环境变量 `CORNUS_STORAGE`）选择。默认是在服务器数据目录下的 filesystem layout。

所有后端上的上传均**可恢复**（S3 后端以原生 multipart upload 流式传输）。空间按需回收——`POST /.cornus/v1/gc` 对 CAS 执行 mark-and-sweep 并清理过期 build cache——也可经 `CORNUS_GC_INTERVAL` 定期回收（参见[服务器环境变量](/zh/reference/server-env-vars)）。

## 后端

| `--storage` 值 | 后端 | 持久性 | 说明 |
| --- | --- | --- | --- |
| 路径或 `file://path` | Filesystem（默认） | 持久化，位于本地磁盘 | 未设置 `--storage` 时的默认值：数据目录下的 filesystem layout。 |
| `mem://` | In-memory | 临时 | 重启时丢失。适合测试和一次性服务器。 |
| `s3://bucket?…` | AWS S3 / S3-compatible | 持久化，对象存储 | 原生 multipart upload。query param 可调整 region、endpoint 和 path style。 |
| `gs://bucket` | Google Cloud Storage | 持久化，对象存储 | 需要 `-tags cloudblob` 构建（见下文）。 |
| `azblob://container` | Azure Blob Storage | 持久化，对象存储 | 需要 `-tags cloudblob` 构建（见下文）。 |

```sh
cornus serve --storage /var/lib/cornus                       # filesystem (default)
cornus serve --storage mem://                                # in-memory (ephemeral)
cornus serve --storage 's3://my-bucket?region=us-east-1'     # AWS S3
```

## Filesystem

默认后端。传入裸路径（或 `file://path`）后，镜像仓库会在其下写入 CAS layout。完全省略 `--storage` 时，存储位于服务器数据目录下。

## In-memory

`mem://` 将完整 CAS 保留在进程内存中。它是临时的——服务器停止时所有内容都会丢失——因此适合测试和短生命周期服务器，而非持久镜像仓库。

> 在主机后端上，`/v2/*` [默认重新导出本地 Docker/containerd store](/zh/reference/server-env-vars#reusing-a-local-image-store)（`CORNUS_REGISTRY_SOURCE=host-native`），并**完全不保留内容 store** —— 连内存版也没有。传入 `--storage` 可在重新导出之下叠加一个 CAS（联合视图），或设置 `CORNUS_REGISTRY_SOURCE=off` 获得传统持久 registry。

## S3 和 S3-compatible

`s3://bucket` 将 CAS 存入 S3 bucket，并以原生 S3 multipart upload 流式传输上传内容。query parameter 用于配置连接：

| Param | 含义 |
| --- | --- |
| `region` | bucket 所在 AWS region。 |
| `endpoint` | 覆盖 S3 endpoint（用于 MinIO 等 S3-compatible service）。 |
| `path_style` | 使用 path-style addressing 时设为 `true`（许多 S3-compatible service 需要）。 |
| `access_key` / `secret_key` | 显式 credential（否则使用标准 AWS credential chain）。 |

```sh
# S3-compatible (MinIO, and similar): override endpoint + path-style
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

多个 replica 共享一个 `s3://` CAS 时，每个 replica 的 interval GC 相互之间不协调；在协调 GC 出现前，最多在一个 replica 上启用 `CORNUS_GC_INTERVAL`（或依赖按需 `POST /.cornus/v1/gc`）。

## Google Cloud Storage 和 Azure Blob（`-tags cloudblob`）

`gs://`（GCS）和 `azblob://`（Azure Blob）经 gocloud blob abstraction 工作，但驱动会引入 Google/Azure SDK。为使默认二进制保持精简，它们**位于 build tag 后**：使用 `-tags cloudblob` 构建以启用。默认构建对这些 scheme 返回明确的“not supported in this build”错误。

```sh
# Enable the Google Cloud Storage / Azure Blob backends:
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
```

`s3://` / `gs://` / `azblob://` 的 credential 来自标准 cloud credential chain。

## 数据目录和持久化

无论选择哪种 CAS 后端，服务器都会将 working state——filesystem CAS（当 `--storage` 为路径或未设置时）、进行中的 upload 和 build cache——放在**数据目录**下，由 `--data-dir`（环境变量 `CORNUS_DATA`）设置。

```sh
cornus serve --data-dir /var/lib/cornus       # or CORNUS_DATA=/var/lib/cornus
```

- 要使容器重启后仍保留数据，请使用持久卷支撑数据目录：Docker named volume 或 PVC（随附 StatefulSet 使用 `volumeClaimTemplates`，挂载 `/var/lib/cornus`）。
- 当 `--storage` 指向对象存储（`s3://`、`gs://`、`azblob://`）时，CAS 位于该 bucket 而非数据目录，因此持久 blob storage 不再依赖本地卷；但数据目录仍保存 build cache 和 upload。

## 另请参阅

- [`cornus serve`](/zh/cli/serve)——`--storage` flag 及其余服务器界面。
- [服务器环境变量](/zh/reference/server-env-vars)——`CORNUS_STORAGE`、`CORNUS_GC_INTERVAL` 和相关设置。
