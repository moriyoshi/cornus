# Ingress

以下是面向任务的操作方法，用于在 Kubernetes 后端为工作负载提供公开 HTTP(S) 主机名。原理参见 [Ingress](/zh/topics/ingress) 和 [deploy spec](/zh/reference/deploy-spec)。Ingress 面向已发布端口，因此工作负载至少必须发布一个端口；`dockerhost` / `containerd` 后端会警告并忽略它。

## 在自动派生的主机名上暴露工作负载

启用 ingress，让服务器依据 base domain（`CORNUS_INGRESS_DOMAIN`）将主机名派生为 `<subdomain>.<domain>`。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true
```

- `subdomain` 默认为部署名称，因此会部署到 `web.<CORNUS_INGRESS_DOMAIN>`（Compose translator 使用 `<service>.<project>`）。如果服务器没有 base domain 且您未设置，部署会被拒绝。

**另请参阅：**[Ingress](/zh/topics/ingress)、[deploy spec](/zh/reference/deploy-spec)

## 设置显式 hostname

将一个或多个 hostname 路由至同一 Service；每个都会成为独立的 Ingress rule。

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- 对 apex（base domain 本身，没有 `<name>.` 前缀）使用特殊 token `@`：`hosts: ["@"]`。

**另请参阅：**[Ingress](/zh/topics/ingress)

## 使用 cert-manager 提供 HTTPS

向 cert-manager cluster-issuer 请求 certificate；cornus 添加 issuer annotation，cert-manager 负责创建 secret。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # empty falls back to CORNUS_INGRESS_TLS_ISSUER
```

- `secretName` 默认为 `<name>-tls`。如要自行提供 certificate，请设置 `tls: { secretName: my-existing-tls }` 并省略 `clusterIssuer`。

**另请参阅：**[Ingress](/zh/topics/ingress)、[保护服务器](/zh/guides/security)

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

**另请参阅：**[Ingress](/zh/topics/ingress)、[deploy spec](/zh/reference/deploy-spec)

## 暴露 Compose 服务

向服务添加 `x-cornus-ingress`（项目级配置提供默认值，但不会启用 ingress）。

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.example.com
      tls: { clusterIssuer: letsencrypt-prod }
```

**另请参阅：**[Ingress](/zh/topics/ingress)、[Compose、devcontainer 与 docker CLI](/zh/guides/compose-devcontainers-docker)
