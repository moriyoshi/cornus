# Compose 扩展与兼容性

Cornus 读取的是普通的 Compose 文件。本指南讲的是它不普通的两处: 它所理解的 `x-cornus-*` 扩展字段和它所委托的 compose-spec `provider:` service，以及少数几个它刻意与 `docker compose` 行为不同的 flag。逐个 flag 的参考在 [`cornus compose`](/zh/cli/compose)，运行 project 的操作步骤在 [Compose、devcontainer 和 docker CLI](/zh/guides/compose-devcontainers-docker)。

## 工作原理 {#how-it-works}

Compose 文件的发现、合并与变量插值方式与 `docker compose` 相同，每个 service 都会成为一个 cornus deployment。Compose 规范定义的一切都保持原有含义。Cornus 在此之上加了两样东西:

- **`x-cornus-*` 扩展字段**，用于规范中没有位置可放的能力——把在你机器上生成的凭据中介给 service、让 container 的出站流量走你的网络、导出它的遥测数据。
- **当某个 `docker compose` flag 无法映射到 deploy API 时，给出它自己的答案。** 任何什么都不做的 flag 都不会被静默接受: 无法遵守的 flag 会在命令开始工作之前在 stderr 上说明，并指出应当改用什么。参见 [Docker Compose 兼容性](#docker-compose-compatibility)。

### 扩展字段 {#the-extension-fields}

| 字段 | 声明的内容 | 详见 |
| --- | --- | --- |
| `x-cornus-shells:` | service 的 image 所拥有的交互 shell (按优先顺序) | [下文](#interactive-shell-candidates) |
| `x-cornus-credentials:` | 在你机器上生成并中介给 service 的凭据 | [凭据](/zh/guides/credentials#从-compose-文件) |
| `x-cornus-egress:` | 经调用方网络路由的出站流量 | [Egress](/zh/guides/egress) |
| `x-cornus-ingress:` | 给 service 的公开 HTTP(S) 主机名 | [Ingress](/zh/guides/ingress) |
| `x-cornus-telemetry:` | service 各信号的 OpenTelemetry 导出 | [可观测性](/zh/guides/observability) |
| `x-cornus-agent-forward:` | 允许对该 service 使用 `exec --forward-agent` | [`cornus exec`](/zh/cli/exec) |

其中大多数也接受**project 级别**的 block: project 级别的 block 为所有未自行声明的 service 提供默认值，而 service 上的 block 是**整体**覆盖而不是逐字段合并，因为这个 block 是一个整体的偏好，而不是一组彼此独立的 key。这是 `x-cornus-shells:`、`x-cornus-credentials:`、`x-cornus-egress:` 和 `x-cornus-telemetry:` 的规则。

`x-cornus-ingress:` 是唯一的例外，而且是刻意如此: project 级别的 block **不会**为任何 service 打开 ingress。ingress 始终是每个 service 自行选择加入的，project 的 block 只是把 domain、class 和 TLS issuer 逐字段合并进那些已经选择加入的 service，并且 service 自身的值优先。否则一个 project 范围的默认值就会把 stack 里的每个 service 都公开出去。`x-cornus-agent-forward:` 根本没有 project 级别的形式。

## 交互 shell 候选 {#interactive-shell-candidates}

`x-cornus-shells:` 按优先顺序列出某个 service 的 image 所拥有的交互 shell。[`cornus web`](/zh/guides/web-ui#终端-shell-探测) 的终端会读取它，并在自己的候选列表之前进行探测，因此镜像里带了不常见 shell 的 service 无需任何浏览器端配置就能用它打开。

```yaml
services:
  api:
    image: myorg/api
    x-cornus-shells:
      - /bin/bash
      - /bin/busybox sh
```

每个条目是一个命令**字符串**，而不是预先切分好的参数列表，切分方式与 `command:` 和 `entrypoint:` 相同——所以 `/bin/busybox sh` 是一个条目。只有一个候选时也接受裸字符串 (`x-cornus-shells: /bin/bash`)。

它不改变部署的任何内容: 没有任何部署 backend 读取它，它也不属于 backend 用来与运行中 container 比对的 spec，因此编辑它永远不会触发重新创建。

## Provider service {#provider-services}

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

## 编辑时自动重载 {#auto-reload-on-edit}

带 [`up --watch`](/zh/cli/compose#cornus-compose-up) 时，`up` 会持续监视 project 加载的每个文件——compose file、同级的 `.env` 或 `--env-file` 条目、每个 service 的 `env_file:`，以及任何 `include:` / `extends` target。当你编辑并保存其中任意一个时，配置会被完整重载，运行中的 project 会朝新的期望状态重新 reconcile: spec 变化的 service 被重建，新增的 service 被启动，移除的 service 被拆除。未变化的 service 保持运行。

```sh
cornus compose up --watch        # foreground
cornus compose up -d --watch     # 由后台 agent 持有
```

- **Foreground** (`up --watch`) : 交互 session 就地重载，随后继续持有新集合 (并重新 attach log) 。被移除的 service——无论是 mounted 还是 fire-and-forget——都会在 server 端删除，与 foreground 退出时的清理一致。
- **Detached** (`up -d --watch`) : 后台 agent 监视文件，变化时重新运行同样的 `up -d --watch` 以重新 plan 并 reconcile。被移除的 *agent 持有* service (client-local mount、forwarded port、relay egress) 会被拆除；被移除的纯 fire-and-forget service 保持运行 (普通的重新 `up -d` 也会保留它——用 `down` 或 `up --remove-orphans` 清除) 。更改文件中的 server 或 conduit 设置需要 `down` + `up`。

完整的 `down` 会停止 watcher；部分的 `down SERVICE` 会让它继续运行。

## Docker Compose 兼容性 {#docker-compose-compatibility}

cornus 与 `docker compose` 并非简单一致的 flag 分为三组。

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

### 刻意的差异 {#deliberate-divergences}

| 差异 | 细节 |
| --- | --- |
| `logs` 没有短 `-f` | `compose` group 为所有 subcommand 占用了 `-f` / `--file`，无法按命令覆盖。请写 `logs --follow`。`logs -f web` 会自己解释，而不是以一句干巴巴的“没有那个文件”失败。 |
| `up --no-attach` 是布尔值 | 在 docker 中它取一个 service name，在整个 project 启动的同时只让那个 service 不 attach。在这里它是 project 级开关，而位置参数选择要启动哪些 service——因此 `up --no-attach web` 只启动 `web`。两者同时使用时会就此发出警告。 |
| `ps` 打印不同的列 | 是 `SERVICE` / `NAME` / `IMAGE` / `STATUS`，而不是 docker 的 `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS`。docker 的这些列中有三个描述的是本地 container，而 cornus 的 deployment 没有对应物: `DeployStatus` 既不携带 command，也没有创建时间和 port binding，因为 deployment 是应用到某个后端上的 spec，而那个后端可能根本没有这些概念 (在 kubernetes 上 port 属于 Service，创建时间属于 ReplicaSet) 。排在最前的是 `SERVICE`——你实际据以查找的 Compose identity——以及作为其对应后端 resource 的 `NAME`。脚本请针对承诺稳定的 `--format json`、`--quiet` 或 `--services` 编写，而不是针对列的构成。 |
| `--no-color` 是全局的 | cornus 只在根命令上声明一次，每个 subcommand 都继承它，因此 `compose logs --no-color` 的行为与 docker 的每命令 flag 一致。 |

**另请参阅:** [`cornus compose`](/zh/cli/compose)、[Compose、devcontainer 和 docker CLI](/zh/guides/compose-devcontainers-docker)、[凭据](/zh/guides/credentials)、[Egress](/zh/guides/egress)、[部署规范参考](/zh/reference/deploy-spec)
