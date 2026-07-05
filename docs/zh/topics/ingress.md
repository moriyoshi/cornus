# 公共 Ingress

Ingress 是[客户端侧 egress](/zh/topics/egress)的入站对应能力：它请求一个面向工作负载已发布端口的公共 **HTTP(S) Ingress**，使服务可通过真实 hostname 访问，而不仅能通过[端口转发](/zh/guides/networking)或[tunnel](/zh/topics/tunnels)访问。这是**Kubernetes 后端功能**——`dockerhost` 和 `containerd` 后端会警告并忽略它——且它面向工作负载的 `ClusterIP` Service，因此 spec 至少要发布一个端口。

Ingress 通过 deploy spec 的 `ingress:` block 或 Compose 的可移植 `x-cornus-ingress:` extension 显式启用，绝不会隐式打开。

## 启用方式

以下任一方式都会启用 ingress：

- deploy spec 中的 `ingress: { enabled: true }`；
- Compose 中的裸 `x-cornus-ingress: {}`（或 `x-cornus-ingress: true`）；
- 任意非空 host（`hosts:` / Compose `host:`），其隐含 `enabled`。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }     # the Service the Ingress fronts
ingress:
  enabled: true                        # host auto-derived from the server domain
  tls: {}                              # HTTPS via the server's default issuer
```

## Host 解析

- **显式 `hosts:`**——每个 hostname 成为单独的 Ingress rule，共用一个 TLS entry 并面向同一 Service。特殊 token `@` 映射为 **apex**（base domain 本身，没有 `<name>.` 前缀），遵循 DNS zone 惯例。
- **自动派生（`hosts` 为空时）**——后端构造唯一 host `<subdomain>.<domain>`：
  - `domain` 是对 base domain 的客户端 override；为空时回退到服务器默认 `CORNUS_INGRESS_DOMAIN`。
  - `subdomain` 默认为部署名称（Compose translator 设为 `<service>.<project>`，使不同项目得到不同 hostname）；label 会被清理为 DNS-1123。
  - 既无显式 host、也无任何 base domain 的部署会被拒绝。

## 路由

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `path` | `/` | 要路由的 HTTP path prefix。 |
| `pathType` | `Prefix` | Kubernetes path match type：`Prefix`、`Exact` 或 `ImplementationSpecific`。 |
| `port` | 第一个已发布端口 | 要路由到的 container port；非零值必须匹配 spec 的一个已发布端口。 |
| `className` | 服务器默认值 | `IngressClassName`；为空时回退到 `CORNUS_INGRESS_CLASS`，再回退到集群默认 IngressClass。 |
| `annotations` | — | 原样合并到 Ingress object，用于 controller 特定设置（rewrite target、body size 等）。 |

## TLS

`tls:` block 会为 host 请求 HTTPS；省略它则使用纯 HTTP。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # cert-manager provisions the cert
    # secretName: app-tls               # or bring your own existing secret
```

- `secretName` 指定已有 TLS secret；为空时默认 `<name>-tls`，当设置 `clusterIssuer`（或服务器默认值）时由 cert-manager 提供。
- `clusterIssuer` 设置 `cert-manager.io/cluster-issuer` annotation；为空时回退到服务器默认 `CORNUS_INGRESS_TLS_ISSUER`。

## 服务器默认值和 domain policy

operator 可设置 fallback，使工作负载可在全部值使用默认值的情况下启用 ingress（Helm `ingress.*` value 会呈现为 env）。留空则要求每个工作负载指定自己的 host，从而不会自动公开任何内容。

| Env var | Helm value | 含义 |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | 用于 host 自动派生的 base wildcard domain（例如 `preview.example.com`）。 |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | 默认 `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | TLS ingress 的默认 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | 为 true（且设置 domain）时，拒绝解析后 host 不在 `domain` 内的工作负载，避免共享 controller 被客户端要求提供任意 hostname。 |

## 在 Compose 中

在项目级或按服务使用 `x-cornus-ingress`。项目级 block 提供**默认值**，但不启用 ingress——必须逐服务 opt-in。`x-` 前缀使文件保持为标准 Compose 工具可用。

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.example.com            # scalar sugar; unioned with hosts:
      tls: { cluster_issuer: letsencrypt-prod }
```

完整 `IngressSpec` / `IngressTLS` 字段参见 [deploy spec](/zh/reference/deploy-spec)，服务器默认值参见 [Helm chart value](/zh/reference/helm-values)，面向任务的步骤参见 [Ingress](/zh/guides/ingress) 指南。
