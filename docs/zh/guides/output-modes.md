# 输出模式

CLI 打印的每一行都经过同一个输出 driver，并由全局 `--output` flag（环境变量 `CORNUS_OUTPUT`，默认 `auto`）选择三种呈现模式之一，因此该选择会一致地应用于每个子命令。

| `--output` | 呈现方式 |
| --- | --- |
| `auto`（默认） | 在交互式终端使用 fancy，否则使用 plain |
| `fancy` | 颜色、对齐表格、实时 spinner 和进度条 |
| `plain` | 确定性、无 ANSI 的文本，适用于 pipe、CI 和日志 |
| `json` | 机器可读的 NDJSON，每行一个 JSON object |

自动检测较为保守：只有 stdout 和 stderr 都是终端时才启用 fancy，因此 `cornus compose ps | cat` 绝不会收到 ANSI escape。在 Windows 上，除非强制颜色，否则保持 plain。无论终端情况如何，`--output json` 始终不带颜色。

## 通道纪律

每种模式都遵循通道纪律：命令结果——表格、值以及作为 `logs` 要点的日志流——写到 stdout；进度和通知写到 stderr。因此 stdout 始终适合 pipe，`--output json` 的消费者可从 stdout 读取结构化结果，从 stderr 读取结构化进度。

## 颜色

颜色遵循通常约定：

* `--no-color`（或 `NO_COLOR` / `CLICOLOR=0`）移除颜色但保留 fancy 布局。
* `CLICOLOR_FORCE` 强制启用颜色。

## Fancy 模式

在交互式终端中，fancy 模式会添加彩色通知 glyph（`✓` / `▸` / `•` / `⚠` / `✗`）、简洁的标题下划线表格，以及按服务名稳定 hash 着色的每服务日志前缀（docker-compose 风格）。实时进度区域会在滚动输出上方的 stderr 绘制：`cornus build` 使用 BuildKit 风格的逐步骤 spinner，`cornus compose up` / `cornus deploy` 使用每服务 reconcile spinner 和总体进度条。该区域不会触碰 stdin（交互式提示仍可使用），且只有 stderr 是真实终端时才会动画，因此不会污染 pipe。

## JSON 模式（供编码 agent 使用）

`--output json`（或 `CORNUS_OUTPUT=json`）输出换行分隔 JSON——每行一个 object，除此之外没有任何内容——因此编码 agent 或脚本无需 screen-scraping 即可使用 Cornus 输出。请同时读取两个 stream：stdout 上是结果，stderr 上是进度和通知。

Object 的形状如下：

* **通知**（stderr）：`{"level":"info","msg":"..."}`，其中 `level` 是 `step` / `done` / `success` / `info` / `warning` / `error` 之一。
* **日志行**（例如 `cornus compose logs`，stdout）：`{"type":"log","tag":"web","line":"...\n"}`。
* **表格行**（例如 `config get-contexts`，stdout）：每行一个 object，以列标题为 key，例如 `{"CURRENT":"*","NAME":"prod","SERVER":"https://prod:8443"}`。
* **单个值**（例如 `version`、`token`）：`{"value":"..."}`。
* **命令结果 / 事件**，即每个命令的结构化记录：
  * `cornus build` 结果（stdout）：`{"event":"built","tag":"localhost:5000/app:v1","digest":"sha256:..."}`；构建进度在 stderr，以 `{"vertex":"[2/5] RUN ...","status":"start"}` 输出（`status` 为 `start` / `done` / `cached` / `error`），日志行为原样 `{"log":"...\n"}`。
  * `cornus deploy`（stdout）：`{"event":"deployed","name":"app","running":2,"total":3}`。
  * `cornus compose up` / `down`（stderr 事件）：`{"service":"web","event":"up","running":2,"total":2}`；`event` 动词可以是 `up`、`removed`、`forwarding`、`started`、`stopped`、`restarted`、`recreated`、`transition` 等。
  * `cornus tunnel`（stdout）：`{"event":"tunnel","name":"app","port":8080,"url":"https://....ngrok...."}`。
  * `cornus daemon status`（stdout）：`{"running":true,"servers":[...],"projects":{...}}`。

```sh
# Drive a build and pull out the pushed digest:
cornus --output json build -t localhost:5000/app:v1 . 2>/dev/null \
  | jq -r 'select(.event=="built") | .digest'

# Stream compose lifecycle events as NDJSON (results on stdout, events on stderr):
CORNUS_OUTPUT=json cornus compose up

# List connection profiles, one JSON object per row:
cornus --output json config get-contexts | jq -r .NAME
```

::: tip
CI 日志和 pipeline 请使用 `plain`；当编码 agent 或脚本需要确定性解析 Cornus 输出时，请使用 `json`。
:::
