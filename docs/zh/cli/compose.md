# cornus compose

兼容 Docker Compose 的 client，经 `/.cornus/v1/*` endpoint 将 Compose command 重定向到运行中的 cornus server。

## 概要

```sh
cornus compose [group flags] <subcommand> [flags]
```

## 说明

`cornus compose` 镜像 `docker compose`：它加载 Compose project（或 devcontainer definition），再针对 cornus server 构建、部署并管理 service。可将 `cornus compose` alias 为 `docker-compose` 以直接替换使用，或者使用 [`cornus daemon docker`](/zh/cli/daemon) 让标准 `docker` / `docker compose` 工作。

Project source 是 Compose file 或 devcontainer。Compose file discovery 在 working directory 查找 `compose.yaml`、`compose.yml`、`docker-compose.yaml` 或 `docker-compose.yml`。给出 `--devcontainer`、`-f` 指向 `devcontainer.json`，或未找到 Compose file 但可发现 `.devcontainer/devcontainer.json`（或 `.devcontainer.json`）时使用 devcontainer。混合 repo 中 Compose file 始终优先。

Server connection 由 `--host` 解析，否则使用 selected connection profile，再否则 `http://localhost:5000`。构建镜像的 tag 和 deploy pull ref 使用以下顺序解析 registry：`--registry` / `CORNUS_REGISTRY` / profile，随后 server-advertised host（`GET /.cornus/v1/info`），最后 endpoint host。产生的 deployment shape 见[Deploy spec 参考](/zh/reference/deploy-spec)。

## Group flag

这些 flag 位于 `compose` group，并适用于每个 subcommand。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-f`, `--file` | — | discovery | Compose file，可重复。默认 working directory 中的 `compose.yaml` / `docker-compose.yml`。 |
| `--env-file` | — | `.env` | 用于 variable interpolation 的 env file，替换默认 `.env` discovery。可重复；后者获胜；process environment 仍优先。 |
| `--profile` | `COMPOSE_PROFILES` | — | 激活给定 profile 的 service（Compose `profiles:`）。可重复；也遵循 `COMPOSE_PROFILES`。 |
| `--devcontainer` | — | — | `devcontainer.json` 文件路径，或用于查找 `.devcontainer/devcontainer.json` 的目录。覆盖 Compose-file discovery。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | 目录名 | Project name（默认 Compose file directory name）。 |
| `-H`, `--host` | `CORNUS_HOST` | `http://localhost:5000` | cornus server endpoint。回退到 selected connection profile，再到默认值。 |
| `--registry` | `CORNUS_REGISTRY` | 派生 | 构建镜像 tag 和 deploy pull ref 所用 registry `host[:port]`。覆盖 profile 和 server-advertised 值；空时从 server、再从 endpoint host 派生。 |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | 经 cornus server proxy 路由 log 和 auto-forwarded port，而非使用 kubeconfig 直接连接 pod（仅 cluster profile）。`--no-via-server` 强制直接路径。 |

### Devcontainer 支持

Project 来自 devcontainer definition（`.devcontainer/devcontainer.json`）时，`cornus compose` 运行其 lifecycle command：`initializeCommand` 在任意 container 创建前于 host 运行；per-service `postCreate` / `postStart` / `postAttach` hook 随 container 启动运行。普通 Compose service 没有 lifecycle hook。

### Provider service

service 可以将其生命周期委托给外部 provider plugin，而不是作为 container 构建/拉取并运行（compose-spec 的 `provider:`）。这样的 service 指定 plugin 的 `type`，并向其传入 provider 专属的 `options`：

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
- **生命周期。** `up` 时，cornus 以 `<plugin> compose --project-name=<project> up [--key=value ...] <service>` 调用 plugin，每个 `options` 条目作为 `--key=value` flag 传入（list 值展开为重复的 flag）。`down` 时以 `down` 同样调用。plugin 应当是幂等的。
- **环境变量注入。** plugin 在 stdout 上报告环境变量（`setenv KEY=VALUE` 协议）。每个变量以大写的 provider service 名为前缀，暴露给 `depends_on` 该 provider 的 service——因此上面的 `database` provider 会向 `app` 提供 `DATABASE_URL`、`DATABASE_TOKEN` 等。`rawsetenv` 变量不加前缀暴露给依赖方。名称冲突时，依赖方自身的 `environment:` 优先。
- **生命周期命令。** `cornus compose stop` 调用 plugin 的 `stop`，`start` 重新运行 `up`（幂等），`restart` 为先 stop 再 up。`down` 通过 plugin 的 `down` 拆除资源。
- **约束。** `provider` 与 `image`、`build`、`deploy` 互斥。provider service 在 `cornus compose ps` 中显示为 `provider:<type>`，而非已部署的 workload。`--watch` 重载会重新运行 plugin 的 `up`（幂等），使编辑后的 provider 配置生效。

## cornus compose up

创建并启动 service（必要时构建，随后部署）。

```sh
cornus compose up [flags] [services...]
```

Service 按 dependency order 启动，并遵循 `depends_on` condition。Foreground `up` 镜像 `docker compose up`: 它持有 client-local bind mount (经 9P 流式传输)、auto-forwarded published port 和 service log，并保持至 `Ctrl-C`，然后移除自己启动的内容。`-d`/`--detach` 将 mount、forwarded port、任意 SOCKS5 proxy 和 relay-backed egress session 交给后台 agent，并立即返回 (之后由 `down` 停止)。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--build` | — | `false` | 启动前构建镜像（带 build service 始终构建）。 |
| `--ssh` | — | — | Build 的 SSH agent forwarding：`default` 或 `id[=socket]`（`RUN --mount=type=ssh`），可重复。与每 service `build.ssh` merge。 |
| `-d`, `--detach` | — | `false` | Detached mode: 部署，将 client-local mount、forwarded port、SOCKS5 和 relay-backed egress 交给后台 agent，并立即返回。 |
| `--watch` | — | `false` | 监视已加载的 compose file 与 env file 的编辑，自动重载配置并重新 reconcile 运行中的 service。在 foreground 生效，配合 `-d` 时在后台 agent 生效。参见下方[自动重载](#auto-reload)。 |
| `--no-forward-ports` | — | `false` | 不将 published service port 自动转发至 local listener。 |
| `--no-attach` | — | `false` | 不在 foreground 流式传输 service log（仍持有 mount/forward 直至 `Ctrl-C`）。 |
| `--no-log-prefix` | — | `false` | 不以 service name 为 streamed log line 添加前缀。 |
| `--remove-orphans` | — | `false` | 移除 Compose file 中已不再定义的 service 的 workload（service 被删除或重命名后残留）。不指定时，`up` 仅对其发出警告。 |
| `--conduit` | `CORNUS_CONDUIT` | profile | Session conduit mode：`port-forward`（每 port local listener，默认）或 `socks5`（按 service name 访问的一个 split-tunnel proxy）。bare word 仅设置 mode；`socks5://host:port[?suffix=SUFFIX]` URL 还覆盖 bind address 和 suffix。`--no-forward-ports` 完全禁用 conduit。 |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | 经 SOCKS5 conduit 访问 service ingress（`x-cornus-ingress`）：`native`（隧道连到真正的 cluster ingress controller）或 `emulate`（带生成证书的 client-side reverse proxy），或 `off`。需要 `--conduit socks5`。优先于 `CORNUS_INGRESS_CONDUIT` 和 profile。参见 [Ingress](/zh/guides/ingress)。 |
| `--egress` | — | — | 让 container egress 经 client-side network 路由：`env`（传播 proxy var）、`proxy`（caretaker forward proxy）或 `transparent`（nftables + relay）。 |
| `--egress-route` | — | — | Egress route `PATTERN=ROUTE`（route：`client`\|`gateway`\|`cluster`\|`deny`），首个匹配获胜。可重复。 |
| `--egress-default` | — | `cluster` | 未匹配目标的 egress route：`cluster`、`client`、`gateway` 或 `deny`。 |
| `--egress-pac` | — | — | 决定 egress route 的 PAC-style JS file（`FindProxyForURL`）路径；优先于 `--egress-route`。 |
| `--telemetry-endpoint` | — | — | 启用内置 Collector，并将每个选定服务的 telemetry 导出到该 OTLP endpoint。 |
| `--telemetry-protocol` | — | `grpc` | exporter protocol：`grpc` 或 `http/protobuf`。 |
| `--telemetry-header` | — | — | 静态 OTLP export header `KEY=VALUE`。可重复。 |
| `--telemetry-insecure` | — | `false` | 禁用到 OTLP endpoint 的传输安全。 |
| `--telemetry-signal` | — | 全部 | 将 pipeline 限制为 `traces`、`metrics` 或 `logs`。可重复。 |
| `--telemetry-service-name` | — | deployment name | 覆盖注入的 `OTEL_SERVICE_NAME`。 |
| `--telemetry-debug` | — | `false` | 同时将收集的 telemetry 输出到 Collector stdout。 |

### 自动重载 {#auto-reload}

带 `--watch` 时，`up` 会持续监视 project 加载的每个文件——compose file、同级的 `.env` 或 `--env-file` 条目、每个 service 的 `env_file:`，以及任何 `include:` / `extends` target。当你编辑并保存其中任意一个时，配置会被完整重载，运行中的 project 会朝新的期望状态重新 reconcile：spec 变化的 service 被重建，新增的 service 被启动，移除的 service 被拆除。未变化的 service 保持运行。

- **Foreground**（`up --watch`）：交互 session 就地重载，随后继续持有新集合（并重新 attach log）。被移除的 service——无论是 mounted 还是 fire-and-forget——都会在 server 端删除，与 foreground 退出时的清理一致。
- **Detached**（`up -d --watch`）：后台 agent 监视文件，变化时重新运行同样的 `up -d --watch` 以重新 plan 并 reconcile。被移除的 *agent 持有* service（client-local mount、forwarded port、relay egress）会被拆除；被移除的纯 fire-and-forget service 保持运行（普通的重新 `up -d` 也会保留它——用 `down` 或 `up --remove-orphans` 清除）。更改文件中的 server 或 conduit 设置需要 `down` + `up`。

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
| `-v`, `--volumes` | — | `false` | 也移除 Compose file 中声明的 named volume（project-scoped、non-external）。external volume 永不移除。 |
| `--remove-orphans` | — | `false` | 也移除 Compose file 中已不再定义的 service 的 workload（service 被删除或重命名后残留）。 |

Orphan 检测按 workload lineage 进行：每次 `compose up` 都会给每个 workload 打上其所属 project 的印记，因此 `up` / `down` 能将某 project 的残留 workload（你删除或重命名的 service）与其他 project 的 workload 区分开。`up` 对其发出警告；（无论在 `up` 还是 `down` 上）`--remove-orphans` 会移除它们。没有记录 project 的 workload——裸 `cornus deploy`，或来自其他 project 的——永不触碰。

## cornus compose ps

列出 service 和状态。

```sh
cornus compose ps [flags] [services...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | 仅打印已创建 service 的 resource identifier，每行一个。 |
| `--services` | — | `false` | 仅按 dependency order 每行打印一个 service name。 |
| `--format` | — | `table` | Output format：`table` 或 `json`。 |

## cornus compose logs

查看 service output。每个 selected service 并发 stream。

```sh
cornus compose logs [flags] [services...]
```

Cluster profile 中，log 以您的 kubeconfig credential 直接从 workload pod 读取，仅该路径无法启动时回退到 server proxy。

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--follow` | — | `false` | Follow log output。 |
| `-n`, `--tail` | — | `all` | 每 service 从 log 末尾显示的行数（`all` 表示全部）。 |
| `-t`, `--timestamps` | — | `false` | 显示 timestamp。 |
| `--since` | — | — | 显示指定 timestamp（RFC3339）或 relative duration（例如 `42m`）之后的 log。 |
| `--until` | — | — | 显示指定 timestamp（RFC3339）或 relative duration 之前的 log。Kubernetes backend 不支持（warning 后忽略）。 |
| `--no-log-prefix` | — | `false` | 不以 service name 为每行 log 添加前缀。 |

注意：`--follow` 没有短 `-f`，因为 `compose` group 已使用 `-f` 表示 `--file`。

## cornus compose build

通过 cornus build engine 构建（并 push）定义了 build section 的 service 镜像。

```sh
cornus compose build [flags] [services...]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--ssh` | — | — | SSH agent forwarding：`default` 或 `id[=socket]`（`RUN --mount=type=ssh`），可重复。与每 service `build.ssh` merge。 |
| `--no-cache` | — | `false` | 不使用 build cache。 |
| `--build-arg` | — | — | 设置 build-time variable `KEY=VALUE`（可重复）。裸 `KEY` 从 environment 取值。覆盖 Compose `build.args`。 |

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
| `--forward-agent` | — | `false` | 将本地 ssh-agent 转发进 exec session（remote-mode dockerhost/containerdhost，或为 service 设置了 `x-cornus-agent-forward: true` 的 kubernetes；参见 [`cornus exec`](/zh/cli/exec)）。 |

::: warning Kubernetes 上 `-e`/`--env` 的可见性
Kubernetes 的 `pods/exec` API 没有 per-exec 的环境变量参数，因此在 cluster profile 上 cornus 通过将命令包装为 `env KEY=VALUE... <cmd>...` 来模拟它。用 `-e` 传入的内容在该进程存活期间，对 pod 内的 `ps` / `/proc/<pid>/cmdline` 可见。此外，即使在 pod 外部，任何拥有该 pod exec 权限的人也能看到，并不仅限于已经在 pod 内运行的进程。dockerhost 和 containerd backend 原生设置 exec 环境变量，没有这种暴露。请勿在 cluster profile 上通过 `-e` 传递 secret；改用挂载的文件，或 image / deploy-time 的环境变量。
:::

## cornus compose restart / stop / start

Restart、stop 或 start service。每个可选接收 service positional list（默认全部）。`stop` 按 reverse dependency order 执行；`start` 和 `restart` 按 forward order 执行。被 background `up -d` helper 持有 client-local mount 的 service 会被拒绝——请使用 `down` 停止。

```sh
cornus compose restart [services...]
cornus compose stop [services...]
cornus compose start [services...]
```

## cornus compose config

解析、resolve 并 render Compose model（cornus 的 parsed/merged view）。

```sh
cornus compose config [flags]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--services` | — | `false` | 按 dependency order 每行打印 service name。 |
| `--volumes` | — | `false` | 按排序每行打印 top-level volume name。 |
| `--images` | — | `false` | 按 dependency order 每行打印 service image。 |
| `--format` | — | `yaml` | 完整 dump 的 output format：`yaml` 或 `json`。 |
| `-q`, `--quiet` | — | `false` | 仅 validate model；不打印。 |

## cornus compose version

显示 Compose CLI version。

```sh
cornus compose version [flags]
```

| Flag | Env var | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--short` | — | `false` | 仅打印 bare version string。 |
| `--format` | — | `pretty` | Output format：`pretty` 或 `json`。 |

## 示例

在 foreground 启动 project 并 stream log：

```sh
cornus compose up
```

面向 remote server，以 detached mode 构建并启动：

```sh
cornus compose --host https://cornus.example.com:5000 up --build -d
```

仅启动 selected service，并通过 SOCKS5 conduit 访问：

```sh
cornus compose up --conduit socks5 web api
```

Follow 一个 service 的最后 100 行 log：

```sh
cornus compose logs --follow --tail 100 web
```

拆除 project 并移除 named volume：

```sh
cornus compose down --volumes
```

在某个 service 的 container 中打开 shell：

```sh
cornus compose exec web -- sh
```
