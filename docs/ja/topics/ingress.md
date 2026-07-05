# パブリックイングレス

イングレスは[クライアント側エグレス](/ja/topics/egress)の受信側に当たる機能です。ワークロードの公開ポートの前段にパブリックな **HTTP(S) Ingress** を作成するため、サービスは[ポート転送](/ja/guides/networking)や[トンネル](/ja/topics/tunnels)だけでなく、実際のホスト名で到達可能になります。これは **Kubernetes バックエンドの機能**です。`dockerhost` と `containerd` バックエンドは警告を出して無視します。ワークロードの `ClusterIP` Service の前段となるため、仕様は少なくとも 1 つのポートを公開しなければなりません。

イングレスはデプロイスペックの `ingress:` ブロックまたは Compose のポータブル拡張 `x-cornus-ingress:` で明示的に有効化します。暗黙に有効になることはありません。

## 有効化する

以下のいずれでもイングレスが有効になります。

- デプロイスペックの `ingress: { enabled: true }`、
- Compose の素の `x-cornus-ingress: {}` (または `x-cornus-ingress: true`)、または
- 空でないホスト (`hosts:` / Compose `host:`)。これは `enabled` を暗黙に有効化します。

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }     # the Service the Ingress fronts
ingress:
  enabled: true                        # host auto-derived from the server domain
  tls: {}                              # HTTPS via the server's default issuer
```

## ホスト解決

- **明示的な `hosts:`** — 各ホスト名は独自のイングレス規則になります。すべて 1 つの TLS 項目を共有し、同じ Service の前段になります。特別なトークン `@` は、DNS ゾーンの慣例に従い、`<name>.` 接頭辞のない**頂点ドメイン** (ベースドメイン自体) に対応します。
- **自動導出 (`hosts` が空の場合)** — バックエンドは `<subdomain>.<domain>` という単一のホスト名を構築します。
  - `domain` はベースドメインのクライアント側上書きです。空ならサーバー既定の `CORNUS_INGRESS_DOMAIN` にフォールバックします。
  - `subdomain` の既定値はデプロイメント名です。Compose トランスレーターは `<service>.<project>` に設定するため、プロジェクトごとに異なるホスト名になります。ラベルは DNS-1123 に合わせて正規化されます。
  - 明示的なホスト名もベースドメインもないデプロイは拒否されます。

## ルーティング

| フィールド | 既定値 | 意味 |
| --- | --- | --- |
| `path` | `/` | ルーティングする HTTP パスの接頭辞。 |
| `pathType` | `Prefix` | Kubernetes のパスマッチ型: `Prefix`、`Exact`、`ImplementationSpecific`。 |
| `port` | first 公開済み | ルーティング先のコンテナポート。非ゼロ値は仕様の公開済みポートのいずれかと一致しなければなりません。 |
| `className` | サーバー既定 | `IngressClassName`。空なら `CORNUS_INGRESS_CLASS`、次にクラスターの既定 IngressClass へフォールバックします。 |
| `annotations` | — | コントローラー固有の設定 (書き換え先、本文サイズなど) のため、Ingress オブジェクトにそのまま統合します。 |

## TLS

`tls:` ブロックはホストに HTTPS を要求します。プレーンな HTTP では省略してください。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # cert-manager provisions the cert
    # secretName: app-tls               # or bring your own existing secret
```

- `secretName` は既存 TLS シークレットの名前です。空なら `<name>-tls` が既定で、`clusterIssuer` (またはサーバー既定) が設定されていれば cert-manager が作成します。
- `clusterIssuer` は `cert-manager.io/cluster-issuer` annotation を設定します。空ならサーバー既定の `CORNUS_INGRESS_TLS_ISSUER` にフォールバックします。

## サーバー側既定値とドメインポリシー

運用者はフォールバックを設定できるため、ワークロードはすべて既定値のままイングレスを有効化できます (Helm の `ingress.*` 値は環境変数として設定されます)。空のままにすれば各ワークロードが自身のホスト名を指定する必要があり、何も自動公開されません。

| 環境変数 | Helm 値 | 意味 |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | ホスト自動導出の base wildcard ドメイン (例: `preview.example.com`)。 |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | 既定の `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | TLS イングレス用の既定 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | true (かつドメイン設定済み) なら、解決したホスト名が `domain` の外にあるワークロードを拒否します。共有コントローラーがクライアントの指定だけで任意のホスト名を提供させられることを防ぎます。 |

## Compose で使う

プロジェクトレベルまたはサービスごとに `x-cornus-ingress` を使います。プロジェクトレベルのブロックは**既定値**を提供するだけで、イングレスは有効にしません。イングレスはサービスごとに明示的に有効化したままです。`x-` 接頭辞により、ファイルは標準の Compose ツールに対して有効なままです。

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.example.com            # scalar sugar; unioned with hosts:
      tls: { cluster_issuer: letsencrypt-prod }
```

完全な `IngressSpec` / `IngressTLS` フィールドは[デプロイスペック](/ja/reference/deploy-spec)、サーバー既定は[Helm chart 値](/ja/reference/helm-values)、タスク指向の手順は[イングレス](/ja/guides/ingress)を参照してください。
