# 在容器中运行使用客户端 egress 路由的 AI agent

## 场景

团队将自主 AI agent（编码 agent、数据 agent）作为集群工作负载运行，但其到 LLM API（例如 Anthropic API）的调用只能经由*您*的网络离开：API 只能通过开发机上的企业代理、VPN 或 SASE 路径访问，集群中并不存在该路径。此外，API key 必须在运行时代理，绝不能写入镜像、deploy spec 或 pod spec。Cornus 同时解决这两个问题：[客户端侧 egress](/zh/topics/egress) 将 agent 的 API 出站调用路由回您的机器，[凭据代理](/zh/topics/credentials) 则交付一个只存在于您笔记本电脑上的 secret。

## 使用的功能

- [客户端侧 egress](/zh/topics/egress)——deploy spec 的 `egress:` block（mode 和 PAC 风格 route script）仅将 `api.anthropic.com` 发往调用方网络。
- [凭据代理](/zh/topics/credentials)——deploy spec 的 `credentials:` block 将 API key 交付给 agent，而不让它进入镜像。
- [`cornus deploy`](/zh/cli/deploy)——前台 `--server` session；relay egress 和 credential fetch 都需要它。
- [Session conduit](/zh/topics/remote-workflows)——使用 `--conduit` 选择访问工作负载自身端口的方式。
- [Deploy spec](/zh/reference/deploy-spec)——`EgressSpec` 和 `CredentialSpec` 字段参考。

## 演练

朴素方案的问题在于：将 key 烘焙进镜像会泄露给任何可拉取镜像或读取 build log 的人；作为普通 pod-spec env var 传递会将其写入集群 control plane。即使已有 key，当唯一合规路径位于企业代理后时，集群中的 pod 也无法连接 API。

**1. 在您的机器上提供 key（永不放到集群中）。**Cornus 在 fetch 时从您的环境生成 credential；这里 `env` backend 从调用方变量读取：

```sh
export ANTHROPIC_API_KEY=sk-ant-...      # stays on your machine
```

**2. 编写 deploy spec。**`egress:` block 使用 PAC script，仅将 `api.anthropic.com` 路由到 client（其他内容保持 `DIRECT`，即 pod 自己的网络）；`credentials:` block 则将 key 作为 agent 已经期望的 `ANTHROPIC_API_KEY` env var 代理进去：

```yaml
name: agent
image: localhost:5000/coding-agent@sha256:1c2d...   # digest-pinned; no secrets inside

env:
  AGENT_TASK: "triage the backlog"

egress:
  mode: proxy                 # caretaker forward proxy, relayed back through the server
  default: cluster            # unmatched destinations egress from the pod's own network
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, "api.anthropic.com")) return "PROXY client";
      return "DIRECT";
    }

credentials:
  sources:
    - name: anthropic
      backend: env                       # mint from a caller-side env var
      config: { var: ANTHROPIC_API_KEY } # non-secret: only the var name travels
      deliver:
        - kind: env
          envVar: ANTHROPIC_API_KEY      # inject into the agent container
```

**3. 在前台 session 部署。**relay egress（`proxy`/`transparent`）和 credential brokering 都经存活 deploy-attach connection 响应，因此需使用 Kubernetes backend 上前台的 `--server` deploy，不能使用 `--detach`：

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com
```

**4. 可选地选择 conduit** 来访问 agent 的端口（health endpoint、UI）。每端口转发是默认方式；按服务名访问的单一 SOCKS5 proxy 是 opt-in 替代：

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com --conduit socks5
```

agent 运行期间 session 保持开启。`Ctrl-C` 会销毁工作负载，并停止响应 egress relay 和 credential fetch。

## 工作原理

两套独立机制在同一 spec 中组合。**Egress** 是每个目标的路由决策。PAC script 会在 caretaker、server 和 client 三处重新检查，因此三者一致，受损 pod 无法提升自己的路由。`api.anthropic.com` 映射到 `PROXY client` 即 `client` route：caretaker forward proxy 将连接经 cornus server tunnel 回您的机器，再由您的机器通过企业 / VPN / SASE 路径连接 API。其他目标返回 `DIRECT`，映射为 `cluster`：由 pod 本地 dial，绝不 relay。由于 `default` 是 `cluster`，启用 egress 不会悄然改道集群内流量；您只将 API 目标显式导出到 client。完整 route table（`client`、`gateway`、`cluster`、`deny`）和 PAC 映射见[客户端侧 egress](/zh/topics/egress)与 [`EgressSpec` 参考](/zh/reference/deploy-spec)。

**凭据代理**与其正交。只有 backend 名称和非 secret `config`（`{ var: ANTHROPIC_API_KEY }`）会到达 server；key 本身在 fetch 时于您的机器上生成，并由 per-pod caretaker sidecar 交付。agent 会照常带着 key 调用 `api.anthropic.com`，该 HTTPS 请求正是 egress policy 经您的网络路由的请求。因此，两项功能在 agent 自己的 HTTPS request 上相遇：credential 将 key 交到它手中，egress 将 packet 带回家。

信任模型是：key 不在镜像（镜像由 digest 固定且无 secret）、不在 deploy spec、也不在线路 control frame 中；它在 live session 上按 fetch 回答，工作负载只能获取自己 deploy session 声明的 credential name，且该 session capability 不可猜测，并在 server relay 和 caretaker 处检查。到 API 的流量由 policy 限制为单一目标。

## 变体

**让 key 完全不进入容器。**上面的 `env` kind delivery 会将 key 一次性 fetch 到 Kubernetes Secret（静态，位于 etcd）。若要最强姿态，请改用 `anthropic-proxy` endpoint provider：caretaker 运行到 API 的 loopback reverse proxy 并自行注入 auth header，因此 agent 不拥有 key。它甚至可使用您的本地 Claude Code / Codex 登录：

```yaml
credentials:
  sources:
    - name: anthropic
      backend: claude-code                 # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional: an Anthropic-compatible gateway
```

**用 rule 而非 PAC 路由。**如没有 PAC 文件，用有序 `rules:` list（或 `--egress-route` CLI flag）替换 `script:`；首个匹配生效：

```yaml
egress:
  mode: proxy
  default: cluster
  rules:
    - { pattern: "api.anthropic.com:443", route: client }
```

**覆盖忽略 proxy env var 的应用。**将 `mode: proxy` 改为 `mode: transparent`；所有 app TCP 都由 nftables redirect 捕获并 relay，因此 agent 不必具备 proxy 感知能力。

**另请参阅：**[客户端侧 egress](/zh/topics/egress) · [凭据代理](/zh/topics/credentials) · [deploy spec](/zh/reference/deploy-spec) · [`cornus deploy`](/zh/cli/deploy) · [egress 操作](/zh/guides/egress) · [凭据操作](/zh/guides/credentials) · [远程工作流](/zh/topics/remote-workflows)
