# 使用远程集群

以下是从自己的机器驱动 remote cornus server 的操作方法，同时文件、secret 和 kube access 保留在本地。完整 field 集见[连接配置](/zh/reference/connection-config)和[远程工作流](/zh/topics/remote-workflows)。

## 将一次性 command 指向 remote server

无需创建 profile，就为单条 command 指定 server。

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` 优先于 `CORNUS_SERVER`，后者优先于 selected profile。Endpoint 接受 `http(s)://` 或 `ws(s)://`。
- Bearer token 从 `CORNUS_TOKEN`（或 profile）读取；它从不是 command flag。

**另请参阅：**[远程工作流](/zh/topics/remote-workflows)、[cornus deploy](/zh/cli/deploy)

## 为 remote server 创建 connection profile

一次保存 server URL、token 和 TLS material，使 command 无需 command-line 参数。

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` 默认替换命名 context；传入 `--merge` 可原地编辑并保留未设置 field。
- 分层顺序为 `--from-file`（base）、flag、`--from-file-override`（top）。

**另请参阅：**[cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)

## 通过 profile 自动 port-forward 至 in-cluster server

对没有 ingress 的 in-cluster cornus，通过指定其 Service 而非 URL 访问；CLI 在每条 command 前后打开 port-forward。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

- 保持 `--server` 未设置：带有 `port-forward` block 的空 `server` 会 dial in-cluster Service。
- `--pf-kube-context` 选择 kubeconfig context；`--pf-service` 跳过 Service auto-detection。

**另请参阅：**[远程工作流](/zh/topics/remote-workflows)、[cornus config](/zh/cli/config)

## 从自己的 kube access 签发短期 credential

通过 Kubernetes TokenRequest API 从 cluster ServiceAccount 派生 bearer token，而不是存储 static token。

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

- `--kube-auth-audience` 必须匹配 server 的 `CORNUS_JWT_AUDIENCE`。
- `--kube-auth-namespace` / `--kube-auth-kube-context` 默认使用 `--pf-*` 值；`--kube-auth-expiration-seconds` 默认 `3600`。

**另请参阅：**[连接配置](/zh/reference/connection-config)、[认证与 TLS](/zh/topics/auth-and-tls)

## 切换、查看和删除 profile

以 kubeconfig 风格管理 connection profile 集合。

```sh
cornus config get-contexts          # list profiles (current marked *)
cornus config use-context staging   # make staging the default
cornus config current-context       # print the current context name
cornus config view                  # print the file (tokens redacted)
cornus config delete-context old    # remove a profile
```

- `view --show-tokens` 打印 bearer token；`view --export --context prod` 输出一个可 round-trip 至 `set-context --from-file` 的 bare Context object。
- `delete-context` 若删除的 context 是 current，则清除 current-context pointer。

**另请参阅：**[cornus config](/zh/cli/config)

## 为 profile 设置默认 namespace

记录 cornus install 的 namespace，使 cluster detection 和 kube-auth 默认使用它。

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` 会自动检测 Service 与 port，除非设置 `--pf-service` 或 `--no-detect`；添加 `--no-detect` 可不联系 cluster 而保存 namespace。

**另请参阅：**[cornus config](/zh/cli/config)、[连接配置](/zh/reference/connection-config)

## 让 client-to-workload 流量经 server 路由

强制 log 和 port-forward 经 cornus server proxy，而不是用 kubeconfig 直接访问 pod（仅 cluster profile）。

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # per-command override
```

- 优先级为每 command 的 `--via-server` / `--no-via-server` flag、`CORNUS_VIA_SERVER`（`1`/`0`）、profile field。
- 该设置只改变 transport；`kube-auth` profile 仍会签发 cluster token。

**另请参阅：**[远程工作流](/zh/topics/remote-workflows)、[cornus port-forward](/zh/cli/port-forward)

## 对 remote deployment tail log 和 exec

通过已解析的 server 或 profile stream workload log，并在其中运行 command。

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- Cluster profile 中，log 与 exec 使用 kubeconfig 直达 pod，必要时 fallback 到 server proxy；`--via-server` 强制 server-routed path。
- `exec` 中 `--` 后的所有内容原样传给 command；stdin 不是 terminal 时，`-t` 降级为 plain stream。

**另请参阅：**[cornus exec](/zh/cli/exec)、[cornus compose](/zh/cli/compose)
