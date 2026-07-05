# cornus activity

读取服务端的飞行记录: 它和它的 caretaker 当时在做什么，以及哪些没有完成。

## 概要

```sh
cornus activity [flags]
```

## 说明

当一次部署出问题时，没有别的东西能告诉你发生了什么。`cornus deploy` 和 `status` 报告的只是*此刻*成立的情况，而且仅限于运行时还记得的部分。服务端日志是临时的，会随容器一起消失。追踪数据则完全离开本机，并且默认关闭。

飞行记录不同: 服务端和它的 caretaker 在工作过程中把它们写到数据目录下的磁盘上，因此它们比进程、比容器、比这次事故都活得更久。

工作以 **begin/end 成对**的方式记录，这让最有用的问题变成一个关于"缺失"的问题 — 凡是开始了却没有结束的，就是没有完成的:

- 没有结束的**进程生命周期**，意味着某个服务端或 caretaker 没有干净地关闭 (SIGKILL、OOM、`docker rm -f`、panic、主机重启)。
- 没有结束的 **9P 挂载**，意味着某个挂载点可能仍然存在而无人负责。下次服务端启动时会自动解除它们。
- 没有结束的**服务**，是在其进程死亡时仍在运行的受监督子任务。把它们放在一起，最接近于进程停止那一刻在做什么的快照；而反复崩溃的服务会表现为每次重启一对记录，而不是一段长久的沉默。

每次启动都会取得自己唯一的实例 ID，因此记录会按运行分组:

```
server 02c22ece4a16 (exited cleanly)
  2026-07-26T03:53:19.985820864Z server    begin addr=127.0.0.1:5000
  2026-07-26T03:53:22.950323418Z server    end   [ok]

server 6c8ba5e0d63f (DID NOT EXIT CLEANLY)
  2026-07-26T03:53:23.973104203Z server    begin addr=127.0.0.1:5000
  2026-07-26T03:53:24.101339812Z 9p-mount  begin /var/lib/cornus/mounts/sess-1/m0 deployment=web
```

仍在运行的一次会显示为 `running`，而不是失败 — 这个命令知道是哪个实例在响应它。

## 标志

| 标志 | 默认值 | 说明 |
| --- | --- | --- |
| `--server` | 连接配置文件 | 远程 cornus 服务器 URL。 |
| `--local` | 关闭 | 直接从磁盘读取记录，而不询问服务器。 |
| `--since` | — | 仅此时间之后的记录: RFC3339，或从现在起回溯的时长 (`2h`)。 |
| `--kind` | — | 仅此种类: `server`、`caretaker`、`service`、`9p-mount`、`build`、`deploy`。 |
| `--unfinished` | 关闭 | 仅开始了却没有结束的活动。 |
| `--follow`、`-f` | 关闭 | 先输出已有记录，然后随着记录被写入持续输出。按 Ctrl-C 结束。 |

`--local` 读取的目录来自全局的 `--data-dir` / `CORNUS_DATA`。

## 持续跟随记录

`--follow` 先输出历史，然后保持打开，每写入一条记录就输出一条:

```sh
cornus activity --follow --kind 9p-mount     # 观察挂载的建立与解除
cornus activity -f --since 5m                # 最近的历史，随后转入实时
```

历史与实时跟随来自同一次读取，因此在"已有什么"和"开始观察"之间写入的内容不会丢失 — 这一点很重要，因为最值得跟随的记录恰恰是在机器最繁忙时写下的。Ctrl-C 是正常的停止方式，退出码为 0。

`cornus activity` 的两条读取路径都支持跟随。远程时，服务端从 `GET /.cornus/v1/activity?follow=1` 以 [Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events) 流式发送: 一条长期存在、大部分时间空闲的连接需要保活，也需要一种中间设备不会缓冲的媒体类型，而 SSE 两者都有定义。每条记录作为一个 `activity` 事件到达，其载荷正是一次性读取所返回的同一个 JSON 对象，因此 `--output json --follow` 就是 `--output json` 所给出的那些记录的 NDJSON 流。使用 `--local` 时则直接跟随文件，不涉及服务器。

实时状态下无法按运行分组 — 一次运行的结论只有在它结束之后才能知道 — 所以每一行都写明是哪个进程与实例写下的:

```
2026-07-26T03:53:24.101339812Z server/6c8ba5e0d63f 9p-mount  begin /var/lib/cornus/mounts/sess-1/m0 deployment=web
2026-07-26T03:53:31.884210553Z caretaker/1f2a0b7c9de4 service   begin mount-relay
```

`--follow` 与 `--unfinished` 不能同时使用。"没有完成"是针对整条流解析出来的 — 一条 `begin` 只在它的 `end` 到达之前算作没有完成 — 因此作为一个数据流，它会输出被下一行推翻的记录，却没有任何东西可以把它们撤回。需要快照就去掉 `--follow` 重新运行，或者一边跟随一边自己配对 `begin`/`end`。

## 远程读取与事后排查

默认情况下它与其他命令一样，向所配置的服务器询问，因为操作者几乎从不在服务端曾运行的那台机器上。

这样仍然能回答事后排查的问题。记录位于**数据目录**下，而数据目录正是 cornus 部署会保持持久化的东西 (Helm chart 的卷、容器化服务端的主机绑定、主机安装的存储目录) — 因此*接替的*服务器会提供前任的飞行记录:

```sh
cornus activity --unfinished        # 上一次运行留下了什么？
cornus activity --since 2h --kind deploy
```

`--local` 覆盖前者无法覆盖的唯一情形: 什么都没在运行，而且也不会再回来。

```sh
# 在主机上，或在镜像内部，完全不涉及服务器
docker run --rm -v /srv/cornus:/var/lib/cornus \
  ghcr.io/moriyoshi/cornus:latest activity --local
```

## 机器可读的输出

`--output json` 输出记录本身，这样脚本或 agent 可以直接读取这次飞行:

```sh
cornus --output json activity --unfinished
```

每条记录都带有时间戳、写入它的进程与实例、种类与阶段、目标，以及在 end 记录上的结果。`recovered` 状态表示某次后续运行关闭了该活动: 事故仍然可见，而不会被改写成一次干净的完成。

agent 客户端可以通过 [`cornus web`](/zh/cli/web) 的 MCP 端点拿到同样的记录: 那里公开了一个 `activity_read` 工具 (与 CLI 相同的 `since`/`kind`/`unfinished` 过滤条件)，以及一个 `cornus://activity/unfinished` 资源。资源这种形式才是关键: 它是上下文而不是操作，客户端可以像附加一个文件那样附加当前的未完成集合，于是被问到某个行为异常的部署时，agent 一开始就已经知道上一个服务端是在飞行途中停止的。两者都会随记录一并返回 `liveInstance` — 没有它，正在应答的进程自身那条尚未闭合的生命周期会被读成一次崩溃。跟随功能仅限 CLI: 记录器本来就是事后才去读的。

## 保留

日志有大小上限，并保留一份此前的世代，因此像任何记录器一样是有界的。上限由 `CORNUS_ACTIVITY_MAX_BYTES` 设置 (默认 8 MiB)。

**另请参阅:** [cornus serve](/zh/cli/serve)、[cornus storage](/zh/cli/storage)、[在容器中运行服务端](/zh/guides/server-in-a-container)。
