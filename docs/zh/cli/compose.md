# cornus compose

兼容 Docker Compose 的 client，经 `/.cornus/v1/*` endpoint 将 Compose command 重定向到运行中的 cornus server。

## 概要

```sh
cornus compose [group flags] <subcommand> [flags]
```

## 说明

`cornus compose` 镜像 `docker compose`: 它加载 Compose project (或 devcontainer definition) ，再针对 cornus server 构建、部署并管理 service。可将 `cornus compose` alias 为 `docker-compose` 以直接替换使用，或者使用 [`cornus daemon docker`](/zh/cli/daemon) 让标准 `docker` / `docker compose` 工作。两个 CLI 不完全一致之处——cornus 无法遵守的 flag，或在这里含义不同的 flag——参见 [Docker Compose 兼容性](#docker-compose-compatibility)。

Project source 是 Compose file 或 devcontainer。Compose file discovery 在 working directory 查找 `compose.yaml`、`compose.yml`、`docker-compose.yaml` 或 `docker-compose.yml`。给出 `--devcontainer`、`-f` 指向 `devcontainer.json`，或未找到 Compose file 但可发现 `.devcontainer/devcontainer.json` (或 `.devcontainer.json`) 时使用 devcontainer。混合 repo 中 Compose file 始终优先。

Server connection 由 `--host` 解析，否则使用 selected connection profile，再否则 `http://localhost:5000`。构建镜像的 tag 和 deploy pull ref 使用以下顺序解析 registry: `--registry` / `CORNUS_REGISTRY` / profile，随后 server-advertised host (`GET /.cornus/v1/info`) ，最后 endpoint host。产生的 deployment shape 见[Deploy spec 参考](/zh/reference/deploy-spec)。

## Group flag

这些 flag 位于 `compose` group，并适用于每个 subcommand。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-f`, `--file` | — | discovery | Compose file，可重复。默认 working directory 中的 `compose.yaml` / `docker-compose.yml`。 |
| `--env-file` | — | `.env` | 用于 variable interpolation 的 env file，替换默认 `.env` discovery。可重复；后者获胜；process environment 仍优先。 |
| `--profile` | `COMPOSE_PROFILES` | — | 激活给定 profile 的 service (Compose `profiles:`) 。可重复；也遵循 `COMPOSE_PROFILES`。 |
| `--devcontainer` | — | — | `devcontainer.json` 文件路径，或用于查找 `.devcontainer/devcontainer.json` 的目录。覆盖 Compose-file discovery。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | 目录名 | Project name (默认 Compose file directory name) 。 |
| `-H`, `--host` | `CORNUS_HOST` | `http://localhost:5000` | cornus server endpoint。回退到 selected connection profile，再到默认值。 |
| `--registry` | `CORNUS_REGISTRY` | 派生 | 构建镜像 tag 和 deploy pull ref 所用 registry `host[:port]`。覆盖 profile 和 server-advertised 值；空时从 server、再从 endpoint host 派生。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | 经 cornus server proxy 路由 log 和 auto-forwarded port，而非使用 kubeconfig 直接连接 pod (仅 cluster profile) 。`--no-via-server` 强制直接路径。 |

### Devcontainer 支持

Project 来自 devcontainer definition (`.devcontainer/devcontainer.json`) 时，`cornus compose` 运行其 lifecycle command: `initializeCommand` 在任意 container 创建前于 host 运行；per-service `postCreate` / `postStart` / `postAttach` hook 随 container 启动运行。普通 Compose service 没有 lifecycle hook。

### 交互 shell 候选

`x-cornus-shells:` 按优先顺序列出某个 service 的 image 所拥有的交互 shell。[`cornus web`](/zh/cli/web#终端-shell-探测) 的终端会读取它，并在自己的候选列表之前进行探测，因此镜像里带了不常见 shell 的 service 无需任何浏览器端配置就能用它打开。

```yaml
services:
  api:
    image: myorg/api
    x-cornus-shells:
      - /bin/bash
      - /bin/busybox sh
```

每个条目是一个命令**字符串**，而不是预先切分好的参数列表，切分方式与 `command:` 和 `entrypoint:` 相同——所以 `/bin/busybox sh` 是一个条目。只有一个候选时也接受裸字符串 (`x-cornus-shells: /bin/bash`)。

project 级别的 `x-cornus-shells:` block 为所有未自行声明的 service 提供默认值; service 上的列表是整体覆盖而不是追加，因为这个列表本身就是优先顺序。

它不改变部署的任何内容: 没有任何部署 backend 读取它，它也不属于 backend 用来与运行中 container 比对的 spec，因此编辑它永远不会触发重新创建。

### Provider service

service 可以将其生命周期委托给外部 provider plugin，而不是作为 container 构建/拉取并运行 (compose-spec 的 `provider:`) 。这样的 service 指定 plugin 的 `type`，并向其传入 provider 专属的 `options`:

```yaml
services:
  database:
    provider:
      type: awesomecloud
      options:
        type: mysql
        version: "8"
  app:
    image: my/app
    depends_on:
      - database
```

- **发现。** 对于 `type: awesomecloud`，若 `PATH` 上存在，cornus 运行 Docker CLI plugin `docker-awesomecloud`，否则运行名为 `awesomecloud` 的二进制。plugin 在运行 `cornus compose` 的机器上执行，而非 server 上。
- **生命周期。** `up` 时，cornus 以 `<plugin> compose --project-name=<project> up [--key=value ...] <service>` 调用 plugin，每个 `options` 条目作为 `--key=value` flag 传入 (list 值展开为重复的 flag) 。`down` 时以 `down` 同样调用。plugin 应当是幂等的。
- **环境变量注入。** plugin 在 stdout 上报告环境变量 (`setenv KEY=VALUE` 协议) 。每个变量以大写的 provider service 名为前缀，暴露给 `depends_on` 该 provider 的 service——因此上面的 `database` provider 会向 `app` 提供 `DATABASE_URL`、`DATABASE_TOKEN` 等。`rawsetenv` 变量不加前缀暴露给依赖方。名称冲突时，依赖方自身的 `environment:` 优先。
- **生命周期命令。** `cornus compose stop` 调用 plugin 的 `stop`，`start` 重新运行 `up` (幂等) ，`restart` 为先 stop 再 up。`down` 通过 plugin 的 `down` 拆除资源。
- **约束。** `provider` 与 `image`、`build`、`deploy` 互斥。provider service 在 `cornus compose ps` 中显示为 `provider:<type>`，而非已部署的 workload。`--watch` 重载会重新运行 plugin 的 `up` (幂等) ，使编辑后的 provider 配置生效。

## cornus compose up

创建并启动 service (必要时构建，随后部署) 。

```sh
cornus compose up [flags] [services...]
```

Service 按 dependency order 启动，并遵循 `depends_on` condition。显式的 service 列表还会启动这些 service 所依赖的内容——其 `depends_on` 的传递闭包，与 `docker compose up web` 的行为一致——并在因此新增了任何内容时明确说明:

```
also starting dependencies of [web]: [cache db] (--no-deps to skip)
```

只有处于 project 当前生效选择中的 service 才会被纳入，因此被 `--profile` / `COMPOSE_PROFILES` 排除的依赖仍然保持排除，不会被重新拉起。`--no-deps` 只启动你点名的 service，不启动其他任何内容。

Foreground `up` 镜像 `docker compose up`: 它持有 client-local bind mount (经 9P 流式传输)、auto-forwarded published port 和 service log，并保持至 `Ctrl-C`，然后移除自己启动的内容。`-d`/`--detach` 将 mount、forwarded port、任意 SOCKS5 proxy 和 relay-backed egress session 交给后台 agent，并立即返回 (之后由 `down` 停止)。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--build` | — | `false` | 启动前构建镜像 (带 build service 始终构建) 。 |
| `--ssh` | — | — | Build 的 SSH agent forwarding: `default` 或 `id[=socket]` (`RUN --mount=type=ssh`) ，可重复。与每 service `build.ssh` merge。 |
| `-d`, `--detach` | — | `false` | Detached mode: 部署，将 client-local mount、forwarded port、SOCKS5 和 relay-backed egress 交给后台 agent，并立即返回。 |
| `--watch` | — | `false` | 监视已加载的 compose file 与 env file 的编辑，自动重载配置并重新 reconcile 运行中的 service。在 foreground 生效，配合 `-d` 时在后台 agent 生效。参见下方[自动重载](#auto-reload)。 |
| `--no-forward-ports` | — | `false` | 不将 published service port 自动转发至 local listener。 |
| `--no-attach` | — | `false` | 不在 foreground 流式传输 service log (仍持有 mount/forward 直至 `Ctrl-C`) 。与 `docker compose` 中 `--no-attach` 点名一个不 attach 的 service 不同，这里它是 project 级开关——参见 [Docker Compose 兼容性](#docker-compose-compatibility)。 |
| `--no-deps` | — | `false` | 不一并启动所点名 service 的 `depends_on` 依赖。仅在给出显式 service 列表时才有意义；不给出时本来就已选中全部 service。 |
| `--force-recreate` | — | `false` | 即使 workload 毫无变化也重新创建。dockerhost 和 kubernetes 会原样保留未发生变化的 workload，因此强制替换靠的就是它；containerd、bare、incus 本来每次 `up` 都会重新创建。 |
| `-t`, `--timeout` | — | — | 为 `docker compose` 兼容而接受，但**不被遵守**: 发出警告后继续。请改为在 service 上设置 `stop_grace_period:`——参见 [Docker Compose 兼容性](#docker-compose-compatibility)。 |
| `--no-log-prefix` | — | `false` | 不以 service name 为 streamed log line 添加前缀。 |
| `--remove-orphans` | — | `false` | 移除 Compose file 中已不再定义的 service 的 workload (service 被删除或重命名后残留) 。不指定时，`up` 仅对其发出警告。 |
| `--conduit` | `CORNUS_CONDUIT` | profile | Session conduit mode: `port-forward` (每 port local listener，默认) 或 `socks5` (按 service name 访问的一个 split-tunnel proxy) 。bare word 仅设置 mode；`socks5://host:port[?suffix=SUFFIX]` URL 还覆盖 bind address 和 suffix。`--no-forward-ports` 完全禁用 conduit。 |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | 经 SOCKS5 conduit 访问 service ingress (`x-cornus-ingress`) : `native` (隧道连到真正的 cluster ingress controller) 或 `emulate` (带生成证书的 client-side reverse proxy) ，或 `off`。需要 `--conduit socks5`。优先于 `CORNUS_INGRESS_CONDUIT` 和 profile。参见 [Ingress](/zh/guides/ingress)。 |
| `--egress` | — | — | 让 container egress 经 client-side network 路由: `env` (传播 proxy var) 、`proxy` (caretaker forward proxy) 或 `transparent` (nftables + relay) 。 |
| `--egress-route` | — | — | Egress route `PATTERN=ROUTE` (route: `client`\|`gateway`\|`cluster`\|`deny`) ，首个匹配获胜。可重复。 |
| `--egress-default` | — | `cluster` | 未匹配目标的 egress route: `cluster`、`client`、`gateway` 或 `deny`。 |
| `--egress-pac` | — | — | 决定 egress route 的 PAC-style JS file (`FindProxyForURL`) 路径；优先于 `--egress-route`。 |
| `--telemetry-endpoint` | — | — | 启用内置 Collector，并将每个选定服务的 telemetry 导出到该 OTLP endpoint。 |
| `--telemetry-protocol` | — | `grpc` | exporter protocol: `grpc` 或 `http/protobuf`。 |
| `--telemetry-header` | — | — | 静态 OTLP export header `KEY=VALUE`。可重复。 |
| `--telemetry-insecure` | — | `false` | 禁用到 OTLP endpoint 的传输安全。 |
| `--telemetry-signal` | — | 全部 | 将 pipeline 限制为 `traces`、`metrics` 或 `logs`。可重复。 |
| `--telemetry-service-name` | — | deployment name | 覆盖注入的 `OTEL_SERVICE_NAME`。 |
| `--telemetry-debug` | — | `false` | 同时将收集的 telemetry 输出到 Collector stdout。 |

### 自动重载 {#auto-reload}

带 `--watch` 时，`up` 会持续监视 project 加载的每个文件——compose file、同级的 `.env` 或 `--env-file` 条目、每个 service 的 `env_file:`，以及任何 `include:` / `extends` target。当你编辑并保存其中任意一个时，配置会被完整重载，运行中的 project 会朝新的期望状态重新 reconcile: spec 变化的 service 被重建，新增的 service 被启动，移除的 service 被拆除。未变化的 service 保持运行。

- **Foreground** (`up --watch`) : 交互 session 就地重载，随后继续持有新集合 (并重新 attach log) 。被移除的 service——无论是 mounted 还是 fire-and-forget——都会在 server 端删除，与 foreground 退出时的清理一致。
- **Detached** (`up -d --watch`) : 后台 agent 监视文件，变化时重新运行同样的 `up -d --watch` 以重新 plan 并 reconcile。被移除的 *agent 持有* service (client-local mount、forwarded port、relay egress) 会被拆除；被移除的纯 fire-and-forget service 保持运行 (普通的重新 `up -d` 也会保留它——用 `down` 或 `up --remove-orphans` 清除) 。更改文件中的 server 或 conduit 设置需要 `down` + `up`。

完整的 `down` 会停止 watcher；部分的 `down SERVICE` 会让它继续运行。

Egress routing model 参见[Egress](/zh/guides/egress)。

## cornus compose down

按反向 dependency order 停止并移除 service。

```sh
cornus compose down [flags] [services...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--wait` / `--no-wait` | — | `true` | 返回前等待 workload terminate。`--no-wait` 在接受 delete 后立即返回。 |
| `-v`, `--volumes` | — | `false` | 也移除 Compose file 中声明的 named volume (project-scoped、non-external) 。external volume 永不移除。 |
| `--remove-orphans` | — | `false` | 也移除 Compose file 中已不再定义的 service 的 workload (service 被删除或重命名后残留) 。 |
| `--rmi` | — | — | 为 `docker compose` 兼容而接受，但**不被遵守** (`local`\|`all`) : 发出警告，然后照常拆除 workload。其他任何取值都会被直接拒绝。参见 [Docker Compose 兼容性](#docker-compose-compatibility)。 |
| `-t`, `--timeout` | — | — | 为 `docker compose` 兼容而接受，但**不被遵守**: 发出警告后继续。请改为在 service 上设置 `stop_grace_period:`。 |

Orphan 检测按 workload lineage 进行: 每次 `compose up` 都会给每个 workload 打上其所属 project 的印记，因此 `up` / `down` 能将某 project 的残留 workload (你删除或重命名的 service) 与其他 project 的 workload 区分开。`up` 对其发出警告； (无论在 `up` 还是 `down` 上) `--remove-orphans` 会移除它们。没有记录 project 的 workload——裸 `cornus deploy`，或来自其他 project 的——永不触碰。

## cornus compose ps

列出 service 和状态。

```sh
cornus compose ps [flags] [services...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | 仅打印已创建 service 的 resource identifier，每行一个。 |
| `--services` | — | `false` | 仅按 dependency order 每行打印一个 service name。 |
| `--format` | — | `table` | Output format: `table` (SERVICE / NAME / IMAGE / STATUS) 或 `json`。 |

默认列是 `SERVICE`、`NAME`、`IMAGE`、`STATUS`，刻意不同于 `docker compose ps` 的 `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS`。docker 的这些列中有三个描述的是本地 container，而 cornus 的 deployment 没有对应物: `DeployStatus` 既不携带 command，也没有创建时间和 port binding，因为 deployment 是应用到某个后端上的 spec，而那个后端可能根本没有这些概念 (在 kubernetes 上 port 属于 Service，创建时间属于 ReplicaSet) 。cornus 打印的是排在最前的 `SERVICE`——你实际据以查找的 Compose identity——以及作为其对应后端 resource 的 `NAME`。

编写脚本时，请使用承诺稳定的输出而不是列的构成: `--format json` (全部字段，机器可读) 、`--quiet` (resource id) 和 `--services` (service name) 。

## cornus compose logs

查看 service output。每个 selected service 并发 stream。

```sh
cornus compose logs [flags] [services...]
```

Cluster profile 中，log 以您的 kubeconfig credential 直接从 workload pod 读取，仅该路径无法启动时回退到 server proxy。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--follow` | — | `false` | Follow log output。 |
| `-n`, `--tail` | — | `all` | 每 service 从 log 末尾显示的行数 (`all` 表示全部) 。 |
| `-t`, `--timestamps` | — | `false` | 显示 timestamp。 |
| `--since` | — | — | 显示指定 timestamp (RFC3339) 或 relative duration (例如 `42m`) 之后的 log。 |
| `--until` | — | — | 显示指定 timestamp (RFC3339) 或 relative duration 之前的 log。Kubernetes backend 不支持 (warning 后忽略) 。 |
| `--no-log-prefix` | — | `false` | 不以 service name 为每行 log 添加前缀。 |
| `--index` | — | — | 仅 stream 每个 selected service 的这个 replica，与 `docker compose logs --index` 一样从 1 开始。读取实时 runtime；与 `--all-replicas`、`--from=store`、`--match` 和 `--severity` 互斥。超出该 service replica 数的 index 会被拒绝，并给出有效范围。 |
| `--all-replicas` | — | `false` | Stream 已扩缩 service 的每个 instance，而不仅是第一个。每行都带有其副本序号。 |
| `--from` | — | `auto` | 读取来源: `auto`、`runtime` 或 `store`。见下文。 |
| `--match` | — | — | 仅显示包含此文本的行。隐含 `--from=store`。 |
| `--severity` | — | — | 仅显示不低于 `debug`、`info`、`warn`、`error` 或 `fatal` 的记录。隐含 `--from=store`。 |

注意: `--follow` 没有短 `-f`，因为 `compose` group 已使用 `-f` 表示 `--file`——请完整写出 `--follow`。按 docker 习惯粘贴的 `cornus compose logs -f web` 会把 `web` 当作 Compose file 名，因此那个失败会解释 `-f` 在这里的含义，而不是只报告文件不存在。

也没有每命令的 `--no-color`: cornus 的[全局 `--no-color`](/zh/cli/) 在每个 subcommand 上都可用，因此 `cornus compose logs --no-color` 的行为与 docker 的每命令 flag 完全一致。

### 读取已记录日志

服务器带 [`--obs`](/zh/guides/observability#内置存储) 运行时，会记录每个工作负载的输出，因此日志比生成它们的容器存活得更久。`--from` 选择来源:

| 值 | 读取来源 |
| --- | --- |
| `auto` (默认) | 实时 runtime；仅当 runtime 没有产生任何内容且失败时才回退到 store，因此不会比 `runtime` 返回更少的行。 |
| `runtime` | 仅实时 container output，与之前完全相同。 |
| `store` | 仅记录的历史，即使执行 `compose down` 也会保留。 |

```sh
# 即使容器已经不存在，仍然可以查询
cornus compose down
cornus compose logs web --from=store --since 1h

# 搜索和级别筛选需要 store: 实时字节流没有记录
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--follow` 跟随实时 runtime，不能与 `--from=store` 组合；`--match` / `--severity` 不能与 `--from=runtime` 组合。两种组合都会给出解释并拒绝，而不会静默解决。

## cornus compose build

通过 cornus build engine 构建 (并 push) 定义了 build section 的 service 镜像。

```sh
cornus compose build [flags] [services...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--ssh` | — | — | SSH agent forwarding: `default` 或 `id[=socket]` (`RUN --mount=type=ssh`) ，可重复。与每 service `build.ssh` merge。 |
| `--no-cache` | — | `false` | 不使用 build cache。 |
| `--build-arg` | — | — | 设置 build-time variable `KEY=VALUE` (可重复) 。裸 `KEY` 从 environment 取值。覆盖 Compose `build.args`。 |
| `--pull` | — | `false` | 始终尝试拉取每个 base 镜像的更新版本。与每个 service 的 `build.pull` 取或，因此它能为未要求拉取的 build 打开拉取，却不能为已要求的 build 关闭。 |
| `--push` | — | `false` | 本来就是默认行为: 每次 `cornus compose build` 都会推送到 cornus 注册表。该 flag 只是打印镜像去了哪里——参见 [Docker Compose 兼容性](#docker-compose-compatibility)。 |
| `-q`, `--quiet` | — | `false` | 不打印 build 进度。失败仍会被完整报告。 |

## cornus compose exec

在 service 运行中的 container 内执行命令 (镜像 `docker compose exec`)。执行至 service 的第一个 instance；更高的 replica index 无法寻址。

```sh
cornus compose exec [flags] <service> -- <cmd> [args...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-d`, `--detach` | — | `false` | Detached mode。cornus 的 exec backend 尚不支持。 |
| `-e`, `--env` | — | — | 设置环境变量 `KEY=VALUE` (可重复)。裸 `KEY` 从 local environment 取值。 |
| `-w`, `--workdir` | — | — | Container 内执行命令的 working directory。 |
| `-u`, `--user` | — | — | 以此 user (name 或 `uid[:gid]`) 执行命令。 |
| `-T`, `--no-TTY` | — | `false` | 禁用 pseudo-TTY 分配 (默认在 stdin 为 terminal 时分配)。 |
| `--privileged` | — | `false` | 赋予命令 extended privilege。 |
| `--index` | — | `1` | Service 有多个 replica 时的 container instance index (仅第一个 instance 可寻址)。 |
| `--forward-agent` | — | `false` | 将本地 ssh-agent 转发进 exec session (remote-mode dockerhost/containerdhost，或为 service 设置了 `x-cornus-agent-forward: true` 的 kubernetes；参见 [`cornus exec`](/zh/cli/exec)) 。 |

::: warning Kubernetes 上 `-e`/`--env` 的可见性
Kubernetes 的 `pods/exec` API 没有 per-exec 的环境变量参数，因此在 cluster profile 上 cornus 通过将命令包装为 `env KEY=VALUE... <cmd>...` 来模拟它。用 `-e` 传入的内容在该进程存活期间，对 pod 内的 `ps` / `/proc/<pid>/cmdline` 可见。此外，即使在 pod 外部，任何拥有该 pod exec 权限的人也能看到，并不仅限于已经在 pod 内运行的进程。dockerhost 和 containerd backend 原生设置 exec 环境变量，没有这种暴露。请勿在 cluster profile 上通过 `-e` 传递 secret；改用挂载的文件，或 image / deploy-time 的环境变量。
:::

## cornus compose restart / stop / start

Restart、stop 或 start service。每个可选接收 service positional list (默认全部) 。`stop` 按 reverse dependency order 执行；`start` 和 `restart` 按 forward order 执行。被 background `up -d` helper 持有 client-local mount 的 service 会被拒绝——请使用 `down` 停止。

```sh
cornus compose restart [services...]
cornus compose stop [services...]
cornus compose start [services...]
```

为 `docker compose` 兼容，`restart` 和 `stop` 也接受 `-t` / `--timeout`。它**不被遵守**——只发出警告后继续；参见 [Docker Compose 兼容性](#docker-compose-compatibility)。

## cornus compose config

解析、resolve 并 render Compose model (cornus 的 parsed/merged view) 。

```sh
cornus compose config [flags]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--services` | — | `false` | 按 dependency order 每行打印 service name。 |
| `--volumes` | — | `false` | 按排序每行打印 top-level volume name。 |
| `--images` | — | `false` | 按 dependency order 每行打印 service image。 |
| `--format` | — | `yaml` | 完整 dump 的 output format: `yaml` 或 `json`。 |
| `-q`, `--quiet` | — | `false` | 仅 validate model；不打印。 |

## cornus compose version

显示 Compose CLI version。

```sh
cornus compose version [flags]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--short` | — | `false` | 仅打印 bare version string。 |
| `--format` | — | `pretty` | Output format: `pretty` 或 `json`。 |

## Docker Compose 兼容性 {#docker-compose-compatibility}

任何什么也不做的 flag 都不会被静默接受: 无法遵守的 flag 会在命令开始干活之前在 stderr 上说明，并指出该改用什么。cornus 与 `docker compose` 并非完全相同的那些 flag 分为三组。

### 已实现

| Flag | 行为 |
| --- | --- |
| `up --no-deps` | 只启动你点名的 service，跳过 `up` 现在默认执行的 `depends_on` 展开。 |
| `up --force-recreate` | 即使 spec 未变也替换 workload。其做法是打上一个 label，其取值是在 `cornus` 进程生命期内固定的 token。在 dockerhost 上，该 label 属于后端与运行中容器比对的指纹的一部分，因此它能强制执行 spec 未变时本会跳过的重新创建；在 kubernetes 上该 label 落在 pod template 的 annotation 中，因此 Deployment 会滚出一个新的 ReplicaSet——与 `kubectl rollout restart` 的机制相同。containerd、bare、incus 本来每次 `up` 都会重新创建。由于 token 是按进程固定的，`up --watch --force-recreate` 的重载不会在每次保存文件时重新滚动所有 service。 |
| `logs --index` | Stream 已扩缩 service 的一个 replica，与 docker 一样从 1 开始。 |
| `build --pull` | 重新解析每个 base 镜像，与该 service 的 `build.pull` 取或。 |
| `build -q`/`--quiet` | 仅抑制 build 进度渲染。失败的 build 仍会报告其错误。 |

### 为兼容而接受但不被遵守

接受它们是为了让从 `docker compose` 复制来的命令行仍能运行，每个都会警告一次，说明原因以及替代做法。

| Flag | 原因与替代 |
| --- | --- |
| `up`、`down`、`stop`、`restart` 上的 `-t`, `--timeout` | cornus 的部署 API 不携带按调用的关闭超时——生命周期时序由服务器掌握。宽限期是 service 的属性: 在 Compose file 中设置 `stop_grace_period:`，后端会把它作为 container 停止超时 / pod 的 `terminationGracePeriodSeconds` 应用。 |
| `down --rmi=local\|all` | 这套栈里没有任何东西能删除镜像: 部署后端只暴露 workload 和 volume，而构建出的镜像位于服务器上的 cornus 注册表中。你要求的拆除仍会发生；用 [`cornus storage`](/zh/cli/storage) 查看服务器持有什么，并在后端主机上回收镜像空间。除 `local` 和 `all` 以外的取值会在加载 project 之前就被拒绝。 |
| `build --push` | 本来就无条件开启: 每次 compose build 都会推送，因为部署要从注册表把镜像取回。之所以注记而不是静默吞掉，是因为含义不同——docker 推送到镜像 tag 中写明的注册表，cornus 始终推送到**它自己的**注册表，而这条注记会打印实际去往的主机。 |

### 刻意的差异

| 差异 | 细节 |
| --- | --- |
| `logs` 没有短 `-f` | `compose` group 为所有 subcommand 占用了 `-f` / `--file`，无法按命令覆盖。请写 `logs --follow`。`logs -f web` 会自己解释，而不是以一句干巴巴的“没有那个文件”失败。 |
| `up --no-attach` 是布尔值 | 在 docker 中它取一个 service name，在整个 project 启动的同时只让那个 service 不 attach。在这里它是 project 级开关，而位置参数选择要启动哪些 service——因此 `up --no-attach web` 只启动 `web`。两者同时使用时会就此发出警告。 |
| `ps` 打印不同的列 | 是 `SERVICE` / `NAME` / `IMAGE` / `STATUS`，而不是 docker 的那一套；`COMMAND`、`CREATED` 和 `PORTS` 在 deployment 的 status 中没有对应物，在 kubernetes 上也没有意义。脚本请针对 `--format json`、`--quiet` 或 `--services` 编写。 |
| `--no-color` 是全局的 | cornus 只在根命令上声明一次，每个 subcommand 都继承它，因此 `compose logs --no-color` 的行为与 docker 的每命令 flag 一致。 |

## 示例

在 foreground 启动 project 并 stream log:

```sh
cornus compose up
```

面向 remote server，以 detached mode 构建并启动:

```sh
cornus compose --host https://cornus.example.com:5000 up --build -d
```

仅启动 selected service，并通过 SOCKS5 conduit 访问:

```sh
cornus compose up --conduit socks5 web api
```

在 socks5 模式下，background agent 托管一个共享 proxy，并在其中注册每个 service 的短名称，因此浏览器 (或任何 SOCKS5 客户端) 可以通过一个 proxy 访问 `web.cornus.internal`、`api.cornus.internal` 等名称。`cornus web --publish-in-conduit` 会把 Web UI 发布到同一个共享 conduit 中，因此一个浏览器 proxy 设置就能同时访问整个 stack 和 UI——请参见[通过一个浏览器 proxy 访问整个 Compose stack 及其 Web UI](/zh/guides/networking)和 [`cornus web`](/zh/cli/web)。

Follow 一个 service 的最后 100 行 log:

```sh
cornus compose logs --follow --tail 100 web
```

拆除 project 并移除 named volume:

```sh
cornus compose down --volumes
```

在某个 service 的 container 中打开 shell:

```sh
cornus compose exec web -- sh
```
