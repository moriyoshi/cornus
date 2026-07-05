# cornus web

为 cornus 服务器管理的工作负载和 Compose 项目提供本地浏览器 UI。

## 概述

```sh
cornus web [flags]
```

## 说明

`cornus web` 会启动内嵌 SolidJS 应用和客户端侧 backend-for-frontend (BFF)。UI 可显示工作负载生命周期与详情、Compose 项目及其 `depends_on` 图、客户端本地挂载、隧道与转发、配置文件、流式日志、并排放置文件浏览器与交互式 exec 终端的[工作区](#工作区)，以及基于服务器内置可观测性存储的[指标仪表板](#指标仪表板)。BFF 还向客户端公开工作负载统计流。

Compose 结构、本地文件源和存活的后台 agent session 不属于服务器扁平化的 workload API，因此 BFF 在客户端运行。它与其他客户端命令一样使用当前选择的连接配置。项目视图使用传给本命令的 Compose 文件；若既未发现也未显式指定文件，服务器工作负载视图仍可使用，但项目视图为空。

UI 没有身份验证。在默认模式下，它只监听 loopback: `--addr` 必须使用 `localhost` 或 loopback IP literal；通配地址和非 loopback 地址会被拒绝。使用 `--publish-in-conduit` 时，它完全不绑定 listener，只能通过 SOCKS5 conduit (见下文) 访问；该 conduit 本身位于 loopback，因此无论哪种方式，无身份验证边界都保持不变。

## 标志

| 标志 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:0` | Loopback 监听地址。端口 `0` 会选择可用端口。与 `--publish-in-conduit` 互斥。 |
| `-H`, `--host` | `CORNUS_HOST` | 配置，然后是 `http://localhost:5000` | cornus 服务器 endpoint。 |
| `-f`, `--file` | — | Compose 自动发现 | Compose 文件，可重复指定。 |
| `--env-file` | — | `.env` 自动发现 | 用于 Compose 变量插值的 env 文件，可重复指定，并替代默认发现。 |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | Compose 目录名 | 项目名。 |
| `--open` | — | `false` | 监听器启动后在默认浏览器中打开 UI。 |
| `--frontend` | `CORNUS_WEB_FRONTEND` | 内嵌资源 | 独立前端开发服务器 URL。非 BFF 请求会反向代理到该服务器，实际 BFF 保持在同一 origin。 |
| `--mcp` / `--no-mcp` | — | `true` | 在 `/.cornus/mcp` 共同托管供 agent 客户端使用的 MCP (Model Context Protocol) 服务器。`--no-mcp` 会禁用它。 |
| `--mcp-stdio` | — | `false` | 只通过 stdin/stdout 提供 MCP 服务器而不绑定 HTTP listener，供启动命令的 agent 客户端使用。不绑定任何端口。与 `--publish-in-conduit` 互斥。 |
| `--publish-in-conduit` | — | `false` | 在 background agent 内托管 UI 并将其发布到共享 SOCKS5 conduit，而不是绑定本地端口。 |
| `--publish-name` | — | suffix apex (例如 `cornus.internal`) | 在 conduit 中发布 UI 所用的主机名。隐含 `--publish-in-conduit`。 |
| `--publish-port` | — | `80` | 发布名称响应的 conduit 端口。 |
| `--conduit` | `CORNUS_CONDUIT` | profile | `--publish-in-conduit` 使用的 SOCKS5 conduit 选择器 (bare `socks5`，或 `socks5://host:port[?suffix=SUFFIX]`) 。 |

## UI 和工作负载共用一个浏览器 proxy 设置

当你通过 SOCKS5 conduit 访问 cornus 服务器的工作负载时——浏览器 proxy 设置为 `cornus socks5` (或 `cornus config set-context --conduit-mode socks5`) 并解析 `*.cornus.internal` 名称——`cornus web` UI 是单独的 `http://127.0.0.1:<port>`，需要自己的浏览器设置。`--publish-in-conduit` 消除了这种分离:

```sh
cornus web --publish-in-conduit
```

这会把 UI backend 交给 background agent；agent 在进程内 listener 上提供该 backend，并在**共享** conduit 中以 `cornus.internal` (service-host suffix apex) 发布。随后，UI 通过访问工作负载的同一个 proxy 在 `http://cornus.internal/` 响应——一个浏览器 proxy 设置即可访问两者。它不绑定本地端口，因此不会新增暴露；UI 的可达范围与 proxy 完全相同。

命令保持在前台运行，并在退出 (或被终止) 时撤销该名称。如果 agent 重启，它会自动重新发布。

注意:

- 浏览器必须通过 proxy 执行 **remote** DNS (SOCKS5h)，使 `cornus.internal` 由 proxy 而不是本地解析——这与现有 `*.cornus.internal` 工作负载名称的要求相同。
- 发布名称只提供 `http://`，不提供 `https://`。
- 工作负载 session 也应使用 **socks5** conduit。如果它们以默认 port-forward 模式运行，UI 仍可解析，工作负载也仍可通过完整部署名称解析，但 Compose 短名称 (例如以 `demo-web` 部署的服务对应的 `web.cornus.internal`) 无法解析——这些 alias 只由 socks5 模式的工作负载 session 注册。
- 此处传入的 conduit 设置必须与工作负载 session 使用的设置一致，否则两个 proxy 会在同一个绑定地址上冲突。

## 面向 agent 客户端的 MCP 端点

同一服务器还在 `/.cornus/mcp` 共同托管 [MCP](https://modelcontextprotocol.io) (Model Context Protocol) 服务器，使 agent 客户端——Zed 的 Agent panel、Claude Desktop 等——可以驱动 UI 所公开的同一组客户端侧能力: 列出并操作工作负载、读取依赖图和挂载、tail 日志、运行一次性命令，以及读取或写入 allow-list 中的 Compose/env/配置文件。它默认启用；传入 `--no-mcp` 可将其禁用。

MCP 工具只是 UI BFF 所用相同逻辑的轻量 adapter，因此两个界面不会产生偏差。流式传输仍仅限 UI: 交互式终端和实时日志/统计流不适合 MCP 的请求/响应模型，因此 MCP 提供有界的 `logs_tail` (最后 N 行) 和一次性的 `exec_run` (捕获 stdout/stderr/退出状态) 。

有一个工具方向相反。`project_apply` 会重新部署已加载的项目 (等价于 `cornus compose ... up -d`，因此标准 Compose 调谐和后台 agent 行为仍是正本) ，而 UI 中没有与之对应的操作。UI 是 CLI 的辅助界面，而不是它的第二个入口，因此重新部署属于你已经打开的那个终端；而驱动 MCP 的 agent 并没有这样的终端。

agent 还可以获取服务器的[飞行记录](/zh/cli/activity)，它回答的是事后“出了什么问题”，而不是“现在是什么状态”: `activity_read` 工具具有与 CLI 相同的 `since`/`kind`/`unfinished` 筛选器，另有 `cornus://activity/unfinished` **资源**，即服务器及其 caretaker 已开始但从未完成的事项集合。资源形式最为实用: 客户端可以像文件一样附加它，因此当 agent 被问到行为异常的部署时，它一开始就知道上一台服务器在执行中途停止。两者都会随记录携带 `liveInstance`；没有它，当前服务进程自身未结束的生命周期会被读成崩溃。跟随 (`cornus activity --follow`) 与日志流出于同一原因仍仅限 CLI。

MCP 完整继承 UI 的威胁模型: 相同的 loopback / 无身份验证边界和相同的 DNS rebinding Host 防护。使用 `--publish-in-conduit` 时，MCP 端点与 UI 发布在同一个 SOCKS5 conduit 中，这会像 UI 已经公开那样向 conduit 用户公开 `file_write` 和 `exec_run`——如果想缩小此处的影响范围，请使用 `--no-mcp`。

大多数 MCP 客户端通过 stdio 启动命令，而不是连接 HTTP URL。对于这类客户端，运行 `cornus web --mcp-stdio`；它通过 stdin/stdout 提供完全相同的工具界面，并且不绑定 HTTP listener。它复用与浏览器 UI 相同的连接配置和 Compose 标志；诊断信息发送到 stderr，因此不会破坏 stdout 上的 JSON-RPC 流。例如，在客户端中注册为:

```json
{
  "command": "cornus",
  "args": ["web", "--mcp-stdio", "-f", "compose.yaml"]
}
```

## 指标仪表板

**Metrics** 页面把服务器[内置可观测性存储](/zh/guides/observability)记录的内容绘制成图表。它不需要在工作负载中做任何埋点，也不需要在 UI 中做任何配置，但它需要这个存储，因此请以 `--obs` 启动服务器:

```sh
cornus serve --obs
```

没有存储时，所有可观测性路由都返回 `501`。页面不会把它报告为错误，而是说明情况并指出所需的标志。

页面标题旁边的 **Scope** 开关用于选择仪表板的对象: **Workloads** (CPU、内存、内存上限、网络 I/O、磁盘 I/O、进程数) 或 **Server** (cornus 进程自身的 CPU、内存、Go 堆、goroutine 数、线程数、文件描述符数、网络 I/O，以及累计的构建数和部署数)。

一行过滤器用于收窄它:

| 控件 | 作用 |
| --- | --- |
| **Range** | 最近 15 分钟 / 1 小时 / 6 小时 / 24 小时。步长和刷新间隔随范围变化，因此 24 小时窗口不会每 15 秒重新读取一次。 |
| **Workload** | 把工作负载面板收窄到单个部署。仅在 Workloads 作用范围下出现。 |

每个面板都带有当前值、每个序列一条折线 (按副本、按 CPU 模式、按 I/O 方向)，以及一个 **Table** 切换，用最新值 / 最小 / 最大 / 平均读取同一批序列 —— 因此没有任何数值只能靠悬停才能读到。悬停图表，或聚焦后使用方向键，会移动一条十字准线，读出该时刻所有序列的值。

累计计数器 (`container_cpu_time`、`container_network_io`、`container_disk_io`、`process_cpu_time`、`cornus_server_network_io`) 在浏览器中被微分为每秒速率，数值下降会被视为计数器重置，而不是负流量。当前部署后端从不上报的指标 (非 Kubernetes 上的 `container_cpu_usage`，或 Kubernetes 上的网络与磁盘 I/O) 显示为"尚无任何来源上报过它"，这正是存储本身给出的答复。

一个面板最多绘制 8 条序列 —— 这是调色板中能可靠区分的颜色数量 —— 并会写明省略了多少条。要看到其余序列，请收窄到单个工作负载，或改读表格。

作用范围会写进 URL (`/metrics?workload=shop-web&range=6h`)，因此某个视图可以被链接和分享；无法识别的值会回落到默认值，而不是把页面清空。

::: tip 从命令行获取同一份数据
仪表板查询的存储与 [`cornus observe metrics`](/zh/cli/observe#cornus-observe-metrics) 相同。后者接受任意 PromQL，也能读到工作负载自己导出的指标；仪表板覆盖的是 Cornus 自动为你记录的那部分。
:::

### 图表就在你所在的位置

同样的面板也会出现在它们所描述的对象旁边，于是那个最常见的问题 —— "这个东西忙不忙?" —— 不再需要专门跑一趟仪表板:

- **Overview 上的每个项目区块和工作负载区块**都带有一个双面板条带: 该标题之下所有对象最近一小时的 CPU 与内存。项目条带只覆盖该项目的部署，不含其他。**All metrics →** 会打开仪表板，并且已经收窄到同一范围。
- **工作负载自己的页面**有一个 **Metrics** 区块 (该页面依次排布 Instances、Spec、Metrics、Logs)，显示仅属于该部署的完整工作负载面板集，并带有自己的范围控件。

只有当服务器带有存储时才会出现这些条带；没有 `--obs` 时，Overview 保持原样，而不是在每个标题下长出一段说明。

这些视图里的 CPU 面板会把两种后端写法 (主机后端的 `container_cpu_time` 与 Kubernetes 的 `container_cpu_usage`) 合并到一张图上，因为它们是同一个量、同一个单位。完整仪表板则把它们保留为两个面板，空的那个会说明自己属于哪种后端。

已停止的部署仍然会画出它此前那段窗口的曲线。工作负载"此刻"如何是状态徽章的职责; 图表的职责是它做过什么。

## 工作区

**工作区**是一块平铺式屏幕，容纳两种窗格: 一种是把本地挂载与运行中容器统一为同一命名空间的文件浏览器，另一种是工作负载上的交互式终端。无论窗格里装的是什么，切分、以标签页形式堆叠和重新排布的方式都一样，布局也能在重新加载后保留。

打开时只有一个文件浏览器窗格，停在挂载列表上。当你身处某个运行中的工作负载内部时，**在终端中打开** (`prefix t`) 会**在你正在浏览的目录里**打开一个 shell — 是屏幕上的那个文件夹，而不是你在其中选中的某一行; 窗格的落点和其他新窗格一样，由你指向某个平铺块来决定。终端是一个站立的位置，而不是对某一行做的事，所以只有这条命令会忽略 **打开** 和 **新建窗格** 都会读取的选择。该命令始终列出，无法执行时会说明原因: 在挂载列表上还没有指定工作负载，本地文件夹没有可连接的容器，已停止的工作负载则会指名说明。

**打开**会把选中的行放进属于它自己的窗格: 文本文件用编辑器，图片用查看器，文件夹则是另一份列表。这三者是同一个命令; 命令面板会写出它将打开的那一行，当这一行是文件夹时末尾带一条斜杠 (`Open "logs/"…`)。`Ctrl+Enter` (Mac 上是 `Cmd+Enter`) 无需前缀即可执行; 对文件来说，在行上按 Enter、双击、点击文件名同样可以。

无论走哪条路径，它都会点亮平铺块上的落点标记，问你**这个窗格该放在哪里**: 按 Space 作为当前平铺块上的标签页，按方向键 (或 `hjkl`) 切分并放在它旁边，按 Esc 取消。在*文件夹*上按不带修饰键的 Enter 仍然是就地进入它，修饰键表达的是“不在这里，而在我接下来指的地方”。只要身处挂载之中，打开就始终列出，无法执行时会说明原因: 没有选中任何行、选中了多行，或者这个文件编辑器和查看器都无法显示。

如果你每次给出的都是同一个答案，那就只说一次。**设置 → 工作区 → 新窗格落点**提供三个选项: *每次询问* (默认)、*总是并排放置*、*总是作为标签页*。后两个就是把上面那两个答案固定下来，于是每条创建窗格的命令都会跳过提示直接落位。无论选哪个，可达的布局都不会因此增加或减少 — 这个设置只决定采用提示本身给出的哪一个答案。**切分窗格**不受影响: 它的名字已经说明了自己做出的落点，它只问放在哪一条边。

::: warning 工作目录是一种期望，而非保证
它作为 exec 的工作目录发送，docker、containerd、裸主机和 Incus 后端都会遵守。Kubernetes 无法表达工作目录 (`PodExecOptions` 没有该字段)，因此在 Kubernetes 工作负载上打开的终端会从镜像指定的位置开始。
:::

## 终端 shell 探测

在工作负载上打开终端时，shell 不是猜出来的，而是找出来的。BFF 会在运行中的容器里跑一次探测，连接到该镜像实际拥有的最佳交互 shell。于是带 `bash` 的镜像给你 `bash`，只有 busybox 的镜像也仍然给你一个 shell。

候选按下列顺序尝试，第一个存在的胜出:

1. 工作负载自身 `entrypoint:` 或 `command:` 所指的 shell (当它确实是 shell 时)——这是镜像作者的选择，也是唯一已有存在证据的候选;
2. 工作负载的 `x-cornus-shells:` 列表 (参见 [Compose 支持](/zh/cli/compose#交互-shell-候选));
3. 所选[连接配置](/zh/reference/connection-config)的 `shells:` 列表;
4. 浏览器自己的列表，位于 **设置 -> 终端 -> shell 候选**。

这些列表是拼接而不是替换: 更具体的来源只把自己的条目提到前面，并不移除后备项。每个条目是一个命令**字符串**，而不是预先切分好的参数列表: `/bin/busybox sh` 是一个条目，切分方式与 Compose 切分 `command:` 相同。

浏览器的默认列表依次为 `/bin/zsh`、`/usr/bin/zsh`、`/bin/bash`、`/usr/bin/bash`、`/bin/dash`、`/usr/bin/dash`、`/bin/ash`、`/usr/bin/ash`、`/bin/sh`、`/usr/bin/sh`、`/busybox/sh`、`/bin/busybox sh`、`/usr/bin/busybox sh`。

当一个候选都不存在时 (distroless 或 scratch 镜像)，窗格会说明这一点并询问要运行的命令，而不是以一条泛泛的连接错误告终。窗格会记住它最终采用的 shell，因此重新打开或刷新都不会再次探测。

镜像每缺少一个候选，探测就多花一次 exec 往返，并在第一个能启动的候选处停止: 一个启动起来的 shell 会一次性报告所有候选。结果按工作负载缓存 30 秒。

配置中的 `shells:` 字段被视为安全敏感项，因为它指定了一个会在你的工作负载内部执行的二进制文件。项目级 `cornus-context.yaml` 只有在 `--trust-context-file` 下才能提供它; 自动发现的文件中该字段会被剥除。

## 文件编辑

编辑器仅限于解析后的 Compose 文件、env 文件和客户端配置文件。任意路径和路径穿越写法都会被拒绝。

编辑 Compose 文件不会触发任何重新部署: 想要应用改动时，请自行运行 `cornus compose up -d`。UI 中没有应用按钮——它是 CLI 的辅助界面，而不是它的第二个入口。(agent 客户端可以通过 `project_apply` MCP 工具获得该操作，参见上文。)

## 示例

使用当前连接配置和自动发现的 Compose 文件，在自动选择的 loopback 端口启动。

```sh
cornus web --open
```

显式选择远程服务器和项目。

```sh
cornus web --host https://cornus.example.com:5000 \
  -f compose.yaml -p demo --addr 127.0.0.1:8080
```

单独运行 Vite 并使用热重载，同时让实际 BFF 保持在同一 origin。

```sh
cornus web --frontend http://localhost:5173
```

将 UI 发布到 SOCKS5 conduit，使一个浏览器 proxy 设置同时访问 UI 和工作负载:

```sh
cornus config set-context --conduit-mode socks5   # 工作负载 session 也使用 socks5
cornus socks5 &                                    # 浏览器指向的 proxy
cornus web --publish-in-conduit                    # UI 位于 http://cornus.internal/
```

另请参阅 [`cornus compose`](/zh/cli/compose)、[`cornus daemon`](/zh/cli/daemon)和[连接配置参考](/zh/reference/connection-config)。
