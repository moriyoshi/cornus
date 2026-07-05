# 服务器、镜像仓库和内容存储

一个 HTTP process 提供全部服务。单个 mux 将 registry 路由到 `/v2/*`，将 build 和 deploy API 路由到 `/.cornus/v1/*`，并将 liveness/readiness 路由到 `/healthz` 和 `/readyz`。由于 build engine 和 deploy backend 在首次使用时才 lazy 构建，operator 可运行 registry-only 或 deploy-only server，无需具备其他 subsystem 的前置条件。

## 运行防护

Server 是运行防护所在：

- **Readiness 真实有效。**`/readyz` 原子地切为 serving，并在 shutdown 时切回 503；`/healthz` 保持纯 liveness。
- **并发和串行化。**Build 在 `CORNUS_BUILD_CONCURRENCY` semaphore 下运行（默认 CPU 数）。给定 deployment name 的 apply 和 delete 由 per-name mutex 串行化，两个 caller 不能对同一 workload race。
- **Request size cap。**build-context tar 上限 2 GiB（`CORNUS_MAX_BUILD_CONTEXT_BYTES`），blob PUT 上限 10 GiB；超限返回 413 并 abort upload session。
- **In-band build failure。**Build 在发送 HTTP 200 后流式输出，因此 mid-stream failure 以 body 中 `BUILD FAILED:` trailer 到达。Client 必须扫描 stream，status code 本身不是事实来源。
- **Deploy-side stream error 会显露而非吞没。**Log、stat 和 archive download 在 backend 首个 output byte 时才写 200，因此任何输出前的 failure 返回真实 4xx/5xx error body。输出开始后的 error 写入 `X-Cornus-Stream-Error` HTTP trailer；Cornus client 在 EOF 后检查它，同时仍传递 partial byte。
- **Fail-closed config。**错误的 policy 环境（`CORNUS_API_POLICY`、`CORNUS_HUB_POLICY`、`CORNUS_HUB_REGISTER_POLICY`）是 hard startup error，绝不 fail-open。

Shutdown 会关闭 lazy-built engine 和 deploy backend，并释放 build engine data-dir lock。

## 镜像仓库

Registry 是自行实现的一组 OCI Distribution v1.1 handler，直接针对 persistent content-addressable store 编写——manifest 和 tag 跨 restart 存活，因此不能使用常见 in-memory registry library。支持 surface 是实用的 spec 子集：ping、blob HEAD/GET（支持 `Range`）、monolithic/chunked/cross-repo-mount blob upload、blob 和 manifest delete（manifest 仅按 digest）、manifest PUT/GET、paginated tag 和 `_catalog` list，以及 Referrers API。

内部拆分有意为之：content store 负责 sha256 addressing、digest verification、upload staging 和 manifest/tag/repo indexing；registry layer 则是位于其上的薄 OCI-protocol HTTP handler 集。

## 可插拔持久化

Persistence 是一个刻意很小的 plugin point。Backend 只实现极简 `ObjectStore`（`Get`/`Put`/`Stat`/`Delete`/`List`），*所有* registry semantic 只在该 interface 上方实现一次。content-addressed key layout——实际落入目录或 bucket 的内容——为：

```
blobs/sha256/<aa>/<hex>          blob content
repos/<repo>/manifests/<hex>     value = media type
repos/<repo>/tags/<tag>          value = digest
```

随附两种 backend。**Filesystem** backend 是 native、零 dependency 的默认值。**Bucket** backend 包装 gocloud bucket，提供 `mem://`、`s3://`，以及由于 driver 引入 Google/Azure SDK 而仅在 `-tags cloudblob` build 中提供的 `gs://` 和 `azblob://`。MinIO 等 S3-compatible server 获得带 custom endpoint 和 path-style addressing 的显式 client。使用 `cornus serve --storage <ref>` / `CORNUS_STORAGE` 选择 backend；空值默认使用 on-disk data-dir layout。配置参见[存储后端](/zh/reference/storage-backends)。

**Resumable upload 基于 capability。**实现 native uploading 的 backend 自己处理 OCI PATCH/PUT upload flow；其他 backend fallback 到 local staging。Filesystem backend 追加 session file，再通过 rename commit 至 blob path。S3 backend 更有代表性：每个 OCI PATCH 都是独立 HTTP request，因此所有 upload state 必须在 server 侧。Part 流入 S3 multipart upload，小型 JSON sidecar object 保存 part ETag、pending tail 和 running sha256 state，使本地 staging 无论 blob 大小均低于 5 MiB。

## 未命中回退：pull-through 镜像与本地 store 重新导出

`/v2/*` 的 manifest 或 blob 未命中时，可以回退到一个只读 source，而不是返回 404。
处理器先查本地 store，再查已配置的 source；这些 source 互斥。

- **Pull-through 镜像**（`CORNUS_REGISTRY_MIRROR=<host>`）。未命中从上游 OCI registry 获取并提供；
  设置 `CORNUS_REGISTRY_MIRROR_CACHE`（默认开启）时，还会把获取到的内容持久化到 store，使后续 pull 在本地解析。
- **本地 store 重新导出**（`CORNUS_REGISTRY_SOURCE=host-native`，在主机后端上是**默认值**）。当你针对本地 Docker 或 containerd 主机开发时，
  镜像通常已经在本地，因此在单独的 cornus registry 中再放一份副本是多余的。它把 `/v2/*` 变成该本地 store 的*视图*（按后端）。
  在 **`containerd`** 下，它以主机 containerd 的原生内容 store **读写**方式支撑 `/v2/*`：push 直接导入该 store（按 digest 的 blob + 一条镜像记录），
  pull 则读回，因此向 `/v2/*` push 的 `cornus build` 立即可部署 —— 无需 build worker 配置。
  在 **`dockerhost`** 下，它是本地 Docker daemon 的**只读**视图（未命中经由 `docker save` 提供）；
  同主机部署会为 daemon 已有的镜像跳过 registry 拉取，且由于传统 Docker 没有可写内容 store，
  向 `/v2/*` 的 push 会被拒绝 `405` —— `cornus build` 经由服务器路由，服务器把结果 `docker load` 进 daemon。

未设置 `--storage` 时，host-native **不保留单独的 CAS**：`_catalog`/标签列表只反映本地 store，生命周期由运行时负责。
docker daemon 视图的 `docker save` 会重新计算 digest（按 tag 拉取）；containerd 视图则保留它们。要保留传统的可 push registry，
设置 `CORNUS_REGISTRY_SOURCE=off` 或传入显式 `--storage`（CAS+source 的联合视图）；
已配置的 mirror 或非主机后端也会保留它。这些模式面向本地开发，而非高扇出的共享 registry。
见[复用本地镜像 store](/zh/reference/server-env-vars#reusing-a-local-image-store)。

## 垃圾回收和 crash-safety

Storage GC 是按需的 **mark-and-sweep**。Root 是每 repo 的 tag 与 manifest marker；mark phase 解析 manifest 和 index（config、layer、嵌套 `manifests[]`、`subject`）；sweep 删除不可达 blob。`POST /.cornus/v1/gc` 触发它（受 `gc` policy action 限制），并以 7 天 TTL 清理 build engine local cache。

设置 `CORNUS_GC_INTERVAL`（Go duration）还会在后台定期运行同一 GC：未设置即完全禁用，错误或非正值是 hard startup error——错误的 schedule 不能悄然禁用 reclaim。多个 replica 时，`CORNUS_GC_LEASE` 通过 Kubernetes `coordination.k8s.io` Lease 的 compare-and-swap 将每个 tick gate 住，replica 不会同时 sweep；拒绝 acquire 仅跳过该 tick（错过 sweep 优于并发 sweep）。过期 upload staging 会在 startup 时以 24 小时 TTL 清扫。运行 GC 请参见[镜像仓库指南](/zh/guides/registry)。

Crash 后不需要 repair pass，因为 manifest write 按依赖顺序发生——**blob、随后 manifest marker、再随后 tag**——因此 crash 最坏只会留下可由 GC reclaim 的 orphan，绝不会让 tag 指向缺失数据。

## 相关页面

- [镜像仓库和存储指南](/zh/guides/registry)——实际提供服务、声明与 GC。
- [存储后端](/zh/reference/storage-backends)——各 backend 配置。
- [服务器环境变量](/zh/reference/server-env-vars)——完整环境变量 surface。
- [cornus serve](/zh/cli/serve)——serve command。
