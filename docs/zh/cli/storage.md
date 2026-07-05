# cornus storage

报告 cornus 服务器的存储占用，且不做任何更改。

## 概要

```sh
cornus storage usage [flags]
```

## 说明

`cornus storage` 用于汇总服务器存储管理。目前它仅提供一个非破坏性报告；回收仍在服务器端进行（`POST /.cornus/v1/gc` 端点与周期性 GC 调度器）。

`cornus storage usage` 获取 `GET /.cornus/v1/storage` 并打印当前占用：注册表内容存储（blob 数量与总字节数），以及在启用逐文件块缓存时其占用。它是垃圾回收的只读对应物——不会删除或逐出任何内容。

该报告通过列举并 stat 每个注册表 blob 计算得出，因此对文件系统后端而言开销较小，但对 S3 之类的对象存储而言开销更大（每个 blob 一次 `HEAD`）。请将其视为偶尔执行的运维查询，而非需要在紧密循环中轮询的指标。

该命令通过所选连接配置（参见 [cornus config](/zh/cli/config)）解析服务器，因此 `--context`、token 和 TLS 均会生效；传入 `--server` 可为单次运行覆盖端点。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | 远程 cornus 服务器 URL（`http(s)://` 或 `ws(s)://`）。回退到所选连接配置。 |
| `--format` | — | `text` | 输出格式：`text`（易读）或 `json`（原始报告）。 |

JSON 形式包含以下字段（禁用块缓存时省略 `fileCache*`）：

| 字段 | 说明 |
| --- | --- |
| `casBlobs` | 注册表内容存储中的 blob 数量。纯重导出配置（无内容存储）下为零。 |
| `casBytes` | 这些 blob 的总字节数。 |
| `fileCacheBytes` | 逐文件块缓存的磁盘大小。 |
| `fileCacheFiles` | 块缓存文件数量。 |

## 示例

打印易读报告：

```sh
cornus storage usage
```

```
Registry CAS: 128 blobs, 3.4 GiB
Block cache:  12 files, 512.0 MiB
```

获取原始报告以便脚本处理：

```sh
cornus storage usage --format json
```

```json
{
  "casBlobs": 128,
  "casBytes": 3650722201,
  "fileCacheBytes": 536870912,
  "fileCacheFiles": 12
}
```

查询指定服务器：

```sh
cornus storage usage --server https://cornus.example.com
```

## 参见

- [cornus config](/zh/cli/config) —— 该命令解析时使用的连接配置。
- 垃圾回收在服务器端进行：参见服务器参考中的 `POST /.cornus/v1/gc` 端点与 `CORNUS_GC_INTERVAL` 周期性调度器。
