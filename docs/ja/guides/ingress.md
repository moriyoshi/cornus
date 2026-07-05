# イングレス

Kubernetes バックエンドでワークロードに公開 HTTP(S) ホスト名を割り当てるためのタスク指向レシピです。仕組みについては、[イングレス](/ja/topics/ingress)と[デプロイスペック](/ja/reference/deploy-spec)を参照してください。イングレスは公開済みポートの前段に配置されるため、ワークロードは少なくとも一つのポートを公開している必要があります。`dockerhost` / `containerd` バックエンドは警告を出してイングレスを無視します。

## 自動導出されたホストでワークロードを公開する

イングレスを有効にし、`<subdomain>.<domain>` 形式のホスト名をサーバーのベースドメイン (`CORNUS_INGRESS_DOMAIN`) から導出させます。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true
```

- `subdomain` の既定値はデプロイメント名なので、これは `web.<CORNUS_INGRESS_DOMAIN>` にデプロイされます (Compose トランスレーターは代わりに `<service>.<project>` を使います)。サーバーにベースドメインがなく、自分でも設定しなければデプロイは拒否されます。

**関連項目:** [イングレス](/ja/topics/ingress)、[デプロイスペック](/ja/reference/deploy-spec)

## 明示的なホスト名を設定する

一つ以上のホスト名を同じ Service にルーティングします。各ホスト名はそれぞれ独立したイングレス規則になります。

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- 特別なトークン `@` は、ルートドメイン (ベースドメインそのもので、`<name>.` の接頭辞なし) に使います: `hosts: ["@"]`。

**関連項目:** [イングレス](/ja/topics/ingress)

## cert-manager で HTTPS を提供する

cert-manager の cluster-issuer に証明書を要求します。Cornus が issuer アノテーションを追加し、cert-manager がシークレットを用意します。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # 空の場合は CORNUS_INGRESS_TLS_ISSUER にフォールバックする
```

- `secretName` の既定値は `<name>-tls` です。自前の証明書を使うには、`tls: { secretName: my-existing-tls }` を設定し、`clusterIssuer` を省略します。

**関連項目:** [イングレス](/ja/topics/ingress)、[サーバーを保護する](/ja/guides/security)

## 特定のパス、ポート、クラスをルーティングする

ワークロードが複数のポートを公開する場合や、クラスターに複数のイングレスコントローラーがある場合に、既定値を上書きします。

```yaml
ingress:
  hosts: ["api.example.com"]
  path: /v1
  pathType: Prefix                       # Exact または ImplementationSpecific も指定可能
  port: 8443                             # 公開済みコンテナポートと一致する必要がある
  className: nginx                       # 空の場合は CORNUS_INGRESS_CLASS、それもなければクラスターの既定値を使う
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

**関連項目:** [イングレス](/ja/topics/ingress)、[デプロイスペック](/ja/reference/deploy-spec)

## Compose サービスを公開する

Service に `x-cornus-ingress` を追加します (プロジェクトレベルでは既定値を指定できますが、イングレス自体は有効になりません)。

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.example.com
      tls: { cluster_issuer: letsencrypt-prod }
```

**関連項目:** [イングレス](/ja/topics/ingress)、[Compose、devcontainers、docker CLI](/ja/guides/compose-devcontainers-docker)
