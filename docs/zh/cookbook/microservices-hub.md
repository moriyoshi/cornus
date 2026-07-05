# 通过 hub 覆盖网络连接微服务

## 场景

多个 service 独立部署——不同 spec、不同 rollout schedule，也可能在不同 node、cluster，甚至 NAT 后的开发者笔记本电脑。每个 service 都需通过 stable name 访问其他 service，无需硬编码 IP 或部署 service mesh。Cornus server 兼作 **star hub**：每个 workload 作为 spoke 加入，注册自己 host 的 service，并按名称访问其他 spoke 的 service——hub relay byte。

## 使用的功能

- 工作负载到工作负载 hub overlay 及其 relay model——见[工作负载 Hub](/zh/guides/hub)。
- In-cluster workload 的 deploy spec `hub:` block——见[Deploy spec](/zh/reference/deploy-spec)。
- 从任意位置（含笔记本电脑）加入 overlay 的 `cornus hub`——见 [`cornus hub`](/zh/cli/hub)。

## 演练

1. **部署 database 并在 hub 上 export。**`hub:` block 将 workload 加入 overlay。`export` 指明 workload host 的 service。若 hub 无法直接 dial pod，请将 export 标为 `deliver: true`，使 hub relay 到 pod，再由 pod dial localhost port。

   ```yaml
   # db.yaml
   name: db
   image: cornus.example:5000/postgres:16
   hub:
     identity: db
     export:
       - { name: db, port: 5432, deliver: true }
   ```

   ```sh
   cornus deploy -f db.yaml --server http://cornus.example:5000 --detach
   ```

2. **部署 API 并按名称 import database。**`import` 列出此 workload 访问的 service。每个 import，backend 分配 synthetic loopback IP、配置 DNS record 并 bind caretaker listener，因此 API container 内对 `db:5432` 的普通 connection 无需应用修改就会进入 hub。

   ```yaml
   # api.yaml
   name: api
   image: cornus.example:5000/api:v1
   env:
     DATABASE_URL: postgres://db:5432/shop
   hub:
     identity: api
     export:
       - { name: api, port: 8080 }
     import:
       - { name: db, ports: [5432] }
   ```

   ```sh
   cornus deploy -f api.yaml --server http://cornus.example:5000 --detach
   ```

   API 以 `db:5432` 访问 database，并作为 `api` 提供给 import 它的其他内容——两端都无需硬编码 address。

3. **从笔记本电脑访问 overlay service。**NAT 后的 developer 使用 `cornus hub` 作为 spoke 加入同一 overlay，bind 一个 local loopback port 并 forward 到 overlay 的 `db` service。Server 从 `--server` 或 selected connection profile 解析。

   ```sh
   cornus hub --identity laptop --reach db=127.0.0.1:5432
   # now: psql 'host=127.0.0.1 port=5432 dbname=shop ...'
   ```

   同一 command 还可用 `--register name=host:port` 将本地运行的 service 提供给 overlay，使笔记本电脑上开发中的 service 在迭代时可由 cluster 按名称访问。

## 工作原理

每个 participant 是一个 **spoke**，server 是 hub。Spoke 以两种 mode 注册它 host 的每个 service：

- **dial-direct**——service 带 hub 可达 address 注册，hub 自己 dial。
- **delivery（relay）**——service 没有可达 address（export 使用 `deliver: true`，或所有 `cornus hub --register`）。访问时 hub 向 hosting spoke 反向打开 ingress stream，spoke dial 自己的 local target 并 splice。这使 NAT 后笔记本电脑或跨 cluster pod 可达——hub 无需通向它的 route。

要访问 peer，source spoke 打开一个标明 service 的 data stream；hub lookup 后自行 dial，或经 owning spoke delivery，然后 copy byte。TCP 和 UDP 都可工作（`/udp` 风格 `protocol: udp` 选择 UDP）。In-cluster workload 完全通过 `hub:` block 声明；笔记本电脑或集群外 host 通过 CLI `cornus hub --register` / `--reach` 加入同一 overlay。完整 field 集——`export`、`import`、`importDynamic`、`identity`——见 [Deploy spec 参考](/zh/reference/deploy-spec)。

Access 由两个可选 policy matrix 管理，且仅在配置时强制：**reach** matrix（caller identity 到允许 callee service，`CORNUS_HUB_POLICY`）和 **register** matrix（identity 到可 host service name，`CORNUS_HUB_REGISTER_POLICY`）。Spoke 声明 `identity`，但 mTLS 下 identity 取自 verified client certificate CommonName，因此 policy 基于 spoke 无法伪造的 credential。identity 和 policy model 见[工作负载 Hub](/zh/guides/hub)。

## 变体

- **Dynamic discovery。**使用带共享 port set 的 `importDynamic` 代替 static `import` list；caretaker 订阅 hub catalog push，并随 service 出现或消失，在每个 cataloged service 上 bind listener。
- **UDP service。**在 export / import 上添加 `protocol: udp`，以 byte-copy 方式处理 UDP flow。
- **Cross-backend。**hub relay byte，因此只要连接到同一 hub，位于不同 backend 或 cluster 的 spoke 都能以相同方式互相访问。

**另请参阅：**[工作负载 Hub](/zh/guides/hub) · [网络与 conduit](/zh/guides/networking) · [Deploy spec](/zh/reference/deploy-spec) · [`cornus hub`](/zh/cli/hub)
