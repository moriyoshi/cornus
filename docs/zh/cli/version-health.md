# cornus version / cornus health

打印 cornus 版本，或探测运行中服务器的健康端点。

## cornus version

打印 cornus 版本。

### 概要

```sh
cornus version
```

### 说明

`cornus version` 打印二进制文件的版本字符串。可在构建时通过 `-ldflags "-X main.version=..."` 覆盖，默认值为 `dev`。

### 示例

```sh
cornus version
```

## cornus health

探测 cornus 服务器的 `/healthz` 端点；若不健康则以非零状态退出。

### 概要

```sh
cornus health [flags]
```

### 说明

`cornus health` 在 5 秒超时时间内向 `http://<addr>/healthz` 发起 HTTP `GET`；除非服务器返回 `200 OK`，否则以非零状态退出。它专为容器 healthcheck 而设，因此镜像内无需额外工具（如 `curl`）。

### Flag

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:5000` | 要探测的服务器地址。 |

### 示例

探测默认本地地址：

```sh
cornus health
```

探测指定地址：

```sh
cornus health --addr 127.0.0.1:8080
```

作为容器 healthcheck 使用（Dockerfile）：

```dockerfile
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```
