# イングレス

イングレスは[クライアント側エグレス](/ja/guides/egress)の受信側に当たる機能です。ワークロードの公開ポートの前段にパブリックな **HTTP(S) Ingress** を作成するため、サービスは[ポート転送](/ja/guides/networking)や[トンネル](/ja/guides/tunnels)だけでなく、実際のホスト名で到達可能になります。これは **Kubernetes バックエンドの機能**です。`dockerhost` と `containerd` バックエンドは警告を出して無視します。ワークロードの `ClusterIP` Service の前段となるため、仕様は少なくとも 1 つのポートを公開しなければなりません。

イングレスはデプロイスペックの `ingress:` ブロックまたは Compose のポータブル拡張 `x-cornus-ingress:` で明示的に有効化します。暗黙に有効になることはありません。

自分のマシンからイングレスホストに到達する方法は、ホストバックエンドでも実際の DNS なしでも使えます。[自分のマシンからイングレスに到達する](#conduit-経由で自分のマシンからイングレスに到達する)を参照してください。SOCKS5 conduit 経由で経路します。

## 仕組み

### 有効化する

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

### ホスト解決

- **明示的な `hosts:`** — 各ホスト名は独自のイングレス規則になります。すべて 1 つの TLS 項目を共有し、同じ Service の前段になります。特別なトークン `@` は、DNS ゾーンの慣例に従い、`<name>.` 接頭辞のない**頂点ドメイン** (ベースドメイン自体) に対応します。
- **自動導出 (`hosts` が空の場合)** — バックエンドは `<subdomain>.<domain>` という単一のホスト名を構築します。
  - `domain` はベースドメインのクライアント側上書きです。空ならサーバー既定の `CORNUS_INGRESS_DOMAIN` にフォールバックします。
  - `subdomain` の既定値はデプロイメント名です。Compose トランスレーターは `<service>.<project>` に設定するため、プロジェクトごとに異なるホスト名になります。ラベルは DNS-1123 に合わせて正規化されます。
  - 明示的なホスト名もベースドメインもないデプロイは拒否されます。

### ルーティング

イングレスはワークロードの**公開済みコンテナポート**のいずれか (その `ClusterIP` Service) の前段になるため、仕様は少なくとも 1 つのポートを公開しなければなりません。`ports:` なしでイングレスを有効化したデプロイは `ingress requires the deployment to publish at least one port` で拒否されます。

| デプロイスペックのフィールド | Compose のキー | 既定値 | 意味 |
| --- | --- | --- | --- |
| `path` | `path` | `/` | ルーティングする HTTP パスの接頭辞。 |
| `pathType` | `path_type` | `Prefix` | Kubernetes のパスマッチ型: `Prefix`、`Exact`、`ImplementationSpecific` (大文字小文字を区別します。小文字の `prefix` は拒否されます)。 |
| `port` | `port` | 最初の公開済みポート | ルーティング先の**コンテナ**ポート、つまりアプリが待ち受けているポートです。パブリックな HTTP/HTTPS ポートでは**ありません** (そちらは 80/443 のままです)。ゼロなら最初の公開済みポートを使います。非ゼロ値はワークロードの公開済みコンテナポートのいずれかと一致しなければならず、そうでなければ `ingress: port N is not among the deployment's published container ports` になります。 |
| `className` | `class_name` | サーバー既定 | `IngressClassName`。空なら `CORNUS_INGRESS_CLASS`、次にクラスターの既定 IngressClass へフォールバックします。 |
| `annotations` | `annotations` | — | コントローラー固有の設定 (書き換え先、本文サイズなど) のため、Ingress オブジェクトにそのまま統合します。 |

デプロイスペックは 1 列目の camelCase のフィールド名を使い、Compose の `x-cornus-ingress` 拡張は 2 列目の snake_case のキーを使います ([Compose サービスを公開する](#compose-サービスを公開する)を参照)。

### サーバー側既定値とドメインポリシー

運用者はフォールバックを設定できるため、ワークロードはすべて既定値のままイングレスを有効化できます (Helm の `ingress.*` 値は環境変数として設定されます)。空のままにすれば各ワークロードが自身のホスト名を指定する必要があり、何も自動公開されません。

| 環境変数 | Helm 値 | 意味 |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | ホスト自動導出の base wildcard ドメイン (例: `preview.example.com`)。 |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | 既定の `IngressClassName`。 |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | TLS イングレス用の既定 cert-manager cluster-issuer。 |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | true (かつドメイン設定済み) なら、解決したホスト名が `domain` の外にあるワークロードを拒否します。共有コントローラーがクライアントの指定だけで任意のホスト名を提供させられることを防ぎます。 |

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[Helm chart values](/ja/reference/helm-values)

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

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## 明示的なホスト名を設定する

一つ以上のホスト名を同じ Service にルーティングします。各ホスト名はそれぞれ独立したイングレス規則になります。

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- 特別なトークン `@` は、ルートドメイン (ベースドメインそのもので、`<name>.` の接頭辞なし) に使います: `hosts: ["@"]`。

## cert-manager で HTTPS を提供する

cert-manager の cluster-issuer に証明書を要求します。Cornus が issuer アノテーションを追加し、cert-manager がシークレットを用意します。

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # 空の場合は CORNUS_INGRESS_TLS_ISSUER にフォールバックする
```

- `tls:` ブロックはホストに HTTPS を要求します。プレーンな HTTP では省略してください。
- `secretName` は既存 TLS シークレットの名前です。空なら `<name>-tls` が既定で、`clusterIssuer` (またはサーバー既定) が設定されていれば cert-manager が作成します。既存のシークレットを自分で用意する場合は、`tls: { secretName: my-existing-tls }` を設定し、`clusterIssuer` を省略します。
- `clusterIssuer` は `cert-manager.io/cluster-issuer` annotation を設定します。空ならサーバー既定の `CORNUS_INGRESS_TLS_ISSUER` にフォールバックします。

**関連項目:** [セキュリティと認証](/ja/guides/security)

## 自前の証明書を使う

証明書の規則は、選択中の接続プロファイルに記述します。`pattern` は任意です。省略した場合、Cornus は証明書のすべての DNS SAN からセレクターを作成します。

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

パターンは厳密なホスト名か、`*.example.com` のような 1 ラベルのワイルドカードです。明示したパターンは証明書の SAN に含まれていなければなりません。厳密一致がワイルドカードより優先され、ワイルドカード同士では最長の接尾辞が優先されます。

`emulate` モードでは、ローカルのイングレスプロキシが提供する証明書を SNI が選択します。一致しないホスト名では通常の生成 CA へのフォールバックを使います。`native` モードでは、Cornus はデプロイ前に具体的なイングレスホストをすべて照合し、選択された証明書ごとにホストをまとめ、ワークロードの Deployment が所有する安定した `kubernetes.io/tls` Secret を作成して、Kubernetes Ingress に接続します。再適用すると Secret のデータをその場でローテーションし、不要になった管理対象 Secret を削除します。

native の管理対象証明書には、具体的な `ingress.hosts` を明示する必要があります。自動導出されるホスト名や `@` の頂点ドメイントークンは、仕様の中で展開してください。すべてのホストがいずれかの証明書規則に一致しなければなりません。証明書はクライアント側 conduit のリスナーではなく永続的な Kubernetes の状態であるため、これはデタッチした Compose やデプロイの操作でも機能します。

native の経路はデプロイ要求で秘密鍵の実体を送信します。そのため Cornus は、リモートのプレーン HTTP 越しの場合は要求のシリアライズ前に拒否します。HTTPS、SSH トンネルのプロファイル、または Kubernetes のポート転送のようなループバックのエンドポイントを使ってください。鍵がステータスや診断出力に現れることはありません。フィールドの一覧は[接続設定リファレンス](/ja/reference/connection-config)を参照してください。

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

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)

## Compose サービスを公開する

プロジェクトレベルまたはサービスごとに `x-cornus-ingress` を使います。プロジェクトレベルのブロックは**既定値**を提供するだけで、イングレスは有効にしません。イングレスはサービスごとに明示的に有効化したままです。`x-` 接頭辞により、ファイルは標準の Compose ツールに対して有効なままです。

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

ここでつまずきやすい点が 3 つあります。いずれも無言で失敗するか、デプロイ時に失敗します。

- **ポートを公開すること。** サービスには `ports:` の項目が必要です。イングレスは公開済みのコンテナポートの前段に配置されるためです。内部でしか待ち受けないサービスでも、それを記載しなければなりません (`ports: ["80"]`、またはホストポートをバインドしない長形式の `- target: 80`。後者は他のサービスとのホストポート衝突も避けられます)。
- **`port` はコンテナポートであり、公開ポートではありません。** これはコンテナ内でアプリが待ち受けているポート (例: `3000`、`8000`) であり、`80`/`443` ではありません。TLS とパブリックな HTTP(S) ポートは `tls: {}` が処理します。
- **キーは snake_case です。** `x-cornus-ingress` の中では `path_type`、`class_name`、`tls:` の下では `secret_name` / `cluster_issuer` と書きます。デプロイスペックの camelCase (`pathType`、`className`、`secretName`、`clusterIssuer`) ではありません。camelCase のキーは未知のフィールドとして無言で無視されます。値も大文字小文字を区別し、`path_type: Prefix` であって `prefix` ではありません。サーバー既定の issuer で HTTPS を要求するだけなら、素の `tls: {}` で十分です。

**関連項目:** [デプロイスペック](/ja/reference/deploy-spec)、[Compose、devcontainers、docker CLI](/ja/guides/compose-devcontainers-docker)

## conduit 経由で自分のマシンからイングレスに到達する

上記のパブリックな Ingress はワークロードに実際のホスト名を与えますが、開発マシンからそこへ到達するには、クラスターのイングレスコントローラーを指す DNS が依然として必要です。[SOCKS5 conduit](/ja/guides/networking) がその隙間を埋めます。ブラウザーのプロキシ設定 1 つで、ワークロードのイングレスホストがプロキシ経由で解決されます。`/etc/hosts` の編集も実際の DNS も不要です。これは**明示的に有効化**するもので、socks5 conduit (`--conduit socks5`) に相乗りし、次の 2 つのモードのいずれかで動作します。

- **native** — クラスターの*実際の*イングレスコントローラー Service への透過的なトンネルです。ブラウザーの TLS ClientHello (SNI) と `Host` ヘッダーはそのまま通過するため、実際のコントローラーが Host / パスのルーティングを行い、クラスター自身の証明書で TLS を終端します。Kubernetes 専用で、セッションにはクラスターへの直接アクセス (ポート転送 / kube-auth のプロファイル) が必要です。コントローラーの Service はサーバーが検出し、`GET /.cornus/v1/info` で通知します (`CORNUS_INGRESS_CONTROLLER=<namespace>/<service>[:http/https]` で上書きできます)。
- **emulate** — `Host` / パスに基づいて conduit 経由でワークロードのコンテナポートへ経路する、小さなクライアント側 HTTP(S) リバースプロキシです。TLS は、一致するユーザー提供の証明書か、生成したフォールバック証明書で終端します。**すべての**バックエンドで動作します (コントローラーを持たない `dockerhost` / `containerd` を含みます)。**TLS の信頼はそのまま使えます。** [mkcert](https://github.com/FiloSottile/mkcert) がインストールされていて `mkcert -install` を実行済みなら、エミュレートしたイングレスはリーフ証明書を mkcert のすでに信頼された ローカル CA で署名するため、ブラウザーと `curl` は**手作業なしで** `https://<host>/` を信頼します。そうでない場合は、一度だけ信頼すればよい永続的な自己署名 CA (`~/.local/share/cornus/ingress-ca.pem`) にフォールバックします (`--cacert` を渡すこともできます)。明示的な `--ingress-emulate-ca` / `--ingress-emulate-ca-key` は両者より優先されます。

実行ごとに有効化するか、プロファイルに固定します。

```sh
# per run
cornus compose up --conduit socks5 --ingress-conduit native
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate

# or pin it in the connection profile (see cornus config)
cornus config set-context prod --conduit-mode socks5 --ingress-conduit native
```

ブラウザーの SOCKS5 プロキシを conduit に向け (**リモート DNS** / socks5h を使います)、ワークロードのイングレスホスト (例: `https://web.example.com/`) を開きます。優先順位は `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > プロファイルで、`off` は無効化します。

`cornus setup` はサーバーを調べて既定値を選びます。コントローラーを検出できれば **native**、到達可能なコントローラーがなくイングレスドメインだけがある場合は **emulate**、それ以外は **off** を提案します。

補足が 2 点あります。native と emulate は同じ `x-cornus-ingress` の仕様に適用されます。実際のコントローラーが存在する場所では native が優先され、emulate はポータブルなフォールバックです。コントローラーの `annotations` / `className` / cert-manager のフィールドは Kubernetes 専用で、エミュレーションでは無視されます。

**関連項目:** [ネットワークと conduit](/ja/guides/networking)、[cornus setup](/ja/cli/setup)
