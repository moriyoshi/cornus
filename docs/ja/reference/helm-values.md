# Helm chart values

Cornus Helm chart (`deploy/helm/cornus`、OCI artifact としても publish) は、サーバーをクラスター内に `StatefulSet` + PVC + `Service` + RBAC としてデプロイし、[`kubernetes`](/ja/reference/deploy-backends) デプロイバックエンドが preset されています。この page は `values.yaml` のすべての値を document します。install walkthrough は [インストール](/ja/introduction/installation) を参照してください。

Chart version `0.3.0`、app version `0.1.0`。

## Installing

```sh
# From the OCI registry (recommended):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# From a checked-out chart, overriding values:
helm install cornus deploy/helm/cornus \
  --set storage='s3://my-bucket?region=us-east-1' \
  --set tls.enabled=true
```

値は `--set key=value` または `-f my-values.yaml` ファイルで上書きします。

## イメージ

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `image.repository` | `ghcr.io/moriyoshi/cornus` | サーバーイメージ。ローカルビルドまたは mirror したイメージに上書きできます。 |
| `image.tag` | `""` | イメージタグ。空の場合は chart `appVersion` が既定です。 |
| `image.pullPolicy` | `IfNotPresent` | 標準 Kubernetes イメージプルポリシー。 |

## サーバー

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `addr` | `":5000"` | コンテナ内の listen アドレス。 |
| `replicas` | `1` | サーバーレプリカ count。`1` は single-replica behavior を保ちます。`> 1` は multi-replica モードを有効化します (要件は [Multi-replica モード](#multi-replica-mode) を参照)。 |
| `deployBackend` | `kubernetes` | `/.cornus/v1/deploy` 経由で作成されるワークロードのバックエンド (`CORNUS_DEPLOY_BACKEND` を設定)。chart はクラスター内で動き、自分の名前空間にデプロイします。`dockerhost` は pod に Docker ソケットがマウントされている場合だけ設定してください。 |
| `resources` | `{}` | Pod resource requests/limits。そのまま描画されます。 |

## ストレージ and garbage collection

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `storage` | `""` | レジストリ永続化バックエンド (`CORNUS_STORAGE`): パス、`file://`、`mem://`、または `s3://bucket?region=...`。空の場合、CAS はデータディレクトリの PVC 上に残ります。`replicas > 1` の場合は `s3://` URL である必要があります。[ストレージバックエンド](/ja/reference/storage-backends) を参照してください。 |
| `persistence.size` | `20Gi` | レプリカごとの data-dir PVC size。 |
| `persistence.storageClassName` | unset | PVC ストレージ class (既定では commented out。クラスター既定が使われます)。 |
| `gc.interval` | `""` | `CORNUS_GC_INTERVAL`: Go duration (例: `24h`)。設定すると、各レプリカがこの周期でストレージ mark-and-sweep GC を実行します。空の場合、GC は `POST /.cornus/v1/gc` による必要に応じてのみ実行されます。 |
| `gc.lease` | `""` | `CORNUS_GC_LEASE`: cross-replica GC coordination の opt-in (`gc.interval` が必要)。`kube` は `coordination.k8s.io` Lease により tick ごとに単一 sweeper を elect します。`kube:<name>` または `kube:<namespace>/<name>` は Lease ID を上書きします。`replicas > 1` で `gc.interval` を安全にします。 |

## サービス and レジストリ exposure

`registry.exposure` は、クラスターノードがワークロードイメージをプルできるように公開する方法を選択します。サーバーは `GET /.cornus/v1/info` で node-reachable レジストリホストを通知し、chart は対応する topology を配線します。

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `service.type` | `""` | サービス型。空の場合は `registry.exposure` から導出します (`nodePort` では `NodePort`、それ以外は `ClusterIP`)。上書きする場合だけ設定してください。 |
| `service.port` | `5000` | サービスポート。 |
| `registry.exposure` | `nodePort` | サーバーが通知する topology: `nodePort`、`clusterIP`、`hostPort`、`hostNetwork`、または `ingress` (下表を参照)。 |
| `registry.nodePort` | `30500` | `nodePort` exposure 用の固定 NodePort (各ノードの containerd レジストリ設定を事前用意できるようにします)。空の場合は Kubernetes が割り当てます。 |
| `registry.hostPort` | `5000` | `hostPort` exposure でレジストリがバインドするノードポート。 |
| `registry.advertiseHost` | `""` | デプロイプル ref に bake されるレジストリホスト (`CORNUS_ADVERTISE_REGISTRY`) を上書きします。`clusterIP` / `hostPort` / `hostNetwork` / `ingress` では必須です。TLS レジストリでは `https://` を prefix します。 |
| `registry.nodeCIDR` | `""` | `nodePort` / `clusterIP` で、この CIDR 内のノードがレジストリポートに到達できる NetworkPolicy を emit します。default-deny posture では必須です。 |

### `registry.exposure` values

| 値 | ノードのプル方法 | Requires |
| --- | --- | --- |
| `nodePort` (既定) | 各ノードの `localhost:<nodePort>` | 追加不要。サービスから auto-advertised。 |
| `clusterIP` | サービス ClusterIP | `advertiseHost`; default-deny では `nodeCIDR` allow; ノードが ClusterIP を trust すること。 |
| `hostPort` | `<nodeIP>:<port>` (CNI portmap) | `advertiseHost`; `nodeSelector` で pod を pin。NetworkPolicy-immune。 |
| `hostNetwork` | ホスト netns リスナー | 特権 PodSecurity 名前空間; `advertiseHost`; pod の pin。NetworkPolicy-immune。 |
| `ingress` | イングレス host/VIP | `advertiseHost`; ノードがホストを解決し cert を trust すること (real DNS + TLS)。 |

## イングレス defaults

[イングレス](/ja/guides/ingress) に opt in するワークロード用のサーバー側フォールバックです (デプロイスペック `ingress:` / Compose `x-cornus-ingress:`)。すべてのフィールドを空の (既定) にすると、各ワークロードが自分のホストを指定する必要があり、何も auto-exposed されません。

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `ingress.domain` | `""` | `CORNUS_INGRESS_DOMAIN`: ホスト auto-derivation 用の base wildcard ドメイン (例: `preview.example.com`)。空の場合、ワークロードは自分のホストまたはドメインを設定する必要があります。 |
| `ingress.className` | `""` | `CORNUS_INGRESS_CLASS`: 既定 `IngressClassName`。空のではクラスター既定を使います。 |
| `ingress.tlsIssuer` | `""` | `CORNUS_INGRESS_TLS_ISSUER`: TLS-enabled イングレス用の既定 cert-manager cluster-issuer。空の場合、TLS を要求するワークロードは自分の secret/issuer を指定する必要があります。 |
| `ingress.enforceDomain` | `false` | `CORNUS_INGRESS_ENFORCE_DOMAIN`: true (かつ `domain` が設定済み) の場合、resolved ホストが `domain` の外に出るワークロードを拒否します。共有 controller に任意の hostname を提供させることを防ぎます。 |

## Privilege

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `privileged` | `true` | プロセス内ビルドエンジンは runc + overlayfs を必要とします。`privileged` が最も単純な posture です。hardened クラスターでは `false` にし、ルートレス prerequisite を用意してください。[Privilege posture](/ja/reference/deploy-backends) を参照してください。 |

## TLS

Opt-in HTTPS です。有効化すると、サーバーは mounted シークレット (`tls.crt` / `tls.key`、mTLS 用には加えて `ca.crt`) から提供し、ファイル change で cert を hot-reload します。

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `tls.enabled` | `false` | HTTPS で提供します。 |
| `tls.secretName` | `cornus-tls` | `/etc/cornus/tls` にマウントされるシークレット。`tls.certManager.enabled` の場合は cert-manager が生成します。それ以外では同じキーを持つ既存シークレットを提供してください。 |
| `tls.clientCA` | `false` | シークレットの `ca.crt` を使ってクライアント cert を検証します (mTLS)。verified cert の CommonName は呼び出し元 ID になります ([セキュリティと認証](/ja/guides/security) の `CORNUS_API_POLICY` を参照)。 |
| `tls.certManager.enabled` | `false` | `secretName` に書き込み auto-rotated される cert-manager `Certificate` を描画します。cert-manager と Issuer/ClusterIssuer が必要です。 |
| `tls.certManager.issuerRef.name` | `""` | Issuer/ClusterIssuer name。 |
| `tls.certManager.issuerRef.kind` | `ClusterIssuer` | `ClusterIssuer` または `Issuer`。 |
| `tls.certManager.dnsNames` | `[]` | cert の DNS name。空の場合はクラスター内サービス name が既定です。 |
| `tls.certManager.duration` | `2160h` | 証明書 lifetime (90d)。 |
| `tls.certManager.renewBefore` | `720h` | Renew-before window (30d)。cornus は新しい cert を hot-reload します。 |

## Auth (JWT)

サーバー API の opt-in JWT verification です (kube-auth turnkey パス)。値が設定されたものごとに対応する `CORNUS_JWT_*` env が描画されます。すべて空の場合は何も描画されません (他で設定されない限り auth は off のまま)。[セキュリティと認証](/ja/guides/security) を参照してください。

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `auth.jwt.jwksURL` | `""` | JWKS document の HTTPS URL (`CORNUS_JWT_JWKS_URL`)。例: クラスターの ServiceAccount OIDC JWKS。`jwksConfigMap` / `jwksSecret` とは mutually exclusive。 |
| `auth.jwt.jwksConfigMap` | `""` | マウントする JWKS document を保持する既存 ConfigMap の name (`CORNUS_JWT_JWKS_FILE`)。`jwksConfigMap` / `jwksSecret` のどちらか 1 つだけを設定してください。 |
| `auth.jwt.jwksSecret` | `""` | マウントする JWKS document を保持する既存シークレットの name。 |
| `auth.jwt.jwksKey` | `jwks.json` | JWKS JSON を保持する ConfigMap/Secret 内のキー。`/etc/cornus/jwks` に読み取り専用マウントされます。 |
| `auth.jwt.audience` | `""` | 必須の `aud` claim (`CORNUS_JWT_AUDIENCE`)。`cornus kube-auth` が発行するトークンは同じ audience を使う必要があります。 |
| `auth.jwt.issuer` | `""` | 任意の expected `iss` claim (`CORNUS_JWT_ISSUER`)。unset では issuer 検査をスキップします。 |

## Caretaker TLS

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `caretakerTlsSecret` | `""` | server-bound caretaker sidecar がサーバーへ接続するときに提示する material を持つ既存シークレット (`CORNUS_CARETAKER_TLS_SECRET`) の name。キーは `kubernetes.io/tls` convention に従います。`ca.crt` (system root に追加されます。private-CA `tls.enabled` cert と使います) と、任意で `tls.crt` / `tls.key` (mTLS クライアント pair、`tls.clientCA` 用)。空のでは何も描画しません。 |

## Tailscale Funnel sidecar

`tailscale` [トンネルバックエンド](/ja/guides/tunnels#バックエンド) 用の opt-in サイドカーです。認証キー Secret を使って tailnet に無人で参加する `tailscaled` コンテナと、`tailscale` CLI を cornus コンテナと共有するボリュームへコピーする initContainer を追加するので、カスタム cornus イメージは不要です。ユーザースペースネットワーキングモードで動作します (`NET_ADMIN` も TUN デバイスも不要)。手順の全体は [トンネルガイド](/ja/guides/tunnels) を参照してください。

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `tailscale.enabled` | `false` | サイドカーを有効化します。cornus コンテナに `CORNUS_TUNNEL_BACKEND=tailscale`、`CORNUS_TUNNEL_TAILSCALE_BIN`、`TS_SOCKET` を設定します。 |
| `tailscale.image.repository` / `tag` / `pullPolicy` | `ghcr.io/tailscale/tailscale` / `stable` / `IfNotPresent` | サイドカーおよび initContainer のイメージ。 |
| `tailscale.authKeySecret` | `""` | **有効化時は必須。** tailnet 認証キーを保持する既存の Secret の name。再利用可能な、できれば ephemeral タグ付きのキーを使ってください — サイドカーの状態ディレクトリは `emptyDir` で、pod 再起動をまたいで永続化されません。 |
| `tailscale.authKeySecretKey` | `authkey` | 認証キーを保持する Secret 内のキー。 |
| `tailscale.hostname` | `""` | `TS_HOSTNAME`: tailnet デバイス名。Funnel の URL が再起動をまたいで安定します。空の場合は release の fullname から導出されます。 |
| `tailscale.extraArgs` | `""` | サイドカーの無人 `tailscale up` に追加するフラグ (`TS_EXTRA_ARGS`)。例: `--accept-dns=false`。`--authkey` と `--hostname` は chart がすでに指定します。 |
| `tailscale.resources` | `{}` | サイドカーコンテナのリソース。 |

## RBAC and scheduling

| 値 | 既定 | 説明 |
| --- | --- | --- |
| `rbac.create` | `true` | クラスター内 kubernetes デプロイバックエンドのため、また `replicas > 1` の場合は kube-native hub ストア (HubEndpoint CR、Lease、CRD self-install) のための RBAC を付与します。Lease verb は `gc.lease` も cover します。 |
| `nodeSelector` | `{}` | 標準 pod `nodeSelector`。 |
| `tolerations` | `[]` | 標準 pod toleration。 |
| `affinity` | `{}` | Pod affinity。設定時はそのまま描画されます。空のかつ `replicas > 1` の場合は、レプリカをノード間に spread する既定 soft pod anti-affinity を描画します。これを設定するとその既定を置き換えます。 |

## Multi-replica モード

`replicas > 1` を設定すると、ワークロード間 [hub](/ja/guides/hub) は multi-replica モードに切り替わります。chart は `CORNUS_HUB_STORE=kube` を設定し、stable per-pod DNS 用の headless サービスを追加し、cross-replica 配送用に `CORNUS_HUB_FORWARD_URL` をそこへ向けます。要件と caveat は次の通りです。

- **`storage` は `s3://` URL でなければなりません** (描画 time に強制)。各レプリカは自分の PVC を持つため、PVC-backed CAS は 1 つのサービスの背後にあるレプリカ間で inconsistency を起こします。その場合 PVC はレプリカごとのビルドキャッシュだけを保持します。
- `StatefulSet` の `serviceName` は headless サービスに切り替わります。このフィールドは immutable です。既存 release を `1` と `> 1` レプリカの間で移動するには、先に `StatefulSet` を削除する必要があります (PVC は保持されます)。
- `tls.enabled` の場合、inter-replica 転送接続は `wss://` を使い、コンテナ trust ストアに照らして serving cert を検証します。そのため cert は per-pod name (`*.<fullname>-hub.<namespace>.svc`) を cover し、trusted root へ chain する必要があります。
- **Garbage collection:** `gc.interval` だけでは、共有 S3 CAS に対して各レプリカが uncoordinated sweep を実行します。`gc.interval` と一緒に `gc.lease: kube` を設定し、レプリカが Lease を通じて tick ごとに単一 sweeper を elect するようにしてください。

## 関連ページ

- [インストール](/ja/introduction/installation) - install walkthrough とサーバーの実行。
- [デプロイ backends](/ja/reference/deploy-backends) - この chart が preset する `kubernetes` バックエンド。
- [ストレージ backends](/ja/reference/storage-backends) - `storage` 値と object-store CAS。
- [サーバー環境変数](/ja/reference/server-env-vars) - chart が描画する `CORNUS_*` 環境変数。
- [トンネルガイド](/ja/guides/tunnels) - すべてのトンネルバックエンド (Tailscale サイドカーを含む) の段階的な設定手順。
