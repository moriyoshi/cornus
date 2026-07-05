# Ingress

Ingress 是[客户端侧 egress](/zh/guides/egress)的入站对应能力：它请求一个面向工作负载已发布端口的公共 **HTTP(S) Ingress**，使服务可通过真实 hostname 访问，而不仅能通过[端口转发](/zh/guides/networking)或[隧道](/zh/guides/tunnels)访问。这是**Kubernetes 后端功能**——`dockerhost` 和 `containerd` 后端会警告并忽略它——且它面向工作负载的 `ClusterIP` Service，因此 spec 至少要发布一个端口。

Ingress 通过 deploy spec 的 `ingress:` block 或 Compose 的可移植 `x-cornus-ingress:` extension 显式启用，绝不会隐式打开。

要**从本机**访问 ingress 主机名 (包括在主机类后端上，且无需真实 DNS)，请跳到[通过 conduit 从本机访问 ingress](#通过-conduit-从本机访问-ingress)，它经由 SOCKS5 conduit 路由。

## 工作原理

### 启用方式

以下任一方式都会启用 ingress：

- deploy spec 中的 `ingress: { enabled: true }`；
- Compose 中的裸 `x-cornus-ingress: {}` (或 `x-cornus-ingress: true`)；
- 任意非空 host (`hosts:` / Compose `host:`)，其隐含 `enabled`。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }     # the Service the Ingress fronts
ingress:
  enabled: true                        # host auto-derived from the server domain
  tls: {}                              # HTTPS via the server's default issuer
```

### Host 解析

- **显式 `hosts:`**——每个 hostname 成为单独的 Ingress rule，共用一个 TLS entry 并面向同一 Service。特殊 token `@` 映射为 **apex** (base domain 本身，没有 `<name>.` 前缀)，遵循 DNS zone 惯例。
- **自动派生 (`hosts` 为空时)**——后端构造唯一 host `<subdomain>.<domain>`：
  - `domain` 是对 base domain 的客户端 override；为空时回退到服务器默认 `CORNUS_INGRESS_DOMAIN`。
  - `subdomain` 默认为部署名称 (Compose translator 设为 `<service>.<project>`，使不同项目得到不同 hostname)；label 会被清理为 DNS-1123。
  - 既无显式 host、也无任何 base domain 的部署会被拒绝。

### 路由

Ingress 面向工作负载的某个**已发布 container port** (即其 `ClusterIP` Service)，因此 spec 至少要发布一个端口。启用了 ingress 却没有 `ports:` 的部署会以 `ingress requires the deployment to publish at least one port` 被拒绝。

| Deploy spec 字段 | Compose 键 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `path` | `path` | `/` | 要路由的 HTTP path prefix。 |
| `pathType` | `path_type` | `Prefix` | Kubernetes path match type：`Prefix`、`Exact` 或 `ImplementationSpecific` (区分大小写——小写的 `prefix` 会被拒绝)。 |
| `port` | `port` | 第一个已发布端口 | 要路由到的 **container** port，即应用监听的端口，**不是**公共 HTTP/HTTPS 端口 (后者仍是 80/443)。为零时使用第一个已发布端口；非零值必须匹配工作负载的某个已发布 container port，否则会报 `ingress: port N is not among the deployment's published container ports`。 |
| `className` | `class_name` | 服务器默认值 | `IngressClassName`；为空时回退到 `CORNUS_INGRESS_CLASS`，再回退到集群默认 IngressClass。 |
| `annotations` | `annotations` | — | 原样合并到 Ingress object，用于 controller 特定设置 (rewrite target、body size 等)。 |

Deploy spec 使用第一列的 camelCase 字段名，Compose 的 `x-cornus-ingress` extension 使用第二列的 snake_case 键 (参见[暴露 Compose 服务](#暴露-compose-服务))。

### 服务器默认值和 domain policy

Operator 可设置 fallback，使工作负载可在全部值使用默认值的情况下启用 ingress (Helm `ingress.*` value 会呈现为 env)。留空则要求每个工作负载指定自己的 host，从而不会自动公开任何内容。

| Env var | Helm value | 含义 |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | 用于 host 自动派生的 base wildcard domain (例如 `preview.example.com`)。 |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | 默认 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | TLS ingress 的默认 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | 为 true (且设置 domain) 时，拒绝解析后 host 不在 `domain` 内的工作负载，避免共享 controller 被客户端要求提供任意 hostname。 |

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)、[Helm chart 值](/zh/reference/helm-values)

## 在自动派生的主机名上暴露工作负载

启用 ingress，让服务器依据 base domain (`CORNUS_INGRESS_DOMAIN`) 将主机名派生为 `<subdomain>.<domain>`。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true
```

- `subdomain` 默认为部署名称，因此会部署到 `web.<CORNUS_INGRESS_DOMAIN>` (Compose translator 使用 `<service>.<project>`)。如果服务器没有 base domain 且您未设置，部署会被拒绝。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)

## 设置显式 hostname

将一个或多个 hostname 路由至同一 Service；每个都会成为独立的 Ingress rule。

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- 对 apex (base domain 本身，没有 `<name>.` 前缀) 使用特殊 token `@`：`hosts: ["@"]`。

## 使用 cert-manager 提供 HTTPS

向 cert-manager cluster-issuer 请求 certificate；cornus 添加 issuer annotation，cert-manager 负责创建 secret。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # empty falls back to CORNUS_INGRESS_TLS_ISSUER
```

- `tls:` block 会为 host 请求 HTTPS；省略它则使用纯 HTTP。
- `secretName` 指定已有 TLS secret；为空时默认 `<name>-tls`，当设置 `clusterIssuer` (或服务器默认值) 时由 cert-manager 提供。如要自行提供已有的 secret，请设置 `tls: { secretName: my-existing-tls }` 并省略 `clusterIssuer`。
- `clusterIssuer` 设置 `cert-manager.io/cluster-issuer` annotation；为空时回退到服务器默认 `CORNUS_INGRESS_TLS_ISSUER`。

**另请参阅：**[安全与认证](/zh/guides/security)

## 使用自己的证书

将证书规则写入选定的连接配置文件。`pattern` 是可选的；省略时，Cornus 会从证书的每个 DNS SAN 创建 selector。

```yaml
contexts:
  prod:
    server: https://cornus.example.com
    conduit:
      ingress:
        mode: native
        certificates:
          - certificate: /etc/cornus/example-com.pem
            key: /etc/cornus/example-com-key.pem
          - pattern: api.other.example
            certificate: /etc/cornus/api.pem
            key: /etc/cornus/api-key.pem
```

Pattern 可以是精确名称，也可以是形如 `*.example.com` 的单标签通配符。显式指定的 pattern 必须被证书的 SAN 覆盖。精确匹配优先于通配符；通配符之间以最长后缀优先。

在 `emulate` 模式下，SNI 决定本地 ingress proxy 提供哪张证书；未匹配的名称使用常规的生成 CA 回退。在 `native` 模式下，Cornus 会在部署前匹配每个具体的 ingress host，按选中的证书对 host 分组，创建由工作负载 Deployment 拥有的稳定 `kubernetes.io/tls` Secret，并将它们接入 Kubernetes Ingress。重新应用会就地轮换 Secret 数据，并删除不再需要的托管 Secret。

Native 的托管证书要求显式给出具体的 `ingress.hosts`：请在 spec 中展开自动派生的 host 或 `@` apex token。每个 host 都必须匹配某条证书规则。由于证书是持久的 Kubernetes 状态而非客户端侧 conduit 监听器，这一方式在分离运行的 Compose 和 deploy 操作中同样有效。

Native 路径会在 deploy 请求中发送私钥材料。因此 Cornus 会在请求序列化之前拒绝经由远程明文 HTTP 的调用；请使用 HTTPS、SSH 隧道配置文件，或诸如 Kubernetes 端口转发这样的 loopback 端点。密钥绝不会出现在状态或诊断输出中。完整字段参见[连接配置参考](/zh/reference/connection-config)。

## 路由特定 path、port 或 class

当工作负载发布多个端口或集群有多个 ingress controller 时，覆盖默认值。

```yaml
ingress:
  hosts: ["api.example.com"]
  path: /v1
  pathType: Prefix                       # or Exact / ImplementationSpecific
  port: 8443                             # must match a published container port
  className: nginx                       # empty uses CORNUS_INGRESS_CLASS, then the cluster default
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)

## 暴露 Compose 服务

在项目级或按服务使用 `x-cornus-ingress`。项目级 block 提供**默认值**，但不启用 ingress——必须逐服务 opt-in。`x-` 前缀使文件保持为标准 Compose 工具可用。

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]                 # the ingress fronts a published port (here container :80)
    x-cornus-ingress:
      host: web.example.com            # scalar sugar; unioned with hosts:
      port: 80                          # container port to route to; omit to use the first published
      path_type: Prefix
      tls: { cluster_issuer: letsencrypt-prod }
```

这里有三点容易出错，且三者都会静默失败或在部署时失败：

- **必须发布端口。** 服务需要一个 `ports:` 条目——ingress 面向的是已发布的 container port。仅在内部监听的服务也必须列出它 (`ports: ["80"]`，或使用长格式 `- target: 80` 以避免绑定 host port，这同时也能规避与其他服务的 host port 冲突)。
- **`port` 是 container port，不是公共端口。** 它是应用在容器内监听的端口 (例如 `3000`、`8000`)，绝不是 `80`/`443`——TLS 和公共 HTTP(S) 端口由 `tls: {}` 为您处理。
- **键使用 snake_case。** 在 `x-cornus-ingress` 内应写 `path_type`、`class_name`，在 `tls:` 下写 `secret_name` / `cluster_issuer`，而不是 deploy spec 的 camelCase (`pathType`、`className`、`secretName`、`clusterIssuer`)。camelCase 键属于未知字段，会被静默忽略。值同样区分大小写：应写 `path_type: Prefix`，而非 `prefix`。若只需以服务器默认 issuer 请求 HTTPS，裸 `tls: {}` 就足够了。

**另请参阅：**[deploy spec](/zh/reference/deploy-spec)、[Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker)

## 通过 conduit 从本机访问 ingress

上面的公共 Ingress 为工作负载提供了真实主机名，但要从开发机访问它，仍需有指向集群 ingress controller 的 DNS。[SOCKS5 conduit](/zh/guides/networking) 填补了这一空缺：只需一项浏览器代理设置，工作负载的 ingress 主机名即可通过代理解析——无需编辑 `/etc/hosts`，也无需真实 DNS。它是**选择性启用**的，依托 socks5 conduit (`--conduit socks5`)，有两种模式：

- **native**——到集群*真实* ingress controller Service 的透明隧道。浏览器的 TLS ClientHello (SNI) 和 `Host` 头会直接透传，因此由真正的 controller 完成 Host/path 路由，并使用集群自身的证书终止 TLS。仅限 Kubernetes，且您的会话必须具备直接的集群访问权限 (端口转发 / kube-auth 配置文件)。Controller Service 由服务器发现并通过 `GET /.cornus/v1/info` 通告 (可用 `CORNUS_INGRESS_CONTROLLER=<namespace>/<service>[:http/https]` 覆盖)。
- **emulate**——一个小型的客户端侧 HTTP(S) reverse proxy，按 `Host`/path 经 conduit 路由到工作负载的 container port，并使用匹配的用户提供证书或生成的回退证书终止 TLS。适用于**所有**后端 (包括没有 controller 的 `dockerhost` / `containerd`)。**开箱即用的 TLS 信任：**如果已安装 [mkcert](https://github.com/FiloSottile/mkcert) 并运行过 `mkcert -install`，模拟 ingress 会用 mkcert 已受信任的本地 CA 签发叶证书，因此浏览器和 `curl` **无需任何手动步骤**即可信任 `https://<host>/`。否则会回退到一个持久化的自签名 CA (`~/.local/share/cornus/ingress-ca.pem`)，只需信任一次 (或传入 `--cacert`)。显式的 `--ingress-emulate-ca` / `--ingress-emulate-ca-key` 会覆盖以上两者。

按运行启用，或固定到配置文件中：

```sh
# per run
cornus compose up --conduit socks5 --ingress-conduit native
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate

# or pin it in the connection profile (see cornus config)
cornus config set-context prod --conduit-mode socks5 --ingress-conduit native
```

将浏览器的 SOCKS5 代理指向该 conduit (启用**远程 DNS** / socks5h)，然后打开工作负载的 ingress 主机名，例如 `https://web.example.com/`。优先级为 `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > 配置文件；`off` 会禁用它。

`cornus setup` 会探测服务器并为您选择默认值：发现 controller 时建议 **native**，只有 ingress domain 而没有可达 controller 时建议 **emulate**，否则为 **off**。

两点说明：native 和 emulate 适用于同一份 `x-cornus-ingress` spec——存在真实 controller 时优先使用 native，emulate 是可移植的回退方案。Controller 的 `annotations` / `className` / cert-manager 字段仅限 Kubernetes，模拟模式会忽略它们。

**另请参阅：**[网络与 conduit](/zh/guides/networking)、[cornus setup](/zh/cli/setup)
